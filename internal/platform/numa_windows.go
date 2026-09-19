//go:build windows

package platform

import (
	"syscall"
	"unsafe"
)

var (
	kernel32                     = syscall.NewLazyDLL("kernel32.dll")
	procGetNumaHighestNodeNumber = kernel32.NewProc("GetNumaHighestNodeNumber")
)

// numaNodeCount calls GetNumaHighestNodeNumber, as the legacy NumaNode static
// constructor did, and returns the highest node number plus one. A failed call
// leaves the out-parameter at zero, which yields a single node; C# observed
// the same because out parameters are zero-initialised before the call.
func numaNodeCount() int {
	var highest uint32
	if r, _, _ := procGetNumaHighestNodeNumber.Call(uintptr(unsafe.Pointer(&highest))); r == 0 {
		return 1
	}
	return int(highest) + 1
}
