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

// These tests run the processor against a real vspipe and a real video. They
// are skipped unless OKEGUIDX_VSPIPE names a vspipe binary, because the tool
// does not ship with the repository.
//
// They exist to document, in executable form, what the generated script does on
// a current VapourSynth + L-SMASH: it fails, because `framelist` and the
// _IFrameList frame property do not exist in any L-SMASH release. See the
// package report for the full finding.
//
// CI coverage: NONE. These two remain manual on purpose. The generated script
// calls `core.lsmas.LWLibavSource`, so the test needs a VapourSynth build that
// also loads the L-SMASH-Works plugin. A stock Ubuntu runner cannot supply
// that: Ubuntu has no current `vapoursynth` binary package at all (the source
// package has no release in any supported suite; only untrusted PPAs carry it),
// and no Ubuntu package contains "lsmash" in its name, so `apt-get install
// vapoursynth` is not a thing and the plugin would have to be built from source
// against a VapourSynth that is itself not packaged. Until that changes, run
// these by hand with:
//
//	$env:OKEGUIDX_VSPIPE="<path>/vspipe"
//	$env:OKEGUIDX_IFRAME_VIDEO="<path>/old.mkv"
//	go test ./internal/jobproc/video/iframe/ -run RealVSPipe -v
//
// The skip messages below say the same thing at the point a reader sees them.

// TestRunRealVSPipeScriptIsRejected pins the observed behaviour of the script
// the legacy code generates. The script is reproduced byte for byte, so this
// test is expected to fail loudly if anyone silently "fixes" it without the
// corresponding decision being made.
func TestRunRealVSPipeScriptIsRejected(t *testing.T) {
	vspipe := os.Getenv("OKEGUIDX_VSPIPE")
	video := os.Getenv("OKEGUIDX_IFRAME_VIDEO")
	if vspipe == "" || video == "" {
		t.Skip("NOT RUNNING: OKEGUIDX_VSPIPE and/or OKEGUIDX_IFRAME_VIDEO is unset. " +
			"This needs a real vspipe plus the L-SMASH plugin (core.lsmas), which no " +
			"Ubuntu package provides, so it is manual-only; see the file comment.")
	}

	dir := t.TempDir()
	p := New(Options{
		VSPipe:         vspipe,
		OldFile:        video,
		WorkingPath:    filepath.Join(dir, "ep01"),
		NumberOfFrames: 1 << 30, // larger than any sample, so no frame mismatch
	})

	err := p.Run(context.Background(), jobproc.NopSink{})
	if err == nil {
		t.Fatal("Run() = nil; the generated script is expected to be rejected " +
			"by L-SMASH, so a success means the tool changed")
	}

	e := okerr.AsError(err)
	if e.Summary != okerr.ErrVpy.Summary {
		t.Errorf("summary = %q, want %q", e.Summary, okerr.ErrVpy.Summary)
	}
	if !strings.Contains(e.Detail, "framelist") {
		t.Errorf("detail = %q, want it to name the unsupported framelist argument", e.Detail)
	}

	// The script must have been written before the tool was invoked.
	raw, readErr := os.ReadFile(ScriptPath(p.opts.WorkingPath))
	if readErr != nil {
		t.Fatalf("generated script not written: %v", readErr)
	}
	if !strings.Contains(string(raw), video) {
		t.Errorf("generated script does not name the old file:\n%s", raw)
	}
}

// TestRunRealVSPipeTracebackIsStructured checks that a script failure reaches
// the caller as a structured vpy error carrying the Python traceback, which is
// the path an operator actually sees when the script above fails.
func TestRunRealVSPipeTracebackIsStructured(t *testing.T) {
	vspipe := os.Getenv("OKEGUIDX_VSPIPE")
	if vspipe == "" {
		t.Skip("NOT RUNNING: OKEGUIDX_VSPIPE is unset. This needs a real vspipe " +
			"plus the L-SMASH plugin (core.lsmas), which no Ubuntu package provides, " +
			"so it is manual-only; see the file comment.")
	}

	dir := t.TempDir()
	old := filepath.Join(dir, "old.mkv")
	if err := os.WriteFile(old, []byte("not a video\n"), 0o600); err != nil {
		t.Fatalf("write old file: %v", err)
	}

	p := New(Options{
		VSPipe:         vspipe,
		OldFile:        old,
		WorkingPath:    filepath.Join(dir, "ep01"),
		NumberOfFrames: 100,
	})

	err := p.Run(context.Background(), jobproc.NopSink{})
	if err == nil {
		t.Fatal("Run() = nil, want a failure for an undecodable old file")
	}
	e := okerr.AsError(err)
	if e.Summary != okerr.ErrVpy.Summary {
		t.Errorf("summary = %q, want %q", e.Summary, okerr.ErrVpy.Summary)
	}
	if e.Detail == "" {
		t.Error("detail is empty, want the Python traceback")
	}
	if e.Kind != okerr.KindTool {
		t.Errorf("kind = %q, want %q", e.Kind, okerr.KindTool)
	}
}
