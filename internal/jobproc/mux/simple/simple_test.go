package simple

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc"
	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
)

// TestSingleVideoArgs covers SingleVideoMuxer.BuildCommandline, including the
// frame-range arithmetic that is easy to get wrong.
func TestSingleVideoArgs(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		opts SingleVideoOptions
		want []string
	}{
		{
			name: "partial range",
			opts: SingleVideoOptions{
				Options:    Options{Mkvmerge: `C:\tools\mkvtoolnix\mkvmerge.exe`, Output: `D:\work\ep01_part1.mkv`},
				Input:      `D:\work\ep01_part1.hevc`,
				Partial:    true,
				FrameRange: model.NewSliceInfo(1200, 3600),
			},
			want: []string{
				"--ui-language", "en", "--output", `D:\work\ep01_part1.mkv`,
				"--no-audio", "--no-subtitles", "--no-buttons", "--no-track-tags",
				"--no-chapters", "--no-attachments", "--no-global-tags",
				"--default-track", "0:1", "--language", "0:und",
				"(", `D:\work\ep01_part1.hevc`, ")",
				"--split", "parts-frames:1201-3601",
			},
		},
		{
			name: "partial range starting at zero",
			opts: SingleVideoOptions{
				Options:    Options{Output: "out.mkv"},
				Input:      "part.hevc",
				Partial:    true,
				FrameRange: model.NewSliceInfo(0, 1440),
			},
			want: []string{
				"--ui-language", "en", "--output", "out.mkv",
				"--no-audio", "--no-subtitles", "--no-buttons", "--no-track-tags",
				"--no-chapters", "--no-attachments", "--no-global-tags",
				"--default-track", "0:1", "--language", "0:und",
				"(", "part.hevc", ")",
				"--split", "parts-frames:1-1441",
			},
		},
		{
			// The profile's open-ended slice reaches the muxer only for parts
			// that are passed through untouched, but the arithmetic must still
			// mirror the C# code rather than invent a special case.
			name: "open-ended range keeps the C# arithmetic",
			opts: SingleVideoOptions{
				Options:    Options{Output: "out.mkv"},
				Input:      "old.mkv",
				Partial:    true,
				FrameRange: model.NewSliceInfo(2000, model.OpenEnded),
			},
			want: []string{
				"--ui-language", "en", "--output", "out.mkv",
				"--no-audio", "--no-subtitles", "--no-buttons", "--no-track-tags",
				"--no-chapters", "--no-attachments", "--no-global-tags",
				"--default-track", "0:1", "--language", "0:und",
				"(", "old.mkv", ")",
				"--split", "parts-frames:2001-0",
			},
		},
		{
			name: "whole input is not split",
			opts: SingleVideoOptions{
				Options:    Options{Output: "whole.mkv"},
				Input:      "encoded.hevc",
				FrameRange: model.NewSliceInfo(10, 20),
			},
			want: []string{
				"--ui-language", "en", "--output", "whole.mkv",
				"--no-audio", "--no-subtitles", "--no-buttons", "--no-track-tags",
				"--no-chapters", "--no-attachments", "--no-global-tags",
				"--default-track", "0:1", "--language", "0:und",
				"(", "encoded.hevc", ")",
			},
		},
		{
			name: "paths with spaces and non-ASCII characters stay one argument",
			opts: SingleVideoOptions{
				Options:    Options{Output: `D:\我的 作品\第01話_part1.mkv`},
				Input:      `D:\我的 作品\第01話_part1.hevc`,
				Partial:    true,
				FrameRange: model.NewSliceInfo(0, 99),
			},
			want: []string{
				"--ui-language", "en", "--output", `D:\我的 作品\第01話_part1.mkv`,
				"--no-audio", "--no-subtitles", "--no-buttons", "--no-track-tags",
				"--no-chapters", "--no-attachments", "--no-global-tags",
				"--default-track", "0:1", "--language", "0:und",
				"(", `D:\我的 作品\第01話_part1.hevc`, ")",
				"--split", "parts-frames:1-100",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertArgs(t, SingleVideoArgs(tc.opts), tc.want)
		})
	}
}

// TestAppendVideoArgs covers AppendVideoMuxer.BuildCommandline.
func TestAppendVideoArgs(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		opts AppendVideoOptions
		want []string
	}{
		{
			name: "three slices with timecodes",
			opts: AppendVideoOptions{
				Options:      Options{Output: "ep01_all.mkv"},
				Slices:       []string{`D:\w\ep01_part1.mkv`, `D:\w\ep01_part2.mkv`, `D:\w\ep01_part3.mkv`},
				TimecodeFile: `D:\w\ep01.v2.tcfile`,
			},
			want: []string{
				"--ui-language", "en", "--output", "ep01_all.mkv",
				"--timestamps", `0:D:\w\ep01.v2.tcfile`,
				"--no-audio", "--no-subtitles", "--no-buttons", "--no-track-tags",
				"--no-chapters", "--no-attachments", "--no-global-tags",
				"--default-track", "0:1", "--language", "0:und",
				"(", `D:\w\ep01_part1.mkv`, ")",
				"+", "(", `D:\w\ep01_part2.mkv`, ")",
				"+", "(", `D:\w\ep01_part3.mkv`, ")",
				"--append-to", "1:0:0:0,2:0:1:0",
			},
		},
		{
			name: "two slices without timecodes",
			opts: AppendVideoOptions{
				Options: Options{Output: "two.mkv"},
				Slices:  []string{"a.mkv", "b.mkv"},
			},
			want: []string{
				"--ui-language", "en", "--output", "two.mkv",
				"--no-audio", "--no-subtitles", "--no-buttons", "--no-track-tags",
				"--no-chapters", "--no-attachments", "--no-global-tags",
				"--default-track", "0:1", "--language", "0:und",
				"(", "a.mkv", ")",
				"+", "(", "b.mkv", ")",
				"--append-to", "1:0:0:0",
			},
		},
		{
			// A single slice needs no append mapping. The legacy code still
			// emitted the flag with an empty value; preserved verbatim.
			name: "single slice leaves --append-to empty",
			opts: AppendVideoOptions{
				Options: Options{Output: "one.mkv"},
				Slices:  []string{"only.mkv"},
			},
			want: []string{
				"--ui-language", "en", "--output", "one.mkv",
				"--no-audio", "--no-subtitles", "--no-buttons", "--no-track-tags",
				"--no-chapters", "--no-attachments", "--no-global-tags",
				"--default-track", "0:1", "--language", "0:und",
				"(", "only.mkv", ")",
				"--append-to",
			},
		},
		{
			name: "no slices still emits --append-to",
			opts: AppendVideoOptions{Options: Options{Output: "empty.mkv"}},
			want: []string{
				"--ui-language", "en", "--output", "empty.mkv",
				"--no-audio", "--no-subtitles", "--no-buttons", "--no-track-tags",
				"--no-chapters", "--no-attachments", "--no-global-tags",
				"--default-track", "0:1", "--language", "0:und",
				"--append-to",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertArgs(t, AppendVideoArgs(tc.opts), tc.want)
		})
	}
}

// TestMergeOldArgs covers MergeOldRemuxer.BuildCommandline.
func TestMergeOldArgs(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		opts MergeOldOptions
		want []string
	}{
		{
			name: "with timecodes",
			opts: MergeOldOptions{
				Options:      Options{Output: `D:\out\ep01.mkv`},
				Input:        `D:\w\ep01_all.mkv`,
				OldFile:      `D:\old\ep01.mkv`,
				TimecodeFile: `D:\w\ep01.v2.tcfile`,
			},
			want: []string{
				"--ui-language", "en", "--output", `D:\out\ep01.mkv`,
				"--no-video", "(", `D:\old\ep01.mkv`, ")",
				"--timestamps", `0:D:\w\ep01.v2.tcfile`,
				"--default-track", "0:1", "--language", "0:und",
				"(", `D:\w\ep01_all.mkv`, ")",
				"--track-order", "1:0",
			},
		},
		{
			name: "without timecodes",
			opts: MergeOldOptions{
				Options: Options{Output: "ep02.mkv"},
				Input:   "ep02_all.mkv",
				OldFile: "ep02_old.mkv",
			},
			want: []string{
				"--ui-language", "en", "--output", "ep02.mkv",
				"--no-video", "(", "ep02_old.mkv", ")",
				"--default-track", "0:1", "--language", "0:und",
				"(", "ep02_all.mkv", ")",
				"--track-order", "1:0",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertArgs(t, MergeOldArgs(tc.opts), tc.want)
		})
	}
}

// TestArgsAccessorReturnsCopy guards against callers mutating the stored argv.
func TestArgsAccessorReturnsCopy(t *testing.T) {
	t.Parallel()
	p := NewMergeOld(MergeOldOptions{Options: Options{Output: "o.mkv"}, Input: "i.mkv", OldFile: "old.mkv"})
	got := p.Args()
	got[0] = "mutated"
	if p.Args()[0] == "mutated" {
		t.Fatal("Args() exposed the internal slice")
	}
}

// TestProgressLines covers the MkvmergeMuxer.ProcessLine progress branch.
func TestProgressLines(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		line        string
		wantPercent float64
		wantReport  bool
	}{
		{"halfway", "Progress: 50%", 50, true},
		{"complete", "Progress: 100%", 100, true},
		{"fractional", "Progress: 12.5%", 12.5, true},
		{"just above one", "Progress: 2%", 2, true},
		// The legacy code ignored anything up to 1% because the first updates
		// are noise.
		{"one percent is ignored", "Progress: 1%", 0, false},
		{"zero percent is ignored", "Progress: 0%", 0, false},
		{"non-numeric is ignored", "Progress: abc%", 0, false},
		{"muxing took ends the run", "Muxing took 00:01:12", 100, true},
		{"newer wording", "Multiplexing took 00:01:12", 100, true},
		{"unrelated line", "The file 'out.mkv' has been opened for writing.", 0, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := NewSingleVideo(SingleVideoOptions{Options: Options{Output: "out.mkv"}})
			var reports []jobproc.Progress
			p.sink = jobproc.ProgressFunc(func(pr jobproc.Progress) {
				reports = append(reports, pr)
			})

			if err := p.onLine(tc.line); err != nil {
				t.Fatalf("onLine(%q) error = %v", tc.line, err)
			}
			if got := p.Progress().Percent; got != tc.wantPercent {
				t.Errorf("Percent after %q = %v, want %v", tc.line, got, tc.wantPercent)
			}
			if got := len(reports) > 0; got != tc.wantReport {
				t.Errorf("reported after %q = %v, want %v", tc.line, got, tc.wantReport)
			}
		})
	}
}

// TestErrorLine mirrors the legacy "Error: " handling, including the fixed
// Substring(7) offset used to build the message detail.
func TestErrorLine(t *testing.T) {
	t.Parallel()

	p := NewMergeOld(MergeOldOptions{
		Options: Options{Output: "out.mkv", SourceFile: "ep01.mkv"},
		Input:   "all.mkv",
		OldFile: "old.mkv",
	})
	const line = "Error: 'old.mkv' cannot be opened: No such file or directory"
	if err := p.onLine(line); err != nil {
		t.Fatalf("onLine() error = %v", err)
	}

	err := p.lineErr
	if err == nil {
		t.Fatal("no error recorded for an Error: line")
	}
	e := okerr.AsError(err)
	if !strings.Contains(e.Summary, okerr.ErrMkvmerge.Summary) {
		t.Errorf("Summary = %q, want it to contain %q", e.Summary, okerr.ErrMkvmerge.Summary)
	}
	if e.Output != line[len("Error: "):] {
		t.Errorf("Output = %q, want %q", e.Output, line[len("Error: "):])
	}
	if e.File != "ep01.mkv" {
		t.Errorf("File = %q, want %q", e.File, "ep01.mkv")
	}
	if !strings.Contains(err.Error(), okerr.ErrMkvmerge.Summary) {
		t.Errorf("Error() = %q, want it to contain the legacy summary", err.Error())
	}
}

// TestFirstErrorWins checks that a second failure does not replace the first.
func TestFirstErrorWins(t *testing.T) {
	t.Parallel()
	p := NewSingleVideo(SingleVideoOptions{Options: Options{Output: "out.mkv"}})
	_ = p.onLine("Error: first")
	_ = p.onLine("Error: second")
	if got := okerr.AsError(p.lineErr).Output; got != "first" {
		t.Errorf("Output = %q, want %q", got, "first")
	}
}

// TestLifecycleBeforeStart ensures the control methods are safe no-ops when no
// child process has been started yet.
func TestLifecycleBeforeStart(t *testing.T) {
	t.Parallel()
	p := NewAppendVideo(AppendVideoOptions{Options: Options{Output: "o.mkv"}, Slices: []string{"a.mkv", "b.mkv"}})
	if err := p.Close(); err != nil {
		t.Errorf("Close() = %v, want nil", err)
	}
	if err := p.Pause(); err != nil {
		t.Errorf("Pause() = %v, want nil", err)
	}
	if err := p.Resume(); err != nil {
		t.Errorf("Resume() = %v, want nil", err)
	}
	if err := p.SetPriority(jobproc.PriorityHigh); err != nil {
		t.Errorf("SetPriority() = %v, want nil", err)
	}
}

// TestNames also pins the interface set every muxer must satisfy.
func TestNames(t *testing.T) {
	t.Parallel()
	var (
		_ jobproc.Processor     = NewSingleVideo(SingleVideoOptions{})
		_ jobproc.Processor     = NewAppendVideo(AppendVideoOptions{})
		_ jobproc.Processor     = NewMergeOld(MergeOldOptions{})
		_ jobproc.Controllable  = NewSingleVideo(SingleVideoOptions{})
		_ jobproc.Prioritizable = NewMergeOld(MergeOldOptions{})
	)
	if got := NewSingleVideo(SingleVideoOptions{}).Name(); got != NameSingleVideo {
		t.Errorf("SingleVideo.Name() = %q, want %q", got, NameSingleVideo)
	}
	if got := NewAppendVideo(AppendVideoOptions{}).Name(); got != NameAppendVideo {
		t.Errorf("AppendVideo.Name() = %q, want %q", got, NameAppendVideo)
	}
	if got := NewMergeOld(MergeOldOptions{}).Name(); got != NameMergeOld {
		t.Errorf("MergeOld.Name() = %q, want %q", got, NameMergeOld)
	}
}

// The tests below drive a real child process: mkvmerge is replaced by this test
// binary re-executed with a sentinel environment variable. TestMain runs before
// the testing package parses its flags, so the child sees the exact argv the
// processor built -- there is no "--" separator to strip.
const (
	helperEnvVar   = "OKEGUIDX_SIMPLE_HELPER"
	helperScript   = "OKEGUIDX_SIMPLE_HELPER_SCRIPT"
	helperExitCode = "OKEGUIDX_SIMPLE_HELPER_EXIT"
)

// TestMain doubles as the fake mkvmerge.
func TestMain(m *testing.M) {
	if os.Getenv(helperEnvVar) == "1" {
		os.Exit(helperMain())
	}
	os.Exit(m.Run())
}

func helperMain() int {
	for _, line := range strings.Split(os.Getenv(helperScript), "\n") {
		if line == "" {
			continue
		}
		// A leading '!' marks a line that goes to stderr.
		if rest, ok := strings.CutPrefix(line, "!"); ok {
			fmt.Fprintln(os.Stderr, rest)
			continue
		}
		fmt.Println(line)
	}
	code, _ := strconv.Atoi(os.Getenv(helperExitCode))
	return code
}

func TestRunSucceedsAndReportsProgress(t *testing.T) {
	// Not parallel: t.Setenv modifies the process environment.
	t.Setenv(helperEnvVar, "1")
	t.Setenv(helperScript, "Progress: 50%\nProgress: 100%\nMuxing took 00:00:12")
	t.Setenv(helperExitCode, "0")

	p := NewSingleVideo(SingleVideoOptions{
		Options: Options{Mkvmerge: os.Args[0], Output: "out.mkv"},
		Input:   "part.hevc",
	})
	var percents []float64
	err := p.Run(t.Context(), jobproc.ProgressFunc(func(pr jobproc.Progress) {
		percents = append(percents, pr.Percent)
	}))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if want := []float64{50, 100, 100}; !slices.Equal(percents, want) {
		t.Errorf("reported percents = %v, want %v", percents, want)
	}
}

func TestRunFailsOnErrorLineFromStderr(t *testing.T) {
	t.Setenv(helperEnvVar, "1")
	t.Setenv(helperScript, "Progress: 10%\n!Error: cannot open file")
	t.Setenv(helperExitCode, "2")

	p := NewMergeOld(MergeOldOptions{
		Options: Options{Mkvmerge: os.Args[0], Output: "out.mkv", SourceFile: "ep01.mkv"},
		Input:   "all.mkv",
		OldFile: "old.mkv",
	})
	err := p.Run(t.Context(), jobproc.NopSink{})
	if err == nil {
		t.Fatal("Run() = nil, want a failure")
	}
	e := okerr.AsError(err)
	if e.Kind != okerr.KindTool {
		t.Errorf("Kind = %q, want %q", e.Kind, okerr.KindTool)
	}
	if e.Output != "cannot open file" {
		t.Errorf("Output = %q, want %q", e.Output, "cannot open file")
	}
	if !errors.Is(err, okerr.ErrMkvmerge) {
		t.Errorf("errors.Is(err, ErrMkvmerge) = false for %v", err)
	}
}

func TestRunExitCodeWithoutErrorLine(t *testing.T) {
	t.Setenv(helperEnvVar, "1")
	t.Setenv(helperScript, "Muxing took 00:00:12")

	cases := []struct {
		code    string
		wantErr bool
	}{
		// mkvmerge exits 1 for warnings; the legacy code only treated code 2
		// as an error, and a warning is not worth discarding the output for.
		{"1", false},
		{"2", true},
	}
	for _, tc := range cases {
		t.Run("exit "+tc.code, func(t *testing.T) {
			t.Setenv(helperExitCode, tc.code)
			p := NewSingleVideo(SingleVideoOptions{Options: Options{Mkvmerge: os.Args[0], Output: "out.mkv"}})
			err := p.Run(t.Context(), jobproc.NopSink{})
			if tc.wantErr && err == nil {
				t.Fatal("Run() = nil, want a failure")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Run() = %v, want nil", err)
			}
			if tc.wantErr {
				e := okerr.AsError(err)
				if e.ExitCode != 2 {
					t.Errorf("ExitCode = %d, want 2", e.ExitCode)
				}
			}
		})
	}
}

func TestRunMissingToolFails(t *testing.T) {
	t.Parallel()
	p := NewSingleVideo(SingleVideoOptions{Options: Options{Mkvmerge: "/definitely/not/here/mkvmerge"}})
	err := p.Run(t.Context(), jobproc.NopSink{})
	if err == nil {
		t.Fatal("Run() = nil, want a not-found error")
	}
	if got := okerr.AsError(err).Kind; got != okerr.KindNotFound {
		t.Errorf("Kind = %q, want %q", got, okerr.KindNotFound)
	}
}

func TestRunHonoursCancellation(t *testing.T) {
	t.Setenv(helperEnvVar, "1")
	t.Setenv(helperScript, "")
	t.Setenv(helperExitCode, "0")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	p := NewAppendVideo(AppendVideoOptions{Options: Options{Mkvmerge: os.Args[0], Output: "out.mkv"}, Slices: []string{"a.mkv"}})
	err := p.Run(ctx, jobproc.NopSink{})
	if err == nil {
		t.Fatal("Run() = nil, want a cancellation error")
	}
	if got := okerr.AsError(err).Kind; got != okerr.KindCanceled {
		t.Errorf("Kind = %q, want %q", got, okerr.KindCanceled)
	}
}

func assertArgs(t *testing.T, got, want []string) {
	t.Helper()
	if slices.Equal(got, want) {
		return
	}
	t.Fatalf("args mismatch\n got: %q\nwant: %q", got, want)
}
