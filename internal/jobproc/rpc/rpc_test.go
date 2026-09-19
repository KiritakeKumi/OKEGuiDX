package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc"
	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
)

// Threshold table: the three-way branch RpChecker.ProcessLine implemented with
// `psnr < 30.0 || psnrU < 40.0 || psnrV < 40.0`.
func TestThresholdDecision(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		line    string
		want    model.RPCStatus
		samples int
	}{
		{
			name:    "Y-only well above the threshold",
			line:    "RPCOUT: 0 40.123457",
			want:    model.RPCWaiting,
			samples: 1,
		},
		{
			// The Y-only placeholder is the chroma threshold itself, so the
			// comparison `u < 40` is false and the sample passes.
			name:    "Y-only exactly at the Y threshold passes",
			line:    "RPCOUT: 0 30.000000",
			want:    model.RPCWaiting,
			samples: 1,
		},
		{
			name:    "Y below the threshold fails",
			line:    "RPCOUT: 0 29.999999",
			want:    model.RPCFailed,
			samples: 1,
		},
		{
			name:    "YUV passes",
			line:    "RPCOUT: 0 40.000000 40.000000 40.000000",
			want:    model.RPCWaiting,
			samples: 1,
		},
		{
			name:    "Y below, chroma fine",
			line:    "RPCOUT: 0 29.999999 45.000000 45.000000",
			want:    model.RPCFailed,
			samples: 1,
		},
		{
			name:    "U below, Y and V fine",
			line:    "RPCOUT: 0 40.000000 39.999999 45.000000",
			want:    model.RPCFailed,
			samples: 1,
		},
		{
			name:    "V below, Y and U fine",
			line:    "RPCOUT: 0 40.000000 45.000000 39.999999",
			want:    model.RPCFailed,
			samples: 1,
		},
		{
			name:    "a failure is sticky across later good frames",
			line:    "RPCOUT: 0 20.000000 20.000000 20.000000",
			want:    model.RPCFailed,
			samples: 1,
		},
		{
			name:    "malformed line is ignored",
			line:    "RPCOUT: only-a-number",
			want:    model.RPCWaiting,
			samples: 0,
		},
		{
			name:    "unrelated line is ignored",
			line:    "vspipe: some diagnostic",
			want:    model.RPCWaiting,
			samples: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := New(Options{TotalFrames: 100})
			handler := p.lineHandler()
			if err := handler(tc.line); err != nil {
				t.Fatalf("lineHandler(%q) error = %v", tc.line, err)
			}
			if got := p.Status(); got != tc.want {
				t.Errorf("Status() = %v, want %v", got, tc.want)
			}
			if got := len(p.result.Samples); got != tc.samples {
				t.Errorf("recorded %d samples, want %d", got, tc.samples)
			}
		})
	}
}

// TestStatusTransitions pins the legacy Chinese status flow:
// 等待中 -> 通过 on "Output ", 等待中 -> 未通过 on a low sample, and
// 等待中 -> 错误 on a VapourSynth traceback.
func TestStatusTransitions(t *testing.T) {
	t.Parallel()

	t.Run("waiting to passed on Output", func(t *testing.T) {
		t.Parallel()
		p := New(Options{})
		if got := p.Status(); got != model.RPCWaiting {
			t.Fatalf("initial Status() = %v, want 等待中", got)
		}
		if err := p.lineHandler()("Output 3 frames in 0.01 seconds (243.58 fps)"); err != nil {
			t.Fatalf("lineHandler() error = %v", err)
		}
		if got := p.Status(); got != model.RPCPassed {
			t.Errorf("Status() = %v, want 通过", got)
		}
	})

	t.Run("failed is not overwritten by Output", func(t *testing.T) {
		t.Parallel()
		p := New(Options{})
		handler := p.lineHandler()
		_ = handler("RPCOUT: 0 10.000000")
		_ = handler("Output 3 frames in 0.01 seconds (243.58 fps)")
		if got := p.Status(); got != model.RPCFailed {
			t.Errorf("Status() = %v, want 未通过", got)
		}
	})

	t.Run("waiting to error on a traceback", func(t *testing.T) {
		t.Parallel()
		p := New(Options{SourceFile: "ep01.mkv"})
		handler := p.lineHandler()
		_ = handler("Script evaluation failed:")
		_ = handler("Python exception: No module named 'x'")
		_ = handler("")
		_ = handler("Traceback (most recent call last):")
		_ = handler("ModuleNotFoundError: No module named 'x'")

		if got := p.Status(); got != model.RPCError {
			t.Errorf("Status() = %v, want 错误", got)
		}
		err := p.takeFrameError()
		if err == nil {
			t.Fatal("no error recorded for a traceback")
		}
		e := okerr.AsError(err)
		if e.Summary != okerr.ErrRPC.Summary {
			t.Errorf("Summary = %q, want %q", e.Summary, okerr.ErrRPC.Summary)
		}
		if !strings.Contains(e.Output, "ModuleNotFoundError") {
			t.Errorf("Output = %q, want it to contain the traceback terminator", e.Output)
		}
		if e.File != "ep01.mkv" {
			t.Errorf("File = %q, want %q", e.File, "ep01.mkv")
		}
	})
}

// TestLineHandlerReadsRealVSPipeOutput drives the parser over the stderr of a
// real vspipe run captured from VapourSynth R42.
func TestLineHandlerReadsRealVSPipeOutput(t *testing.T) {
	t.Parallel()

	p := New(Options{TotalFrames: 3})
	var reports []jobproc.Progress
	p.sink = jobproc.ProgressFunc(func(pr jobproc.Progress) { reports = append(reports, pr) })

	handler := p.lineHandler()
	forEachLine(t, "vspipe_yuv_stderr.txt", func(line string) {
		if err := handler(line); err != nil {
			t.Fatalf("lineHandler(%q) error = %v", line, err)
		}
	})

	if got := len(p.result.Samples); got != 3 {
		t.Fatalf("recorded %d samples, want 3", got)
	}
	if !p.result.YUV {
		t.Error("YUV = false, want true for a four-field run")
	}
	if got := p.Status(); got != model.RPCPassed {
		t.Errorf("Status() = %v, want 通过", got)
	}
	if len(reports) != 3 {
		t.Errorf("reported %d progress updates, want 3", len(reports))
	}
	if got := p.Progress().Percent; got != 100 {
		t.Errorf("Percent = %v, want 100", got)
	}
	if got := p.Progress().FramesDone; got != 3 {
		t.Errorf("FramesDone = %d, want 3", got)
	}
}

// TestLineHandlerReadsRealTraceback checks the error path against a real
// vspipe failure.
func TestLineHandlerReadsRealTraceback(t *testing.T) {
	t.Parallel()

	p := New(Options{})
	handler := p.lineHandler()
	forEachLine(t, "vspipe_vpy_error_stderr.txt", func(line string) {
		if err := handler(line); err != nil {
			t.Fatalf("lineHandler(%q) error = %v", line, err)
		}
	})

	if got := p.Status(); got != model.RPCError {
		t.Errorf("Status() = %v, want 错误", got)
	}
	err := p.takeFrameError()
	if err == nil {
		t.Fatal("no error recorded for a real traceback")
	}
	if !strings.Contains(okerr.AsError(err).Output, "ModuleNotFoundError") {
		t.Errorf("Output = %q, want the real traceback", okerr.AsError(err).Output)
	}
}

// TestProgressUnknownTotal mirrors `100.0 * frameCount / RJob.TotalFrame`
// being skipped when the frame count is unknown.
func TestProgressUnknownTotal(t *testing.T) {
	t.Parallel()

	p := New(Options{})
	handler := p.lineHandler()
	_ = handler("RPCOUT: 0 40.000000")
	if got := p.Progress().Percent; got != -1 {
		t.Errorf("Percent = %v, want -1 when the total is unknown", got)
	}
}

// TestOutputPathNaming covers the legacy `Output.Replace(".rpc", "-<status>.rpc")`
// and the switch to FailedRPCOutputFile when the check did not pass.
func TestOutputPathNaming(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		opts   Options
		status model.RPCStatus
		want   string
	}{
		{
			name:   "passed uses the source extension",
			opts:   Options{SourceScript: `D:\work\source.vpy`, FailedOutput: `D:\out\ep01.rpc`},
			status: model.RPCPassed,
			want:   `D:\work\source-通过.rpc`,
		},
		{
			name:   "failed switches to the failed-result path",
			opts:   Options{SourceScript: `D:\work\source.vpy`, FailedOutput: `D:\out\ep01.rpc`},
			status: model.RPCFailed,
			want:   `D:\out\ep01-未通过.rpc`,
		},
		{
			name:   "error also switches to the failed-result path",
			opts:   Options{SourceScript: `D:\work\source.vpy`, FailedOutput: `D:\out\ep01.rpc`},
			status: model.RPCError,
			want:   `D:\out\ep01-错误.rpc`,
		},
		{
			name:   "an explicit output overrides both",
			opts:   Options{SourceScript: `D:\work\source.vpy`, FailedOutput: `D:\out\ep01.rpc`, Output: `E:\chosen\result.rpc`},
			status: model.RPCPassed,
			want:   `E:\chosen\result-通过.rpc`,
		},
		{
			name:   "a source without an extension still gets one",
			opts:   Options{SourceScript: `D:\work\source`},
			status: model.RPCPassed,
			want:   `D:\work\source-通过.rpc`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := New(tc.opts)
			p.status = tc.status
			if got := p.outputPath(); got != tc.want {
				t.Errorf("outputPath() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestWriteResultLayout verifies which of the two legacy classes ends up on
// disk: RpcResult for a Y-only run, RpcResult3 for everything else.
func TestWriteResultLayout(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		lines      []string
		wantFields int // tuple members in the raw JSON: 2 or 4
		wantSample int
		wantPair   bool
	}{
		{
			name:       "two-field run writes RpcResult",
			lines:      []string{"RPCOUT: 0 40.500000", "RPCOUT: 1 41.500000"},
			wantFields: 2,
			wantSample: 2,
			wantPair:   true,
		},
		{
			name:       "four-field run writes RpcResult3",
			lines:      []string{"RPCOUT: 0 40.500000 45.500000 46.500000"},
			wantFields: 4,
			wantSample: 1,
		},
		{
			// No RPCOUT line: RpChecker serialized its (empty) RpcResult3.
			name:       "no samples writes an empty RpcResult3",
			wantFields: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			p := New(Options{
				SourceScript: `D:\work\source.vpy`,
				RippedFile:   `D:\work\00001.mkv`,
				Output:       filepath.Join(dir, "result.rpc"),
			})
			handler := p.lineHandler()
			for _, line := range tc.lines {
				_ = handler(line)
			}
			if err := p.writeResult(); err != nil {
				t.Fatalf("writeResult() error = %v", err)
			}

			raw := readFile(t, p.outputPath())
			if got := tupleFields(t, raw); got != tc.wantFields {
				t.Errorf("tuple member count = %d, want %d\n%s", got, tc.wantFields, raw)
			}

			got, err := ParseResult(raw)
			if err != nil {
				t.Fatalf("ParseResult() error = %v", err)
			}
			if len(got.Samples) != tc.wantSample {
				t.Errorf("samples = %d, want %d", len(got.Samples), tc.wantSample)
			}
			if tc.wantPair {
				if got.FileNamePair == nil {
					t.Fatal("FileNamePair = nil, want the legacy pair")
				}
				if got.FileNamePair.Src != `D:\work\source.vpy` || got.FileNamePair.Opt != `D:\work\00001.mkv` {
					t.Errorf("FileNamePair = %+v, want the source/ripped pair", *got.FileNamePair)
				}
			} else if got.FileNamePair != nil {
				t.Errorf("FileNamePair = %+v, want nil for the YUV layout", *got.FileNamePair)
			}
		})
	}
}

// tupleFields reports how many members the first sample tuple has, or 0 when
// the Data array is empty. It reads the raw JSON because the decoder cannot
// tell an empty RpcResult3 from an empty RpcResult.
func tupleFields(t *testing.T, raw []byte) int {
	t.Helper()
	var files []struct {
		Data []map[string]any `json:"Data"`
	}
	if err := json.Unmarshal(raw, &files); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	if len(files) == 0 || len(files[0].Data) == 0 {
		return 0
	}
	return len(files[0].Data[0])
}

// The tests below drive a real child process. vspipe is replaced by this test
// binary re-executed with a sentinel environment variable, which keeps the
// production Run code path untouched, including the exact argv it builds.
const (
	helperEnvVar   = "OKEGUIDX_RPC_HELPER"
	helperScript   = "OKEGUIDX_RPC_HELPER_SCRIPT"
	helperExitCode = "OKEGUIDX_RPC_HELPER_EXIT"
	helperRecord   = "OKEGUIDX_RPC_HELPER_RECORD"
)

// TestMain doubles as the fake vspipe.
func TestMain(m *testing.M) {
	if os.Getenv(helperEnvVar) == "1" {
		os.Exit(helperMain())
	}
	os.Exit(m.Run())
}

func helperMain() int {
	if record := os.Getenv(helperRecord); record != "" {
		if err := os.WriteFile(record, []byte(strings.Join(os.Args[1:], "\n")), 0o600); err != nil {
			return 6
		}
	}
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
	code := 0
	if _, err := fmt.Sscanf(os.Getenv(helperExitCode), "%d", &code); err != nil {
		code = 0
	}
	return code
}

func TestRunWritesScriptAndResult(t *testing.T) {
	t.Setenv(helperEnvVar, "1")
	t.Setenv(helperScript, "!RPCOUT: 0 40.123457 45.123457 46.123457\n!Output 1 frames in 0.01 seconds (243.58 fps)")
	t.Setenv(helperExitCode, "0")

	dir := t.TempDir()
	templatePath := writeTemplate(t, dir)
	ripped := filepath.Join(dir, "00001.mkv")
	record := filepath.Join(dir, "argv.txt")
	t.Setenv(helperRecord, record)

	p := New(Options{
		VSPipe:       os.Args[0],
		Template:     templatePath,
		SourceScript: filepath.Join(dir, "source.vpy"),
		RippedFile:   ripped,
		VSPipeArgs:   []string{"deint=yes"},
		TotalFrames:  1,
		Output:       filepath.Join(dir, "result.rpc"),
	})

	if err := p.Run(t.Context(), jobproc.NopSink{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := p.Status(); got != model.RPCPassed {
		t.Errorf("Status() = %v, want 通过", got)
	}

	// The argv must be `<script> .`, exactly what the legacy command line was.
	argv := strings.Split(strings.TrimRight(string(readFile(t, record)), "\n"), "\n")
	want := []string{ScriptPath(ripped), "."}
	if len(argv) != len(want) {
		t.Fatalf("argv = %q, want %q", argv, want)
	}
	for i := range want {
		if argv[i] != want[i] {
			t.Errorf("argv[%d] = %q, want %q", i, argv[i], want[i])
		}
	}

	// The generated script must have been written with the substitutions.
	script := string(readFile(t, ScriptPath(ripped)))
	if !strings.Contains(script, "setattr(mod, 'deint', b'yes')") {
		t.Error("generated script is missing the vspipe argument clause")
	}

	got, err := ParseResult(readFile(t, p.outputPath()))
	if err != nil {
		t.Fatalf("ParseResult() error = %v", err)
	}
	if !got.YUV || len(got.Samples) != 1 {
		t.Fatalf("result = %+v, want one four-field sample", got)
	}
	if got.Samples[0].Value != 40.123457 {
		t.Errorf("sample value = %v, want 40.123457", got.Samples[0].Value)
	}
}

func TestRunFailsOnNonZeroExit(t *testing.T) {
	t.Setenv(helperEnvVar, "1")
	t.Setenv(helperScript, "!Script evaluation failed:\n!Python exception: boom\n!\n!ModuleNotFoundError: No module named 'x'")
	t.Setenv(helperExitCode, "1")

	dir := t.TempDir()
	p := New(Options{
		VSPipe:       os.Args[0],
		Template:     writeTemplate(t, dir),
		SourceScript: filepath.Join(dir, "source.vpy"),
		RippedFile:   filepath.Join(dir, "00001.mkv"),
		SourceFile:   "ep01.mkv",
		Output:       filepath.Join(dir, "result.rpc"),
	})

	err := p.Run(t.Context(), jobproc.NopSink{})
	if err == nil {
		t.Fatal("Run() = nil, want a failure")
	}
	if got := p.Status(); got != model.RPCError {
		t.Errorf("Status() = %v, want 错误", got)
	}
	if e := okerr.AsError(err); e.Summary != okerr.ErrRPC.Summary {
		t.Errorf("Summary = %q, want %q", e.Summary, okerr.ErrRPC.Summary)
	}
}

// TestRunFailsOnNonZeroExitWithoutTraceback is the deliberate deviation: the
// legacy code reported a pass whenever the run ended with an "Output " line,
// even when vspipe had failed.
func TestRunFailsOnNonZeroExitWithoutTraceback(t *testing.T) {
	t.Setenv(helperEnvVar, "1")
	t.Setenv(helperScript, "!Output 1 frames in 0.01 seconds (243.58 fps)")
	t.Setenv(helperExitCode, "1")

	dir := t.TempDir()
	p := New(Options{
		VSPipe:       os.Args[0],
		Template:     writeTemplate(t, dir),
		SourceScript: filepath.Join(dir, "source.vpy"),
		RippedFile:   filepath.Join(dir, "00001.mkv"),
		Output:       filepath.Join(dir, "result.rpc"),
	})

	err := p.Run(t.Context(), jobproc.NopSink{})
	if err == nil {
		t.Fatal("Run() = nil, want a failure for a non-zero exit")
	}
	if e := okerr.AsError(err); e.Kind != okerr.KindTool {
		t.Errorf("Kind = %q, want %q", e.Kind, okerr.KindTool)
	}
}

func TestRunMissingToolFails(t *testing.T) {
	t.Parallel()

	p := New(Options{
		VSPipe:       filepath.Join(t.TempDir(), "no-such-vspipe"),
		Template:     filepath.Join("testdata", "RpcTemplate.vpy"),
		SourceScript: "source.vpy",
		RippedFile:   filepath.Join(t.TempDir(), "00001.mkv"),
	})
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

	dir := t.TempDir()
	p := New(Options{
		VSPipe:       os.Args[0],
		Template:     writeTemplate(t, dir),
		SourceScript: filepath.Join(dir, "source.vpy"),
		RippedFile:   filepath.Join(dir, "00001.mkv"),
		Output:       filepath.Join(dir, "result.rpc"),
	})

	err := p.Run(ctx, jobproc.NopSink{})
	if err == nil {
		t.Fatal("Run() = nil, want a cancellation error")
	}
	if got := okerr.AsError(err).Kind; got != okerr.KindCanceled {
		t.Errorf("Kind = %q, want %q", got, okerr.KindCanceled)
	}
}

func TestNamesAndInterfaces(t *testing.T) {
	t.Parallel()

	var (
		_ jobproc.Processor     = New(Options{})
		_ jobproc.Controllable  = New(Options{})
		_ jobproc.Prioritizable = New(Options{})
	)
	if got := New(Options{}).Name(); got != Name {
		t.Errorf("Name() = %q, want %q", got, Name)
	}
	if DefaultTemplatePath == "" {
		t.Error("DefaultTemplatePath is empty")
	}
}

func TestLifecycleBeforeStart(t *testing.T) {
	t.Parallel()

	p := New(Options{})
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

func writeTemplate(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "RpcTemplate.vpy")
	if err := os.WriteFile(path, readFixture(t, "RpcTemplate.vpy"), 0o600); err != nil {
		t.Fatalf("write template: %v", err)
	}
	return path
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
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
