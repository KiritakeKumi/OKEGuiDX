// Package vspipeinfo parses the output of `vspipe --info`.
//
// Behaviour is defined by JobProcessor/Video/VSPipeInfoProcessor.cs. The
// processor runs vspipe with --info, reads the script's properties from its
// output, and fails when the script's frame rate does not match what the
// profile declared.
package vspipeinfo

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc"
	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
	"github.com/KiritakeKumi/OKEGuiDX/internal/proc"
)

// Name is the processor name used in logs and status events.
const Name = "vspipe-info"

// Output patterns, taken verbatim from VSPipeInfoProcessor.ProcessLine. The
// original compiled them per line; they are hoisted here because compiling a
// regex per output line is a measurable cost on a long script.
var (
	reWidth      = regexp.MustCompile(`Width: ([0-9]+)`)
	reHeight     = regexp.MustCompile(`Height: ([0-9]+)`)
	reFrames     = regexp.MustCompile(`Frames: ([0-9]+)`)
	reFPS        = regexp.MustCompile(`FPS: ([0-9]+)/([0-9]+) \(([0-9]+\.[0-9]+) fps\)`)
	reFormatName = regexp.MustCompile(`Format Name: ([a-zA-Z0-9]+)`)
	reColorFam   = regexp.MustCompile(`Color Family: ([a-zA-Z]+)`)
	reBits       = regexp.MustCompile(`Bits: ([0-9]+)`)
	reLwi        = regexp.MustCompile(`Creating lwi index file ([0-9]+)%`)
)

// reTraceback matches the terminal line of a VapourSynth traceback, which is
// what turns an accumulating Python error into a reportable failure.
var reTraceback = regexp.MustCompile(`^([a-zA-Z_.]*)(Error|Exception|Exit|Interrupt|Iteration|Warning)(.*)`)

// Options configures a vspipe --info run.
type Options struct {
	// VSPipe is the absolute path to vspipe.
	VSPipe string
	// Script is the .vpy path.
	Script string
	// VSPipeArgs are extra `--arg` values passed through to the script.
	VSPipeArgs []string
	// ExpectedFpsNum and ExpectedFpsDen are the profile's frame rate, checked
	// against the script's unless VFR is set.
	ExpectedFpsNum int64
	ExpectedFpsDen int64
	// VFR skips the frame rate check, because a variable frame rate source
	// legitimately reports whatever it likes.
	VFR bool
}

// Processor runs vspipe --info and parses its output.
type Processor struct {
	opts Options

	info model.VSVideoInfo

	mu       sync.Mutex
	progress float64
	sink     jobproc.ProgressSink
}

// New returns a Processor for the given options.
func New(opts Options) *Processor {
	return &Processor{opts: opts}
}

// Name implements jobproc.Processor.
func (p *Processor) Name() string { return Name }

// Info returns the parsed video information. It is only meaningful after Run
// has returned successfully.
func (p *Processor) Info() model.VSVideoInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.info
}

// Run implements jobproc.Processor.
func (p *Processor) Run(ctx context.Context, sink jobproc.ProgressSink) error {
	if sink == nil {
		sink = jobproc.NopSink{}
	}
	p.mu.Lock()
	p.sink = sink
	p.mu.Unlock()

	args := make([]string, 0, 2+2*len(p.opts.VSPipeArgs)+1)
	args = append(args, "--info")
	for _, a := range p.opts.VSPipeArgs {
		args = append(args, "--arg", a)
	}
	// The script path is quoted in the legacy command line; here it is a single
	// argument, so no quoting is needed or wanted.
	args = append(args, p.opts.Script, "-")

	process, err := proc.Start(proc.Spec{
		Path:     p.opts.VSPipe,
		Args:     args,
		Priority: proc.DefaultPriority,
		Name:     "vspipe",
	})
	if err != nil {
		return err
	}
	_ = process.Stdin().Close()

	// Cancellation has to kill the child: Consume/Wait block on its pipes, and
	// a script that hangs (a slow index build, a stalled network source) would
	// otherwise ignore the cancelled context until it finishes on its own.
	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go func() {
		select {
		case <-ctx.Done():
			_ = process.Kill()
		case <-stopWatch:
		}
	}()

	// Both streams carry properties: vspipe writes the script's output to
	// stdout and its own diagnostics to stderr, and either may hold the
	// traceback when the script fails.
	handler := p.lineHandler()
	consumeErr := process.Consume(handler, handler)
	waitErr := process.Wait()

	// The context check comes first: a kill from the watcher above also breaks
	// the pipe reads, and reporting that as a vspipe failure would hide the
	// real reason.
	if ctx.Err() != nil {
		return okerr.Wrap(ctx.Err(), okerr.KindCanceled, "任务已取消", "vspipe 已被终止")
	}
	if consumeErr != nil {
		return consumeErr
	}
	if waitErr != nil {
		e := okerr.AsError(waitErr)
		return okerr.Wrap(waitErr, okerr.KindTool, okerr.ErrVpy.Summary,
			"vspipe 异常退出").WithTool("vspipe", e.ExitCode).WithOutput(process.RecentOutput())
	}

	if err := p.checkFps(); err != nil {
		return err
	}
	return nil
}

// Close implements jobproc.Processor.
func (p *Processor) Close() error { return nil }

// lineHandler builds the per-line parser.
//
// The state machine mirrors the original: a line containing "Python exception: "
// starts collecting a traceback, and the traceback ends at the first line that
// looks like an exception summary. The collected text becomes the error detail.
func (p *Processor) lineHandler() proc.LineFunc {
	var (
		inError bool
		buf     strings.Builder
	)
	return func(line string) error {
		switch {
		case strings.Contains(line, "Python exception: "):
			inError = true
			buf.Reset()
			return nil

		case inError:
			if reTraceback.MatchString(line) {
				buf.WriteString("\n")
				buf.WriteString(line)
				return okerr.New(okerr.KindTool, okerr.ErrVpy.Summary, "%s",
					strings.TrimSpace(buf.String())).WithOutput(strings.TrimSpace(buf.String()))
			}
			if line != "" {
				buf.WriteString("\n")
				buf.WriteString(line)
			}
			return nil
		}

		// Property lines. The original used a chain of Contains checks in this
		// order, which matters: "Bits" appears in no other property line, but
		// the order is preserved to keep the behaviour identical.
		switch {
		case strings.Contains(line, "Width"):
			if m := reWidth.FindStringSubmatch(line); m != nil {
				if w, err := strconv.Atoi(m[1]); err == nil && w > 0 {
					p.mu.Lock()
					p.info.Width = w
					p.mu.Unlock()
				}
			}

		case strings.Contains(line, "Height"):
			if m := reHeight.FindStringSubmatch(line); m != nil {
				if h, err := strconv.Atoi(m[1]); err == nil && h > 0 {
					p.mu.Lock()
					p.info.Height = h
					p.mu.Unlock()
				}
			}

		case strings.Contains(line, "Frames"):
			if m := reFrames.FindStringSubmatch(line); m != nil {
				if f, err := strconv.ParseInt(m[1], 10, 64); err == nil && f > 0 {
					p.mu.Lock()
					p.info.NumFrames = f
					p.mu.Unlock()
				}
			}

		case strings.Contains(line, "FPS"):
			// A variable frame rate source is rejected outright, exactly as the
			// original did: the rest of the pipeline assumes a constant rate
			// even for VFR jobs, which are handled through a timecode file.
			if strings.Contains(line, "Variable") {
				return okerr.New(okerr.KindUnsupported, "不支持VFR输出",
					"VFR output not supported, even for VFR jobs.")
			}
			if m := reFPS.FindStringSubmatch(line); m != nil {
				p.mu.Lock()
				if n, err := strconv.ParseInt(m[1], 10, 64); err == nil && n > 0 {
					p.info.FpsNum = n
				}
				if n, err := strconv.ParseInt(m[2], 10, 64); err == nil && n > 0 {
					p.info.FpsDen = n
				}
				if f, err := strconv.ParseFloat(m[3], 64); err == nil && f > 0 {
					p.info.FPS = f
				}
				p.mu.Unlock()
			}

		case strings.Contains(line, "Format Name:"):
			if m := reFormatName.FindStringSubmatch(line); m != nil {
				p.mu.Lock()
				p.info.Format.Name = m[1]
				p.mu.Unlock()
			}

		case strings.Contains(line, "Color Family"):
			if m := reColorFam.FindStringSubmatch(line); m != nil {
				p.mu.Lock()
				p.info.Format.ColorFamilyName = m[1]
				p.mu.Unlock()
			}

		case strings.Contains(line, "Bits"):
			if m := reBits.FindStringSubmatch(line); m != nil {
				if b, err := strconv.Atoi(m[1]); err == nil && b > 0 {
					p.mu.Lock()
					p.info.Format.BitsPerSample = b
					p.mu.Unlock()
				}
			}

		case reLwi.MatchString(line):
			if m := reLwi.FindStringSubmatch(line); m != nil {
				if v, err := strconv.Atoi(m[1]); err == nil {
					p.report(jobproc.Progress{Percent: float64(v), Status: "建立索引"})
				}
			}
		}
		return nil
	}
}

func (p *Processor) report(prog jobproc.Progress) {
	p.mu.Lock()
	if prog.Percent >= 0 {
		p.progress = prog.Percent
	}
	sink := p.sink
	p.mu.Unlock()
	if sink != nil {
		sink.Report(prog)
	}
}

// checkFps compares the script's frame rate against the profile's.
//
// Mirrors VSPipeInfoProcessor.CheckFps: a VFR job takes the source's rate
// verbatim, a CFR job must match, and anything else is a hard failure.
func (p *Processor) checkFps() error {
	p.mu.Lock()
	info := p.info
	p.mu.Unlock()

	if info.FpsDen == 0 {
		return okerr.New(okerr.KindMismatch, okerr.ErrFpsMismatch.Summary,
			"vspipe 没有报告帧率，无法校验")
	}

	if p.opts.VFR {
		p.mu.Lock()
		p.info.VFR = true
		p.mu.Unlock()
		return nil
	}

	if info.FpsNum == p.opts.ExpectedFpsNum && info.FpsDen == p.opts.ExpectedFpsDen {
		return nil
	}

	// The legacy message renders both rates with three decimals.
	src := formatFps(p.opts.ExpectedFpsNum, p.opts.ExpectedFpsDen)
	dst := formatFps(info.FpsNum, info.FpsDen)
	return okerr.New(okerr.KindMismatch, okerr.ErrFpsMismatch.Summary,
		"json里指定帧率为%s，vs输出帧率为%s", src, dst)
}

func formatFps(num, den int64) string {
	if den == 0 {
		return "0.000"
	}
	return strconv.FormatFloat(float64(num)/float64(den), 'f', 3, 64)
}

// ErrNoOutput reports that vspipe produced no recognisable properties, which
// usually means the script failed silently.
var ErrNoOutput = errors.New("vspipeinfo: vspipe reported no video information")

// Validate reports whether the parsed information is usable. Callers use it to
// turn a silent failure into a clear message.
func (p *Processor) Validate() error {
	p.mu.Lock()
	info := p.info
	p.mu.Unlock()
	if info.NumFrames == 0 && info.Width == 0 && info.FpsDen == 0 {
		return okerr.New(okerr.KindTool, okerr.ErrVpy.Summary,
			"vspipe 没有输出任何视频信息，脚本可能没有执行到 set_output")
	}
	return nil
}
