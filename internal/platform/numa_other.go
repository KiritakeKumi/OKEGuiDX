//go:build !windows && !linux

package platform

// numaNodeCount reports a single node on platforms without a NUMA query. The
// legacy code only ran on Windows; everywhere else pinning degrades to a
// no-op, which is what the toolchain documents as well.
func numaNodeCount() int { return 1 }
