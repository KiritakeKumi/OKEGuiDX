// wizard-core.js is the pure half of the new-task wizard: profile parsing and
// validation, the #OKE tag rewriting, and the working/output path derivation.
//
// It is the Web UI counterpart of Task/AddTaskService.cs, Task/AddEpProfileService.cs
// and the path block of Gui/WizardWindow.xaml.cs. The algorithm is the one
// DECISIONS-NEEDED.md §A5 records; internal/wizard (W2) implements the same
// algorithm server-side, and the two must agree, so nothing here touches the DOM
// or the network and every function is unit-testable on its own.
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

// deprecatedOptionFound returns the first deprecated option present in the raw
// text, or "".
function deprecatedOptionFound(raw) {
  const lower = String(raw).toLowerCase();
  for (const opt of DEPRECATED_OPTIONS) {
    if (lower.includes(opt.toLowerCase())) {
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
  const windows = /^[A-Za-z]:[\\/]/.test(text) || text.includes("\\");
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
// turn a network path into a drive-relative one.
function normalizePath(path) {
  const text = String(path || "");
  const { sep } = pathStyle(text);
  const unc = text.startsWith("\\\\");
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

// dirName returns the parent directory of a path, or "" when there is none.
// Mirrors Path.GetDirectoryName for the shapes a source path can have.
function dirName(path) {
  const { sep } = pathStyle(path);
  const unix = String(path).replace(/\\/g, "/").replace(/\/+$/, "");
  const i = unix.lastIndexOf("/");
  if (i < 0) {
    return "";
  }
  if (i === 0) {
    return sep;
  }
  return normalizePath(unix.slice(0, i));
}

// baseName returns the final element of a path. Mirrors FileInfo.Name.
function baseName(path) {
  const unix = String(path).replace(/\\/g, "/").replace(/\/+$/, "");
  const i = unix.lastIndexOf("/");
  return i < 0 ? unix : unix.slice(i + 1);
}

// fileStem returns the file name without its extension.
function fileStem(name) {
  const base = baseName(name);
  const i = base.lastIndexOf(".");
  return i <= 0 ? base : base.slice(0, i);
}

// fileExt returns the extension including the dot, lowercased. Mirrors
// Path.GetExtension(...).ToLower().
function fileExt(name) {
  const base = baseName(name);
  const i = base.lastIndexOf(".");
  return i <= 0 ? "" : base.slice(i).toLowerCase();
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
function resolveInputFiles(prof, baseDir) {
  const files = Array.isArray(prof.InputFiles) ? prof.InputFiles : [];
  const resolved = [];
  const seen = new Set();
  for (const file of files) {
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
  const videoFormat = { x264: "AVC", x265: "HEVC", svtav1: "AV1" }[prof.EncoderType];
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
// #OKE tag rewriting (WizardWindow.WizardFinish, lines 202-270)
// ---------------------------------------------------------------------------

// The three tag patterns mirror profile.InputTagPattern / ProjectDirTagPattern /
// DebugTagPattern, which in turn mirror Constants.inputRegex and friends.
//
// The legacy code indexed Regex.Split's output as [0]=before, [1]=group1,
// [2]=group2, [3]=after, and every replacement kept group1 while replacing
// group2:
//
//	vsScript = dirTag[0] + dirTag[1] + "R\"" + projectDir + "\"" + dirTag[3]
//
// String.replace hands the callback the whole match followed by the capture
// groups, so the piece before group1 is recovered from the match's length; the
// result is the same expression with the two outer terms left in place.
const TAG_INPUT = /^# *OKE:INPUTFILE([\s]+\w+[ ]*=[ ]*)(r*["'].*["'])/gim;
const TAG_PROJECTDIR = /^# *OKE:PROJECTDIR([\s]+\w+[ ]*=[ ]*)(r*["'].*["'])/gim;
const TAG_DEBUG = /^# *OKE:DEBUG([\s]+[\w]+[ ]*=[ ]*)(\w+)/gim;

// replaceTagValue rewrites capture group2 of a tag match to value, keeping
// everything before and after it byte for byte.
function replaceTagValue(match, head, value, replacement) {
  const before = match.slice(0, match.length - head.length - value.length);
  return before + head + replacement;
}

// applyProjectDirTag rewrites the PROJECTDIR tag to the profile's directory,
// exactly as WizardFinish did. The tag is the only place the generated script
// learns where the project lives.
function applyProjectDirTag(script, projectDir) {
  return String(script).replace(TAG_PROJECTDIR, (match, head, value) =>
    replaceTagValue(match, head, value, 'R"' + projectDir + '"')
  );
}

// applyDebugTag rewrites the DEBUG tag's value to "None", which is what the
// wizard did before handing the script to the engine.
function applyDebugTag(script) {
  return String(script).replace(TAG_DEBUG, (match, head, value) =>
    replaceTagValue(match, head, value, "None")
  );
}

// generateVpy renders one source's script:
//
//	inputTemplate[0] + inputTemplate[1] + "R\"" + inputFile + "\"" + inputTemplate[3]
//
// The legacy code split on inputRegex once, outside the loop, and reused the
// template for every source; replacing the match in place gives the same result
// because only group2 changes.
function generateVpy(script, inputFile) {
  const text = String(script);
  if (!hasInputTag(text)) {
    throw new ValidationError(
      "vpy没有为OKEGui设计",
      "vpy里没有#OKE:INPUTFILE的标签。",
      "InputScript"
    );
  }
  return text.replace(TAG_INPUT, (match, head, value) =>
    replaceTagValue(match, head, value, 'R"' + inputFile + '"')
  );
}

// hasInputTag reports whether a script carries the tag at all, which is what
// LoadVsScript checked before the wizard could advance.
function hasInputTag(script) {
  TAG_INPUT.lastIndex = 0;
  return TAG_INPUT.test(String(script));
}

// ---------------------------------------------------------------------------
// Working / output path derivation (WizardWindow.WizardFinish, lines 268-321)
// ---------------------------------------------------------------------------

// STRIP_COMPONENTS is the legacy list of directory levels that carry no
// information. The C# code carried a "FIXME: do not hardcode this"; the value is
// kept because it is exactly what names every existing release's working
// directory, and changing it would rename them all.
const STRIP_COMPONENTS = "BDBOX/BDROM/BD/BDMV/STREAM/BD_VIDEO";

// VOLUME_PATTERN matches a trailing volume number. Mirrors the legacy
// `.*Vol[.\- ]?(?<vol>\d+).*`.
const VOLUME_PATTERN = /.*Vol[.\- ]?(\d+).*/i;

// CRC32_TABLE is the standard reflected CRC-32 table (polynomial 0xEDB88320),
// the same algorithm as the legacy CRC32/SafeProxy pair.
const CRC32_TABLE = (() => {
  const table = new Uint32Array(256);
  for (let i = 0; i < 256; i++) {
    let c = i;
    for (let k = 0; k < 8; k++) {
      c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    }
    table[i] = c >>> 0;
  }
  return table;
})();

// crc32 returns the CRC-32 of the UTF-8 bytes of text, mirroring
// CRC32.Compute(Encoding.UTF8.GetBytes(...)).
function crc32(text) {
  let crc = 0xffffffff;
  const bytes = typeof TextEncoder !== "undefined"
    ? new TextEncoder().encode(String(text))
    : Buffer.from(String(text), "utf8");
  for (const byte of bytes) {
    crc = CRC32_TABLE[(crc ^ byte) & 0xff] ^ (crc >>> 8);
  }
  return (crc ^ 0xffffffff) >>> 0;
}

// hex8 renders a CRC the way C# did with ToString("X8").
function hex8(value) {
  return (value >>> 0).toString(16).toUpperCase().padStart(8, "0");
}

// escapeRegExp mirrors Regex.Escape closely enough for a path component.
function escapeRegExp(text) {
  return String(text).replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

// stripCommonComponents removes the generic directory levels. Mirrors the
// per-component `Regex.Replace(path, @"[/\\]" + comp + @"[/\\]", "\\")` loop,
// including the way the replacement swallows the surrounding separators and the
// way a removal can expose a new match: "BDMV\BDMV\STREAM\x" loses both levels,
// because after the first is rewritten the second is surrounded by separators
// again.
//
// Both separators are matched, and the comparison ignores ASCII case: the strip
// list is written in the case a real BD tree uses, and a tree spelled the other
// way should still have its levels removed rather than silently kept.
function stripCommonComponents(inputSuffixPath, sep) {
  let out = inputSuffixPath;
  for (const comp of STRIP_COMPONENTS.split(/[\\/]/)) {
    if (!comp) {
      continue;
    }
    const needle = escapeRegExp(comp).replace(/[A-Za-z]/g, (c) => "[" + c + c.toLowerCase() + "]");
    out = out.replace(new RegExp("[/\\\\]" + needle + "[/\\\\]", "g"), sep);
  }
  return out;
}

// derivePaths reproduces the path block of WizardFinish for one source:
//
//  1. inputSuffixPath = inputFile with ':' replaced by '_';
//  2. the common path components are stripped;
//  3. when more than three components remain and reducePath is on, the middle
//     levels collapse into a CRC32-tagged directory;
//  4. working = projectDir + the result;
//  5. output  = working with a "/._/" (or "\_\" ) level replaced by "output";
//  6. the script is working + "-" + MMddHHmm + ".vpy".
//
// The C# code created both directories and wrote the script here; a browser can
// do neither, so this returns the paths plus the rendered script, and the caller
// decides what to do with them.
//
// reduceMap is the entry the legacy code appended to ReducePathMap.log, or null.
function derivePaths(inputFile, projectDir, reducePath) {
  const { sep } = pathStyle(projectDir);
  let inputSuffixPath = String(inputFile).replace(/:/g, "_");
  inputSuffixPath = stripCommonComponents(inputSuffixPath, sep);

  const components = inputSuffixPath.split(/[\\/]/).filter((c) => c !== "");
  let reduceMap = null;
  if (components.length > 3 && reducePath) {
    // components[0] is the drive, components[length-1] the file name, and
    // components[1 .. length-2] is the effective path to be reduced.
    const prefix = components.slice(1, components.length - 2).join(sep);
    const last = components[components.length - 2];
    let effective = last;
    if (!VOLUME_PATTERN.test(last)) {
      // The last level is preserved and the CRC of the prefix is prepended to
      // keep two same-named episodes apart.
      const crc = crc32(prefix);
      effective = hex8(crc) + "-" + last;
      reduceMap = { crc, prefix };
    }
    inputSuffixPath = [components[0], effective, components[components.length - 1]].join(sep);
  }

  const working = combineLikeDotNet(projectDir, inputSuffixPath);
  // Regex.Replace(newPath, @"[/\\]._[/\\]", "\\output\\"): a directory level
  // literally named "._" becomes "output".
  const output = normalizePath(
    working.replace(/[\\/]\._[\\/]/g, sep + "output" + sep)
  );
  return { working, output, reduceMap };
}

// combineLikeDotNet mirrors Path.Combine: a second argument that is rooted wins
// over the first. Go's filepath.Join — and a plain join — would instead append
// it, which would move a source tree that already lives outside the project
// directory underneath it. A UNC source or a source on a volume the strip list
// does not recognise keeps the legacy behaviour of working in place.
function combineLikeDotNet(dir, suffix) {
  if (isAbsolute(suffix)) {
    return normalizePath(suffix);
  }
  return normalizePath(joinPath(dir, suffix));
}

// timestamp renders the "MMddHHmm" suffix the legacy code appended to the
// generated script's name (DateTime.Now.ToString("MMddHHmm")).
function timestamp(now) {
  const at = now instanceof Date ? now : new Date();
  const pad = (v) => String(v).padStart(2, "0");
  return (
    pad(at.getMonth() + 1) + pad(at.getDate()) + pad(at.getHours()) + pad(at.getMinutes())
  );
}

// scriptPath is `working + "-" + MMddHHmm + ".vpy"`. The legacy code appended the
// suffix to the whole path, so the file name is "<source>-<stamp>.vpy".
function scriptPath(working, now) {
  return working + "-" + timestamp(now) + ".vpy";
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
  // errors
  ValidationError,
  errorFromBody,
  // paths
  pathStyle,
  isAbsolute,
  joinPath,
  normalizePath,
  dirName,
  baseName,
  fileStem,
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
  TAG_PROJECTDIR,
  TAG_DEBUG,
  applyProjectDirTag,
  applyDebugTag,
  generateVpy,
  hasInputTag,  // derived paths
  STRIP_COMPONENTS,
  crc32,
  hex8,
  stripCommonComponents,
  derivePaths,
  combineLikeDotNet,
  timestamp,
  scriptPath,
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
