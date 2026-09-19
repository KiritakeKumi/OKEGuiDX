// Package platform groups the pieces of OKEGuiDX that are tied to the
// operating system or to the machine it runs on.
//
// Three legacy concerns live here:
//
//   - NUMA node assignment for workers (Worker/NumaNode.cs).
//   - The per-user configuration directory that replaces the
//     HKCU\Software\OKEGui registry key (Utils/RegistryStorage.cs). The
//     registry key/value helpers had exactly one user, the updater's LastCheck
//     timestamp, and the updater was deleted in commit 0ca3dba;
//     RegistryStorage.RegistryAddCount never had a caller at all and is
//     deliberately not ported.
//   - The log file layout (Utils/Initializer.ConfigLogger and ClearOldLogs).
//
// Platform differences are expressed with build constraints, one file per
// GOOS (numa_windows.go, numa_linux.go, numa_other.go), so the shared code
// contains no runtime.GOOS checks.
//
// WindowUtil.PreventSystemPowerdown/AllowSystemPowerdown are ported in
// power_windows.go and power_other.go even though INVENTORY.md §3 records that
// neither had a caller: the workstream lists 防休眠 as part of this package,
// and a long encode is exactly the case they exist for. The remaining 1450
// lines of WindowsUtil.cs are dead code and are not ported.
package platform

import "runtime"

// UsableCoreCount is the number of logical processors the runtime reports,
// the equivalent of Environment.ProcessorCount in the legacy NumaNode class.
// The encoders used it to decide whether to pass their own --threads/--lp
// defaults.
func UsableCoreCount() int { return runtime.NumCPU() }
