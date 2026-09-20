// wizard.js is the "new task" wizard page: the Web UI counterpart of
// Gui/WizardWindow.xaml(.cs) plus Task/AddTaskService.cs.
//
// The legacy wizard read a project profile, previewed it, listed the profile's
// InputFiles, and then created one task per source, each with its own generated
// .vpy script and its own working/output paths. The REST API adds exactly one
// task per request (internal/api/tasks.go), so the loop over the sources lives
// here, in the client, and a partial failure is reported per file.
//
// This file is the DOM and network layer only. The profile checks come from
// OKEWizardCore (wizard-core.js), which is pure and unit-tested separately; the
// derived paths come from the server's own assembly pass, which the page asks
// for with POST /api/v1/tasks/prepare, so the page does not re-implement it.

"use strict";

const API = "/api/v1";
const Core = window.OKEWizardCore;

// ---------------------------------------------------------------------------
// State
// ---------------------------------------------------------------------------

// state is the wizard's working set. `profileText` keeps the operator's bytes
// verbatim so the server's tolerant parser sees the same file; `profile` is the
// parsed copy the wizard previews and mutates.
const state = {
  profileText: "",
  profileName: "",
  baseDir: "",
  profile: null,
  inputs: [],
  selected: new Set(),
  results: [],
};

// ---------------------------------------------------------------------------
// DOM helpers
// ---------------------------------------------------------------------------

const $ = (id) => document.getElementById(id);

// showError renders a ValidationError and highlights the field it names. The
// highlight is what turns the legacy code's 22 MessageBox calls into something
// the operator can act on.
function showError(err) {
  const box = $("wizard-error");
  const field = err && err.field ? String(err.field) : "";
  box.hidden = false;
  box.replaceChildren();

  const title = document.createElement("strong");
  title.textContent = (err && err.summary) || "出错了";
  box.appendChild(title);

  if (err && err.detail) {
    const detail = document.createElement("p");
    detail.textContent = err.detail;
    box.appendChild(detail);
  }
  if (field) {
    const where = document.createElement("p");
    where.className = "muted";
    where.textContent = "字段：" + field;
    box.appendChild(where);
  }
  highlightField(field);
}

function clearError() {
  const box = $("wizard-error");
  box.hidden = true;
  box.replaceChildren();
  highlightField("");
}

// highlightField marks the control a profile field maps to, so the operator is
// pointed at the thing to fix rather than only told what is wrong. An exact
// match wins; an indexed name such as AudioTracks[0].OutputCodec falls back to
// its array, then to its top-level field. A field with no element of its own
// falls back to the panel that owns the rule.
function highlightField(field) {
  for (const el of document.querySelectorAll("[data-field]")) {
    el.classList.remove("field-error");
  }
  if (!field) {
    return;
  }

  const candidates = [
    field,
    field.replace(/\[\d+\].*$/, ""),
    field.replace(/\[\d+\].*$/, "").replace(/\..*$/, ""),
  ];
  for (const name of candidates) {
    const el = document.querySelector(`[data-field="${CSS.escape(name)}"]`);
    if (el) {
      el.classList.add("field-error");
      return;
    }
  }

  // ContainerFormat is a profile field, but a re-encode against MP4 is refused
  // in the confirm step, so that is where the operator has to look.
  if (field === "ContainerFormat") {
    $("step-confirm").classList.add("field-error");
    return;
  }
  if (/^(ReEncode|EnableReEncode|ReExtractSource|VspipeArgs|Config)/.test(field)) {
    $("step-confirm").classList.add("field-error");
    return;
  }
  $("profile-block").classList.add("field-error");
}

function setStatus(text, kind) {
  const el = $("wizard-status");
  el.textContent = text || "";
  el.className = kind ? "status-" + kind : "";
}

// showStep reveals one of the three panels.
function showStep(name) {
  for (const id of ["step-profile", "step-inputs", "step-confirm"]) {
    $(id).hidden = id !== name;
  }
  window.scrollTo({ top: 0, behavior: "smooth" });
}

// ---------------------------------------------------------------------------
// Step 1: load and preview the profile
// ---------------------------------------------------------------------------

// loadProfileFile reads a profile through the File API. A browser cannot reveal
// the file's path, so the profile's own directory — which is what relative paths
// in it resolve against — has to be typed by the operator; the base directory
// field is where that happens.
async function loadProfileFile(file) {
  clearError();
  state.profileName = file.name;
  state.profileText = await file.text();
  $("profile-text").value = state.profileText;
  parseLoadedProfile();
}

// parseLoadedProfile parses and validates state.profileText. It returns whether
// the wizard may advance.
function parseLoadedProfile() {
  clearError();
  state.profile = null;
  state.inputs = [];
  state.selected = new Set();
  renderPreview();
  renderInputs();

  const raw = state.profileText;
  if (!raw.trim()) {
    showError(new Core.ValidationError("没有项目文件", "请先选择、拖入或粘贴一份 profile json。", ""));
    updateFinishState();
    return false;
  }

  const deprecated = Core.deprecatedOptionFound(raw);
  if (deprecated) {
    showError(new Core.ValidationError("json文件版本太老了", deprecated + "已不再支持", deprecated));
    updateFinishState();
    return false;
  }

  let prof;
  try {
    prof = JSON.parse(Core.stripTrailingCommas(raw));
  } catch (err) {
    showError(new Core.ValidationError("json文件写错了诶", String(err.message || err), ""));
    updateFinishState();
    return false;
  }

  // The preview is drawn from the parsed profile before it is validated, so a
  // rejected field has a row to highlight. Without this the preview would be
  // empty exactly when the operator needs to see which field is wrong.
  state.profile = prof;
  renderPreview();

  // LoadVsScript refused a script without the INPUTFILE tag before the wizard
  // could advance; the template is checked here, before anything is derived
  // from it.
  if (!Core.hasInputTag($("vpy-text").value)) {
    showError(
      new Core.ValidationError(
        "vpy没有为OKEGui设计",
        "vpy 模板里没有 #OKE:INPUTFILE 的标签，无法为每个输入文件生成脚本。",
        "InputScript"
      )
    );
    updateFinishState();
    return false;
  }
  try {
    state.inputs = Core.validateProfile(prof, state.baseDir);
  } catch (err) {
    // The preview is redrawn first: the derived fields the validator filled in
    // (VideoFormat, AudioFormat, the rational frame rate) belong in it, and the
    // highlight has to land on the final rows.
    renderPreview();
    showError(toValidationError(err));
    updateFinishState();
    return false;
  }

  state.selected = new Set(state.inputs);
  renderPreview();
  renderInputs();
  return true;
}

// toValidationError keeps an unexpected exception displayable.
function toValidationError(err) {
  if (err instanceof Core.ValidationError) {
    return err;
  }
  return new Core.ValidationError("处理失败", String((err && err.message) || err), "");
}

// renderPreview draws the read-only project summary. The field list follows
// TaskProfile.ToString, which is what the legacy wizard showed.
//
// Every row carries the profile field it came from in data-field, which is what
// makes the validation mapping visible: a rejected field has no form control of
// its own (the profile is one text area), so the preview row is what the
// highlight marks.
function renderPreview() {
  const dl = $("preview");
  dl.replaceChildren();
  const prof = state.profile;
  if (!prof) {
    const dt = document.createElement("dt");
    dt.textContent = "状态";
    const dd = document.createElement("dd");
    dd.textContent = "尚未载入可用的 profile。";
    dl.append(dt, dd);
    return;
  }

  const rows = [
    ["Version", "版本", String(prof.Version)],
    ["VSVersion", "VS 版本", prof.VSVersion || "（v2 不需要）"],
    ["ProjectName", "项目名字", prof.ProjectName || ""],
    ["EncoderType", "编码器类型", prof.EncoderType || ""],
    ["Encoder", "编码器路径", prof.Encoder || "（由工具链决定）"],
    ["EncoderParam", "编码参数", String(prof.EncoderParam || "").slice(0, 30) + "......"],
    ["ContainerFormat", "封装格式", prof.ContainerFormat || ""],
    ["VideoFormat", "视频编码", prof.VideoFormat || ""],
    ["Fps", "视频帧率", prof.TimeCode ? "VFR" : Number(prof.Fps).toFixed(3) + " fps"],
    ["TimeCode", "时间码(VFR)", prof.TimeCode ? "YES" : "NO"],
    ["AudioTracks", "音轨数量", String((prof.AudioTracks || []).length)],
    ["AudioFormat", "音频编码(主音轨)", prof.AudioFormat || ""],
    ["RenumberChapters", "章节名重编号", prof.RenumberChapters ? "YES" : "NO"],
    ["InputFiles", "输入文件数量", String(state.inputs.length)],
  ];
  for (const [field, key, value] of rows) {
    const dt = document.createElement("dt");
    dt.textContent = key;
    dt.dataset.field = field;
    const dd = document.createElement("dd");
    dd.textContent = value;
    dl.append(dt, dd);
  }
}

// ---------------------------------------------------------------------------
// Step 2: choose the sources
// ---------------------------------------------------------------------------

// renderInputs draws one checkbox per source. One checked source becomes one
// task, which is the legacy "loop over InputFiles" step.
function renderInputs() {
  const list = $("input-list");
  list.replaceChildren();
  if (state.inputs.length === 0) {
    const p = document.createElement("p");
    p.className = "muted";
    p.textContent = "这份 profile 没有列出输入文件。";
    list.appendChild(p);
    updateFinishState();
    return;
  }

  for (const path of state.inputs) {
    const row = document.createElement("label");
    row.className = "input-row";
    const box = document.createElement("input");
    box.type = "checkbox";
    box.checked = state.selected.has(path);
    box.addEventListener("change", () => {
      if (box.checked) {
        state.selected.add(path);
      } else {
        state.selected.delete(path);
      }
      renderPaths();
      updateFinishState();
    });
    const text = document.createElement("span");
    text.textContent = path;
    row.append(box, text);
    list.appendChild(row);
  }
  renderPaths();
  updateFinishState();
}

// selectedInputs returns the checked sources, in profile order.
function selectedInputs() {
  return state.inputs.filter((path) => state.selected.has(path));
}

function updateFinishState() {
  const count = selectedInputs().length;
  $("wizard-finish").disabled = count === 0;
  $("wizard-count").textContent =
    count === 0 ? "还没有选中任何输入文件。" : `已选择 ${count} 个输入文件，将新建 ${count} 个任务。`;
}

// ---------------------------------------------------------------------------
// Step 3: derived paths, generated scripts and the episode config
// ---------------------------------------------------------------------------

// PREVIEW_DELAY is the debounce for the preview request. The table is rebuilt on
// every keystroke in the base-directory field and on every selection change, and
// each rebuild is a server round trip, so the calls are coalesced. Without it,
// typing a base directory a character at a time would send one request per
// character. 200 ms is short enough that the table still feels live and long
// enough to swallow a burst of typing.
const PREVIEW_DELAY = 200;

// pathsTimer coalesces a burst of repaints into one request; pathsTicket numbers
// the requests so a slow answer that arrives after a newer one cannot overwrite
// it. Only the answer whose ticket is still current is drawn.
let pathsTimer = 0;
let pathsTicket = 0;

// renderPaths schedules the per-source preview. The request itself runs in
// refreshPaths.
//
// The old rows are deliberately left on screen for the debounce interval rather
// than cleared here: clearing them would blink the table empty on every
// keystroke. The empty-state message is updated immediately, because it needs no
// network.
function renderPaths() {
  $("paths-empty").hidden = selectedInputs().length > 0;
  if (pathsTimer) {
    clearTimeout(pathsTimer);
  }
  pathsTimer = setTimeout(refreshPaths, PREVIEW_DELAY);
}

// refreshPaths draws the per-source table from the server's own derivation.
//
// It is the same assembly POST /api/v1/tasks runs (wizard.Derive), so the table
// shows exactly the paths the task will store instead of a second implementation
// of the same algorithm. The endpoint writes and queues nothing, which is what
// makes it safe to call while the operator is still editing.
async function refreshPaths() {
  pathsTimer = 0;
  const ticket = ++pathsTicket;
  const inputs = selectedInputs();
  const tbody = $("path-rows");
  $("paths-empty").hidden = inputs.length > 0;

  if (inputs.length === 0) {
    tbody.replaceChildren();
    return;
  }

  // The body is the profile-carrying half of buildTaskBody. It is built here
  // rather than by calling buildTaskBody because name and write_vpy mean nothing
  // to /tasks/prepare, and because the episode config is deliberately left out:
  // it only affects the encode, not the derived paths, so folding it in would
  // make the preview fail on a config the operator is still typing.
  const body = {
    profile_text: state.profileText,
    base_dir: state.baseDir,
    inputs: inputs,
  };

  let tasks;
  try {
    tasks = await postPrepare(body);
  } catch (err) {
    // A daemon that is unreachable or refuses the profile is drawn as one row
    // per selected source, the shape a per-source derivation failure used to
    // draw. The failure never escapes as a rejected promise, so the page cannot
    // die on an unhandled rejection; profile-level problems keep going through
    // showError in parseLoadedProfile.
    if (ticket !== pathsTicket) {
      return;
    }
    tbody.replaceChildren();
    const message = pathErrorMessage(err);
    for (const inputFile of inputs) {
      tbody.appendChild(errorPathRow(inputFile, message));
    }
    return;
  }

  // A stale answer must not overwrite a newer one: the ticket makes that race
  // impossible without comparing payloads.
  if (ticket !== pathsTicket) {
    return;
  }
  renderPathRows(tbody, inputs, tasks);
}

// renderPathRows draws one row per selected source from the server's tasks. A
// source the server did not return keeps its row with empty prefixes rather than
// disappearing, so the row count still matches the selection.
function renderPathRows(tbody, inputs, tasks) {
  const byInput = new Map();
  for (const task of tasks) {
    byInput.set(task.input_file, task);
  }

  tbody.replaceChildren();
  for (const inputFile of inputs) {
    const task = byInput.get(inputFile);
    const cells = task
      ? [task.name, task.working_path_prefix, task.output_path_prefix]
      : [Core.baseName(inputFile), "", ""];
    const tr = document.createElement("tr");
    for (const text of cells) {
      const td = document.createElement("td");
      td.textContent = text;
      tr.appendChild(td);
    }
    tbody.appendChild(tr);
  }
}

// errorPathRow is the table's failure row: one cell spanning the three columns,
// naming the source and the reason. It mirrors the row the page drew when a
// single source could not be derived, before the derivation moved to the server.
function errorPathRow(inputFile, message) {
  const tr = document.createElement("tr");
  const td = document.createElement("td");
  td.colSpan = 3;
  td.textContent = `${inputFile} → ${message}`;
  tr.appendChild(td);
  return tr;
}

// pathErrorMessage keeps an unexpected exception displayable in the same shape
// the banner uses: the summary, and the detail when there is one.
function pathErrorMessage(err) {
  const error = toValidationError(err);
  return error.detail ? `${error.summary}：${error.detail}` : error.summary;
}

// postPrepare asks the daemon for every selected source's derivation without
// writing or queueing anything. It is the preview counterpart of postTask: the
// same profile-carrying fields and the same error decoding, so a refusal reads
// the same in the table as it would in the result list.
async function postPrepare(body) {
  const resp = await fetch(API + "/tasks/prepare", {
    method: "POST",
    headers: { "Content-Type": "application/json", Accept: "application/json" },
    body: JSON.stringify(body),
  });
  const payload = await resp.json().catch(() => ({}));
  if (!resp.ok) {
    throw Core.errorFromBody(payload, `HTTP ${resp.status}`);
  }
  return payload.tasks || [];
}

// episodeConfig builds the EpisodeConfig for every task this wizard creates. The
// legacy code read it from a `<input>.json` beside the source; a browser cannot
// read a sibling file, so the operator enters it here.
//
// It is folded into the profile's own Config field before the request, because
// POST /api/v1/tasks has no separate field for it and rejects unknown ones
// (internal/api/respond.go decodeBody).
function episodeConfig() {
  const cfg = {
    VspipeArgs: $("ep-vspipe-args")
      .value.split(/\r?\n/)
      .map((s) => s.trim())
      .filter((s) => s !== ""),
    EnableReEncode: $("ep-reencode").checked,
    ReExtractSource: $("ep-reextract").checked,
    // Resolved against the profile's directory, the way AddEpProfileService
    // resolved it against the episode file's directory.
    ReEncodeOldFile: Core.resolveRelative($("ep-oldfile").value.trim(), state.baseDir),
    ReEncodeSliceArray: Core.parseSlices($("ep-slices").value),
  };
  const checked = Core.validateEpisodeConfig(cfg);
  // WizardFinish refused a re-encode against a non-MKV container:
  // "ReEncode项目暂时只支持mkv格式输出". It is a per-task check in the legacy
  // loop, but the container comes from the profile, so it is the same answer for
  // every source.
  if (checked.EnableReEncode && state.profile.ContainerFormat !== "MKV") {
    throw new Core.ValidationError(
      "封装格式不支持",
      `ReEncode项目暂时只支持mkv格式输出，${state.profile.ContainerFormat}格式暂不支持`,
      "ContainerFormat"
    );
  }
  return checked;
}

// profileTextWithConfig returns the profile text to send. When the episode
// config is empty the operator's own bytes go through untouched; otherwise the
// parsed profile is re-serialized with Config folded in.
//
// The profile format is frozen, and this round trip is safe for it: every value
// is a plain JSON scalar, array or object, and the server parses the result with
// the same tolerant parser.
function profileTextWithConfig(cfg) {
  const empty =
    !cfg.EnableReEncode && cfg.VspipeArgs.length === 0 && !cfg.ReExtractSource;
  if (empty) {
    return state.profileText;
  }
  const merged = Object.assign({}, state.profile, { Config: cfg });
  return JSON.stringify(merged, null, 4);
}

// buildTaskBody turns one source into a POST /api/v1/tasks body.
//
// profile_text is used rather than profile so the bytes go through the server's
// tolerant parser exactly like a file would, which is what keeps the trailing
// commas every real profile contains working. base_dir stands in for the
// profile's own directory, which the browser cannot know.
//
// write_vpy is what makes this request work at all. The three fields the
// pipeline needs (input_script, working_path_prefix, output_path_prefix) are
// profile fields, not request fields, and the HTTP layer does not derive them
// for a plain task: it refuses a profile that is missing them. The legacy
// wizard filled them in as its finishing step, and write_vpy makes the server
// do exactly that - it runs wizard.Assemble, writes the per-source .vpy next to
// the source, and sets the three fields from that result.
//
// They are deliberately not sent from here. A value in the request wins over
// the derivation, so sending the page's own idea of the paths would override
// the server's and could name a .vpy that was never written. The page's table
// is a preview drawn with the same algorithm; the server's answer is the one
// that counts.
function buildTaskBody(inputFile, cfg) {
  return {
    profile_text: profileTextWithConfig(cfg),
    base_dir: state.baseDir,
    inputs: [inputFile],
    name: taskName(inputFile),
    write_vpy: true,
  };
}

// taskName mirrors the legacy TaskDetail.TaskName:
//
//	ProjectName == "" ? FileInfo(input).Name : ProjectName + "-" + FileInfo(input).Name
function taskName(inputFile) {
  const base = Core.baseName(inputFile);
  const project = state.profile.ProjectName || "";
  return project === "" ? base : project + "-" + base;
}

// ---------------------------------------------------------------------------
// Submitting
// ---------------------------------------------------------------------------

// postTask sends one task and throws a ValidationError carrying the server's
// summary/detail/field triple when it is refused.
async function postTask(body) {
  const resp = await fetch(API + "/tasks", {
    method: "POST",
    headers: { "Content-Type": "application/json", Accept: "application/json" },
    body: JSON.stringify(body),
  });
  const payload = await resp.json().catch(() => ({}));
  if (!resp.ok) {
    throw Core.errorFromBody(payload, `HTTP ${resp.status}`);
  }
  return payload.task;
}

// createTasks runs the legacy WizardFinish loop: one task per selected source. A
// failure on one source does not stop the others, and the outcome is reported
// per file — which is the improvement the one-request-per-task contract buys.
async function createTasks() {
  clearError();
  state.results = [];

  let cfg;
  try {
    cfg = episodeConfig();
  } catch (err) {
    showError(toValidationError(err));
    return;
  }

  const inputs = selectedInputs();
  if (inputs.length === 0) {
    showError(new Core.ValidationError("没有输入文件", "请至少选中一个输入文件。", "InputFiles"));
    return;
  }

  $("wizard-finish").disabled = true;
  setStatus(`正在新建 ${inputs.length} 个任务…`, "busy");

  for (const inputFile of inputs) {
    let body;
    try {
      body = buildTaskBody(inputFile, cfg);
    } catch (err) {
      state.results.push({ input: inputFile, ok: false, error: toValidationError(err) });
      continue;
    }
    try {
      const task = await postTask(body);
      state.results.push({ input: inputFile, ok: true, task });
    } catch (err) {
      state.results.push({ input: inputFile, ok: false, error: toValidationError(err) });
    }
  }

  renderResults();
  const failed = state.results.filter((r) => !r.ok);
  const ok = state.results.length - failed.length;
  if (failed.length === 0) {
    setStatus(`已新建 ${ok} 个任务。`, "ok");
  } else {
    setStatus(`已新建 ${ok} 个任务，${failed.length} 个失败。`, "error");
    // The first failure is what the banner explains; the rest stay in the list.
    showError(failed[0].error);
  }
  updateFinishState();
}

// renderResults lists the per-source outcome of createTasks.
function renderResults() {
  const box = $("wizard-results");
  box.replaceChildren();
  if (state.results.length === 0) {
    return;
  }
  const list = document.createElement("ul");
  for (const r of state.results) {
    const li = document.createElement("li");
    li.className = r.ok ? "result-ok" : "result-error";
    li.textContent = r.ok
      ? `${r.input} → 已加入队列（${r.task.name || r.task.id}）`
      : `${r.input} → ${r.error.summary}：${r.error.detail}`;
    list.appendChild(li);
  }
  box.appendChild(list);
}

// ---------------------------------------------------------------------------
// Wiring
// ---------------------------------------------------------------------------

// wireDropZone loads a profile dropped on the page, which is the legacy
// SelectProjectFile_Drop behaviour.
function wireDropZone() {
  const zone = $("drop-zone");
  zone.addEventListener("dragover", (e) => {
    e.preventDefault();
    zone.classList.add("dragging");
  });
  zone.addEventListener("dragleave", () => zone.classList.remove("dragging"));
  zone.addEventListener("drop", async (e) => {
    e.preventDefault();
    zone.classList.remove("dragging");
    const file = e.dataTransfer.files[0];
    if (file) {
      await loadProfileFile(file);
    }
  });
}

function wire() {
  $("profile-file").addEventListener("change", async (e) => {
    const file = e.target.files[0];
    if (file) {
      await loadProfileFile(file);
    }
  });

  $("profile-text").addEventListener("input", (e) => {
    state.profileText = e.target.value;
    state.profileName = "";
    parseLoadedProfile();
  });

  $("base-dir").addEventListener("input", (e) => {
    state.baseDir = e.target.value.trim();
    parseLoadedProfile();
  });

  // The template decides whether the wizard can advance, so a change to it is
  // re-validated like a change to the profile.
  $("vpy-text").addEventListener("input", parseLoadedProfile);

  $("wizard-next").addEventListener("click", () => {
    if (parseLoadedProfile()) {
      showStep("step-inputs");
    }
  });
  $("wizard-back").addEventListener("click", () => showStep("step-profile"));
  $("wizard-next2").addEventListener("click", () => {
    renderPaths();
    showStep("step-confirm");
  });
  $("wizard-back2").addEventListener("click", () => showStep("step-inputs"));

  $("select-all").addEventListener("click", () => {
    state.selected = new Set(state.inputs);
    renderInputs();
  });
  $("select-none").addEventListener("click", () => {
    state.selected = new Set();
    renderInputs();
  });

  $("ep-reencode").addEventListener("change", (e) => {
    $("reencode-fields").hidden = !e.target.checked;
  });
  $("wizard-finish").addEventListener("click", createTasks);
  wireDropZone();
}

wire();

// The DOM layer's surface, exposed so a test can drive the page. The pure half
// lives in window.OKEWizardCore.
window.OKEWizard = {
  state,
  selectedInputs,
  episodeConfig,
  profileTextWithConfig,
  buildTaskBody,
  parseLoadedProfile,
  showStep,
};
