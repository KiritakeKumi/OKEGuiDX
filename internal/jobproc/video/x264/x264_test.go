package x264

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
)

// The acceptance criterion is that every progress line in the fixtures is
// recognised, the completion line wins over the progress pattern, and the
// argument shape matches what the original command line produced.

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

func TestParseEightBitProgress(t *testing.T) {
	t.Parallel()
	p := New(Options{TotalFrames: 34567})

	var (
		updates int
		done    int64
		lastPct float64
	)
	forEachLine(t, "progress-8bit.log", func(line string) {
		prog, ok, err := p.parse(line)
		if err != nil {
			t.Fatalf("parse(%q) error = %v", line, err)
		}
		if !ok {
			return
		}
		updates++
		if prog.FramesDone > 0 {
			done = prog.FramesDone
		}
		if prog.Percent >= 0 {
			lastPct = prog.Percent
		}
	})

	if updates == 0 {
		t.Fatal("no progress lines recognised")
	}
	if done != 34567 {
		t.Errorf("final frame count = %d, want 34567", done)
	}
	if lastPct != 100 {
		t.Errorf("final percent = %v, want 100", lastPct)
	}
}

func TestParseTenBitProgress(t *testing.T) {
	t.Parallel()
	p := New(Options{TotalFrames: 13000})

	var done int64
	forEachLine(t, "progress-10bit.log", func(line string) {
		prog, ok, _ := p.parse(line)
		if ok && prog.FramesDone > 0 {
			done = prog.FramesDone
		}
	})
	if done != 13000 {
		t.Errorf("final frame count = %d, want 13000", done)
	}
}

func TestCompletionLineIsNotTreatedAsProgress(t *testing.T) {
	t.Parallel()
	p := New(Options{TotalFrames: 34567})

	// The completion line also contains "N frames", so a naive ordering would
	// report it as progress and lose the terminal status.
	prog, ok, err := p.parse("x264 [info]: encoded 34567 frames, 12.45 fps, 2347.89 kb/s")
	if err != nil {
		t.Fatalf("parse() error = %v", err)
	}
	if !ok {
		t.Fatal("completion line was not recognised")
	}
	if prog.Status != "压制完成" {
		t.Errorf("Status = %q, want %q", prog.Status, "压制完成")
	}
	if prog.Percent != 100 {
		t.Errorf("Percent = %v, want 100", prog.Percent)
	}
	if prog.Speed != "12.45 fps" {
		t.Errorf("Speed = %q, want %q", prog.Speed, "12.45 fps")
	}
	if prog.BitRate != "2347.89 kb/s" {
		t.Errorf("BitRate = %q, want %q", prog.BitRate, "2347.89 kb/s")
	}
}

func TestProgressLineStatusIsRunning(t *testing.T) {
	t.Parallel()
	p := New(Options{TotalFrames: 34567})
	prog, ok, err := p.parse("x264 [info]: 34000 frames: 12.34 fps, 2345.67 kb/s")
	if err != nil {
		t.Fatalf("parse() error = %v", err)
	}
	if !ok {
		t.Fatal("progress line was not recognised")
	}
	if prog.Status != "压制中" {
		t.Errorf("Status = %q, want %q", prog.Status, "压制中")
	}
	if prog.FramesDone != 34000 {
		t.Errorf("FramesDone = %d, want 34000", prog.FramesDone)
	}
}

func TestInformationalLinesAreIgnored(t *testing.T) {
	t.Parallel()
	p := New(Options{TotalFrames: 34567})
	for _, line := range []string{
		"x264 [info]: using cpu capabilities: MMX2 SSE2Fast",
		"x264 [info]: profile High, level 4.1, 4:2:0, 8-bit",
		"x264 [info]: frame I:     28    Avg QP:15.42  size: 92104",
		"x264 [info]: kb/s:2345.67",
		"x264 [info]: consecutive B-frames:  2.1%  3.4% 12.5% 82.0%",
		"",
	} {
		if _, ok, _ := p.parse(line); ok {
			t.Errorf("parse(%q) reported progress, want none", line)
		}
	}
}

func TestPercentWithUnknownTotal(t *testing.T) {
	t.Parallel()
	p := New(Options{})
	prog, ok, _ := p.parse("x264 [info]: 100 frames: 12.34 fps, 2345.67 kb/s")
	if !ok {
		t.Fatal("progress line not recognised")
	}
	if prog.Percent != -1 {
		t.Errorf("Percent = %v, want -1 when the total is unknown", prog.Percent)
	}
}

func TestFatalLinesAreDetected(t *testing.T) {
	t.Parallel()
	cases := []struct {
		line     string
		wantKind okerr.Kind
		wantSum  string
	}{
		{"x264 [error]: malloc of size 8388608 failed", okerr.KindTool, okerr.ErrX264.Summary},
		{"unknown option --not-a-real-option", okerr.KindTool, okerr.ErrX264.Summary},
		{"Error: fwrite() call failed when writing frame: 120, plane 0, errno: 28", okerr.KindToolCrash, okerr.ErrX264Crash.Summary},
	}
	for _, tc := range cases {
		t.Run(tc.line, func(t *testing.T) {
			t.Parallel()
			err := FatalLine(tc.line)
			if err == nil {
				t.Fatalf("FatalLine(%q) = nil, want an error", tc.line)
			}
			e := okerr.AsError(err)
			if e.Kind != tc.wantKind {
				t.Errorf("kind = %q, want %q", e.Kind, tc.wantKind)
			}
			if e.Summary != tc.wantSum {
				t.Errorf("summary = %q, want %q", e.Summary, tc.wantSum)
			}
		})
	}
}

func TestFatalLineIgnoresNormalOutput(t *testing.T) {
	t.Parallel()
	for _, line := range []string{
		"x264 [info]: encoded 34567 frames, 12.45 fps, 2347.89 kb/s",
		"x264 [warning]: some non-fatal warning",
		"x264 [info]: profile High, level 4.1",
	} {
		if err := FatalLine(line); err != nil {
			t.Errorf("FatalLine(%q) = %v, want nil", line, err)
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
			params: "--preset veryslow --crf 19.0",
			asm:    "avx512",
			want:   []string{"--asm", "avx512", "--preset", "veryslow", "--crf", "19.0"},
		},
		{
			name:   "asm already present",
			params: "--asm avx2 --crf 19.0",
			asm:    "avx512",
			want:   []string{"--asm", "avx2", "--crf", "19.0"},
		},
		{
			name:   "no asm configured",
			params: "--crf 19.0",
			want:   []string{"--crf", "19.0"},
		},
		{
			name:   "quoted value with space",
			params: `--zones "0,100,q=20" --crf 19.0`,
			want:   []string{"--zones", "0,100,q=20", "--crf", "19.0"},
		},
		{
			name:   "real profile parameters",
			params: "--preset veryslow --tune animation --crf 19.0 --deblock 0:0 --keyint 360 --psy-rd 0.00:0.20",
			want: []string{
				"--preset", "veryslow", "--tune", "animation", "--crf", "19.0",
				"--deblock", "0:0", "--keyint", "360", "--psy-rd", "0.00:0.20",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := BuildArgs(Options{Params: tc.params, Asm: tc.asm})
			if len(got) != len(tc.want) {
				t.Fatalf("BuildArgs() = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("BuildArgs()[%d] = %q, want %q (full: %v)", i, got[i], tc.want[i], got)
				}
			}
		})
	}
}

func TestInputIsDemuxerY4MFromStdin(t *testing.T) {
	t.Parallel()
	// x264 takes three tokens for its input, unlike x265's single "--y4m -".
	// The base class appends them verbatim, so the shape is asserted here to
	// catch a regression that would make every encode fail to start.
	p := New(Options{
		VSPipe: "/tools/vspipe", Script: "x.vpy", Encoder: "/tools/x264",
		Params: "--crf 19", Output: "/out/x.h264",
	})
	spec := p.Spec()
	want := []string{"--demuxer", "y4m", "-"}
	if len(spec.InputArgs) != len(want) {
		t.Fatalf("InputArgs = %v, want %v", spec.InputArgs, want)
	}
	for i := range want {
		if spec.InputArgs[i] != want[i] {
			t.Fatalf("InputArgs[%d] = %q, want %q", i, spec.InputArgs[i], want[i])
		}
	}
}

func TestSplitParamsHandlesUnbalancedQuotes(t *testing.T) {
	t.Parallel()
	// A malformed profile must not lose the rest of the parameters; the
	// original passed the string through cmd.exe, which behaved the same way.
	got := splitParams(`--crf 19 --zones "unclosed`)
	if len(got) != 4 {
		t.Fatalf("splitParams() = %v, want 4 tokens", got)
	}
	if !strings.HasPrefix(got[3], "unclosed") {
		t.Errorf("last token = %q, want it to start with the unclosed value", got[3])
	}
}

func TestName(t *testing.T) {
	t.Parallel()
	if got := New(Options{}).Name(); got != Name {
		t.Errorf("Name() = %q, want %q", got, Name)
	}
}
