// Package ffmpegqaac wraps the two-stage `ffmpeg | qaac` audio pipeline.
//
// It is the port of JobProcessor/Audio/FFmpegPipeQAACEncoder.cs. The legacy
// code drove the pair through cmd.exe (`/c "start ... | ..."`); here they are
// two processes wired together with an in-process pipe, so no shell is involved
// (FANOUT-BRIEF.md §6).
//
// qaac depends on Apple CoreAudioToolbox and therefore only exists on Windows.
// The wrapper refuses the job when the node does not advertise the AAC
// capability, instead of silently substituting another encoder (PLAN.md §2.2).
package ffmpegqaac

import (
	"context"
	"io"
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
const Name = "ffmpeg-qaac"

// DefaultBitrate mirrors Constants.QAACBitrate, which is also the initial value
// of AudioInfo.Bitrate in the legacy model.
const DefaultBitrate = 192

// Markers qaac prints when the encode finished or failed. Both are
// case-sensitive substring tests, exactly as in the legacy ProcessLine.
const (
	doneMarker  = ".done"
	errorMarker = "ERROR"
)

// Progress patterns, copied verbatim from FFmpegPipeQAACEncoder.ProcessLine.
//
// qaac is invoked with -i (--ignorelength) on a pipe, so it does not know the
// total duration and prints "<elapsed> (<speed>x)" rather than
// "[pct] elapsed/total"; the trailing " (" selects that timestamp. The dot is a
// wildcard in the legacy pattern and stays one here.
var (
	reElapsedHMS = regexp.MustCompile(`(\d*):(\d*):(\d*).(\d*) \(`)
	reElapsedMS  = regexp.MustCompile(`(\d*):(\d*).(\d*) \(`)
)

// FFmpegErrorSummary names an ffmpeg failure. The legacy wrapper had no such
// path: it ignored the exit code, so a dead ffmpeg either hung the job or left a
// truncated file behind. The wording follows the "<tool>出错" shape of the
// other Constants.*Smr strings.
const FFmpegErrorSummary = "ffmpeg出错"

// Options configures one ffmpeg|qaac run.
type Options struct {
	// Caps describes the node. The AAC capability is what decides whether the
	// job may run at all.
	Caps node.Capabilities
	// FFmpeg is the absolute path to ffmpeg.
	FFmpeg string
	// QAAC is the absolute path to qaac64.exe.
	QAAC string
	// Input is the source audio file.
	Input string
	// Output is the destination file (.m4a/.aac).
	Output string
	// Info carries the track parameters the legacy code read from
	// AJob.Info: Bitrate (kbps, 0 means DefaultBitrate) and Length (the source
	// duration in milliseconds, used for progress only).
	Info model.AudioInfo
	// Priority is applied to both child processes.
	Priority proc.Priority
}

// Processor encodes one audio track with ffmpeg feeding qaac.
type Processor struct {
	opts Options

	mu     sync.Mutex
	sink   jobproc.ProgressSink
	ffmpeg *proc.Process
	qaac   *proc.Process
}

var (
	_ jobproc.Processor     = (*Processor)(nil)
	_ jobproc.Controllable  = (*Processor)(nil)
	_ jobproc.Prioritizable = (*Processor)(nil)
)

// New returns a Processor for the given options.
func New(opts Options) *Processor {
	if opts.Info.Bitrate <= 0 {
		opts.Info.Bitrate = DefaultBitrate
	}
	if opts.Priority == 0 {
		opts.Priority = proc.DefaultPriority
	}
	return &Processor{opts: opts}
}

// Name implements jobproc.Processor.
func (p *Processor) Name() string { return Name }

// Run starts ffmpeg, wires its stdout into qaac's stdin and blocks until both
// processes finish.
func (p *Processor) Run(ctx context.Context, sink jobproc.ProgressSink) error {
	if sink == nil {
		sink = jobproc.NopSink{}
	}
	// The capability gate comes first: a node without qaac must be refused
	// before any process is started (PLAN.md §2.2).
	if err := toolchain.EnsureAAC(p.opts.Caps); err != nil {
		return err
	}
	return p.run(ctx, sink, proc.Spec{
		Path:     p.opts.FFmpeg,
		Args:     ffmpegArgs(p.opts),
		Priority: p.opts.Priority,
		Name:     toolchain.ToolFFmpeg,
	}, proc.Spec{
		Path:     p.opts.QAAC,
		Args:     qaacArgs(p.opts),
		Priority: p.opts.Priority,
		Name:     toolchain.ToolQAAC,
	})
}

// run wires an ffmpeg producer into a qaac consumer. The specs are passed in so
// that the smoke test can substitute the test binary for both tools while still
// exercising this exact code path.
func (p *Processor) run(ctx context.Context, sink jobproc.ProgressSink, ffmpegSpec, qaacSpec proc.Spec) error {
	p.mu.Lock()
	p.sink = sink
	p.mu.Unlock()

	ffmpeg, err := proc.Start(ffmpegSpec)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.ffmpeg = ffmpeg
	p.mu.Unlock()

	qaac, err := proc.Start(qaacSpec)
	if err != nil {
		_ = ffmpeg.Kill()
		_ = ffmpeg.Close()
		return err
	}
	p.mu.Lock()
	p.qaac = qaac
	p.mu.Unlock()

	// Wire the pipeline in-process. This replaces the legacy
	// `cmd.exe /c "start ... | ..."` (INVENTORY.md §2 #9).
	var pipeWG sync.WaitGroup
	pipeWG.Add(1)
	go func() {
		defer pipeWG.Done()
		_, copyErr := io.Copy(qaac.Stdin(), ffmpeg.Stdout())
		if copyErr != nil {
			// A broken pipe is expected when one side died; the exit codes
			// below carry the real explanation.
			log.Debug("管道中断", "err", copyErr)
		}
		// Closing qaac's stdin signals end-of-input; without this it would wait
		// forever.
		_ = qaac.Stdin().Close()
	}()

	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go func() {
		select {
		case <-ctx.Done():
			_ = ffmpeg.Kill()
			_ = qaac.Kill()
		case <-stopWatch:
		}
	}()

	// ffmpeg's stdout belongs to the pipe; only its stderr is read here. The
	// legacy code parsed both stages through one ProcessLine, so both stderr
	// streams feed the same handler.
	ffmpegErr := make(chan error, 1)
	go func() { ffmpegErr <- ffmpeg.FinishStderr(p.handleChunk) }()

	qaacErr := qaac.Finish(p.handleChunk, p.handleChunk)

	pipeWG.Wait()
	perr := <-ffmpegErr

	if ctx.Err() != nil {
		return okerr.Wrap(ctx.Err(), okerr.KindCanceled, "任务已取消", "%s 已被终止", Name)
	}
	if qaacErr != nil {
		return wrapExit(qaac, qaacErr, okerr.ErrQAAC.Summary)
	}
	if perr != nil {
		return wrapExit(ffmpeg, perr, FFmpegErrorSummary)
	}
	return nil
}

// Close implements jobproc.Processor.
func (p *Processor) Close() error {
	ffmpeg, qaac := p.processes()
	if ffmpeg != nil {
		_ = ffmpeg.Close()
	}
	if qaac != nil {
		_ = qaac.Close()
	}
	return nil
}

// Pause implements jobproc.Controllable. The consumer is suspended first so it
// stops draining the pipe; suspending ffmpeg first could fill the pipe buffer
// and block it.
func (p *Processor) Pause() error {
	ffmpeg, qaac := p.processes()
	if qaac != nil {
		if err := qaac.Pause(); err != nil {
			return err
		}
	}
	if ffmpeg != nil {
		if err := ffmpeg.Pause(); err != nil {
			return err
		}
	}
	return nil
}

// Resume implements jobproc.Controllable.
func (p *Processor) Resume() error {
	ffmpeg, qaac := p.processes()
	if ffmpeg != nil {
		if err := ffmpeg.Resume(); err != nil {
			return err
		}
	}
	if qaac != nil {
		if err := qaac.Resume(); err != nil {
			return err
		}
	}
	return nil
}

// SetPriority implements jobproc.Prioritizable.
func (p *Processor) SetPriority(prio jobproc.Priority) error {
	ffmpeg, qaac := p.processes()
	converted := proc.Priority(prio)
	if ffmpeg != nil {
		if err := ffmpeg.SetPriority(converted); err != nil {
			return err
		}
	}
	if qaac != nil {
		if err := qaac.SetPriority(converted); err != nil {
			return err
		}
	}
	return nil
}

func (p *Processor) processes() (ffmpeg, qaac *proc.Process) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ffmpeg, p.qaac
}

func (p *Processor) report(prog jobproc.Progress) {
	p.mu.Lock()
	sink := p.sink
	p.mu.Unlock()
	if sink != nil {
		sink.Report(prog)
	}
}

// handleChunk feeds one scanner line into the legacy parser. qaac rewrites its
// progress with a bare carriage return, which the legacy .NET reader treated as
// a line break but bufio.Scanner does not, so the chunk is split again here.
func (p *Processor) handleChunk(chunk string) error {
	for _, line := range strings.Split(chunk, "\r") {
		if line == "" {
			continue // the legacy reader skipped empty lines too
		}
		if err := p.handleLine(line); err != nil {
			return err
		}
	}
	return nil
}

// handleLine mirrors ProcessLine: a line that matches a progress pattern is
// never inspected for the finish or error markers, exactly as the legacy
// if/else-if/else did.
func (p *Processor) handleLine(line string) error {
	prog, matched := p.parseLine(line)
	if matched {
		log.Trace("音频转码进度", "tool", Name, "line", line, "percent", prog.Percent)
		if prog.Percent > 1 {
			p.report(prog)
		}
		return nil
	}
	log.Debug("音频转码输出", "tool", Name, "line", line)
	if strings.Contains(line, doneMarker) {
		p.report(jobproc.Progress{Percent: 100, Status: "音频转码完成"})
	}
	if strings.Contains(line, errorMarker) {
		return okerr.New(okerr.KindTool, okerr.ErrQAAC.Summary, "%s", line).WithOutput(line)
	}
	return nil
}

// parseLine extracts the elapsed time qaac reports and turns it into a
// percentage of the source length. The two patterns are tried in the legacy
// order; the first one that matches wins. The returned Progress carries no
// percentage when the legacy code would have suppressed it (p <= 1), so the
// caller can tell "matched" from "worth reporting".
func (p *Processor) parseLine(line string) (jobproc.Progress, bool) {
	if m := reElapsedHMS.FindStringSubmatch(line); m != nil {
		return p.percent(digits(m[1])*3600 + digits(m[2])*60 + digits(m[3])), true
	}
	if m := reElapsedMS.FindStringSubmatch(line); m != nil {
		return p.percent(digits(m[1])*60 + digits(m[2])), true
	}
	return jobproc.Progress{}, false
}

// percent mirrors "p = elapsed * 1.0 / length * 100; if (p > 1)". The legacy
// code divided by the length in whole seconds; Info.Length is used directly,
// which is the same number for the whole-second lengths the demuxer produces.
func (p *Processor) percent(elapsed int) jobproc.Progress {
	if p.opts.Info.Length <= 0 {
		// The legacy code divided by zero here; a missing length simply means
		// no percentage can be shown.
		return jobproc.Progress{}
	}
	value := float64(elapsed) * 1000 / float64(p.opts.Info.Length) * 100
	if value <= 1 {
		return jobproc.Progress{}
	}
	return jobproc.Progress{Percent: value, Status: "音频转码中"}
}

// digits parses a regex capture, treating an empty or malformed one as zero.
// The legacy code called int.Parse, which threw; a mangled progress line must
// not abort an otherwise healthy encode.
func digits(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

// ffmpegArgs mirrors the legacy command line:
//
//	ffmpeg -i <input> -vn -sn -dn -f wav -v warning -
func ffmpegArgs(opts Options) []string {
	return []string{"-i", opts.Input, "-vn", "-sn", "-dn", "-f", "wav", "-v", "warning", "-"}
}

// qaacArgs mirrors the legacy command line:
//
//	qaac -i -v <bitrate> -q 2 --no-delay --threading -o <output> -
//
// Note that this pipeline always uses -v <bitrate>; the quality field only
// applies to the standalone QAACEncoder (A6).
func qaacArgs(opts Options) []string {
	return []string{
		"-i",
		"-v", strconv.Itoa(opts.Info.Bitrate),
		"-q", "2",
		"--no-delay",
		"--threading",
		"-o", opts.Output,
		"-",
	}
}

// wrapExit turns a process failure into a structured error. Errors raised by the
// line handler already carry a summary and are passed through unchanged.
func wrapExit(pr *proc.Process, err error, summary string) error {
	if e := okerr.AsError(err); e.Kind == okerr.KindTool || e.Kind == okerr.KindToolCrash {
		return e
	}
	return okerr.Wrap(err, okerr.KindTool, summary, "%s 异常退出", pr.Name()).
		WithTool(pr.Name(), pr.ExitCode())
}
