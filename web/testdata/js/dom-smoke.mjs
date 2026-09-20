// dom-smoke.mjs drives app.js through a minimal DOM shim. It is the strongest
// check available without a browser: it exercises the real boot path (build the
// page, read the API, render the table, apply an event) and asserts the markup
// that comes out.
//
// The shim is deliberately small. It implements only what app.js uses — create
// element, append, replaceChildren, classList, dataset, attributes, textContent
// — which keeps the shim honest: if the page starts using a DOM feature the
// shim does not have, this test fails loudly instead of passing vacuously.
//
// Run with: node --no-warnings dom-smoke.mjs

import assert from "node:assert/strict";

let checks = 0;
function eq(actual, expected, label) {
  checks++;
  assert.deepEqual(actual, expected, label);
}

// --------------------------------------------------------------- the DOM shim

class FakeClassList {
  constructor(node) {
    this.node = node;
  }

  add(...names) {
    for (const name of names) {
      this.node._classes.add(name);
    }
  }

  remove(...names) {
    for (const name of names) {
      this.node._classes.delete(name);
    }
  }

  contains(name) {
    return this.node._classes.has(name);
  }
}

class FakeNode {
  constructor(tag) {
    this.tagName = String(tag).toUpperCase();
    this.children = [];
    this.attributes = new Map();
    this.dataset = {};
    this.listeners = new Map();
    this.parent = null;
    this._classes = new Set();
    this._text = "";
    this.hidden = false;
    this.disabled = false;
    this.checked = false;
    this.value = "";
    this.type = "";
    this.classList = new FakeClassList(this);
  }

  get className() {
    return Array.from(this._classes).join(" ");
  }

  set className(value) {
    this._classes = new Set(String(value).split(/\s+/).filter(Boolean));
  }

  get id() {
    return this.attributes.get("id") || "";
  }

  set id(value) {
    this.attributes.set("id", String(value));
    registry.set(String(value), this);
  }

  get textContent() {
    if (this.children.length === 0) {
      return this._text;
    }
    return this.children.map((child) => child.textContent).join("");
  }

  set textContent(value) {
    this.children = [];
    this._text = value === null || value === undefined ? "" : String(value);
  }

  get firstElementChild() {
    return this.children[0] || null;
  }

  get childElementCount() {
    return this.children.length;
  }

  append(...nodes) {
    for (const node of nodes) {
      if (node === null || node === undefined) {
        continue;
      }
      const child = typeof node === "string" ? new FakeText(node) : node;
      child.parent = this;
      this.children.push(child);
    }
  }

  replaceChildren(...nodes) {
    this.children = [];
    this._text = "";
    this.append(...nodes);
  }

  remove() {
    if (!this.parent) {
      return;
    }
    const at = this.parent.children.indexOf(this);
    if (at >= 0) {
      this.parent.children.splice(at, 1);
    }
    this.parent = null;
  }

  setAttribute(name, value) {
    this.attributes.set(name, String(value));
    if (name === "id") {
      registry.set(String(value), this);
    }
    if (name === "class") {
      this.className = value;
    }
    if (name === "disabled") {
      this.disabled = true;
    }
  }

  getAttribute(name) {
    return this.attributes.has(name) ? this.attributes.get(name) : null;
  }

  toggleAttribute(name, force) {
    const on = force === undefined ? !this.attributes.has(name) : Boolean(force);
    if (on) {
      this.attributes.set(name, "");
    } else {
      this.attributes.delete(name);
    }
  }

  addEventListener(type, handler) {
    if (!this.listeners.has(type)) {
      this.listeners.set(type, []);
    }
    this.listeners.get(type).push(handler);
  }

  removeEventListener(type, handler) {
    const list = this.listeners.get(type) || [];
    const at = list.indexOf(handler);
    if (at >= 0) {
      list.splice(at, 1);
    }
  }

  // dispatch runs the listeners a test wants to fire.
  dispatch(type, event = {}) {
    for (const handler of this.listeners.get(type) || []) {
      handler({ target: this, preventDefault() {}, stopPropagation() {}, ...event });
    }
  }

  // queryAll walks the tree, for the tests that look for a rendered node.
  queryAll(predicate, out = []) {
    for (const child of this.children) {
      if (predicate(child)) {
        out.push(child);
      }
      child.queryAll(predicate, out);
    }
    return out;
  }

  find(tag) {
    return this.queryAll((node) => node.tagName === String(tag).toUpperCase());
  }
}

class FakeText {
  constructor(text) {
    this.tagName = "#text";
    this.children = [];
    this._text = String(text);
    this.parent = null;
  }

  get textContent() {
    return this._text;
  }
}

const registry = new Map();

const document = {
  createElement: (tag) => new FakeNode(tag),
  createTextNode: (text) => new FakeText(text),
  getElementById: (id) => registry.get(id) || null,
  querySelectorAll: () => [],
  addEventListener() {},
  removeEventListener() {},
  visibilityState: "visible",
  hidden: false,
};

// The shell the page expects, built by hand so the ids the controller looks up
// exist. It mirrors web/index.html.
function buildShell() {
  const body = new FakeNode("body");
  const header = new FakeNode("header");
  const pill = new FakeNode("span");
  pill.id = "stream-state";
  const nodeStatus = new FakeNode("p");
  nodeStatus.id = "node-status";
  header.append(pill, nodeStatus);
  const page = new FakeNode("main");
  page.id = "page";
  const toasts = new FakeNode("div");
  toasts.id = "toasts";
  const modal = new FakeNode("div");
  modal.id = "modal";
  const modalTitle = new FakeNode("h2");
  modalTitle.id = "modal-title";
  const modalBody = new FakeNode("div");
  modalBody.id = "modal-body";
  const modalActions = new FakeNode("div");
  modalActions.id = "modal-actions";
  modal.append(modalTitle, modalBody, modalActions);
  body.append(header, page, toasts, modal);
  return body;
}

const body = buildShell();

// ------------------------------------------------------------ the fake server

// The fixtures are real API payloads: the task list response and a status
// envelope as the hub sends them.
const tasks = [
  {
    id: "11111111-2222-4333-8444-555555555555",
    name: "ep01",
    output: "local/out/ep01.mkv",
    status: {
      enabled: true,
      progress: "RUNNING",
      status: "压制中",
      progress_value: 42.5,
      speed: "12.34 fps",
      bit_rate: "1234.56 kb/s",
      time_remain_seconds: 3661,
      task_type: 1,
      worker_name: "工作单元-1",
      chapter_status: "Maybe",
      rpc_status: "等待中",
      input: "local/work/ep01.m2ts",
    },
  },
  {
    id: "99999999-8888-4777-8666-555555555555",
    name: "ep02",
    output: "",
    status: {
      enabled: false,
      progress: "WAITING",
      status: "等待中",
      progress_value: 0,
      speed: "0.0 fps",
      chapter_status: "No",
      rpc_status: "等待中",
      input: "local/work/ep02.m2ts",
    },
  },
];

const statusPayload = {
  node: { node_id: "host", os: "windows", arch: "amd64", role: "standalone", numa_nodes: 1, cpus: 16, tools: {} },
  pool: { running: true, workers: 1, active_workers: 1 },
  queue: { total: 2, waiting: 1, running: 1, finished: 0, error: 0, enabled: 1 },
};

const calls = [];
async function fetchStub(url, options = {}) {
  calls.push({ url, method: options.method || "GET" });
  if (url.endsWith("/tasks") && (options.method || "GET") === "GET") {
    return jsonResponse(200, { tasks, count: tasks.length });
  }
  if (url.endsWith("/status")) {
    return jsonResponse(200, statusPayload);
  }
  return jsonResponse(200, {});
}

function jsonResponse(status, payload) {
  return {
    ok: status >= 200 && status < 300,
    status,
    async text() {
      return JSON.stringify(payload);
    },
  };
}

// The WebSocket stub records the instance so a test can push an envelope into
// the page's message handler.
let socket = null;
class FakeWebSocket {
  constructor(url) {
    this.url = String(url);
    this.listeners = new Map();
    socket = this;
  }

  addEventListener(type, handler) {
    if (!this.listeners.has(type)) {
      this.listeners.set(type, []);
    }
    this.listeners.get(type).push(handler);
  }

  emit(type, event) {
    for (const handler of this.listeners.get(type) || []) {
      handler(event);
    }
  }

  close() {}
}

// -------------------------------------------------------------- the page boot

globalThis.document = document;
globalThis.window = {
  location: { href: "http://127.0.0.1:8090/" },
  setTimeout: () => 0,
  clearTimeout: () => {},
  requestAnimationFrame: (fn) => {
    fn();
    return 0;
  },
  confirm: () => true,
  sessionStorage: { setItem() {}, getItem: () => null },
};
globalThis.fetch = fetchStub;
globalThis.WebSocket = FakeWebSocket;
globalThis.requestAnimationFrame = globalThis.window.requestAnimationFrame;
globalThis.URL = URL;

await import(new URL("../../static/app.js", import.meta.url));

// boot() kicks off an async refresh; give the microtask queue a turn.
await new Promise((resolve) => setImmediate(resolve));

// --- the shell was built -----------------------------------------------------
const page = registry.get("page");
eq(page.children.length, 1, "the page holds one section");

const toolbar = registry.get("toolbar");
eq(toolbar !== null, true, "the toolbar was built");
const toolbarLabels = toolbar.children
  .filter((node) => node.tagName === "BUTTON")
  .map((node) => node.textContent);
eq(toolbarLabels.includes("运行"), true, "the toolbar has 运行");
eq(toolbarLabels.includes("终止"), true, "the toolbar has 终止");
eq(toolbarLabels.includes("新建任务"), true, "the toolbar has 新建任务");
eq(toolbarLabels.includes("刷新"), true, "the toolbar has 刷新");

// --- the table rendered ------------------------------------------------------
const host = registry.get("table-host");
const table = host.find("table")[0];
eq(table !== undefined, true, "the table was rendered");

const headers = table
  .find("th")
  .map((node) => node.textContent);
eq(
  headers,
  ["", "任务名称", "输入文件", "章节", "输出文件", "状态", "进度", "速度", "码率", "剩余时间", "任务类型", "工作单元", "花屏检查", "操作"],
  "the DataGrid header row"
);

const rows = table.find("tbody")[0].children;
eq(rows.length, 2, "one row per task");

const firstCells = rows[0].children.map((cell) => cell.textContent);
eq(firstCells[1], "ep01", "the name column");
eq(firstCells[2], "/work/ep01.m2ts", "the input column");
eq(firstCells[3], "Maybe", "the chapter column");
eq(firstCells[4], "/out/ep01.mkv", "the output column");
eq(firstCells[5], "压制中", "the status column");
eq(firstCells[6], "42.50%", "the progress column");
eq(firstCells[7], "12.34 fps", "the speed column");
eq(firstCells[8], "1234.56 kb/s", "the bit rate column");
eq(firstCells[9], "1:01:01", "the ETA column");
eq(firstCells[10], "ReEncode", "the task type column");
eq(firstCells[11], "工作单元-1", "the worker column");
eq(firstCells[12], "等待中", "the RPC column");

const secondCells = rows[1].children.map((cell) => cell.textContent);
eq(secondCells[1], "ep02", "the second row name");
eq(secondCells[6], "0.00%", "the second row progress");

// --- the toolbar reflects the queue -----------------------------------------
// 运行 is disabled because the pool is already running (BtnRun required
// !wm.IsRunning), which is the rule bulkActions implements.
const runButton = toolbar.children.find((node) => node.textContent === "运行");
eq(runButton.disabled, true, "运行 is disabled while the pool is running");
const stopButton = toolbar.children.find((node) => node.textContent === "终止");
eq(stopButton.disabled, false, "终止 is enabled with a running task");

// --- the node summary --------------------------------------------------------
eq(
  registry.get("node-status").textContent.includes("节点 host"),
  true,
  "the header names the node"
);

// --- the event patch ---------------------------------------------------------
eq(socket !== null, true, "the page opened a WebSocket");
eq(socket.url.startsWith("ws://"), true, "the stream URL uses ws://");

socket.emit("message", {
  data: JSON.stringify({
    type: "status",
    event: {
      task_id: "11111111-2222-4333-8444-555555555555",
      progress: "FINISHED",
      step: "完成",
      percent: 100,
    },
  }),
});

// The redraw replaces the table, so the row is looked up again rather than
// through the stale reference.
const redrawn = host.find("table")[0];
const patched = redrawn
  .find("tbody")[0]
  .children[0].children.map((cell) => cell.textContent);
eq(patched[5], "完成", "the event updated the status column");
eq(patched[6], "100.00%", "the event updated the progress column");

// --- the row actions ---------------------------------------------------------
const actionButtons = redrawn
  .find("tbody")[0]
  .children[0].children[13].children.map((node) => node.textContent);
eq(actionButtons, ["置顶", "上移", "下移", "停止", "删除"], "the row action set");

// --- a mutation goes through the API ----------------------------------------
const before = calls.length;
redrawn.find("tbody")[0].children[1].children[0].children[0].checked = true;
redrawn.find("tbody")[0].children[1].children[0].children[0].dispatch("click");
await new Promise((resolve) => setImmediate(resolve));
const patch = calls.slice(before).find((call) => call.method === "PATCH");
eq(patch !== undefined, true, "the checkbox sent a PATCH");
eq(patch.url, "/api/v1/tasks/99999999-8888-4777-8666-555555555555", "the PATCH targets the row");

// --- an unknown task forces a re-fetch --------------------------------------
// The E3 contract: the stream carries increments, so an event for a task the
// baseline does not know means the baseline is stale.
const listCallsBefore = calls.filter((call) => call.url.endsWith("/tasks") && call.method === "GET").length;
socket.emit("message", {
  data: JSON.stringify({ type: "status", event: { task_id: "new-task", progress: "WAITING" } }),
});
await new Promise((resolve) => setImmediate(resolve));
const listCallsAfter = calls.filter((call) => call.url.endsWith("/tasks") && call.method === "GET").length;
eq(listCallsAfter > listCallsBefore, true, "an unknown task re-reads the list");

// --- a dropped event forces a re-fetch --------------------------------------
const listCallsBeforeDrop = listCallsAfter;
socket.emit("message", {
  data: JSON.stringify({
    type: "status",
    dropped: 3,
    event: { task_id: "11111111-2222-4333-8444-555555555555", progress: "RUNNING", percent: 1 },
  }),
});
await new Promise((resolve) => setImmediate(resolve));
const listCallsAfterDrop = calls.filter((call) => call.url.endsWith("/tasks") && call.method === "GET").length;
eq(listCallsAfterDrop > listCallsBeforeDrop, true, "a dropped event re-reads the list");

// The gap is reported to the operator rather than silently swallowed.
const toasts = registry.get("toasts");
eq(toasts.childElementCount > 0, true, "a dropped event raises a notice");

console.log(`app.js: ${checks} checks passed`);
