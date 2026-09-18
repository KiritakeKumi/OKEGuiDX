// Package video holds the video-encoding processors.
//
// Every encoder in this package shares the same shape: a `vspipe --y4m`
// producer piped into an encoder binary, with the encoder's stderr parsed for
// frame counts, speed and bitrate. That shared behaviour lives in base.go so
// that x264/x265/SVT-AV1 wrappers only carry their own output patterns
// (WORKSTREAMS.md §1 P0-8).
package video

import (
	"context"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc"
	"github.com/KiritakeKumi/OKEGuiDX/internal/log"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
	"github.com/KiritakeKumi/OKEGuiDX/internal/proc"
)

// EncodeSpec describes one encode: a vspipe invocation feeding an encoder.
type EncodeSpec struct {
	// VSPipe is the absolute path to vspipe.
	VSPipe string
	// VSPipeArgs are the arguments passed to vspipe, excluding --y4m and the
	// script path, which the base class adds.
	VSPipeArgs []string
	// Script is the .vpy path.
	Script string
	// Encoder is the absolute path to the encoder binary.
	Encoder string
	// EncoderName is the short name used in logs and status, e.g. "x265".
	EncoderName string
	// EncoderArgs are the encoder's arguments, excluding the input and output
	// specification, which the base class adds.
	EncoderArgs []string
	// Output is the destination file.
	Output string
	// FrameStart and FrameEnd bound a partial encode. FrameEnd < 0 means "to
	// the end". Both are ignored when FrameStart == 0 && FrameEnd < 0.
	FrameStart int64
	FrameEnd   int64
	// TotalFrames is the expected frame count, used for progress percentages
	// and for detecting a truncated encode.
	TotalFrames int64
	// Priority is applied to both child processes.
	Priority proc.Priority
	// StdinArg supplies the encoder's input via a flag that names stdin, such
	// as "-" for x265. Required, because the input is a pipe.
	StdinArg string
	// InputArgs, when non-empty, replaces StdinArg. Use it for encoders whose
	// input specification is more than one token, such as x264's
	// "--demuxer y4m -". The output flag and path are appended after it either
	// way.
	InputArgs []string
}

// Parser extracts progress from one line of encoder output. Implementations are
// supplied by the concrete encoders.
type Parser interface {
	// Parse inspects a line and updates the given progress. It returns true when
	// the line carried progress information.
	Parse(line string) (jobproc.Progress, bool, error)
}

// Base implements the shared pipeline and progress bookkeeping.
type Base struct {
	name      string
	spec      EncodeSpec
	parser    Parser
	fatalLine func(string) error

	mu       sync.Mutex
	prog     jobproc.Progress
	sink     jobproc.ProgressSink
	producer *proc.Process
	encoder  *proc.Process
	started  time.Time
	finished bool
}

// NewBase returns a Base for the given spec.
func NewBase(spec EncodeSpec) *Base {
	if spec.Priority == 0 {
		spec.Priority = proc.DefaultPriority
	}
	return &Base{name: spec.EncoderName, spec: spec}
}

// SetParser installs the encoder-specific output parser. Concrete encoders call
// this from their constructor.
func (b *Base) SetParser(p Parser) { b.parser = p }

// Name implements jobproc.Processor.
func (b *Base) Name() string { return b.name }

// Run starts the vspipe producer, wires its stdout into the encoder's stdin and
// blocks until both processes finish.
func (b *Base) Run(ctx context.Context, sink jobproc.ProgressSink) error {
	if sink == nil {
		sink = jobproc.NopSink{}
	}
	b.mu.Lock()
	b.sink = sink
	b.started = time.Now()
	b.mu.Unlock()

	producer, err := proc.Start(proc.Spec{
		Path:     b.spec.VSPipe,
		Args:     b.vspipeArgs(),
		Priority: b.spec.Priority,
		Name:     "vspipe",
	})
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.producer = producer
	b.mu.Unlock()

	encoder, err := proc.Start(proc.Spec{
		Path:     b.spec.Encoder,
		Args:     b.encoderArgs(),
		Priority: b.spec.Priority,
		Name:     b.spec.EncoderName,
	})
	if err != nil {
		_ = producer.Kill()
		_ = producer.Close()
		return err
	}
	b.mu.Lock()
	b.encoder = encoder
	b.mu.Unlock()

	// Wire the pipeline in-process. This is the zero-shell replacement for the
	// legacy `cmd.exe /c "vspipe ... | x265 ..."` (INVENTORY.md §2 #9).
	var pipeWG sync.WaitGroup
	pipeWG.Add(1)
	go func() {
		defer pipeWG.Done()
		_, copyErr := io.Copy(encoder.Stdin(), producer.Stdout())
		if copyErr != nil && !isClosedPipe(copyErr) {
			log.Debug("管道中断", "err", copyErr)
		}
		// Closing the encoder's stdin signals end-of-input; without this the
		// encoder would wait forever.
		_ = encoder.Stdin().Close()
	}()

	// Cancellation kills both ends.
	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go func() {
		select {
		case <-ctx.Done():
			_ = producer.Kill()
			_ = encoder.Kill()
		case <-stopWatch:
		}
	}()

	// vspipe's stdout belongs to the pipe; only its stderr is parsed here.
	producerErr := make(chan error, 1)
	go func() { producerErr <- producer.FinishStderr(b.vspipeLineHandler()) }()

	encoderErr := encoder.Finish(nil, b.encoderLineHandler())

	pipeWG.Wait()
	perr := <-producerErr

	b.mu.Lock()
	b.finished = true
	b.mu.Unlock()

	if ctx.Err() != nil {
		return okerr.Wrap(ctx.Err(), okerr.KindCanceled, "任务已取消", "%s 已被终止", b.name)
	}
	if perr != nil && !isCanceledProcessErr(perr) {
		return okerr.Wrap(perr, okerr.KindTool, okerr.ErrVpy.Summary, "vspipe 异常退出")
	}
	if encoderErr != nil {
		return b.wrapEncoderFailure(encoderErr)
	}

	// A finished encode that produced fewer frames than the source means the
	// pipeline died halfway. The legacy code reported this as a crash
	// (Constants.vsCrashSmr) and kept the partial file.
	b.mu.Lock()
	done := b.prog.FramesDone
	b.mu.Unlock()
	if b.spec.TotalFrames > 0 && done > 0 && done < b.spec.TotalFrames {
		return okerr.New(okerr.KindToolCrash, okerr.ErrVSCrash.Summary,
			"编码只完成 %d/%d 帧，预计是 vs 或编码器崩溃", done, b.spec.TotalFrames)
	}
	return nil
}

// Close implements jobproc.Processor.
func (b *Base) Close() error {
	b.mu.Lock()
	prod, enc := b.producer, b.encoder
	b.mu.Unlock()
	if prod != nil {
		_ = prod.Close()
	}
	if enc != nil {
		_ = enc.Close()
	}
	return nil
}

// Pause implements jobproc.Controllable.
func (b *Base) Pause() error {
	b.mu.Lock()
	prod, enc := b.producer, b.encoder
	b.mu.Unlock()
	// The encoder is paused first so that it stops draining the pipe; pausing
	// the producer first would fill the pipe buffer and block.
	if enc != nil {
		if err := enc.Pause(); err != nil {
			return err
		}
	}
	if prod != nil {
		if err := prod.Pause(); err != nil {
			return err
		}
	}
	return nil
}

// Resume implements jobproc.Controllable.
func (b *Base) Resume() error {
	b.mu.Lock()
	prod, enc := b.producer, b.encoder
	b.mu.Unlock()
	if prod != nil {
		if err := prod.Resume(); err != nil {
			return err
		}
	}
	if enc != nil {
		if err := enc.Resume(); err != nil {
			return err
		}
	}
	return nil
}

// SetPriority implements jobproc.Prioritizable.
func (b *Base) SetPriority(p jobproc.Priority) error {
	b.mu.Lock()
	prod, enc := b.producer, b.encoder
	b.mu.Unlock()
	pp := proc.Priority(p)
	if prod != nil {
		if err := prod.SetPriority(pp); err != nil {
			return err
		}
	}
	if enc != nil {
		if err := enc.SetPriority(pp); err != nil {
			return err
		}
	}
	return nil
}

// Progress returns the latest progress snapshot.
func (b *Base) Progress() jobproc.Progress {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.prog
}

// update merges a partial progress into the snapshot and forwards it to the
// sink. Fields left at their zero value keep their previous value.
func (b *Base) update(p jobproc.Progress) {
	b.mu.Lock()
	if p.Percent != 0 || p.FramesDone != 0 {
		b.prog.Percent = p.Percent
	}
	if p.Speed != "" {
		b.prog.Speed = p.Speed
	}
	if p.BitRate != "" {
		b.prog.BitRate = p.BitRate
	}
	if p.TimeRemainSeconds != 0 {
		b.prog.TimeRemainSeconds = p.TimeRemainSeconds
	}
	if p.FramesDone != 0 {
		b.prog.FramesDone = p.FramesDone
	}
	if p.FramesTotal != 0 {
		b.prog.FramesTotal = p.FramesTotal
	}
	if p.Status != "" {
		b.prog.Status = p.Status
	}
	snapshot := b.prog
	sink := b.sink
	b.mu.Unlock()
	if sink != nil {
		sink.Report(snapshot)
	}
}

// Fail forwards an error to the sink as a final progress update. Concrete
// encoders call it from their fatal-line hook when they want the UI to show a
// terminal status before the run unwinds.
func (b *Base) Fail(err error) {
	e := okerr.AsError(err)
	b.update(jobproc.Progress{Status: e.Summary})
}

func (b *Base) vspipeArgs() []string {
	args := []string{"--y4m"}
	if b.spec.FrameStart > 0 || b.spec.FrameEnd >= 0 {
		if b.spec.FrameStart > 0 {
			args = append(args, "-s", strconv.FormatInt(b.spec.FrameStart, 10))
		}
		if b.spec.FrameEnd >= 0 {
			// vspipe's -e is inclusive, while the profile's slice end is
			// exclusive, hence the -1 (mirrors the legacy BuildCommandline).
			args = append(args, "-e", strconv.FormatInt(b.spec.FrameEnd-1, 10))
		}
	}
	for _, a := range b.spec.VSPipeArgs {
		args = append(args, "--arg", a)
	}
	args = append(args, b.spec.Script, "-")
	return args
}

func (b *Base) encoderArgs() []string {
	input := b.spec.InputArgs
	if len(input) == 0 {
		input = []string{b.spec.StdinArg}
	}
	args := make([]string, 0, len(b.spec.EncoderArgs)+len(input)+2)
	args = append(args, b.spec.EncoderArgs...)
	args = append(args, input...)
	args = append(args, "-o", b.spec.Output)
	return args
}

// encoderLineHandler feeds encoder output into the concrete parser.
func (b *Base) encoderLineHandler() proc.LineFunc {
	return func(line string) error {
		if b.fatalLine != nil {
			if err := b.fatalLine(line); err != nil {
				return err
			}
		}
		if b.parser == nil {
			return nil
		}
		p, ok, err := b.parser.Parse(line)
		if err != nil {
			return err
		}
		if ok {
			b.update(p)
		}
		return nil
	}
}

// SetFatalLine installs a hook that inspects each encoder output line for
// unrecoverable errors. Returning a non-nil error aborts the run.
func (b *Base) SetFatalLine(fn func(string) error) { b.fatalLine = fn }

// Spec returns the encoder specification this Base was built from. Encoders use
// it in tests to assert the argument shape without starting a process.
func (b *Base) Spec() EncodeSpec { return b.spec }

// vspipeLineHandler surfaces Python exceptions raised by the script.
func (b *Base) vspipeLineHandler() proc.LineFunc {
	var inError bool
	var buf strings.Builder
	return func(line string) error {
		switch {
		case strings.Contains(line, "Python exception:"):
			inError = true
			buf.Reset()
		case inError:
			buf.WriteString(line)
			buf.WriteByte('\n')
			if vpyErrorLine.MatchString(line) {
				return okerr.New(okerr.KindTool, okerr.ErrVpy.Summary, "%s", strings.TrimSpace(buf.String()))
			}
		}
		return nil
	}
}

// vpyErrorLine matches the terminal line of a VapourSynth traceback.
var vpyErrorLine = regexp.MustCompile(`^[a-zA-Z_.]*(Error|Exception|Exit|Interrupt|Iteration|Warning)(.*)`)

// wrapEncoderFailure converts a process error into a structured encoder error.
func (b *Base) wrapEncoderFailure(err error) error {
	e := okerr.AsError(err)
	if e.Kind == okerr.KindTool || e.Kind == okerr.KindToolCrash {
		return e
	}
	return okerr.Wrap(err, okerr.KindTool, b.name+"出错", "%s 异常退出", b.name).
		WithTool(b.name, e.ExitCode)
}

// ParseFunc adapts a function to Parser.
type ParseFunc func(line string) (jobproc.Progress, bool, error)

// Parse implements Parser.
func (f ParseFunc) Parse(line string) (jobproc.Progress, bool, error) { return f(line) }

// FPSFromFrames computes the instantaneous rate and the remaining time. It is
// exported for the concrete encoders, which report speed in their own dialect
// but share this calculation.
func FPSFromFrames(done, total int64, elapsed time.Duration) (string, float64) {
	if done <= 0 || elapsed <= 0 {
		return "", -1
	}
	fps := float64(done) / elapsed.Seconds()
	speed := strconv.FormatFloat(fps, 'f', 2, 64) + " fps"
	remain := -1.0
	if total > done {
		remain = float64(total-done) / fps
	}
	return speed, remain
}

func isClosedPipe(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "file already closed") ||
		strings.Contains(s, "pipe is being closed") ||
		strings.Contains(s, "broken pipe") ||
		strings.Contains(s, "The pipe has been ended")
}

func isCanceledProcessErr(err error) bool {
	if err == nil {
		return false
	}
	e := okerr.AsError(err)
	return e.Kind == okerr.KindCanceled
}
