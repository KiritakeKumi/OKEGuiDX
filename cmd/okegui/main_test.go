package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
	"github.com/KiritakeKumi/OKEGuiDX/internal/profile"
)

// result captures one run of the command line.
type result struct {
	code   int
	stdout string
	stderr string
}

// syncWriter makes a bytes.Buffer safe to write from several goroutines. The
// worker pool logs and reports progress from its own goroutines while the
// command's main goroutine writes its own lines, so a plain bytes.Buffer would
// race. os.Stderr, which the real process uses, is safe.
type syncWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write implements io.Writer.
func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

// String returns everything written so far.
func (w *syncWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// Tests in this package are deliberately serial: internal/log is a
// process-global logger, so two parallel commands would race on its level and
// output, and the command line is fast enough that parallelism buys nothing.
// The isolation that matters comes from t.TempDir() and the pinned --config,
// --queue and --tools flags, not from goroutines.

// runCLI invokes the command line with fresh output buffers. It is the only
// entry point the tests use, so they exercise exactly what the process does.
func runCLI(t *testing.T, args ...string) result {
	t.Helper()
	var stdout, stderr syncWriter
	code := run(args, &stdout, &stderr)
	return result{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

// runCLIWithCancelledContext runs a long-lived subcommand with a context that
// is already cancelled, which drives its graceful-shutdown path without a real
// signal. Only the resident commands (daemon, gui) need it: the others return
// on their own.
func runCLIWithCancelledContext(t *testing.T, args ...string) result {
	t.Helper()
	cmd, rest, err := parseCommand(args)
	if err != nil {
		t.Fatalf("parseCommand(%v) error = %v", args, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var stdout, stderr syncWriter
	app := &application{stdout: &stdout, stderr: &stderr, ctx: ctx}
	err = cmd.run(app, rest)
	code := exitOK
	if err != nil {
		if errors.Is(err, context.Canceled) {
			code = exitInterrupted
		} else {
			code = exitCodeOf(err)
		}
	}
	return result{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func TestParseCommand(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    string
		rest    []string
		wantErr error
	}{
		{name: "no arguments prints help", args: nil, wantErr: errHelp},
		{name: "help flag", args: []string{"--help"}, wantErr: errHelp},
		{name: "short help flag", args: []string{"-h"}, wantErr: errHelp},
		{name: "help subcommand", args: []string{"help"}, wantErr: errHelp},
		{name: "version flag", args: []string{"--version"}, wantErr: errVersion},
		{name: "short version flag", args: []string{"-v"}, wantErr: errVersion},
		{name: "version subcommand", args: []string{"version"}, wantErr: errVersion},
		{name: "run", args: []string{"run", "a.json"}, want: "run", rest: []string{"a.json"}},
		{name: "daemon", args: []string{"daemon"}, want: "daemon", rest: []string{}},
		{name: "status", args: []string{"status", "--json"}, want: "status", rest: []string{"--json"}},
		{name: "gui", args: []string{"gui"}, want: "gui", rest: []string{}},
		{name: "unknown command", args: []string{"frobnicate"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd, rest, err := parseCommand(tc.args)
			switch {
			case tc.wantErr != nil:
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("parseCommand(%v) error = %v, want %v", tc.args, err, tc.wantErr)
				}
				if cmd != nil {
					t.Errorf("parseCommand(%v) returned command %q, want nil", tc.args, cmd.name)
				}
			case tc.want == "":
				if err == nil {
					t.Fatalf("parseCommand(%v) = nil error, want a failure", tc.args)
				}
				if !strings.Contains(err.Error(), "未知命令") {
					t.Errorf("parseCommand(%v) error = %v, want an unknown-command message", tc.args, err)
				}
			default:
				if err != nil {
					t.Fatalf("parseCommand(%v) error = %v", tc.args, err)
				}
				if cmd.name != tc.want {
					t.Errorf("parseCommand(%v) command = %q, want %q", tc.args, cmd.name, tc.want)
				}
				if strings.Join(rest, " ") != strings.Join(tc.rest, " ") {
					t.Errorf("parseCommand(%v) rest = %v, want %v", tc.args, rest, tc.rest)
				}
			}
		})
	}
}

// TestExitCodes is the contract a script driving okegui depends on, so it is
// asserted against the real command line rather than against the helpers.
func TestExitCodes(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantErr  string
	}{
		{
			name:     "help succeeds",
			args:     []string{"--help"},
			wantCode: exitOK,
		},
		{
			name:     "version succeeds",
			args:     []string{"--version"},
			wantCode: exitOK,
		},
		{
			name:     "unknown command is a usage error",
			args:     []string{"nope"},
			wantCode: exitUsage,
			wantErr:  "未知命令",
		},
		{
			name:     "run without a profile is a usage error",
			args:     []string{"run"},
			wantCode: exitUsage,
			wantErr:  "至少要指定一个 profile.json",
		},
		{
			name:     "invalid role is a usage error",
			args:     []string{"run", "--role", "master", "x.json"},
			wantCode: exitUsage,
			wantErr:  "无效的 --role",
		},
		{
			name:     "coordinator is refused explicitly",
			args:     []string{"run", "--role", "coordinator", "x.json"},
			wantCode: exitUsage,
			wantErr:  "尚未实现",
		},
		{
			name:     "worker is refused explicitly",
			args:     []string{"status", "--role", "worker"},
			wantCode: exitUsage,
			wantErr:  "尚未实现",
		},
		{
			name:     "status rejects positional arguments",
			args:     []string{"status", "extra"},
			wantCode: exitUsage,
			wantErr:  "不接受位置参数",
		},
		{
			name:     "daemon rejects positional arguments",
			args:     []string{"daemon", "extra"},
			wantCode: exitUsage,
			wantErr:  "不接受位置参数",
		},
		{
			name:     "gui rejects positional arguments",
			args:     []string{"gui", "extra"},
			wantCode: exitUsage,
			wantErr:  "不接受位置参数",
		},
		{
			name:     "missing profile is a usage error",
			args:     []string{"run", "does-not-exist.json"},
			wantCode: exitUsage,
			wantErr:  "无法读取json文件",
		},
		{
			name:     "unknown flag is a usage error",
			args:     []string{"status", "--nope"},
			wantCode: exitUsage,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := runCLI(t, tc.args...)
			if got.code != tc.wantCode {
				t.Errorf("exit code = %d, want %d (stderr: %s)", got.code, tc.wantCode, got.stderr)
			}
			if tc.wantErr != "" && !strings.Contains(got.stderr, tc.wantErr) {
				t.Errorf("stderr = %q, want it to contain %q", got.stderr, tc.wantErr)
			}
		})
	}
}

func TestExitCodeOf(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "nil is success", err: nil, want: exitOK},
		{name: "explicit code wins", err: fail(exitTaskFailed, "boom"), want: exitTaskFailed},
		{
			name: "validation error is a usage error",
			err:  &profile.ValidationError{Summary: "版本不对", Detail: "Version=1"},
			want: exitUsage,
		},
		{
			name: "wrapped validation error keeps its code",
			err:  wrapExit(exitUsage, &profile.ValidationError{Summary: "坏"}),
			want: exitUsage,
		},
		{
			name: "config error is a usage error",
			err:  okerr.New(okerr.KindConfig, "配置错误", "detail"),
			want: exitUsage,
		},
		{
			name: "missing tool is a usage error",
			err:  okerr.New(okerr.KindNotFound, "找不到外部工具", "detail"),
			want: exitUsage,
		},
		{
			name: "unsupported platform is a usage error",
			err:  okerr.ErrUnsupportedAAC,
			want: exitUsage,
		},
		{
			name: "mismatch is a usage error",
			err:  okerr.ErrAudioNumMismatch,
			want: exitUsage,
		},
		{
			name: "tool failure is an internal failure",
			err:  okerr.ErrX265,
			want: exitFailure,
		},
		{
			name: "plain error is an internal failure",
			err:  errors.New("something broke"),
			want: exitFailure,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := exitCodeOf(tc.err); got != tc.want {
				t.Errorf("exitCodeOf(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

func TestPrintError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want []string
	}{
		{
			name: "validation error shows summary and detail",
			err:  &profile.ValidationError{Summary: "版本不对", Detail: "只接受 2 或 3", Field: "Version"},
			want: []string{"版本不对", "只接受 2 或 3"},
		},
		{
			name: "structured error goes through the legacy template",
			err:  okerr.ErrUnsupportedAAC,
			want: []string{"该平台不支持AAC编码"},
		},
		{
			name: "plain error is printed verbatim",
			err:  errors.New("raw failure"),
			want: []string{"raw failure"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var b bytes.Buffer
			printError(&b, tc.err)
			for _, want := range tc.want {
				if !strings.Contains(b.String(), want) {
					t.Errorf("printError(%v) = %q, want it to contain %q", tc.err, b.String(), want)
				}
			}
		})
	}
}

func TestFormatEvent(t *testing.T) {
	id := model.NewTaskID()
	short := shortID(id)

	tests := []struct {
		name string
		ev   model.StatusEvent
		want []string
		// empty means the event renders to nothing at all
		empty bool
	}{
		{
			name: "progress line",
			ev: model.StatusEvent{
				TaskID: id, Progress: model.TaskRunning, Step: "x265",
				Percent: 42.5, FramesDone: 100, FramesTotal: 200, Speed: "12.34 fps",
			},
			want: []string{short, "x265", "42.50%", "100/200 帧", "12.34 fps"},
		},
		{
			name: "unknown percent is omitted",
			ev: model.StatusEvent{
				TaskID: id, Progress: model.TaskRunning, Step: "demux", Percent: -1,
			},
			want: []string{short, "demux"},
		},
		{
			name: "eta is rendered",
			ev: model.StatusEvent{
				TaskID: id, Progress: model.TaskRunning, Step: "x265", Percent: 50,
				TimeRemainSeconds: 90,
			},
			want: []string{"剩余 0:01:30"},
		},
		{
			name: "eta beyond a week uses the legacy wording",
			ev: model.StatusEvent{
				TaskID: id, Progress: model.TaskRunning, Step: "x265", Percent: 1,
				TimeRemainSeconds: 30 * 24 * 3600,
			},
			want: []string{"大于一周"},
		},
		{
			name: "finished",
			ev:   model.StatusEvent{TaskID: id, Progress: model.TaskFinished, Percent: 100},
			want: []string{short, "完成"},
		},
		{
			name: "failed with a structured error",
			ev: model.StatusEvent{TaskID: id, Progress: model.TaskError, Error: &model.ErrorInfo{
				Summary: "x265出错", Detail: "退出代码2",
			}},
			want: []string{short, "失败", "x265出错", "退出代码2"},
		},
		{
			name:  "empty running event renders nothing",
			ev:    model.StatusEvent{TaskID: id, Progress: model.TaskRunning},
			empty: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := formatEvent(tc.ev)
			if tc.empty {
				if got != "" {
					t.Fatalf("formatEvent() = %q, want an empty string", got)
				}
				return
			}
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("formatEvent() = %q, want it to contain %q", got, want)
				}
			}
		})
	}
}

func TestProgressReporterCollapsesRepeats(t *testing.T) {
	var b bytes.Buffer
	r := newProgressReporter(&b)

	ev := model.StatusEvent{TaskID: model.NewTaskID(), Progress: model.TaskRunning, Step: "x265", Percent: 10}
	r.report(ev)
	r.report(ev) // identical, must not be printed twice
	r.report(model.StatusEvent{TaskID: ev.TaskID, Progress: model.TaskRunning, Step: "x265", Percent: 11})
	r.report(model.StatusEvent{TaskID: ev.TaskID, Progress: model.TaskFinished, Percent: 100})

	out := b.String()
	if got := strings.Count(out, "10.00%"); got != 1 {
		t.Errorf("repeated event printed %d times, want 1 (output: %q)", got, out)
	}
	if !strings.Contains(out, "11.00%") {
		t.Errorf("changed progress was not printed: %q", out)
	}
	if !strings.Contains(out, "完成") {
		t.Errorf("terminal event was not printed: %q", out)
	}
	// A terminal line ends with a newline so the shell prompt cannot land on
	// top of it.
	if !strings.HasSuffix(out, "\n") {
		t.Errorf("terminal line does not end with a newline: %q", out)
	}
}

func TestProgressReporterFinishClearsLine(t *testing.T) {
	var b bytes.Buffer
	r := newProgressReporter(&b)
	r.tty = true // pretend a terminal, so the escape is exercised
	r.report(model.StatusEvent{TaskID: model.NewTaskID(), Progress: model.TaskRunning, Step: "x265", Percent: 5})
	r.finish()

	if !strings.HasSuffix(b.String(), "\r\033[K") {
		t.Errorf("finish() did not clear the line: %q", b.String())
	}
	// A second call has nothing left to clear.
	before := b.String()
	r.finish()
	if b.String() != before {
		t.Errorf("second finish() wrote more output: %q", b.String()[len(before):])
	}
}

// TestProgressReporterOmitsEscapesForFiles asserts a redirected progress log
// stays readable: no carriage returns, no escape sequences.
func TestProgressReporterOmitsEscapesForFiles(t *testing.T) {
	var b bytes.Buffer
	r := newProgressReporter(&b)
	r.report(model.StatusEvent{TaskID: model.NewTaskID(), Progress: model.TaskRunning, Step: "x265", Percent: 5})
	r.report(model.StatusEvent{TaskID: model.NewTaskID(), Progress: model.TaskFinished, Percent: 100})
	r.finish()

	out := b.String()
	if strings.ContainsAny(out, "\r\033") {
		t.Errorf("non-terminal output contains terminal escapes: %q", out)
	}
	if !strings.Contains(out, "x265") || !strings.Contains(out, "完成") {
		t.Errorf("non-terminal output lost its lines: %q", out)
	}
}

// TestIsTerminal rejects anything that is not a character device. It does not
// assert that the test process's stderr *is* one: `go test` pipes it, so the
// answer depends on how the suite was launched.
func TestIsTerminal(t *testing.T) {
	if isTerminal(&bytes.Buffer{}) {
		t.Error("isTerminal(*bytes.Buffer) = true, want false")
	}
	f, err := os.CreateTemp(t.TempDir(), "not-a-tty")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	defer f.Close()
	if isTerminal(f) {
		t.Error("isTerminal(regular file) = true, want false")
	}
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer devNull.Close()
	// os.DevNull is a character device on both supported platforms, so this
	// documents the check rather than asserting a specific answer.
	if isTerminal(devNull) {
		t.Logf("%s reports as a character device on this platform", os.DevNull)
	}
}

func TestShortID(t *testing.T) {
	id := model.NewTaskID()
	got := shortID(id)
	if got == id.String() {
		t.Errorf("shortID(%s) = %q, want a shortened form", id, got)
	}
	if !strings.HasPrefix(id.String(), got) {
		t.Errorf("shortID(%s) = %q, want it to be a prefix", id, got)
	}
	if got := shortID(""); got != "task" {
		t.Errorf("shortID(\"\") = %q, want \"task\"", got)
	}
}

func TestRoleValue(t *testing.T) {
	tests := []struct {
		name    string
		role    string
		want    string
		wantErr string
	}{
		{name: "standalone", role: "standalone", want: "standalone"},
		{name: "empty defaults to standalone", role: "", want: "standalone"},
		{name: "coordinator is refused", role: "coordinator", wantErr: "尚未实现"},
		{name: "worker is refused", role: "worker", wantErr: "尚未实现"},
		{name: "typo is rejected", role: "Standalone", wantErr: "无效的 --role"},
		{name: "nonsense is rejected", role: "master", wantErr: "无效的 --role"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := &options{role: tc.role}
			got, err := o.roleValue()
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("roleValue(%q) = %v, want an error", tc.role, got)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("roleValue(%q) error = %v, want it to contain %q", tc.role, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("roleValue(%q) error = %v", tc.role, err)
			}
			if string(got) != tc.want {
				t.Errorf("roleValue(%q) = %q, want %q", tc.role, got, tc.want)
			}
		})
	}
}

func TestOutputRef(t *testing.T) {
	tests := []struct {
		name      string
		container string
		input     string
		wantBase  string
		wantDir   string
	}{
		{
			name:      "mkv keeps the source name",
			container: "MKV",
			input:     `D:\work\00000.m2ts`,
			wantBase:  "00000.m2ts.mkv",
			wantDir:   "local/work",
		},
		{
			name:      "mp4 is lowercased",
			container: "MP4",
			input:     `/media/Show/ep01.mkv`,
			wantBase:  "ep01.mkv.mp4",
			wantDir:   "local/media/Show",
		},
		{
			name:      "a bare file name has no directory",
			container: "MKV",
			input:     `in.m2ts`,
			wantBase:  "in.m2ts.mkv",
			wantDir:   "local/",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &profile.Profile{ContainerFormat: tc.container}
			ref := outputRef(p, tc.input)
			if got := ref.Base(); got != tc.wantBase {
				t.Errorf("outputRef(%q) base = %q, want %q", tc.input, got, tc.wantBase)
			}
			// The output must land next to its input, which is the frozen
			// behaviour of the legacy wizard's UpdateOutputFileName.
			if got := ref.Dir().String(); got != tc.wantDir {
				t.Errorf("outputRef(%q) dir = %q, want %q", tc.input, got, tc.wantDir)
			}
		})
	}
}

func TestResolveFrom(t *testing.T) {
	tests := []struct {
		name string
		dir  string
		rel  string
		want string
	}{
		{name: "relative path joins the directory", dir: `D:\work`, rel: `in.m2ts`, want: `D:\work\in.m2ts`},
		{name: "nested relative path", dir: `D:\work`, rel: `a\b.vpy`, want: `D:\work\a\b.vpy`},
		{name: "absolute path is kept", dir: `D:\work`, rel: `E:\media\in.m2ts`, want: `E:\media\in.m2ts`},
		{name: "empty stays empty", dir: `D:\work`, rel: ``, want: ``},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveFrom(tc.dir, tc.rel); got != tc.want {
				t.Errorf("resolveFrom(%q, %q) = %q, want %q", tc.dir, tc.rel, got, tc.want)
			}
		})
	}
}
