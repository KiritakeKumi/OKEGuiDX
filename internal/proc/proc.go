// Package proc starts and controls external tools.
//
// Two rules shape this package:
//
//  1. No shell. The legacy code ran everything through
//     `cmd.exe /c "vspipe ... | x265 ..."`, which forced fragile quote escaping
//     and made Windows-only behaviour unavoidable (INVENTORY.md §2 #9). Here,
//     pipelines are wired with io.Pipe and arguments are passed as a slice.
//
//  2. Every process is owned by a job/process group so that pause, resume and
//     kill affect the whole tree, including grandchildren such as the encoder
//     behind vspipe (INVENTORY.md §2 #8).
package proc

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/KiritakeKumi/OKEGuiDX/internal/log"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
)

// Priority mirrors the legacy ProcessPriority enum. PARALLEL has no direct
// mapping on either platform and behaves like NORMAL.
type Priority int

// Priorities.
const (
	PriorityIdle Priority = iota
	PriorityBelowNormal
	PriorityNormal
	PriorityAboveNormal
	PriorityHigh
	PriorityParallel
)

// String implements fmt.Stringer.
func (p Priority) String() string {
	switch p {
	case PriorityIdle:
		return "idle"
	case PriorityBelowNormal:
		return "below-normal"
	case PriorityNormal:
		return "normal"
	case PriorityAboveNormal:
		return "above-normal"
	case PriorityHigh:
		return "high"
	default:
		return "parallel"
	}
}

// DefaultPriority is what the legacy CommandlineJobProcessor applied to every
// child process.
const DefaultPriority = PriorityBelowNormal

// Spec describes a process to start.
type Spec struct {
	// Path is the executable. It must not rely on PATH lookup subtleties: the
	// toolchain always resolves an absolute path.
	Path string
	// Args are the arguments, passed verbatim without shell interpretation.
	Args []string
	// Dir is the working directory. Empty means inherit.
	Dir string
	// Env is the full environment. Nil means inherit.
	Env []string
	// Priority is applied right after start. Zero value is PriorityIdle, so
	// callers that do not care should use DefaultPriority explicitly.
	Priority Priority
	// Name is used in log output; it defaults to the executable's base name.
	Name string
}

// Process is a running external tool.
type Process struct {
	cmd  *exec.Cmd
	spec Spec
	name string

	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser

	mu       sync.Mutex
	waitErr  error
	exited   bool
	exitCode int
	done     chan struct{}
	// job holds the OS-level container used for tree control.
	job treeController

	// capture ring buffer of the most recent output lines, used to explain
	// failures without keeping the whole (potentially huge) log in memory.
	capture *lineRing
}

// LineFunc receives one line of output. Returning an error stops the reader.
type LineFunc func(line string) error

// Start launches the process and wires its pipes.
func Start(spec Spec) (*Process, error) {
	if spec.Path == "" {
		return nil, okerr.New(okerr.KindNotFound, "找不到外部工具", "未指定可执行文件路径")
	}
	if _, err := os.Stat(spec.Path); err != nil {
		return nil, okerr.Wrap(err, okerr.KindNotFound, "找不到外部工具", "%s 不存在", spec.Path)
	}

	cmd := exec.Command(spec.Path, spec.Args...) //nolint:gosec // the toolchain resolves every path; args are never shell-interpreted
	cmd.Dir = spec.Dir
	if spec.Env != nil {
		cmd.Env = spec.Env
	}
	// A process group is what makes tree-wide signals possible on Unix.
	cmd.SysProcAttr = sysProcAttr()

	// Output is captured through pipes this package owns, not through
	// exec.Cmd's StdoutPipe/StderrPipe helpers. Those helpers tie the parent's
	// read end to cmd.Wait, which closes it the instant the child exits; a
	// reader that has not finished draining yet then silently loses the tail of
	// the output. Since the whole design hinges on pipelines, the read ends must
	// outlive the reap and close only when every writer is gone.
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		return nil, okerr.Wrap(err, okerr.KindIO, "无法建立管道", "stdout: %v", err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		return nil, okerr.Wrap(err, okerr.KindIO, "无法建立管道", "stderr: %v", err)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		closeAll(stdoutR, stdoutW, stderrR, stderrW)
		return nil, okerr.Wrap(err, okerr.KindIO, "无法建立管道", "stdin: %v", err)
	}
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW

	name := spec.Name
	if name == "" {
		name = baseName(spec.Path)
	}
	p := &Process{
		cmd:     cmd,
		spec:    spec,
		name:    name,
		stdin:   stdin,
		stdout:  stdoutR,
		stderr:  stderrR,
		done:    make(chan struct{}),
		capture: newLineRing(40),
	}

	log.Info("启动进程", "tool", name, "args", strings.Join(spec.Args, " "))
	if err := cmd.Start(); err != nil {
		closeAll(stdoutR, stdoutW, stderrR, stderrW)
		return nil, okerr.Wrap(err, okerr.KindTool, "无法启动外部工具", "%s: %v", spec.Path, err)
	}
	// The child holds its own copies of the write ends; drop ours so that EOF
	// is reported as soon as the child (and any grandchild it spawned) exits.
	_ = stdoutW.Close()
	_ = stderrW.Close()

	// Put the child into an OS container so pause/resume/kill reach the whole
	// tree. Failure here is not fatal: the process still runs, we just lose
	// tree-wide control.
	if j, err := newTreeController(cmd.Process.Pid); err != nil {
		log.Warn("无法创建进程组容器，暂停/终止将只作用于直接子进程", "tool", name, "err", err)
	} else {
		p.job = j
	}

	p.setPriority(spec.Priority) //nolint:errcheck // a failure here only loses the priority hint

	go func() {
		err := cmd.Wait()
		p.mu.Lock()
		p.waitErr = err
		p.exited = true
		if cmd.ProcessState != nil {
			p.exitCode = cmd.ProcessState.ExitCode()
		}
		p.mu.Unlock()
		if p.job != nil {
			_ = p.job.Close()
		}
		close(p.done)
	}()

	return p, nil
}

// Name returns the tool name used in logs.
func (p *Process) Name() string { return p.name }

// PID returns the OS process id.
func (p *Process) PID() int {
	if p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

// Stdin returns the writable end of the child's standard input.
func (p *Process) Stdin() io.WriteCloser { return p.stdin }

// Stdout returns the readable end of the child's standard output.
func (p *Process) Stdout() io.ReadCloser { return p.stdout }

// Stderr returns the readable end of the child's standard error.
func (p *Process) Stderr() io.ReadCloser { return p.stderr }

// Done returns a channel closed when the process has exited.
func (p *Process) Done() <-chan struct{} { return p.done }

// Exited reports whether the process has already terminated.
func (p *Process) Exited() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exited
}

// ExitCode returns the exit code; only meaningful after Done is closed.
func (p *Process) ExitCode() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exitCode
}

// Wait blocks until the process exits and returns its error, if any.
//
// Wait does not close the output pipes: readers may still be draining them.
// Use Finish (or FinishStderr) to consume output and reap in the correct order.
func (p *Process) Wait() error {
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.waitErr
}

// WaitTimeout blocks until the process exits or the timeout elapses. It reports
// whether the process exited.
func (p *Process) WaitTimeout(d time.Duration) bool {
	select {
	case <-p.done:
		return true
	case <-time.After(d):
		return false
	}
}

// Kill terminates the whole process tree and waits for it to disappear.
func (p *Process) Kill() error {
	if p.Exited() {
		return nil
	}
	log.Debug("终止进程", "tool", p.name, "pid", p.PID())
	var err error
	if p.job != nil {
		err = p.job.Kill()
	} else if p.cmd.Process != nil {
		err = p.cmd.Process.Kill()
	}
	if err != nil && !errors.Is(err, os.ErrProcessDone) {
		return okerr.Wrap(err, okerr.KindTool, "无法终止外部工具", "%s: %v", p.name, err)
	}
	p.WaitTimeout(10 * time.Second)
	return nil
}

// Pause suspends the whole process tree.
func (p *Process) Pause() error {
	if p.Exited() {
		return okerr.New(okerr.KindTool, "无法暂停", "%s 已经退出", p.name)
	}
	if p.job == nil {
		return okerr.New(okerr.KindTool, "无法暂停", "%s 没有可控的进程组", p.name)
	}
	if err := p.job.Pause(); err != nil {
		return okerr.Wrap(err, okerr.KindTool, "无法暂停", "%s: %v", p.name, err)
	}
	log.Debug("暂停进程", "tool", p.name)
	return nil
}

// Resume undoes Pause.
func (p *Process) Resume() error {
	if p.Exited() {
		return nil
	}
	if p.job == nil {
		return okerr.New(okerr.KindTool, "无法恢复", "%s 没有可控的进程组", p.name)
	}
	if err := p.job.Resume(); err != nil {
		return okerr.Wrap(err, okerr.KindTool, "无法恢复", "%s: %v", p.name, err)
	}
	log.Debug("恢复进程", "tool", p.name)
	return nil
}

// SetPriority changes the process priority.
func (p *Process) SetPriority(prio Priority) error {
	if p.Exited() {
		return okerr.New(okerr.KindTool, "无法调整优先级", "%s 已经退出", p.name)
	}
	if err := p.setPriority(prio); err != nil {
		return okerr.Wrap(err, okerr.KindTool, "无法调整优先级", "%s: %v", p.name, err)
	}
	return nil
}

func (p *Process) setPriority(prio Priority) error {
	if p.cmd.Process == nil {
		return nil
	}
	if err := setPriorityOS(p.cmd.Process.Pid, prio); err != nil {
		log.Debug("设置优先级失败", "tool", p.name, "priority", prio.String(), "err", err)
		return err
	}
	return nil
}

// RecentOutput returns the last lines the process wrote, for error reporting.
func (p *Process) RecentOutput() string { return p.capture.String() }

// Consume reads both output streams, invoking the handlers for every line, and
// blocks until both streams are exhausted. It returns the first handler error.
//
// Handlers may be nil, in which case the lines are only logged and captured.
// A tool that is the left-hand side of a pipeline must NOT use this: its stdout
// belongs to the downstream process. Use ConsumeStderr for that case.
func (p *Process) Consume(onStdout, onStderr LineFunc) error {
	var wg sync.WaitGroup
	errs := make([]error, 2)

	wg.Add(2)
	go func() {
		defer wg.Done()
		errs[0] = p.scan(p.stdout, "stdout", onStdout)
	}()
	go func() {
		defer wg.Done()
		errs[1] = p.scan(p.stderr, "stderr", onStderr)
	}()
	wg.Wait()

	if errs[0] != nil {
		return errs[0]
	}
	return errs[1]
}

// ConsumeStdout drains and parses only standard output, leaving standard error
// available to the caller.
func (p *Process) ConsumeStdout(fn LineFunc) error {
	return p.scan(p.stdout, "stdout", fn)
}

// ConsumeStderr drains and parses only standard error. This is what a pipeline
// producer uses: stdout is being piped into the next process, while stderr
// carries the tool's diagnostics and progress.
func (p *Process) ConsumeStderr(fn LineFunc) error {
	return p.scan(p.stderr, "stderr", fn)
}

func (p *Process) scan(r io.Reader, stream string, fn LineFunc) error {
	sc := bufio.NewScanner(r)
	// Encoder progress lines are long but bounded; 1 MiB is far above anything
	// the tools emit on a single line while still bounding memory.
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		p.capture.Add(line)
		log.Trace("进程输出", "tool", p.name, "stream", stream, "line", line)
		if fn == nil {
			continue
		}
		if err := fn(line); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.ErrClosedPipe) && !errors.Is(err, os.ErrClosed) {
		return okerr.Wrap(err, okerr.KindIO, "读取外部工具输出失败", "%s: %v", p.name, err)
	}
	return nil
}

// WaitWithContext waits for the process, killing it when ctx is canceled.
func (p *Process) WaitWithContext(ctx context.Context) error {
	select {
	case <-ctx.Done():
		_ = p.Kill()
		<-p.done
		return okerr.Wrap(ctx.Err(), okerr.KindCanceled, "任务已取消", "%s 已被终止", p.name)
	case <-p.done:
		return p.Wait()
	}
}

// Close releases the process's pipes. It is safe to call more than once and is
// intended for callers that never drain the streams (for example when a process
// failed to start or was abandoned).
func (p *Process) Close() error {
	var first error
	for _, c := range []io.Closer{p.stdin, p.stdout, p.stderr} {
		if c == nil {
			continue
		}
		if err := c.Close(); err != nil && first == nil && !errors.Is(err, os.ErrClosed) {
			first = err
		}
	}
	return first
}

// Finish consumes both output streams and then waits for the process to exit,
// returning the first error encountered.
//
// Prefer this over calling Consume and Wait separately so that readers always
// finish before the process is reaped.
func (p *Process) Finish(onStdout, onStderr LineFunc) error {
	consumeErr := p.Consume(onStdout, onStderr)
	waitErr := p.Wait()
	if consumeErr != nil {
		return consumeErr
	}
	return waitErr
}

// FinishStderr drains standard error and waits, leaving standard output to a
// downstream process. Use this for pipeline producers.
func (p *Process) FinishStderr(onStderr LineFunc) error {
	consumeErr := p.ConsumeStderr(onStderr)
	waitErr := p.Wait()
	if consumeErr != nil {
		return consumeErr
	}
	return waitErr
}

// lineRing keeps the last n lines of output.
type lineRing struct {
	mu    sync.Mutex
	lines []string
	n     int
	next  int
	full  bool
}

func newLineRing(n int) *lineRing { return &lineRing{lines: make([]string, n), n: n} }

func (r *lineRing) Add(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines[r.next] = line
	r.next = (r.next + 1) % r.n
	if r.next == 0 {
		r.full = true
	}
}

func (r *lineRing) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	if r.full {
		out = append(out, r.lines[r.next:]...)
		out = append(out, r.lines[:r.next]...)
	} else {
		out = append(out, r.lines[:r.next]...)
	}
	return strings.Join(out, "\n")
}

// RunResult is the outcome of a one-shot command.
type RunResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
	// LastOutput is the tail of the combined output, handy for error messages.
	LastOutput string
}

// Run executes a tool to completion, capturing its output. It is meant for
// short-lived helper invocations such as `x265 --version` or `ffprobe`.
func Run(ctx context.Context, spec Spec) (*RunResult, error) {
	p, err := Start(spec)
	if err != nil {
		return nil, err
	}
	_ = p.Stdin().Close()

	var (
		mu     sync.Mutex
		stdout strings.Builder
		stderr strings.Builder
	)
	onOut := func(line string) error {
		mu.Lock()
		defer mu.Unlock()
		stdout.WriteString(line)
		stdout.WriteByte('\n')
		return nil
	}
	onErr := func(line string) error {
		mu.Lock()
		defer mu.Unlock()
		stderr.WriteString(line)
		stderr.WriteByte('\n')
		return nil
	}

	consumeDone := make(chan error, 1)
	go func() { consumeDone <- p.Consume(onOut, onErr) }()

	waitErr := p.WaitWithContext(ctx)
	<-consumeDone

	res := &RunResult{
		ExitCode:   p.ExitCode(),
		Stdout:     stdout.String(),
		Stderr:     stderr.String(),
		LastOutput: p.RecentOutput(),
	}
	if waitErr != nil {
		return res, okerr.Wrap(waitErr, okerr.KindTool, "外部工具执行失败",
			"%s 退出代码 %d", p.Name(), res.ExitCode).
			WithTool(p.Name(), res.ExitCode).
			WithOutput(res.LastOutput)
	}
	return res, nil
}

func baseName(path string) string {
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		return path[i+1:]
	}
	return path
}

func closeAll(closers ...io.Closer) {
	for _, c := range closers {
		if c != nil {
			_ = c.Close()
		}
	}
}

// Ensure fmt is used even when all error paths are compiled out.
var _ = fmt.Sprintf
