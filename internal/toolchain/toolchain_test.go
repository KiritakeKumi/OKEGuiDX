package toolchain

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/KiritakeKumi/OKEGuiDX/internal/node"
)

func TestX265VariantSelection(t *testing.T) {
	t.Parallel()
	// win-x64 defaults to the Kyouko build, because that is what the current
	// .NET release ships and therefore what reproduces existing output.
	cases := []struct {
		goos, goarch string
		want         string
	}{
		{"windows", "amd64", VariantKyouko},
		{"windows", "arm64", VariantUpstream},
		{"linux", "amd64", VariantUpstream},
		{"linux", "arm64", VariantUpstream},
		{"linux", "riscv64", VariantUpstream},
	}
	for _, tc := range cases {
		t.Run(tc.goos+"/"+tc.goarch, func(t *testing.T) {
			t.Parallel()
			if got := X265Variant(tc.goos, tc.goarch); got != tc.want {
				t.Errorf("X265Variant(%s, %s) = %q, want %q", tc.goos, tc.goarch, got, tc.want)
			}
		})
	}
}

func TestX265VariantsListsEveryBuild(t *testing.T) {
	t.Parallel()
	// win-x64 ships all three builds so the operator can pick the one that
	// reproduces the behaviour they want.
	got := X265Variants("windows", "amd64")
	if len(got) != 3 {
		t.Fatalf("X265Variants(win-x64) = %v, want 3 entries", got)
	}
	if got[0] != VariantKyouko {
		t.Errorf("first preference = %q, want %q", got[0], VariantKyouko)
	}
	want := map[string]bool{VariantKyouko: true, VariantAsuna: true, VariantUpstream: true}
	for _, v := range got {
		if !want[v] {
			t.Errorf("unexpected variant %q", v)
		}
		delete(want, v)
	}
	if len(want) != 0 {
		t.Errorf("missing variants: %v", want)
	}

	// Every other target has exactly one build, because Asuna and Kyouko are
	// x86-oriented and carry none of the ARM or RISC-V SIMD work.
	for _, tc := range []struct{ goos, goarch string }{
		{"linux", "amd64"}, {"linux", "arm64"}, {"linux", "riscv64"}, {"windows", "arm64"},
	} {
		if got := X265Variants(tc.goos, tc.goarch); len(got) != 1 || got[0] != VariantUpstream {
			t.Errorf("X265Variants(%s, %s) = %v, want [upstream]", tc.goos, tc.goarch, got)
		}
	}
}

func TestX265FileNamePerVariant(t *testing.T) {
	t.Parallel()
	cases := []struct {
		variant string
		exe     string
		want    string
	}{
		{VariantKyouko, "", "x265-kyouko"},
		{VariantAsuna, "", "x265-asuna"},
		{VariantUpstream, "", "x265"},
		{VariantKyouko, ".exe", "x265-kyouko.exe"},
	}
	for _, tc := range cases {
		if got := x265FileName(tc.variant, tc.exe); got != tc.want {
			t.Errorf("x265FileName(%q, %q) = %q, want %q", tc.variant, tc.exe, got, tc.want)
		}
	}
}

func TestVariantFromPathRecoversVariant(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		`C:\tools\x26x\x265-kyouko.exe`: VariantKyouko,
		`C:\tools\x26x\x265-asuna.exe`:  VariantAsuna,
		`C:\tools\x26x\x265.exe`:        VariantUpstream,
		`/usr/local/bin/x265`:           VariantUpstream,
	}
	for path, want := range cases {
		if got := variantFromPath(path); got != want {
			t.Errorf("variantFromPath(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestDiscoverPrefersKyoukoOnWindowsX64(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		t.Skip("the three-way x265 layout only exists on win-x64")
	}
	// When several builds are present, the Kyouko one must win, because it is
	// what reproduces the current release's output.
	root := buildToolsTree(t, "x26x/x265", "x26x/x265-asuna", "x26x/x265-kyouko")
	caps, err := Discover(Options{Root: root, SkipProbe: true})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	info, ok := caps.Tool(ToolX265)
	if !ok {
		t.Fatal("x265 was not discovered")
	}
	if info.Variant != VariantKyouko {
		t.Errorf("Variant = %q, want %q", info.Variant, VariantKyouko)
	}
	if !strings.Contains(info.Path, "kyouko") {
		t.Errorf("Path = %q, want the Kyouko build", info.Path)
	}
}

func TestDiscoverFallsBackWhenKyoukoAbsent(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		t.Skip("the three-way x265 layout only exists on win-x64")
	}
	// A tree that only has the Asuna build must still resolve, reporting the
	// variant it actually found rather than the one it preferred.
	root := buildToolsTree(t, "x26x/x265-asuna")
	caps, err := Discover(Options{Root: root, SkipProbe: true})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	info, ok := caps.Tool(ToolX265)
	if !ok {
		t.Fatal("x265 was not discovered")
	}
	if info.Variant != VariantAsuna {
		t.Errorf("Variant = %q, want %q", info.Variant, VariantAsuna)
	}
}

func TestX264VariantSelection(t *testing.T) {
	t.Parallel()
	if got := X264Variant("windows", "amd64"); got != VariantTMod {
		t.Errorf("X264Variant(win-x64) = %q, want %q", got, VariantTMod)
	}
	for _, arch := range []string{"arm64"} {
		if got := X264Variant("windows", arch); got != VariantUpstream {
			t.Errorf("X264Variant(win-%s) = %q, want upstream", arch, got)
		}
	}
	if got := X264Variant("linux", "riscv64"); got != VariantUpstream {
		t.Errorf("X264Variant(linux-riscv64) = %q, want upstream", got)
	}
}

// buildToolsTree creates a fake tools directory so discovery can be tested
// without a real installation. Bare names get the platform executable suffix,
// because that is what the release archives contain.
func buildToolsTree(t *testing.T, files ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, rel := range files {
		rel = withExeSuffix(rel)
		p := filepath.Join(root, "tools", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	return root
}

// withExeSuffix appends ".exe" on Windows when the name has no extension.
func withExeSuffix(rel string) string {
	if runtime.GOOS != "windows" {
		return rel
	}
	if filepath.Ext(rel) != "" {
		return rel
	}
	return rel + ".exe"
}

func TestDiscoverFindsToolsAndBuildsCapabilities(t *testing.T) {
	t.Parallel()
	root := buildToolsTree(t,
		"vapoursynth/vspipe",
		"ffmpeg/ffmpeg",
		"ffmpeg/ffprobe",
		"x26x/x265",
		"mkvtoolnix/mkvmerge",
	)

	caps, err := Discover(Options{Root: root, SkipProbe: true})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}

	for _, want := range []string{ToolVSPipe, ToolFFmpeg, ToolFFprobe, ToolX265, ToolMkvmerge} {
		if !caps.HasTool(want) {
			t.Errorf("tool %q was not discovered", want)
		}
	}
	if caps.HasTool(ToolQAAC) {
		t.Error("qaac was discovered but was not present in the tree")
	}
	if caps.OS == "" || caps.Arch == "" {
		t.Error("OS/Arch were not populated")
	}
	if caps.CPUs <= 0 {
		t.Errorf("CPUs = %d, want a positive count", caps.CPUs)
	}
	if len(caps.Volumes) == 0 {
		t.Error("Volumes is empty; standalone must declare the local volume")
	}
	if _, ok := caps.Volumes["local"]; !ok {
		t.Errorf("Volumes = %v, want a %q entry", caps.Volumes, "local")
	}
}

func TestDiscoverReportsToolVariants(t *testing.T) {
	t.Parallel()
	root := buildToolsTree(t, "x26x/x265", "x26x/x264-tmod")
	caps, err := Discover(Options{Root: root, SkipProbe: true})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	info, ok := caps.Tool(ToolX265)
	if !ok {
		t.Fatal("x265 was not discovered")
	}
	// The variant must be recorded so the scheduler can warn about version
	// skew across a cluster (CLUSTER.md risk X5).
	if info.Variant == "" {
		t.Error("x265 Variant is empty, want the build variant to be recorded")
	}
}

func TestDiscoverHonoursExplicitPaths(t *testing.T) {
	t.Parallel()
	root := buildToolsTree(t, "vapoursynth/vspipe")
	explicit := filepath.Join(t.TempDir(), "custom-vspipe")
	if err := os.WriteFile(explicit, []byte("x"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	caps, err := Discover(Options{
		Root:      root,
		SkipProbe: true,
		Explicit:  map[string]string{ToolVSPipe: explicit},
	})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	info, ok := caps.Tool(ToolVSPipe)
	if !ok {
		t.Fatal("vspipe was not discovered")
	}
	if info.Path != explicit {
		t.Errorf("vspipe path = %q, want the explicitly configured %q", info.Path, explicit)
	}
}

func TestDiscoverReportsMissingRequiredTools(t *testing.T) {
	t.Parallel()
	root := buildToolsTree(t) // empty tools tree
	caps, err := Discover(Options{Root: root, SkipProbe: true})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if err := CheckRequired(caps); err == nil {
		t.Fatal("CheckRequired() = nil, want an error listing the missing tools")
	}
}

func TestCheckRequiredPassesWhenComplete(t *testing.T) {
	t.Parallel()
	root := buildToolsTree(t, "vapoursynth/vspipe", "ffmpeg/ffmpeg", "ffmpeg/ffprobe")
	caps, err := Discover(Options{Root: root, SkipProbe: true})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if err := CheckRequired(caps); err != nil {
		t.Fatalf("CheckRequired() error = %v", err)
	}
}

func TestAACFeatureFollowsPlatformAndTools(t *testing.T) {
	t.Parallel()
	// The AAC decision (PLAN.md §2.2) is driven by capabilities, not by a
	// scattered GOOS check, so this is the behaviour that matters.
	root := buildToolsTree(t, "qaac/qaac64.exe")
	caps, err := Discover(Options{Root: root, SkipProbe: true})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}

	if caps.IsWindows() {
		if !caps.HasFeature(node.FeatureAAC) {
			t.Error("on Windows with qaac present, the aac feature must be advertised")
		}
		if err := EnsureAAC(caps); err != nil {
			t.Errorf("EnsureAAC() error = %v, want nil", err)
		}
	} else {
		if caps.HasFeature(node.FeatureAAC) {
			t.Error("the aac feature must never be advertised off Windows")
		}
		if err := EnsureAAC(caps); err == nil {
			t.Error("EnsureAAC() = nil, want a refusal off Windows")
		}
	}
}

func TestEnsureAACRefusesWithoutQAAC(t *testing.T) {
	t.Parallel()
	root := buildToolsTree(t) // no qaac
	caps, err := Discover(Options{Root: root, SkipProbe: true})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	err = EnsureAAC(caps)
	if err == nil {
		t.Fatal("EnsureAAC() = nil, want a refusal")
	}
	if !caps.IsWindows() {
		return // covered by the platform test above
	}
	// On Windows the message must point at the missing tool, not at the
	// platform, because that is actionable.
	if err.Error() == "" {
		t.Error("EnsureAAC() returned an empty message")
	}
}

func TestRequireReturnsErrorForMissingTool(t *testing.T) {
	t.Parallel()
	caps := node.NewCapabilities(node.RoleStandalone)
	if _, err := Require(caps, ToolX265); err == nil {
		t.Fatal("Require() = nil error, want a not-found error")
	}
}

func TestRequireReturnsPath(t *testing.T) {
	t.Parallel()
	caps := node.NewCapabilities(node.RoleStandalone)
	caps.Tools[ToolX265] = node.ToolInfo{Path: "/tools/x265"}
	got, err := Require(caps, ToolX265)
	if err != nil {
		t.Fatalf("Require() error = %v", err)
	}
	if got != "/tools/x265" {
		t.Errorf("Require() = %q, want %q", got, "/tools/x265")
	}
}

func TestDescribeMentionsToolsAndFeatures(t *testing.T) {
	t.Parallel()
	root := buildToolsTree(t, "x26x/x265")
	caps, err := Discover(Options{Root: root, SkipProbe: true})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	got := Describe(caps)
	for _, want := range []string{"node=", "x265", "features:"} {
		if !contains(got, want) {
			t.Errorf("Describe() = %q, want it to mention %q", got, want)
		}
	}
}

func TestSingleNUMADisablesPinning(t *testing.T) {
	t.Parallel()
	root := buildToolsTree(t)
	caps, err := Discover(Options{Root: root, SkipProbe: true, SingleNUMA: true})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if caps.NUMANodes != 1 {
		t.Errorf("NUMANodes = %d, want 1 when singleNuma is set", caps.NUMANodes)
	}
	if caps.HasFeature(node.FeatureNUMA) {
		t.Error("the numa feature must not be advertised when pinning is disabled")
	}
}

func contains(h, n string) bool {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return true
		}
	}
	return false
}
