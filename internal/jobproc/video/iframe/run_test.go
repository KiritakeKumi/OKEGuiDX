package iframe

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
)

// Run is driven end to end with a fake vspipe (see helper_test.go), so the
// script writing, the process wiring and the parsing are all exercised
// together, through the exact argv production builds.
//
// These tests are deliberately not parallel: the fake vspipe is selected
// through the process environment, which is global to the test binary.

// startProcessor builds a processor whose "vspipe" is this test binary.
func startProcessor(t *testing.T, frames int64) *Processor {
	t.Helper()
	dir := t.TempDir()
	return New(Options{
		VSPipe:         os.Args[0],
		OldFile:        filepath.Join(dir, "old release.mkv"),
		WorkingPath:    filepath.Join(dir, "ep01"),
		NumberOfFrames: frames,
	})
}

// withRole configures the fake vspipe for the duration of a test.
func withRole(t *testing.T, role, fixture string) {
	t.Helper()
	t.Setenv(helperEnvVar, "1")
	t.Setenv(helperRoleEnvVar, role)
	t.Setenv(helperFixtureEnvVar, fixture)
}

func TestRunWritesScriptAndParsesList(t *testing.T) {
	p := startProcessor(t, 35175)
	withRole(t, roleFixture, filepath.Join("testdata", "iframes-typical.txt"))

	if err := p.Run(context.Background(), jobproc.NopSink{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	// The generated script is on disk and byte-identical to the template.
	script := ScriptPath(p.opts.WorkingPath)
	raw, err := os.ReadFile(script)
	if err != nil {
		t.Fatalf("generated script not written: %v", err)
	}
	if got, want := string(raw), ScriptContent(p.opts.OldFile); got != want {
		t.Errorf("generated script =\n%q\nwant\n%q", got, want)
	}

	got := p.IFrames()
	// 300 captured I-frames plus the appended frame count.
	if len(got) != 301 {
		t.Fatalf("len(IFrames()) = %d, want 301", len(got))
	}
	if got[0] != 0 || got[len(got)-1] != 35175 {
		t.Errorf("IFrames() ends = [%d, %d], want [0, 35175]", got[0], got[len(got)-1])
	}
}

// The command line must stay `--info <script> -`. The legacy code omitted the
// trailing `-`, which vspipe R42 rejects outright; this test pins the fix.
func TestRunCommandLine(t *testing.T) {
	p := startProcessor(t, 35175)
	record := filepath.Join(t.TempDir(), "argv.txt")
	withRole(t, roleFixture, filepath.Join("testdata", "iframes-typical.txt"))
	t.Setenv(helperRecordEnvVar, record)

	if err := p.Run(context.Background(), jobproc.NopSink{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("read recorded argv: %v", err)
	}
	got := strings.Split(strings.TrimSpace(string(raw)), "\n")
	want := []string{"--info", ScriptPath(p.opts.WorkingPath), "-"}
	if len(got) != len(want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argv[%d] = %q, want %q (full: %q)", i, got[i], want[i], got)
		}
	}
}

func TestRunWithoutListFails(t *testing.T) {
	p := startProcessor(t, 100)
	withRole(t, roleNoList, "")

	err := p.Run(context.Background(), jobproc.NopSink{})
	if err == nil {
		t.Fatal("Run() = nil, want a failure when no I-frame list is printed")
	}
	e := okerr.AsError(err)
	if e.Kind != okerr.KindTool {
		t.Errorf("kind = %q, want %q", e.Kind, okerr.KindTool)
	}
	if e.Summary != okerr.ErrVpy.Summary {
		t.Errorf("summary = %q, want %q", e.Summary, okerr.ErrVpy.Summary)
	}
}

func TestRunProcessFailureIsStructured(t *testing.T) {
	p := startProcessor(t, 100)
	withRole(t, roleFail, "")

	err := p.Run(context.Background(), jobproc.NopSink{})
	if err == nil {
		t.Fatal("Run() = nil, want a failure when vspipe exits non-zero")
	}
	e := okerr.AsError(err)
	if e.Kind != okerr.KindTool {
		t.Errorf("kind = %q, want %q", e.Kind, okerr.KindTool)
	}
	if e.Summary != okerr.ErrVpy.Summary {
		t.Errorf("summary = %q, want %q", e.Summary, okerr.ErrVpy.Summary)
	}
	if !strings.Contains(e.Output, "some diagnostic") {
		t.Errorf("output = %q, want the tool's diagnostics", e.Output)
	}
}

func TestRunCancellation(t *testing.T) {
	p := startProcessor(t, 100)
	withRole(t, roleEmpty, "")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := p.Run(ctx, jobproc.NopSink{})
	if err == nil {
		t.Fatal("Run() = nil, want a cancellation error")
	}
	if got := okerr.AsError(err).Kind; got != okerr.KindCanceled {
		t.Errorf("kind = %q, want %q", got, okerr.KindCanceled)
	}
}

// A traceback raised by the script must reach the caller as a vpy error even
// when the process itself exits zero, which is what vspipe does for a script
// failure in some builds.
func TestRunScriptErrorIsReported(t *testing.T) {
	p := startProcessor(t, 35175)
	withRole(t, roleFixture, filepath.Join("testdata", "python-error.txt"))

	err := p.Run(context.Background(), jobproc.NopSink{})
	if err == nil {
		t.Fatal("Run() = nil, want the script's Python error")
	}
	e := okerr.AsError(err)
	if e.Summary != okerr.ErrVpy.Summary {
		t.Errorf("summary = %q, want %q", e.Summary, okerr.ErrVpy.Summary)
	}
	if !strings.Contains(e.Detail, "failed to open the file") {
		t.Errorf("detail = %q, want the Python error text", e.Detail)
	}
}

func TestRunUnwritableScriptFails(t *testing.T) {
	// A working path in a directory that does not exist cannot be written to.
	dir := t.TempDir()
	p := New(Options{
		VSPipe:         os.Args[0],
		OldFile:        "old.mkv",
		WorkingPath:    filepath.Join(dir, "missing", "ep01"),
		NumberOfFrames: 100,
	})
	withRole(t, roleFixture, filepath.Join("testdata", "iframes-typical.txt"))

	err := p.Run(context.Background(), jobproc.NopSink{})
	if err == nil {
		t.Fatal("Run() = nil, want a write failure")
	}
	e := okerr.AsError(err)
	if e.Kind != okerr.KindIO {
		t.Errorf("kind = %q, want %q", e.Kind, okerr.KindIO)
	}
}

func TestRunCloseIsSafe(t *testing.T) {
	t.Parallel()
	p := New(Options{})
	if err := p.Close(); err != nil {
		t.Errorf("Close() = %v, want nil", err)
	}
	if err := p.Close(); err != nil {
		t.Errorf("second Close() = %v, want nil", err)
	}
}

func TestName(t *testing.T) {
	t.Parallel()
	if got := New(Options{}).Name(); got != Name {
		t.Errorf("Name() = %q, want %q", got, Name)
	}
}
