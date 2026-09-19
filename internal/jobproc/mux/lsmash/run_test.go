package lsmash

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc"
	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
)

// The processor drives a real external tool through internal/proc, so the tests
// below run one. Rather than shipping a fake l-smash binary, the test binary
// re-executes itself as the child, the same pattern internal/proc uses for its
// own tests.
//
// The child is started with "-test.run=TestHelperProcess -- <muxer args>", so
// the test flags are valid for the Go test binary and everything after "--" is
// the command line under test.

const (
	helperEnvVar = "OKEGUIDX_LSMASH_HELPER"
	helperMode   = "OKEGUIDX_LSMASH_MODE"
	helperArgs   = "OKEGUIDX_LSMASH_ARGS"
)

// TestHelperProcess is not a real test. It acts as the fake l-smash muxer.
func TestHelperProcess(t *testing.T) {
	if os.Getenv(helperEnvVar) != "1" {
		return
	}
	args := argsAfterDashDash(os.Args)
	// Record the argument slice the processor passed, so the test can assert on
	// the exact command line the child received.
	if argsFile := os.Getenv(helperArgs); argsFile != "" {
		if err := os.WriteFile(argsFile, []byte(strings.Join(args, "\n")), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, "Error: cannot record args")
			os.Exit(3)
		}
	}
	switch os.Getenv(helperMode) {
	case "success":
		fmt.Fprint(os.Stderr, "MP4 muxing mode\n")
		fmt.Fprint(os.Stderr, "Track 1: H.265 High Efficiency Video Coding\n")
		fmt.Fprint(os.Stderr, "Importing: 4194304 bytes\r")
		fmt.Fprint(os.Stderr, "Importing: 20971520 bytes\r")
		fmt.Fprint(os.Stderr, "Muxing completed!\n")
		os.Exit(0)
	case "error":
		fmt.Fprint(os.Stderr, "Error: failed to open input file.\n")
		os.Exit(1)
	default:
		os.Exit(2)
	}
}

func argsAfterDashDash(args []string) []string {
	for i, a := range args {
		if a == "--" {
			return args[i+1:]
		}
	}
	return nil
}

// newHelperProcessor builds a processor whose Muxer is the test binary running
// TestHelperProcess. The muxer arguments themselves are the ones the real
// builder produced, wrapped in the -test.run/-- preamble.
func newHelperProcessor(t *testing.T, mode string, extraEnv ...string) *Processor {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	p := NewEpisode(exe, testOutput, &model.MediaFile{
		Video:       videoTrack(testVideo, 24000, 1001),
		AudioTracks: []*model.AudioTrack{audioTrack(testAudioJpn, "jpn", "")},
		Chapter:     chapterTrack(testChapter),
	}, 400<<20, rootsLocal)
	p.args = append([]string{"-test.run=TestHelperProcess", "--"}, p.args...)
	t.Setenv(helperEnvVar, "1")
	t.Setenv(helperMode, mode)
	for _, kv := range extraEnv {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	return p
}

func TestRunSuccessDrivesTheProcess(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	p := newHelperProcessor(t, "success", helperArgs+"="+argsFile)
	want := append([]string(nil), p.args[2:]...)

	sink := &recordingSink{}
	if err := p.Run(context.Background(), sink); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if err := p.Close(); err != nil {
		t.Errorf("Close() error = %v", err)
	}

	// The fake muxer records what it received; it must be the processor's own
	// arguments.
	recorded, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read recorded args: %v", err)
	}
	got := strings.Split(string(recorded), "\n")
	assertArgs(t, got, want)

	if len(sink.reports) == 0 {
		t.Fatal("no progress reported")
	}
	if last := sink.reports[len(sink.reports)-1]; last.Percent != 100 || last.Status != "封装完成" {
		t.Errorf("final report = %+v, want 100%% and 封装完成", last)
	}
}

func TestRunErrorLineBecomesStructuredError(t *testing.T) {
	p := newHelperProcessor(t, "error")
	err := p.Run(context.Background(), &recordingSink{})
	if err == nil {
		t.Fatal("Run() = nil, want the child's failure")
	}
	e := okerr.AsError(err)
	if e.Summary != okerr.ErrLSmash.Summary {
		t.Errorf("Summary = %q, want %q", e.Summary, okerr.ErrLSmash.Summary)
	}
	if !strings.Contains(e.Detail, "failed to open input file") {
		t.Errorf("Detail = %q, want the l-smash message", e.Detail)
	}
	if e.File != testOutput {
		t.Errorf("File = %q, want %q", e.File, testOutput)
	}
}

func TestRunNonZeroExitWithoutErrorLineIsReported(t *testing.T) {
	p := newHelperProcessor(t, "silent-failure")
	err := p.Run(context.Background(), &recordingSink{})
	if err == nil {
		t.Fatal("Run() = nil, want a failure for a non-zero exit code")
	}
	e := okerr.AsError(err)
	if e.Summary != okerr.ErrLSmash.Summary {
		t.Errorf("Summary = %q, want %q", e.Summary, okerr.ErrLSmash.Summary)
	}
	if e.ExitCode == 0 {
		t.Error("ExitCode = 0, want the child's non-zero code")
	}
}

func TestRunMissingMuxerIsReported(t *testing.T) {
	p := New(Options{Muxer: filepath.Join(t.TempDir(), "not-here"), Output: testOutput})
	err := p.Run(context.Background(), &recordingSink{})
	if err == nil {
		t.Fatal("Run() = nil, want a not-found error")
	}
	if got := okerr.AsError(err).Kind; got != okerr.KindNotFound {
		t.Errorf("Kind = %q, want %q", got, okerr.KindNotFound)
	}
}

func TestRunCanceledContextIsReported(t *testing.T) {
	p := newHelperProcessor(t, "success")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := p.Run(ctx, &recordingSink{})
	if err == nil {
		t.Fatal("Run() = nil, want a cancellation error")
	}
	if got := okerr.AsError(err).Kind; got != okerr.KindCanceled {
		t.Errorf("Kind = %q, want %q", got, okerr.KindCanceled)
	}
}

func TestControlsAreSafeWithoutARun(t *testing.T) {
	// Pause/Resume/SetPriority/Close must not panic before Run started a child.
	p := New(Options{Muxer: "muxer", Output: testOutput})
	if err := p.Pause(); err != nil {
		t.Errorf("Pause() error = %v", err)
	}
	if err := p.Resume(); err != nil {
		t.Errorf("Resume() error = %v", err)
	}
	if err := p.SetPriority(jobproc.PriorityHigh); err != nil {
		t.Errorf("SetPriority() error = %v", err)
	}
	if err := p.Close(); err != nil {
		t.Errorf("Close() error = %v", err)
	}
}

// Compile-time checks that the wrapper honours the frozen jobproc contracts.
var (
	_ jobproc.Processor     = (*Processor)(nil)
	_ jobproc.Controllable  = (*Processor)(nil)
	_ jobproc.Prioritizable = (*Processor)(nil)
)
