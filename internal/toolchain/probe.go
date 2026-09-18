package toolchain

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/KiritakeKumi/OKEGuiDX/internal/proc"
)

// versionTimeout bounds how long a version probe may take. Tools that hang on
// `--version` (usually because a shared library is missing) must not stall
// startup.
const versionTimeout = 5 * time.Second

// versionArgv lists the argument that makes each tool print its version. Tools
// not listed here are not probed.
var versionArgv = map[string][]string{
	ToolX264:       {"--version"},
	ToolX265:       {"--version"},
	ToolSVTAV1:     {"--version"},
	ToolFFmpeg:     {"-version"},
	ToolFFprobe:    {"-version"},
	ToolMkvmerge:   {"--version"},
	ToolMkvextract: {"--version"},
	ToolFlac:       {"--version"},
	ToolQAAC:       {"--check"},
	ToolEac3to:     {"--version"},
	ToolLSmash:     {"--version"},
	ToolTChapter:   {"--version"},
}

// probeVersion runs the tool and extracts a version string. Failures are not
// errors: a tool that exists but cannot report a version is still usable.
func probeVersion(path, name string) string {
	args, ok := versionArgv[name]
	if !ok {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), versionTimeout)
	defer cancel()

	res, err := proc.Run(ctx, proc.Spec{Path: path, Args: args, Name: name})
	if res == nil {
		return ""
	}
	// Several tools print their version to stderr, so both streams are searched.
	text := res.Stdout
	if strings.TrimSpace(text) == "" {
		text = res.Stderr
	}
	if err != nil && strings.TrimSpace(text) == "" {
		return ""
	}
	return extractVersion(text)
}

// versionPattern pulls the first version-looking token out of a tool banner.
var versionPattern = regexp.MustCompile(`\d+\.\d+(?:\.\d+)*(?:[-+._][0-9A-Za-z.]+)*`)

func extractVersion(text string) string {
	line := firstNonEmptyLine(text)
	if m := versionPattern.FindString(line); m != "" {
		return m
	}
	if m := versionPattern.FindString(text); m != "" {
		return m
	}
	return strings.TrimSpace(line)
}

func firstNonEmptyLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if s := strings.TrimSpace(line); s != "" {
			return s
		}
	}
	return ""
}

// numaNodeCount reports how many NUMA nodes the machine has, or 1 when the
// information is unavailable. On Linux this reads sysfs; on other platforms it
// falls back to a single node, matching the legacy behaviour where pinning
// silently degraded to no-op on non-Windows.
func numaNodeCount(single bool) int {
	if single {
		return 1
	}
	entries, err := os.ReadDir("/sys/devices/system/node")
	if err != nil {
		return 1
	}
	n := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "node") {
			if _, err := strconv.Atoi(strings.TrimPrefix(e.Name(), "node")); err == nil {
				n++
			}
		}
	}
	if n == 0 {
		return 1
	}
	return n
}

// InstallRootFor returns the tools root that would be used for a given
// executable path. Exposed for the CLI so `okegui status` can explain where it
// looked.
func InstallRootFor(exePath string) string { return filepath.Dir(exePath) }
