package qaac

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc"
	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/node"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
)

// aacCaps returns capabilities with qaac installed, which is what
// toolchain.Discover produces on a Windows node that has the tools package.
func aacCaps() node.Capabilities {
	caps := node.NewCapabilities(node.RoleStandalone)
	caps.Tools["qaac"] = node.ToolInfo{Path: `C:\tools\qaac\qaac64.exe`}
	caps.AddFeature(node.FeatureAAC)
	return caps
}

// noAACCaps returns capabilities without qaac, as on every non-Windows node.
func noAACCaps() node.Capabilities {
	return node.NewCapabilities(node.RoleStandalone)
}

func TestBuildArgs(t *testing.T) {
	t.Parallel()

	q100 := 100
	q0 := 0
	q127 := 127

	cases := []struct {
		name string
		opts Options
		want []string
	}{
		{
			name: "bitrate mode",
			opts: Options{Input: `Q:\ep01.flac`, Output: `Q:\ep01.m4a`, Bitrate: 192},
			want: []string{"-i", "-v", "192", "-q", "2", "--no-delay",
				"-o", `Q:\ep01.m4a`, `Q:\ep01.flac`},
		},
		{
			name: "quality mode",
			opts: Options{Input: `Q:\ep01.flac`, Output: `Q:\ep01.m4a`, Quality: &q100},
			want: []string{"-i", "-V", "100", "-q", "2", "--no-delay",
				"-o", `Q:\ep01.m4a`, `Q:\ep01.flac`},
		},
		{
			// The profile validator rejects this combination, but the legacy
			// constructor gave Quality precedence, so the encoder does too.
			name: "quality wins over bitrate",
			opts: Options{Input: "in.flac", Output: "out.m4a", Bitrate: 320, Quality: &q100},
			want: []string{"-i", "-V", "100", "-q", "2", "--no-delay", "-o", "out.m4a", "in.flac"},
		},
		{
			name: "quality bounds",
			opts: Options{Input: "in.flac", Output: "out.m4a", Quality: &q0},
			want: []string{"-i", "-V", "0", "-q", "2", "--no-delay", "-o", "out.m4a", "in.flac"},
		},
		{
			name: "quality upper bound",
			opts: Options{Input: "in.flac", Output: "out.m4a", Quality: &q127},
			want: []string{"-i", "-V", "127", "-q", "2", "--no-delay", "-o", "out.m4a", "in.flac"},
		},
		{
			name: "paths with spaces and non-ASCII characters stay one argument",
			opts: Options{Input: `Q:\我的 作品\第01話.flac`, Output: `Q:\我的 作品\第01話.m4a`, Bitrate: 192},
			want: []string{"-i", "-v", "192", "-q", "2", "--no-delay",
				"-o", `Q:\我的 作品\第01話.m4a`, `Q:\我的 作品\第01話.flac`},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertArgs(t, BuildArgs(tc.opts), tc.want)
		})
	}
}

// TestOptionsFromInfo covers the legacy AudioInfo field initializer
// (Bitrate = Constants.QAACBitrate) and the AJob.Info passthrough.
func TestOptionsFromInfo(t *testing.T) {
	t.Parallel()

	q99 := 99
	cases := []struct {
		name      string
		info      model.AudioInfo
		wantRate  int
		wantQual  *int
		wantInput string
	}{
		{
			name:      "unspecified bitrate becomes the QAAC default",
			info:      model.AudioInfo{},
			wantRate:  DefaultBitrate,
			wantInput: "ep.flac",
		},
		{
			name:      "explicit bitrate is kept",
			info:      model.AudioInfo{Bitrate: 320},
			wantRate:  320,
			wantInput: "ep.flac",
		},
		{
			name:      "quality is passed through",
			info:      model.AudioInfo{Quality: &q99},
			wantRate:  DefaultBitrate,
			wantQual:  &q99,
			wantInput: "ep.flac",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := OptionsFromInfo(tc.info, tc.wantInput, "ep.m4a")
			if got.Bitrate != tc.wantRate {
				t.Errorf("Bitrate = %d, want %d", got.Bitrate, tc.wantRate)
			}
			if (got.Quality == nil) != (tc.wantQual == nil) {
				t.Fatalf("Quality = %v, want %v", got.Quality, tc.wantQual)
			}
			if got.Quality != nil && *got.Quality != *tc.wantQual {
				t.Errorf("Quality = %d, want %d", *got.Quality, *tc.wantQual)
			}
			if got.Input != tc.wantInput {
				t.Errorf("Input = %q, want %q", got.Input, tc.wantInput)
			}
			if got.Output != "ep.m4a" {
				t.Errorf("Output = %q, want %q", got.Output, "ep.m4a")
			}
		})
	}
}

// TestNewRejectsNodeWithoutAAC is the acceptance criterion from PLAN.md §2.2:
// without qaac there is no AAC, and no other encoder may be substituted.
func TestNewRejectsNodeWithoutAAC(t *testing.T) {
	t.Parallel()

	opts := Options{Input: "ep.flac", Output: "ep.m4a", Bitrate: 192}
	if _, err := New(noAACCaps(), opts); !errors.Is(err, okerr.ErrUnsupportedAAC) {
		t.Fatalf("New() error = %v, want okerr.ErrUnsupportedAAC", err)
	}
	if _, err := NewCheck(noAACCaps()); !errors.Is(err, okerr.ErrUnsupportedAAC) {
		t.Fatalf("NewCheck() error = %v, want okerr.ErrUnsupportedAAC", err)
	}
	// A node that advertises the feature but has no tool path must fail with
	// the toolchain's not-found error instead of running an empty command.
	broken := noAACCaps()
	broken.AddFeature(node.FeatureAAC)
	if _, err := New(broken, opts); !errors.Is(err, okerr.ErrToolNotFound) {
		t.Fatalf("New() with a missing tool path error = %v, want okerr.ErrToolNotFound", err)
	}
}

func TestNewAcceptsNodeWithAAC(t *testing.T) {
	t.Parallel()

	q100 := 100
	p, err := New(aacCaps(), Options{Input: "ep.flac", Output: "ep.m4a", Quality: &q100})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if got := p.Name(); got != Name {
		t.Errorf("Name() = %q, want %q", got, Name)
	}
	assertArgs(t, p.Args(), []string{"-i", "-V", "100", "-q", "2", "--no-delay", "-o", "ep.m4a", "ep.flac"})
}

// TestNewCheckBuildsSelfTest covers EnvironmentChecker.CheckQAAC, which ran
// qaac with the bare --check switch.
func TestNewCheckBuildsSelfTest(t *testing.T) {
	t.Parallel()

	p, err := NewCheck(aacCaps())
	if err != nil {
		t.Fatalf("NewCheck() error = %v", err)
	}
	assertArgs(t, p.Args(), []string{checkArg})
}

func TestCheckBannerFixture(t *testing.T) {
	t.Parallel()

	// The --check banner is plain output: no progress, no failure, no
	// completion marker. The fixture is the shape qaac 2.85 prints (banner,
	// then one line per loaded library).
	p, err := NewCheck(aacCaps())
	if err != nil {
		t.Fatalf("NewCheck() error = %v", err)
	}
	forEachLine(t, "check_banner.txt", func(line string) {
		p.handleLine(line)
	})
	if p.Finished() {
		t.Error("Finished() = true after a --check banner, want false")
	}
	if got := p.Progress(); got.Percent != 0 {
		t.Errorf("Percent = %v, want 0", got.Percent)
	}
	if p.lineErr != nil {
		t.Errorf("line error = %v, want nil", p.lineErr)
	}
}

func TestProgressFixture(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		fixture  string
		wantDone bool
		wantPct  float64
	}{
		{name: "true VBR", fixture: "tvbr_q100.log", wantDone: true, wantPct: 100},
		{name: "constrained VBR", fixture: "cvbr_192.log", wantDone: true, wantPct: 100},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, err := New(aacCaps(), Options{Input: "ep.flac", Output: "ep.m4a", Bitrate: 192})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}

			var (
				updates int
				lastPct float64
			)
			forEachLine(t, tc.fixture, func(line string) {
				p.handleLine(line)
				if p.Progress().Percent != lastPct {
					updates++
					lastPct = p.Progress().Percent
				}
			})

			if updates == 0 {
				t.Fatal("no progress updates recognised in the fixture")
			}
			if lastPct != tc.wantPct {
				t.Errorf("final percent = %v, want %v", lastPct, tc.wantPct)
			}
			if p.Finished() != tc.wantDone {
				t.Errorf("Finished() = %v, want %v", p.Finished(), tc.wantDone)
			}
			if p.lineErr != nil {
				t.Errorf("line error = %v, want nil", p.lineErr)
			}
		})
	}
}

// TestCarriageReturnProgress is the reason onLine splits on "\r": qaac redraws
// its progress with a bare carriage return, which .NET's ReadLine treated as a
// line separator but bufio.ScanLines does not.
func TestCarriageReturnProgress(t *testing.T) {
	t.Parallel()

	p, err := New(aacCaps(), Options{Input: "ep.flac", Output: "ep.m4a", Bitrate: 192})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := p.onLine("[12.4%] 0:03.058/0:24.619 (11.9x), ETA 0:00.000\r[68.1%] 0:16.772/0:24.619 (24.1x), ETA 0:00.000\r[100.0%] 0:24.619/0:24.619 (25.0x), ETA 0:00.000  "); err != nil {
		t.Fatalf("onLine() error = %v", err)
	}
	if got := p.Progress().Percent; got != 100 {
		t.Errorf("Percent = %v, want 100", got)
	}
}

func TestProgressAtOrBelowOnePercentIsNotReported(t *testing.T) {
	t.Parallel()

	p, err := New(aacCaps(), Options{Input: "ep.flac", Output: "ep.m4a", Bitrate: 192})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	p.handleLine("[0.4%] 0:00.100/0:24.619 (0.2x), ETA 0:00.000")
	p.handleLine("[1.0%] 0:00.246/0:24.619 (0.5x), ETA 0:00.000")
	if got := p.Progress().Percent; got != 0 {
		t.Errorf("Percent = %v, want 0 (the legacy threshold is > 1)", got)
	}
	p.handleLine("[1.1%] 0:00.271/0:24.619 (0.5x), ETA 0:00.000")
	if got := p.Progress().Percent; got != 1.1 {
		t.Errorf("Percent = %v, want 1.1", got)
	}
}

func TestErrorLineFails(t *testing.T) {
	t.Parallel()

	p, err := New(aacCaps(), Options{Input: "ep.flac", Output: "ep.m4a", Bitrate: 192})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	forEachLine(t, "coreaudio_missing.log", func(line string) {
		p.handleLine(line)
	})

	if p.lineErr == nil {
		t.Fatal("line error = nil, want a QAAC error")
	}
	e := okerr.AsError(p.lineErr)
	if e.Kind != okerr.KindTool {
		t.Errorf("kind = %q, want %q", e.Kind, okerr.KindTool)
	}
	if e.Summary != okerr.ErrQAAC.Summary {
		t.Errorf("summary = %q, want %q", e.Summary, okerr.ErrQAAC.Summary)
	}
	// The legacy parser only knew the summary, so Render must produce the
	// operator-facing Constants.qaacErrorMsg.
	if got, want := okerr.Render(e), okerr.MsgQAAC; got != want {
		t.Errorf("Render() = %q, want %q", got, want)
	}
	if !strings.Contains(e.Output, "CoreAudioToolbox.dll") {
		t.Errorf("Output = %q, want it to carry the failing line", e.Output)
	}
}

// TestFirstErrorWins mirrors ThrowException, which stored only the first
// exception while the output was still being drained.
func TestFirstErrorWins(t *testing.T) {
	t.Parallel()

	p, err := New(aacCaps(), Options{Input: "ep.flac", Output: "ep.m4a", Bitrate: 192})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	p.handleLine("ERROR: first failure")
	p.handleLine("ERROR: second failure")
	if !strings.Contains(p.lineErr.Error(), "first failure") {
		t.Errorf("line error = %v, want the first failure", p.lineErr)
	}
}

func TestInformationalLinesAreIgnored(t *testing.T) {
	t.Parallel()

	p, err := New(aacCaps(), Options{Input: "ep.flac", Output: "ep.m4a", Bitrate: 192})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	for _, line := range []string{
		"qaac 2.85, CoreAudioToolbox 7.10.9.0",
		"AAC-LC Encoder, CVBR 192kbps, Quality 96",
		"614400/614400 samples processed in 0:00.985",
		"Overall bitrate: 191.842kbps",
		"Optimizing...done",
	} {
		p.handleLine(line)
	}
	if p.lineErr != nil {
		t.Errorf("line error = %v, want nil", p.lineErr)
	}
	if p.Progress().Percent != 0 {
		t.Errorf("Percent = %v, want 0", p.Progress().Percent)
	}
	// "Optimizing...done" carries the legacy completion marker.
	if !p.Finished() {
		t.Error("Finished() = false, want true after the completion marker")
	}
}

// TestReportReachesSink keeps the sink contract honest: the engine's status
// stream only sees updates that were actually published.
func TestReportReachesSink(t *testing.T) {
	t.Parallel()

	p, err := New(aacCaps(), Options{Input: "ep.flac", Output: "ep.m4a", Bitrate: 192})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	var got []float64
	p.sink = jobproc.ProgressFunc(func(pr jobproc.Progress) { got = append(got, pr.Percent) })

	p.handleLine("[43.7%] 0:10.760/0:24.619 (18.3x), ETA 0:00.000")
	if len(got) != 1 || got[0] != 43.7 {
		t.Fatalf("sink saw %v, want [43.7]", got)
	}
}

func assertArgs(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("args[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

func forEachLine(t *testing.T, name string, fn func(string)) {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fn(sc.Text())
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("read fixture: %v", err)
	}
}
