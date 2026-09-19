// Package qaac wraps the qaac AAC encoder.
//
// Behaviour is defined by JobProcessor/Audio/QAACEncoder.cs; the startup
// self-test comes from Utils/EnvironmentChecker.cs (CheckQAAC). The legacy code
// assembled one command-line string and let cmd.exe split it; here every
// argument is a separate slice element.
//
// qaac exists only where Apple's CoreAudioToolbox does. This package therefore
// refuses to build a processor on a node without node.FeatureAAC instead of
// substituting another encoder, which would silently change the output
// (PLAN.md §2.2).
package qaac

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc"
	"github.com/KiritakeKumi/OKEGuiDX/internal/log"
	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/node"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
	"github.com/KiritakeKumi/OKEGuiDX/internal/proc"
	"github.com/KiritakeKumi/OKEGuiDX/internal/toolchain"
)

// Name is the processor name used in logs and status events.
const Name = "qaac"

// Encoder limits, mirroring Constants.QAACBitrate, Constants.QAACQualityMin and
// Constants.QAACQualityMax. The profile validator already enforces the quality
// range; they are repeated here because the encoder is the last place a wrong
// value can still be caught.
const (
	// DefaultBitrate is the bitrate AudioInfo uses when the profile does not
	// name one.
	DefaultBitrate = 192
	// QualityMin and QualityMax bound qaac's True VBR quality.
	QualityMin = 0
	QualityMax = 127
)

// checkArg is qaac's self-test switch. EnvironmentChecker.CheckQAAC ran exactly
// this to prove that CoreAudioToolbox is installed and usable.
const checkArg = "--check"

// Markers taken from QAACEncoder.ProcessLine. They are matched with
// strings.Contains, so their case is significant.
const (
	errorMarker = "ERROR"
	doneMarker  = ".done"
)

// reProgress matches qaac's progress redraw, e.g. "[42.0%] 1:43.196/4:02.517".
// The legacy pattern was \[([0-9.]+)%\].
var reProgress = regexp.MustCompile(`\[([0-9.]+)%\]`)

// Options configures a qaac run.
type Options struct {
	// Input is the source file qaac reads (FLAC, WAV or raw PCM).
	Input string
	// Output is the destination .m4a file.
	Output string
	// Bitrate is the constrained-VBR bitrate in kbit/s. It is only used when
	// Quality is nil.
	Bitrate int
	// Quality selects True VBR and must lie within QualityMin..QualityMax. A
	// non-nil value wins over Bitrate, which is what the legacy constructor
	// did when both were present; the profile validator rejects that case
	// before it reaches here.
	Quality *int
	// Priority is applied to the child process. The zero value selects
	// proc.DefaultPriority.
	Priority proc.Priority
}

// OptionsFromInfo fills the options from a track's metadata, mirroring the
// legacy constructor reading AJob.Info. A zero Bitrate means "the profile did
// not specify one" and becomes DefaultBitrate, exactly like AudioInfo's field
// initializer did.
func OptionsFromInfo(info model.AudioInfo, input, output string) Options {
	bitrate := info.Bitrate
	if bitrate == 0 {
		bitrate = DefaultBitrate
	}
	return Options{
		Input:   input,
		Output:  output,
		Bitrate: bitrate,
		Quality: info.Quality,
	}
}

// BuildArgs assembles the argument list the legacy constructor put together:
//
//	-i [-V quality | -v bitrate] -q 2 --no-delay -o <output> <input>
//
// "-q 2" is CoreAudio's own quality knob [0-2]; it is not the VBR quality that
// -V selects.
func BuildArgs(opts Options) []string {
	args := []string{"-i"}
	if opts.Quality != nil {
		args = append(args, "-V", strconv.Itoa(*opts.Quality))
	} else {
		args = append(args, "-v", strconv.Itoa(opts.Bitrate))
	}
	return append(args, "-q", "2", "--no-delay", "-o", opts.Output, opts.Input)
}

// Processor runs qaac once.
type Processor struct {
	tool     string
	args     []string
	priority proc.Priority

	mu       sync.Mutex
	child    *proc.Process
	sink     jobproc.ProgressSink
	prog     jobproc.Progress
	lineErr  error
	finished bool
}

// New returns a processor that encodes opts with qaac.
//
// It refuses to build one on a node without node.FeatureAAC: this project does
// not substitute another AAC encoder, because that would silently change the
// output (PLAN.md §2.2). The returned error is okerr.ErrUnsupportedAAC, so
// callers can recognise it with errors.Is.
func New(caps node.Capabilities, opts Options) (*Processor, error) {
	return newProcessor(caps, BuildArgs(opts), opts.Priority)
}

// NewCheck returns a processor that runs qaac's self-test. It is the Go
// equivalent of the legacy QAACEncoder(string) constructor.
func NewCheck(caps node.Capabilities) (*Processor, error) {
	return newProcessor(caps, []string{checkArg}, 0)
}

func newProcessor(caps node.Capabilities, args []string, priority proc.Priority) (*Processor, error) {
	if err := toolchain.EnsureAAC(caps); err != nil {
		return nil, err
	}
	path, err := toolchain.Require(caps, toolchain.ToolQAAC)
	if err != nil {
		return nil, err
	}
	if priority == 0 {
		priority = proc.DefaultPriority
	}
	return &Processor{tool: path, args: args, priority: priority}, nil
}

// Check runs qaac's self-test and returns nil when the encoder can work. It
// replaces EnvironmentChecker.CheckQAAC; a failure carries okerr.ErrQAAC as its
// summary, so okerr.Render produces Constants.qaacErrorMsg.
func Check(ctx context.Context, caps node.Capabilities) error {
	p, err := NewCheck(caps)
	if err != nil {
		return err
	}
	return p.Run(ctx, nil)
}

// Name implements jobproc.Processor.
func (p *Processor) Name() string { return Name }

// Args returns a copy of the arguments qaac is started with.
func (p *Processor) Args() []string { return append([]string(nil), p.args...) }

// Finished reports whether qaac printed its completion marker. The legacy job
// waited for that marker; here the process exiting is what ends the run, so a
// missing marker is not treated as a failure.
func (p *Processor) Finished() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.finished
}

// Progress returns the latest progress snapshot.
func (p *Processor) Progress() jobproc.Progress {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.prog
}

// Run implements jobproc.Processor.
func (p *Processor) Run(ctx context.Context, sink jobproc.ProgressSink) error {
	if sink == nil {
		sink = jobproc.NopSink{}
	}
	p.mu.Lock()
	p.sink = sink
	p.mu.Unlock()

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

	// qaac prints both progress and errors on stderr; stdout only carries the
	// --check banner. The legacy code parsed the two streams identically.
	finishErr := child.Finish(p.onLine, p.onLine)

	p.mu.Lock()
	lineErr := p.lineErr
	p.mu.Unlock()

	if ctx.Err() != nil {
		return okerr.Wrap(ctx.Err(), okerr.KindCanceled, "任务已取消", "%s 已被终止", Name)
	}
	if lineErr != nil {
		return lineErr
	}
	if finishErr != nil {
		// The legacy code only failed on an "ERROR" line and treated every
		// other exit as success, which would let a truncated encode reach the
		// muxer. A non-zero exit is an error here as well.
		code := child.ExitCode()
		log.Error("qaac退出异常", "code", code)
		return okerr.Wrap(finishErr, okerr.KindTool, okerr.ErrQAAC.Summary, "qaac 退出代码 %d", code).
			WithTool(Name, code).
			WithOutput(child.RecentOutput())
	}
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

func (p *Processor) process() *proc.Process {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.child
}

// onLine mirrors QAACEncoder.ProcessLine. A failure is recorded rather than
// returned, so the rest of the output is still drained before Run reports it,
// exactly as the legacy handler did.
func (p *Processor) onLine(line string) error {
	// qaac redraws its progress with a bare carriage return. .NET's ReadLine
	// treated that as a line separator, bufio's ScanLines does not, so the
	// chunks are split here to keep the legacy update granularity.
	for _, chunk := range strings.Split(line, "\r") {
		p.handleLine(chunk)
	}
	return nil
}

func (p *Processor) handleLine(line string) {
	if m := reProgress.FindStringSubmatch(line); m != nil {
		// The legacy code used double.TryParse and published progress only
		// above 1%, so a malformed capture and the initial 0.0% are ignored.
		if v, err := strconv.ParseFloat(m[1], 64); err == nil && v > 1 {
			p.report(v)
		}
		return
	}
	log.Debug("qaac输出", "line", line)
	if strings.Contains(line, doneMarker) {
		p.mu.Lock()
		p.finished = true
		p.mu.Unlock()
	}
	if strings.Contains(line, errorMarker) {
		p.fail(okerr.New(okerr.KindTool, okerr.ErrQAAC.Summary, "%s", line).WithOutput(line))
	}
}

// fail keeps the first failure seen on either output stream.
func (p *Processor) fail(err error) {
	p.mu.Lock()
	if p.lineErr == nil {
		p.lineErr = err
	}
	p.mu.Unlock()
}

// report publishes a progress update. qaac reports a position within the track,
// so only the percentage is known.
func (p *Processor) report(percent float64) {
	p.mu.Lock()
	p.prog.Percent = percent
	snapshot := p.prog
	sink := p.sink
	p.mu.Unlock()
	if sink != nil {
		sink.Report(snapshot)
	}
}
