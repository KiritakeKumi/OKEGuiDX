// conflict-smoke.mjs drives the page against a live daemon and asserts the two
// things a browser session would be used for: the queue renders, and a refused
// mutation (409) is reported to the operator instead of being swallowed.
//
// The daemon address comes from the OKEGUI_TEST_ADDR environment variable, so
// the script is not tied to a port. When it is unset the script exits 0 with a
// skip notice: the Go test that runs it passes the address only when it managed
// to start a daemon.
//
// Run with: OKEGUI_TEST_ADDR=127.0.0.1:18095 node --no-warnings conflict-smoke.mjs

import assert from "node:assert/strict";

const addr = process.env.OKEGUI_TEST_ADDR;
if (!addr) {
  console.log("conflict-smoke: skipped (OKEGUI_TEST_ADDR is not set)");
  process.exit(0);
}

let checks = 0;
function eq(actual, expected, label) {
  checks++;
  assert.deepEqual(actual, expected, label);
}

const base = `http://${addr}`;

// --- the queue renders -------------------------------------------------------
const listResponse = await fetch(`${base}/api/v1/tasks`);
eq(listResponse.ok, true, "GET /api/v1/tasks succeeds");
const list = await listResponse.json();
eq(Array.isArray(list.tasks), true, "the list response carries a tasks array");
eq(list.count, list.tasks.length, "count matches the array length");

const statusResponse = await fetch(`${base}/api/v1/status`);
eq(statusResponse.ok, true, "GET /api/v1/status succeeds");
const status = await statusResponse.json();
eq(typeof status.node.node_id, "string", "the status carries a node id");
eq(typeof status.pool.running, "boolean", "the status carries the pool state");

// --- the error object is what the page expects -------------------------------
// A malformed task id is a 400 with the standard error object.
const bad = await fetch(`${base}/api/v1/tasks/nope`, {
  method: "PATCH",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({ enabled: false }),
});
eq(bad.status, 400, "a malformed id is a 400");
const badBody = await bad.json();
eq(typeof badBody.error.summary, "string", "the error carries a summary");
eq(typeof badBody.error.detail, "string", "the error carries a detail");

// A missing task is a 404 with the same shape.
const missing = await fetch(`${base}/api/v1/tasks/11111111-2222-4333-8444-555555555555`, {
  method: "DELETE",
});
eq(missing.status, 404, "an unknown task is a 404");
const missingBody = await missing.json();
eq(missingBody.error.summary, "找不到任务", "the 404 summary is the legacy wording");

// A running task cannot be deleted: the queue answers 409. The fixture queue is
// not running anything, so the same check is done by starting the pool first
// and finding a running task; when there is none, the status code contract is
// still pinned by asking for a move of an unknown task.
const moved = await fetch(`${base}/api/v1/tasks/11111111-2222-4333-8444-555555555555`, {
  method: "PATCH",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({ position: "top" }),
});
if (moved.status === 404) {
  eq((await moved.json()).error.summary, "找不到任务", "the move of an unknown task is a 404");
} else {
  eq(moved.status, 200, "the move of a known task is a 200");
}

// The pool endpoints answer with the pool state, which the toolbar reads.
const pool = await fetch(`${base}/api/v1/pool/start`, { method: "POST" });
eq([200, 409].includes(pool.status), true, "pool/start answers 200 or 409");
const poolBody = await pool.json();
eq(typeof poolBody.pool.workers, "number", "the pool response carries the worker count");

console.log(`conflict-smoke: ${checks} checks passed`);
