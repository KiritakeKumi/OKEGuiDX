package x265

import (
	"bufio"
	"os"
	"path/filepath"
	"testing"

	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
)

// The fixtures in testdata/ are real x265 output shapes, one per build variant.
// The acceptance criterion for this package is that every progress line in them
// is recognised and that the final frame count matches the summary line.

func TestParseOfficialBuildProgress(t *testing.T) {
	t.Parallel()
	p := New(Options{TotalFrames: 324})

	var (
		lastPercent float64
		updates     int
		done        int64
	)
	forEachLine(t, "official_4.1.log", func(line string) {
		prog, ok, err := p.parse(line)
		if err != nil {
			t.Fatalf("parse(%q) error = %v", line, err)
		}
		if !ok {
			return
		}
		updates++
		if prog.Percent >= 0 {
			lastPercent = prog.Percent
		}
		if prog.FramesDone > 0 {
			done = prog.FramesDone
		}
	})

	if updates == 0 {
		t.Fatal("no progress lines recognised in the official fixture")
	}
	if done != 324 {
		t.Errorf("final frame count = %d, want 324", done)
	}
	if lastPercent != 100 {
		t.Errorf("final percent = %v, want 100", lastPercent)
	}
}

func TestParseAsunaBuildProgress(t *testing.T) {
	t.Parallel()
	p := New(Options{TotalFrames: 324})

	var (
		lastPercent float64
		updates     int
		speed       string
	)
	forEachLine(t, "asuna_3.5.log", func(line string) {
		prog, ok, err := p.parse(line)
		if err != nil {
			t.Fatalf("parse(%q) error = %v", line, err)
		}
		if !ok {
			return
		}
		updates++
		if prog.Percent >= 0 {
			lastPercent = prog.Percent
		}
		if prog.Speed != "" {
			speed = prog.Speed
		}
	})

	if updates == 0 {
		t.Fatal("no progress lines recognised in the Asuna fixture")
	}
	if lastPercent != 100 {
		t.Errorf("final percent = %v, want 100", lastPercent)
	}
	if speed == "" {
		t.Error("no speed parsed from the Asuna fixture")
	}
}

func TestAsunaLineIsNotMisreadAsOfficial(t *testing.T) {
	t.Parallel()
	p := New(Options{TotalFrames: 324})

	// The Asuna form is "N/M frames, X fps, Y kb/s"; it must not be matched by
	// the official pattern, which expects a colon after the frame count.
	prog, ok, err := p.parse("x265 [info]: 318/324 frames, 3.91 fps, 4795.06 kb/s, Avg QP:22.19")
	if err != nil {
		t.Fatalf("parse() error = %v", err)
	}
	if !ok {
		t.Fatal("Asuna progress line was not recognised")
	}
	if prog.FramesDone != 318 {
		t.Errorf("FramesDone = %d, want 318", prog.FramesDone)
	}
	if prog.Speed != "3.91 fps" {
		t.Errorf("Speed = %q, want %q", prog.Speed, "3.91 fps")
	}
	if prog.BitRate != "4795.06 kb/s" {
		t.Errorf("BitRate = %q, want %q", prog.BitRate, "4795.06 kb/s")
	}
}

func TestParseIgnoresInformationalLines(t *testing.T) {
	t.Parallel()
	p := New(Options{TotalFrames: 324})
	info := []string{
		"x265 [info]: HEVC encoder version 4.1+1-1a9d0ba7e",
		"x265 [info]: Main 10 profile, Level-4 (Main tier)",
		"x265 [info]: frame I:      1, Avg QP:14.94  kb/s: 41390.76",
		"x265 [info]: consecutive B-frames: 10.7% 6.3% 20.4% 62.5%",
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
	}{
		{"x265 [error]: Unable to open input file <->, error 2", okerr.KindTool},
		{"unknown option --not-a-real-option", okerr.KindTool},
		{"Error: fwrite() call failed when writing frame: 120, plane 0, errno: 28", okerr.KindToolCrash},
	}
	for _, tc := range cases {
		t.Run(tc.line, func(t *testing.T) {
			t.Parallel()
			err := fatalLine(tc.line)
			if err == nil {
				t.Fatalf("fatalLine(%q) = nil, want an error", tc.line)
			}
			if got := okerr.AsError(err).Kind; got != tc.wantKind {
				t.Errorf("kind = %q, want %q", got, tc.wantKind)
			}
		})
	}
}

func TestFatalLineIgnoresNormalOutput(t *testing.T) {
	t.Parallel()
	for _, line := range []string{
		"x265 [info]: encoded 324 frames in 84.29s (3.84 fps), 4789.24 kb/s",
		"x265 [warning]: some non-fatal warning",
	} {
		if err := fatalLine(line); err != nil {
			t.Errorf("fatalLine(%q) = %v, want nil", line, err)
		}
	}
}

func TestBuildArgsAppendsAsmOnlyWhenAbsent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		params string
		asm    string
		want   []string
	}{
		{
			name:   "asm appended",
			params: "--crf 16 --preset slower",
			asm:    "avx512",
			want:   []string{"--asm", "avx512", "--crf", "16", "--preset", "slower"},
		},
		{
			name:   "asm already present",
			params: "--asm avx2 --crf 16",
			asm:    "avx512",
			want:   []string{"--asm", "avx2", "--crf", "16"},
		},
		{
			name:   "no asm configured",
			params: "--crf 16",
			want:   []string{"--crf", "16"},
		},
		{
			name:   "quoted value with space",
			params: `--csv "my file.csv" --crf 16`,
			want:   []string{"--csv", "my file.csv", "--crf", "16"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := buildArgs(Options{Params: tc.params, Asm: tc.asm})
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

func TestPercentWithUnknownTotal(t *testing.T) {
	t.Parallel()
	p := New(Options{})
	prog, ok, _ := p.parse("x265 [info]: 100/324 frames, 3.91 fps, 4795.06 kb/s, Avg QP:22.19")
	if !ok {
		t.Fatal("progress line not recognised")
	}
	if prog.Percent != -1 {
		t.Errorf("Percent = %v, want -1 when the total is unknown", prog.Percent)
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
