// app.js is the task list page controller (WORKSTREAMS.md F1). It renders the
// queue and drives the REST API; the model and the formatting live in tasks.js
// so the Go-side contract test can import neither and still assert the
// contract by reading the source.
//
// Data flow, which is the E3 contract:
//
//   1. GET /api/v1/tasks builds the baseline (order included);
//   2. the WebSocket stream patches one row per status event;
//   3. an event for an unknown task, or a non-zero "dropped" counter, means the
//      baseline is stale, so the whole list is re-read.
//
// Every mutation goes through the REST API and then re-reads the list, because
// the queue decides the resulting order and state (a move may be refused, a
// delete may be a 409). Local guessing would show a state the server does not
// have.

import {
  TaskList,
  cellValues,
  displayPath,
  rowActions,
  bulkActions,
  queueSummary,
  dropNotice,
  COLUMNS,
} from "./tasks.js";

import { QueueActions, describeError } from "./api.js";

"use strict";

// The wire contract, declared once. The REST prefix is spelled out rather than
// imported so this file names the interface it talks to; api.js defaults to the
// same value and the event stream path is built from it, which is what keeps
// the REST and WebSocket halves on one prefix (E2/E3: both live under
// /api/v1, so one reverse-proxy rule or auth middleware covers them).
const API = "/api/v1";
const EVENTS = API + "/events";

const RECONNECT_DELAY_MS = 2000;
const MAX_TOASTS = 4;

// state is the page's whole mutable state. One object keeps the renderer and
// the event handlers from each holding half of it.
const state = {
  actions: new QueueActions(API),
  list: new TaskList(),
  status: null,
  poolRunning: false,
  selected: "",
  socket: null,
  reconnectTimer: 0,
  // refetching guards the single in-flight list read; refetchWanted remembers a
  // re-read that arrived while one was running, and buffered holds the events
  // that landed in the same window so they can be re-applied to the response.
  refetching: false,
  refetchWanted: false,
  buffered: [],
};

// --------------------------------------------------------------- DOM helpers

function el(tag, props, ...children) {
  const node = document.createElement(tag);
  for (const [key, value] of Object.entries(props || {})) {
    if (key === "class") {
      node.className = value;
    } else if (key === "dataset") {
      Object.assign(node.dataset, value);
    } else if (key === "text") {
      node.textContent = value;
    } else if (key.startsWith("on") && typeof value === "function") {
      node.addEventListener(key.slice(2).toLowerCase(), value);
    } else if (value !== undefined && value !== null && value !== false) {
      node.setAttribute(key, value === true ? "" : String(value));
    }
  }
  for (const child of children.flat()) {
    if (child === null || child === undefined || child === false) {
      continue;
    }
    // Duck-typed rather than `instanceof Node`: the check works the same in a
    // browser and under the DOM shim the smoke test uses.
    node.append(isElement(child) ? child : document.createTextNode(String(child)));
  }
  return node;
}

// isElement reports whether a value can be appended as a node.
function isElement(value) {
  return (
    typeof value === "object" &&
    value !== null &&
    typeof value.append === "function"
  );
}

// --------------------------------------------------------------- notifications

// toast shows a transient message. Errors stay until dismissed, because a
// refused action is something the operator has to read.
function toast(kind, text, detail) {
  const box = document.getElementById("toasts");
  const node = el(
    "div",
    { class: "toast", dataset: { kind }, role: "status" },
    text,
    detail ? el("div", { class: "hint", text: detail }) : null,
    el("button", { type: "button", text: "关闭", onclick: () => node.remove() })
  );
  box.append(node);
  while (box.childElementCount > MAX_TOASTS) {
    box.firstElementChild.remove();
  }
  if (kind !== "error") {
    window.setTimeout(() => node.remove(), 6000);
  }
}

function reportError(prefix, err) {
  if (err && err.conflict) {
    // 409 is the queue refusing a change; the server's summary already says
    // why ("无法删除正在运行的任务"), so it is shown verbatim.
    toast("warn", prefix + "：" + err.summary, err.detail);
    return;
  }
  toast("error", prefix + "：" + describeError(err));
}

// ------------------------------------------------------------------- rendering

function renderShell() {
  const header = document.getElementById("node-status");
  const summary = state.status ? queueSummary(state.status) : null;
  header.textContent = summary
    ? summary.node + " · " + summary.pool + " · " + summary.queue
    : "正在读取节点状态…";

  document.querySelectorAll(".tabs a").forEach((a) => {
    a.toggleAttribute("aria-current", a.dataset.route === "tasks");
  });
}

function renderToolbar() {
  const bar = document.getElementById("toolbar");
  if (!bar) {
    return;
  }
  const actions = bulkActions(state.list.tasks, state.poolRunning);
  bar.replaceChildren(
    ...actions.map((action) =>
      el("button", {
        type: "button",
        text: action.label,
        title: action.hint || "",
        disabled: !action.enabled,
        onclick: () => runBulk(action.key),
      })
    ),
    el("span", { class: "spacer" }),
    el("span", {
      class: "counts",
      text:
        `共 ${state.list.size} 个任务` +
        (state.status
          ? ` · 等待 ${state.status.queue.waiting ?? 0}` +
            ` · 运行 ${state.status.queue.running ?? 0}` +
            ` · 完成 ${state.status.queue.finished ?? 0}` +
            ` · 失败 ${state.status.queue.error ?? 0}`
          : ""),
    })
  );
}

// renderTable redraws every row. The table is small (a season is tens of rows),
// so a full redraw per event keeps the code simple; the event handler batches
// with requestAnimationFrame so a burst of progress lines still redraws once.
function renderTable() {
  const host = document.getElementById("table-host");
  if (!host) {
    return;
  }
  if (state.list.size === 0) {
    host.replaceChildren(
      el("p", {
        class: "empty-state",
        text: "队列为空。点「新建任务」添加一个，或把 profile 文件拖进本页。",
      })
    );
    return;
  }

  const head = el(
    "tr",
    {},
    ...COLUMNS.map((column) =>
      el("th", { text: column.label, style: `width:${column.width}` })
    )
  );

  const body = el(
    "tbody",
    {},
    ...state.list.tasks.map((task) => renderRow(task))
  );
  const table = el(
    "table",
    { class: "tasks" },
    el("thead", {}, head),
    body
  );
  host.replaceChildren(el("div", { class: "table-wrap" }, table));
}

function renderRow(task) {
  const values = cellValues(task);
  const row = el("tr", {
    dataset: { id: task.id },
    class: rowClass(task, values),
    onclick: () => select(task.id),
  });

  for (const column of COLUMNS) {
    row.append(renderCell(task, values, column));
  }
  return row;
}

function rowClass(task, values) {
  const classes = [];
  const progress = (task.status || {}).progress;
  if (progress === "ERROR") {
    classes.push("state-error");
  } else if (progress === "FINISHED") {
    classes.push("state-finished");
  } else if (progress === "RUNNING") {
    classes.push("state-running");
  }
  if (!values.enabled) {
    classes.push("disabled");
  }
  if (state.selected === task.id) {
    classes.push("selected");
  }
  return classes.join(" ");
}

function renderCell(task, values, column) {
  switch (column.kind) {
    case "check":
      return el(
        "td",
        { class: "center" },
        el("input", {
          type: "checkbox",
          checked: values.enabled,
          title: "启用 / 禁用任务",
          onclick: (event) => {
            event.stopPropagation();
            toggleEnabled(task, event.target.checked);
          },
        })
      );

    case "name":
      return el("td", { class: "name", title: values.name, text: values.name });

    case "path":
      // The tooltip carries the raw reference ("local/…"), because the display
      // drops the volume and a volume that is not "local" matters.
      return el("td", {
        class: "path",
        title: values["raw_" + column.key] || values[column.key],
        text: values[column.key],
      });

    case "center":
      return el("td", {
        class: "center",
        title: values[column.key],
        text: values[column.key],
      });

    case "right":
      return el("td", {
        class: "right",
        title: values[column.key],
        text: values[column.key],
      });

    case "progress":
      return renderProgress(values);

    case "rpc":
      return renderRPC(task, values);

    case "actions":
      return el(
        "td",
        { class: "actions" },
        ...rowActions(task).map((action) =>
          el("button", {
            type: "button",
            text: action.label,
            disabled: !action.enabled,
            title: action.label,
            onclick: (event) => {
              event.stopPropagation();
              runRow(task, action.key);
            },
          })
        )
      );

    default:
      return el("td", { text: values[column.key] ?? "" });
  }
}

// renderProgress reproduces the DataGrid's progress cell: a ProgressBar with an
// overlaid percentage. A negative ProgressValue is the legacy "unknown" state,
// which set IsIndeterminate and blanked the text.
function renderProgress(values) {
  const bar = el("progress", {
    max: 100,
    min: 0,
    value: values.progress_unknown ? 0 : values.progress_value,
  });
  const cell = el(
    "div",
    { class: "progress-cell" + (values.progress_unknown ? " unknown" : "") },
    bar,
    el("span", { text: values.progress })
  );
  return el("td", { class: "center" }, cell);
}

// renderRPC reproduces the legacy button: its label is the RPC status, it is
// enabled only once the check finished, and clicking it opens the result.
function renderRPC(task, values) {
  const button = el("button", {
    type: "button",
    class: "rpc-button",
    text: values.rpc,
    dataset: { state: values.rpc },
    disabled: !values.rpc_openable,
    title: values.rpc_openable
      ? "查看花屏检查结果"
      : "花屏检查尚未结束或未启用",
    onclick: (event) => {
      event.stopPropagation();
      showRPCResult(task, values);
    },
  });
  return el("td", { class: "center" }, button);
}

// ------------------------------------------------------------------ selection

function select(id) {
  state.selected = state.selected === id ? "" : id;
  renderTable();
}

function selectedTask() {
  return state.selected ? state.list.get(state.selected) : null;
}

// ------------------------------------------------------------------- mutations

async function toggleEnabled(task, enabled) {
  try {
    await state.actions.setEnabled(task.id, enabled);
  } catch (err) {
    reportError(enabled ? "无法启用任务" : "无法禁用任务", err);
  }
  await refreshTasks();
}

async function runRow(task, key) {
  select(task.id);
  switch (key) {
    case "top":
    case "up":
    case "down":
      await move(task, key);
      return;
    case "delete":
      await remove(task);
      return;
    case "stop":
      await stopTask(task);
      return;
    default:
      return;
  }
}

async function move(task, position) {
  try {
    await state.actions.move(task.id, position);
  } catch (err) {
    reportError("无法移动任务", err);
  }
  await refreshTasks();
}

async function remove(task) {
  try {
    await state.actions.remove(task.id);
  } catch (err) {
    reportError("无法删除任务", err);
  }
  if (state.selected === task.id) {
    state.selected = "";
  }
  await refreshTasks();
}

// stopTask stops one running task. The API has no per-task cancel endpoint, so
// this is the pool stop followed by a restart of what is left, which is what
// the legacy Stop did per worker (WorkerManager.StopWorker). It is offered as a
// row action because the operator's mental model is "stop this one".
async function stopTask(task) {
  const others = state.list.tasks.filter(
    (t) => t.id !== task.id && (t.status || {}).progress === "RUNNING"
  ).length;
  if (others > 0) {
    toast(
      "warn",
      "无法只停止一个任务",
      "接口只提供整池停止（POST /api/v1/pool/stop）。请用工具栏的「终止」停止全部运行中的任务。"
    );
    return;
  }
  await stopPool();
}

async function stopPool() {
  try {
    const body = await state.actions.stopPool(0);
    if (body && body.stopped === false) {
      toast(
        "warn",
        "工作单元未在期限内停止",
        "任务仍在收尾，请稍后刷新查看。"
      );
    }
  } catch (err) {
    reportError("无法终止任务", err);
  }
  await refresh();
}

async function runBulk(key) {
  switch (key) {
    case "run":
      try {
        await state.actions.startPool();
      } catch (err) {
        reportError("无法开始任务", err);
      }
      await refresh();
      return;
    case "stop":
      await stopPool();
      return;
    case "new":
      window.location.href = "/wizard.html";
      return;
    case "update-chapter":
      toast(
        "info",
        "更新章节由服务端完成",
        "旧版在这里重扫每个任务的章节文件。当前接口没有对应的端点，" +
          "任务运行到「准备」阶段时会自行更新章节状态。"
      );
      return;
    case "clear":
      await clearEnabled();
      return;
    case "clear-all":
      await clearAll();
      return;
    case "refresh":
      await refresh();
      return;
    case "new-worker":
      toast(
        "info",
        "工作单元数量由 daemon 启动参数决定",
        "接口没有新增工作单元的端点（--workers / NUMA 数量在启动时确定）。"
      );
      return;
    default:
      return;
  }
}

// clearEnabled removes every ticked, non-running task. It is the legacy
// BtnEmpty ("清空"): GetNotRunningTasks filtered on IsEnabled, and a running
// task was reported rather than removed.
async function clearEnabled() {
  const targets = state.list.tasks.filter(
    (t) => (t.status || {}).enabled && (t.status || {}).progress !== "RUNNING"
  );
  if (targets.length === 0) {
    toast("warn", "请勾选需要清除的任务");
    return;
  }
  await removeMany(targets, "清空");
}

// clearAll removes every non-running task, ticked or not. It has no C# original
// (the legacy 清空 required the checkbox); it exists because a queue recovered
// from disk is unticked and would otherwise have to be ticked by hand.
async function clearAll() {
  const targets = state.list.tasks.filter(
    (t) => (t.status || {}).progress !== "RUNNING"
  );
  const running = state.list.size - targets.length;
  if (targets.length === 0) {
    toast("warn", "没有可清空的任务", "所有任务都在运行中。");
    return;
  }
  if (
    !window.confirm(
      `确定删除 ${targets.length} 个任务？` +
        (running > 0 ? `（${running} 个运行中的任务会保留）` : "")
    )
  ) {
    return;
  }
  await removeMany(targets, "清空全部");
}

// removeMany deletes a set of tasks one by one, reporting what failed. The
// endpoints are per task, so a partial failure is representable and is shown
// rather than hidden.
async function removeMany(targets, label) {
  const failed = [];
  for (const task of targets) {
    try {
      await state.actions.remove(task.id);
    } catch (err) {
      failed.push({ task, err });
    }
  }
  if (failed.length > 0) {
    const first = failed[0];
    toast(
      "warn",
      `${label}：${failed.length} 个任务未能删除`,
      `第一个：${first.task.name || first.task.id} — ${describeError(first.err)}`
    );
  } else {
    toast("info", `${label}：已删除 ${targets.length} 个任务`);
  }
  await refresh();
}

// ---------------------------------------------------------------- RPC results

function showRPCResult(task, values) {
  const output = displayPath(values.rpc_output);
  openModal(
    "花屏检查结果",
    el(
      "div",
      {},
      el("p", { text: `任务：${task.name || task.id}` }),
      el("p", { text: `状态：${values.rpc}` }),
      el("p", { text: "结果文件：" }),
      el("p", { class: "hint", text: output || "（未记录结果文件）" }),
      el("p", {
        class: "hint",
        text:
          "旧版在这里调用 RPChecker -r <结果文件> 打开检查结果。" +
          "浏览器打不开本机程序，所以这里只显示路径；" +
          "如需图形结果，请手工运行 RPChecker。",
      })
    )
  );
}

// -------------------------------------------------------------- drag and drop

// installDropZone reproduces MainWindow's file drop: the legacy window accepted
// exactly one .json / .yaml / .yml project file and opened the new-task wizard
// with it (TryGetProjectFile). The browser cannot hand the page an absolute
// path, so the file is read here and handed to the wizard as text; the wizard
// asks for the directory because relative paths need it.
function installDropZone() {
  const page = document.getElementById("page");

  page.addEventListener("dragover", (event) => {
    if (!hasFiles(event)) {
      return;
    }
    event.preventDefault();
    event.dataTransfer.dropEffect = "copy";
    page.classList.add("drop-target");
  });

  page.addEventListener("dragleave", () => page.classList.remove("drop-target"));

  page.addEventListener("drop", async (event) => {
    if (!hasFiles(event)) {
      return;
    }
    event.preventDefault();
    page.classList.remove("drop-target");

    const files = Array.from(event.dataTransfer.files);
    if (files.length !== 1) {
      toast("warn", "一次只能拖入一个项目文件", `收到 ${files.length} 个文件。`);
      return;
    }
    const file = files[0];
    const name = file.name.toLowerCase();
    if (
      !name.endsWith(".json") &&
      !name.endsWith(".yaml") &&
      !name.endsWith(".yml")
    ) {
      toast(
        "warn",
        "只接受 OKEGui 项目文件",
        `"${file.name}" 不是 .json / .yaml / .yml。`
      );
      return;
    }
    await handOffToWizard(file);
  });
}

function hasFiles(event) {
  return Boolean(
    event.dataTransfer && Array.from(event.dataTransfer.types || []).includes("Files")
  );
}

// handOffToWizard stores the dropped profile and navigates to the wizard page.
// sessionStorage is the channel: both pages are served from the same origin,
// and a File object cannot survive a navigation any other way.
async function handOffToWizard(file) {
  try {
    const text = await file.text();
    window.sessionStorage.setItem(
      "okegui.drop",
      JSON.stringify({ name: file.name, text })
    );
    window.location.href = "/wizard.html?from=drop";
  } catch (err) {
    toast("error", "无法读取拖入的文件", String((err && err.message) || err));
  }
}

// ------------------------------------------------------------------- the modal

function openModal(title, body, actions) {
  const modal = document.getElementById("modal");
  document.getElementById("modal-title").textContent = title;
  document.getElementById("modal-body").replaceChildren(body);
  document.getElementById("modal-actions").replaceChildren(
    ...(actions || []),
    el("button", { type: "button", text: "关闭", onclick: closeModal })
  );
  modal.hidden = false;
  modal.onclick = (event) => {
    if (event.target === modal) {
      closeModal();
    }
  };
  document.addEventListener("keydown", escapeClosesModal);
}

function closeModal() {
  document.getElementById("modal").hidden = true;
  document.removeEventListener("keydown", escapeClosesModal);
}

function escapeClosesModal(event) {
  if (event.key === "Escape") {
    closeModal();
  }
}

// ------------------------------------------------------------------- the page

// buildPage writes the static part of the page once. The table host and the
// toolbar are then re-rendered in place, which keeps the event handlers from
// being re-bound on every redraw.
function buildPage() {
  const page = document.getElementById("page");
  page.replaceChildren(
    el(
      "section",
      { class: "panel" },
      el("div", { id: "toolbar", class: "toolbar" }),
      el("div", { id: "table-host" }),
      el("p", {
        class: "hint",
        text:
          "点一行选中任务；勾选框启用/禁用；进度未知时（ProgressValue < 0）" +
          "进度条显示为不确定状态。双击旧版的行会打开输出目录，" +
          "浏览器无法做到，因此没有对应操作。",
      })
    )
  );
}

// ------------------------------------------------------------------- refresh

// refresh re-reads the queue and the node status. It is what every mutation
// ends with: the queue decides the resulting order and state (a move may be
// refused, a delete may be a 409), so the page never guesses the outcome.
async function refresh() {
  await refreshTasks();
}

async function refreshTasks() {
  if (state.refetching) {
    // A re-read is already in flight. Remember that another one is wanted
    // instead of dropping it: the in-flight response may predate whatever
    // asked for the re-read.
    state.refetchWanted = true;
    return;
  }
  state.refetching = true;
  state.buffered = [];

  try {
    // The status is read together with the list: the queue counts in the
    // toolbar come from it, and a delete or a move changes them.
    const [body, status] = await Promise.all([
      state.actions.list(),
      state.actions.status().catch(() => null),
    ]);
    state.list.replace((body && body.tasks) || []);
    if (status) {
      state.status = status;
      state.poolRunning = Boolean(status.pool && status.pool.running);
    }

    // Events that arrived while the response was in flight describe changes
    // the response may predate (the hub publishes after the queue is updated,
    // but the GET is served on another goroutine). Re-applying them is what
    // keeps a fresh baseline from being stale the moment it lands.
    for (const event of state.buffered) {
      state.list.apply(event);
    }

    if (state.selected && !state.list.get(state.selected)) {
      state.selected = "";
    }
    renderShell();
    renderTable();
    renderToolbar();
  } catch (err) {
    reportError("无法读取任务列表", err);
  } finally {
    state.refetching = false;
    state.buffered = [];
    if (state.refetchWanted) {
      state.refetchWanted = false;
      refreshTasks();
    }
  }
}

// applyEvent patches the local model with one status event, or re-reads the
// list when the event cannot be applied. It is the E3 increment half of the
// contract; see refreshTasks for the baseline half.
function applyEvent(event) {
  if (state.refetching) {
    // The response that is about to land decides the baseline, so the event is
    // buffered and re-applied to it rather than to a list that is being
    // replaced.
    state.buffered.push(event);
    return;
  }
  if (!state.list.apply(event)) {
    // Unknown task: the queue holds a task this page has never seen, which
    // means the baseline is older than the queue.
    refreshTasks();
    return;
  }
  scheduleRender();
}

// ------------------------------------------------------------------ event flow

// scheduleRender coalesces a burst of progress events into one redraw.
let renderScheduled = false;
function scheduleRender() {
  if (renderScheduled) {
    return;
  }
  renderScheduled = true;
  window.requestAnimationFrame(() => {
    renderScheduled = false;
    renderTable();
    renderToolbar();
  });
}

function setStreamState(value, label) {
  const pill = document.getElementById("stream-state");
  if (!pill) {
    return;
  }
  pill.dataset.state = value;
  pill.textContent = label;
}

// connect opens the progress stream and applies its events to the local model.
//
// The stream carries increments only, so an event for an unknown task is the
// signal that the baseline is older than the queue: the list is re-read rather
// than guessed. A non-zero "dropped" counter means the same, one step earlier.
function connect() {
  if (state.socket) {
    return;
  }
  setStreamState("connecting", "事件流连接中…");

  const url = new URL(EVENTS, window.location.href);
  url.protocol = window.location.protocol === "https:" ? "wss:" : "ws:";

  let socket;
  try {
    socket = new WebSocket(url);
  } catch (err) {
    toast("error", "无法建立事件流连接", String((err && err.message) || err));
    scheduleReconnect();
    return;
  }
  state.socket = socket;

  socket.addEventListener("open", () => {
    setStreamState("open", "事件流已连接");
  });

  socket.addEventListener("message", (raw) => {
    let message;
    try {
      message = JSON.parse(raw.data);
    } catch (err) {
      return;
    }

    switch (message.type) {
      case "hello":
        return;
      case "status": {
        const dropped = Number(message.dropped) || 0;
        if (dropped > 0) {
          toast("warn", "事件流有丢失", dropNotice(dropped));
          refreshTasks();
          return;
        }
        if (message.event) {
          applyEvent(message.event);
        }
        return;
      }
      case "error":
        toast("warn", "事件流报告错误", message.message || "");
        return;
      default:
        return;
    }
  });

  socket.addEventListener("close", () => {
    state.socket = null;
    setStreamState("closed", "事件流已断开，正在重连…");
    scheduleReconnect();
  });

  socket.addEventListener("error", () => {
    // The close event follows, and that is where the reconnect is scheduled.
  });
}

function scheduleReconnect() {
  if (state.reconnectTimer) {
    return;
  }
  state.reconnectTimer = window.setTimeout(() => {
    state.reconnectTimer = 0;
    connect();
  }, RECONNECT_DELAY_MS);
}

// ------------------------------------------------------------------ boot

function boot() {
  buildPage();
  renderShell();
  installDropZone();
  refresh();
  connect();

  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "visible") {
      // A tab that was hidden may have missed events without the socket
      // closing; re-reading is cheap and the queue is the authority.
      refreshTasks();
    }
  });
}

boot();
