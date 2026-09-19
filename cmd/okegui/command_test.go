package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
)

// vpyWithInputTag is the smallest script the validator accepts: it must contain
// the "# OKE:INPUTFILE" marker the wizard rewrites.
const vpyWithInputTag = `import vapoursynth as vs
#OKE:INPUTFILE
a = "in.m2ts"
`

// profileJSON renders a minimal, valid version-3 profile. It mirrors the shape
// of dist/windows/examples/*.json, including the trailing comma the tolerant
// parser exists for.
func profileJSON(container string, inputs ...string) string {
	quoted := make([]string, 0, len(inputs))
	for _, in := range inputs {
		quoted = append(quoted, `"`+strings.ReplaceAll(in, `\`, `\\`)+`"`)
	}
	return `{
    "Version" : 3,
    "VSVersion" : "2024H1",
    "ProjectName" : "Test - 1080p",
    "EncoderType" : "x265",
    "EncoderParam" : "--preset slower --crf 16",
    "ContainerFormat" : "` + container + `",
    "AudioTracks" : [],
    "InputScript" : "test.vpy",
    "InputFiles" : [` + strings.Join(quoted, ", ") + `],
    "Fps" : 23.976,
}`
}

// fixture is a profile directory laid out the way a technical director would
// create it: the .json, the .vpy and the input files side by side.
type fixture struct {
	dir      string
	profile  string
	inputs   []string
	config   string
	queue    string
	toolsDir string
}

// newFixture writes a valid profile plus its inputs into a temporary
// directory. It never creates a real media file or a real encoder: the tests
// must not run any tool, and the engine refuses to run the pipeline anyway
// (D3).
func newFixture(t *testing.T, container string, inputCount int) *fixture {
	t.Helper()
	dir := t.TempDir()

	inputs := make([]string, 0, inputCount)
	for i := range inputCount {
		name := "in" + string(rune('0'+i)) + ".m2ts"
		if err := os.WriteFile(filepath.Join(dir, name), []byte("not a real stream"), 0o600); err != nil {
			t.Fatalf("write input %s: %v", name, err)
		}
		inputs = append(inputs, name)
	}
	if err := os.WriteFile(filepath.Join(dir, "test.vpy"), []byte(vpyWithInputTag), 0o600); err != nil {
		t.Fatalf("write vpy: %v", err)
	}
	writeEncoderStub(t, dir)

	f := &fixture{
		dir:      dir,
		profile:  filepath.Join(dir, "test.json"),
		inputs:   inputs,
		config:   filepath.Join(dir, "OKEGuiConfig.json"),
		queue:    filepath.Join(dir, "queue.json"),
		toolsDir: dir,
	}
	f.writeProfile(t, container, inputs...)
	return f
}

// writeEncoderStub creates the smallest tools tree that lets the toolchain
// resolve an x265 encoder, which validation requires when the profile does not
// name one. The file is never executed: nothing in these tests starts a
// process.
func writeEncoderStub(t *testing.T, root string) {
	t.Helper()
	exe := ""
	if runtime.GOOS == "windows" {
		exe = ".exe"
	}
	dir := filepath.Join(root, "tools", "x26x")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create tools tree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x265"+exe), []byte("stub"), 0o700); err != nil {
		t.Fatalf("write encoder stub: %v", err)
	}
}

// writeProfile replaces the profile file with one naming the given inputs.
func (f *fixture) writeProfile(t *testing.T, container string, inputs ...string) {
	t.Helper()
	if err := os.WriteFile(f.profile, []byte(profileJSON(container, inputs...)), 0o600); err != nil {
		t.Fatalf("write profile: %v", err)
	}
}

// writeRaw replaces the profile file with arbitrary bytes, for the malformed
// cases.
func (f *fixture) writeRaw(t *testing.T, raw string) {
	t.Helper()
	if err := os.WriteFile(f.profile, []byte(raw), 0o600); err != nil {
		t.Fatalf("write raw profile: %v", err)
	}
}

// args returns the command line a test would type, with every path pinned into
// the temporary directory so nothing touches the real user profile.
func (f *fixture) args(extra ...string) []string {
	return f.argsAtLevel("ERROR", extra...)
}

// argsAtLevel is args with an explicit log level, for the tests that assert on
// a warning.
func (f *fixture) argsAtLevel(level string, extra ...string) []string {
	base := make([]string, 0, 8+len(extra))
	base = append(base,
		"--config", f.config,
		"--queue", f.queue,
		"--tools", f.toolsDir,
		"--log-level", level,
	)
	return append(base, extra...)
}

// runArgs prepends the subcommand.
func (f *fixture) runArgs(sub string, extra ...string) []string {
	return append([]string{sub}, f.args(extra...)...)
}

// runArgsVerbose is runArgs with a level low enough to show warnings.
func (f *fixture) runArgsVerbose(sub string, extra ...string) []string {
	return append([]string{sub}, f.argsAtLevel("WARN", extra...)...)
}

func TestRunDryRunValidatesProfile(t *testing.T) {
	f := newFixture(t, "MKV", 2)

	got := runCLI(t, f.runArgs("run", "--dry-run", f.profile)...)
	if got.code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", got.code, exitOK, got.stderr)
	}
	if !strings.Contains(got.stdout, "配置校验通过：2 个任务") {
		t.Errorf("stdout = %q, want it to report two tasks", got.stdout)
	}
	// A dry run must not create a queue: nothing was executed.
	if _, err := os.Stat(f.queue); !os.IsNotExist(err) {
		t.Errorf("dry run created a queue file: %v", err)
	}
}

func TestRunRejectsBadProfiles(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantMsg string
	}{
		{
			name:    "not json at all",
			raw:     `this is not json`,
			wantMsg: "json文件写错了诶",
		},
		{
			name:    "wrong schema version",
			raw:     `{"Version": 1, "EncoderType": "x265"}`,
			wantMsg: "版本不对",
		},
		{
			name:    "missing VSVersion for v3",
			raw:     `{"Version": 3, "EncoderType": "x265", "ContainerFormat": "mkv"}`,
			wantMsg: "没有指定VS版本",
		},
		{
			name:    "unknown encoder",
			raw:     `{"Version": 3, "VSVersion": "2024H1", "EncoderType": "x265-asuna"}`,
			wantMsg: "编码器版本错误",
		},
		{
			name:    "unknown container",
			raw:     `{"Version": 3, "VSVersion": "2024H1", "EncoderType": "x265", "ContainerFormat": "avi"}`,
			wantMsg: "封装格式指定的有问题",
		},
		{
			name:    "vfr mp4 is refused",
			raw:     `{"Version": 3, "VSVersion": "2024H1", "EncoderType": "x265", "ContainerFormat": "mp4", "TimeCode": true}`,
			wantMsg: "MP4暂不支持VFR封装",
		},
		{
			name:    "unsupported audio codec",
			raw:     `{"Version": 3, "VSVersion": "2024H1", "EncoderType": "x265", "ContainerFormat": "mkv", "Fps": 23.976, "AudioTracks": [{"OutputCodec": "opus"}]}`,
			wantMsg: "音轨格式不支持",
		},
		{
			name:    "flac in mp4 is refused",
			raw:     `{"Version": 3, "VSVersion": "2024H1", "EncoderType": "x265", "ContainerFormat": "mp4", "Fps": 23.976, "AudioTracks": [{"OutputCodec": "flac"}]}`,
			wantMsg: "MP4格式没法封FLAC",
		},
		{
			name:    "unknown frame rate",
			raw:     `{"Version": 3, "VSVersion": "2024H1", "EncoderType": "x265", "ContainerFormat": "mkv", "Fps": 17.5}`,
			wantMsg: "不知道的帧率诶",
		},
		{
			name:    "deprecated option",
			raw:     `{"Version": 3, "VSVersion": "2024H1", "EncoderType": "x265", "SkipMuxing": true}`,
			wantMsg: "json文件版本太老了",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, "MKV", 1)
			f.writeRaw(t, tc.raw)

			got := runCLI(t, f.runArgs("run", "--dry-run", f.profile)...)
			if got.code != exitUsage {
				t.Errorf("exit code = %d, want %d (stderr: %s)", got.code, exitUsage, got.stderr)
			}
			if !strings.Contains(got.stderr, tc.wantMsg) {
				t.Errorf("stderr = %q, want it to contain %q", got.stderr, tc.wantMsg)
			}
		})
	}
}

func TestRunRejectsMissingInputsAndScript(t *testing.T) {
	t.Run("input file missing", func(t *testing.T) {
		f := newFixture(t, "MKV", 0)
		f.writeProfile(t, "MKV", "gone.m2ts")

		got := runCLI(t, f.runArgs("run", "--dry-run", f.profile)...)
		if got.code != exitUsage {
			t.Errorf("exit code = %d, want %d", got.code, exitUsage)
		}
		if !strings.Contains(got.stderr, "找不到输入文件啊") {
			t.Errorf("stderr = %q, want a missing-input message", got.stderr)
		}
	})

	t.Run("vpy missing", func(t *testing.T) {
		f := newFixture(t, "MKV", 1)
		if err := os.Remove(filepath.Join(f.dir, "test.vpy")); err != nil {
			t.Fatalf("remove vpy: %v", err)
		}

		got := runCLI(t, f.runArgs("run", "--dry-run", f.profile)...)
		if got.code != exitUsage {
			t.Errorf("exit code = %d, want %d", got.code, exitUsage)
		}
		if !strings.Contains(got.stderr, "vpy文件找不到") {
			t.Errorf("stderr = %q, want a missing-vpy message", got.stderr)
		}
	})

	t.Run("vpy has no input tag", func(t *testing.T) {
		f := newFixture(t, "MKV", 1)
		if err := os.WriteFile(filepath.Join(f.dir, "test.vpy"), []byte("import vapoursynth as vs\n"), 0o600); err != nil {
			t.Fatalf("write vpy: %v", err)
		}

		got := runCLI(t, f.runArgs("run", "--dry-run", f.profile)...)
		if got.code != exitUsage {
			t.Errorf("exit code = %d, want %d", got.code, exitUsage)
		}
		if !strings.Contains(got.stderr, "#OKE:INPUTFILE") {
			t.Errorf("stderr = %q, want a missing-tag message", got.stderr)
		}
	})
}

// TestRunFailsTasksWithUnusableSources covers the exit code a real run produces
// when the task cannot be carried out. The fixture's "input" is a text file, so
// the pipeline refuses it; what is asserted is the contract, not the pipeline:
// the CLI ran, the task failed, so the code is exitTaskFailed.
func TestRunFailsTasksWithUnusableSources(t *testing.T) {
	f := newFixture(t, "MKV", 1)

	got := runCLI(t, f.runArgs("run", f.profile)...)
	if got.code != exitTaskFailed {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", got.code, exitTaskFailed, got.stderr)
	}
	if !strings.Contains(got.stderr, "未成功") {
		t.Errorf("stderr = %q, want a summary of the failed task", got.stderr)
	}

	// The run must leave a queue behind: that is what gives the standalone
	// engine crash recovery, and the status command reads it back.
	raw, err := os.ReadFile(f.queue)
	if err != nil {
		t.Fatalf("read queue: %v", err)
	}
	var qf struct {
		Tasks []struct {
			Task model.Task `json:"task"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(raw, &qf); err != nil {
		t.Fatalf("parse queue: %v", err)
	}
	if len(qf.Tasks) != 1 {
		t.Fatalf("queue holds %d tasks, want 1", len(qf.Tasks))
	}
	if got := qf.Tasks[0].Task.Status.Progress; got != model.TaskError {
		t.Errorf("queued task progress = %v, want ERROR", got)
	}
}

// TestRunSplitsProfileIntoOneTaskPerInput pins the granularity decision: a
// profile listing three sources produces three queue entries, which is what the
// legacy wizard did and what CLUSTER.md §3 assumes.
func TestRunSplitsProfileIntoOneTaskPerInput(t *testing.T) {
	f := newFixture(t, "MKV", 3)

	got := runCLI(t, f.runArgs("run", "--dry-run", f.profile)...)
	if got.code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", got.code, exitOK, got.stderr)
	}
	if n := strings.Count(got.stdout, "Test - 1080p"); n != 3 {
		t.Errorf("stdout lists %d tasks, want 3:\n%s", n, got.stdout)
	}
	for _, in := range f.inputs {
		if !strings.Contains(got.stdout, in) {
			t.Errorf("stdout does not mention input %s:\n%s", in, got.stdout)
		}
	}
}

func TestRunAcceptsSeveralProfiles(t *testing.T) {
	f := newFixture(t, "MKV", 1)

	second := filepath.Join(f.dir, "second.json")
	secondVpy := filepath.Join(f.dir, "second.vpy")
	if err := os.WriteFile(secondVpy, []byte(vpyWithInputTag), 0o600); err != nil {
		t.Fatalf("write second vpy: %v", err)
	}
	raw := strings.ReplaceAll(profileJSON("MKV", "in0.m2ts"), `"test.vpy"`, `"second.vpy"`)
	if err := os.WriteFile(second, []byte(raw), 0o600); err != nil {
		t.Fatalf("write second profile: %v", err)
	}

	got := runCLI(t, f.runArgs("run", "--dry-run", f.profile, second)...)
	if got.code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", got.code, exitOK, got.stderr)
	}
	if !strings.Contains(got.stdout, "配置校验通过：2 个任务") {
		t.Errorf("stdout = %q, want two tasks", got.stdout)
	}
}

// TestRunChecksEveryProfileBeforeStarting asserts the all-or-nothing rule: a
// bad second profile must stop the command before the first one is queued.
func TestRunChecksEveryProfileBeforeStarting(t *testing.T) {
	f := newFixture(t, "MKV", 1)

	bad := filepath.Join(f.dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"Version": 1}`), 0o600); err != nil {
		t.Fatalf("write bad profile: %v", err)
	}

	got := runCLI(t, f.runArgs("run", f.profile, bad)...)
	if got.code != exitUsage {
		t.Errorf("exit code = %d, want %d (stderr: %s)", got.code, exitUsage, got.stderr)
	}
	if _, err := os.Stat(f.queue); !os.IsNotExist(err) {
		t.Errorf("a rejected run created a queue file: %v", err)
	}
}

func TestStatusReportsCapabilitiesAndQueue(t *testing.T) {
	f := newFixture(t, "MKV", 1)

	got := runCLI(t, f.runArgs("status", "--verbose")...)
	if got.code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", got.code, exitOK, got.stderr)
	}

	// toolchain.Describe owns the capability block, so the test asserts the
	// fields it must carry rather than its exact layout.
	for _, want := range []string{"node=", "role=standalone", "features:", "任务队列:", "共 0 个任务"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("status output does not contain %q:\n%s", want, got.stdout)
		}
	}
	// The tools tree is empty in the fixture, so the mandatory tools must be
	// reported as missing rather than silently ignored.
	if !strings.Contains(got.stdout, "必需工具: 缺失") {
		t.Errorf("status did not report the missing tools:\n%s", got.stdout)
	}
	if !strings.Contains(got.stdout, "卷 local ->") {
		t.Errorf("status --verbose did not print the volume map:\n%s", got.stdout)
	}
}

func TestStatusSummarisesAQueue(t *testing.T) {
	f := newFixture(t, "MKV", 1)

	// A real run writes a queue with a failed task; the status command must
	// count it.
	if got := runCLI(t, f.runArgs("run", f.profile)...); got.code != exitTaskFailed {
		t.Fatalf("run exit code = %d, want %d (stderr: %s)", got.code, exitTaskFailed, got.stderr)
	}

	got := runCLI(t, f.runArgs("status")...)
	if got.code != exitOK {
		t.Fatalf("status exit code = %d, want %d", got.code, exitOK)
	}
	if !strings.Contains(got.stdout, "共 1 个任务") || !strings.Contains(got.stdout, "失败 1") {
		t.Errorf("status did not summarise the queue:\n%s", got.stdout)
	}
}

func TestStatusReportsADamagedQueue(t *testing.T) {
	f := newFixture(t, "MKV", 1)
	if err := os.WriteFile(f.queue, []byte("{ this is not json"), 0o600); err != nil {
		t.Fatalf("write queue: %v", err)
	}

	got := runCLI(t, f.runArgs("status")...)
	// A damaged queue is part of the status, not a failure of the command.
	if got.code != exitOK {
		t.Errorf("exit code = %d, want %d (stderr: %s)", got.code, exitOK, got.stderr)
	}
	if !strings.Contains(got.stdout, "无法读取") {
		t.Errorf("status did not report the damaged queue:\n%s", got.stdout)
	}
}

func TestStatusJSONIsMachineReadable(t *testing.T) {
	f := newFixture(t, "MKV", 1)

	got := runCLI(t, f.runArgs("status", "--json")...)
	if got.code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", got.code, exitOK, got.stderr)
	}

	var report nodeReport
	if err := json.Unmarshal([]byte(got.stdout), &report); err != nil {
		t.Fatalf("status --json is not valid JSON: %v\n%s", err, got.stdout)
	}
	if report.Capabilities.Role != "standalone" {
		t.Errorf("role = %q, want standalone", report.Capabilities.Role)
	}
	if report.Capabilities.OS == "" || report.Capabilities.Arch == "" {
		t.Errorf("capabilities lost the platform: %+v", report.Capabilities)
	}
	if report.Queue == nil {
		t.Fatal("queue is missing from the report")
	}
	if report.Queue.Path != f.queue {
		t.Errorf("queue path = %q, want %q", report.Queue.Path, f.queue)
	}
	if report.Queue.Total != 0 {
		t.Errorf("queue total = %d, want 0", report.Queue.Total)
	}
	if report.ToolError == "" {
		t.Error("tool_error is empty although the fixture has no tools")
	}
}

// TestStatusIgnoresAConfigFileItCannotParse asserts that a damaged settings
// file does not stop the command: the defaults apply and the run continues.
func TestStatusIgnoresAConfigFileItCannotParse(t *testing.T) {
	f := newFixture(t, "MKV", 1)
	if err := os.WriteFile(f.config, []byte("{ not json"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	got := runCLI(t, f.runArgsVerbose("status")...)
	if got.code != exitOK {
		t.Errorf("exit code = %d, want %d (stderr: %s)", got.code, exitOK, got.stderr)
	}
	if !strings.Contains(got.stderr, "无法读取配置文件") {
		t.Errorf("stderr = %q, want a warning about the settings file", got.stderr)
	}
}

func TestStatusConfigFileSuppliesToolPaths(t *testing.T) {
	f := newFixture(t, "MKV", 1)

	// The legacy configuration let an operator point at a vspipe outside the
	// tools tree; that override must still win over discovery.
	vspipe := filepath.Join(f.dir, "custom-vspipe.exe")
	if err := os.WriteFile(vspipe, []byte("stub"), 0o600); err != nil {
		t.Fatalf("write vspipe stub: %v", err)
	}
	cfg := `{"vspipePath": "` + strings.ReplaceAll(vspipe, `\`, `\\`) + `", "logLevel": "INFO"}`
	if err := os.WriteFile(f.config, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	got := runCLI(t, f.runArgs("status", "--json")...)
	if got.code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", got.code, exitOK, got.stderr)
	}
	var report nodeReport
	if err := json.Unmarshal([]byte(got.stdout), &report); err != nil {
		t.Fatalf("parse status: %v", err)
	}
	info, ok := report.Capabilities.Tool("vspipe")
	if !ok {
		t.Fatalf("configured vspipe was not discovered: %s", got.stdout)
	}
	if info.Path != vspipe {
		t.Errorf("vspipe path = %q, want %q", info.Path, vspipe)
	}
}

// TestHelpTextsAreAvailable asserts every subcommand answers -h with its own
// usage text and exit code 0, which is what a user typing `okegui run -h`
// expects.
func TestHelpTextsAreAvailable(t *testing.T) {
	for _, name := range []string{"run", "daemon", "status", "gui"} {
		t.Run(name, func(t *testing.T) {
			got := runCLI(t, name, "-h")
			if got.code != exitOK {
				t.Errorf("%s -h exit code = %d, want %d", name, got.code, exitOK)
			}
			if !strings.Contains(got.stdout, "用法: okegui "+name) {
				t.Errorf("%s -h stdout = %q, want its usage text", name, got.stdout)
			}
			if !strings.Contains(got.stdout, "退出码") {
				t.Errorf("%s -h does not document the exit codes:\n%s", name, got.stdout)
			}
		})
	}
}

// TestGUIPrintsTheInterfaceAddress asserts `gui` reports where the interface
// lives and honours --open=false. It reuses the daemon's lifecycle, so the
// shutdown path is exercised too.
func TestGUIPrintsTheInterfaceAddress(t *testing.T) {
	f := newFixture(t, "MKV", 1)

	// gui never returns on its own: it waits for a signal like the daemon. The
	// test therefore drives it with a context that is already cancelled, which
	// is what a signal produces, so the command returns after a clean
	// shutdown.
	args := f.runArgs("gui", "--addr", "127.0.0.1:19999", "--open=false")
	got := runCLIWithCancelledContext(t, args...)

	if got.code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", got.code, exitOK, got.stderr)
	}
	if !strings.Contains(got.stdout, "http://127.0.0.1:19999/") {
		t.Errorf("gui did not print the interface address:\n%s", got.stdout)
	}
	if !strings.Contains(got.stdout, "浏览器启动: 已禁用") {
		t.Errorf("gui ignored --open=false:\n%s", got.stdout)
	}
}

// TestGUIAcceptsTheDaemonFlags asserts `gui` shares the daemon's flag set, so
// an operator does not have to learn a second one.
func TestGUIAcceptsTheDaemonFlags(t *testing.T) {
	f := newFixture(t, "MKV", 1)
	// Port 0 binds a free port: the test must not collide with a daemon the
	// operator happens to have running on 8090.
	args := f.runArgs("gui", "--addr", "127.0.0.1:0", "--open=false",
		"--shutdown-grace", "2s", "--pid-file", filepath.Join(f.dir, "gui.pid"))
	got := runCLIWithCancelledContext(t, args...)

	if got.code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", got.code, exitOK, got.stderr)
	}
	// The pid file is removed on the way out, so its absence proves the
	// daemon's cleanup path ran.
	if _, err := os.Stat(filepath.Join(f.dir, "gui.pid")); !os.IsNotExist(err) {
		t.Errorf("gui did not remove its pid file: %v", err)
	}
}

// TestDaemonRejectsAnUnknownFlag asserts the daemon's flag set is wired up, so
// a typo fails loudly instead of being ignored.
func TestDaemonRejectsAnUnknownFlag(t *testing.T) {
	got := runCLI(t, "daemon", "--listen", "0.0.0.0:1")
	if got.code != exitUsage {
		t.Errorf("exit code = %d, want %d", got.code, exitUsage)
	}
}

// TestVersionFlag(t *testing.T) asserts the version line names the program and
// the platform.
func TestVersionFlag(t *testing.T) {
	got := runCLI(t, "--version")
	if got.code != exitOK {
		t.Fatalf("exit code = %d, want %d", got.code, exitOK)
	}
	if !strings.Contains(got.stdout, "okegui ") {
		t.Errorf("stdout = %q, want a version line", got.stdout)
	}
	if !strings.Contains(got.stdout, runtime.GOOS+"/"+runtime.GOARCH) {
		t.Errorf("stdout = %q, want the platform", got.stdout)
	}
}
