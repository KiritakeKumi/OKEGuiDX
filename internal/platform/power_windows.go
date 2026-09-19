//go:build windows

package platform

import "syscall"

// Execution state flags for SetThreadExecutionState, mirroring the
// EXECUTION_STATE enum in the legacy WindowUtil class.
const (
	esSystemRequired = 0x00000001
	esContinuous     = 0x80000000
)

var procSetThreadExecutionState = syscall.NewLazyDLL("kernel32.dll").NewProc("SetThreadExecutionState")

// PreventSystemPowerdown asks the system to stay awake while work is running,
// mirroring WindowUtil.PreventSystemPowerdown. The request is continuous and
// lasts until AllowSystemPowerdown is called; the display may still turn off.
//
// SetThreadExecutionState fails when another thread already holds a continuous
// request; the legacy code ignored the result and so does this one.
func PreventSystemPowerdown() {
	procSetThreadExecutionState.Call(esSystemRequired | esContinuous) //nolint:errcheck // best-effort, as in the legacy code
}

// AllowSystemPowerdown restores the default power policy, mirroring
// WindowUtil.AllowSystemPowerdown.
func AllowSystemPowerdown() {
	procSetThreadExecutionState.Call(esContinuous) //nolint:errcheck // best-effort, as in the legacy code
}
