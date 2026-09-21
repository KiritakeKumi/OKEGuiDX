package chapter

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The tests below drive the real sidecars rather than the replayed fixtures.
// They are skipped unless the environment names the binaries, so a normal
// `go test ./...` stays hermetic while a maintainer can run the end-to-end
// check with:
//
//	$env:OKEGUIDX_TCHAPTER="<repo>/native/tchapter/build/tchapter.exe"
//	$env:OKEGUIDX_TCHAPTER_FIXTURE="<repo>/native/tchapter/testdata/OGM/00001.txt"
//	$env:OKEGUIDX_FFPROBE="<path>/ffprobe.exe"
//	$env:OKEGUIDX_FFPROBE_FIXTURE="<path>/ep01.mkv"
//	go test ./internal/chapter/ -run Real -v
//
// This is what proves the JSON contract in parser.go and probe.go matches what
// the tools actually print, which a fixture alone cannot.
//
// CI coverage (the "Real-tool tests" step of the "build and test" job in
// .github/workflows/ci.yml). All three of these now run on the Ubuntu runner,
// so a green CI run is real coverage rather than an implied one:
//
//   - TestParserAgainstRealCLI: the workflow builds the CLI with
//     `make cli` in native/tchapter and points OKEGUIDX_TCHAPTER_FIXTURE at the
//     git-tracked native/tchapter/testdata/OGM/00001.txt (13 marks, last at
//     22:41.860 — the assertions below are tied to that exact file).
//   - TestOGMRoundTripsThroughTheRealCLI: same CLI, no fixture needed.
//   - TestProbeAgainstRealFFprobe: the workflow installs ffmpeg and points
//     OKEGUIDX_FFPROBE_FIXTURE at the git-tracked
//     native/tchapter/testdata/mkv-00001.mkv (a real 29.15 s Matroska file).
//
// The skip branches below are therefore only reached when someone runs the
// suite by hand without the tools; they no longer describe the CI run.

func TestParserAgainstRealCLI(t *testing.T) {
	tool := os.Getenv("OKEGUIDX_TCHAPTER")
	fixture := os.Getenv("OKEGUIDX_TCHAPTER_FIXTURE")
	if tool == "" || fixture == "" {
		t.Skip("NOT RUNNING: OKEGUIDX_TCHAPTER and/or OKEGUIDX_TCHAPTER_FIXTURE is unset. " +
			"Set both to a built native/tchapter/build/tchapter[.exe] and a chapter file " +
			"(native/tchapter/testdata/OGM/00001.txt) to run this against the real CLI.")
	}

	info, err := (&Parser{Tool: tool}).ParseFirst(t.Context(), fixture)
	if err != nil {
		t.Fatalf("ParseFirst() error = %v", err)
	}
	if info == nil {
		t.Fatal("ParseFirst() = nil, want an entry")
	}
	// native/tchapter/testdata/OGM/00001.txt is a known fixture: 13 marks,
	// the first at zero and the last at 22:41.860.
	if info.Count() != 13 {
		t.Errorf("chapter count = %d, want 13", info.Count())
	}
	if info.Chapters[0].Time != 0 {
		t.Errorf("first chapter = %v, want 0", info.Chapters[0].Time)
	}
	if want := 22*time.Minute + 41*time.Second + 860*time.Millisecond; info.Duration != want {
		t.Errorf("duration = %v, want %v", info.Duration, want)
	}
	t.Logf("parsed %d chapters, duration %v", info.Count(), info.Duration)
}

// TestOGMRoundTripsThroughTheRealCLI writes a chapter file and reads it back
// with the actual libtchapter binary. This is the property that matters: the
// engine writes the file for the muxer and for the next run's chapter lookup,
// and the parser that reads it back is the CLI.
func TestOGMRoundTripsThroughTheRealCLI(t *testing.T) {
	tool := os.Getenv("OKEGUIDX_TCHAPTER")
	if tool == "" {
		t.Skip("NOT RUNNING: OKEGUIDX_TCHAPTER is unset. Set it to a built " +
			"native/tchapter/build/tchapter[.exe] to run this against the real CLI.")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "ep01.txt")

	original := &Info{Chapters: []Chapter{
		{Number: 1, Time: 0, Name: "Chapter 01"},
		{Number: 2, Time: 41*time.Second + 41*time.Millisecond, Name: "Chapter 02"},
		{Number: 3, Time: 2*time.Minute + 12*time.Second + 799*time.Millisecond, Name: "Chapter 03"},
	}}
	if err := original.WriteOGM(path); err != nil {
		t.Fatalf("WriteOGM() error = %v", err)
	}

	info, err := (&Parser{Tool: tool}).ParseFirst(t.Context(), path)
	if err != nil {
		t.Fatalf("ParseFirst() error = %v", err)
	}
	if info == nil {
		t.Fatal("ParseFirst() = nil, want an entry")
	}
	if info.Count() != original.Count() {
		t.Fatalf("round trip lost chapters: %d, want %d", info.Count(), original.Count())
	}
	for k, want := range original.Chapters {
		got := info.Chapters[k]
		if got.Time != want.Time {
			t.Errorf("chapter %d time = %v, want %v", k, got.Time, want.Time)
		}
		if got.Name != want.Name {
			t.Errorf("chapter %d name = %q, want %q", k, got.Name, want.Name)
		}
	}
}

func TestProbeAgainstRealFFprobe(t *testing.T) {
	tool := os.Getenv("OKEGUIDX_FFPROBE")
	fixture := os.Getenv("OKEGUIDX_FFPROBE_FIXTURE")
	if tool == "" || fixture == "" {
		t.Skip("NOT RUNNING: OKEGUIDX_FFPROBE and/or OKEGUIDX_FFPROBE_FIXTURE is unset. " +
			"Set both to an ffprobe executable and a media file with a duration " +
			"(native/tchapter/testdata/mkv-00001.mkv) to run this against the real tool.")
	}

	res, err := (&Probe{Tool: tool}).Run(t.Context(), fixture)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	t.Logf("chapters=%d duration=%dms", res.ChapterCount, res.DurationMS)
	if res.DurationMS <= 0 {
		t.Errorf("DurationMS = %d, want a positive duration", res.DurationMS)
	}
}
