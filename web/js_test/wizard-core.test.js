// wizard-core.test.js pins the pure half of the new-task wizard.
//
// It runs under `node --test`, with no dependencies and no build step, which is
// the only test runner available to a project that must not add a front-end
// toolchain. The fixtures are derived from the C# specification
// (Gui/WizardWindow.xaml.cs, Task/AddTaskService.cs, Task/AddEpProfileService.cs)
// and from the frozen profile format, not invented.

"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const Core = require("../static/wizard-core.js");

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// profileText is a profile in the frozen format, with the trailing comma every
// shipped example contains.
const profileText = `{
    "Version" : 3,
    "VSVersion" : "2024H1",
    "ProjectName" : "ep01",
    "EncoderType" : "x265",
    "EncoderParam" : "--crf 16 --preset slow --extra-long-parameter-that-is-truncated",
    "ContainerFormat" : "mkv",
    "AudioTracks" : [{
        "OutputCodec" : "flac",
    }],
    "InputScript" : "demo.vpy",
    "InputFiles" : [
        "00001.m2ts",
        "00002.m2ts",
    ],
    "Fps" : 23.976,
}
`;

// vpyTemplate is the shape a real script has: the tag on its own line, and the
// value the wizard replaces in group 2.
const vpyTemplate = `#OKE:INPROJECTDIR
#OKE:PROJECTDIR arg=r""
#OKE:DEBUG flag=Debug
#OKE:INPUTFILE arg=r""
clip = core.lsmas.LWLibavSource(arg)
clip.set_output()
`;

function loadProfile(text) {
  return JSON.parse(Core.stripTrailingCommas(text));
}

// ---------------------------------------------------------------------------
// Tolerant parsing
// ---------------------------------------------------------------------------

test("stripTrailingCommas removes trailing commas outside strings", () => {
  const cases = [
    { name: "object and array", in: '{"a":1,"b":[1,2,],}', want: '{"a":1,"b":[1,2]}' },
    { name: "no trailing comma", in: '{"a":1}', want: '{"a":1}' },
    { name: "comma inside a string", in: '{"a":"x,]","b":2}', want: '{"a":"x,]","b":2}' },
    { name: "escaped quote", in: '{"a":"x\\",]","b":2}', want: '{"a":"x\\",]","b":2}' },
  ];
  for (const tc of cases) {
    assert.equal(Core.stripTrailingCommas(tc.in), tc.want, tc.name);
  }
});

test("stripTrailingCommas keeps the fixture parseable", () => {
  const prof = loadProfile(profileText);
  assert.equal(prof.ProjectName, "ep01");
  assert.equal(prof.InputFiles.length, 2);
});

test("deprecatedOptionFound finds the legacy options", () => {
  assert.equal(Core.deprecatedOptionFound('{"SkipMuxing":true}'), "SkipMuxing");
  assert.equal(Core.deprecatedOptionFound('{"a":"skipmuxing"}'), "SkipMuxing");
  assert.equal(Core.deprecatedOptionFound('{"SubtitleLanguage":"jpn"}'), "SubtitleLanguage");
  assert.equal(Core.deprecatedOptionFound('{"ProjectName":"ep01"}'), "");
});

// ---------------------------------------------------------------------------
// Profile validation (AddTaskService.ProcessJsonProfile)
// ---------------------------------------------------------------------------

test("validateProfile accepts the fixture and derives the legacy fields", () => {
  const prof = loadProfile(profileText);
  const inputs = Core.validateProfile(prof, "D:\\proj\\ep01");

  assert.equal(prof.VideoFormat, "HEVC");
  assert.equal(prof.ContainerFormat, "MKV");
  assert.equal(prof.AudioFormat, "FLAC");
  assert.equal(prof.FpsNum, 24000);
  assert.equal(prof.FpsDen, 1001);
  assert.deepEqual(inputs, ["D:\\proj\\ep01\\00001.m2ts", "D:\\proj\\ep01\\00002.m2ts"]);
});

test("validateProfile lowercases the encoder and uppercases the container", () => {
  const prof = loadProfile(profileText);
  prof.EncoderType = "X265";
  prof.ContainerFormat = "mkv";
  Core.validateProfile(prof, "");
  assert.equal(prof.EncoderType, "x265");
  assert.equal(prof.ContainerFormat, "MKV");
});

// The table below mirrors the MessageBox calls of ProcessJsonProfile one for
// one: each case is a profile the legacy code refused, and the expected triple
// is what profile.ValidationError carries over the API.
test("validateProfile rejects what ProcessJsonProfile rejected", () => {
  const cases = [
    {
      name: "version 1",
      patch: (p) => (p.Version = 1),
      field: "Version",
      summary: "版本不对",
    },
    {
      name: "v3 without VSVersion",
      patch: (p) => (p.VSVersion = ""),
      field: "VSVersion",
      summary: "JSON错误",
    },
    {
      name: "unknown encoder",
      patch: (p) => (p.EncoderType = "xvid"),
      field: "EncoderType",
      summary: "编码器版本错误",
    },
    {
      name: "unknown container",
      patch: (p) => (p.ContainerFormat = "avi"),
      field: "ContainerFormat",
      summary: "封装格式指定的有问题",
    },
    {
      name: "mp4 with a timecode",
      patch: (p) => {
        p.ContainerFormat = "mp4";
        p.TimeCode = true;
      },
      field: "TimeCode",
      summary: "MP4暂不支持VFR封装",
    },
    {
      name: "no frame rate",
      patch: (p) => {
        p.Fps = 0;
        p.FpsNum = 0;
        p.FpsDen = 0;
      },
      field: "Fps",
      summary: "帧率没有指定诶",
    },
    {
      name: "unknown frame rate",
      patch: (p) => (p.Fps = 31.5),
      field: "Fps",
      summary: "不知道的帧率诶",
    },
    {
      name: "audio track without a codec",
      patch: (p) => (p.AudioTracks = [{ OutputCodec: "" }]),
      field: "AudioTracks[0].OutputCodec",
      summary: "音轨编码错误",
    },
    {
      name: "bitrate and quality together",
      patch: (p) => (p.AudioTracks = [{ OutputCodec: "aac", Bitrate: 320, Quality: 90 }]),
      field: "AudioTracks[0]",
      summary: "音轨编码错误",
    },
    {
      name: "quality out of range",
      patch: (p) => (p.AudioTracks = [{ OutputCodec: "aac", Quality: 200 }]),
      field: "AudioTracks[0].Quality",
      summary: "音轨编码错误",
    },
    {
      name: "unsupported audio codec",
      patch: (p) => (p.AudioTracks = [{ OutputCodec: "opus" }]),
      field: "AudioTracks[0].OutputCodec",
      summary: "音轨格式不支持",
    },
    {
      name: "flac inside mp4",
      patch: (p) => {
        p.ContainerFormat = "mp4";
        p.AudioTracks = [{ OutputCodec: "flac" }];
      },
      field: "AudioTracks[0].OutputCodec",
      summary: "音轨格式不支持",
    },
    {
      name: "duplicate input",
      patch: (p) => (p.InputFiles = ["00001.m2ts", "00001.m2ts"]),
      field: "InputFiles",
      summary: "输入文件有重复",
    },
  ];

  for (const tc of cases) {
    const prof = loadProfile(profileText);
    tc.patch(prof);
    assert.throws(
      () => Core.validateProfile(prof, "D:\\proj"),
      (err) => {
        assert.ok(err instanceof Core.ValidationError, tc.name + ": type");
        assert.equal(err.summary, tc.summary, tc.name + ": summary");
        assert.equal(err.field, tc.field, tc.name + ": field");
        assert.ok(err.detail.length > 0, tc.name + ": detail");
        return true;
      },
      tc.name
    );
  }
});

test("validateProfile accepts every known frame rate", () => {
  for (const [fps, num, den] of Core.KNOWN_FPS) {
    const prof = loadProfile(profileText);
    prof.Fps = fps;
    prof.FpsNum = 0;
    prof.FpsDen = 0;
    Core.validateProfile(prof, "");
    assert.equal(prof.FpsNum, num, "fps " + fps);
    assert.equal(prof.FpsDen, den, "fps " + fps);
  }
});

test("validateProfile keeps FpsNum/FpsDen when both are given", () => {
  const prof = loadProfile(profileText);
  prof.Fps = 0;
  prof.FpsNum = 30000;
  prof.FpsDen = 1001;
  Core.validateProfile(prof, "");
  assert.equal(prof.Fps, 30000 / 1001);
});

test("validateProfile defaults the audio format to AAC with no tracks", () => {
  const prof = loadProfile(profileText);
  delete prof.AudioTracks;
  Core.validateProfile(prof, "");
  assert.equal(prof.AudioFormat, "AAC");
});

test("validateProfile skips a skipped audio track", () => {
  const prof = loadProfile(profileText);
  prof.AudioTracks = [{ MuxOption: "Skip", OutputCodec: "" }];
  Core.validateProfile(prof, "");
  assert.equal(prof.AudioFormat, "AAC");
});

test("validateProfile accepts a VFR profile with no frame rate", () => {
  const prof = loadProfile(profileText);
  prof.TimeCode = true;
  prof.Fps = 0;
  prof.FpsNum = 0;
  prof.FpsDen = 0;
  Core.validateProfile(prof, "");
  assert.equal(prof.Fps, 1);
});

test("validateProfile resolves an absolute input against no directory", () => {
  const prof = loadProfile(profileText);
  prof.InputFiles = ["/srv/ep01/00001.m2ts"];
  assert.deepEqual(Core.validateProfile(prof, "D:\\proj"), ["/srv/ep01/00001.m2ts"]);
});

// ---------------------------------------------------------------------------
// #OKE tag detection (AddTaskService.LoadVsScript)
// ---------------------------------------------------------------------------
// The tag rewriting and the path derivation moved server-side (internal/wizard),
// so the page only keeps the guard LoadVsScript applied before the wizard could
// advance.

test("hasInputTag accepts the fixture and refuses a plain script", () => {
  assert.equal(Core.hasInputTag(vpyTemplate), true);
  assert.equal(Core.hasInputTag("# just a comment\nclip.set_output()\n"), false);
});

// ---------------------------------------------------------------------------
// Path helpers
// ---------------------------------------------------------------------------

test("resolveRelative keeps an absolute path and joins a relative one", () => {
  assert.equal(Core.resolveRelative("00001.m2ts", "D:\\proj"), "D:\\proj\\00001.m2ts");
  assert.equal(Core.resolveRelative("D:\\abs\\00001.m2ts", "D:\\proj"), "D:\\abs\\00001.m2ts");
  assert.equal(Core.resolveRelative("/abs/00001.m2ts", "D:\\proj"), "/abs/00001.m2ts");
  assert.equal(Core.resolveRelative("00001.m2ts", ""), "00001.m2ts");
  assert.equal(Core.resolveRelative("", "D:\\proj"), "");
});

test("normalizePath collapses dots and doubles", () => {
  const cases = [
    ["D:\\a\\.\\b", "D:\\a\\b"],
    ["D:\\a\\\\b", "D:\\a\\b"],
    ["/a/b/../c", "/a/c"],
    ["a/b", "a/b"],
  ];
  for (const [input, want] of cases) {
    assert.equal(Core.normalizePath(input), want, input);
  }
});

test("baseName splits a path the way FileInfo.Name did", () => {
  assert.equal(Core.baseName("D:\\proj\\00001.m2ts"), "00001.m2ts");
  assert.equal(Core.baseName("/srv/x.mkv"), "x.mkv");
});

test("fileExt ignores a leading dot", () => {
  assert.equal(Core.fileExt("00001.m2ts"), ".m2ts");
  assert.equal(Core.fileExt("ARCHIVE.MKV"), ".mkv");
  assert.equal(Core.fileExt(".gitignore"), "");
});

// ---------------------------------------------------------------------------
// Episode config (AddEpProfileService.ProcessJsonProfile)
// ---------------------------------------------------------------------------

test("validateEpisodeConfig passes a disabled config through", () => {
  const got = Core.validateEpisodeConfig({});
  assert.equal(got.EnableReEncode, false);
  assert.deepEqual(got.ReEncodeSliceArray, []);
});

test("validateEpisodeConfig rejects a re-encode without the old file", () => {
  assert.throws(
    () => Core.validateEpisodeConfig({ EnableReEncode: true }),
    (err) => err.summary === "未指定旧版压制成品" && err.field === "ReEncodeOldFile"
  );
});

test("validateEpisodeConfig rejects a non-mkv old file without ReExtractSource", () => {
  assert.throws(
    () =>
      Core.validateEpisodeConfig({
        EnableReEncode: true,
        ReEncodeOldFile: "old.mp4",
        ReEncodeSliceArray: [{ Begin: 0, End: 10 }],
      }),
    (err) => err.summary === "旧版压制成品格式不支持" && err.field === "ReEncodeOldFile"
  );
});

test("validateEpisodeConfig accepts a non-mkv old file with ReExtractSource", () => {
  const got = Core.validateEpisodeConfig({
    EnableReEncode: true,
    ReExtractSource: true,
    ReEncodeOldFile: "old.mp4",
    ReEncodeSliceArray: [{ Begin: 0, End: 10 }],
  });
  assert.equal(got.ReEncodeSliceArray.length, 1);
});

test("validateEpisodeConfig rejects an empty slice array", () => {
  assert.throws(
    () =>
      Core.validateEpisodeConfig({
        EnableReEncode: true,
        ReEncodeOldFile: "old.mkv",
        ReEncodeSliceArray: [],
      }),
    (err) => err.summary === "未指定切片序列" && err.field === "ReEncodeSliceArray"
  );
});

test("validateEpisodeConfig rejects an illegal slice", () => {
  const cases = [
    { Begin: -1, End: 10 },
    { Begin: 10, End: 10 },
    { Begin: 10, End: 5 },
    { Begin: 10, End: -5 },
  ];
  for (const slice of cases) {
    assert.throws(
      () =>
        Core.validateEpisodeConfig({
          EnableReEncode: true,
          ReEncodeOldFile: "old.mkv",
          ReEncodeSliceArray: [slice],
        }),
      (err) => err.summary === "切片不合法" && err.field === "ReEncodeSliceArray",
      JSON.stringify(slice)
    );
  }
});

test("validateEpisodeConfig sorts and merges contiguous slices", () => {
  // SliceInfoArray.Sorted then CheckAndMerge: [40,2400) and [2400,2900) are
  // contiguous, so they become one.
  const got = Core.validateEpisodeConfig({
    EnableReEncode: true,
    ReEncodeOldFile: "old.mkv",
    ReEncodeSliceArray: [
      { Begin: 2400, End: 2900 },
      { Begin: 40, End: 2400 },
    ],
  });
  assert.deepEqual(got.ReEncodeSliceArray, [{ Begin: 40, End: 2900 }]);
});

test("validateEpisodeConfig merges onto an open-ended slice", () => {
  const got = Core.validateEpisodeConfig({
    EnableReEncode: true,
    ReEncodeOldFile: "old.mkv",
    ReEncodeSliceArray: [
      { Begin: 100, End: 200 },
      { Begin: 200, End: -1 },
    ],
  });
  assert.deepEqual(got.ReEncodeSliceArray, [{ Begin: 100, End: -1 }]);
});

test("validateEpisodeConfig refuses overlapping slices", () => {
  assert.throws(
    () =>
      Core.validateEpisodeConfig({
        EnableReEncode: true,
        ReEncodeOldFile: "old.mkv",
        ReEncodeSliceArray: [
          { Begin: 100, End: 300 },
          { Begin: 200, End: 400 },
        ],
      }),
    (err) => err.summary === "切片重叠" && err.field === "ReEncodeSliceArray"
  );
});

test("validateEpisodeConfig accepts the lowercase field spelling", () => {
  // encoding/json matches Go struct tags case-insensitively; the wizard accepts
  // both spellings so a profile written either way works.
  const got = Core.validateEpisodeConfig({
    EnableReEncode: true,
    ReEncodeOldFile: "old.mkv",
    ReEncodeSliceArray: [{ begin: 0, end: 100 }],
  });
  assert.deepEqual(got.ReEncodeSliceArray, [{ Begin: 0, End: 100 }]);
});

test("parseSlices reads the profile's JSON array", () => {
  const got = Core.parseSlices('[ { "Begin": 100, "End": 200 }, { "Begin": 200, "End": -1 } ]');
  assert.deepEqual(got, [
    { Begin: 100, End: 200 },
    { Begin: 200, End: -1 },
  ]);
});

test("parseSlices tolerates trailing commas in the array", () => {
  const got = Core.parseSlices('[ { "Begin": 0, "End": 100 }, ]');
  assert.deepEqual(got, [{ Begin: 0, End: 100 }]);
});

test("parseSlices reads the begin/end shorthand", () => {
  const got = Core.parseSlices("0 100\n100 -1\n");
  assert.deepEqual(got, [
    { Begin: 0, End: 100 },
    { Begin: 100, End: -1 },
  ]);
});

test("parseSlices rejects a malformed line", () => {
  assert.throws(
    () => Core.parseSlices("0 100 200"),
    (err) => err.summary === "切片不合法" && err.field === "ReEncodeSliceArray"
  );
});

test("parseSlices returns an empty array for empty input", () => {
  assert.deepEqual(Core.parseSlices(""), []);
  assert.deepEqual(Core.parseSlices("   \n  "), []);
});

// ---------------------------------------------------------------------------
// Server error mapping
// ---------------------------------------------------------------------------

test("errorFromBody maps the API error object onto a ValidationError", () => {
  const err = Core.errorFromBody({
    error: { summary: "找不到输入文件啊", detail: "指定的文件(x)不存在啊", field: "InputFiles" },
  });
  assert.equal(err.summary, "找不到输入文件啊");
  assert.equal(err.detail, "指定的文件(x)不存在啊");
  assert.equal(err.field, "InputFiles");
});

test("errorFromBody falls back when the body is not the API shape", () => {
  const err = Core.errorFromBody(null, "HTTP 500");
  assert.equal(err.summary, "HTTP 500");
  assert.equal(err.field, "");
});
