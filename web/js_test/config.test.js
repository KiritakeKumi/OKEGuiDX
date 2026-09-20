// config.test.js pins the pure half of the settings panel (WORKSTREAMS.md F3).
//
// It runs under `node --test`, with no dependencies and no build step, which is
// the only test runner available to a project that must not add a front-end
// toolchain. The fixtures are the JSON names in internal/platform/configdir.go
// and the response shapes of internal/api/config.go and internal/api/status.go,
// not invented values.

"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const Config = require("../static/config.js");

// configJSONKeys is the wire format of platform.Config, in the order the file
// writes it. It is written out here rather than imported from the Go side so
// that a rename on either side fails this test.
const configJSONKeys = [
  "vspipePath",
  "logLevel",
  "singleNuma",
  "rpCheckerPath",
  "avx512",
  "reducePath",
];

// sampleConfig is a GET /api/v1/config body with every field set to a
// non-default value, so a panel that dropped one would be caught.
const sampleConfig = {
  vspipePath: "C:\\tools\\vapoursynth\\vspipe.exe",
  logLevel: "INFO",
  singleNuma: true,
  rpCheckerPath: "C:\\tools\\rpc\\RPChecker.exe",
  avx512: true,
  reducePath: false,
};

test("the field list is exactly the settings schema", () => {
  assert.deepEqual([...Config.CONFIG_FIELDS].sort(), [...configJSONKeys].sort());
});

test("every setting has a control in the template", () => {
  for (const key of configJSONKeys) {
    assert.match(Config.TEMPLATE, new RegExp(`id="${key}"`), `no control for ${key}`);
  }
});

test("the template names the two path fields and the three switches", () => {
  for (const id of ["vspipe-fill", "rpChecker-fill", "save", "discard", "tool-rows"]) {
    assert.match(Config.TEMPLATE, new RegExp(`id="${id}"`), `no element ${id}`);
  }
});

test("the log level list is the legacy ComboBox minus OFF", () => {
  // ConfigPanel.xaml listed OFF, FATAL, ERROR, WARN, INFO, DEBUG, TRACE. The API
  // rejects OFF (validateConfig), so the panel must not offer it.
  assert.deepEqual([...Config.LOG_LEVELS], ["FATAL", "ERROR", "WARN", "INFO", "DEBUG", "TRACE"]);
  assert.ok(!Config.LOG_LEVELS.includes("OFF"));
});

test("the request body nests the settings under config", () => {
  assert.deepEqual(Config.configBody(sampleConfig), { config: sampleConfig });
});

test("the request body always carries every field", () => {
  // PUT /api/v1/config replaces the whole file, so a missing field is an erased
  // setting. readForm builds the object from CONFIG_FIELDS, and this pins that
  // the list covers the schema: the two assertions are the same invariant seen
  // from the two sides.
  const body = Config.configBody(sampleConfig);
  assert.deepEqual(Object.keys(body.config).sort(), [...configJSONKeys].sort());
});

test("required tools match internal/toolchain", () => {
  // internal/toolchain.requiredTools is {vspipe, ffmpeg, ffprobe}; everything
  // else only removes a feature.
  assert.deepEqual([...Config.REQUIRED_TOOLS].sort(), ["ffmpeg", "ffprobe", "vspipe"]);
  assert.equal(Config.isRequiredTool("vspipe"), true);
  assert.equal(Config.isRequiredTool("qaac"), false);
  assert.equal(Config.isRequiredTool("nope"), false);
});

test("the tool notes cover the toolchain constants", () => {
  // The keys are the Tool* constants of internal/toolchain. ToolLSmash is
  // "muxer" and ToolEac3to is "eac3to-wrapper", which is why they are listed
  // rather than derived from the tool's file name.
  const names = [
    "vspipe",
    "x264",
    "x265",
    "svtav1",
    "ffmpeg",
    "ffprobe",
    "mkvmerge",
    "mkvextract",
    "muxer",
    "qaac",
    "eac3to-wrapper",
    "flac",
    "tchapter",
    "rpchecker",
  ];
  assert.deepEqual([...Config.knownTools()].sort(), [...names].sort());
  for (const name of names) {
    assert.ok(Config.TOOL_NOTES[name], `no note for ${name}`);
  }
});

test("the feature notes cover internal/node", () => {
  // node.FeatureAAC/Eac3to/NUMA/FdkAAC.
  for (const feature of ["aac", "eac3to", "numa", "fdk-aac"]) {
    assert.ok(Config.FEATURE_NOTES[feature], `no note for ${feature}`);
  }
});

test("configPaths mirrors platform.ConfigDirPath", () => {
  // os.UserConfigDir()/OKEGuiDX on Windows is %AppData%\OKEGuiDX.
  const win = Config.configPaths("windows");
  assert.match(win.dir, /%AppData%\\OKEGuiDX/);
  assert.match(win.file, /OKEGuiConfig\.json$/);
  assert.match(win.file, /%AppData%\\OKEGuiDX/);

  // On Linux it is $XDG_CONFIG_HOME/OKEGuiDX or ~/.config/OKEGuiDX.
  const posix = Config.configPaths("linux");
  assert.match(posix.file, /OKEGuiConfig\.json$/);
  assert.match(posix.file, /\.config\/OKEGuiDX/);
});

test("readError prefers the API's own wording", () => {
  // The shape is internal/api/respond.go: {"error":{summary,detail,field}}.
  const err = Config.readError(400, "Bad Request", {
    error: { summary: "日志级别不合法", detail: "logLevel 只能是 ...", field: "logLevel" },
  });
  assert.equal(err.summary, "日志级别不合法");
  assert.equal(err.detail, "logLevel 只能是 ...");
  assert.equal(err.field, "logLevel");
  assert.match(err.message, /日志级别不合法/);
  assert.match(err.message, /logLevel 只能是/);
});

test("readError survives a body that is not the error object", () => {
  // A proxy or a panic can answer with something else; the status text is the
  // fallback so the operator still sees something.
  const err = Config.readError(500, "Internal Server Error", null);
  assert.equal(err.summary, "Internal Server Error");
  assert.equal(err.detail, "");
  assert.equal(err.field, "");

  const empty = Config.readError(500, "", {});
  assert.equal(empty.summary, "HTTP 500");
});

test("the panel talks to the frozen endpoints", () => {
  const src = require("node:fs").readFileSync(require("node:path").join(__dirname, "..", "static", "config.js"), "utf8");
  assert.match(src, /const API = "\/api\/v1"/);
  assert.match(src, /API \+ "\/config"/);
  assert.match(src, /API \+ "\/status"/);
  assert.match(src, /method: "PUT"/);
});
