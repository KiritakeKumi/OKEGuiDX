// Package jobproc defines the contract every external-tool wrapper implements.
//
// A Processor is one step of a task: demux, encode, mux, check. The engine
// drives Processors; it never knows which tool is behind one. This is the
// boundary that keeps the pipeline readable (see WORKSTREAMS.md §1 P0-6).
package jobproc

import (
	"context"

	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
)

// Progress reports how far a processor has got. All fields are optional; a
// processor that cannot estimate simply leaves them zero.
type Progress struct {
	// Percent is the completion percentage, 0..100. Negative means unknown.
	Percent float64
	// Speed is a preformatted human string such as "12.34 fps".
	Speed string
	// BitRate is a preformatted human string such as "1234.56 kb/s".
	BitRate string
	// TimeRemainSeconds is the estimated remaining time; negative means unknown.
	TimeRemainSeconds float64
	// FramesDone and FramesTotal are set by video encoders.
	FramesDone  int64
	FramesTotal int64
	// Status is a short human description of what is happening now.
	Status string
}

// ProgressSink receives progress updates from a processor. Implementations must
// be safe for concurrent use and must never block for long: the engine's
// scheduler and the API stream share this path.
type ProgressSink interface {
	Report(p Progress)
}

// ProgressFunc adapts a function to ProgressSink.
type ProgressFunc func(p Progress)

// Report implements ProgressSink.
func (f ProgressFunc) Report(p Progress) { f(p) }

// NopSink discards progress updates.
type NopSink struct{}

// Report implements ProgressSink.
func (NopSink) Report(Progress) {}

// LineHandler receives one line of a tool's output. Returning an error stops the
// processor; the error is returned from Run.
type LineHandler func(line string) error

// Processor is a single executable step.
//
// Lifecycle: the engine calls Run exactly once, then Close. Run must block until
// the step finishes or ctx is canceled, and must return a *okerr.Error so the
// caller can present a structured message.
type Processor interface {
	// Name returns a short identifier used in logs and status events, such as
	// "x265" or "demux".
	Name() string
	// Run performs the step.
	Run(ctx context.Context, sink ProgressSink) error
	// Close releases resources. It must be safe to call after a failed Run and
	// safe to call more than once.
	Close() error
}

// Controllable is implemented by processors whose work can be suspended. The
// engine exposes pause/resume only when a processor supports it.
type Controllable interface {
	Pause() error
	Resume() error
}

// Prioritizable is implemented by processors that can adjust the OS priority of
// their child processes at runtime.
type Prioritizable interface {
	SetPriority(p Priority) error
}

// Priority mirrors proc.Priority without forcing importers of this interface to
// depend on the process package.
type Priority int

// Priorities, matching proc.Priority.
const (
	PriorityIdle Priority = iota
	PriorityBelowNormal
	PriorityNormal
	PriorityAboveNormal
	PriorityHigh
	PriorityParallel
)

// Result carries the artifacts a processor produced. Most processors only fill
// in Outputs; encoders also fill Frames.
type Result struct {
	// Outputs are the files the step created, in the order they should be
	// cleaned up on failure.
	Outputs []model.FileRef
	// Frames is the number of frames processed, for video steps.
	Frames int64
	// DurationMS is the media duration, for audio and demux steps.
	DurationMS int64
}

// Error helpers re-exported so wrapper packages do not need to import okerr
// directly for the common cases.

// ErrToolNotFound reports a missing external tool.
func ErrToolNotFound(tool, path string) *okerr.Error {
	return okerr.New(okerr.KindNotFound, "找不到外部工具", "%s 不存在：%s", tool, path)
}

// ErrUnsupported reports that the current platform cannot run a step.
func ErrUnsupported(what, why string) *okerr.Error {
	return okerr.New(okerr.KindUnsupported, what, "%s", why)
}
