package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KiritakeKumi/OKEGuiDX/internal/profile"
	"github.com/KiritakeKumi/OKEGuiDX/internal/wizard"
)

// These tests cover the headless half of DECISIONS-NEEDED.md B4: `okegui run`
// assembles its tasks the way the legacy wizard did, instead of requiring a
// profile that was prepared by hand.

// TestRunAssemblesGeneratedScripts is the acceptance test: a real run writes
// the per-source .vpy and gives every task the three path fields the pipeline
// requires.
func TestRunAssemblesGeneratedScripts(t *testing.T) {
	f := newFixture(t, "MKV", 2)

	// The run itself fails (the fixture's sources are not real streams), but
	// the assembly has to happen before the first task starts.
	got := runCLI(t, f.runArgs("run", f.profile)...)
	if got.code != exitTaskFailed {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", got.code, exitTaskFailed, got.stderr)
	}

	prof, err := profile.Load(f.profile)
	if err != nil {
		t.Fatalf("profile.Load() error = %v", err)
	}
	want, err := wizard.Derive(prof, wizard.Options{ProjectFile: f.profile, ReducePath: true})
	if err != nil {
		t.Fatalf("wizard.Derive() error = %v", err)
	}
	if len(want.Tasks) != 2 {
		t.Fatalf("the wizard derived %d tasks, want 2", len(want.Tasks))
	}

	// Every generated script exists and holds the wizard's text.
	for _, task := range want.Tasks {
		raw, err := os.ReadFile(task.VpyFile)
		if err != nil {
			t.Fatalf("the generated vpy %s is missing: %v", task.VpyFile, err)
		}
		if string(raw) != task.Script {
			t.Errorf("%s differs from the wizard's text:\n got %q\nwant %q", task.VpyFile, raw, task.Script)
		}
	}

	// The queue records the derived fields per task, which is what makes them
	// runnable.
	raw, err := os.ReadFile(f.queue)
	if err != nil {
		t.Fatalf("read queue: %v", err)
	}
	var qf struct {
		Tasks []struct {
			Task struct {
				ID      string `json:"id"`
				Profile struct {
					InputScript       string `json:"InputScript"`
					WorkingPathPrefix string `json:"WorkingPathPrefix"`
					OutputPathPrefix  string `json:"OutputPathPrefix"`
				} `json:"profile"`
			} `json:"task"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(raw, &qf); err != nil {
		t.Fatalf("parse queue: %v", err)
	}
	if len(qf.Tasks) != 2 {
		t.Fatalf("queue holds %d tasks, want 2", len(qf.Tasks))
	}
	for _, entry := range qf.Tasks {
		if entry.Task.Profile.InputScript == "" {
			t.Errorf("task %s has no InputScript in the queue", entry.Task.ID)
		}
		if entry.Task.Profile.WorkingPathPrefix == "" {
			t.Errorf("task %s has no WorkingPathPrefix in the queue", entry.Task.ID)
		}
		if entry.Task.Profile.OutputPathPrefix == "" {
			t.Errorf("task %s has no OutputPathPrefix in the queue", entry.Task.ID)
		}
	}
	// The generated names must appear in the queue file.
	for _, task := range want.Tasks {
		if !strings.Contains(string(raw), filepath.Base(task.VpyFile)) {
			t.Errorf("the queue does not mention the generated script %s", task.VpyFile)
		}
	}
}

// TestRunDryRunDoesNotAssemble pins that --dry-run keeps its promise: it
// validates and reports, and touches no file.
func TestRunDryRunDoesNotAssemble(t *testing.T) {
	f := newFixture(t, "MKV", 1)

	got := runCLI(t, f.runArgs("run", "--dry-run", f.profile)...)
	if got.code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", got.code, exitOK, got.stderr)
	}

	prof, err := profile.Load(f.profile)
	if err != nil {
		t.Fatalf("profile.Load() error = %v", err)
	}
	want, err := wizard.Derive(prof, wizard.Options{ProjectFile: f.profile, ReducePath: true})
	if err != nil {
		t.Fatalf("wizard.Derive() error = %v", err)
	}
	for _, task := range want.Tasks {
		if _, err := os.Stat(task.VpyFile); !os.IsNotExist(err) {
			t.Errorf("--dry-run wrote %s (stat err = %v)", task.VpyFile, err)
		}
	}
}

// TestRunAssemblesOneTaskPerSource pins the granularity: each source of a
// profile gets its own generated script, and no two tasks share one.
func TestRunAssemblesOneTaskPerSource(t *testing.T) {
	f := newFixture(t, "MKV", 3)

	got := runCLI(t, f.runArgs("run", f.profile)...)
	if got.code != exitTaskFailed {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", got.code, exitTaskFailed, got.stderr)
	}

	prof, err := profile.Load(f.profile)
	if err != nil {
		t.Fatalf("profile.Load() error = %v", err)
	}
	want, err := wizard.Derive(prof, wizard.Options{ProjectFile: f.profile, ReducePath: true})
	if err != nil {
		t.Fatalf("wizard.Derive() error = %v", err)
	}

	seen := make(map[string]struct{}, len(want.Tasks))
	for _, task := range want.Tasks {
		if _, dup := seen[task.VpyFile]; dup {
			t.Errorf("two tasks share the script %s", task.VpyFile)
		}
		seen[task.VpyFile] = struct{}{}
		raw, err := os.ReadFile(task.VpyFile)
		if err != nil {
			t.Fatalf("read %s: %v", task.VpyFile, err)
		}
		if !strings.Contains(string(raw), `R"`+task.InputFile+`"`) {
			t.Errorf("%s does not reference its own source %s", task.VpyFile, task.InputFile)
		}
	}
}

// TestRunAssemblyRejectsAScriptWithoutTheTag pins the failure mode: a script
// that was not written for OKEGui is a usage error, not a task that starts and
// fails later.
func TestRunAssemblyRejectsAScriptWithoutTheTag(t *testing.T) {
	f := newFixture(t, "MKV", 1)
	if err := os.WriteFile(filepath.Join(f.dir, "test.vpy"),
		[]byte("import vapoursynth as vs\n"), 0o600); err != nil {
		t.Fatalf("write vpy: %v", err)
	}

	got := runCLI(t, f.runArgs("run", f.profile)...)
	if got.code != exitUsage {
		t.Fatalf("exit code = %d, want %d (stderr: %s)", got.code, exitUsage, got.stderr)
	}
	if !strings.Contains(got.stderr, "#OKE:INPUTFILE") {
		t.Errorf("stderr = %q, want a missing-tag message", got.stderr)
	}
}
