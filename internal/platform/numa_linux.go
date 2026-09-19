//go:build linux

package platform

import (
	"os"
	"strconv"
	"strings"
)

// numaNodeCount derives the count from sysfs, where each node is a node<N>
// directory. The highest number found plus one mirrors the Windows API, so
// node numbers can be used as --pools indexes on both platforms. A missing or
// empty sysfs entry degrades to a single node, which makes pinning a no-op.
func numaNodeCount() int {
	entries, err := os.ReadDir("/sys/devices/system/node")
	if err != nil {
		return 1
	}
	highest := -1
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		rest, ok := strings.CutPrefix(e.Name(), "node")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(rest)
		if err != nil {
			continue
		}
		if n > highest {
			highest = n
		}
	}
	if highest < 0 {
		return 1
	}
	return highest + 1
}
