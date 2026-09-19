package ffmpegqaac

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc"
	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/node"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
	"github.com/KiritakeKumi/OKEGuiDX/internal/toolchain"
)

func TestFFmpegArgs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		opts Options
		want []string
	}{
		{
			name: "plain path",
			opts: Options{Input: `D:\work\a.flac`},
			want: []string{"-i", `D:\work\a.flac`, "-vn", "-sn", "-dn", "-f", "wav", "-v", "warning", "-"},
		},
		{
			name: "path with spaces stays one argument",
			opts: Options{Input: `D:\work dir\my track.eac3`},
			want: []string{"-i", `D:\work dir\my track.eac3`, "-vn", "-sn", "-dn", "-f", "wav", "-v", "warning", "-"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertArgs(t, ffmpegArgs(tc.opts), tc.want)
		})
	}
}

func TestQAACArgs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		opts Options
		want []string
	}{
		{
			name: "explicit bitrate",
			opts: Options{Output: `D:\work\a.m4a`, Info: model.AudioInfo{Bitrate: 256}},
			want: []string{"-i", "-v", "256", "-q", "2", "--no-delay", "--threading", "-o", `D:\work\a.m4a`, "-"},
		},
		{
			name: "default bitrate fills in",
			opts: Options{Output: `D:\work\b.m4a`, Info: model.AudioInfo{}},
			want: []string{"-i", "-v", "192", "-q", "2", "--no-delay", "--threading", "-o", `D:\work\b.m4a`, "-"},
		},
		{
			name: "output with spaces stays one argument",
			opts: Options{Output: `D:\work dir\my track.m4a`, Info: model.AudioInfo{Bitrate: 96}},
			want: []string{"-i", "-v", "96", "-q", "2", "--no-delay", "--threading", "-o", `D:\work dir\my track.m4a`, "-"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// New applies the legacy default (Constants.QAACBitrate) before the
			// arguments are built, so go through it as Run does.
			assertArgs(t, qaacArgs(New(tc.opts).opts), tc.want)
		})
	}
}

func TestParseLine(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		line        string
		lengthMS    int
		wantMatched bool
		wantPercent float64
	}{
		{
			name:        "hh:mm:ss.mss without total",
			line:        "\r1:02:03.456 (5.0x)   ",
			lengthMS:    8 * 3600 * 1000,
			wantMatched: true,
			wantPercent: (1*3600 + 2*60 + 3) * 1000 * 100.0 / (8 * 3600 * 1000),
		},
		{
			name:        "mm:ss.mss without total",
			line:        "\r5:00.000 (10.0x)   ",
			lengthMS:    10 * 60 * 1000,
			wantMatched: true,
			wantPercent: 50,
		},
		{
			// The legacy pattern has no anchor, so on a line that carries
			// "elapsed/total" it matches the *last* timestamp (the total) and
			// reports 100%. Real piped output does not include the total, but
			// the behaviour is inherited rather than silently corrected.
			name:        "elapsed slash total picks the total",
			line:        "\r[ 50.0%] 5:00.000/10:00.000 (10.0x), ETA 0:30.000  ",
			lengthMS:    10 * 60 * 1000,
			wantMatched: true,
			wantPercent: 100,
		},
		{
			name:        "below one percent is matched but suppressed",
			line:        "\r0:00.500 (10.0x)   ",
			lengthMS:    10 * 60 * 1000,
			wantMatched: true,
			wantPercent: 0,
		},
		{
			name:        "unknown length yields no percentage",
			line:        "\r5:00.000 (10.0x)   ",
			lengthMS:    0,
			wantMatched: true,
			wantPercent: 0,
		},
		{
			name:        "plain banner line does not match",
			line:        "qaac 2.82, CoreAudioToolbox 7.10.9.0",
			lengthMS:    10 * 60 * 1000,
			wantMatched: false,
		},
		{
			name:        "bitrate summary does not match",
			line:        "Overall bitrate: 223.677kbps",
			lengthMS:    10 * 60 * 1000,
			wantMatched: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := New(Options{Info: model.AudioInfo{Length: tc.lengthMS}})
			prog, matched := p.parseLine(tc.line)
			if matched != tc.wantMatched {
				t.Fatalf("parseLine(%q) matched = %v, want %v", tc.line, matched, tc.wantMatched)
			}
			if !matched {
				return
			}
			if diff := prog.Percent - tc.wantPercent; diff > 0.0001 || diff < -0.0001 {
				t.Errorf("parseLine(%q) percent = %v, want %v", tc.line, prog.Percent, tc.wantPercent)
			}
		})
	}
}

func TestHandleLineMarkers(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		line        string
		wantErr     bool
		wantUpdates int
		wantPercent float64
	}{
		{name: "done marker", line: "Optimizing...done", wantUpdates: 1, wantPercent: 100},
		{name: "plain line", line: "423/423 chunks written (optimizing)"},
		{name: "error marker", line: "ERROR: Cannot seek back the input", wantErr: true},
		// The legacy if/else-if/else only looked for the markers on lines that
		// were not progress, so a progress line never raises the QAAC error.
		{name: "progress line wins over markers", line: "\r0:05.000 (10.0x) ERROR", wantUpdates: 1, wantPercent: 50},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var updates []jobproc.Progress
			p := New(Options{Info: model.AudioInfo{Length: 10_000}})
			p.sink = jobproc.ProgressFunc(func(prog jobproc.Progress) {
				updates = append(updates, prog)
			})

			err := p.handleLine(tc.line)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("handleLine(%q) = nil, want an error", tc.line)
				}
				if !errors.Is(err, okerr.ErrQAAC) {
					t.Errorf("handleLine(%q) error = %v, want the QAAC summary", tc.line, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("handleLine(%q) error = %v", tc.line, err)
			}
			if len(updates) != tc.wantUpdates {
				t.Fatalf("handleLine(%q) updates = %+v, want %d update(s)", tc.line, updates, tc.wantUpdates)
			}
			if tc.wantUpdates > 0 && updates[0].Percent != tc.wantPercent {
				t.Errorf("handleLine(%q) percent = %v, want %v", tc.line, updates[0].Percent, tc.wantPercent)
			}
		})
	}
}

// TestHandleChunkSplitsCarriageReturns covers qaac's progress rewrites: it
// redraws the same line with a bare \r, which the legacy .NET reader treated as
// a line break but bufio.Scanner does not.
func TestHandleChunkSplitsCarriageReturns(t *testing.T) {
	t.Parallel()
	var updates []jobproc.Progress
	p := New(Options{Info: model.AudioInfo{Length: 10_000}})
	p.sink = jobproc.ProgressFunc(func(prog jobproc.Progress) {
		updates = append(updates, prog)
	})

	chunk := "qaac 2.82, CoreAudioToolbox 7.10.9.0\r0:05.000 (10.0x)\r0:07.000 (3.0x)"
	if err := p.handleChunk(chunk); err != nil {
		t.Fatalf("handleChunk() error = %v", err)
	}
	if len(updates) != 2 {
		t.Fatalf("handleChunk() reported %d updates, want 2 (%+v)", len(updates), updates)
	}
	if updates[0].Percent != 50 || updates[1].Percent != 70 {
		t.Errorf("percents = %v, %v, want 50, 70", updates[0].Percent, updates[1].Percent)
	}
}

func TestRunRefusesWithoutAACCapability(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		caps node.Capabilities
	}{
		{
			name: "bare node",
			caps: node.NewCapabilities(node.RoleStandalone),
		},
		{
			name: "tools present but no capability",
			caps: func() node.Capabilities {
				c := node.NewCapabilities(node.RoleStandalone)
				c.Tools[toolchain.ToolQAAC] = node.ToolInfo{Path: "qaac64.exe"}
				c.Tools[toolchain.ToolFFmpeg] = node.ToolInfo{Path: "ffmpeg.exe"}
				return c
			}(),
		},
		{
			name: "only unrelated features",
			caps: func() node.Capabilities {
				c := node.NewCapabilities(node.RoleStandalone)
				c.AddFeature(node.FeatureEac3to)
				return c
			}(),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := New(Options{Caps: tc.caps, FFmpeg: "ffmpeg", QAAC: "qaac"})
			err := p.Run(t.Context(), nil)
			if err == nil {
				t.Fatal("Run() = nil, want a refusal")
			}
			if !errors.Is(err, okerr.ErrUnsupportedAAC) {
				t.Fatalf("Run() error = %v, want okerr.ErrUnsupportedAAC", err)
			}
			if e := okerr.AsError(err); e.Kind != okerr.KindUnsupported {
				t.Errorf("kind = %q, want %q", e.Kind, okerr.KindUnsupported)
			}
		})
	}
}

// TestRunRefusesWithoutQAACTool documents the platform rule: the decision comes
// from the discovered capabilities, so a node that cannot find qaac is refused
// wherever it runs, and off Windows the feature is never advertised at all.
func TestRunRefusesWithoutQAACTool(t *testing.T) {
	t.Parallel()
	caps, err := toolchain.Discover(toolchain.Options{Root: t.TempDir(), SkipProbe: true})
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if caps.HasFeature(node.FeatureAAC) {
		t.Fatal("an empty tools tree must not advertise the AAC feature")
	}
	p := New(Options{Caps: caps, FFmpeg: "ffmpeg", QAAC: "qaac"})
	if err := p.Run(t.Context(), nil); !errors.Is(err, okerr.ErrUnsupportedAAC) {
		t.Fatalf("Run() error = %v, want okerr.ErrUnsupportedAAC", err)
	}
}

// TestPipelineSmoke runs a real two-process pipeline through Processor.run.
// Both stages are played by the test binary itself, following the helper pattern
// of internal/proc: the "ffmpeg" stage writes a payload to stdout and a progress
// line to stderr, the "qaac" stage copies its stdin into the -o file and prints
// the finish marker. This is what proves the in-process pipe really replaces
// `cmd.exe /c "... | ..."`.
func TestPipelineSmoke(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	out := filepath.Join(dir, "out.m4a")

	var (
		mu      sync.Mutex
		updates []jobproc.Progress
	)
	sink := jobproc.ProgressFunc(func(prog jobproc.Progress) {
		mu.Lock()
		defer mu.Unlock()
		updates = append(updates, prog)
	})

	caps := node.NewCapabilities(node.RoleStandalone)
	caps.AddFeature(node.FeatureAAC)
	p := New(Options{
		Caps:   caps,
		FFmpeg: helperPath(t),
		QAAC:   helperPath(t),
		Input:  filepath.Join(dir, "in.eac3"),
		Output: out,
		Info:   model.AudioInfo{Bitrate: 192, Length: 10_000},
	})

	// The specs carry the real argument lists; only the executable is swapped
	// for the test binary, so the builders under test are exercised too.
	ffmpegSpec := helperSpec(t, "ffmpeg", ffmpegArgs(p.opts)...)
	qaacSpec := helperSpec(t, "qaac", qaacArgs(p.opts)...)

	if err := p.run(t.Context(), sink, ffmpegSpec, qaacSpec); err != nil {
		t.Fatalf("run() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(updates) == 0 {
		t.Fatal("no progress updates were reported")
	}
	if last := updates[len(updates)-1]; last.Percent != 100 {
		t.Errorf("final percent = %v, want 100 (updates: %+v)", last.Percent, updates)
	}
	sawProgress := false
	for _, u := range updates {
		if u.Percent > 1 && u.Percent < 100 {
			sawProgress = true
		}
	}
	if !sawProgress {
		t.Errorf("no intermediate progress was reported (updates: %+v)", updates)
	}

	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if string(got) != helperPayload {
		t.Errorf("qaac received %q, want %q", got, helperPayload)
	}
}

// TestFixtureProgressAndMarkers runs the parser over real qaac transcripts, the
// same fixture-driven acceptance the x265 package uses. The fixtures are
// published qaac console output (see the file headers): one full run with the
// finish marker, one piped run that ends in qaac's seek error.
func TestFixtureProgressAndMarkers(t *testing.T) {
	t.Parallel()
	cases := []struct {
		fixture     string
		lengthMS    int
		wantUpdates int
		wantDone    bool
		wantErr     bool
	}{
		{
			fixture:  "qaac_2.79_he_7.1.log",
			lengthMS: 49_055,
			// One update from the [100.0%] progress line (the pattern captures
			// whole seconds, hence 99.887%) and one from the finish marker.
			wantUpdates: 2,
			wantDone:    true,
		},
		{
			fixture:     "qaac_pipe_elapsed.log",
			lengthMS:    290_000,
			wantUpdates: 2,
			wantErr:     true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			t.Parallel()
			var (
				updates int
				done    bool
				gotErr  error
			)
			p := New(Options{Info: model.AudioInfo{Length: tc.lengthMS}})
			p.sink = jobproc.ProgressFunc(func(prog jobproc.Progress) {
				updates++
				if prog.Percent == 100 {
					done = true
				}
			})
			forEachLine(t, tc.fixture, func(line string) {
				if gotErr != nil {
					return
				}
				if err := p.handleChunk(line); err != nil {
					gotErr = err
				}
			})

			if updates != tc.wantUpdates {
				t.Errorf("progress updates = %d, want %d", updates, tc.wantUpdates)
			}
			if done != tc.wantDone {
				t.Errorf("finish marker reported = %v, want %v", done, tc.wantDone)
			}
			if (gotErr != nil) != tc.wantErr {
				t.Errorf("error = %v, want an error: %v", gotErr, tc.wantErr)
			}
			if gotErr != nil && !errors.Is(gotErr, okerr.ErrQAAC) {
				t.Errorf("error = %v, want the QAAC summary", gotErr)
			}
		})
	}
}

// assertArgs compares two argument slices element by element so a failure shows
// which position differs.
func assertArgs(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("args = %q, want %q", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("args[%d] = %q, want %q (full: %q)", i, got[i], want[i], got)
		}
	}
}

// forEachLine feeds every line of a fixture to fn.
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
