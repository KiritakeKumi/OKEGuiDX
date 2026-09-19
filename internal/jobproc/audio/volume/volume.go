// Package volume measures how loud an audio file is with ffmpeg.
//
// It is the port of JobProcessor/Audio/FFmpegVolumeChecker.cs. The legacy class
// ran
//
//	ffmpeg -i <file> -af astats=measure_perchannel=none -f null /dev/null
//
// and scraped the "Peak level dB" and "RMS level dB" lines from its output. The
// two numbers are what the demuxer needs to tell silent and duplicated audio
// tracks apart: internal/jobproc/demux/eac3to feeds them into
// TrackInfo.IsEmpty and TrackInfo.IsDuplicate.
//
// Two details differ from the legacy class, both because a headless engine must
// not block forever:
//
//   - A run that reports no level is an error. The legacy waitForFinish waited
//     for the Peak level line and hung when it never arrived.
//   - Both output streams are parsed and the process is reaped with
//     proc.Finish, so the tail of a burst of output is never lost
//     (FANOUT-BRIEF.md §6).
//
// The volumedetect spelling of the same two numbers is recognised as well. The
// command line used that filter before commit 3d1f31c switched it to astats,
// and the fixture set keeps real captures of both; a single run uses exactly
// one filter, so the dialects cannot mix in practice.
package volume

import (
	"context"
	"math"
	"regexp"
	"strconv"
	"sync"

	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc"
	"github.com/KiritakeKumi/OKEGuiDX/internal/log"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
	"github.com/KiritakeKumi/OKEGuiDX/internal/proc"
)

// Name is the processor name used in logs and status events.
const Name = "ffmpeg-volume"

// StatusDone is the terminal status reported to the sink.
const StatusDone = "音量检测完成"

// Summaries for the failures this package can report. okerr carries no ffmpeg
// sentinel; the wording follows the "<tool>出错" shape of the legacy
// Constants.*Smr strings, as ffmpegqaac does for the same tool.
const (
	// FFmpegErrorSummary names an ffmpeg run that failed before it could be
	// measured.
	FFmpegErrorSummary = "ffmpeg出错"
	// NoLevelsSummary names a run that completed without reporting a level.
	NoLevelsSummary = "音量检测失败"
)

// Level patterns, copied verbatim from FFmpegVolumeChecker.ProcessLine. The
// capture is either a decimal number or the literal "inf" ffmpeg prints for
// digital silence.
var (
	reRMSLevel  = regexp.MustCompile(`RMS level dB: (-?(?:\d+\.\d+|inf))`)
	rePeakLevel = regexp.MustCompile(`Peak level dB: (-?(?:\d+\.\d+|inf))`)
)

// Patterns of the volumedetect filter, the pre-3d1f31c command line. They only
// fill a field astats did not report, so the newer filter always wins.
var (
	reMeanVolume = regexp.MustCompile(`mean_volume: (-?(?:\d+\.\d+|inf)) dB`)
	reMaxVolume  = regexp.MustCompile(`max_volume: (-?(?:\d+\.\d+|inf)) dB`)
)

// nullSink is the output the legacy command line used on every platform. ffmpeg
// maps it to NUL on Windows, so one constant works everywhere.
const nullSink = "/dev/null"

// BuildArgs assembles the ffmpeg argv the legacy constructor built:
//
//	-i <input> -af astats=measure_perchannel=none -f null /dev/null
//
// The quotes the legacy code wrapped around the path are gone: arguments are
// passed verbatim here, so a path with spaces survives without them.
func BuildArgs(input string) []string {
	return []string{"-i", input, "-af", "astats=measure_perchannel=none", "-f", "null", nullSink}
}

// Options configures one volume check.
type Options struct {
	// FFmpeg is the absolute path to ffmpeg.
	FFmpeg string
	// Input is the audio file to measure.
	Input string
	// Priority is applied to the child process. The zero value selects
	// proc.DefaultPriority.
	Priority proc.Priority
}

// Result holds the levels one run measured, in dB.
type Result struct {
	// MeanVolume is the RMS level. It stays 0 when no RMS line was seen,
	// which is the default the legacy property had.
	MeanVolume float64
	// MaxVolume is the peak level. -Inf means digital silence.
	MaxVolume float64
}

// Processor runs ffmpeg once and keeps the levels it reported.
type Processor struct {
	tool     string
	input    string
	args     []string
	priority proc.Priority

	mu      sync.Mutex
	child   *proc.Process
	mean    float64
	max     float64
	meanSet bool
	maxSet  bool
}

var (
	_ jobproc.Processor     = (*Processor)(nil)
	_ jobproc.Controllable  = (*Processor)(nil)
	_ jobproc.Prioritizable = (*Processor)(nil)
)

// New returns a Processor for the given options.
func New(opts Options) *Processor {
	priority := opts.Priority
	if priority == 0 {
		priority = proc.DefaultPriority
	}
	return &Processor{
		tool:     opts.FFmpeg,
		input:    opts.Input,
		args:     BuildArgs(opts.Input),
		priority: priority,
	}
}

// Name implements jobproc.Processor.
func (p *Processor) Name() string { return Name }

// Args returns a copy of the arguments ffmpeg is started with.
func (p *Processor) Args() []string { return append([]string(nil), p.args...) }

// Run implements jobproc.Processor. It returns nil only when a maximum level
// was reported: a run without one cannot be told apart from a broken file, and
// the demuxer would silently classify the track as audible.
func (p *Processor) Run(ctx context.Context, sink jobproc.ProgressSink) error {
	if sink == nil {
		sink = jobproc.NopSink{}
	}

	child, err := proc.Start(proc.Spec{
		Path:     p.tool,
		Args:     p.args,
		Priority: p.priority,
		Name:     Name,
	})
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.child = child
	p.mu.Unlock()

	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go func() {
		select {
		case <-ctx.Done():
			_ = child.Kill()
		case <-stopWatch:
		}
	}()

	// astats writes its report to stderr; the legacy ProcessLine inspected both
	// streams, so both are parsed here.
	finishErr := child.Finish(p.onLine, p.onLine)

	if ctx.Err() != nil {
		return okerr.Wrap(ctx.Err(), okerr.KindCanceled, "任务已取消", "%s 已被终止", Name)
	}
	// A non-zero exit is ignored when a level was measured: the legacy base
	// class stopped checking exit codes entirely (8e6750e removed
	// checkExitCode), and a track that has been read far enough to be measured
	// is what the demuxer needs. Without a level there is nothing to report, so
	// the exit code becomes the explanation.
	if !p.measured() {
		return p.noLevels(child, finishErr)
	}
	// Nothing can be reported while the filter runs: it has no known total.
	// The terminal status keeps the UI in step with the other processors.
	sink.Report(jobproc.Progress{Percent: 100, Status: StatusDone})
	return nil
}

// Close implements jobproc.Processor.
func (p *Processor) Close() error {
	if child := p.process(); child != nil {
		return child.Close()
	}
	return nil
}

// Pause implements jobproc.Controllable.
func (p *Processor) Pause() error {
	if child := p.process(); child != nil {
		return child.Pause()
	}
	return nil
}

// Resume implements jobproc.Controllable.
func (p *Processor) Resume() error {
	if child := p.process(); child != nil {
		return child.Resume()
	}
	return nil
}

// SetPriority implements jobproc.Prioritizable.
func (p *Processor) SetPriority(prio jobproc.Priority) error {
	child := p.process()
	if child == nil {
		return nil
	}
	return child.SetPriority(proc.Priority(prio))
}

// Result returns the levels of the last run. Only meaningful once Run has
// returned nil.
func (p *Processor) Result() Result {
	p.mu.Lock()
	defer p.mu.Unlock()
	return Result{MeanVolume: p.mean, MaxVolume: p.max}
}

func (p *Processor) process() *proc.Process {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.child
}

// measured reports whether a maximum level was reported. The legacy code ended
// the run on that line, so it is the signal that a measurement arrived.
func (p *Processor) measured() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.maxSet
}

// noLevels turns "the filter never reported a maximum" into an error. The
// legacy code waited for that line and would have hung here.
func (p *Processor) noLevels(child *proc.Process, finishErr error) error {
	code := child.ExitCode()
	if finishErr != nil {
		log.Error("ffmpeg音量检测异常退出", "code", code)
		return okerr.Wrap(finishErr, okerr.KindTool, FFmpegErrorSummary, "ffmpeg 退出代码 %d", code).
			WithTool(Name, code).
			WithOutput(child.RecentOutput()).
			WithFile(p.input)
	}
	return okerr.New(okerr.KindTool, NoLevelsSummary, "ffmpeg 未输出音量信息").
		WithTool(Name, code).
		WithOutput(child.RecentOutput()).
		WithFile(p.input)
}

// onLine mirrors FFmpegVolumeChecker.ProcessLine. A matching RMS line ends the
// inspection of that line, so one line can only ever set one value; the last
// line of a field wins, as it did in the legacy assignments.
func (p *Processor) onLine(line string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if m := reRMSLevel.FindStringSubmatch(line); m != nil {
		p.mean, p.meanSet = parseLevel(m[1]), true
		return nil
	}
	if m := rePeakLevel.FindStringSubmatch(line); m != nil {
		p.max, p.maxSet = parseLevel(m[1]), true
		return nil
	}
	if m := reMeanVolume.FindStringSubmatch(line); m != nil {
		if !p.meanSet {
			p.mean, p.meanSet = parseLevel(m[1]), true
		}
		return nil
	}
	if m := reMaxVolume.FindStringSubmatch(line); m != nil && !p.maxSet {
		p.max, p.maxSet = parseLevel(m[1]), true
	}
	return nil
}

// parseLevel converts a regex capture into a level. The legacy code used
// double.TryParse and stored negative infinity when it failed, which is how
// silence is represented. strconv additionally accepts "inf", so a positive
// infinity is folded back to keep the legacy result.
func parseLevel(s string) float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsInf(v, 1) {
		return math.Inf(-1)
	}
	return v
}

// Checker measures files with one ffmpeg path. It is the implementation of the
// VolumeMeasurer seam that internal/jobproc/demux/eac3to declares, so the
// demuxer can take a *Checker without this package knowing about it.
type Checker struct {
	ffmpeg   string
	priority proc.Priority
}

// The demuxer only needs Measure; pinning the signature here keeps a future
// edit from silently breaking internal/jobproc/demux/eac3to.
var _ interface {
	Measure(ctx context.Context, file string) (mean, max float64, err error)
} = (*Checker)(nil)

// NewChecker returns a Checker that runs ffmpeg at the given path.
func NewChecker(ffmpeg string, priority proc.Priority) *Checker {
	if priority == 0 {
		priority = proc.DefaultPriority
	}
	return &Checker{ffmpeg: ffmpeg, priority: priority}
}

// Measure returns the RMS level and the peak level of file, in dB. Both are
// negative in practice; -Inf means silence.
func (c *Checker) Measure(ctx context.Context, file string) (mean, max float64, err error) {
	p := New(Options{FFmpeg: c.ffmpeg, Input: file, Priority: c.priority})
	defer func() { _ = p.Close() }()

	if err := p.Run(ctx, nil); err != nil {
		return 0, 0, err
	}
	r := p.Result()
	return r.MeanVolume, r.MaxVolume, nil
}
