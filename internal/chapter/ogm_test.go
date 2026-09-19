package chapter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// tick is one .NET TimeSpan tick, the unit the reference table below uses.
const tick = 100 * time.Nanosecond

// TestFormatTimestamp pins the exact bytes TChapter's ChapterUtil.Time2String
// produces. The expectations were measured by running the .NET expression
// `Math.Round((TotalSeconds - Floor(TotalSeconds)) * 1000)` and formatting the
// result with `{0:D2}:{1:D2}:{2:D2}.{3:D3}`.
func TestFormatTimestamp(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"zero", 0, "00:00:00.000"},
		{"whole millisecond", 41*time.Second + 41*time.Millisecond, "00:00:41.041"},
		{"two minutes", 2*time.Minute + 12*time.Second + 799*time.Millisecond, "00:02:12.799"},
		{"over an hour", time.Hour + 2*time.Minute + 3*time.Second + 456*time.Millisecond, "01:02:03.456"},
		// Halfway values go to the even neighbour: 1.5 -> 2, 2.5 -> 2.
		{"half rounds to even (1.5 ms)", 15000 * tick, "00:00:00.002"},
		{"half rounds to even (2.5 ms)", 25000 * tick, "00:00:00.002"},
		{"half rounds to even (3.5 ms)", 35000 * tick, "00:00:00.004"},
		{"half rounds to even (4.5 ms)", 45000 * tick, "00:00:00.004"},
		{"below half rounds down", 5000 * tick, "00:00:00.000"},
		// The fraction can round up to 1000, which renders as four digits.
		{"rounds up to four digits", 19999999 * tick, "00:00:01.1000"},
		{"ordinary sub-second", 12345678 * tick, "00:00:01.235"},
		{"the ogm fixture's duration", 13618600000 * tick, "00:22:41.860"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := FormatTimestamp(tt.d); got != tt.want {
				t.Errorf("FormatTimestamp(%v) = %q, want %q", tt.d, got, tt.want)
			}
		})
	}
}

// TestOGMMatchesTheReferenceWriter checks the exact line layout of
// TChapter.ConvertUtil.ToOGM, CRLF terminators included.
func TestOGMMatchesTheReferenceWriter(t *testing.T) {
	t.Parallel()
	info := &Info{Chapters: []Chapter{
		{Number: 1, Time: 0, Name: "Chapter 01"},
		{Number: 2, Time: 41*time.Second + 41*time.Millisecond, Name: "Chapter 02"},
	}}
	want := "CHAPTER01=00:00:00.000\r\n" +
		"CHAPTER01NAME=Chapter 01\r\n" +
		"CHAPTER02=00:00:41.041\r\n" +
		"CHAPTER02NAME=Chapter 02\r\n"
	if got := info.OGM(); got != want {
		t.Errorf("OGM() =\n%q\nwant\n%q", got, want)
	}
}

// TestOGMNumberingIsNotTruncated: the reference formats with D2, which pads to
// two digits but keeps growing past 99.
func TestOGMNumberingIsNotTruncated(t *testing.T) {
	t.Parallel()
	info := &Info{Chapters: []Chapter{{Number: 100, Time: 0, Name: "x"}}}
	got := info.OGM()
	if !strings.Contains(got, "CHAPTER100=") {
		t.Errorf("OGM() = %q, want a three-digit chapter number", got)
	}
}

// TestOGMEmptyListIsEmpty: an Info with no chapters writes nothing rather than
// a stray newline.
func TestOGMEmptyListIsEmpty(t *testing.T) {
	t.Parallel()
	if got := (&Info{}).OGM(); got != "" {
		t.Errorf("OGM() = %q, want an empty string", got)
	}
}

// TestWriteOGMUsesABom: .NET's File.WriteAllText with Encoding.UTF8 emitted a
// UTF-8 BOM, and the operators' existing chapter files carry one.
func TestWriteOGMUsesABom(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "ep01.txt")

	info := &Info{Chapters: []Chapter{{Number: 1, Time: 0, Name: "Chapter 01"}}}
	if err := info.WriteOGM(path); err != nil {
		t.Fatalf("WriteOGM() error = %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(raw) < 3 || raw[0] != 0xEF || raw[1] != 0xBB || raw[2] != 0xBF {
		t.Fatalf("file does not start with a UTF-8 BOM: % x", raw[:min(3, len(raw))])
	}
	if body := string(raw[3:]); body != info.OGM() {
		t.Errorf("body = %q, want %q", body, info.OGM())
	}
}

// TestOGMRoundTripsThroughTheCli is the interesting property: what the engine
// writes must be readable by the parser that will read it back on a re-run.
func TestOGMRoundTripsThroughTheCli(t *testing.T) {
	t.Parallel()
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

	// The fake tool replays the real CLI's document shape; the times below are
	// what the CLI reports for the file just written (see
	// TestParserReadsRealOgmFixture for the real capture).
	body := `{"version":1,"format":"ogm","entries":[{"title":"","source":"","fps_num":0,"fps_den":1,"duration_ns":132799000000,"chapters":[` +
		`{"name":"Chapter 01","time_ns":0,"frames":-1},` +
		`{"name":"Chapter 02","time_ns":41041000000,"frames":-1},` +
		`{"name":"Chapter 03","time_ns":132799000000,"frames":-1}]}]}`
	info, err := fakeParser(body).ParseFirst(t.Context(), path)
	if err != nil {
		t.Fatalf("ParseFirst() error = %v", err)
	}
	if info.Count() != original.Count() {
		t.Fatalf("round trip lost chapters: %d, want %d", info.Count(), original.Count())
	}
	for k, want := range original.Chapters {
		if got := info.Chapters[k]; got.Time != want.Time || got.Name != want.Name {
			t.Errorf("chapter %d = %+v, want %+v", k, got, want)
		}
	}
}

func TestQPFile(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		frames []int64
		want   string
	}{
		{"empty", nil, ""},
		{"single", []int64{0}, "0 I\r\n"},
		{"several", []int64{0, 24, 1440}, "0 I\r\n24 I\r\n1440 I\r\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := QPFile(tt.frames); got != tt.want {
				t.Errorf("QPFile(%v) = %q, want %q", tt.frames, got, tt.want)
			}
		})
	}
}

func TestQPFileAV1(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		frames []int64
		want   string
	}{
		{"empty", nil, ""},
		{"single", []int64{0}, "0f"},
		{"several", []int64{0, 24, 1440}, "0f,24f,1440f"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := QPFileAV1(tt.frames); got != tt.want {
				t.Errorf("QPFileAV1(%v) = %q, want %q", tt.frames, got, tt.want)
			}
		})
	}
}

func TestWriteQPFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "ep01.qpf")
	if err := WriteQPFile(path, QPFile([]int64{0, 24})); err != nil {
		t.Fatalf("WriteQPFile() error = %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if want := "0 I\r\n24 I\r\n"; string(raw) != want {
		t.Errorf("file = %q, want %q", raw, want)
	}
}
