//go:build windows

package proc

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	procQueryInformationJobObject = kernel32.NewProc("QueryInformationJobObject")
	procOpenThread                = kernel32.NewProc("OpenThread")
	procSuspendThread             = kernel32.NewProc("SuspendThread")
	procResumeThread              = kernel32.NewProc("ResumeThread")
	procCreateToolhelp32Snapshot  = kernel32.NewProc("CreateToolhelp32Snapshot")
	procProcess32First            = kernel32.NewProc("Process32FirstW")
	procProcess32Next             = kernel32.NewProc("Process32NextW")
)

const (
	threadSuspendResume = 0x0002
	th32csSnapProcess   = 0x00000002
	invalidHandleValue  = ^uintptr(0)
)

// jobObjectBasicProcessIDList mirrors the JOBOBJECT_BASIC_PROCESS_ID_LIST
// structure. The list is variable-length, so the buffer is allocated by hand.
type jobObjectBasicProcessIDList struct {
	NumberOfAssignedProcesses uint32
	NumberOfProcessIDsInList  uint32
	ProcessIDList             [1]uintptr
}

type processEntry32 struct {
	Size              uint32
	CntUsage          uint32
	ProcessID         uint32
	DefaultHeapID     uintptr
	ModuleID          uint32
	CntThreads        uint32
	ParentProcessID   uint32
	PriorityClassBase int32
	ExeFile           [260]uint16
}

// suspendJobThreads suspends or resumes every thread of every process assigned
// to the job. This is the direct replacement for the legacy WMI + OpenThread
// walk in SubProcessService.cs, minus the WMI dependency.
func suspendJobThreads(job uintptr, suspend bool) error {
	pids, err := jobProcessIDs(job)
	if err != nil {
		return err
	}
	for _, pid := range pids {
		threads, err := processThreadIDs(uint32(pid)) //nolint:gosec // a PID always fits in 32 bits on Windows
		if err != nil {
			continue
		}
		for _, tid := range threads {
			h, _, _ := procOpenThread.Call(threadSuspendResume, 0, uintptr(tid))
			if h == 0 {
				continue
			}
			if suspend {
				procSuspendThread.Call(h) //nolint:errcheck
			} else {
				// A thread may have been suspended more than once; drain the
				// count the same way the legacy code did.
				for {
					if n, _, _ := procResumeThread.Call(h); n <= 1 {
						break
					}
				}
			}
			procCloseHandle.Call(h) //nolint:errcheck
		}
	}
	return nil
}

func jobProcessIDs(job uintptr) ([]uintptr, error) {
	var size uint32 = 4096
	buf := make([]byte, size)
	for {
		r, _, err := procQueryInformationJobObject.Call(
			job, jobObjectBasicProcessIDListClass,
			uintptr(unsafe.Pointer(&buf[0])), uintptr(size), 0)
		if r == 0 {
			// Grow the buffer if it was too small.
			if size >= 1<<20 {
				return nil, fmt.Errorf("QueryInformationJobObject: %w", err)
			}
			size *= 2
			buf = make([]byte, size)
			continue
		}
		list := (*jobObjectBasicProcessIDList)(unsafe.Pointer(&buf[0]))
		n := int(list.NumberOfProcessIDsInList)
		if n == 0 {
			return nil, nil
		}
		ids := unsafe.Slice(&list.ProcessIDList[0], n)
		out := make([]uintptr, 0, n)
		out = append(out, ids...)
		return out, nil
	}
}

func processThreadIDs(pid uint32) ([]uint32, error) {
	snap, _, err := procCreateToolhelp32Snapshot.Call(th32csSnapProcess, 0)
	if snap == invalidHandleValue {
		return nil, fmt.Errorf("CreateToolhelp32Snapshot: %w", err)
	}
	defer procCloseHandle.Call(snap) //nolint:errcheck

	var entry processEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	r, _, err := procProcess32First.Call(snap, uintptr(unsafe.Pointer(&entry)))
	if r == 0 {
		return nil, fmt.Errorf("Process32First: %w", err)
	}
	for {
		if entry.ProcessID == pid {
			return threadIDsOf(pid)
		}
		r, _, err = procProcess32Next.Call(snap, uintptr(unsafe.Pointer(&entry)))
		if r == 0 {
			return nil, fmt.Errorf("process %d not found: %w", pid, err)
		}
	}
}

// threadIDsOf enumerates threads of a process. The Toolhelp API exposes a
// thread snapshot; it is declared separately to keep the struct definitions
// close to their use.
func threadIDsOf(pid uint32) ([]uint32, error) {
	const th32csSnapThread = 0x00000004

	type threadEntry32 struct {
		Size           uint32
		CntUsage       uint32
		ThreadID       uint32
		OwnerProcessID uint32
		BasePri        int32
		DeltaPri       int32
		Flags          uint32
	}

	snap, _, err := procCreateToolhelp32Snapshot.Call(th32csSnapThread, uintptr(pid))
	if snap == invalidHandleValue {
		return nil, fmt.Errorf("CreateToolhelp32Snapshot(thread): %w", err)
	}
	defer procCloseHandle.Call(snap) //nolint:errcheck

	procThread32First := kernel32.NewProc("Thread32First")
	procThread32Next := kernel32.NewProc("Thread32Next")

	var entry threadEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	r, _, err := procThread32First.Call(snap, uintptr(unsafe.Pointer(&entry)))
	if r == 0 {
		return nil, fmt.Errorf("Thread32First: %w", err)
	}
	var out []uint32
	for {
		if entry.OwnerProcessID == pid {
			out = append(out, entry.ThreadID)
		}
		r, _, err = procThread32Next.Call(snap, uintptr(unsafe.Pointer(&entry)))
		if r == 0 {
			break
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no threads found for process %d: %w", pid, err)
	}
	return out, nil
}

var _ = syscall.Getpid
