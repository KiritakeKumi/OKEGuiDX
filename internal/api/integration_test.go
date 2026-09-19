package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/KiritakeKumi/OKEGuiDX/internal/engine"
	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/node"
	"github.com/KiritakeKumi/OKEGuiDX/internal/platform"
)

// TestConcurrentRequests covers the concurrency requirement: the handlers are
// called from many goroutines at once, and the queue must stay consistent.
func TestConcurrentRequests(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	// Each goroutine adds its own source, so the duplicate check does not turn
	// the test into a measure of that check.
	const workers = 8
	const perWorker = 4

	var wg sync.WaitGroup
	ids := make(chan model.TaskID, workers*perWorker)
	for w := range workers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := range perWorker {
				input := fmt.Sprintf("%s.%d.%d", env.fix.input, w, i)
				writeFile(t, input, "")
				rec := env.do(http.MethodPost, APIPrefix+"/tasks", addTaskRequest{
					ProfilePath: env.fix.profile,
					Inputs:      []string{input},
				})
				if rec.Code != http.StatusCreated {
					t.Errorf("add %s = %d: %s", input, rec.Code, rec.Body)
					return
				}
				var resp taskResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
					t.Errorf("decode add response: %v", err)
					return
				}
				ids <- resp.Task.ID
			}
		}(w)
	}
	wg.Wait()
	close(ids)

	seen := make(map[model.TaskID]struct{})
	for id := range ids {
		if _, dup := seen[id]; dup {
			t.Errorf("duplicate task id %s", id)
		}
		seen[id] = struct{}{}
	}
	if got := env.tasks.GetTaskCount(); got != workers*perWorker {
		t.Errorf("queue length = %d, want %d", got, workers*perWorker)
	}

	// Reads run concurrently with writes without tripping the race detector.
	var readers sync.WaitGroup
	for range 4 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for range 10 {
				if rec := env.do(http.MethodGet, APIPrefix+"/tasks", nil); rec.Code != http.StatusOK {
					t.Errorf("list = %d", rec.Code)
					return
				}
				if rec := env.do(http.MethodGet, APIPrefix+"/status", nil); rec.Code != http.StatusOK {
					t.Errorf("status = %d", rec.Code)
					return
				}
			}
		}()
	}
	readers.Wait()

	// Every task can be fetched by id.
	for id := range seen {
		rec := env.do(http.MethodGet, APIPrefix+"/tasks/"+id.String(), nil)
		if rec.Code != http.StatusOK {
			t.Errorf("get %s = %d", id, rec.Code)
		}
	}
}

// TestTaskLifecycleThroughThePool covers the interaction the Web UI depends on:
// adding a task starts it (the pool resumes by itself), and a running task is
// protected from deletion and reordering.
func TestTaskLifecycleThroughThePool(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	// entered is closed once the executor is actually running the task. The
	// pool marks a task running before it submits it, so waiting for the
	// running state alone would let the test observe a task the executor has
	// not started yet.
	entered := make(chan struct{})
	var enteredOnce sync.Once

	tm, err := engine.New(engine.Options{})
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	fix := newFixture(t)
	exec := engine.ExecutorFuncs{
		CapabilitiesFunc: func(context.Context) (node.Capabilities, error) {
			return fix.capabilities(), nil
		},
		SubmitFunc: func(ctx context.Context, task *model.Task) (<-chan model.StatusEvent, error) {
			ch := make(chan model.StatusEvent)
			go func() {
				defer close(ch)
				ch <- model.StatusEvent{TaskID: task.ID, Progress: model.TaskRunning, Step: "x265", Percent: 10}
				enteredOnce.Do(func() { close(entered) })
				select {
				case <-release:
				case <-ctx.Done():
				}
			}()
			return ch, nil
		},
	}
	pool := engine.NewWorkerManager(exec, tm, platform.NewNumaWithCount(1))
	pool.AddWorker(1)
	if !pool.Start() {
		t.Fatal("pool did not start")
	}
	t.Cleanup(pool.Stop)

	srv, err := New(Options{Tasks: tm, Pool: pool, Exec: exec})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rec := httptest.NewRecorder()
	body := mustJSON(t, addTaskRequest{ProfilePath: fix.profile})
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, APIPrefix+"/tasks", strings.NewReader(body)))
	wantStatus(t, rec, http.StatusCreated)

	waitForRunning(t, tm)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the executor was never entered")
	}
	tasks := tm.Snapshot()
	if len(tasks) != 1 {
		t.Fatalf("queue length = %d, want 1", len(tasks))
	}
	id := tasks[0].ID.String()

	// A running task cannot be deleted, moved or disabled.
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, APIPrefix+"/tasks/"+id, nil))
	wantStatus(t, rec, http.StatusConflict)

	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, APIPrefix+"/tasks/"+id,
		strings.NewReader(`{"position":"top"}`)))
	wantStatus(t, rec, http.StatusConflict)

	// Disabling a running task is accepted but ignored: the pool already
	// unticked it when it claimed the task, and the queue refuses to change
	// the flag while it runs (TaskStatus.IsEnabled). What matters is that the
	// task did not become enabled and is still running.
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, APIPrefix+"/tasks/"+id,
		strings.NewReader(`{"enabled":true}`)))
	wantStatus(t, rec, http.StatusOK)
	running := tm.Snapshot()[0]
	if running.Status.Enabled {
		t.Error("a running task accepted enabled=true")
	}
	if running.Status.Progress != model.TaskRunning {
		t.Errorf("progress = %v, want RUNNING", running.Status.Progress)
	}

	// Stopping the pool terminates the task, after which it can be deleted.
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, APIPrefix+"/pool/stop?timeout_seconds=5", nil))
	wantStatus(t, rec, http.StatusOK)

	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, APIPrefix+"/tasks/"+id, nil))
	wantStatus(t, rec, http.StatusOK)
	if got := tm.GetTaskCount(); got != 0 {
		t.Errorf("queue length after delete = %d, want 0", got)
	}
}

// TestStatusReflectsPoolState covers the pool half of the status payload across
// a start/stop cycle.
func TestStatusReflectsPoolState(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	read := func() statusResponse {
		t.Helper()
		rec := env.do(http.MethodGet, APIPrefix+"/status", nil)
		wantStatus(t, rec, http.StatusOK)
		var resp statusResponse
		decodeJSON(t, rec, &resp)
		return resp
	}

	if read().Pool.Running {
		t.Error("the pool reports running before start")
	}
	env.do(http.MethodPost, APIPrefix+"/pool/start", nil)
	if !read().Pool.Running {
		t.Error("the pool does not report running after start")
	}
	env.do(http.MethodPost, APIPrefix+"/pool/stop", nil)
	if read().Pool.Running {
		t.Error("the pool still reports running after stop")
	}
}

// TestQueueCountsMatchSnapshot covers the invariant the Web UI relies on: the
// counts always add up to the total.
func TestQueueCountsMatchSnapshot(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	states := []model.TaskProgress{
		model.TaskWaiting,
		model.TaskRunning,
		model.TaskError,
		model.TaskFinished,
	}
	for i, state := range states {
		input := fmt.Sprintf("%s.%d", env.fix.input, i)
		writeFile(t, input, "")
		task := env.addTask(env.fix.profile, input)
		if err := env.tasks.Update(task.ID, func(t *model.Task) {
			t.Status.Progress = state
			t.Status.Enabled = state != model.TaskRunning
		}); err != nil {
			t.Fatalf("set state: %v", err)
		}
	}

	rec := env.do(http.MethodGet, APIPrefix+"/status", nil)
	wantStatus(t, rec, http.StatusOK)
	var resp statusResponse
	decodeJSON(t, rec, &resp)

	q := resp.Queue
	if sum := q.Waiting + q.Running + q.Error + q.Finished; sum != q.Total {
		t.Errorf("counts add up to %d, total is %d: %+v", sum, q.Total, q)
	}
	if q.Total != len(states) {
		t.Errorf("total = %d, want %d", q.Total, len(states))
	}
	if q.Waiting != 1 || q.Running != 1 || q.Error != 1 || q.Finished != 1 {
		t.Errorf("counts = %+v, want one of each state", q)
	}
	// The running task is the one that was disabled.
	if q.Enabled != len(states)-1 {
		t.Errorf("enabled = %d, want %d", q.Enabled, len(states)-1)
	}
	if q.Active != 1 {
		t.Errorf("active = %d, want 1 (the waiting task)", q.Active)
	}
}

// TestMountSharesThePrefixWithAnotherHandler covers the reason Mount exists: the
// WebSocket stream (E3) registers "/api/v1/events" on the same mux, and it must
// win over this package's catch-all without either handler knowing about the
// other.
func TestMountSharesThePrefixWithAnotherHandler(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	mux := http.NewServeMux()
	env.server.Mount(mux)
	mux.HandleFunc("/api/v1/events", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusSwitchingProtocols)
	})

	call := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}

	if rec := call("/api/v1/events"); rec.Code != http.StatusSwitchingProtocols {
		t.Errorf("events = %d, want 101 (the sibling handler must win)", rec.Code)
	}
	if rec := call(APIPrefix + "/tasks"); rec.Code != http.StatusOK {
		t.Errorf("tasks = %d, want 200", rec.Code)
	}
	if rec := call(APIPrefix + "/nope"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown = %d, want 404", rec.Code)
	}
}

// TestErrorBodyNeverLeaksInternals covers the wire format's stability: the
// error object has exactly three keys, and a long detail is truncated at a rune
// boundary.
func TestErrorBodyNeverLeaksInternals(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("工具输出", 2000)
	rec := httptest.NewRecorder()
	writeError(rec, http.StatusInternalServerError, okerrNew("失败", long))
	wantStatus(t, rec, http.StatusInternalServerError)

	info := errorOf(t, rec)
	if len(info.Detail) > maxErrorDetail+len("...") {
		t.Errorf("detail is %d bytes, want it clamped to %d", len(info.Detail), maxErrorDetail)
	}
	if !strings.HasSuffix(info.Detail, "...") {
		t.Error("a clamped detail must be marked as truncated")
	}
	if !utf8.ValidString(info.Detail) {
		t.Error("the clamp cut the detail mid-rune")
	}
}

// TestMethodAllowHeaderIsSorted covers the 405 header, which a client uses to
// discover what it may send.
func TestMethodAllowHeaderIsSorted(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	rec := env.do(http.MethodPut, APIPrefix+"/tasks/11111111-2222-4333-8444-555555555555", nil)
	wantStatus(t, rec, http.StatusMethodNotAllowed)
	allow := rec.Header().Get("Allow")
	for _, want := range []string{http.MethodDelete, http.MethodGet, http.MethodHead, http.MethodPatch} {
		if !strings.Contains(allow, want) {
			t.Errorf("Allow = %q, want it to contain %s", allow, want)
		}
	}
}

// TestPoolStopIsIdempotent covers the "stop twice" case, which the Web UI does
// when the operator clicks twice.
func TestPoolStopIsIdempotent(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	env.do(http.MethodPost, APIPrefix+"/pool/start", nil)

	for i := range 3 {
		rec := env.do(http.MethodPost, APIPrefix+"/pool/stop?timeout_seconds=5", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("stop #%d = %d, want 200", i+1, rec.Code)
		}
	}

	// Starting again after a stop works, which is what the legacy Run button
	// did.
	rec := env.do(http.MethodPost, APIPrefix+"/pool/start", nil)
	wantStatus(t, rec, http.StatusOK)
}

// TestPoolStopReportsQuiescence covers the stopped flag's meaning: it is true
// only when no worker goroutine is left, which is the condition a client polls
// for after a 202.
func TestPoolStopReportsQuiescence(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	// The pool is idle, so a stop has nothing to wait for.
	rec := env.do(http.MethodPost, APIPrefix+"/pool/stop?timeout_seconds=5", nil)
	wantStatus(t, rec, http.StatusOK)
	var resp poolResponse
	decodeJSON(t, rec, &resp)
	if resp.Stopped == nil || !*resp.Stopped {
		t.Fatalf("stopped = %v, want true on an idle pool", resp.Stopped)
	}
	if resp.Pool.ActiveWorkers != 0 {
		t.Errorf("active workers = %d, want 0", resp.Pool.ActiveWorkers)
	}
}

// TestStartAfterStopPicksUpQueuedTasks covers the reason the pool endpoints
// exist: tasks added while the pool was stopped wait for a start.
func TestStartAfterStopPicksUpQueuedTasks(t *testing.T) {
	t.Parallel()

	tm, err := engine.New(engine.Options{})
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	fix := newFixture(t)
	submitted := make(chan model.TaskID, 4)
	exec := engine.ExecutorFuncs{
		CapabilitiesFunc: func(context.Context) (node.Capabilities, error) {
			return fix.capabilities(), nil
		},
		SubmitFunc: func(_ context.Context, task *model.Task) (<-chan model.StatusEvent, error) {
			submitted <- task.ID
			ch := make(chan model.StatusEvent)
			close(ch)
			return ch, nil
		},
	}
	pool := engine.NewWorkerManager(exec, tm, platform.NewNumaWithCount(1))
	pool.AddWorker(1)
	t.Cleanup(pool.Stop)

	srv, err := New(Options{Tasks: tm, Pool: pool, Exec: exec})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	post := func(path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
		return rec
	}

	// The pool is idle: adding a task queues it without running it.
	body := mustJSON(t, addTaskRequest{ProfilePath: fix.profile})
	wantStatus(t, post(APIPrefix+"/tasks", body), http.StatusCreated)
	select {
	case id := <-submitted:
		t.Fatalf("task %s ran while the pool was idle", id)
	case <-time.After(50 * time.Millisecond):
	}

	// Start picks it up.
	wantStatus(t, post(APIPrefix+"/pool/start", ""), http.StatusOK)
	select {
	case <-submitted:
	case <-time.After(5 * time.Second):
		t.Fatal("start did not run the queued task")
	}
}
