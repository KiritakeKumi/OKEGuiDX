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

func TestParserAgainstRealCLI(t *testing.T) {
	tool := os.Getenv("OKEGUIDX_TCHAPTER")
	fixture := os.Getenv("OKEGUIDX_TCHAPTER_FIXTURE")
	if tool == "" || fixture == "" {
		t.Skip("set OKEGUIDX_TCHAPTER and OKEGUIDX_TCHAPTER_FIXTURE to run")
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
		t.Skip("set OKEGUIDX_TCHAPTER to run")
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
		t.Skip("set OKEGUIDX_FFPROBE and OKEGUIDX_FFPROBE_FIXTURE to run")
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
