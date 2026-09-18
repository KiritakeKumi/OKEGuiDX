//go:build !windows

package proc

import (
	"fmt"
	"os/exec"
	"syscall"
)

// sysProcAttr creates a new session so that the child becomes a process group
// leader. Signalling the group then reaches every descendant, including the
// encoder behind a vspipe pipe.
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

func setPriorityOS(pid int, prio Priority) error {
	// Unix expresses priority as a niceness value; -20 is highest, 19 lowest.
	var nice int
	switch prio {
	case PriorityIdle:
		nice = 19
	case PriorityBelowNormal:
		nice = 10
	case PriorityNormal, PriorityParallel:
		nice = 0
	case PriorityAboveNormal:
		nice = -5
	case PriorityHigh:
		nice = -10
	default:
		nice = 10
	}
	// Lowering niceness requires privileges; an EPERM here is expected and not
	// worth failing the run over.
	if err := syscall.Setpriority(syscall.PRIO_PROCESS, pid, nice); err != nil {
		return fmt.Errorf("setpriority(%d): %w", nice, err)
	}
	return nil
}

// unixTree controls the process group created by sysProcAttr.
type unixTree struct {
	pgid int
}

func newTreeController(pid int) (treeController, error) {
	// The child is its own group leader, so its pgid equals its pid.
	return &unixTree{pgid: pid}, nil
}

func (t *unixTree) signal(sig syscall.Signal) error {
	if t.pgid <= 0 {
		return nil
	}
	// Negative pid means "the whole process group".
	if err := syscall.Kill(-t.pgid, sig); err != nil {
		return fmt.Errorf("kill(-%d, %v): %w", t.pgid, sig, err)
	}
	return nil
}

func (t *unixTree) Pause() error  { return t.signal(syscall.SIGSTOP) }
func (t *unixTree) Resume() error { return t.signal(syscall.SIGCONT) }

func (t *unixTree) Kill() error {
	// SIGKILL first: an encoder mid-write cannot be expected to honour SIGTERM
	// promptly, and the legacy code killed unconditionally too.
	if err := t.signal(syscall.SIGKILL); err != nil {
		return err
	}
	t.pgid = 0
	return nil
}

func (t *unixTree) Close() error { return nil }

var _ treeController = (*unixTree)(nil)
var _ = exec.Command
