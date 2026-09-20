package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KiritakeKumi/OKEGuiDX/internal/engine"
	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/node"
	"github.com/KiritakeKumi/OKEGuiDX/internal/platform"
	"github.com/KiritakeKumi/OKEGuiDX/internal/profile"
	"github.com/KiritakeKumi/OKEGuiDX/internal/wizard"
)

// The tests in this file cover the two endpoints that close the gaps
// DECISIONS-NEEDED.md B3 and B4 describe: the wizard's preview/assemble split
// (POST /tasks/prepare and write_vpy) and single-task cancellation
// (POST /tasks/{id}/cancel).

// exampleDir holds the profiles shipped with the current release. They are the
// frozen-format regression fixtures, and the API consumes them unchanged.
const exampleDir = "../../dist/windows/examples"

// exampleProfile copies one shipped example into a temporary directory and
// copies the script it names next to it, so a test can assemble a real profile
// without writing into the repository.
func exampleProfile(t *testing.T, name string) (profilePath, dir string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(exampleDir, name))
	if err != nil {
		t.Skipf("example profile not available: %v", err)
	}
	dir = t.TempDir()
	profilePath = filepath.Join(dir, name)
	writeFile(t, profilePath, string(raw))

	prof, err := profile.Load(profilePath)
	if err != nil {
		t.Fatalf("profile.Load(%s) error = %v", profilePath, err)
	}
	script, err := os.ReadFile(filepath.Join(exampleDir, filepath.Base(prof.InputScript)))
	if err != nil {
		t.Skipf("example script not available: %v", err)
	}
	writeFile(t, filepath.Join(dir, filepath.Base(prof.InputScript)), string(script))
	return profilePath, dir
}

// exampleInputs creates the source files a shipped profile names, so the
// derivation has something to point at. The content is irrelevant: these tests
// never run a tool.
func exampleInputs(t *testing.T, profilePath string) {
	t.Helper()
	prof, err := profile.Load(profilePath)
	if err != nil {
		t.Fatalf("profile.Load(%s) error = %v", profilePath, err)
	}
	dir := filepath.Dir(profilePath)
	for _, in := range prof.InputFiles {
		path := resolveRelative(in, dir)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		writeFile(t, path, "")
	}
}

// TestTaskPrepareMatchesTheWizard is the B4 acceptance test: for a real shipped
// profile, the endpoint's derivation is identical to calling internal/wizard
// directly. The comparison is against the package rather than against a
// hand-written expectation, because the package is the specification of the
// derivation.
func TestTaskPrepareMatchesTheWizard(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	cases := []struct {
		name string
		file string
	}{
		{name: "demo", file: "demo.json"},
		{name: "vfr", file: "vfr.json"},
		{name: "720p", file: "demo_720p.json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			profilePath, _ := exampleProfile(t, tc.file)
			exampleInputs(t, profilePath)

			rec := env.do(http.MethodPost, APIPrefix+"/tasks/prepare", prepareRequest{
				ProfilePath: profilePath,
			})
			wantStatus(t, rec, http.StatusOK)

			var resp prepareResponse
			decodeJSON(t, rec, &resp)

			prof, err := profile.Load(profilePath)
			if err != nil {
				t.Fatalf("profile.Load() error = %v", err)
			}
			want, err := wizard.Derive(prof, wizard.Options{
				ProjectFile: profilePath,
				// The harness's store carries platform.DefaultConfig, whose
				// ReducePath is true; the endpoint reads the same value.
				ReducePath: true,
			})
			if err != nil {
				t.Fatalf("wizard.Derive() error = %v", err)
			}

			if resp.Count != len(want.Tasks) {
				t.Fatalf("count = %d, want %d", resp.Count, len(want.Tasks))
			}
			if resp.MapFile != want.MapFile {
				t.Errorf("map_file = %q, want %q", resp.MapFile, want.MapFile)
			}
			for i := range want.Tasks {
				got := resp.Tasks[i]
				w := want.Tasks[i]
				if got.Name != w.Name {
					t.Errorf("task %d name = %q, want %q", i, got.Name, w.Name)
				}
				if got.InputFile != w.InputFile {
					t.Errorf("task %d input = %q, want %q", i, got.InputFile, w.InputFile)
				}
				if got.VpyFile != w.VpyFile {
					t.Errorf("task %d vpy = %q, want %q", i, got.VpyFile, w.VpyFile)
				}
				if got.WorkingPathPrefix != w.WorkingPathPrefix {
					t.Errorf("task %d working prefix = %q, want %q", i, got.WorkingPathPrefix, w.WorkingPathPrefix)
				}
				if got.OutputPathPrefix != w.OutputPathPrefix {
					t.Errorf("task %d output prefix = %q, want %q", i, got.OutputPathPrefix, w.OutputPathPrefix)
				}
				if got.Script != w.Script {
					t.Errorf("task %d script differs from the wizard's:\n got %q\nwant %q", i, got.Script, w.Script)
				}
				if !strings.Contains(got.Script, `R"`+got.InputFile+`"`) {
					t.Errorf("task %d script does not reference its own source", i)
				}
			}

			// A preview writes nothing: the generated script must not exist.
			for _, task := range resp.Tasks {
				if _, err := os.Stat(task.VpyFile); !os.IsNotExist(err) {
					t.Errorf("prepare wrote %s (stat err = %v)", task.VpyFile, err)
				}
			}
			if env.tasks.GetTaskCount() != 0 {
				t.Error("prepare queued a task")
			}
		})
	}
}

// TestTaskPrepareHonoursTheInlineForms covers the E2 contract on the new
// endpoint: profile_text and base_dir work exactly as they do for POST /tasks.
func TestTaskPrepareHonoursTheInlineForms(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	rec := env.do(http.MethodPost, APIPrefix+"/tasks/prepare", prepareRequest{
		ProfileText: env.fix.profileText(),
		BaseDir:     env.fix.dir,
	})
	wantStatus(t, rec, http.StatusOK)

	var resp prepareResponse
	decodeJSON(t, rec, &resp)
	if resp.Count != 1 {
		t.Fatalf("count = %d, want 1", resp.Count)
	}
	got := resp.Tasks[0]
	// The fixture profile names one input and one script; the derived vpy name
	// is the working prefix plus a timestamp.
	if got.InputFile != env.fix.input {
		t.Errorf("input = %q, want %q", got.InputFile, env.fix.input)
	}
	if !strings.HasPrefix(got.VpyFile, got.WorkingPathPrefix+"-") || !strings.HasSuffix(got.VpyFile, ".vpy") {
		t.Errorf("vpy = %q, want %q + timestamp + .vpy", got.VpyFile, got.WorkingPathPrefix)
	}
	// base_dir stands in for the profile's directory, which the browser cannot
	// know: the derived tree must be rooted there.
	if !strings.HasPrefix(got.WorkingPathPrefix, env.fix.dir) {
		t.Errorf("working prefix = %q, want it under base_dir %q", got.WorkingPathPrefix, env.fix.dir)
	}
}

// TestTaskPrepareRequestErrors covers the 400 paths, which are the same three
// profile forms addTaskRequest requires.
func TestTaskPrepareRequestErrors(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	cases := []struct {
		name string
		body string
		want int
	}{
		{name: "no profile", body: `{}`, want: http.StatusBadRequest},
		{name: "both forms", body: `{"profile_path":"a.json","profile":{"Version":3}}`, want: http.StatusBadRequest},
		{name: "malformed", body: `{"profile_path":`, want: http.StatusBadRequest},
		{name: "unknown field", body: `{"profile_path":"a.json","nope":1}`, want: http.StatusBadRequest},
		{
			// An inline profile has no directory of its own, so the derivation
			// has no root. Saying so is better than deriving into the process
			// working directory.
			name: "inline without base_dir",
			body: `{"profile_text":"{\"Version\":3,\"InputFiles\":[\"a.m2ts\"]}"}`,
			want: http.StatusBadRequest,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := env.do(http.MethodPost, APIPrefix+"/tasks/prepare", tc.body)
			wantStatus(t, rec, tc.want)
			errorOf(t, rec)
		})
	}
}

// TestTaskPrepareUnknownInputIsNotFound covers the subset selection: asking for
// a source the profile does not list is a missing resource, not an empty
// success.
func TestTaskPrepareUnknownInputIsNotFound(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	rec := env.do(http.MethodPost, APIPrefix+"/tasks/prepare", prepareRequest{
		ProfilePath: env.fix.profile,
		Inputs:      []string{filepath.Join(env.fix.dir, "nope.m2ts")},
	})
	wantStatus(t, rec, http.StatusNotFound)
	errorOf(t, rec)

	// Naming the real one returns just that task.
	rec = env.do(http.MethodPost, APIPrefix+"/tasks/prepare", prepareRequest{
		ProfilePath: env.fix.profile,
		Inputs:      []string{env.fix.input},
	})
	wantStatus(t, rec, http.StatusOK)
	var resp prepareResponse
	decodeJSON(t, rec, &resp)
	if resp.Count != 1 || resp.Tasks[0].InputFile != env.fix.input {
		t.Errorf("subset preview = %+v, want only %s", resp, env.fix.input)
	}
}

// TestTaskAddWriteVpy is the other half of B4: the server writes the generated
// script, the profile keeps the three derived fields, and the queued task can
// run.
func TestTaskAddWriteVpy(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	// The fixture profile has no InputScript path that could survive a rewrite
	// (the wizard resolves it against the profile's directory), so it is given
	// one that does: the shipped script is the real template.
	profilePath, _ := exampleProfile(t, "demo.json")
	exampleInputs(t, profilePath)
	prof, err := profile.Load(profilePath)
	if err != nil {
		t.Fatalf("profile.Load() error = %v", err)
	}

	rec := env.do(http.MethodPost, APIPrefix+"/tasks", addTaskRequest{
		ProfilePath: profilePath,
		Inputs:      []string{prof.InputFiles[0]},
		WriteVpy:    true,
	})
	wantStatus(t, rec, http.StatusCreated)

	var resp taskResponse
	decodeJSON(t, rec, &resp)

	// The .vpy really landed on disk, with the text the wizard derived.
	want, err := wizard.Derive(prof, wizard.Options{ProjectFile: profilePath, ReducePath: true})
	if err != nil {
		t.Fatalf("wizard.Derive() error = %v", err)
	}
	var derived wizard.Task
	for _, task := range want.Tasks {
		if task.InputFile == resolveRelative(prof.InputFiles[0], filepath.Dir(profilePath)) {
			derived = task
		}
	}
	if derived.VpyFile == "" {
		t.Fatalf("the wizard did not derive the selected input: %+v", want.Tasks)
	}

	raw, err := os.ReadFile(derived.VpyFile)
	if err != nil {
		t.Fatalf("the generated vpy is missing: %v", err)
	}
	if string(raw) != derived.Script {
		t.Errorf("vpy content differs from the wizard's:\n got %q\nwant %q", raw, derived.Script)
	}
	if !strings.Contains(string(raw), `R"`+derived.InputFile+`"`) {
		t.Error("the generated vpy does not reference the source")
	}

	// The task the API stored carries the same three fields, which is what
	// makes it runnable.
	stored, ok := env.tasks.Task(resp.Task.ID)
	if !ok {
		t.Fatal("the task is not in the queue")
	}
	storedProf, ok := stored.Profile.(*profile.Profile)
	if !ok {
		t.Fatal("the queued task does not carry a typed profile")
	}
	if storedProf.InputScript != derived.VpyFile {
		t.Errorf("queued InputScript = %q, want %q", storedProf.InputScript, derived.VpyFile)
	}
	if storedProf.WorkingPathPrefix != derived.WorkingPathPrefix {
		t.Errorf("queued WorkingPathPrefix = %q, want %q", storedProf.WorkingPathPrefix, derived.WorkingPathPrefix)
	}
	if storedProf.OutputPathPrefix != derived.OutputPathPrefix {
		t.Errorf("queued OutputPathPrefix = %q, want %q", storedProf.OutputPathPrefix, derived.OutputPathPrefix)
	}

	// The input profile the request named is untouched: the derivation works on
	// the parsed value, and the file on disk is still the operator's.
	reloaded, err := profile.Load(profilePath)
	if err != nil {
		t.Fatalf("profile.Load() error = %v", err)
	}
	if reloaded.InputScript != prof.InputScript {
		t.Errorf("the profile file's InputScript changed: %q, want %q", reloaded.InputScript, prof.InputScript)
	}
	if reloaded.WorkingPathPrefix != "" || reloaded.OutputPathPrefix != "" {
		t.Errorf("the profile file gained path prefixes: %q / %q",
			reloaded.WorkingPathPrefix, reloaded.OutputPathPrefix)
	}
}

// TestTaskAddWriteVpyDoesNotOverrideExplicitFields pins that write_vpy is an
// addition, not a replacement: a caller that names a path keeps it.
func TestTaskAddWriteVpyDoesNotOverrideExplicitFields(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	explicitScript := filepath.Join(env.fix.dir, "explicit.vpy")
	writeFile(t, explicitScript, fixtureVpy)

	rec := env.do(http.MethodPost, APIPrefix+"/tasks", addTaskRequest{
		ProfilePath:       env.fix.profile,
		WriteVpy:          true,
		InputScript:       explicitScript,
		WorkingPathPrefix: filepath.Join(env.fix.dir, "mine", "work"),
	})
	wantStatus(t, rec, http.StatusCreated)

	var resp taskResponse
	decodeJSON(t, rec, &resp)
	stored, ok := env.tasks.Task(resp.Task.ID)
	if !ok {
		t.Fatal("the task is not in the queue")
	}
	storedProf, ok := stored.Profile.(*profile.Profile)
	if !ok {
		t.Fatal("the queued task does not carry a typed profile")
	}
	if storedProf.InputScript != explicitScript {
		t.Errorf("InputScript = %q, want the caller's %q", storedProf.InputScript, explicitScript)
	}
	if storedProf.WorkingPathPrefix != filepath.Join(env.fix.dir, "mine", "work") {
		t.Errorf("WorkingPathPrefix = %q, want the caller's", storedProf.WorkingPathPrefix)
	}
	// The field the caller did not name still comes from the derivation.
	if storedProf.OutputPathPrefix == "" {
		t.Error("OutputPathPrefix is empty, want the derived value")
	}
}

// TestTaskAddWriteVpyNeedsAProjectDirectory pins the error an inline profile
// gets without base_dir: there is no directory to derive from, so the request
// fails instead of writing into the process working directory.
func TestTaskAddWriteVpyNeedsAProjectDirectory(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	rec := env.do(http.MethodPost, APIPrefix+"/tasks", addTaskRequest{
		ProfileText: env.fix.profileText(),
		WriteVpy:    true,
	})
	wantStatus(t, rec, http.StatusBadRequest)
	info := errorOf(t, rec)
	if !strings.Contains(info.Summary+info.Detail, "工作目录") {
		t.Errorf("summary/detail = %q / %q, want it to name the missing root", info.Summary, info.Detail)
	}
	if env.tasks.GetTaskCount() != 0 {
		t.Error("a rejected task reached the queue")
	}
}

// TestTaskAddWriteVpyUsesReducePathFromTheConfig pins the switch the wizard
// reads from the settings, never from the request: with reducePath off a long
// source path keeps every level instead of being shortened.
func TestTaskAddWriteVpyUsesReducePathFromTheConfig(t *testing.T) {
	t.Parallel()

	// A source deep enough to be shortened: more than three path levels. It has
	// to exist, because validation runs before assembly and checks the inputs.
	root := filepath.VolumeName(t.TempDir()) + string(filepath.Separator)
	deep := filepath.Join(root, "A", "B", "C", "D", "00000.m2ts")
	if err := os.MkdirAll(filepath.Dir(deep), 0o700); err != nil {
		t.Skipf("cannot create %s: %v", filepath.Dir(deep), err)
	}
	writeFile(t, deep, "")
	t.Cleanup(func() { _ = os.Remove(deep) })

	cases := []struct {
		name       string
		reducePath bool
		// wantShortened is whether the derived prefix must carry the CRC level
		// the shortening produces.
		wantShortened bool
	}{
		{name: "shortened when the setting is on", reducePath: true, wantShortened: true},
		{name: "kept when the setting is off", reducePath: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t)
			env.config.cfg.ReducePath = tc.reducePath

			raw := strings.ReplaceAll(env.fix.profileText(), `"00001.m2ts"`, strconvQuote(deep))
			rec := env.do(http.MethodPost, APIPrefix+"/tasks", addTaskRequest{
				ProfileText: raw,
				BaseDir:     env.fix.dir,
				WriteVpy:    true,
			})
			wantStatus(t, rec, http.StatusCreated)

			var resp taskResponse
			decodeJSON(t, rec, &resp)
			stored, ok := env.tasks.Task(resp.Task.ID)
			if !ok {
				t.Fatal("the task is not in the queue")
			}
			prof, ok := stored.Profile.(*profile.Profile)
			if !ok {
				t.Fatal("the queued task does not carry a typed profile")
			}

			// The escaped drive level ("C_") is what the legacy expression
			// produced; the shortening replaces the levels between the drive and
			// the file with "<CRC>-D".
			want := filepath.Join(env.fix.dir, "C_", "A", "B", "C", "D", "00000.m2ts")
			if tc.wantShortened {
				if !strings.Contains(prof.WorkingPathPrefix, "-D") {
					t.Errorf("working prefix = %q, want the shortened form", prof.WorkingPathPrefix)
				}
				if prof.WorkingPathPrefix == want {
					t.Errorf("working prefix = %q, want it shortened", prof.WorkingPathPrefix)
				}
			} else if prof.WorkingPathPrefix != want {
				t.Errorf("working prefix = %q, want the unshortened %q", prof.WorkingPathPrefix, want)
			}

			logPath := filepath.Join(env.fix.dir, "output", "ReducePathMap.log")
			_, err := os.Stat(logPath)
			if tc.wantShortened && err != nil {
				t.Errorf("ReducePathMap.log is missing with reducePath on: %v", err)
			}
			if !tc.wantShortened && !os.IsNotExist(err) {
				t.Errorf("ReducePathMap.log exists with reducePath off (stat err = %v)", err)
			}
		})
	}
}

// TestTaskCancelRunning covers the running half of B3: the task really stops,
// the queue records the legacy terminal state, and the pool keeps running
// instead of being stopped.
func TestTaskCancelRunning(t *testing.T) {
	t.Parallel()
	srv, tm, pool, fix, blocking := blockingEnv(t)

	// Two tasks: the second keeps the queue non-empty, so the pool cannot
	// legitimately drain and IsRunning stays meaningful.
	first := addOne(t, srv, fix.profile, fix.input)
	secondInput := filepath.Join(fix.dir, "00002.m2ts")
	writeFile(t, secondInput, "")
	second := addOne(t, srv, fix.profile, secondInput)
	blocking.WaitEntered(t, first)

	rec := httptestPOST(t, srv, APIPrefix+"/tasks/"+first.String()+"/cancel", "")
	wantStatus(t, rec, http.StatusOK)

	var resp taskResponse
	decodeJSON(t, rec, &resp)
	if resp.Task.ID != first {
		t.Errorf("cancel returned task %s, want %s", resp.Task.ID, first)
	}

	// The worker writes the terminal state once the event stream closes, so
	// wait for it rather than reading the queue immediately.
	waitForStatus(t, tm, first, model.TaskError)
	if got := taskProgressOf(t, tm, first).Status.Status; got != "已终止" {
		t.Errorf("status = %q, want 已终止", got)
	}
	if !pool.IsRunning() {
		t.Error("cancelling one task stopped the pool")
	}
	if got := pool.GetBGWorkerCount(); got != 1 {
		t.Errorf("active workers = %d, want 1: the worker must stay", got)
	}
	// The worker moved on to the next task, which is the point of keeping it.
	blocking.WaitEntered(t, second)
	blocking.Release(second)
	waitForStatus(t, tm, second, model.TaskFinished)
}

// TestTaskCancelWaiting covers the waiting half: cancelling a task that has not
// started takes it out of the run without ever reaching the executor.
func TestTaskCancelWaiting(t *testing.T) {
	t.Parallel()

	submitted := make(chan model.TaskID, 4)
	tm, err := engine.New(engine.Options{})
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	fix := newFixture(t)
	exec := engine.ExecutorFuncs{
		CapabilitiesFunc: func(context.Context) (node.Capabilities, error) { return fix.capabilities(), nil },
		SubmitFunc: func(_ context.Context, task *model.Task) (<-chan model.StatusEvent, error) {
			submitted <- task.ID
			ch := make(chan model.StatusEvent)
			close(ch)
			return ch, nil
		},
	}
	// The pool is never started, so every task stays waiting.
	pool := engine.NewWorkerManager(exec, tm, platform.NewNumaWithCount(1))
	pool.AddWorker(1)

	srv, err := New(Options{Tasks: tm, Pool: pool, Exec: exec})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rec := httptestPOST(t, srv, APIPrefix+"/tasks", mustJSON(t, addTaskRequest{ProfilePath: fix.profile}))
	wantStatus(t, rec, http.StatusCreated)
	var added taskResponse
	decodeJSON(t, rec, &added)

	rec = httptestPOST(t, srv, APIPrefix+"/tasks/"+added.Task.ID.String()+"/cancel", "")
	wantStatus(t, rec, http.StatusOK)

	var resp taskResponse
	decodeJSON(t, rec, &resp)
	if resp.Task.Status.Progress != model.TaskError {
		t.Errorf("progress = %v, want ERROR", resp.Task.Status.Progress)
	}
	if resp.Task.Status.Status != "已终止" {
		t.Errorf("status = %q, want 已终止", resp.Task.Status.Status)
	}
	if got := tm.GetActiveTaskCount(); got != 0 {
		t.Errorf("active task count = %d, want 0", got)
	}

	// Starting the pool finds nothing runnable, so the cancelled task is not
	// executed.
	if !pool.Start() {
		t.Fatal("pool did not start")
	}
	select {
	case id := <-submitted:
		t.Fatalf("the cancelled task %s reached the executor", id)
	case <-time.After(100 * time.Millisecond):
	}
	pool.Stop()
}

// TestTaskCancelErrors covers 404 and 409.
func TestTaskCancelErrors(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	added := env.addTask(env.fix.profile, env.fix.input)

	// Unknown id: 404.
	rec := env.do(http.MethodPost, APIPrefix+"/tasks/11111111-2222-4333-8444-555555555555/cancel", nil)
	wantStatus(t, rec, http.StatusNotFound)
	errorOf(t, rec)

	// Malformed id: 400, like every other endpoint's {id} route.
	rec = env.do(http.MethodPost, APIPrefix+"/tasks/bogus/cancel", nil)
	wantStatus(t, rec, http.StatusBadRequest)
	errorOf(t, rec)

	// A settled task: 409, and the state is left alone.
	for _, state := range []model.TaskProgress{model.TaskFinished, model.TaskError} {
		if err := env.tasks.Update(added.ID, func(t *model.Task) {
			t.Status.Progress = state
			t.Status.Status = "原有状态"
		}); err != nil {
			t.Fatalf("mark %v: %v", state, err)
		}
		rec = env.do(http.MethodPost, APIPrefix+"/tasks/"+added.ID.String()+"/cancel", nil)
		wantStatus(t, rec, http.StatusConflict)
		info := errorOf(t, rec)
		if !strings.Contains(info.Summary, "无法取消") {
			t.Errorf("summary = %q, want it to explain the conflict", info.Summary)
		}
		task, _ := env.tasks.Task(added.ID)
		if task.Status.Progress != state || task.Status.Status != "原有状态" {
			t.Errorf("a rejected cancel changed the task: %v / %q", task.Status.Progress, task.Status.Status)
		}
	}

	// Wrong method: 405 with an Allow header.
	rec = env.do(http.MethodGet, APIPrefix+"/tasks/"+added.ID.String()+"/cancel", nil)
	wantStatus(t, rec, http.StatusMethodNotAllowed)
	if rec.Header().Get("Allow") == "" {
		t.Error("405 without an Allow header")
	}
}

// TestTaskCancelDoesNotStopThePool pins the difference from POST /pool/stop:
// the pool keeps running and a task added afterwards still starts.
func TestTaskCancelDoesNotStopThePool(t *testing.T) {
	t.Parallel()
	srv, tm, pool, fix, blocking := blockingEnv(t)

	first := addOne(t, srv, fix.profile, fix.input)
	// A second task keeps the queue non-empty, so the pool cannot drain.
	other := filepath.Join(fix.dir, "00002.m2ts")
	writeFile(t, other, "")
	second := addOne(t, srv, fix.profile, other)
	blocking.WaitEntered(t, first)

	rec := httptestPOST(t, srv, APIPrefix+"/tasks/"+first.String()+"/cancel", "")
	wantStatus(t, rec, http.StatusOK)
	waitForStatus(t, tm, first, model.TaskError)

	if !pool.IsRunning() {
		t.Fatal("cancelling a task stopped the pool")
	}
	if got := pool.GetBGWorkerCount(); got != 1 {
		t.Errorf("active workers = %d, want 1: the worker must stay", got)
	}

	// The task behind the cancelled one still runs, which is what the Web UI
	// needs: cancelling is not stopping.
	blocking.WaitEntered(t, second)
	blocking.Release(second)
	waitForStatus(t, tm, second, model.TaskFinished)
}

// addOne creates a task through the API and returns its id.
func addOne(t *testing.T, srv *Server, profilePath, input string) model.TaskID {
	t.Helper()
	rec := httptestPOST(t, srv, APIPrefix+"/tasks",
		mustJSON(t, addTaskRequest{ProfilePath: profilePath, Inputs: []string{input}}))
	wantStatus(t, rec, http.StatusCreated)
	var resp taskResponse
	decodeJSON(t, rec, &resp)
	return resp.Task.ID
}

// blockingEnv builds a server whose executor holds each task until the test
// releases it, so tasks stay in the running state and the queue stays visible.
// The fixture's default executor closes its event channel immediately, which is
// fine for the other tests but cannot express "a task is still running".
func blockingEnv(t *testing.T) (*Server, *engine.TaskManager, *engine.WorkerManager, *fixture, *blockingExecutor) {
	t.Helper()

	tm, err := engine.New(engine.Options{})
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	fix := newFixture(t)
	blocking := newBlockingExecutor(fix.capabilities())
	pool := engine.NewWorkerManager(blocking, tm, platform.NewNumaWithCount(1))
	pool.AddWorker(1)
	if !pool.Start() {
		t.Fatal("pool did not start")
	}
	t.Cleanup(pool.Stop)

	srv, err := New(Options{Tasks: tm, Pool: pool, Exec: blocking})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return srv, tm, pool, fix, blocking
}

// blockingExecutor is a controllable Executor: a submitted task reports one
// running event and then waits for the context or for Release. Every exit path
// sends a terminal event, like LocalExecutor does, so the queue always settles.
type blockingExecutor struct {
	caps node.Capabilities

	mu      sync.Mutex
	running map[model.TaskID]chan struct{}
	entered map[model.TaskID]chan struct{}
}

// newBlockingExecutor returns an executor that reports the given capabilities.
func newBlockingExecutor(caps node.Capabilities) *blockingExecutor {
	return &blockingExecutor{
		caps:    caps,
		running: map[model.TaskID]chan struct{}{},
		entered: map[model.TaskID]chan struct{}{},
	}
}

// Capabilities implements engine.Executor.
func (e *blockingExecutor) Capabilities(context.Context) (node.Capabilities, error) {
	return e.caps, nil
}

// Submit implements engine.Executor.
func (e *blockingExecutor) Submit(ctx context.Context, t *model.Task) (<-chan model.StatusEvent, error) {
	e.mu.Lock()
	if _, dup := e.running[t.ID]; dup {
		e.mu.Unlock()
		return nil, errors.New("blocking executor: task is already running")
	}
	release := make(chan struct{})
	entered := make(chan struct{})
	e.running[t.ID] = release
	e.entered[t.ID] = entered
	e.mu.Unlock()

	ch := make(chan model.StatusEvent)
	go func() {
		defer close(ch)
		ch <- model.StatusEvent{TaskID: t.ID, Progress: model.TaskRunning, Step: "x265", Percent: 10}
		close(entered)
		select {
		case <-release:
			ch <- model.StatusEvent{TaskID: t.ID, Progress: model.TaskFinished, Percent: 100}
		case <-ctx.Done():
			ch <- model.StatusEvent{TaskID: t.ID, Progress: model.TaskError, Percent: -1,
				Error: &model.ErrorInfo{Summary: "任务已取消"}}
		}
		e.mu.Lock()
		delete(e.running, t.ID)
		e.mu.Unlock()
	}()
	return ch, nil
}

// Cancel implements engine.Executor.
func (e *blockingExecutor) Cancel(_ context.Context, id model.TaskID) error {
	// The context handed to Submit is cancelled separately by the pool, which
	// is what the executor observes; nothing to do here.
	_ = id
	return nil
}

// WaitEntered blocks until the executor is running the task.
func (e *blockingExecutor) WaitEntered(t *testing.T, id model.TaskID) {
	t.Helper()
	e.mu.Lock()
	entered := e.entered[id]
	e.mu.Unlock()
	if entered == nil {
		t.Fatalf("task %s was never submitted", id)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatalf("the executor never entered task %s", id)
	}
}

// Release lets a task finish successfully.
func (e *blockingExecutor) Release(id model.TaskID) {
	e.mu.Lock()
	release := e.running[id]
	e.mu.Unlock()
	if release != nil {
		close(release)
	}
}

// TestPoolStopSettlesWaitingTasks pins the pool-level half of B3: a stop leaves
// nothing to run, so a later start cannot resurrect a task the operator
// cancelled by pressing stop.
func TestPoolStopSettlesWaitingTasks(t *testing.T) {
	t.Parallel()
	srv, tm, _, fix, blocking := blockingEnv(t)

	var ids = make([]model.TaskID, 0, 3)
	for i := range 3 {
		input := filepath.Join(fix.dir, "wait"+string(rune('a'+i))+".m2ts")
		writeFile(t, input, "")
		rec := httptestPOST(t, srv, APIPrefix+"/tasks",
			mustJSON(t, addTaskRequest{ProfilePath: fix.profile, Inputs: []string{input}}))
		wantStatus(t, rec, http.StatusCreated)
		var resp taskResponse
		decodeJSON(t, rec, &resp)
		ids = append(ids, resp.Task.ID)
	}
	blocking.WaitEntered(t, ids[0])
	if got := tm.GetActiveTaskCount(); got != 2 {
		t.Fatalf("active task count = %d, want 2 waiting behind the running one", got)
	}

	rec := httptestPOST(t, srv, APIPrefix+"/pool/stop?timeout_seconds=5", "")
	wantStatus(t, rec, http.StatusOK)

	if got := tm.GetActiveTaskCount(); got != 0 {
		t.Errorf("active task count = %d after a pool stop, want 0", got)
	}
	for _, id := range ids {
		task := taskProgressOf(t, tm, id)
		if task.Status.Progress != model.TaskError {
			t.Errorf("task %s progress = %v, want ERROR", id, task.Status.Progress)
		}
		if task.Status.Status != "已终止" {
			t.Errorf("task %s status = %q, want 已终止", id, task.Status.Status)
		}
	}
}

// TestCancelRoutePrecedence covers the routing: /tasks/prepare and
// /tasks/{id}/cancel are distinct patterns from /tasks/{id}, and each one is
// answered by its own handler.
func TestCancelRoutePrecedence(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	// A literal "prepare" must not be parsed as a task id.
	rec := env.do(http.MethodGet, APIPrefix+"/tasks/prepare", nil)
	wantStatus(t, rec, http.StatusMethodNotAllowed)
	if allow := rec.Header().Get("Allow"); !strings.Contains(allow, http.MethodPost) {
		t.Errorf("Allow = %q, want it to contain POST", allow)
	}

	// /tasks/{id}/cancel is not /tasks/{id}: the trailing segment routes.
	added := env.addTask(env.fix.profile, env.fix.input)
	rec = env.do(http.MethodGet, APIPrefix+"/tasks/"+added.ID.String()+"/cancel", nil)
	wantStatus(t, rec, http.StatusMethodNotAllowed)
	rec = env.do(http.MethodGet, APIPrefix+"/tasks/"+added.ID.String()+"/nope", nil)
	wantStatus(t, rec, http.StatusNotFound)
}

// httptestPOST runs a POST against a specific server, for the tests that build
// their own pool.
func httptestPOST(t *testing.T, srv *Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	return rec
}

// waitForStatus polls until the queue reports the task in the given state.
func waitForStatus(t *testing.T, tm *engine.TaskManager, id model.TaskID, want model.TaskProgress) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if task, ok := tm.Task(id); ok && task.Status.Progress == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("task %s never reached %v", id, want)
}

// taskProgressOf reads one task's state, failing when it is gone.
func taskProgressOf(t *testing.T, tm *engine.TaskManager, id model.TaskID) model.Task {
	t.Helper()
	task, ok := tm.Task(id)
	if !ok {
		t.Fatalf("task %s is not in the queue", id)
	}
	return task
}
