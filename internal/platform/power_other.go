//go:build !windows

package platform

// PreventSystemPowerdown is a no-op off Windows. There is no portable API for
// keeping the machine awake, and the daemon runs under the system's own power
// policy; callers must not treat this as a failure.
//
// The legacy WindowUtil.PreventSystemPowerdown/AllowSystemPowerdown pair used
// SetThreadExecutionState, a Windows-only API, and INVENTORY.md §3 records
// that neither function had a caller in the shipped program. They are ported
// because a long encode is exactly the case they exist for: keeping the
// machine awake while a worker is running.
func PreventSystemPowerdown() {}

// AllowSystemPowerdown undoes PreventSystemPowerdown. It is a no-op off
// Windows.
func AllowSystemPowerdown() {}
