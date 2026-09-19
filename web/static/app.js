// app.js is the placeholder front end. It lists the queue and follows the
// WebSocket progress stream, which is exactly what the wiring round needs to
// demonstrate that the daemon, the REST API and the event stream agree. The F
// workstream replaces this file; the API contract it talks to is frozen.

"use strict";

const API = "/api/v1";
const EVENTS = API + "/events";
const MAX_EVENT_LINES = 50;

// tasks holds the queue by id, so an event can patch one row instead of
// refetching the whole list.
let tasks = new Map();
let socket = null;

// get fetches one API path and returns the decoded JSON, or throws.
async function get(path) {
  const resp = await fetch(path, { headers: { Accept: "application/json" } });
  if (!resp.ok) {
    let detail = resp.statusText;
    try {
      const body = await resp.json();
      detail = body.error?.summary || detail;
    } catch (err) {
      // A non-JSON body is already reported through the status text.
    }
    throw new Error(detail);
  }
  return resp.json();
}

// post performs a bodyless POST, used by the two pool buttons.
async function post(path) {
  const resp = await fetch(path, { method: "POST" });
  if (!resp.ok) {
    throw new Error(resp.statusText);
  }
  return resp.json();
}

// renderStatus draws the node and pool summary.
function renderStatus(status) {
  const node = status.node || {};
  const pool = status.pool || {};
  const queue = status.queue || {};
  const tools = Object.keys(node.tools || {}).length;
  document.getElementById("status").textContent =
    `节点 ${node.node_id || "?"} (${node.os || "?"}/${node.arch || "?"}) · ` +
    `角色 ${node.role || "?"} · NUMA ${node.numa_nodes ?? "?"} · 工具 ${tools} 个 · ` +
    `工作单元 ${pool.workers ?? 0}（活跃 ${pool.active_workers ?? 0}，` +
    `${pool.running ? "运行中" : "已停止"}） · ` +
    `队列 ${queue.total ?? 0}（等待 ${queue.waiting ?? 0}，运行 ${queue.running ?? 0}，` +
    `完成 ${queue.finished ?? 0}，失败 ${queue.error ?? 0}）`;
}

// percent renders a progress value; a negative value means "unknown".
function percent(task) {
  const value = task.status?.progress_value ?? 0;
  return value < 0 ? "未知" : value.toFixed(2) + "%";
}

// renderTasks redraws the table from the local map.
function renderTasks() {
  const tbody = document.getElementById("task-rows");
  const rows = [];
  for (const task of tasks.values()) {
    const tr = document.createElement("tr");
    for (const text of [
      task.name || task.id,
      task.status?.input || "",
      task.status?.progress || "",
      task.status?.status || "",
      percent(task),
    ]) {
      const td = document.createElement("td");
      td.textContent = text;
      tr.appendChild(td);
    }
    rows.push(tr);
  }
  tbody.replaceChildren(...rows);
  document.getElementById("tasks-empty").hidden = rows.length > 0;
}

// refresh reloads the queue and the status.
async function refresh() {
  try {
    const [status, list] = await Promise.all([
      get(API + "/status"),
      get(API + "/tasks"),
    ]);
    renderStatus(status);
    tasks = new Map((list.tasks || []).map((task) => [task.id, task]));
    renderTasks();
  } catch (err) {
    document.getElementById("status").textContent = "读取失败：" + err.message;
  }
}

// appendEvent adds one line to the event log, capped so a long encode does not
// grow the DOM without bound.
function appendEvent(text) {
  const log = document.getElementById("events");
  const line = document.createElement("div");
  line.textContent = text;
  log.prepend(line);
  while (log.childElementCount > MAX_EVENT_LINES) {
    log.lastElementChild.remove();
  }
}

// connect opens the progress stream. The hub is the pool's event sink, so every
// status event the worker pool applies arrives here.
function connect() {
  if (socket) {
    return;
  }
  const url = new URL(EVENTS, window.location.href);
  url.protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
  socket = new WebSocket(url);

  socket.addEventListener("message", (raw) => {
    let msg;
    try {
      msg = JSON.parse(raw.data);
    } catch (err) {
      appendEvent("无法解析的消息：" + raw.data);
      return;
    }
    if (msg.type === "hello") {
      appendEvent("事件流已连接：" + (msg.message || ""));
      return;
    }
    if (msg.type !== "status" || !msg.event) {
      appendEvent("事件：" + JSON.stringify(msg));
      return;
    }
    const ev = msg.event;
    if (tasks.has(ev.task_id)) {
      const task = tasks.get(ev.task_id);
      task.status.progress = ev.progress;
      if (ev.step) {
        task.status.status = ev.step;
      }
      task.status.progress_value = ev.percent;
      renderTasks();
    }
    const dropped = msg.dropped ? `（丢弃 ${msg.dropped} 条）` : "";
    appendEvent(
      `${ev.task_id} ${ev.progress} ${ev.step || ""} ${ev.percent}%${dropped}`
    );
  });

  socket.addEventListener("close", () => {
    appendEvent("事件流已断开，2 秒后重连");
    socket = null;
    window.setTimeout(connect, 2000);
  });
}

document.getElementById("refresh").addEventListener("click", refresh);
document.getElementById("pool-start").addEventListener("click", async () => {
  try {
    await post(API + "/pool/start");
  } catch (err) {
    appendEvent("启动失败：" + err.message);
  }
  refresh();
});
document.getElementById("pool-stop").addEventListener("click", async () => {
  try {
    await post(API + "/pool/stop");
  } catch (err) {
    appendEvent("停止失败：" + err.message);
  }
  refresh();
});

refresh();
connect();
