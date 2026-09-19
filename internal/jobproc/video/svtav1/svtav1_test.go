package svtav1

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
)

// The fixtures in testdata/ are captured from real SvtAv1EncApp runs, using the
// exact argument shape New() produces ("--progress 2 <params> -b <out> -i - -o
// NUL"). v141_stdin.txt comes from a v1.4.1 build with -DLOG_ENC_DONE=1, which
// is the dialect SVTAV1Encoder.cs was written against; v420_stdin.txt comes
// from the v4.2.0 build that OKEGuiDX-tools' versions.lock pins. See
// testdata/README.md for the full provenance.

// scannerLines reproduces how internal/proc hands output to the parser: a
// bufio.Scanner that splits on "\n" only, so each returned string can contain
// many carriage-return separated progress records.
func scannerLines(t *testing.T, name string) []string {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer func() { _ = f.Close() }()

	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return lines
}

// TestScannerDeliversProgressAsOneLine guards the assumption the parser is
// built on. If the scanner ever split on "\r" the in-line scan would become
// dead weight, and if it stopped delivering the blob the parser would silently
// stop reporting progress.
func TestScannerDeliversProgressAsOneLine(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"v141_stdin.txt", "v420_stdin.txt"} {
		lines := scannerLines(t, name)
		withCR := 0
		for _, l := range lines {
			if strings.Contains(l, "\r") {
				withCR++
			}
		}
		if withCR == 0 {
			t.Errorf("%s: no scanned line contains a carriage return; "+
				"the in-line scan is no longer exercised", name)
		}
	}
}

func TestParseV141Progress(t *testing.T) {
	t.Parallel()
	p := New(Options{TotalFrames: 120})

	var (
		updates     int
		done        int64
		lastPercent float64
		speed       string
		bitrate     string
	)
	for _, line := range scannerLines(t, "v141_stdin.txt") {
		prog, ok, err := p.parse(line)
		if err != nil {
			t.Fatalf("parse error = %v", err)
		}
		if !ok {
			continue
		}
		updates++
		if prog.FramesDone > 0 {
			done = prog.FramesDone
		}
		if prog.Percent >= 0 {
			lastPercent = prog.Percent
		}
		if prog.Speed != "" {
			speed = prog.Speed
		}
		if prog.BitRate != "" {
			bitrate = prog.BitRate
		}
	}

	if updates == 0 {
		t.Fatal("no progress records recognised in the v1.4.1 fixture")
	}
	if done != 120 {
		t.Errorf("final frame count = %d, want 120", done)
	}
	if lastPercent != 100 {
		t.Errorf("final percent = %v, want 100", lastPercent)
	}
	if !strings.HasSuffix(speed, " fps") {
		t.Errorf("Speed = %q, want an fps suffix", speed)
	}
	if !strings.HasSuffix(bitrate, " kb/s") {
		t.Errorf("BitRate = %q, want a kb/s suffix", bitrate)
	}
}

func TestParseV420Progress(t *testing.T) {
	t.Parallel()
	p := New(Options{TotalFrames: 120})

	var (
		updates     int
		done        int64
		lastPercent float64
		speed       string
		bitrate     string
	)
	for _, line := range scannerLines(t, "v420_stdin.txt") {
		prog, ok, err := p.parse(line)
		if err != nil {
			t.Fatalf("parse error = %v", err)
		}
		if !ok {
			continue
		}
		updates++
		if prog.FramesDone > 0 {
			done = prog.FramesDone
		}
		if prog.Percent >= 0 {
			lastPercent = prog.Percent
		}
		if prog.Speed != "" {
			speed = prog.Speed
		}
		if prog.BitRate != "" {
			bitrate = prog.BitRate
		}
	}

	if updates == 0 {
		t.Fatal("no progress records recognised in the v4.2.0 fixture")
	}
	if done != 120 {
		t.Errorf("final frame count = %d, want 120", done)
	}
	if lastPercent != 100 {
		t.Errorf("final percent = %v, want 100", lastPercent)
	}
	if speed == "" {
		t.Error("no speed parsed from the v4.2.0 fixture")
	}
	if bitrate == "" {
		t.Error("no bitrate parsed from the v4.2.0 fixture")
	}
}

// TestParseLastRecordWins pins the behaviour that makes the in-line scan
// worthwhile: a single scanned line carries many records, and the newest frame
// count must be the one reported.
func TestParseLastRecordWins(t *testing.T) {
	t.Parallel()
	p := New(Options{TotalFrames: 120})
	blob := "Encoding:    1/120 Frames @ 60.61 fps | 6.51 kb/s\r" +
		"Encoding:   50/120 Frames @ 57.00 fps | 114.19 kb/s\r" +
		"Encoding:  120/120 Frames @ 57.29 fps | 287.55 kb/s\r"

	prog, ok, err := p.parse(blob)
	if err != nil {
		t.Fatalf("parse error = %v", err)
	}
	if !ok {
		t.Fatal("blob was not recognised")
	}
	if prog.FramesDone != 120 {
		t.Errorf("FramesDone = %d, want the last record (120)", prog.FramesDone)
	}
	if prog.Speed != "57.29 fps" {
		t.Errorf("Speed = %q, want %q", prog.Speed, "57.29 fps")
	}
	if prog.BitRate != "287.55 kb/s" {
		t.Errorf("BitRate = %q, want %q", prog.BitRate, "287.55 kb/s")
	}
}

// TestProgressWithUnknownTotal covers the pipe case: the app drops the "/total"
// suffix because it cannot know the frame count from a stream.
func TestProgressWithUnknownTotal(t *testing.T) {
	t.Parallel()
	p := New(Options{TotalFrames: 120})

	prog, ok, err := p.parse("Encoding:   42 Frames @ 3.91 fps | 4795.06 kb/s | Size: 1.00 MB")
	if err != nil {
		t.Fatalf("parse error = %v", err)
	}
	if !ok {
		t.Fatal("total-less progress line was not recognised")
	}
	if prog.FramesDone != 42 {
		t.Errorf("FramesDone = %d, want 42", prog.FramesDone)
	}
}

func TestParseLegacyDialect(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		line    string
		frames  int64
		speed   string
		bitrate string
	}{
		{
			name:    "fps",
			line:    "Encoding frame   42 4795.06 kbps 3.91 fps  ",
			frames:  42,
			speed:   "3.91 fps",
			bitrate: "4795.06 kb/s",
		},
		{
			name:    "fpm is divided by sixty",
			line:    "Encoding frame    7 120.00 kbps 30.00 fpm  ",
			frames:  7,
			speed:   "0.50 fps",
			bitrate: "120.00 kb/s",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := New(Options{TotalFrames: 120})
			prog, ok, err := p.parse(tc.line)
			if err != nil {
				t.Fatalf("parse error = %v", err)
			}
			if !ok {
				t.Fatalf("parse(%q) reported no progress", tc.line)
			}
			if prog.FramesDone != tc.frames {
				t.Errorf("FramesDone = %d, want %d", prog.FramesDone, tc.frames)
			}
			if prog.Speed != tc.speed {
				t.Errorf("Speed = %q, want %q", prog.Speed, tc.speed)
			}
			if prog.BitRate != tc.bitrate {
				t.Errorf("BitRate = %q, want %q", prog.BitRate, tc.bitrate)
			}
		})
	}
}

// TestParseSummaryWithoutDoneLine covers builds made without
// -DLOG_ENC_DONE=1, where the summary table is the only end-of-encode signal.
func TestParseSummaryWithoutDoneLine(t *testing.T) {
	t.Parallel()
	p := New(Options{TotalFrames: 120})

	if _, ok, _ := p.parse("Total Frames\t\tFrame Rate\t\tByte Count\t\tBitrate"); ok {
		t.Fatal("the header line should not itself report progress")
	}
	prog, ok, err := p.parse("         120\t\t24.00 fps\t\t    179721\t\t287.55 kbps")
	if err != nil {
		t.Fatalf("parse error = %v", err)
	}
	if !ok {
		t.Fatal("summary row was not recognised")
	}
	if prog.FramesDone != 120 {
		t.Errorf("FramesDone = %d, want 120", prog.FramesDone)
	}
	if prog.Percent != 100 {
		t.Errorf("Percent = %v, want 100", prog.Percent)
	}
	if prog.Status != "压制完成" {
		t.Errorf("Status = %q, want %q", prog.Status, "压制完成")
	}
}

// TestSummaryRowIsIgnoredWithoutHeader keeps the C# rule that the table's first
// row is only read when its header preceded it.
func TestSummaryRowIsIgnoredWithoutHeader(t *testing.T) {
	t.Parallel()
	p := New(Options{TotalFrames: 120})
	if _, ok, _ := p.parse("         120\t\t24.00 fps\t\t    179721\t\t287.55 kbps"); ok {
		t.Error("a summary row without its header reported progress")
	}
}

func TestParseIgnoresInformationalLines(t *testing.T) {
	t.Parallel()
	p := New(Options{TotalFrames: 120})
	info := []string{
		"Svt[info]: SVT [version]:\tSVT-AV1 Encoder Lib v4.2.0",
		"Svt[info]: SVT [config]: preset / tune / pred struct \t\t\t\t\t: 10 / PSNR / random access",
		"Svt[warn]: Screen-content detection and tools are disabled for RA mode coding at M9 and above",
		"SUMMARY -----------------------------------------------------------------",
		"Average Speed:\t\t1269.841 fps",
		"",
	}
	for _, line := range info {
		if _, ok, _ := p.parse(line); ok {
			t.Errorf("parse(%q) reported progress, want none", line)
		}
	}
}

func TestFatalLinesAreDetected(t *testing.T) {
	t.Parallel()
	cases := []struct {
		line     string
		wantKind okerr.Kind
		wantIs   error
	}{
		{
			line:     "Svt[error]: Invalid intra Refresh Type [1-2]",
			wantKind: okerr.KindTool,
			wantIs:   okerr.ErrSVTAV1,
		},
		{
			line:     "[SVT-Error]: single dash long tokens have been removed!",
			wantKind: okerr.KindTool,
			wantIs:   okerr.ErrSVTAV1,
		},
		{
			line:     "Error: Invalid parameter '-i' with value 'missing.y4m'",
			wantKind: okerr.KindTool,
			wantIs:   okerr.ErrSVTAV1,
		},
		{
			line:     "Error: fwrite() call failed when writing frame: 120, plane 0, errno: 28",
			wantKind: okerr.KindToolCrash,
			wantIs:   okerr.ErrSVTAV1Crash,
		},
	}
	for _, tc := range cases {
		t.Run(tc.line, func(t *testing.T) {
			t.Parallel()
			err := fatalLine(tc.line)
			if err == nil {
				t.Fatalf("fatalLine(%q) = nil, want an error", tc.line)
			}
			got := okerr.AsError(err)
			if got.Kind != tc.wantKind {
				t.Errorf("kind = %q, want %q", got.Kind, tc.wantKind)
			}
			if !errorsIs(err, tc.wantIs) {
				t.Errorf("error does not match %v", tc.wantIs)
			}
			if got.Output == "" {
				t.Error("error carries no output for troubleshooting")
			}
		})
	}
}

func TestFatalLineIgnoresNormalOutput(t *testing.T) {
	t.Parallel()
	for _, line := range []string{
		"Svt[info]: SVT [config]: BRC mode / rate factor \t\t\t\t\t: CRF / 35 ",
		"Svt[warn]: Non-RTC M10+ are meant for automation tooling usage.",
		"Encoding:  120/120 Frames @ 57.29 fps | 287.55 kb/s",
		"all_done_encoding  120 frames",
	} {
		if err := fatalLine(line); err != nil {
			t.Errorf("fatalLine(%q) = %v, want nil", line, err)
		}
	}
}

// TestFatalLinesFromRealFailures runs the classifier over the captured stderr
// of two genuine startup failures.
func TestFatalLinesFromRealFailures(t *testing.T) {
	t.Parallel()
	cases := []struct {
		fixture  string
		wantLine string
	}{
		{"error_color_format.txt", "Svt[error]: Only support 420 now"},
		{"error_single_dash.txt", "[SVT-Error]: single dash long tokens have been removed!"},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			t.Parallel()
			var found error
			for _, line := range scannerLines(t, tc.fixture) {
				if err := fatalLine(line); err != nil && found == nil {
					found = err
				}
			}
			if found == nil {
				t.Fatalf("no fatal line detected in %s", tc.fixture)
			}
			if !errorsIs(found, okerr.ErrSVTAV1) {
				t.Errorf("error = %v, want ErrSVTAV1", found)
			}
			if got := okerr.AsError(found).Output; !strings.Contains(got, tc.wantLine) {
				t.Errorf("output = %q, want it to contain %q", got, tc.wantLine)
			}
		})
	}
}

func TestBuildArgs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		params string
		output string
		want   []string
	}{
		{
			name:   "params are tokenised after the progress flag",
			params: "--preset 4 --crf 16",
			output: `C:\out\ep.ivf`,
			want:   []string{"--progress", "2", "--preset", "4", "--crf", "16", "-b", `C:\out\ep.ivf`},
		},
		{
			name:   "no params still asks for progress and names the bitstream",
			output: "out.ivf",
			want:   []string{"--progress", "2", "-b", "out.ivf"},
		},
		{
			name:   "quoted value with space survives",
			params: `--qpfile "my qp.txt" --preset 4`,
			output: "out.ivf",
			want:   []string{"--progress", "2", "--qpfile", "my qp.txt", "--preset", "4", "-b", "out.ivf"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := buildArgs(Options{Params: tc.params, Output: tc.output})
			if len(got) != len(tc.want) {
				t.Fatalf("buildArgs() = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("buildArgs()[%d] = %q, want %q (full: %v)", i, got[i], tc.want[i], got)
				}
			}
		})
	}
}

// TestReconOutputIsNotTheBitstream documents why Output carries the null
// device: the base class appends "-o" after the encoder's arguments, and in
// SVT-AV1 "-o" is the reconstructed YUV path, so pointing it at the bitstream
// makes the app refuse to start.
func TestReconOutputIsNotTheBitstream(t *testing.T) {
	t.Parallel()
	p := New(Options{Output: `C:\out\ep.ivf`})
	if p.opts.Output != `C:\out\ep.ivf` {
		t.Errorf("the processor should keep the real output path, got %q", p.opts.Output)
	}
	if ReconOutput == `C:\out\ep.ivf` {
		t.Error("ReconOutput must not be the bitstream path")
	}
}

func TestPercentWithUnknownTotal(t *testing.T) {
	t.Parallel()
	p := New(Options{})
	prog, ok, err := p.parse("Encoding frame   42 4795.06 kbps 3.91 fps  ")
	if err != nil {
		t.Fatalf("parse error = %v", err)
	}
	if !ok {
		t.Fatal("legacy progress line was not recognised")
	}
	if prog.Percent != -1 {
		t.Errorf("Percent = %v, want -1 when the total is unknown", prog.Percent)
	}
}

// errorsIs reports whether err matches the okerr sentinel target.
func errorsIs(err, target error) bool {
	e := okerr.AsError(err)
	t := okerr.AsError(target)
	if e == nil || t == nil {
		return false
	}
	return e.Is(t)
}
