package engine

import (
	"strconv"

	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc"
	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
)

// reporter turns jobproc progress into model.StatusEvent values. It is the one
// place in the pipeline that writes to the event channel, so the mapping is
// stated once.
//
// The channel is best-effort: LocalExecutor buffers 64 events and drops what the
// consumer cannot keep up with, so a slow consumer must never stall an encode. A
// send that cannot proceed immediately is therefore skipped rather than blocked
// on.
type reporter struct {
	taskID model.TaskID
	events chan<- model.StatusEvent

	// status is the current stage's display text; percent is its progress.
	status  string
	percent float64
}

// newReporter returns a reporter for one task.
func newReporter(taskID model.TaskID, events chan<- model.StatusEvent) *reporter {
	return &reporter{taskID: taskID, events: events, percent: -1}
}

// step starts a stage: it sets the display text and the progress value and emits
// an event. A negative percent means "unknown".
func (r *reporter) step(status string, percent float64) {
	r.status = status
	r.percent = percent
	r.emit()
}

// sink returns the ProgressSink for the current stage.
func (r *reporter) sink() jobproc.ProgressSink { return jobproc.ProgressFunc(r.report) }

// report applies one processor progress update.
//
// The mapping follows model.StatusEvent's contract: Percent is negative when
// unknown, and the display fields are only overwritten when the processor
// actually reported one. Speed and bitrate are preformatted by the wrappers.
func (r *reporter) report(p jobproc.Progress) {
	if p.Status != "" {
		r.status = p.Status
	}
	switch {
	case p.Percent >= 0:
		r.percent = p.Percent
	case p.FramesTotal > 0 && p.FramesDone > 0:
		r.percent = float64(p.FramesDone) / float64(p.FramesTotal) * 100
	default:
		r.percent = -1
	}
	r.emitFull(p)
}

// emit sends the current state without a processor update.
func (r *reporter) emit() { r.emitFull(jobproc.Progress{}) }

// emitFull builds and sends one event. A send that cannot proceed immediately is
// dropped: the event stream is a progress display, not a record, and the worker
// pool mirrors it into the queue.
func (r *reporter) emitFull(p jobproc.Progress) {
	if r.events == nil {
		return
	}
	ev := model.StatusEvent{
		TaskID:      r.taskID,
		Progress:    model.TaskRunning,
		Step:        r.status,
		Percent:     r.percent,
		Speed:       p.Speed,
		BitRate:     p.BitRate,
		FramesDone:  p.FramesDone,
		FramesTotal: p.FramesTotal,
	}
	if p.TimeRemainSeconds > 0 {
		ev.TimeRemainSeconds = p.TimeRemainSeconds
	}
	select {
	case r.events <- ev:
	default:
	}
}

// formatIFrames renders an I-frame list the way the legacy IFrameInfo.ToString
// did: "[ a, b, c, ]". It is only used for log lines.
func formatIFrames(frames []int64) string {
	b := make([]byte, 0, 2+8*len(frames))
	b = append(b, '[', ' ')
	for _, f := range frames {
		b = append(b, strconv.FormatInt(f, 10)...)
		b = append(b, ',', ' ')
	}
	return string(append(b, ']'))
}
