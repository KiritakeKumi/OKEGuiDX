//go:build windows

package proc

import (
	"fmt"
	"os/exec"
	"syscall"
	"unsafe"
)

// Windows process priority classes.
const (
	idlePriorityClass        = 0x00000040
	belowNormalPriorityClass = 0x00004000
	normalPriorityClass      = 0x00000020
	aboveNormalPriorityClass = 0x00008000
	highPriorityClass        = 0x00000080
)

var (
	kernel32                    = syscall.NewLazyDLL("kernel32.dll")
	procSetPriorityClass        = kernel32.NewProc("SetPriorityClass")
	procCreateJobObjectW        = kernel32.NewProc("CreateJobObjectW")
	procAssignProcessToJob      = kernel32.NewProc("AssignProcessToJobObject")
	procTerminateJobObject      = kernel32.NewProc("TerminateJobObject")
	procSetInformationJobObject = kernel32.NewProc("SetInformationJobObject")
	procOpenProcess             = kernel32.NewProc("OpenProcess")
	procCloseHandle             = kernel32.NewProc("CloseHandle")
)

const (
	processSetInformation            = 0x0200
	processSetQuota                  = 0x0100
	processTerminate                 = 0x0001
	jobObjectExtendedLimit           = 9
	jobObjectLimitKillOnClose        = 0x2000
	jobObjectBasicProcessIDListClass = 3
)

// sysProcAttr puts the child into its own process group so that console control
// events do not leak into the parent, and hides the console window.
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}

func setPriorityOS(pid int, prio Priority) error {
	h, _, err := procOpenProcess.Call(processSetInformation, 0, uintptr(uint32(pid))) //nolint:gosec // a PID always fits in 32 bits on Windows
	if h == 0 {
		return fmt.Errorf("OpenProcess: %w", err)
	}
	defer procCloseHandle.Call(h) //nolint:errcheck

	var class uintptr
	switch prio {
	case PriorityIdle:
		class = idlePriorityClass
	case PriorityBelowNormal:
		class = belowNormalPriorityClass
	case PriorityNormal, PriorityParallel:
		class = normalPriorityClass
	case PriorityAboveNormal:
		class = aboveNormalPriorityClass
	case PriorityHigh:
		class = highPriorityClass
	default:
		class = belowNormalPriorityClass
	}
	if r, _, err := procSetPriorityClass.Call(h, class); r == 0 {
		return fmt.Errorf("SetPriorityClass: %w", err)
	}
	return nil
}

// windowsTree is a Job Object holding the child process and, because job
// membership is inherited, all of its descendants.
type windowsTree struct {
	handle uintptr
}

// The following mirror the Win32 structures exactly; field order and padding
// matter because the kernel validates the buffer length.

type jobObjectBasicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

type ioCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

type jobObjectExtendedLimitInformation struct {
	BasicLimitInformation jobObjectBasicLimitInformation
	IoInfo                ioCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

func newTreeController(pid int) (treeController, error) {
	h, _, err := procCreateJobObjectW.Call(0, 0)
	if h == 0 {
		return nil, fmt.Errorf("CreateJobObject: %w", err)
	}

	// KILL_ON_JOB_CLOSE guarantees that no encoder is left behind if the
	// engine dies unexpectedly. SetInformationJobObject requires the full
	// JOBOBJECT_EXTENDED_LIMIT_INFORMATION layout, not just the flags field,
	// otherwise it rejects the call with ERROR_BAD_LENGTH.
	info := jobObjectExtendedLimitInformation{}
	info.BasicLimitInformation.LimitFlags = jobObjectLimitKillOnClose
	if r, _, err := procSetInformationJobObject.Call(h, jobObjectExtendedLimit, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info)); r == 0 {
		procCloseHandle.Call(h) //nolint:errcheck
		return nil, fmt.Errorf("SetInformationJobObject: %w", err)
	}

	ph, _, err := procOpenProcess.Call(processSetInformation|processSetQuota|processTerminate, 0, uintptr(uint32(pid))) //nolint:gosec // a PID always fits in 32 bits on Windows
	if ph == 0 {
		procCloseHandle.Call(h) //nolint:errcheck
		return nil, fmt.Errorf("OpenProcess: %w", err)
	}
	defer procCloseHandle.Call(ph) //nolint:errcheck

	if r, _, err := procAssignProcessToJob.Call(h, ph); r == 0 {
		procCloseHandle.Call(h) //nolint:errcheck
		return nil, fmt.Errorf("AssignProcessToJobObject: %w", err)
	}
	return &windowsTree{handle: h}, nil
}

func (t *windowsTree) Pause() error {
	// Suspending every thread of every process in a job requires enumerating
	// them, which is what the legacy code did through WMI. The Job Object API
	// has no suspend primitive, so threads are walked directly.
	return suspendJobThreads(t.handle, true)
}

func (t *windowsTree) Resume() error {
	return suspendJobThreads(t.handle, false)
}

func (t *windowsTree) Kill() error {
	if r, _, err := procTerminateJobObject.Call(t.handle, 1); r == 0 {
		return fmt.Errorf("TerminateJobObject: %w", err)
	}
	return nil
}

func (t *windowsTree) Close() error {
	if t.handle == 0 {
		return nil
	}
	procCloseHandle.Call(t.handle) //nolint:errcheck
	t.handle = 0
	return nil
}

// Compile-time assertion that the Windows implementation satisfies the
// interface declared in the platform-independent file.
var _ treeController = (*windowsTree)(nil)

// Ensure exec is referenced on this platform (SysProcAttr types live there).
var _ = exec.Command
