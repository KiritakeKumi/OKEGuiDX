// Package okerr defines the structured error type used across OKEGuiDX.
//
// The legacy code called MessageBox.Show from deep inside the engine, which
// made the core impossible to run headless (INVENTORY.md §2 #16). Every failure
// is now a value that carries a short summary plus a detailed explanation, and
// the caller decides how to present it: the CLI writes stderr, the daemon
// returns JSON, the Web UI shows a dialog.
package okerr

import (
	"errors"
	"fmt"
	"strings"
)

// Kind classifies a failure so callers can react without string matching.
type Kind string

// Error kinds.
const (
	// KindUnknown is the fallback for unclassified failures.
	KindUnknown Kind = "unknown"
	// KindTool means an external tool exited with a failure code.
	KindTool Kind = "tool"
	// KindToolCrash means an external tool died without a meaningful exit code
	// (VapourSynth or an encoder crashing mid-run).
	KindToolCrash Kind = "tool_crash"
	// KindConfig means the profile or application configuration is wrong.
	KindConfig Kind = "config"
	// KindNotFound means a required file or tool is missing.
	KindNotFound Kind = "not_found"
	// KindUnsupported means the platform cannot perform the requested work.
	KindUnsupported Kind = "unsupported"
	// KindMismatch means the source does not match what the profile declared
	// (track counts, frame rate, frame count).
	KindMismatch Kind = "mismatch"
	// KindCanceled means the user stopped the task.
	KindCanceled Kind = "canceled"
	// KindIO means a filesystem operation failed.
	KindIO Kind = "io"
)

// Error is the structured error carried through the whole engine.
type Error struct {
	// Kind classifies the failure.
	Kind Kind `json:"kind"`
	// Summary is a short, stable title suitable for a dialog header or a
	// single-line log entry.
	Summary string `json:"summary"`
	// Detail explains the failure and what to do about it.
	Detail string `json:"detail"`
	// File is the input file the failure relates to, when applicable.
	File string `json:"file,omitempty"`
	// Tool names the external tool involved, when applicable.
	Tool string `json:"tool,omitempty"`
	// ExitCode is the tool's exit code, when applicable.
	ExitCode int `json:"exit_code,omitempty"`
	// Output holds the last lines of tool output, for troubleshooting.
	Output string `json:"output,omitempty"`
	// Err is the wrapped cause, if any. Not serialized.
	Err error `json:"-"`
}

// New builds an error with a summary and a formatted detail.
func New(kind Kind, summary, format string, args ...any) *Error {
	return &Error{Kind: kind, Summary: summary, Detail: fmt.Sprintf(format, args...)}
}

// Wrap adds a structured summary around an existing error.
func Wrap(err error, kind Kind, summary, format string, args ...any) *Error {
	return &Error{Kind: kind, Summary: summary, Detail: fmt.Sprintf(format, args...), Err: err}
}

// Error implements error.
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(e.Summary)
	if e.Detail != "" {
		b.WriteString(": ")
		b.WriteString(e.Detail)
	}
	if e.File != "" {
		fmt.Fprintf(&b, " [%s]", e.File)
	}
	return b.String()
}

// Unwrap exposes the wrapped cause to errors.Is / errors.As.
func (e *Error) Unwrap() error { return e.Err }

// WithFile returns a copy of the error annotated with the offending file.
func (e *Error) WithFile(file string) *Error {
	c := *e
	c.File = file
	return &c
}

// WithTool returns a copy of the error annotated with the failing tool and its
// exit code.
func (e *Error) WithTool(tool string, exitCode int) *Error {
	c := *e
	c.Tool = tool
	c.ExitCode = exitCode
	return &c
}

// WithOutput returns a copy of the error annotated with tool output.
func (e *Error) WithOutput(output string) *Error {
	c := *e
	c.Output = output
	return &c
}

// Is reports whether target matches this error's kind or summary. It lets
// callers write errors.Is(err, okerr.ErrUnsupportedAAC).
func (e *Error) Is(target error) bool {
	var other *Error
	if !errors.As(target, &other) {
		return false
	}
	if other.Kind != "" && other.Kind != e.Kind {
		return false
	}
	if other.Summary != "" && other.Summary != e.Summary {
		return false
	}
	return true
}

// AsError extracts the structured error from err, converting a plain error when
// necessary.
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return &Error{Kind: KindUnknown, Summary: "未知错误", Detail: err.Error(), Err: err}
}

// Sentinel errors used with errors.Is. The summaries match the legacy
// Constants.*Smr strings so that log output and user-facing text are unchanged.
var (
	// ErrEac3to mirrors Constants.eac3toErrorSmr.
	ErrEac3to = &Error{Kind: KindTool, Summary: "eac3to出错"}
	// ErrAudioNumMismatch mirrors Constants.audioNumMismatchSmr.
	ErrAudioNumMismatch = &Error{Kind: KindMismatch, Summary: "音轨数不一致"}
	// ErrSubNumMismatch mirrors Constants.subNumMismatchSmr.
	ErrSubNumMismatch = &Error{Kind: KindMismatch, Summary: "字幕数不一致"}
	// ErrFpsMismatch mirrors Constants.fpsMismatchSmr.
	ErrFpsMismatch = &Error{Kind: KindMismatch, Summary: "FPS不一致"}
	// ErrX264 mirrors Constants.x264ErrorSmr.
	ErrX264 = &Error{Kind: KindTool, Summary: "x264出错"}
	// ErrX265 mirrors Constants.x265ErrorSmr.
	ErrX265 = &Error{Kind: KindTool, Summary: "x265出错"}
	// ErrSVTAV1 mirrors Constants.svtav1ErrorSmr.
	ErrSVTAV1 = &Error{Kind: KindTool, Summary: "svt-av1出错"}
	// ErrVpy mirrors Constants.vpyErrorSmr.
	ErrVpy = &Error{Kind: KindTool, Summary: "vpy出错"}
	// ErrMkvmerge mirrors Constants.mmgErrorSmr.
	ErrMkvmerge = &Error{Kind: KindTool, Summary: "mkvmerge出错"}
	// ErrLSmash mirrors Constants.lsmashErrorSmr.
	ErrLSmash = &Error{Kind: KindTool, Summary: "l-smash出错"}
	// ErrVSCrash mirrors Constants.vsCrashSmr.
	ErrVSCrash = &Error{Kind: KindToolCrash, Summary: "vs崩溃"}
	// ErrX264Crash mirrors Constants.x264CrashSmr.
	ErrX264Crash = &Error{Kind: KindToolCrash, Summary: "x264崩溃"}
	// ErrX265Crash mirrors Constants.x265CrashSmr.
	ErrX265Crash = &Error{Kind: KindToolCrash, Summary: "x265崩溃"}
	// ErrSVTAV1Crash mirrors Constants.svtav1CrashSmr.
	ErrSVTAV1Crash = &Error{Kind: KindToolCrash, Summary: "svt-av1崩溃"}
	// ErrQAAC mirrors Constants.qaacErrorSmr.
	ErrQAAC = &Error{Kind: KindTool, Summary: "QAAC无法运行"}
	// ErrAudioFormatMismatch mirrors Constants.audioFormatMistachSmr.
	ErrAudioFormatMismatch = &Error{Kind: KindMismatch, Summary: "音轨格式不匹配"}
	// ErrRPC mirrors Constants.rpcErrorSmr.
	ErrRPC = &Error{Kind: KindTool, Summary: "RPC出错"}
	// ErrReEncodeSlice mirrors Constants.reEncodeSliceErrorSmr.
	ErrReEncodeSlice = &Error{Kind: KindMismatch, Summary: "切片不合法"}
	// ErrReEncodeFrames mirrors Constants.reEncodeFramesErrorSmr.
	ErrReEncodeFrames = &Error{Kind: KindMismatch, Summary: "帧数错误"}
	// ErrUnknown mirrors Constants.unknownErrorSmr.
	ErrUnknown = &Error{Kind: KindUnknown, Summary: "未知错误"}

	// ErrUnsupportedAAC is returned when an AAC encode is requested on a
	// platform without qaac. The project refuses to silently degrade quality
	// (PLAN.md §2.2).
	ErrUnsupportedAAC = &Error{
		Kind:    KindUnsupported,
		Summary: "该平台不支持AAC编码",
		Detail:  "非 Windows 平台没有 qaac（依赖 Apple CoreAudioToolbox）。为保证成品质量一致，本程序不会用其他 AAC 编码器替代。请改用 FLAC 或直通音轨。",
	}
	// ErrToolNotFound is returned by the toolchain when a required tool is
	// missing on this machine.
	ErrToolNotFound = &Error{Kind: KindNotFound, Summary: "找不到外部工具"}
)
