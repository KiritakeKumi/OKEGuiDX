// check.mjs is a dependency-free smoke test of the F1 page's pure helpers. It
// is run with `node --no-warnings check.mjs`, so it needs no package.json, no
// npm install and no test framework; the Go test suite (web_test.go) invokes it
// when node is available and skips otherwise.
//
// It lives under testdata/ rather than next to the page so it is not part of
// the embedded tree: the UI ships inside the daemon binary, and a test file has
// no business being in a release.
//
// What it pins is the formatting contract, because that is where a rewrite
// silently drifts from the C# original: the "0.00%" progress, the "大于一周"
// ETA, the Chinese RPC states, the chapter names and the task type names. The
// expected values come from the C# sources (Task/TaskStatus.cs) and from the
// engine's own fixture (internal/engine/testdata/status_fixture.json).

import assert from "node:assert/strict";

const mod = await import(new URL("../../static/tasks.js", import.meta.url));

const {
  formatPercent,
  formatFixed2,
  formatTimeRemain,
  formatFrames,
  formatSize,
  chapterText,
  rpcText,
  taskTypeText,
  statusText,
  statusClass,
  displayPath,
  rawPath,
  baseName,
  cellValues,
  rowActions,
  bulkActions,
  queueSummary,
  dropNotice,
  patchStatus,
  TaskList,
  COLUMNS,
} = mod;

let checks = 0;
function eq(actual, expected, label) {
  checks++;
  assert.deepEqual(actual, expected, label);
}

// --- progress: TaskStatus.cs ProgressValue -> ProgressStr --------------------
// The values are the engine's own percent_format fixture, so the browser and
// the Go formatter are pinned to the same numbers.
eq(formatPercent(-1), { text: "", unknown: true }, "negative means unknown");
eq(formatPercent(-0.5), { text: "", unknown: true }, "any negative is unknown");
eq(formatPercent(0), { text: "0.00%", unknown: false }, "zero");
eq(formatPercent(0.005), { text: "0.01%", unknown: false }, "half-up at 2 digits");
eq(formatPercent(0.125), { text: "0.13%", unknown: false }, "0.125 rounds up");
eq(formatPercent(12.5), { text: "12.50%", unknown: false }, "12.5");
eq(formatPercent(33.333333), { text: "33.33%", unknown: false }, "truncate at 2");
eq(formatPercent(99.995), { text: "100.00%", unknown: false }, "99.995 -> 100.00");
eq(formatPercent(100), { text: "100.00%", unknown: false }, "100");
eq(formatPercent(150), { text: "150.00%", unknown: false }, "over 100");
eq(formatPercent(2.675), { text: "2.68%", unknown: false }, "2.675");
eq(formatPercent(1.005), { text: "1.01%", unknown: false }, "1.005");
eq(formatFixed2(-3.456), "-3.46", "negative fixed2");

// --- time remain: TaskStatus.cs TimeRemainStr -------------------------------
eq(formatTimeRemain(0), "0:00:00", "zero");
eq(formatTimeRemain(1), "0:00:01", "one second");
eq(formatTimeRemain(60), "0:01:00", "one minute");
eq(formatTimeRemain(3540), "0:59:00", "59 minutes");
eq(formatTimeRemain(3600), "1:00:00", "one hour");
eq(formatTimeRemain(3601), "1:00:01", "just over an hour");
eq(formatTimeRemain(25 * 3600 + 30 * 60), "25:30:00", "25 hours");
eq(formatTimeRemain(100 * 3600 + 45 * 60), "100:45:00", "100 hours");
eq(formatTimeRemain(7 * 24 * 3600), "168:00:00", "exactly one week");
eq(formatTimeRemain(7 * 24 * 3600 + 1), "大于一周", "one second past a week");
eq(formatTimeRemain(30 * 24 * 3600), "大于一周", "the unknown sentinel");
eq(formatTimeRemain(-90 * 60), "-1:30:00", "negative keeps the sign");
eq(formatTimeRemain(1.9), "0:00:01", "fractional seconds truncate");
eq(formatTimeRemain(NaN), "", "NaN renders empty");

// --- chapter / RPC / task type names ----------------------------------------
eq(chapterText("No"), "No", "chapter No");
eq(chapterText("Yes"), "Yes", "chapter Yes");
eq(chapterText("Added"), "Added", "chapter Added");
eq(chapterText("Maybe"), "Maybe", "chapter Maybe");
eq(chapterText("MKV"), "MKV", "chapter MKV");
eq(chapterText("Warn"), "Warn", "chapter Warn");
eq(chapterText(undefined), "No", "a missing chapter status is No");
eq(chapterText("Future"), "Future", "an unknown chapter status is shown raw");

eq(rpcText("等待中"), { text: "等待中", openable: false, known: true }, "RPC waiting");
eq(rpcText("跳过"), { text: "跳过", openable: false, known: true }, "RPC skipped");
eq(rpcText("错误"), { text: "错误", openable: false, known: true }, "RPC error");
eq(rpcText("未通过"), { text: "未通过", openable: true, known: true }, "RPC failed");
eq(rpcText("通过"), { text: "通过", openable: true, known: true }, "RPC passed");
eq(rpcText(""), { text: "等待中", openable: false, known: true }, "an empty RPC status waits");
eq(rpcText("未知状态"), { text: "未知状态", openable: false, known: false }, "an unknown RPC status is shown raw");

eq(taskTypeText("Normal"), "Normal", "normal task");
eq(taskTypeText("ReEncode"), "ReEncode", "re-encode task");
eq(taskTypeText(0), "Normal", "the numeric normal value");
eq(taskTypeText(1), "ReEncode", "the numeric re-encode value");
eq(taskTypeText(undefined), "Normal", "an absent type is normal");
eq(taskTypeText("Future"), "Future", "an unknown type name is shown raw");

eq(statusText("WAITING"), "等待中", "waiting");
eq(statusText("RUNNING"), "运行中", "running");
eq(statusText("ERROR"), "错误", "error");
eq(statusText("FINISHED"), "完成", "finished");
eq(statusClass("ERROR"), "error", "error class");
eq(statusClass("FINISHED"), "finished", "finished class");

// --- paths ------------------------------------------------------------------
// The wire form of a FileRef is "volume/rel" (model.FileRef MarshalText), and
// the standalone volume's root is the filesystem root. The Windows drive letter
// is not part of the reference: the queue stores paths relative to a volume root
// so that a path stays valid when the root changes (CLUSTER.md §2).
eq(displayPath("local/WORKS/ep01/00000.m2ts"), "/WORKS/ep01/00000.m2ts", "a windows path");
eq(displayPath("local/mnt/media/ep01/00000.m2ts"), "/mnt/media/ep01/00000.m2ts", "a unix path");
eq(displayPath("nas/ep01/00001.m2ts"), "nas:/ep01/00001.m2ts", "a remote volume");
eq(displayPath("local"), "local", "a bare volume");
eq(displayPath("local/"), "", "an unset reference is empty, not the root");
eq(displayPath(""), "", "an empty reference");
eq(displayPath(undefined), "", "an absent reference");
eq(rawPath("local/WORKS/ep01.m2ts"), "local/WORKS/ep01.m2ts", "the raw reference");
eq(baseName("local/WORKS/ep01/00000.m2ts"), "00000.m2ts", "the base name");
eq(baseName("local/WORKS/ep01/sub/00000.m2ts"), "00000.m2ts", "the base name, nested");

// --- frames and sizes -------------------------------------------------------
eq(formatFrames(120, 240), "120 / 240", "frame counters");
eq(formatFrames(0, 0), "", "no frames");
eq(formatFrames(10, undefined), "", "a missing total");
eq(formatSize(0), "", "zero bytes");
eq(formatSize(1536), "1.50 KB", "1536 bytes");
eq(formatSize(1024 * 1024), "1.00 MB", "one mebibyte");

// --- the cell mapping -------------------------------------------------------
const sample = {
  id: "11111111-2222-4333-8444-555555555555",
  name: "ep01",
  output: "local/out/ep01.mkv",
  status: {
    enabled: true,
    progress: "RUNNING",
    status: "压制中",
    progress_value: 42.125,
    speed: "12.34 fps",
    bit_rate: "1234.56 kb/s",
    time_remain_seconds: 3661,
    task_type: 1,
    worker_name: "Worker-1",
    chapter_status: "Maybe",
    rpc_status: "未通过",
    rpc_output: "local/out/ep01.rpc",
    input: "local/work/ep01.m2ts",
    frames_done: 100,
    frames_total: 200,
  },
};
const cells = cellValues(sample);
eq(cells.name, "ep01", "cell name");
eq(cells.input, "/work/ep01.m2ts", "cell input");
eq(cells.output, "/out/ep01.mkv", "cell output");
eq(cells.chapter, "Maybe", "cell chapter");
eq(cells.status, "压制中", "cell status");
eq(cells.progress, "42.13%", "cell progress");
eq(cells.speed, "12.34 fps", "cell speed");
eq(cells.bit_rate, "1234.56 kb/s", "cell bit rate");
eq(cells.time_remain, "1:01:01", "cell ETA");
eq(cells.task_type, "ReEncode", "cell task type");
eq(cells.worker, "Worker-1", "cell worker");
eq(cells.rpc, "未通过", "cell RPC");
eq(cells.rpc_openable, true, "RPC result is openable");
eq(cells.frames, "100 / 200", "cell frames");
eq(cells.enabled, true, "cell checkbox");
eq(cells.raw_input, "local/work/ep01.m2ts", "cell input keeps the raw reference");
eq(cells.raw_output, "local/out/ep01.mkv", "cell output keeps the raw reference");

// A task without a status object must not throw: the queue can hold a task
// whose JSON predates a field.
const bare = cellValues({ id: "x" });
eq(bare.name, "x", "a bare task falls back to its id");
eq(bare.progress, "0.00%", "a bare task has zero progress");
eq(bare.chapter, "No", "a bare task has no chapter");

// --- per-row and bulk actions ----------------------------------------------
const waiting = { status: { progress: "WAITING", enabled: true } };
const running = { status: { progress: "RUNNING", enabled: false } };
eq(
  rowActions(waiting).map((a) => [a.key, a.enabled]),
  [
    ["top", true],
    ["up", true],
    ["down", true],
    ["stop", false],
    ["delete", true],
  ],
  "a waiting task can be moved and deleted"
);
eq(
  rowActions(running).map((a) => [a.key, a.enabled]),
  [
    ["top", false],
    ["up", false],
    ["down", false],
    ["stop", true],
    ["delete", false],
  ],
  "a running task can only be stopped"
);

const bulk = (tasks, pool) =>
  Object.fromEntries(bulkActions(tasks, pool).map((a) => [a.key, a.enabled]));
eq(
  bulk([waiting, running], false),
  {
    run: true,
    stop: true,
    new: true,
    "update-chapter": true,
    clear: true,
    "clear-all": true,
    refresh: true,
    "new-worker": true,
  },
  "bulk actions with work to do"
);
eq(
  bulk([], false),
  {
    run: false,
    stop: false,
    new: true,
    "update-chapter": false,
    clear: false,
    "clear-all": false,
    refresh: true,
    "new-worker": true,
  },
  "an empty queue disables the work actions"
);
eq(bulk([waiting], true).run, false, "a running pool cannot be started again");

// --- the queue summary ------------------------------------------------------
const summary = queueSummary({
  node: { node_id: "host", os: "windows", arch: "amd64", role: "standalone", numa_nodes: 2, cpus: 16, tools: { x265: {} } },
  pool: { running: true, workers: 2, active_workers: 1 },
  queue: { total: 3, waiting: 1, running: 1, finished: 1, error: 0, enabled: 2 },
});
eq(summary.node.includes("host"), true, "the summary names the node");
eq(summary.pool.includes("运行中"), true, "the summary reports the pool");
eq(summary.queue.includes("任务 3"), true, "the summary reports the queue");
eq(summary.queue.includes("勾选 2"), true, "the summary reports the ticked count");

// --- event patching ---------------------------------------------------------
// The mapping mirrors internal/engine/worker.go applyStatus, including the two
// rules that are easy to get wrong: a terminal event without a percent keeps
// the last value, and the display fields are sticky.
{
  const list = new TaskList();
  list.replace([
    { id: "a", name: "ep01", status: { progress: "WAITING", progress_value: 0, speed: "0.0 fps" } },
  ]);
  eq(list.apply({ task_id: "a", progress: "RUNNING", step: "压制中", percent: 42.5, speed: "10.00 fps", time_remain_seconds: 60 }), true, "the event matched");
  const task = list.get("a");
  eq(task.status.progress, "RUNNING", "progress applied");
  eq(task.status.status, "压制中", "step applied");
  eq(task.status.progress_value, 42.5, "percent applied");
  eq(task.status.speed, "10.00 fps", "speed applied");
  eq(task.status.time_remain_seconds, 60, "ETA applied");

  list.apply({ task_id: "a", progress: "ERROR", percent: 0, error: { summary: "vpy出错" } });
  eq(task.status.progress, "ERROR", "the terminal event landed");
  eq(task.status.status, "vpy出错", "the error summary is shown");
  eq(task.status.progress_value, 42.5, "a failed task keeps its last percent");
  eq(task.status.speed, "10.00 fps", "speed is sticky");
  eq(task.status.time_remain_seconds, 60, "the ETA is sticky");

  eq(list.apply({ task_id: "unknown", progress: "RUNNING" }), false, "an unknown task is reported");
  eq(list.size, 1, "an unknown event does not add a row");

  // A negative percent is the "unknown" state, which blanks the text and sets
  // the indeterminate flag.
  list.apply({ task_id: "a", progress: "RUNNING", step: "封装中", percent: -1 });
  eq(task.status.progress_unknown, true, "a negative percent sets unknown");
  eq(task.status.progress_value, -1, "a negative percent is stored as -1");
}

// --- the column list --------------------------------------------------------
// The legacy DataGrid column order, which is what the operator reads left to
// right. The last entry is the new per-row action column.
eq(
  COLUMNS.map((c) => c.label),
  ["", "任务名称", "输入文件", "章节", "输出文件", "状态", "进度", "速度", "码率", "剩余时间", "任务类型", "工作单元", "花屏检查", "操作"],
  "the DataGrid column order"
);

eq(
  dropNotice(3).includes("3"),
  true,
  "a dropped-event notice names the count"
);

// --- the error object --------------------------------------------------------
// The frozen error shape is {"error":{summary,detail,field}} and 409 is the one
// status the UI words specially (a refused move, a delete of a running task, a
// pool start with no workers).
{
  const { ApiError, QueueActions, describeError } = await import(
    new URL("../../static/api.js", import.meta.url)
  );

  // ApiError's path is the full request URL, which is what a diagnostic needs;
  // the constructor argument is that URL, not the route.
  const conflict = new ApiError(409, "无法删除正在运行的任务", "任务 x 正在运行，请先停止工作单元。", "/api/v1/tasks/x");
  eq(conflict.conflict, true, "409 is a conflict");
  eq(
    describeError(conflict),
    "无法删除正在运行的任务：任务 x 正在运行，请先停止工作单元。",
    "a conflict is reported with the server's own summary and detail"
  );
  eq(new ApiError(404, "找不到任务", "", "/tasks/x").conflict, false, "404 is not a conflict");
  eq(
    describeError(new ApiError(404, "找不到任务", "", "/tasks/x")),
    "找不到任务",
    "an error with no detail is reported by its summary"
  );
  eq(
    describeError(new ApiError(0, "无法连接服务", "Failed to fetch", "/tasks")),
    "无法连接服务：Failed to fetch",
    "a transport failure is reported with its reason"
  );

  // The client turns the server's error object into an ApiError, and a 409 is
  // recognisable from the status alone.
  const client = new QueueActions("/api/v1");
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => ({
    ok: false,
    status: 409,
    async text() {
      return JSON.stringify({
        error: {
          summary: "无法移动任务",
          detail: "任务 x 不在等待中，无法移动。",
        },
      });
    },
  });
  let caught = null;
  try {
    await client.move("x", "up");
  } catch (err) {
    caught = err;
  }
  globalThis.fetch = originalFetch;
  eq(caught !== null, true, "a refused move throws");
  eq(caught.status, 409, "the status is preserved");
  eq(caught.conflict, true, "a refused move is a conflict");
  eq(caught.summary, "无法移动任务", "the summary comes from the server");
  eq(caught.detail, "任务 x 不在等待中，无法移动。", "the detail comes from the server");
  eq(caught.path, "/api/v1/tasks/x", "the request path is recorded");
}

console.log(`tasks.js: ${checks} checks passed`);
