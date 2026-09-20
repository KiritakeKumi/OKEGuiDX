// tasks.js is the pure half of the task list page (WORKSTREAMS.md F1): the
// queue model, the column list and every display-string port. It is a plain ES
// module with no DOM and no network access, which keeps it importable by the
// node smoke test (web/testdata/js) as well as by the browser.
//
// The three pieces of the page are split by what they touch:
//
//   - tasks.js (this file): the model and the formatting, no I/O;
//   - api.js: the REST client, no DOM;
//   - app.js: the DOM, the event stream and the user's actions.
//
// The formatting helpers are ports of the C# code that produced the strings, so
// the browser shows what the WPF window showed: "0.00%" progress, the
// "大于一周" ETA, the Chinese RPC states, and the TaskType names.

"use strict";

// ---------------------------------------------------------------- formatting

// TaskProgress enum values as they arrive on the wire (internal/model/status.go
// MarshalText). WAITING is the zero value and the fallback.
const PROGRESS = {
  WAITING: "WAITING",
  RUNNING: "RUNNING",
  ERROR: "ERROR",
  FINISHED: "FINISHED",
};

// ChapterStatus values as they arrive on the wire (internal/model/status.go).
// The legacy DataGrid bound the enum name directly, so these are the strings
// the operator saw.
const CHAPTER_TEXT = {
  No: "No",
  Yes: "Yes",
  Added: "Added",
  Maybe: "Maybe",
  MKV: "MKV",
  Warn: "Warn",
};

// RPCStatus values are already the Chinese display text (model.RPCStatus
// MarshalText), because the legacy enum members were Chinese. They are listed
// here so an unknown value can be reported rather than shown raw.
const RPC_KNOWN = new Set(["等待中", "跳过", "错误", "未通过", "通过"]);

// TaskType values (model.TaskType.String): the legacy enum was Normal /
// ReEncode and the DataGrid printed it as-is.
//
// NOTE: model.TaskType is the one status enum without MarshalText, so it
// arrives as a NUMBER (0 = Normal, 1 = ReEncode) while TaskProgress,
// ChapterStatus and RPCStatus arrive as strings. The numeric values are
// persisted (internal/model/status.go "do not reorder"), so the numbers are the
// wire form and the names are only used for display. taskTypeText accepts both
// so a future MarshalText would not break the page.
const TASK_TYPE = { Normal: "Normal", ReEncode: "ReEncode" };

// statusText maps a TaskProgress onto a display label. The legacy UI showed
// CurrentStatus instead (a free-form step name), which is why this is only used
// when a task has no step text yet.
export function statusText(progress) {
  switch (progress) {
    case PROGRESS.RUNNING:
      return "运行中";
    case PROGRESS.ERROR:
      return "错误";
    case PROGRESS.FINISHED:
      return "完成";
    default:
      return "等待中";
  }
}

// statusClass is the row's severity class, which is what colours the state
// column. It is a UI concern, so it lives here rather than in the model.
export function statusClass(progress) {
  switch (progress) {
    case PROGRESS.RUNNING:
      return "running";
    case PROGRESS.ERROR:
      return "error";
    case PROGRESS.FINISHED:
      return "finished";
    default:
      return "waiting";
  }
}

// formatPercent renders TaskStatus.ProgressValue the way the C# property did
// (Task/TaskStatus.cs:126): a negative value means "unknown" and renders as an
// empty string with the indeterminate flag set; anything else is "0.00%".
//
// A missing field is treated as zero, which is what the queue does when it
// stores a task (TaskManager.AddTask sets ProgressValue = 0). A non-finite
// value cannot come from JSON; it can only come from a malformed local patch,
// so it is reported as unknown rather than rendered as "NaN%".
export function formatPercent(value) {
  const percent = value === undefined || value === null ? 0 : Number(value);
  if (!Number.isFinite(percent) || percent < 0) {
    return { text: "", unknown: true };
  }
  return { text: formatFixed2(percent) + "%", unknown: false };
}

// formatFixed2 reproduces .NET's "0.00" format the way the engine's own
// formatFixed2 does (internal/engine/status.go): round the shortest
// round-trippable decimal representation half away from zero at two digits.
//
// Naive toFixed is not the same function. 2.675 is stored as
// 2.67499999999999982..., so toFixed rounds the binary value down to "2.67",
// while .NET first renders the shortest decimal that round-trips ("2.675") and
// rounds that up to "2.68". The difference is visible in the progress column,
// so the algorithm is ported rather than approximated.
export function formatFixed2(value) {
  if (!Number.isFinite(value)) {
    return String(value);
  }
  const negative = value < 0;
  const text = String(Math.abs(value));

  // String() switches to exponential notation below 1e-6, which a progress
  // value never reaches; fall back to toFixed so the function stays total.
  if (text.includes("e") || text.includes("E")) {
    return (negative ? "-" : "") + Math.abs(value).toFixed(2);
  }

  const dot = text.indexOf(".");
  const intPart = dot < 0 ? text : text.slice(0, dot);
  const frac = dot < 0 ? "" : text.slice(dot + 1);

  if (frac.length <= 2) {
    return (
      (negative ? "-" : "") + intPart + "." + frac + "0".repeat(2 - frac.length)
    );
  }

  const digits = (intPart + frac.slice(0, 2)).split("");
  if (frac[2] >= "5") {
    let i = digits.length - 1;
    while (i >= 0 && digits[i] === "9") {
      digits[i] = "0";
      i--;
    }
    if (i < 0) {
      digits.unshift("1");
    } else {
      digits[i] = String(Number(digits[i]) + 1);
    }
  }
  let cut = intPart.length;
  if (digits.length > intPart.length + 2) {
    cut++;
  }
  return (
    (negative ? "-" : "") +
    digits.slice(0, cut).join("") +
    "." +
    digits.slice(cut).join("")
  );
}

// formatTimeRemain renders TaskStatus.TimeRemainStr (Task/TaskStatus.cs:197):
// whole hours, then ":mm:ss", and "大于一周" beyond a week. Seconds are the
// caller's unit because that is what model.TaskStatus persists.
export function formatTimeRemain(seconds) {
  const value = Number(seconds);
  if (!Number.isFinite(value)) {
    return "";
  }
  if (value > 7 * 24 * 3600) {
    return "大于一周";
  }
  const negative = value < 0;
  const total = Math.trunc(Math.abs(value));
  const hours = Math.trunc(total / 3600);
  const minutes = Math.trunc((total % 3600) / 60);
  const secs = total % 60;
  return (
    (negative ? "-" : "") +
    hours +
    ":" +
    pad2(minutes) +
    ":" +
    pad2(secs)
  );
}

function pad2(n) {
  return n < 10 ? "0" + n : String(n);
}

// formatSize renders a byte count for the input-size column. The engine
// preformats the deliverable's size into BitRate, so this is only used where no
// engine-side string exists.
export function formatSize(bytes) {
  const value = Number(bytes);
  if (!Number.isFinite(value) || value <= 0) {
    return "";
  }
  const units = ["B", "KB", "MB", "GB", "TB"];
  let index = 0;
  let scaled = value;
  while (scaled >= 1024 && index < units.length - 1) {
    scaled /= 1024;
    index++;
  }
  return scaled.toFixed(2) + " " + units[index];
}

// formatFrames renders the frame counters a video encoder reports.
export function formatFrames(done, total) {
  const d = Number(done);
  const t = Number(total);
  if (!Number.isFinite(d) || !Number.isFinite(t) || t <= 0) {
    return "";
  }
  return Math.trunc(d) + " / " + Math.trunc(t);
}

// taskTypeText maps the TaskType enum value onto its legacy name.
//
// The wire form is a number because model.TaskType has no MarshalText (see the
// TASK_TYPE note); the string form is accepted too, so the page keeps working if
// that is ever added.
export function taskTypeText(value) {
  if (value === TASK_TYPE.ReEncode || value === 1 || value === "1") {
    return "ReEncode";
  }
  if (typeof value === "string" && value !== "" && value !== TASK_TYPE.Normal) {
    // An unrecognised name is shown as-is rather than blanked, so a newer engine
    // does not silently lose information in an older page.
    return value;
  }
  return "Normal";
}

// chapterText maps a ChapterStatus onto the text the DataGrid bound. An unknown
// value is shown as-is rather than blanked, so a newer engine does not silently
// lose information in an older page.
export function chapterText(value) {
  if (typeof value !== "string" || value === "") {
    return CHAPTER_TEXT.No;
  }
  return Object.prototype.hasOwnProperty.call(CHAPTER_TEXT, value)
    ? CHAPTER_TEXT[value]
    : value;
}

// rpcText returns the RPC button's label and whether it can open a result. The
// legacy button was enabled only for 未通过 and 通过 (TaskStatus.RpcButtonEnabled),
// and its Tag held the result path.
//
// An unrecognised value is shown as-is rather than blanked, so a newer engine
// does not silently lose information in an older page; it is just not openable.
export function rpcText(value) {
  const text = typeof value === "string" && value !== "" ? value : "等待中";
  return {
    text,
    openable: text === "未通过" || text === "通过",
    known: RPC_KNOWN.has(text),
  };
}

// -------------------------------------------------------------- display paths

// FileRef arrives as the single string "volume/rel" (model.FileRef MarshalText),
// e.g. "local/WORKS/ep01/00000.m2ts". The leading volume is the queue's logical
// volume, not part of the path, so the display drops it: on the standalone
// volume the root is the filesystem root and the remainder is what the operator
// typed, minus the Windows drive letter, which FileRef does not carry (the
// queue stores paths relative to a volume root precisely so that a path stays
// valid when the root changes).
//
// The raw reference is what the API is spoken to with; it is kept in the cell's
// title attribute so the volume is still visible on hover.
export function displayPath(ref) {
  if (typeof ref !== "string" || ref === "") {
    return "";
  }
  const slash = ref.indexOf("/");
  if (slash < 0) {
    return ref;
  }
  const volume = ref.slice(0, slash);
  const rel = ref.slice(slash + 1);
  // An unset reference serializes as "local/" (Rel is "/" after normalization),
  // which is not a path: rendering it as "/" would look like a real root.
  if (rel === "" || rel === "/") {
    return "";
  }
  if (volume === "local") {
    return rel.startsWith("/") ? rel : "/" + rel;
  }
  return volume + ":" + (rel.startsWith("/") ? rel : "/" + rel);
}

// rawPath is the reference exactly as the API sent it, for a tooltip.
export function rawPath(ref) {
  return typeof ref === "string" ? ref : "";
}

// baseName returns the file name of a reference, for the compact columns.
export function baseName(ref) {
  const path = displayPath(ref);
  const at = Math.max(path.lastIndexOf("/"), path.lastIndexOf("\\"));
  return at < 0 ? path : path.slice(at + 1);
}

// ------------------------------------------------------------------- the model

// TaskList is the queue model: an ordered array plus an index, so the event
// stream patches one row instead of re-rendering the table from scratch.
//
// Order matters: the queue is a priority list and the legacy Move buttons
// reordered it, so the array is kept in server order and a move triggers a
// re-fetch rather than a local guess.
export class TaskList {
  constructor() {
    this.tasks = [];
    this.byId = new Map();
  }

  // replace installs a fresh baseline (the response of GET /api/v1/tasks).
  replace(tasks) {
    this.tasks = Array.isArray(tasks) ? tasks.slice() : [];
    this.byId = new Map();
    for (const task of this.tasks) {
      this.byId.set(task.id, task);
    }
  }

  // apply patches one task with a status event. It reports whether the event
  // matched a known task: an event for an unknown id means the baseline is
  // older than the queue, which is the caller's cue to re-fetch.
  apply(event) {
    const task = this.byId.get(event.task_id);
    if (!task) {
      return false;
    }
    patchStatus(task, event);
    return true;
  }

  get(id) {
    return this.byId.get(id);
  }

  get size() {
    return this.tasks.length;
  }
}

// patchStatus copies a StatusEvent onto a task's status, mirroring the mapping
// the engine's worker pool applies (internal/engine/worker.go applyStatus):
//
//   - the terminal event of a cancelled or failed task carries no percent, so
//     an existing value is kept instead of falling back to 0.00%;
//   - Speed, BitRate and the ETA are sticky: the event stream omits them on
//     most events and the legacy TaskStatus kept the last value it was given.
export function patchStatus(task, event) {
  const status = task.status || (task.status = {});
  if (event.progress) {
    status.progress = event.progress;
  }
  if (event.error && event.error.summary) {
    status.status = event.error.summary;
  } else if (event.step) {
    status.status = event.step;
  }

  const percent = Number(event.percent);
  if (event.progress === PROGRESS.ERROR && !(percent > 0)) {
    // The failure carries no progress of its own; keep the last one.
  } else if (!Number.isFinite(percent) || percent < 0) {
    status.progress_value = -1;
    status.progress_unknown = true;
  } else {
    status.progress_value = percent;
    status.progress_unknown = false;
  }

  if (event.speed) {
    status.speed = event.speed;
  }
  if (event.bit_rate) {
    status.bit_rate = event.bit_rate;
  }
  const remain = Number(event.time_remain_seconds);
  if (Number.isFinite(remain) && remain > 0) {
    status.time_remain_seconds = remain;
  }
  return task;
}

// ---------------------------------------------------------------- actions

// The action names below are the PATCH body's position values, which is the
// frozen contract (internal/api/tasks.go positionTop/Up/Down).
export const POSITION = { top: "top", up: "up", down: "down" };

// ------------------------------------------------------------------- the table

// COLUMNS is the DataGrid column list from Gui/MainWindow.xaml, in the order
// the XAML declared it. The header text is the legacy header text.
//
// Two legacy columns have no wire field of their own and are derived:
// "输入文件" is status.input, and "输出文件" is status.output (falling back to
// the task's own output, which the pipeline fills in only after a run).
export const COLUMNS = [
  { key: "enabled", label: "", width: "2rem", kind: "check" },
  { key: "name", label: "任务名称", width: "10rem", kind: "name" },
  { key: "input", label: "输入文件", width: "14rem", kind: "path" },
  { key: "chapter", label: "章节", width: "4rem", kind: "center" },
  { key: "output", label: "输出文件", width: "14rem", kind: "path" },
  { key: "status", label: "状态", width: "7rem", kind: "center" },
  { key: "progress", label: "进度", width: "10rem", kind: "progress" },
  { key: "speed", label: "速度", width: "6rem", kind: "right" },
  { key: "bit_rate", label: "码率", width: "7rem", kind: "right" },
  { key: "time_remain", label: "剩余时间", width: "6rem", kind: "center" },
  { key: "task_type", label: "任务类型", width: "5rem", kind: "center" },
  { key: "worker", label: "工作单元", width: "6rem", kind: "center" },
  { key: "rpc", label: "花屏检查", width: "6rem", kind: "rpc" },
  { key: "actions", label: "操作", width: "13rem", kind: "actions" },
];

// cellValues returns the display text of one cell, keyed by column key. It is
// exported so the smoke test can assert the mapping without a DOM.
//
// Each path column has a "raw_*" twin: the reference exactly as the API sent
// it, which the renderer puts in the cell's tooltip so the volume stays visible
// even though the display drops it.
export function cellValues(task) {
  const status = task.status || {};
  const percent = formatPercent(status.progress_value);
  const rpc = rpcText(status.rpc_status);
  const input = status.input || (task.inputs || [])[0] || "";
  const output = status.output || task.output || "";
  return {
    enabled: Boolean(status.enabled),
    name: task.name || status.name || task.id,
    input: displayPath(input),
    raw_input: rawPath(input),
    chapter: chapterText(status.chapter_status),
    output: displayPath(output),
    raw_output: rawPath(output),
    status: status.status || statusText(status.progress),
    progress: percent.text,
    progress_unknown: percent.unknown,
    progress_value: Number(status.progress_value) || 0,
    speed: status.speed || "",
    bit_rate: status.bit_rate || "",
    time_remain: formatTimeRemain(status.time_remain_seconds),
    task_type: taskTypeText(status.task_type),
    worker: status.worker_name || "",
    rpc: rpc.text,
    rpc_openable: rpc.openable,
    rpc_output: status.rpc_output || "",
    frames: formatFrames(status.frames_done, status.frames_total),
    input_size: formatSize(status.input_size),
  };
}

// canMove reports whether the queue will accept a reorder for this task: the
// engine only moves waiting tasks (TaskManager.canMoveLocked), so the buttons
// are disabled otherwise and the operator does not get a 409 for a click that
// was never possible.
export function canMove(task) {
  return (task.status || {}).progress === PROGRESS.WAITING;
}

// isRunning reports whether a task is in the running state.
export function isRunning(task) {
  return (task.status || {}).progress === PROGRESS.RUNNING;
}

// rowActions returns the per-row operations the legacy window offered for the
// selected task, with the same enabled rules the C# buttons applied:
//
//   - 上移 / 下移 / 置顶 need a waiting task (MainWindow.UpdateActiveRelatedButtons
//     plus TaskManager.SwapTasksByIndex / MoveTaskTop);
//   - 删除 needs a task that is not running (TaskManager.DeleteTask);
//   - 停止 needs a running task (the pool's StopWorker).
export function rowActions(task) {
  const running = isRunning(task);
  const movable = canMove(task);
  return [
    { key: "top", label: "置顶", enabled: movable },
    { key: "up", label: "上移", enabled: movable },
    { key: "down", label: "下移", enabled: movable },
    { key: "stop", label: "停止", enabled: running },
    { key: "delete", label: "删除", enabled: !running },
  ];
}

// bulkActions returns the toolbar buttons that act on the whole queue, with the
// enabled rules from the C# handlers:
//
//   - 运行 needs an enabled waiting task and a pool that is not running
//     (BtnRun: activeTaskCount > 0 && !wm.IsRunning);
//   - 终止 needs a running task (BtnStop warns "没有正在运行的任务");
//   - 清空 needs at least one ticked task (BtnEmpty: enabledTaskCount == 0 is
//     the "请勾选需要清除的任务" case) and removes the non-running ones.
export function bulkActions(tasks, poolRunning) {
  const list = tasks || [];
  const active = list.filter(
    (t) => (t.status || {}).enabled && (t.status || {}).progress === PROGRESS.WAITING
  ).length;
  const running = list.filter(isRunning).length;
  const enabled = list.filter((t) => (t.status || {}).enabled).length;
  const finished = list.filter(
    (t) => (t.status || {}).progress === PROGRESS.FINISHED
  ).length;

  return [
    { key: "run", label: "运行", enabled: active > 0 && !poolRunning },
    { key: "stop", label: "终止", enabled: running > 0 },
    { key: "new", label: "新建任务", enabled: true },
    { key: "update-chapter", label: "更新章节", enabled: active > 0 },
    { key: "clear", label: "清空已完成", enabled: enabled > 0 },
    { key: "clear-all", label: "清空全部", enabled: list.length > 0 },
    { key: "refresh", label: "刷新", enabled: true },
    {
      key: "new-worker",
      label: "新建工作单元",
      enabled: true,
      hint: finished + " 个已完成",
    },
  ];
}

// queueSummary renders the one-line queue summary the legacy window kept in its
// WorkerNumber label: the worker count plus the queue counts.
export function queueSummary(status) {
  const node = status.node || {};
  const pool = status.pool || {};
  const queue = status.queue || {};
  const tools = Object.keys(node.tools || {}).length;
  return {
    node:
      `节点 ${node.node_id || "?"}（${node.os || "?"}/${node.arch || "?"}）` +
      ` · 角色 ${node.role || "?"} · NUMA ${node.numa_nodes ?? "?"}` +
      ` · CPU ${node.cpus ?? "?"} · 工具 ${tools} 个`,
    pool:
      `工作单元 ${pool.workers ?? 0}（活跃 ${pool.active_workers ?? 0}，` +
      `${pool.running ? "运行中" : "已停止"}）`,
    queue:
      `任务 ${queue.total ?? 0}（等待 ${queue.waiting ?? 0}，运行 ${queue.running ?? 0}，` +
      `完成 ${queue.finished ?? 0}，失败 ${queue.error ?? 0}，勾选 ${queue.enabled ?? 0}）`,
  };
}

// dropNotice words a WebSocket gap. The hub counts events a slow client lost;
// each event is a snapshot rather than a delta, so the only correct reaction is
// to re-read the queue instead of applying the rest blindly.
export function dropNotice(dropped) {
  return (
    `事件流丢弃了 ${dropped} 条事件，正在重新读取任务列表以保证显示正确。`
  );
}
