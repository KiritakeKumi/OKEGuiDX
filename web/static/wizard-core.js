// wizard-core.js is the pure half of the new-task wizard: profile parsing and
// validation, the episode-config checks, and the guard that refuses a .vpy
// template without the #OKE:INPUTFILE tag.
//
// It is the Web UI counterpart of Task/AddTaskService.cs and
// Task/AddEpProfileService.cs. The #OKE tag rewriting and the working/output
// path derivation live in internal/wizard (wizard.go), which POST
// /api/v1/tasks/prepare and POST /api/v1/tasks both run server-side; keeping a
// second copy of that algorithm here is what the page used to do and no longer
// needs to, because the page now shows the server's own answer.
//
// The module works both as a browser classic script (it attaches OKEWizardCore to
// window) and under Node's test runner (it fills module.exports), which is what
// lets web/js_test/wizard-core.test.js exercise it without a build step.

"use strict";

// ---------------------------------------------------------------------------
// Tolerant profile parsing
// ---------------------------------------------------------------------------

// stripTrailingCommas mirrors internal/profile/stripTrailingCommas: real profiles
// are hand-written and every shipped example has a trailing comma before a
// closing bracket. The scan never touches string literals.
function stripTrailingCommas(raw) {
  const out = [];
  let inString = false;
  let escaped = false;
  for (let i = 0; i < raw.length; i++) {
    const c = raw[i];
    if (inString) {
      out.push(c);
      if (escaped) {
        escaped = false;
      } else if (c === "\\") {
        escaped = true;
      } else if (c === '"') {
        inString = false;
      }
      continue;
    }
    if (c === '"') {
      inString = true;
      out.push(c);
      continue;
    }
    if (c === "," && nextNonSpaceCloses(raw, i + 1)) {
      continue;
    }
    out.push(c);
  }
  return out.join("");
}

// nextNonSpaceCloses reports whether the first non-whitespace byte at or after
// start is a closing bracket or brace.
function nextNonSpaceCloses(raw, start) {
  for (let i = start; i < raw.length; i++) {
    const c = raw[i];
    if (c === " " || c === "\t" || c === "\r" || c === "\n") {
      continue;
    }
    return c === "]" || c === "}";
  }
  return false;
}

// DEPRECATED_OPTIONS must not appear anywhere in a profile. Mirrors
// profile.DeprecatedOptions / Constants.deprecatedOptions.
const DEPRECATED_OPTIONS = ["SkipMuxing", "IncludeSub", "SubtitleLanguage"];

// foldASCII lowercases only the ASCII letters A-Z and leaves every other code
// point untouched, exactly as .NET's StringComparison.OrdinalIgnoreCase does for
// an ASCII needle. It is deliberately not String.prototype.toLowerCase: that
// folds U+212A KELVIN SIGN to "k", which would make the page disagree with the
// server.
function foldASCII(text) {
  let out = "";
  for (let i = 0; i < text.length; i++) {
    const c = text.charCodeAt(i);
    out += c >= 0x41 && c <= 0x5a ? String.fromCharCode(c + 0x20) : text[i];
  }
  return out;
}

// deprecatedOptionFound returns the first deprecated option present in the raw
// text, or "".
//
// The comparison reproduces the legacy AddTaskService.LoadJsonAsProfile check
// and the Go port (profile.DeprecatedOptionFound) character for character:
//
//	if (profileStr.IndexOf(option, StringComparison.OrdinalIgnoreCase) >= 0)
//
// StringComparison.OrdinalIgnoreCase folds only the ASCII letters A-Z; it does
// NOT fold U+212A KELVIN SIGN, U+017F LATIN SMALL LETTER LONG S, U+0131
// DOTLESS I, U+0130 I WITH DOT ABOVE or any other non-ASCII code point. An
// earlier version lowercased the whole text with toLowerCase, which folded
// KELVIN to "k" and rejected a profile containing "S\u212aIPMUXING" that the
// C# accepted - the wrong direction for a frozen format. Because the option
// names are pure ASCII, foldASCII matches the C# for every input, and the page
// and the server can never disagree.
function deprecatedOptionFound(raw) {
  const folded = foldASCII(String(raw));
  for (const opt of DEPRECATED_OPTIONS) {
    if (folded.includes(foldASCII(opt))) {
      return opt;
    }
  }
  return "";
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

// ValidationError mirrors profile.ValidationError: a stable title, an
// explanation, and the offending field, which the UI highlights. The REST API
// returns the same triple in {"error": {...}}, so a server rejection and a local
// one are displayed identically.
class ValidationError extends Error {
  constructor(summary, detail, field) {
    super(detail ? summary + ": " + detail : summary);
    this.name = "ValidationError";
    this.summary = summary || "出错了";
    this.detail = detail || "";
    this.field = field || "";
  }
}

// errorFromBody turns a decoded {"error": {...}} payload into a ValidationError.
function errorFromBody(payload, fallback) {
  const info = (payload && payload.error) || {};
  return new ValidationError(
    info.summary || fallback || "请求失败",
    info.detail || "",
    info.field || ""
  );
}

// ---------------------------------------------------------------------------
// Path handling
// ---------------------------------------------------------------------------

// pathStyle reports the separator convention a base directory implies. The
// wizard has to emit native-looking absolute paths because the server stores the
// three derived fields verbatim (internal/api/tasks.go applyPaths), so the
// operator's own base directory is the best signal available.
function pathStyle(reference) {
  const text = String(reference || "");
  // A UNC share spelled "//server/share" is a Windows path too: the legacy
  // FileInfo.FullName and the Go server's filepath.Clean both render it with
  // backslashes, so treating it as POSIX would make the preview disagree with
  // the path the task stores.
  const windows =
    /^[A-Za-z]:[\\/]/.test(text) || text.includes("\\") || text.startsWith("//");
  return { sep: windows ? "\\" : "/", windows };
}

// isAbsolute recognizes the forms a profile can contain: a Windows drive path, a
// UNC share and a POSIX path.
function isAbsolute(path) {
  const text = String(path || "");
  return /^[A-Za-z]:[\\/]/.test(text) || text.startsWith("/") || text.startsWith("\\\\");
}

// joinPath joins with the separator of the left-hand side, so a Windows base
// directory stays Windows-shaped.
function joinPath(dir, rel) {
  const { sep } = pathStyle(dir);
  return String(dir).replace(/[\\/]+$/, "") + sep + rel;
}

// normalizePath collapses "." and ".." segments and doubled separators,
// preserving the leading root and the separator convention of its input.
// Mirrors filepath.Clean plus the legacy Path.GetFullPath behaviour on the parts
// that matter here. A UNC prefix ("\\server") is preserved: collapsing it would
// turn a network path into a drive-relative one. A forward-slash share
// ("//server/share") is the same thing — .NET renders it with backslashes — so
// it is recognized too.
function normalizePath(path) {
  const text = String(path || "");
  const { sep } = pathStyle(text);
  const unc = text.startsWith("\\\\") || text.startsWith("//");
  const unix = text.replace(/\\/g, "/");
  const rooted = unix.startsWith("/");
  const segments = [];
  for (const part of unix.split("/")) {
    if (part === "" || part === ".") {
      continue;
    }
    if (part === ".." && segments.length > 0 && segments[segments.length - 1] !== "..") {
      segments.pop();
      continue;
    }
    segments.push(part);
  }
  const joined = segments.join(sep);
  if (unc) {
    return sep + sep + joined;
  }
  if (rooted) {
    return sep + joined;
  }
  return joined;
}

// baseName returns the final element of a path. Mirrors FileInfo.Name.
function baseName(path) {
  const unix = String(path).replace(/\\/g, "/").replace(/\/+$/, "");
  const i = unix.lastIndexOf("/");
  return i < 0 ? unix : unix.slice(i + 1);
}

// fileExt returns the extension including the dot, lowercased. It mirrors
// Path.GetExtension(...).ToLower().
//
// Path.GetExtension scans back to the last dot in the file name, so a name
// whose only dot is its first character is entirely an extension (".mkv" is
// ".mkv", not ""), while a dot that is the last character is not an extension
// ("trailing." has none). Both rules come straight from the .NET
// implementation, and fileExt("x.mkv") and fileExt(".mkv") must both answer
// ".mkv" for the re-encode check below to agree with the legacy code.
function fileExt(name) {
  const base = baseName(name);
  const i = base.lastIndexOf(".");
  if (i < 0 || i === base.length - 1) {
    return "";
  }
  return base.slice(i).toLowerCase();
}

// resolveRelative mirrors api.resolveRelative / PathUtils.GetFullPath: an
// absolute path is kept as-is, a relative one is joined onto the profile's
// directory.
function resolveRelative(rel, dir) {
  const path = String(rel || "");
  if (!path) {
    return "";
  }
  if (isAbsolute(path)) {
    return normalizePath(path);
  }
  if (!dir) {
    return normalizePath(path);
  }
  return normalizePath(joinPath(dir, path));
}

// ---------------------------------------------------------------------------
// Profile validation (AddTaskService.ProcessJsonProfile + LoadInputFiles)
// ---------------------------------------------------------------------------

// KNOWN_FPS maps the friendly frame rates to exact rationals. Mirrors
// profile.KnownFps and the switch in AddTaskService.ProcessJsonProfile.
const KNOWN_FPS = [
  [1.0, 1, 1],
  [23.976, 24000, 1001],
  [24.0, 24, 1],
  [25.0, 25, 1],
  [29.97, 30000, 1001],
  [30.0, 30, 1],
  [50.0, 50, 1],
  [59.94, 60000, 1001],
  [60.0, 60, 1],
];

// QAAC quality bounds, mirroring profile.QualityMin/Max.
const QUALITY_MIN = 0;
const QUALITY_MAX = 127;

// DEFAULT_BITRATE mirrors profile.DefaultBitrate / Constants.QAACBitrate.
const DEFAULT_BITRATE = 192;

// normalizeFps fills FpsNum/FpsDen from Fps, or the other way round. Mirrors
// profile.normalizeFps.
function normalizeFps(prof) {
  const fps = Number(prof.Fps) || 0;
  const num = Number(prof.FpsNum) || 0;
  const den = Number(prof.FpsDen) || 0;
  if (fps <= 0 && (num <= 0 || den <= 0)) {
    if (prof.TimeCode) {
      prof.Fps = 1;
    } else {
      throw new ValidationError(
        "帧率没有指定诶",
        "现在json文件中需要指定帧率，哪怕 Fps : 23.976",
        "Fps"
      );
    }
  }
  if (num > 0 && den > 0) {
    prof.FpsNum = num;
    prof.FpsDen = den;
    prof.Fps = num / den;
    return;
  }
  const known = KNOWN_FPS.find((k) => k[0] === prof.Fps);
  if (!known) {
    throw new ValidationError(
      "不知道的帧率诶",
      `请通过FpsNum和FpsDen来指定（当前 Fps=${prof.Fps}）`,
      "Fps"
    );
  }
  prof.FpsNum = known[1];
  prof.FpsDen = known[2];
}

// resolveInputFiles mirrors AddTaskService.LoadInputFiles: the list is resolved
// against the profile's directory and a duplicate stops it. Existence cannot be
// checked from a browser; the server re-runs that check and reports it with the
// legacy wording.
//
// An empty entry is rejected here, which is what api.selectInput does: the
// legacy code let Path.Combine produce the directory itself and then failed the
// existence check, so the operator never got past it either.
function resolveInputFiles(prof, baseDir) {
  const files = Array.isArray(prof.InputFiles) ? prof.InputFiles : [];
  const resolved = [];
  const seen = new Set();
  for (const file of files) {
    if (String(file).trim() === "") {
      throw new ValidationError("输入文件不合法", "输入文件路径为空。", "InputFiles");
    }
    const full = resolveRelative(file, baseDir);
    if (seen.has(full)) {
      throw new ValidationError(
        "输入文件有重复",
        `指定的文件(${full})重复了，请总监复查下输入文件列表？`,
        "InputFiles"
      );
    }
    seen.add(full);
    resolved.push(full);
  }
  return resolved;
}

// validateProfile applies every check AddTaskService.ProcessJsonProfile ran, in
// the same order, and mutates the profile the way the legacy code did
// (lowercased EncoderType, upper-cased ContainerFormat, derived
// VideoFormat/AudioFormat/FpsNum/FpsDen). It returns the resolved input paths.
//
// The checks that need the filesystem — does the script exist, does the encoder
// exist, do the inputs exist, what VapourSynth version is installed — are
// deliberately absent: a browser cannot stat a path. POST /api/v1/tasks re-runs
// the full validation (profile.Validate) and answers 400 with the same
// summary/detail/field triple, so nothing is silently accepted.
function validateProfile(prof, baseDir) {
  if (!prof || typeof prof !== "object") {
    throw new ValidationError("json文件写错了诶", "profile 不是一个 JSON 对象。", "");
  }

  if (Number(prof.Version) !== 2 && Number(prof.Version) !== 3) {
    throw new ValidationError(
      "版本不对",
      "你是不是把单个文件追加用的json当成一套任务用的json了？",
      "Version"
    );
  }
  if (Number(prof.Version) >= 3 && !prof.VSVersion) {
    throw new ValidationError(
      "JSON错误",
      "v3版的 JSON 中没有指定VS版本（VSVersion），请联系总监修改。",
      "VSVersion"
    );
  }

  prof.EncoderType = String(prof.EncoderType || "").toLowerCase();
  // A Map, not an object literal: a bare object inherits Object.prototype, so
  // EncoderType "constructor" or "__proto__" would look like a known encoder
  // (their inherited values are truthy) and pass validation with a nonsense
  // VideoFormat, where profile.Validate rejects anything but the three names.
  const videoFormat = new Map([
    ["x264", "AVC"],
    ["x265", "HEVC"],
    ["svtav1", "AV1"],
  ]).get(prof.EncoderType);
  if (!videoFormat) {
    throw new ValidationError("编码器版本错误", "EncoderType请填写x264/x265/svtav1", "EncoderType");
  }
  prof.VideoFormat = videoFormat;

  prof.ContainerFormat = String(prof.ContainerFormat || "").toUpperCase();
  if (prof.ContainerFormat !== "MKV" && prof.ContainerFormat !== "MP4") {
    throw new ValidationError("封装格式指定的有问题", "MKV/MP4，只能这两种", "ContainerFormat");
  }

  if (prof.ContainerFormat === "MP4" && prof.TimeCode) {
    throw new ValidationError("MP4暂不支持VFR封装", "MP4暂不支持VFR封装，请联系技术总监。", "TimeCode");
  }

  normalizeFps(prof);
  validateAudio(prof);

  return resolveInputFiles(prof, baseDir);
}

// validateAudio mirrors the audio half of ProcessJsonProfile, including the
// derived AudioFormat and the two cross-checks against the container.
function validateAudio(prof) {
  const tracks = Array.isArray(prof.AudioTracks) ? prof.AudioTracks : [];
  if (tracks.length > 0) {
    for (let i = 0; i < tracks.length; i++) {
      const track = tracks[i] || {};
      if (String(track.MuxOption || "").toLowerCase() !== "skip" && !String(track.OutputCodec || "").trim()) {
        throw new ValidationError(
          "音轨编码错误",
          "音轨未设置 OutputCodec，请检查大小写",
          `AudioTracks[${i}].OutputCodec`
        );
      }
      if (track.Quality !== null && track.Quality !== undefined) {
        const bitrate = Number(track.Bitrate) || 0;
        if (bitrate !== 0 && bitrate !== DEFAULT_BITRATE) {
          throw new ValidationError(
            "音轨编码错误",
            "音轨不能同时指定 Bitrate 和 Quality，请只保留其中一个",
            `AudioTracks[${i}]`
          );
        }
        const quality = Number(track.Quality);
        if (!(quality >= QUALITY_MIN && quality <= QUALITY_MAX)) {
          throw new ValidationError(
            "音轨编码错误",
            `音轨 Quality 的值必须介于 ${QUALITY_MIN}-${QUALITY_MAX} 之间（闭区间），请检查`,
            `AudioTracks[${i}].Quality`
          );
        }
      }
    }
    prof.AudioFormat = String(tracks[0].OutputCodec || "").toUpperCase() || "AAC";
  } else {
    prof.AudioFormat = "AAC";
  }

  if (!["FLAC", "AAC", "AC3", "DTS", "EAC3"].includes(prof.AudioFormat)) {
    throw new ValidationError(
      "音轨格式不支持",
      `目标音轨只能是FLAC/AAC/AC3/DTS/EAC3（当前 ${prof.AudioFormat}）`,
      "AudioTracks[0].OutputCodec"
    );
  }
  if (prof.AudioFormat === "FLAC" && prof.ContainerFormat === "MP4") {
    throw new ValidationError("音轨格式不支持", "MP4格式没法封FLAC", "AudioTracks[0].OutputCodec");
  }
}

// ---------------------------------------------------------------------------
// #OKE tag detection (AddTaskService.LoadVsScript)
// ---------------------------------------------------------------------------

// TAG_INPUT mirrors profile.InputTagPattern, which in turn mirrors
// Constants.inputRegex. The wizard used to rewrite this tag's value into every
// generated script; that rewriting now runs server-side in internal/wizard
// (buildVpy), so the only thing left for the page is the check LoadVsScript ran
// before the wizard could advance: a template without the tag cannot be turned
// into a per-source script at all.
//
// The class is spelled [ \t\r\n\f] rather than \s on purpose: JavaScript's \s
// also matches U+00A0, U+2028, U+2029 and U+3000, none of which .NET's \s or
// Go's RE2 \s match. A stray non-breaking space after the tag would otherwise
// let the page accept a template the server's profile.Validate rejects.
const TAG_INPUT = /^# *OKE:INPUTFILE([ \t\r\n\f]+\w+[ ]*=[ ]*)(r*["'].*["'])/gim;

// hasInputTag reports whether a script carries the tag at all, which is what
// LoadVsScript checked before the wizard could advance. The pattern is global,
// so lastIndex is reset to keep repeated calls from resuming mid-string.
function hasInputTag(script) {
  TAG_INPUT.lastIndex = 0;
  return TAG_INPUT.test(String(script));
}

// ---------------------------------------------------------------------------
// Episode config (AddEpProfileService.ProcessJsonProfile)
// ---------------------------------------------------------------------------

// sliceBegin/End accept both the profile's PascalCase spelling and the Go
// struct's lowercase one; encoding/json matches either.
function sliceBegin(slice) {
  return Number(slice.Begin ?? slice.begin);
}

function sliceEnd(slice) {
  return Number(slice.End ?? slice.end);
}

// sliceString mirrors model.SliceInfo.String, which is what the legacy
// `切片{s}不合法` message interpolated.
function sliceString(slice) {
  return "[" + sliceBegin(slice) + ", " + sliceEnd(slice) + "]";
}

// validateEpisodeConfig applies AddEpProfileService.ProcessJsonProfile to a
// per-source config, in the legacy order, and returns it with the slice array
// sorted and merged.
//
// Two legacy checks need the filesystem — does ReEncodeOldFile exist, and is the
// profile's container MKV — and only the second is reproducible here; the first
// is re-checked by the server when the task is added.
function validateEpisodeConfig(cfg) {
  const out = cfg || {};
  if (!out.EnableReEncode) {
    out.EnableReEncode = false;
    out.ReExtractSource = !!out.ReExtractSource;
    out.ReEncodeOldFile = out.ReEncodeOldFile || "";
    out.ReEncodeSliceArray = Array.isArray(out.ReEncodeSliceArray) ? out.ReEncodeSliceArray : [];
    return out;
  }

  if (!out.ReEncodeOldFile) {
    throw new ValidationError(
      "未指定旧版压制成品",
      "ReEncode项目必须指定旧版压制成品",
      "ReEncodeOldFile"
    );
  }
  // C# also required the file to exist; the browser cannot check that.
  if (!out.ReExtractSource && fileExt(out.ReEncodeOldFile) !== ".mkv") {
    throw new ValidationError(
      "旧版压制成品格式不支持",
      "需要从旧版压制成品获取非视频轨道，但旧版压制成品不为mkv格式",
      "ReEncodeOldFile"
    );
  }
  if (!Array.isArray(out.ReEncodeSliceArray) || out.ReEncodeSliceArray.length === 0) {
    throw new ValidationError(
      "未指定切片序列",
      "ReEncode项目必须指定需要重压的切片序列",
      "ReEncodeSliceArray"
    );
  }

  const slices = out.ReEncodeSliceArray.map((slice) => {
    const begin = sliceBegin(slice);
    const end = sliceEnd(slice);
    if (!Number.isInteger(begin) || begin < 0) {
      throw new ValidationError("切片不合法", `切片${sliceString(slice)}不合法`, "ReEncodeSliceArray");
    }
    // End == -1 is model.OpenEnded, "to the end of the video".
    if (end !== -1 && (!Number.isInteger(end) || end <= begin)) {
      throw new ValidationError("切片不合法", `切片${sliceString(slice)}不合法`, "ReEncodeSliceArray");
    }
    return { Begin: begin, End: end };
  });
  slices.sort((a, b) => a.Begin - b.Begin);

  out.ReEncodeSliceArray = mergeSlices(slices);
  return out;
}

// mergeSlices mirrors model.CheckAndMerge: contiguous slices are merged, an
// overlap is refused. The input must already be sorted by Begin.
function mergeSlices(slices) {
  if (slices.length === 0) {
    return slices;
  }
  const OPEN = Number.MAX_SAFE_INTEGER;
  const work = slices.map((s) => ({ Begin: s.Begin, End: s.End === -1 ? OPEN : s.End }));
  const out = [work[0]];
  for (let i = 1; i < work.length; i++) {
    const prev = out[out.length - 1];
    if (work[i].Begin < prev.End) {
      throw new ValidationError("切片重叠", "切片之间有重叠，请总监复查。", "ReEncodeSliceArray");
    }
    if (work[i].Begin === prev.End) {
      prev.End = work[i].End;
      continue;
    }
    out.push(work[i]);
  }
  return out.map((s) => ({ Begin: s.Begin, End: s.End === OPEN ? -1 : s.End }));
}

// parseSlices reads the slice array from the editor. The profile's own format is
// the JSON array
//
//	[ { "Begin": 100, "End": 200 }, { "Begin": 200, "End": -1 } ]
//
// and a bare "begin end" pair per line is accepted as the shorthand an operator
// writes when adding one slice by hand.
function parseSlices(text) {
  const trimmed = String(text || "").trim();
  if (!trimmed) {
    return [];
  }
  if (trimmed.startsWith("[")) {
    let parsed;
    try {
      parsed = JSON.parse(stripTrailingCommas(trimmed));
    } catch (err) {
      throw new ValidationError("切片不合法", "切片序列不是合法的 JSON：" + err.message, "ReEncodeSliceArray");
    }
    if (!Array.isArray(parsed)) {
      throw new ValidationError("切片不合法", "切片序列必须是一个数组。", "ReEncodeSliceArray");
    }
    return parsed;
  }

  const slices = [];
  for (const line of trimmed.split(/\r?\n/)) {
    const entry = line.trim();
    if (!entry) {
      continue;
    }
    const parts = entry.split(/[\s,]+/).filter((s) => s !== "");
    if (parts.length !== 2) {
      throw new ValidationError(
        "切片不合法",
        `切片行 "${entry}" 不是 "begin end" 两列。`,
        "ReEncodeSliceArray"
      );
    }
    slices.push({ Begin: Number(parts[0]), End: Number(parts[1]) });
  }
  return slices;
}

// ---------------------------------------------------------------------------
// Exports
// ---------------------------------------------------------------------------

const OKEWizardCore = {
  // parsing
  stripTrailingCommas,
  nextNonSpaceCloses,
  DEPRECATED_OPTIONS,
  deprecatedOptionFound,
  foldASCII,
  // errors
  ValidationError,
  errorFromBody,
  // paths
  pathStyle,
  isAbsolute,
  joinPath,
  normalizePath,
  baseName,
  fileExt,
  resolveRelative,
  // profile validation
  KNOWN_FPS,
  DEFAULT_BITRATE,
  normalizeFps,
  validateProfile,
  validateAudio,
  resolveInputFiles,
  // tags
  TAG_INPUT,
  hasInputTag,
  // episode config
  validateEpisodeConfig,
  mergeSlices,
  parseSlices,
  sliceString,
};

if (typeof window !== "undefined") {
  window.OKEWizardCore = OKEWizardCore;
}
if (typeof module !== "undefined" && module.exports) {
  module.exports = OKEWizardCore;
}
