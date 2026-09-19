package volume

import (
	"bufio"
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
)

// The fixtures in testdata/ are real ffmpeg output captured with the exact
// command line this package builds (see the package comment). Three of them use
// the astats filter of the legacy FFmpegVolumeChecker, two use the older
// volumedetect spelling, and one is a run that never reports a level.

// TestExtractLevels drives the whole fixture set through the line parser and
// asserts the values the demuxer's IsEmpty/IsDuplicate rely on.
func TestExtractLevels(t *testing.T) {
	t.Parallel()

	cases := []struct {
		fixture  string
		wantMean float64
		wantMax  float64
		// wantNoMax marks a fixture that never reports a peak level.
		wantNoMax bool
	}{
		{fixture: "astats_normal.log", wantMean: -21.073717, wantMax: -18.063656},
		// Digital silence: astats prints "-inf" for both levels. -Inf dB is
		// what makes TrackInfo.IsEmpty report a silent track.
		{fixture: "astats_silent.log", wantMean: math.Inf(-1), wantMax: math.Inf(-1)},
		// Full scale: the peak is -0.000001, not exactly 0, which is the case
		// the "0.0 dB" fixes in the legacy history were about.
		{fixture: "astats_fullscale.log", wantMean: -0.000001, wantMax: -0.000001},
		// The pre-3d1f31c volumedetect dialect, kept for the older filter.
		{fixture: "volumedetect_normal.log", wantMean: -21.1, wantMax: -18.1},
		{fixture: "volumedetect_silent.log", wantMean: -91, wantMax: -91},
		// A video-only file: ffmpeg exits 0 but the filter never runs.
		{fixture: "no_levels.log", wantNoMax: true},
	}

	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			t.Parallel()

			p := New(Options{})
			forEachLine(t, tc.fixture, func(line string) {
				if err := p.onLine(line); err != nil {
					t.Fatalf("onLine(%q) error = %v", line, err)
				}
			})

			got := p.Result()
			if tc.wantNoMax {
				if p.measured() {
					t.Fatalf("measured() = true, want false (result %+v)", got)
				}
				return
			}
			if !p.measured() {
				t.Fatal("measured() = false, want true")
			}
			if got.MeanVolume != tc.wantMean {
				t.Errorf("MeanVolume = %v, want %v", got.MeanVolume, tc.wantMean)
			}
			if got.MaxVolume != tc.wantMax {
				t.Errorf("MaxVolume = %v, want %v", got.MaxVolume, tc.wantMax)
			}
		})
	}
}

// TestSilentFixtureIsEmptyAndDuplicate checks the values against the consumer's
// thresholds directly: the demuxer reads MeanVolume and MaxVolume out of this
// package and compares them with -70/-30 and with 0.01.
func TestSilentFixtureIsEmptyAndDuplicate(t *testing.T) {
	t.Parallel()

	silent := measureFixture(t, "astats_silent.log")
	if !(silent.MeanVolume < -70 && silent.MaxVolume < -30) {
		t.Errorf("silent levels (%v, %v) are not below the empty thresholds -70/-30",
			silent.MeanVolume, silent.MaxVolume)
	}

	loud := measureFixture(t, "astats_normal.log")
	if loud.MeanVolume < -70 || loud.MaxVolume < -30 {
		t.Errorf("normal levels (%v, %v) look silent", loud.MeanVolume, loud.MaxVolume)
	}
	if math.Abs(loud.MeanVolume-silent.MeanVolume) < 0.01 && math.Abs(loud.MaxVolume-silent.MaxVolume) < 0.01 {
		t.Error("normal and silent levels would be reported as duplicates")
	}
}

// TestLineParsing covers the line-level rules the fixture set cannot show:
// per-line precedence, the first-match-wins order and the last-value-wins
// assignment.
func TestLineParsing(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		lines    []string
		wantMean float64
		wantMax  float64
		wantSet  bool
	}{
		{
			name:     "astats rms only",
			lines:    []string{"[Parsed_astats_0 @ 000001] RMS level dB: -21.073717"},
			wantMean: -21.073717,
		},
		{
			name:    "astats peak only",
			lines:   []string{"[Parsed_astats_0 @ 000001] Peak level dB: -18.063656"},
			wantMax: -18.063656,
			wantSet: true,
		},
		{
			name:     "astats positive infinity folds to silence",
			lines:    []string{"[Parsed_astats_0 @ 000001] RMS level dB: inf"},
			wantMean: math.Inf(-1),
		},
		{
			name:    "astats negative infinity",
			lines:   []string{"[Parsed_astats_0 @ 000001] Peak level dB: -inf"},
			wantMax: math.Inf(-1),
			wantSet: true,
		},
		{
			name:     "volumedetect dialect",
			lines:    []string{"[Parsed_volumedetect_0 @ 000001] mean_volume: -21.1 dB", "[Parsed_volumedetect_0 @ 000001] max_volume: -18.1 dB"},
			wantMean: -21.1,
			wantMax:  -18.1,
			wantSet:  true,
		},
		{
			// A mean line fills only the mean: the peak, which is what ends a
			// run, stays unset.
			name:     "a volumedetect mean line does not set the peak",
			lines:    []string{"[Parsed_volumedetect_0 @ 000001] mean_volume: -21.1 dB"},
			wantMean: -21.1,
			wantSet:  false,
		},
		{
			name:     "the last value of a field wins",
			lines:    []string{"RMS level dB: -21.073717", "RMS level dB: -20.5", "Peak level dB: -18.0"},
			wantMean: -20.5,
			wantMax:  -18,
			wantSet:  true,
		},
		{
			name:     "unrelated astats statistics are ignored",
			lines:    []string{"Max level: 4095.000000", "RMS peak dB: -21.066779", "RMS trough dB: -23.071085", "Entropy: 0.601921"},
			wantMean: 0,
			wantMax:  0,
			wantSet:  false,
		},
		{
			name:    "a mangled capture is not a measurement",
			lines:   []string{"RMS level dB: -", "Peak level dB: ."},
			wantSet: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := New(Options{})
			for _, line := range tc.lines {
				if err := p.onLine(line); err != nil {
					t.Fatalf("onLine(%q) error = %v", line, err)
				}
			}
			if got := p.measured(); got != tc.wantSet {
				t.Errorf("measured() = %v, want %v", got, tc.wantSet)
			}
			got := p.Result()
			if got.MeanVolume != tc.wantMean {
				t.Errorf("MeanVolume = %v, want %v", got.MeanVolume, tc.wantMean)
			}
			if got.MaxVolume != tc.wantMax {
				t.Errorf("MaxVolume = %v, want %v", got.MaxVolume, tc.wantMax)
			}
		})
	}
}

// TestBuildArgs pins the command line to the legacy one. ffmpeg accepts "/dev/null"
// as the null muxer target on Windows too, so one argv works everywhere.
func TestBuildArgs(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "plain path",
			input: "track.flac",
			want:  []string{"-i", "track.flac", "-af", "astats=measure_perchannel=none", "-f", "null", "/dev/null"},
		},
		{
			name:  "path with spaces stays one argument",
			input: `D:\我的 作品\第01話_2.flac`,
			want:  []string{"-i", `D:\我的 作品\第01話_2.flac`, "-af", "astats=measure_perchannel=none", "-f", "null", "/dev/null"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := BuildArgs(tc.input); !slices.Equal(got, tc.want) {
				t.Fatalf("BuildArgs() = %q, want %q", got, tc.want)
			}
			if got := New(Options{Input: tc.input}).Args(); !slices.Equal(got, tc.want) {
				t.Fatalf("Args() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestParseLevel covers the double.TryParse replacement, including the values
// the legacy code folded to negative infinity.
func TestParseLevel(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want float64
	}{
		{"-21.073717", -21.073717},
		{"0.0", 0},
		{"-0.000001", -0.000001},
		{"-inf", math.Inf(-1)},
		{"inf", math.Inf(-1)}, // TryParse("inf") failed and stored -Inf
		{"Infinity", math.Inf(-1)},
		{"-Infinity", math.Inf(-1)},
		{"NaN", math.NaN()},
		{"", math.Inf(-1)},
		{"-", math.Inf(-1)},
		// parseLevel is only reached with a capture the pattern accepted, so
		// "-21" never occurs; it parses to itself here and is rejected by the
		// regex in TestRegexesRejectNonLevels instead.
		{"-21", -21},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			got := parseLevel(tc.in)
			switch {
			case math.IsNaN(tc.want):
				if !math.IsNaN(got) {
					t.Errorf("parseLevel(%q) = %v, want NaN", tc.in, got)
				}
			case got != tc.want:
				t.Errorf("parseLevel(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestRegexesRejectNonLevels guards the patterns against over-matching: the
// capture must be a decimal number or "inf", nothing else.
func TestRegexesRejectNonLevels(t *testing.T) {
	t.Parallel()

	notLevels := []string{
		"RMS peak dB: -21.066779",
		"RMS trough dB: -23.071085",
		"Noise floor dB: -18.060739",
		"mean_volume: -21.1",   // missing the trailing "dB"
		"max_volume: -18.1 dB", // max_volume, not max_volume:
		"Peak level dB: -18",
	}
	for _, line := range notLevels {
		if m := reRMSLevel.FindStringSubmatch(line); m != nil {
			t.Errorf("reRMSLevel matched %q", line)
		}
		if m := rePeakLevel.FindStringSubmatch(line); m != nil {
			t.Errorf("rePeakLevel matched %q", line)
		}
		if m := reMeanVolume.FindStringSubmatch(line); m != nil && !strings.HasPrefix(line, "mean_volume") {
			t.Errorf("reMeanVolume matched %q", line)
		}
		if m := reMaxVolume.FindStringSubmatch(line); m != nil && !strings.HasPrefix(line, "max_volume") {
			t.Errorf("reMaxVolume matched %q", line)
		}
	}
}

// TestLifecycleBeforeStart ensures the control methods are safe no-ops before a
// child process exists.
func TestLifecycleBeforeStart(t *testing.T) {
	t.Parallel()

	p := New(Options{FFmpeg: "/definitely/not/here/ffmpeg"})
	if err := p.Close(); err != nil {
		t.Errorf("Close() = %v, want nil", err)
	}
	if err := p.Pause(); err != nil {
		t.Errorf("Pause() = %v, want nil", err)
	}
	if err := p.Resume(); err != nil {
		t.Errorf("Resume() = %v, want nil", err)
	}
	if err := p.SetPriority(jobproc.PriorityHigh); err != nil {
		t.Errorf("SetPriority() = %v, want nil", err)
	}
	if got := p.Name(); got != Name {
		t.Errorf("Name() = %q, want %q", got, Name)
	}
	if got := (Result{}); got.MeanVolume != 0 || got.MaxVolume != 0 {
		t.Errorf("zero Result = %+v, want both levels 0", got)
	}
}

func TestRunMissingToolFails(t *testing.T) {
	t.Parallel()

	p := New(Options{FFmpeg: "/definitely/not/here/ffmpeg", Input: "track.flac"})
	err := p.Run(t.Context(), jobproc.NopSink{})
	if err == nil {
		t.Fatal("Run() = nil, want a not-found error")
	}
	if got := okerr.AsError(err).Kind; got != okerr.KindNotFound {
		t.Errorf("Kind = %q, want %q", got, okerr.KindNotFound)
	}
}

// The tests below drive a real child process: ffmpeg is replaced by this test
// binary re-executed with a sentinel environment variable, the pattern
// internal/proc and jobproc/mux/simple use. TestMain runs before the testing
// package parses its flags, so the child sees the exact argv the processor
// built.
const (
	helperEnvVar   = "OKEGUIDX_VOLUME_HELPER"
	helperScript   = "OKEGUIDX_VOLUME_HELPER_SCRIPT"
	helperExitCode = "OKEGUIDX_VOLUME_HELPER_EXIT"
)

// TestMain doubles as the fake ffmpeg.
func TestMain(m *testing.M) {
	if os.Getenv(helperEnvVar) == "1" {
		os.Exit(helperMain())
	}
	os.Exit(m.Run())
}

// helperMain replays a captured fixture when one is named, otherwise the
// newline-separated script. A leading '!' sends the line to stderr, which is
// where ffmpeg writes both its banner and the astats report.
func helperMain() int {
	if fixture := os.Getenv(helperScript); strings.HasSuffix(fixture, ".log") {
		data, err := os.ReadFile(filepath.Join("testdata", fixture))
		if err != nil {
			return 3
		}
		_, _ = os.Stderr.Write(data)
	} else {
		for _, line := range strings.Split(fixture, "\n") {
			if line == "" {
				continue
			}
			if rest, ok := strings.CutPrefix(line, "!"); ok {
				_, _ = os.Stderr.WriteString(rest + "\n")
				continue
			}
			_, _ = os.Stdout.WriteString(line + "\n")
		}
	}
	code, _ := strconv.Atoi(os.Getenv(helperExitCode))
	return code
}

func TestRunMeasuresRealFixture(t *testing.T) {
	t.Setenv(helperEnvVar, "1")
	t.Setenv(helperScript, "astats_normal.log")
	t.Setenv(helperExitCode, "0")

	p := New(Options{FFmpeg: os.Args[0], Input: "track.flac"})
	var statuses []string
	err := p.Run(t.Context(), jobproc.ProgressFunc(func(pr jobproc.Progress) {
		statuses = append(statuses, pr.Status)
	}))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	got := p.Result()
	if got.MeanVolume != -21.073717 || got.MaxVolume != -18.063656 {
		t.Errorf("Result() = %+v, want the fixture levels", got)
	}
	if want := []string{StatusDone}; !slices.Equal(statuses, want) {
		t.Errorf("reported statuses = %q, want %q", statuses, want)
	}
}

func TestRunMeasuresSilentFixture(t *testing.T) {
	t.Setenv(helperEnvVar, "1")
	t.Setenv(helperScript, "astats_silent.log")
	t.Setenv(helperExitCode, "0")

	p := New(Options{FFmpeg: os.Args[0], Input: "silent.flac"})
	if err := p.Run(t.Context(), jobproc.NopSink{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	got := p.Result()
	if !math.IsInf(got.MeanVolume, -1) || !math.IsInf(got.MaxVolume, -1) {
		t.Errorf("Result() = %+v, want negative infinity for both levels", got)
	}
}

// TestRunWithoutLevelsFails is the boundary the legacy code could not handle:
// the process exits 0 but no peak level ever appears, and Run must report it
// instead of returning zeros that read as a loud track.
func TestRunWithoutLevelsFails(t *testing.T) {
	t.Setenv(helperEnvVar, "1")
	t.Setenv(helperScript, "no_levels.log")
	t.Setenv(helperExitCode, "0")

	p := New(Options{FFmpeg: os.Args[0], Input: "video-only.mp4"})
	err := p.Run(t.Context(), jobproc.NopSink{})
	if err == nil {
		t.Fatal("Run() = nil, want a failure")
	}
	e := okerr.AsError(err)
	if e.Kind != okerr.KindTool {
		t.Errorf("Kind = %q, want %q", e.Kind, okerr.KindTool)
	}
	if e.Summary != NoLevelsSummary {
		t.Errorf("Summary = %q, want %q", e.Summary, NoLevelsSummary)
	}
	if e.File != "video-only.mp4" {
		t.Errorf("File = %q, want the measured file", e.File)
	}
	if e.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", e.ExitCode)
	}
	if !strings.Contains(e.Output, "video:12kB audio:0kB") {
		t.Errorf("Output = %q, want the tail of the ffmpeg log", e.Output)
	}
}

// TestRunExitFailureIsReported covers a run that dies before reporting
// anything, for example an unreadable input file.
func TestRunExitFailureIsReported(t *testing.T) {
	t.Setenv(helperEnvVar, "1")
	t.Setenv(helperScript, "!track.flac: No such file or directory")
	t.Setenv(helperExitCode, "1")

	p := New(Options{FFmpeg: os.Args[0], Input: "missing.flac"})
	err := p.Run(t.Context(), jobproc.NopSink{})
	if err == nil {
		t.Fatal("Run() = nil, want a failure")
	}
	e := okerr.AsError(err)
	if e.Kind != okerr.KindTool {
		t.Errorf("Kind = %q, want %q", e.Kind, okerr.KindTool)
	}
	if e.Summary != FFmpegErrorSummary {
		t.Errorf("Summary = %q, want %q", e.Summary, FFmpegErrorSummary)
	}
	if e.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", e.ExitCode)
	}
	if !strings.Contains(e.Output, "No such file or directory") {
		t.Errorf("Output = %q, want the ffmpeg message", e.Output)
	}
}

// TestRunAcceptsNonZeroExitAfterMeasurement mirrors the legacy base class,
// which stopped checking exit codes (8e6750e): a stream that was read far
// enough to report a peak is measured, and the exit code is not consulted.
func TestRunAcceptsNonZeroExitAfterMeasurement(t *testing.T) {
	t.Setenv(helperEnvVar, "1")
	t.Setenv(helperScript, "astats_normal.log")
	t.Setenv(helperExitCode, "1")

	p := New(Options{FFmpeg: os.Args[0], Input: "track.flac"})
	if err := p.Run(t.Context(), jobproc.NopSink{}); err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if got := p.Result(); got.MaxVolume != -18.063656 {
		t.Errorf("Result() = %+v, want the fixture levels", got)
	}
}

// TestRunReportsLevelFromStdout checks that both streams are parsed, as the
// legacy ProcessLine was called for stdout and stderr alike.
func TestRunReportsLevelFromStdout(t *testing.T) {
	t.Setenv(helperEnvVar, "1")
	t.Setenv(helperScript, "RMS level dB: -21.073717\nPeak level dB: -18.063656")
	t.Setenv(helperExitCode, "0")

	p := New(Options{FFmpeg: os.Args[0], Input: "track.flac"})
	if err := p.Run(t.Context(), jobproc.NopSink{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := p.Result(); got.MaxVolume != -18.063656 {
		t.Errorf("Result() = %+v, want the stdout peak level", got)
	}
}

func TestRunHonoursCancellation(t *testing.T) {
	t.Setenv(helperEnvVar, "1")
	t.Setenv(helperScript, "")
	t.Setenv(helperExitCode, "0")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	p := New(Options{FFmpeg: os.Args[0], Input: "track.flac"})
	err := p.Run(ctx, jobproc.NopSink{})
	if err == nil {
		t.Fatal("Run() = nil, want a cancellation error")
	}
	if got := okerr.AsError(err).Kind; got != okerr.KindCanceled {
		t.Errorf("Kind = %q, want %q", got, okerr.KindCanceled)
	}
}

// TestCheckerMeasuresFixture drives the VolumeMeasurer implementation the
// demuxer consumes, end to end.
func TestCheckerMeasuresFixture(t *testing.T) {
	t.Setenv(helperEnvVar, "1")
	t.Setenv(helperScript, "volumedetect_normal.log")
	t.Setenv(helperExitCode, "0")

	c := NewChecker(os.Args[0], 0)
	mean, max, err := c.Measure(t.Context(), "track.flac")
	if err != nil {
		t.Fatalf("Measure() error = %v", err)
	}
	if mean != -21.1 || max != -18.1 {
		t.Errorf("Measure() = (%v, %v), want (-21.1, -18.1)", mean, max)
	}
}

func TestCheckerPropagatesFailure(t *testing.T) {
	t.Setenv(helperEnvVar, "1")
	t.Setenv(helperScript, "no_levels.log")
	t.Setenv(helperExitCode, "0")

	c := NewChecker(os.Args[0], 0)
	if _, _, err := c.Measure(t.Context(), "video-only.mp4"); err == nil {
		t.Fatal("Measure() = nil, want a failure")
	} else if !errors.Is(err, &okerr.Error{Kind: okerr.KindTool, Summary: NoLevelsSummary}) {
		t.Errorf("Measure() error = %v, want the no-levels error", err)
	}
}

func TestCheckerMissingTool(t *testing.T) {
	t.Parallel()

	c := NewChecker("/definitely/not/here/ffmpeg", 0)
	if _, _, err := c.Measure(t.Context(), "track.flac"); err == nil {
		t.Fatal("Measure() = nil, want a not-found error")
	} else if got := okerr.AsError(err).Kind; got != okerr.KindNotFound {
		t.Errorf("Kind = %q, want %q", got, okerr.KindNotFound)
	}
}

// measureFixture replays one fixture through the line parser.
func measureFixture(t *testing.T, name string) Result {
	t.Helper()
	p := New(Options{})
	forEachLine(t, name, func(line string) {
		if err := p.onLine(line); err != nil {
			t.Fatalf("onLine(%q) error = %v", line, err)
		}
	})
	return p.Result()
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
