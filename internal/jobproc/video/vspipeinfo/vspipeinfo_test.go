package vspipeinfo

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
)

// The acceptance criterion for this package is that every property in the
// fixtures is extracted, and that a script failure produces a structured error
// rather than a partially populated result.

// feed runs the line handler over a fixture and returns the processor.
func feed(t *testing.T, name string, opts Options) (*Processor, error) {
	t.Helper()
	p := New(opts)
	handler := p.lineHandler()

	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if err := handler(sc.Text()); err != nil {
			return p, err
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return p, nil
}

func TestParseTenBitCFR(t *testing.T) {
	t.Parallel()
	p, err := feed(t, "10bit-cfr.txt", Options{ExpectedFpsNum: 24000, ExpectedFpsDen: 1001})
	if err != nil {
		t.Fatalf("line handler error = %v", err)
	}
	got := p.Info()

	if got.Width != 1920 {
		t.Errorf("Width = %d, want 1920", got.Width)
	}
	if got.Height != 1080 {
		t.Errorf("Height = %d, want 1080", got.Height)
	}
	if got.NumFrames != 34567 {
		t.Errorf("NumFrames = %d, want 34567", got.NumFrames)
	}
	if got.FpsNum != 24000 || got.FpsDen != 1001 {
		t.Errorf("fps = %d/%d, want 24000/1001", got.FpsNum, got.FpsDen)
	}
	if got.Format.Name != "YUV420P10" {
		t.Errorf("Format.Name = %q, want %q", got.Format.Name, "YUV420P10")
	}
	if got.Format.ColorFamilyName != "YUV" {
		t.Errorf("Format.ColorFamilyName = %q, want %q", got.Format.ColorFamilyName, "YUV")
	}
	if got.Format.BitsPerSample != 10 {
		t.Errorf("Format.BitsPerSample = %d, want 10", got.Format.BitsPerSample)
	}
	if err := p.checkFps(); err != nil {
		t.Errorf("checkFps() error = %v, want nil", err)
	}
}

func TestParseEightBitCFR(t *testing.T) {
	t.Parallel()
	p, err := feed(t, "8bit-cfr.txt", Options{ExpectedFpsNum: 24000, ExpectedFpsDen: 1001})
	if err != nil {
		t.Fatalf("line handler error = %v", err)
	}
	got := p.Info()
	if got.Width != 1280 || got.Height != 720 {
		t.Errorf("dimensions = %dx%d, want 1280x720", got.Width, got.Height)
	}
	if got.Format.BitsPerSample != 8 {
		t.Errorf("Format.BitsPerSample = %d, want 8", got.Format.BitsPerSample)
	}
	if got.Format.Name != "YUV420P8" {
		t.Errorf("Format.Name = %q, want %q", got.Format.Name, "YUV420P8")
	}
}

func TestFpsMismatchIsRejected(t *testing.T) {
	t.Parallel()
	// The fixture reports 23.976; a profile asking for 25 must be refused.
	p, err := feed(t, "10bit-cfr.txt", Options{ExpectedFpsNum: 25, ExpectedFpsDen: 1})
	if err != nil {
		t.Fatalf("line handler error = %v", err)
	}
	err = p.checkFps()
	if err == nil {
		t.Fatal("checkFps() = nil, want a mismatch error")
	}
	if !okerr.AsError(err).Is(okerr.ErrFpsMismatch) {
		t.Errorf("error = %v, want it to match ErrFpsMismatch", err)
	}
	// The message must name both rates, because that is what the operator needs
	// to fix the profile.
	detail := okerr.AsError(err).Detail
	if detail == "" {
		t.Error("Detail is empty, want both frame rates named")
	}
}

func TestVFRJobSkipsFpsCheck(t *testing.T) {
	t.Parallel()
	// A VFR profile deliberately does not know the source's rate, so a
	// mismatch is not a failure; the source's rate is adopted instead.
	p, err := feed(t, "vfr-source.txt", Options{
		ExpectedFpsNum: 24000, ExpectedFpsDen: 1001, VFR: true,
	})
	if err != nil {
		t.Fatalf("line handler error = %v", err)
	}
	if err := p.checkFps(); err != nil {
		t.Fatalf("checkFps() error = %v, want nil for a VFR job", err)
	}
	if !p.Info().VFR {
		t.Error("Info().VFR = false, want true")
	}
	if got := p.Info().FpsNum; got != 30000 {
		t.Errorf("FpsNum = %d, want the source's 30000", got)
	}
}

func TestLwiProgressIsReported(t *testing.T) {
	t.Parallel()
	p, err := feed(t, "lwi-progress.txt", Options{ExpectedFpsNum: 24000, ExpectedFpsDen: 1001})
	if err != nil {
		t.Fatalf("line handler error = %v", err)
	}
	// The last index line reports 100%, so the recorded progress is complete.
	p.mu.Lock()
	got := p.progress
	p.mu.Unlock()
	if got != 100 {
		t.Errorf("progress = %v, want 100", got)
	}
	// The properties after the index lines must still have been parsed.
	if p.Info().NumFrames != 34567 {
		t.Errorf("NumFrames = %d, want 34567", p.Info().NumFrames)
	}
}

func TestPythonExceptionBecomesStructuredError(t *testing.T) {
	t.Parallel()
	_, err := feed(t, "python-error.txt", Options{})
	if err == nil {
		t.Fatal("line handler = nil, want a script error")
	}
	e := okerr.AsError(err)
	if !e.Is(okerr.ErrVpy) {
		t.Errorf("summary = %q, want %q", e.Summary, okerr.ErrVpy.Summary)
	}
	// The collected traceback is the detail; without it the operator cannot
	// tell which line of the script failed.
	if e.Detail == "" {
		t.Error("Detail is empty, want the collected traceback")
	}
}

func TestVariableFrameRateIsRejected(t *testing.T) {
	t.Parallel()
	p := New(Options{})
	handler := p.lineHandler()
	err := handler("FPS: Variable (23.976 fps)")
	if err == nil {
		t.Fatal("handler = nil, want a rejection for a variable frame rate")
	}
	if got := okerr.AsError(err).Kind; got != okerr.KindUnsupported {
		t.Errorf("kind = %q, want %q", got, okerr.KindUnsupported)
	}
}

func TestInfoLinesAreIgnored(t *testing.T) {
	t.Parallel()
	p := New(Options{})
	handler := p.lineHandler()
	// Lines that mention a property word but carry no value must not corrupt
	// the result, which is what the original's TryParse guards were for.
	for _, line := range []string{
		"Width: abc",
		"Height: ",
		"Frames: 0",
		"FPS: 0/0 (0.0 fps)",
		"",
		"vspipe version 68",
	} {
		if err := handler(line); err != nil {
			t.Fatalf("handler(%q) error = %v", line, err)
		}
	}
	got := p.Info()
	if got.Width != 0 || got.Height != 0 || got.NumFrames != 0 || got.FpsNum != 0 {
		t.Errorf("invalid lines produced data: %+v", got)
	}
}

func TestValidateDetectsSilentFailure(t *testing.T) {
	t.Parallel()
	// A script that never reaches set_output produces no properties at all.
	p := New(Options{})
	if err := p.Validate(); err == nil {
		t.Fatal("Validate() = nil, want an error when nothing was parsed")
	}

	p2, err := feed(t, "10bit-cfr.txt", Options{})
	if err != nil {
		t.Fatalf("line handler error = %v", err)
	}
	if err := p2.Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil for a parsed result", err)
	}
}

func TestCheckFpsRejectsMissingRate(t *testing.T) {
	t.Parallel()
	p := New(Options{ExpectedFpsNum: 24000, ExpectedFpsDen: 1001})
	if err := p.checkFps(); err == nil {
		t.Fatal("checkFps() = nil, want an error when vspipe reported no rate")
	}
}

func TestFormatFps(t *testing.T) {
	t.Parallel()
	cases := []struct {
		num, den int64
		want     string
	}{
		{24000, 1001, "23.976"},
		{25, 1, "25.000"},
		{30000, 1001, "29.970"},
		{1, 0, "0.000"},
	}
	for _, tc := range cases {
		if got := formatFps(tc.num, tc.den); got != tc.want {
			t.Errorf("formatFps(%d, %d) = %q, want %q", tc.num, tc.den, got, tc.want)
		}
	}
}

func TestName(t *testing.T) {
	t.Parallel()
	if got := New(Options{}).Name(); got != Name {
		t.Errorf("Name() = %q, want %q", got, Name)
	}
}

func TestRunMissingTool(t *testing.T) {
	t.Parallel()
	p := New(Options{VSPipe: "/definitely/not/here/vspipe", Script: "x.vpy"})
	if err := p.Run(t.Context(), nil); err == nil {
		t.Fatal("Run() = nil, want a not-found error")
	}
}

// TestMain turns the test binary into a vspipe stand-in for the cancellation
// test. The pipeline drives real processes, and this package is the first stage
// of it: a cancel that arrives while vspipe is still running has to kill it.
func TestMain(m *testing.M) {
	if os.Getenv(fakeEnvMarker) != "" {
		runFakeTool()
		return
	}
	os.Exit(m.Run())
}

// fakeEnvMarker identifies a child that must behave as a tool.
const fakeEnvMarker = "OKEGUIDX_VSPIPEINFO_FAKE"

// runFakeTool is the child body: it prints the properties a real vspipe would
// and then hangs, so the test can cancel while the process is alive.
//
// The wait is a sleep rather than an empty select: a `select {}` in a program
// with one goroutine trips the runtime's deadlock detector and exits the child
// before the test can cancel it.
func runFakeTool() {
	os.Stdout.WriteString("Width: 1920\nHeight: 1080\nFrames: 100\nFPS: 24000/1001 (23.976 fps)\n")
	time.Sleep(time.Hour)
}

// TestRunKillsTheChildOnCancel pins the mechanism: the processor watches the
// context and kills vspipe, instead of blocking on its pipes until the script
// finishes on its own. Without the watcher a cancelled task stays RUNNING
// forever, which is exactly what a manual cancel of a hanging index build
// showed.
func TestRunKillsTheChildOnCancel(t *testing.T) {
	// The child is the test binary, which needs the marker to act as a tool.
	t.Setenv(fakeEnvMarker, "1")
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	p := New(Options{VSPipe: self, Script: "x.vpy"})
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx, nil) }()

	// Give the child time to start and print its properties, then cancel.
	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run() = nil, want a cancellation error")
		}
		if okerr.AsError(err).Kind != okerr.KindCanceled {
			t.Errorf("Run() error = %v, want a cancellation error", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run() did not return after the context was cancelled: the child was not killed")
	}
}
