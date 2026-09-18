// Package toolchain locates the external tools OKEGuiDX drives and decides
// which build variant to use on each platform.
//
// It replaces two legacy files: Constants.cs, which hardcoded
// `.\tools\xxx.exe` with backslashes in a dozen places, and
// EnvironmentChecker.cs, which walked the registry and popped dialogs
// (INVENTORY.md §2 #6, #10, #13).
//
// The output is a node.Capabilities value: a structured description of what
// this machine can do, which is both what the engine needs today and what the
// future scheduler needs (CLUSTER.md §2).
package toolchain

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/KiritakeKumi/OKEGuiDX/internal/node"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
)

// Tool names used as keys in node.Capabilities.Tools and in Options.Paths.
const (
	ToolVSPipe     = "vspipe"
	ToolX264       = "x264"
	ToolX265       = "x265"
	ToolSVTAV1     = "svtav1"
	ToolFFmpeg     = "ffmpeg"
	ToolFFprobe    = "ffprobe"
	ToolMkvmerge   = "mkvmerge"
	ToolMkvextract = "mkvextract"
	ToolLSmash     = "muxer"
	ToolQAAC       = "qaac"
	ToolEac3to     = "eac3to-wrapper"
	ToolFlac       = "flac"
	ToolTChapter   = "tchapter"
	ToolRPCChecker = "rpchecker"
)

// x265 build variants. Only win-x64 uses Asuna; every other target uses
// upstream, because Asuna is pinned to the 3.5 (2021) baseline and therefore
// has none of the ARM or RISC-V SIMD work (PLAN.md §2.0, TOOLS-REPO.md §3).
const (
	VariantAsuna    = "asuna"
	VariantUpstream = "upstream"
)

// X265Variant returns the x265 build to use on the given platform.
func X265Variant(goos, goarch string) string {
	if goos == "windows" && goarch == "amd64" {
		return VariantAsuna
	}
	return VariantUpstream
}

// x264 build variants, following the same rule as x265: the tmod fork is
// x86-oriented, so non-win-x64 targets use upstream (TOOLS-REPO.md §3).
const (
	VariantTMod = "tmod"
)

// X264Variant returns the x264 build to use on the given platform.
func X264Variant(goos, goarch string) string {
	if goos == "windows" && goarch == "amd64" {
		return VariantTMod
	}
	return VariantUpstream
}

// Options configures discovery.
type Options struct {
	// Root is the directory containing the `tools` tree. Empty means the
	// executable's directory, which is how the legacy package was laid out.
	Root string
	// Explicit maps a tool name to a user-specified absolute path. It takes
	// precedence over discovery, matching the legacy behaviour where a path
	// saved in OKEGuiConfig.json won.
	Explicit map[string]string
	// Role is the role to report in Capabilities.
	Role node.Role
	// SingleNUMA disables per-socket pinning, mirroring the legacy
	// singleNuma configuration option.
	SingleNUMA bool
	// SkipProbe disables executing tools to read their version. Useful for
	// tests and for fast startup; versions are then left empty.
	SkipProbe bool
}

// defaultRelativePaths maps each tool to its location inside the tools tree,
// matching the layout the release archives use so an existing installation
// keeps working.
func defaultRelativePaths(goos, goarch string) map[string][]string {
	exe := ""
	if goos == "windows" {
		exe = ".exe"
	}
	x265 := "x265" + exe
	if X265Variant(goos, goarch) == VariantAsuna {
		x265 = "x265-asuna" + exe
	}
	x264 := "x264" + exe
	if X264Variant(goos, goarch) == VariantTMod {
		x264 = "x264-tmod" + exe
	}
	return map[string][]string{
		// Candidates are tried in order; the first existing file wins. The
		// extra names keep compatibility with the current release layout.
		ToolVSPipe:     {"vapoursynth/vspipe" + exe, "vspipe" + exe},
		ToolX264:       {"x26x/" + x264, "x26x/x264" + exe},
		ToolX265:       {"x26x/" + x265, "x26x/x265" + exe},
		ToolSVTAV1:     {"svtav1/SvtAv1EncApp" + exe, "svtav1/svtav1" + exe},
		ToolFFmpeg:     {"ffmpeg/ffmpeg" + exe, "ffmpeg" + exe},
		ToolFFprobe:    {"ffmpeg/ffprobe" + exe, "ffprobe" + exe},
		ToolMkvmerge:   {"mkvtoolnix/mkvmerge" + exe, "mkvmerge" + exe},
		ToolMkvextract: {"mkvtoolnix/mkvextract" + exe, "mkvextract" + exe},
		ToolLSmash:     {"l-smash/muxer" + exe, "muxer" + exe},
		ToolQAAC:       {"qaac/qaac64" + exe, "qaac/qaac" + exe},
		ToolEac3to:     {"eac3to/eac3to-wrapper" + exe, "eac3to-wrapper" + exe},
		ToolFlac:       {"flac/flac" + exe, "flac" + exe},
		ToolTChapter:   {"tchapter/tchapter" + exe, "tchapter" + exe},
		ToolRPCChecker: {"rpc/RPChecker" + exe, "rpc/rpchecker" + exe},
	}
}

// requiredTools are the tools whose absence makes the engine unusable. Missing
// optional tools only remove a feature.
var requiredTools = []string{ToolVSPipe, ToolFFmpeg, ToolFFprobe}

// Discover locates the tools, probes their versions and builds a
// node.Capabilities describing this machine.
func Discover(opts Options) (node.Capabilities, error) {
	root := opts.Root
	if root == "" {
		exe, err := os.Executable()
		if err != nil {
			return node.Capabilities{}, okerr.Wrap(err, okerr.KindIO,
				"无法确定程序目录", "无法定位可执行文件: %v", err)
		}
		root = filepath.Dir(exe)
	}

	caps := node.NewCapabilities(opts.Role)
	caps.NUMANodes = numaNodeCount(opts.SingleNUMA)
	if opts.SingleNUMA {
		caps.NUMANodes = 1
	} else {
		caps.AddFeature(node.FeatureNUMA)
	}
	// In standalone mode the single volume root is the filesystem root, so a
	// FileRef resolves to the absolute path the user expects.
	caps.Volumes["local"] = string(filepath.Separator)
	if runtime.GOOS == "windows" {
		// On Windows the "root" of a drive-qualified path is the drive itself;
		// an empty root makes filepath.Join behave correctly.
		caps.Volumes["local"] = ""
	}

	rels := defaultRelativePaths(caps.OS, caps.Arch)
	for name, candidates := range rels {
		path, ok := resolveOne(root, opts.Explicit, name, candidates)
		if !ok {
			continue
		}
		info := node.ToolInfo{Path: path}
		switch name {
		case ToolX265:
			info.Variant = X265Variant(caps.OS, caps.Arch)
		case ToolX264:
			info.Variant = X264Variant(caps.OS, caps.Arch)
		}
		if !opts.SkipProbe {
			info.Version = probeVersion(path, name)
		}
		caps.Tools[name] = info
	}

	// Features follow from which tools are present and which platform this is.
	if caps.IsWindows() {
		if _, ok := caps.Tools[ToolQAAC]; ok {
			caps.AddFeature(node.FeatureAAC)
		}
		if _, ok := caps.Tools[ToolEac3to]; ok {
			caps.AddFeature(node.FeatureEac3to)
		}
	}
	if _, ok := caps.Tools[ToolTChapter]; ok {
		caps.AddFeature("tchapter")
	}
	return caps, nil
}

// resolveOne finds the first candidate that exists, letting an explicit
// configuration value win.
func resolveOne(root string, explicit map[string]string, name string, candidates []string) (string, bool) {
	if explicit != nil {
		if p, ok := explicit[name]; ok && p != "" {
			if fileExists(p) {
				return p, true
			}
			// An explicitly configured but missing path is still reported as
			// the intended location so the error message names it.
			return p, false
		}
	}
	for _, rel := range candidates {
		p := filepath.Join(root, "tools", filepath.FromSlash(rel))
		if fileExists(p) {
			return p, true
		}
	}
	return "", false
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// Require returns the path of a tool, or a structured error naming it. The
// engine calls this before starting a job so the failure happens early and with
// a clear message.
func Require(caps node.Capabilities, name string) (string, error) {
	info, ok := caps.Tool(name)
	if !ok || info.Path == "" {
		return "", okerr.New(okerr.KindNotFound, "找不到外部工具",
			"工具链里没有 %s，请更新 tools 包或检查配置。", name)
	}
	return info.Path, nil
}

// CheckRequired verifies that every mandatory tool is present, returning one
// error listing all the missing ones.
func CheckRequired(caps node.Capabilities) error {
	var missing []string
	for _, name := range requiredTools {
		if !caps.HasTool(name) {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return okerr.New(okerr.KindNotFound, "找不到外部工具",
		"缺少必需的工具：%s。请更新 tools 包。", strings.Join(missing, ", "))
}

// EnsureAAC returns a structured error when AAC output was requested on a
// platform that cannot produce it. The project refuses to substitute another
// encoder, because that would silently change output quality (PLAN.md §2.2).
func EnsureAAC(caps node.Capabilities) error {
	if caps.HasFeature(node.FeatureAAC) {
		return nil
	}
	return okerr.ErrUnsupportedAAC
}

// Describe renders a short human-readable summary, used by `okegui status` and
// by the startup log.
func Describe(caps node.Capabilities) string {
	var b strings.Builder
	fmt.Fprintf(&b, "node=%s role=%s os=%s/%s cpus=%d numa=%d\n",
		caps.NodeID, caps.Role, caps.OS, caps.Arch, caps.CPUs, caps.NUMANodes)
	names := make([]string, 0, len(caps.Tools))
	for n := range caps.Tools {
		names = append(names, n)
	}
	sortStrings(names)
	for _, n := range names {
		t := caps.Tools[n]
		variant := t.Variant
		if variant != "" {
			variant = " (" + variant + ")"
		}
		version := t.Version
		if version == "" {
			version = "?"
		}
		fmt.Fprintf(&b, "  %-12s %s%s %s\n", n, version, variant, t.Path)
	}
	fmt.Fprintf(&b, "features: %s\n", strings.Join(caps.Features, ", "))
	return b.String()
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
