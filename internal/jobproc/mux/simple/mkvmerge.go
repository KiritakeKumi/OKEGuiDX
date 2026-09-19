// Package simple wraps the three mkvmerge invocations the re-encode pipeline
// uses: muxing one encoded part, appending the parts into a single video, and
// merging that video with the tracks of the previous release.
//
// Behaviour is defined by JobProcessor/Muxer/MkvmergeMuxer.cs and its
// subclasses SingleVideoMuxer, AppendVideoMuxer and MergeOldRemuxer. The legacy
// code assembled one command-line string and let cmd.exe split it; here every
// option is a separate argv element.
//
// One deliberate deviation: the legacy code only logged mkvmerge's exit code 2
// and let the job continue. A failed mux must not be reported as a success, so
// code 2 (mkvmerge's "error") becomes a structured error here while code 1
// ("warnings") stays a log entry.
package simple

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc"
	"github.com/KiritakeKumi/OKEGuiDX/internal/log"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
	"github.com/KiritakeKumi/OKEGuiDX/internal/proc"
)

// Options are shared by the three muxers.
type Options struct {
	// Mkvmerge is the absolute path to mkvmerge.
	Mkvmerge string
	// Output is the destination container.
	Output string
	// SourceFile is the task's source file. It is used only to fill the
	// "该文件X将跳过处理" part of the legacy error message and may be empty.
	SourceFile string
	// Priority is applied to the child process. The zero value selects
	// proc.DefaultPriority, as in every other processor.
	Priority proc.Priority
}

// baseArgs is MkvmergeMuxer.BuildCommandline.
func baseArgs(opts Options) []string {
	return []string{"--ui-language", "en", "--output", opts.Output}
}

// Output markers taken from MkvmergeMuxer.ProcessLine.
const (
	errorMarker     = "Error: "
	muxingMarker    = "Muxing took"
	multiplexMarker = "Multiplexing took"
)

// reProgress matches the "Progress: N%" lines mkvmerge prints. The legacy code
// used the lazy pattern `Progress: (\d*?)%` followed by double.TryParse, so a
// line whose capture was not a number was ignored.
var reProgress = regexp.MustCompile(`Progress: ([0-9]+(?:\.[0-9]+)?)%`)

// runner is the shared lifecycle of the three muxers.
type runner struct {
	name       string
	tool       string
	args       []string
	sourceFile string
	priority   proc.Priority

	mu      sync.Mutex
	proc    *proc.Process
	sink    jobproc.ProgressSink
	prog    jobproc.Progress
	lineErr error
}

func newRunner(name string, opts Options, args []string) *runner {
	priority := opts.Priority
	if priority == 0 {
		priority = proc.DefaultPriority
	}
	return &runner{
		name:       name,
		tool:       opts.Mkvmerge,
		args:       args,
		sourceFile: opts.SourceFile,
		priority:   priority,
	}
}

// Name implements jobproc.Processor.
func (r *runner) Name() string { return r.name }

// Args returns the mkvmerge arguments this processor runs with.
func (r *runner) Args() []string { return append([]string(nil), r.args...) }

// Run implements jobproc.Processor.
func (r *runner) Run(ctx context.Context, sink jobproc.ProgressSink) error {
	if sink == nil {
		sink = jobproc.NopSink{}
	}
	r.mu.Lock()
	r.sink = sink
	r.mu.Unlock()

	p, err := proc.Start(proc.Spec{
		Path:     r.tool,
		Args:     r.args,
		Priority: r.priority,
		Name:     "mkvmerge",
	})
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.proc = p
	r.mu.Unlock()

	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go func() {
		select {
		case <-ctx.Done():
			_ = p.Kill()
		case <-stopWatch:
		}
	}()

	// mkvmerge prints progress on stdout and errors on stderr; the legacy code
	// parsed both streams identically, so both are handled here.
	finishErr := p.Finish(r.onLine, r.onLine)

	r.mu.Lock()
	lineErr := r.lineErr
	r.mu.Unlock()

	if ctx.Err() != nil {
		return okerr.Wrap(ctx.Err(), okerr.KindCanceled, "任务已取消", "%s 已被终止", r.name)
	}
	if lineErr != nil {
		return lineErr
	}
	if finishErr != nil {
		return r.exitError(p, finishErr)
	}
	return nil
}

// exitError turns a non-zero exit into a structured error. mkvmerge uses exit
// code 1 for warnings and 2 for errors; the legacy code only logged code 2, but
// a run that failed without printing an "Error:" line has to fail here too.
func (r *runner) exitError(p *proc.Process, err error) error {
	code := p.ExitCode()
	switch code {
	case 0:
		// A read failure rather than an exit failure; proc already wrapped it.
		return err
	case 1:
		log.Warn("mkvmerge以警告结束", "code", code)
		return nil
	case 2:
		log.Error("mkvmerge封装出错")
	}
	return okerr.Wrap(err, okerr.KindTool, okerr.ErrMkvmerge.Summary, "mkvmerge 退出代码 %d", code).
		WithTool("mkvmerge", code).
		WithOutput(p.RecentOutput()).
		WithFile(r.sourceFile)
}

// onLine mirrors MkvmergeMuxer.ProcessLine. The legacy code stored the first
// exception and kept reading, so a failure is recorded rather than returned.
func (r *runner) onLine(line string) error {
	log.Debug("mkvmerge输出", "line", line)

	if strings.Contains(line, errorMarker) {
		// The legacy code reported line.Substring(7), i.e. it cut at a fixed
		// position rather than at the marker. The .NET StreamReader had already
		// decoded the line as UTF-8 with replacement, hence ToValidUTF8.
		detail := strings.ToValidUTF8(line[len(errorMarker):], "\uFFFD")
		r.fail(okerr.New(okerr.KindTool, okerr.ErrMkvmerge.Summary, "%s", line).
			WithOutput(detail).
			WithFile(r.sourceFile))
	}
	if m := reProgress.FindStringSubmatch(line); m != nil {
		if p, err := strconv.ParseFloat(m[1], 64); err == nil && p > 1 {
			r.report(p)
		}
	}
	if strings.Contains(line, muxingMarker) || strings.Contains(line, multiplexMarker) {
		r.report(100)
	}
	return nil
}

// fail keeps the first failure seen on either output stream.
func (r *runner) fail(err error) {
	r.mu.Lock()
	if r.lineErr == nil {
		r.lineErr = err
	}
	r.mu.Unlock()
}

// report updates the progress snapshot and forwards it to the sink.
func (r *runner) report(percent float64) {
	r.mu.Lock()
	r.prog.Percent = percent
	snapshot := r.prog
	sink := r.sink
	r.mu.Unlock()
	if sink != nil {
		sink.Report(snapshot)
	}
}

// Close implements jobproc.Processor.
func (r *runner) Close() error {
	if p := r.process(); p != nil {
		return p.Close()
	}
	return nil
}

// Pause implements jobproc.Controllable.
func (r *runner) Pause() error {
	if p := r.process(); p != nil {
		return p.Pause()
	}
	return nil
}

// Resume implements jobproc.Controllable.
func (r *runner) Resume() error {
	if p := r.process(); p != nil {
		return p.Resume()
	}
	return nil
}

// SetPriority implements jobproc.Prioritizable.
func (r *runner) SetPriority(p jobproc.Priority) error {
	child := r.process()
	if child == nil {
		return nil
	}
	return child.SetPriority(proc.Priority(p))
}

// Progress returns the latest progress snapshot.
func (r *runner) Progress() jobproc.Progress {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.prog
}

func (r *runner) process() *proc.Process {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.proc
}
