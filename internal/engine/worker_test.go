package engine

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/node"
	"github.com/KiritakeKumi/OKEGuiDX/internal/platform"
)

// testTimeout bounds every wait in this file. It is generous because the race
// detector slows the goroutines down considerably.
const testTimeout = 10 * time.Second

// fakeOutcome is how a fake task terminates.
type fakeOutcome int

const (
	fakeFinished fakeOutcome = iota
	fakeFailed
)

// fakeRun is one task the fake executor accepted. The test decides when it
// terminates, so the order of events is deterministic.
type fakeRun struct {
	events chan model.StatusEvent
	// outcome is buffered, so a completion never blocks the test even after the
	// task was cancelled.
	outcome chan fakeOutcome
	once    sync.Once
}

// complete terminates the task once; later calls are ignored.
func (r *fakeRun) complete(out fakeOutcome) {
	r.once.Do(func() { r.outcome <- out })
}

// fakeExecutor is a controllable Executor: it records what it was asked to run
// and never starts a process. It exists so the pool's scheduling can be tested
// without touching the pipeline (or the filesystem).
type fakeExecutor struct {
	mu        sync.Mutex
	runs      map[model.TaskID]*fakeRun
	order     []model.TaskID
	nodes     map[model.TaskID]int
	active    int
	maxActive int
	cancels   int
	submitErr error

	// onSubmit and onCancel run before the call takes effect, outside any pool
	// lock. A test uses them to call back into the manager, which turns a lock
	// held across the Executor boundary into a deadlock instead of a silent
	// correctness bug.
	onSubmit func(*model.Task)
	onCancel func(model.TaskID)
}

func newFakeExecutor() *fakeExecutor {
	return &fakeExecutor{
		runs:  make(map[model.TaskID]*fakeRun),
		nodes: make(map[model.TaskID]int),
	}
}

// Capabilities implements Executor.
func (f *fakeExecutor) Capabilities(context.Context) (node.Capabilities, error) {
	return node.NewCapabilities(node.RoleStandalone), nil
}

// Submit implements Executor. The returned channel stays open until the test
// finishes or fails the task, or until ctx is cancelled, exactly like a real
// run.
func (f *fakeExecutor) Submit(ctx context.Context, t *model.Task) (<-chan model.StatusEvent, error) {
	if f.onSubmit != nil {
		f.onSubmit(t)
	}

	f.mu.Lock()
	if f.submitErr != nil {
		err := f.submitErr
		f.mu.Unlock()
		return nil, err
	}
	if _, dup := f.runs[t.ID]; dup {
		f.mu.Unlock()
		return nil, errors.New("fake executor: task is already running: " + t.ID.String())
	}
	run := &fakeRun{events: make(chan model.StatusEvent, 16), outcome: make(chan fakeOutcome, 1)}
	f.runs[t.ID] = run
	f.order = append(f.order, t.ID)
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	if n, ok := NUMANodeFrom(ctx); ok {
		f.nodes[t.ID] = n
	}
	f.mu.Unlock()

	go func() {
		run.events <- model.StatusEvent{TaskID: t.ID, Progress: model.TaskRunning, Step: "x265", Percent: 42, Speed: "10.00 fps"}

		select {
		case out := <-run.outcome:
			if out == fakeFailed {
				run.events <- model.StatusEvent{TaskID: t.ID, Progress: model.TaskError, Percent: -1,
					Error: &model.ErrorInfo{Summary: "x265出错"}}
			} else {
				run.events <- model.StatusEvent{TaskID: t.ID, Progress: model.TaskFinished, Percent: 100}
			}
		case <-ctx.Done():
			run.events <- model.StatusEvent{TaskID: t.ID, Progress: model.TaskError, Percent: -1,
				Error: &model.ErrorInfo{Summary: "任务已取消"}}
		}

		f.mu.Lock()
		delete(f.runs, t.ID)
		f.active--
		f.mu.Unlock()
		close(run.events)
	}()
	return run.events, nil
}

// Cancel implements Executor.
func (f *fakeExecutor) Cancel(_ context.Context, id model.TaskID) error {
	if f.onCancel != nil {
		f.onCancel(id)
	}
	f.mu.Lock()
	f.cancels++
	f.mu.Unlock()
	return nil
}

// Finish terminates one task successfully.
func (f *fakeExecutor) Finish(id model.TaskID) {
	if run := f.run(id); run != nil {
		run.complete(fakeFinished)
	}
}

// Fail terminates one task with the standard error.
func (f *fakeExecutor) Fail(id model.TaskID) {
	if run := f.run(id); run != nil {
		run.complete(fakeFailed)
	}
}

func (f *fakeExecutor) run(id model.TaskID) *fakeRun {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runs[id]
}

// FinishAll terminates every task that is currently running. A task submitted
// afterwards is not affected, which is what lets a test drive a pool through
// several rounds of work.
func (f *fakeExecutor) FinishAll() {
	f.mu.Lock()
	ids := make([]model.TaskID, 0, len(f.runs))
	for id := range f.runs {
		ids = append(ids, id)
	}
	f.mu.Unlock()
	for _, id := range ids {
		f.Finish(id)
	}
}

func (f *fakeExecutor) submittedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.order)
}

func (f *fakeExecutor) maxActiveCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maxActive
}

func (f *fakeExecutor) cancelCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cancels
}

// nodeOf returns the NUMA node the worker that ran the task was given.
func (f *fakeExecutor) nodeOf(id model.TaskID) (int, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, ok := f.nodes[id]
	return n, ok
}

// submittedTwice reports whether any task was handed to the executor twice,
// which would mean two workers claimed the same queue entry.
func (f *fakeExecutor) submittedTwice() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	seen := make(map[model.TaskID]struct{}, len(f.order))
	for _, id := range f.order {
		if _, dup := seen[id]; dup {
			return true
		}
		seen[id] = struct{}{}
	}
	return false
}

// waitFor polls until cond holds. It is the only synchronisation the tests use
// besides the pool's own API, because the pool deliberately exposes no
// completion future.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(testTimeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// newFakePool builds a pool with workerCount permanent workers over a fake
// executor and an in-memory queue.
func newFakePool(t *testing.T, workerCount int) (*WorkerManager, *TaskManager, *fakeExecutor) {
	t.Helper()
	tm := newTestManager(t, Options{})
	fake := newFakeExecutor()
	wm := NewWorkerManager(fake, tm, platform.NewNumaWithCount(max(workerCount, 1)))
	for i := 1; i <= workerCount; i++ {
		wm.AddWorker(i)
	}
	return wm, tm, fake
}

// addTasks enqueues n tasks with distinct inputs and returns their ids in queue
// order. Adding through the pool means each call also runs the worker sweep.
func addTasks(t *testing.T, wm *WorkerManager, n int) []model.TaskID {
	t.Helper()
	ids := make([]model.TaskID, 0, n)
	for i := range n {
		task := &model.Task{
			ID:     model.NewTaskID(),
			Name:   "task-" + string(rune('a'+i)),
			Inputs: []model.FileRef{model.NewFileRef("/media/ep" + string(rune('a'+i)) + ".m2ts")},
		}
		if _, err := wm.AddTask(task, "/cfg/project.json"); err != nil {
			t.Fatalf("AddTask(%d) error = %v", i, err)
		}
		ids = append(ids, task.ID)
	}
	return ids
}

// taskProgress reads one task's lifecycle state.
func taskProgress(t *testing.T, tm *TaskManager, id model.TaskID) model.Task {
	t.Helper()
	task, ok := tm.Task(id)
	if !ok {
		t.Fatalf("task %s is not in the queue", id)
	}
	return task
}

// allFinished reports whether every id finished.
func allFinished(tm *TaskManager, ids []model.TaskID) bool {
	for _, id := range ids {
		task, ok := tm.Task(id)
		if !ok || task.Status.Progress != model.TaskFinished {
			return false
		}
	}
	return true
}

// settled reports whether no task is waiting or running any more.
func settled(tm *TaskManager) bool {
	return tm.GetActiveTaskCount() == 0 && len(tm.GetRunningTasks()) == 0
}

// workerOfTask returns the worker name the pool recorded for a task.
func workerOfTask(tm *TaskManager, id model.TaskID) string {
	task, ok := tm.Task(id)
	if !ok {
		return ""
	}
	return task.Status.WorkerName
}

// Compile-time proof that the fake honours the same boundary as LocalExecutor.
var _ Executor = (*fakeExecutor)(nil)

func TestWorkerManagerStartRequiresAWorker(t *testing.T) {
	t.Parallel()
	tm := newTestManager(t, Options{})
	wm := NewWorkerManager(newFakeExecutor(), tm, platform.NewNumaWithCount(1))

	if wm.state != poolIdle {
		t.Errorf("a fresh pool state = %v, want IDLE", wm.state)
	}
	if wm.IsRunning() {
		t.Error("IsRunning() = true before Start")
	}
	if wm.Start() {
		t.Error("Start() = true without any worker, want false")
	}
	if wm.IsRunning() {
		t.Error("IsRunning() = true after a refused Start")
	}
	if !wm.AddWorker(1) {
		t.Fatal("AddWorker(1) = false")
	}
	if !wm.Start() {
		t.Error("Start() = false with one worker, want true")
	}
	if !wm.IsRunning() {
		t.Error("IsRunning() = false right after Start")
	}
	wm.Stop()
}

func TestTryStartNewWorkerScheduling(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		workers     int
		running     bool
		tasks       int
		wantStarted bool
		wantSubmits int
	}{
		{name: "no waiting task starts nothing", workers: 2, running: true, tasks: 0, wantStarted: false},
		{name: "idle pool starts nothing", workers: 2, running: false, tasks: 2, wantStarted: false},
		{name: "one task one worker", workers: 1, running: true, tasks: 1, wantStarted: true, wantSubmits: 1},
		{name: "fewer workers than tasks", workers: 2, running: true, tasks: 3, wantStarted: true, wantSubmits: 2},
		{name: "more workers than tasks", workers: 3, running: true, tasks: 1, wantStarted: true, wantSubmits: 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tm := newTestManager(t, Options{})
			fake := newFakeExecutor()
			wm := NewWorkerManager(fake, tm, platform.NewNumaWithCount(max(tc.workers, 1)))
			for i := 1; i <= tc.workers; i++ {
				wm.AddWorker(i)
			}
			if tc.running && !wm.Start() {
				t.Fatal("Start() = false")
			}
			// Tasks are added through the manager rather than the pool so that
			// the sweep under test is the only one that can start a worker.
			for i := range tc.tasks {
				addTestTask(t, tm, "ep", "/media/ep"+string(rune('a'+i))+".m2ts")
			}

			if got := wm.TryStartNewWorker(); got != tc.wantStarted {
				t.Errorf("TryStartNewWorker() = %v, want %v", got, tc.wantStarted)
			}
			waitFor(t, "the expected submissions", func() bool {
				return fake.submittedCount() == tc.wantSubmits
			})
			if got := wm.GetBGWorkerCount(); got != tc.wantSubmits {
				t.Errorf("GetBGWorkerCount() = %d, want %d", got, tc.wantSubmits)
			}
			if got := wm.GetWorkerCount(); got != tc.workers {
				t.Errorf("GetWorkerCount() = %d, want %d", got, tc.workers)
			}
			fake.FinishAll()
		})
	}
}

func TestTryStartNewWorkerCountsWaitingTasksOnly(t *testing.T) {
	t.Parallel()
	tm := newTestManager(t, Options{})
	fake := newFakeExecutor()
	wm := NewWorkerManager(fake, tm, platform.NewNumaWithCount(3))
	for i := 1; i <= 3; i++ {
		wm.AddWorker(i)
	}
	if !wm.Start() {
		t.Fatal("Start() = false")
	}
	// One finished and one disabled task must not occupy a worker.
	ids := buildQueue(t, tm, []taskState{
		{name: "done", progress: model.TaskFinished, enabled: false, input: "/media/done.m2ts"},
		{name: "off", progress: model.TaskWaiting, enabled: false, input: "/media/off.m2ts"},
		{name: "waiting", progress: model.TaskWaiting, enabled: true, input: "/media/waiting.m2ts"},
	})
	if !wm.TryStartNewWorker() {
		t.Fatal("TryStartNewWorker() = false, want true for one waiting task")
	}
	waitFor(t, "the waiting task to be submitted", func() bool { return fake.submittedCount() == 1 })
	if got := wm.GetBGWorkerCount(); got != 1 {
		t.Errorf("GetBGWorkerCount() = %d, want 1", got)
	}
	if task := taskProgress(t, tm, ids[2]); task.Status.Progress != model.TaskRunning {
		t.Errorf("waiting task progress = %v, want RUNNING", task.Status.Progress)
	}
	fake.FinishAll()
}

func TestAddTaskRunsTheWorkerSweep(t *testing.T) {
	t.Parallel()
	wm, tm, fake := newFakePool(t, 2)
	if !wm.Start() {
		t.Fatal("Start() = false")
	}
	ids := addTasks(t, wm, 3)
	waitFor(t, "two tasks to start", func() bool { return fake.submittedCount() == 2 })
	waitFor(t, "all three tasks to finish", func() bool {
		fake.FinishAll()
		return allFinished(tm, ids)
	})
}

func TestWorkerPoolNeverExceedsTheWorkerCount(t *testing.T) {
	t.Parallel()
	wm, tm, fake := newFakePool(t, 2)
	if !wm.Start() {
		t.Fatal("Start() = false")
	}
	ids := addTasks(t, wm, 5)

	waitFor(t, "all five tasks to finish", func() bool {
		fake.FinishAll()
		return allFinished(tm, ids)
	})
	if got := fake.submittedCount(); got != 5 {
		t.Errorf("submitted %d tasks, want 5", got)
	}
	if got := fake.maxActiveCount(); got > 2 {
		t.Errorf("max concurrent tasks = %d, want at most 2", got)
	}
	if fake.submittedTwice() {
		t.Error("a task was submitted twice")
	}
	waitFor(t, "the pool to drain", func() bool { return !wm.IsRunning() })
	waitFor(t, "every worker to exit", func() bool { return wm.GetBGWorkerCount() == 0 })
}

func TestWorkerPoolAppliesStatusEvents(t *testing.T) {
	t.Parallel()
	wm, tm, fake := newFakePool(t, 1)
	if !wm.Start() {
		t.Fatal("Start() = false")
	}

	var mu sync.Mutex
	var seen []model.StatusEvent
	wm.SetEventSink(func(ev model.StatusEvent) {
		mu.Lock()
		seen = append(seen, ev)
		mu.Unlock()
	})

	ids := addTasks(t, wm, 1)
	waitFor(t, "the task to start", func() bool { return fake.submittedCount() == 1 })

	// The worker name is recorded before the task is handed over.
	waitFor(t, "the worker name to be recorded", func() bool {
		return taskProgress(t, tm, ids[0]).Status.WorkerName == "工作单元-1"
	})
	fake.FinishAll()
	waitFor(t, "the task to finish", func() bool { return allFinished(tm, ids) })

	task := taskProgress(t, tm, ids[0])
	if task.Status.ProgressValue != 100 {
		t.Errorf("ProgressValue = %v, want 100", task.Status.ProgressValue)
	}
	if task.Status.Status != "x265" {
		t.Errorf("Status = %q, want the last step name", task.Status.Status)
	}
	// The terminal event carries no speed of its own, so the last reported
	// value must survive it, as it did on the legacy TaskStatus.
	if task.Status.Speed != "10.00 fps" {
		t.Errorf("Speed = %q, want %q", task.Status.Speed, "10.00 fps")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) < 2 {
		t.Fatalf("sink saw %d events, want at least 2", len(seen))
	}
	if seen[len(seen)-1].Progress != model.TaskFinished {
		t.Errorf("last sink event = %v, want FINISHED", seen[len(seen)-1].Progress)
	}
}

func TestWorkerPoolRecordsAFailedTask(t *testing.T) {
	t.Parallel()
	wm, tm, fake := newFakePool(t, 1)
	if !wm.Start() {
		t.Fatal("Start() = false")
	}
	ids := addTasks(t, wm, 1)
	waitFor(t, "the task to start", func() bool { return fake.submittedCount() == 1 })

	fake.Fail(ids[0])
	waitFor(t, "the task to fail", func() bool {
		return taskProgress(t, tm, ids[0]).Status.Progress == model.TaskError
	})
	task := taskProgress(t, tm, ids[0])
	if task.Status.Status != "x265出错" {
		t.Errorf("Status = %q, want the error summary", task.Status.Status)
	}
	if task.Status.ProgressUnknown {
		t.Error("ProgressUnknown = true, want the last reported value to stay known")
	}
	// The input must be free again, otherwise a retry on the same source would
	// wait forever.
	waitFor(t, "the input to be released", func() bool {
		return !tm.HasActiveTask("/cfg/project.json", model.NewFileRef("/media/epa.m2ts"))
	})
	waitFor(t, "the pool to drain", func() bool { return !wm.IsRunning() })
}

func TestWorkerPoolRecordsASubmitFailure(t *testing.T) {
	t.Parallel()
	tm := newTestManager(t, Options{})
	fake := newFakeExecutor()
	fake.submitErr = errors.New("node is unreachable")
	wm := NewWorkerManager(fake, tm, platform.NewNumaWithCount(1))
	wm.AddWorker(1)
	if !wm.Start() {
		t.Fatal("Start() = false")
	}
	ids := addTasks(t, wm, 1)

	// A refused submission is a task failure, not a pool failure: the legacy
	// catch block marked the task and continued the loop.
	waitFor(t, "the task to fail", func() bool {
		return taskProgress(t, tm, ids[0]).Status.Progress == model.TaskError
	})
	if got := taskProgress(t, tm, ids[0]).Status.Status; got != "未知错误" {
		t.Errorf("Status = %q, want the structured summary", got)
	}
	waitFor(t, "the input to be released", func() bool {
		return !tm.HasActiveTask("/cfg/project.json", model.NewFileRef("/media/epa.m2ts"))
	})
	waitFor(t, "the pool to drain", func() bool { return !wm.IsRunning() })
}

func TestAddWorkerRejectsADuplicateNumber(t *testing.T) {
	t.Parallel()
	wm, _, _ := newFakePool(t, 0)
	if !wm.AddWorker(1) {
		t.Fatal("first AddWorker(1) = false")
	}
	if wm.AddWorker(1) {
		t.Error("second AddWorker(1) = true, want false")
	}
	if got := wm.GetWorkerCount(); got != 1 {
		t.Errorf("GetWorkerCount() = %d, want 1", got)
	}
}

func TestWorkerNamesAndKeys(t *testing.T) {
	t.Parallel()
	wm, _, _ := newFakePool(t, 0)
	wm.AddWorker(1)
	wm.AddWorker(2)

	workers := wm.Workers()
	if len(workers) != 2 {
		t.Fatalf("Workers() returned %d entries, want 2", len(workers))
	}
	if workers[0].Name != "工作单元-1" || workers[0].WType != WorkerNormal || workers[0].Wid != 1 {
		t.Errorf("Workers()[0] = %+v, want 工作单元-1/1/Normal", workers[0])
	}

	// The legacy API accepted the number the UI displayed. It must resolve to
	// the same worker as the display name.
	if !wm.DeleteWorker("2") {
		t.Error("DeleteWorker(\"2\") = false")
	}
	if got := wm.GetWorkerCount(); got != 1 {
		t.Errorf("GetWorkerCount() = %d after deleting worker 2, want 1", got)
	}
	if !wm.DeleteWorker("工作单元-2") {
		t.Error("deleting an unknown worker must still report success")
	}
	if !wm.DeleteWorker("1") {
		t.Error("DeleteWorker(\"1\") = false")
	}
	if got := wm.GetWorkerCount(); got != 0 {
		t.Errorf("GetWorkerCount() = %d after deleting worker 1, want 0", got)
	}
}

func TestWorkerKey(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
	}{
		{in: "1", want: "工作单元-1"},
		{in: "12", want: "工作单元-12"},
		{in: "工作单元-3", want: "工作单元-3"},
		{in: "Temp-1", want: "Temp-1"},
		{in: "", want: ""},
	}
	for _, tc := range cases {
		if got := workerKey(tc.in); got != tc.want {
			t.Errorf("workerKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestAddTempWorkerNamesAndCounter(t *testing.T) {
	t.Parallel()
	wm, _, _ := newFakePool(t, 0)
	if got := wm.AddTempWorker(); got != "Temp-1" {
		t.Errorf("first AddTempWorker() = %q, want Temp-1", got)
	}
	if got := wm.AddTempWorker(); got != "Temp-2" {
		t.Errorf("second AddTempWorker() = %q, want Temp-2", got)
	}
	workers := wm.Workers()
	if len(workers) != 2 {
		t.Fatalf("Workers() returned %d entries, want 2", len(workers))
	}
	for _, w := range workers {
		if w.WType != WorkerTemporary {
			t.Errorf("worker %s type = %v, want Temporary", w.Name, w.WType)
		}
	}
}

func TestTempWorkerRunsOneTaskThenIsReleased(t *testing.T) {
	t.Parallel()
	tm := newTestManager(t, Options{})
	fake := newFakeExecutor()
	wm := NewWorkerManager(fake, tm, platform.NewNumaWithCount(1))

	if got := wm.AddTempWorker(); got != "Temp-1" {
		t.Fatalf("AddTempWorker() = %q, want Temp-1", got)
	}
	if !wm.Start() {
		t.Fatal("Start() = false with a temporary worker registered")
	}
	ids := addTasks(t, wm, 2)

	// Only one worker exists, so only one task may start.
	waitFor(t, "the first task to start", func() bool { return fake.submittedCount() == 1 })
	if got := taskProgress(t, tm, ids[1]).Status.Progress; got != model.TaskWaiting {
		t.Errorf("second task progress = %v, want WAITING", got)
	}

	fake.FinishAll()
	waitFor(t, "the temporary worker to be released", func() bool { return wm.GetWorkerCount() == 0 })
	waitFor(t, "the first task to finish", func() bool { return allFinished(tm, []model.TaskID{ids[0]}) })
	if got := taskProgress(t, tm, ids[1]).Status.Progress; got != model.TaskWaiting {
		t.Errorf("second task progress = %v after the temporary worker left, want WAITING", got)
	}
	waitFor(t, "the pool to drain", func() bool { return !wm.IsRunning() })
	if got := fake.submittedCount(); got != 1 {
		t.Errorf("submitted %d tasks, want 1", got)
	}

	// A permanent worker picks the queue up again without an explicit Start,
	// because the pool only drained rather than being stopped.
	if !wm.AddWorker(1) {
		t.Fatal("AddWorker(1) = false")
	}
	waitFor(t, "the second task to start", func() bool { return fake.submittedCount() == 2 })
	fake.FinishAll()
	waitFor(t, "the second task to finish", func() bool { return allFinished(tm, []model.TaskID{ids[1]}) })
}

func TestDeleteWorkerLetsTheRunningTaskFinish(t *testing.T) {
	t.Parallel()
	wm, tm, fake := newFakePool(t, 2)
	if !wm.Start() {
		t.Fatal("Start() = false")
	}
	ids := addTasks(t, wm, 3)
	waitFor(t, "two tasks to start", func() bool { return fake.submittedCount() == 2 })

	if !wm.DeleteWorker("2") {
		t.Fatal("DeleteWorker(\"2\") = false")
	}
	if got := wm.GetWorkerCount(); got != 1 {
		t.Errorf("GetWorkerCount() = %d, want 1", got)
	}

	waitFor(t, "all three tasks to finish", func() bool {
		fake.FinishAll()
		return allFinished(tm, ids)
	})
	if got := fake.submittedCount(); got != 3 {
		t.Errorf("submitted %d tasks, want 3", got)
	}
	waitFor(t, "the deleted worker to exit", func() bool { return wm.GetBGWorkerCount() <= 1 })
	waitFor(t, "the pool to drain", func() bool { return !wm.IsRunning() })
	if got := fake.cancelCount(); got != 0 {
		t.Errorf("Cancel was called %d times, want 0: deleting a worker must not kill its task", got)
	}
}

func TestStopWorkerCancelsTheTaskAndFreesTheSlot(t *testing.T) {
	t.Parallel()
	wm, tm, fake := newFakePool(t, 2)
	if !wm.Start() {
		t.Fatal("Start() = false")
	}
	ids := addTasks(t, wm, 2)
	waitFor(t, "two tasks to start", func() bool { return fake.submittedCount() == 2 })

	// Which worker got which task is not deterministic, so ask the queue.
	victim := ids[0]
	other := ids[1]
	waitFor(t, "the worker names to be recorded", func() bool {
		return workerOfTask(tm, victim) != "" && workerOfTask(tm, other) != ""
	})
	if workerOfTask(tm, victim) != "工作单元-1" {
		victim, other = other, victim
	}
	if got := workerOfTask(tm, victim); got != "工作单元-1" {
		t.Fatalf("no task is running on 工作单元-1: %q/%q", workerOfTask(tm, ids[0]), workerOfTask(tm, ids[1]))
	}

	wm.StopWorker("工作单元-1")
	waitFor(t, "the stopped task to reach its error state", func() bool {
		return taskProgress(t, tm, victim).Status.Progress == model.TaskError
	})
	if got := taskProgress(t, tm, victim).Status.Status; got != statusTerminated {
		t.Errorf("Status = %q, want %q", got, statusTerminated)
	}
	if got := fake.cancelCount(); got != 1 {
		t.Errorf("Cancel was called %d times, want 1", got)
	}
	if got := wm.GetWorkerCount(); got != 2 {
		t.Errorf("GetWorkerCount() = %d, want 2: the worker stays registered", got)
	}
	// The stopped worker leaves the schedulable set; the other one is untouched.
	waitFor(t, "the stopped worker to leave the pool", func() bool { return wm.GetBGWorkerCount() == 1 })
	if got := taskProgress(t, tm, other).Status.Progress; got != model.TaskRunning {
		t.Errorf("the other task progress = %v, want RUNNING", got)
	}
	fake.FinishAll()
	waitFor(t, "the other task to finish", func() bool { return allFinished(tm, []model.TaskID{other}) })
}

func TestStopWorkerUnknownIsNoop(t *testing.T) {
	t.Parallel()
	wm, _, fake := newFakePool(t, 1)
	wm.StopWorker("nobody")
	if got := fake.cancelCount(); got != 0 {
		t.Errorf("Cancel was called %d times, want 0", got)
	}
}

func TestStopAllWorkerCancelsEveryTask(t *testing.T) {
	t.Parallel()
	wm, tm, fake := newFakePool(t, 3)
	if !wm.Start() {
		t.Fatal("Start() = false")
	}
	ids := addTasks(t, wm, 3)
	waitFor(t, "all three tasks to start", func() bool { return fake.submittedCount() == 3 })

	wm.StopAllWorker()
	if got := fake.cancelCount(); got != 3 {
		t.Errorf("Cancel was called %d times, want 3", got)
	}
	waitFor(t, "every worker to leave the schedulable set", func() bool { return wm.GetBGWorkerCount() == 0 })
	for _, id := range ids {
		if got := taskProgress(t, tm, id).Status.Status; got != statusTerminated {
			t.Errorf("task %s status = %q, want %q", id, got, statusTerminated)
		}
	}
	// With every worker gone the pool has nothing left to schedule, so it
	// drains. The legacy StopAllWorker left IsRunning alone; the only caller
	// was Stop, which cleared it immediately afterwards anyway.
	waitFor(t, "the pool to drain", func() bool { return !wm.IsRunning() })
	wm.Stop()
}

func TestStopCancelsRunningTasksAndWaits(t *testing.T) {
	t.Parallel()
	wm, tm, fake := newFakePool(t, 2)
	if !wm.Start() {
		t.Fatal("Start() = false")
	}
	ids := addTasks(t, wm, 4)
	waitFor(t, "two tasks to start", func() bool { return fake.submittedCount() == 2 })

	wm.Stop()

	if wm.IsRunning() {
		t.Error("IsRunning() = true after Stop")
	}
	if got := wm.GetBGWorkerCount(); got != 0 {
		t.Errorf("GetBGWorkerCount() = %d after Stop, want 0", got)
	}
	if got := fake.cancelCount(); got != 2 {
		t.Errorf("Cancel was called %d times, want 2", got)
	}
	stopped := 0
	for _, id := range ids {
		task := taskProgress(t, tm, id)
		if task.Status.Progress != model.TaskError {
			continue
		}
		stopped++
		if task.Status.Status != statusTerminated {
			t.Errorf("task %s status = %q, want %q", id, task.Status.Status, statusTerminated)
		}
	}
	if stopped != 2 {
		t.Errorf("%d tasks reached the error state, want 2", stopped)
	}
	// Nothing new may start after Stop.
	if got := fake.submittedCount(); got != 2 {
		t.Errorf("submitted %d tasks after Stop, want 2", got)
	}
	// A stopped pool can be started again. The two tasks that were cancelled
	// are terminal, so only the two still waiting ones may run.
	if !wm.Start() {
		t.Fatal("Start() = false after Stop")
	}
	waitFor(t, "the remaining tasks to start", func() bool { return fake.submittedCount() == 4 })
	fake.FinishAll()
	waitFor(t, "the waiting tasks to finish", func() bool { return allFinished(tm, ids[2:]) })
}

func TestStopWithoutStartIsANoop(t *testing.T) {
	t.Parallel()
	wm, _, fake := newFakePool(t, 2)
	wm.Stop()
	if wm.IsRunning() {
		t.Error("IsRunning() = true after Stop")
	}
	if got := fake.cancelCount(); got != 0 {
		t.Errorf("Cancel was called %d times, want 0", got)
	}
}

func TestStopContextHonoursItsDeadline(t *testing.T) {
	t.Parallel()
	tm := newTestManager(t, Options{})
	// An executor that never reports anything: its channel stays open until
	// the test closes it, so the worker cannot exit on its own.
	release := make(chan struct{})
	submitted := make(chan struct{}, 1)
	exec := ExecutorFuncs{
		SubmitFunc: func(context.Context, *model.Task) (<-chan model.StatusEvent, error) {
			ch := make(chan model.StatusEvent)
			go func() {
				<-release
				close(ch)
			}()
			submitted <- struct{}{}
			return ch, nil
		},
	}
	wm := NewWorkerManager(exec, tm, platform.NewNumaWithCount(1))
	wm.AddWorker(1)
	if !wm.Start() {
		t.Fatal("Start() = false")
	}
	addTasks(t, wm, 1)
	// Wait until the worker actually owns the task; otherwise StopContext would
	// run against a worker that is still between tasks and could exit cleanly.
	<-submitted

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if err := wm.StopContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("StopContext() = %v, want DeadlineExceeded", err)
	}
	if wm.IsRunning() {
		t.Error("IsRunning() = true after a timed-out StopContext")
	}
	close(release)
	waitFor(t, "the worker to exit once the executor finishes", func() bool {
		return wm.GetBGWorkerCount() == 0
	})
}

func TestAfterFinishRunsOnlyOnFullSuccess(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		failFirst bool
		wantCalls int
	}{
		{name: "every task finished", wantCalls: 1},
		{name: "one task failed", failFirst: true, wantCalls: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			wm, tm, fake := newFakePool(t, 1)
			var calls int
			var mu sync.Mutex
			wm.SetAfterFinish(func() {
				mu.Lock()
				calls++
				mu.Unlock()
			})
			if !wm.Start() {
				t.Fatal("Start() = false")
			}
			ids := addTasks(t, wm, 2)
			waitFor(t, "the first task to start", func() bool { return fake.submittedCount() == 1 })

			if tc.failFirst {
				fake.Fail(ids[0])
			}
			waitFor(t, "the queue to drain", func() bool {
				fake.FinishAll()
				return settled(tm) && !wm.IsRunning()
			})
			mu.Lock()
			defer mu.Unlock()
			if calls != tc.wantCalls {
				t.Errorf("AfterFinish called %d times, want %d", calls, tc.wantCalls)
			}
		})
	}
}

func TestWorkersAreAssignedNUMANodes(t *testing.T) {
	t.Parallel()
	tm := newTestManager(t, Options{})
	fake := newFakeExecutor()
	// Four nodes start at the highest number and count down, so two workers get
	// 3 and 2.
	wm := NewWorkerManager(fake, tm, platform.NewNumaWithCount(4))
	wm.AddWorker(1)
	wm.AddWorker(2)
	if !wm.Start() {
		t.Fatal("Start() = false")
	}
	ids := addTasks(t, wm, 2)
	waitFor(t, "both tasks to start", func() bool { return fake.submittedCount() == 2 })

	nodes := make(map[int]struct{}, 2)
	for _, id := range ids {
		n, ok := fake.nodeOf(id)
		if !ok {
			t.Fatalf("task %s ran without a NUMA node", id)
		}
		if n != 2 && n != 3 {
			t.Errorf("task %s ran on node %d, want 2 or 3", id, n)
		}
		nodes[n] = struct{}{}
	}
	if len(nodes) != 2 {
		t.Errorf("workers share a NUMA node: %v", nodes)
	}
	fake.FinishAll()
}

func TestWorkerKeepsItsNUMANodeAcrossTasks(t *testing.T) {
	t.Parallel()
	tm := newTestManager(t, Options{})
	fake := newFakeExecutor()
	wm := NewWorkerManager(fake, tm, platform.NewNumaWithCount(4))
	wm.AddWorker(1)
	if !wm.Start() {
		t.Fatal("Start() = false")
	}
	ids := addTasks(t, wm, 2)

	waitFor(t, "all tasks to finish", func() bool {
		fake.FinishAll()
		return allFinished(tm, ids)
	})
	first, ok := fake.nodeOf(ids[0])
	if !ok {
		t.Fatal("the first task ran without a NUMA node")
	}
	second, ok := fake.nodeOf(ids[1])
	if !ok {
		t.Fatal("the second task ran without a NUMA node")
	}
	if first != second {
		t.Errorf("one worker used nodes %d and %d, want the same node", first, second)
	}
}

func TestNUMANodeContext(t *testing.T) {
	t.Parallel()
	ctx := WithNUMANode(t.Context(), 3)
	if got, ok := NUMANodeFrom(ctx); !ok || got != 3 {
		t.Errorf("NUMANodeFrom() = (%d, %v), want (3, true)", got, ok)
	}
	if got, ok := NUMANodeFrom(t.Context()); ok || got != 0 {
		t.Errorf("NUMANodeFrom(plain ctx) = (%d, %v), want (0, false)", got, ok)
	}
}

func TestPoolDoesNotHoldTheLockAcrossCallbacks(t *testing.T) {
	t.Parallel()
	wm, tm, fake := newFakePool(t, 1)
	// Both callbacks call back into the pool. If the pool held its lock while
	// submitting a task or forwarding an event, this test would deadlock and
	// fail on the timeout instead of passing silently.
	fake.onSubmit = func(*model.Task) {
		wm.IsRunning()
		wm.GetWorkerCount()
		wm.GetBGWorkerCount()
		wm.Workers()
	}
	fake.onCancel = func(model.TaskID) { wm.GetBGWorkerCount() }
	wm.SetEventSink(func(model.StatusEvent) { wm.GetBGWorkerCount() })

	if !wm.Start() {
		t.Fatal("Start() = false")
	}
	ids := addTasks(t, wm, 1)
	waitFor(t, "the task to start", func() bool { return fake.submittedCount() == 1 })
	fake.FinishAll()
	waitFor(t, "the task to finish", func() bool { return allFinished(tm, ids) })
}

func TestDrainedPoolResumesWhenWorkArrives(t *testing.T) {
	t.Parallel()
	wm, tm, fake := newFakePool(t, 1)
	if !wm.Start() {
		t.Fatal("Start() = false")
	}
	ids := addTasks(t, wm, 1)
	waitFor(t, "the first task to finish", func() bool {
		fake.FinishAll()
		return allFinished(tm, ids)
	})
	waitFor(t, "the pool to drain", func() bool { return !wm.IsRunning() })

	// The queue emptied rather than being stopped, so a new task resumes the
	// run without an explicit Start. The legacy code required a Start here.
	more := addTasks(t, wm, 1)
	waitFor(t, "the new task to start", func() bool { return fake.submittedCount() == 2 })
	if !wm.IsRunning() {
		t.Error("IsRunning() = false while the resumed run is working")
	}
	fake.FinishAll()
	waitFor(t, "the new task to finish", func() bool { return allFinished(tm, more) })
}

func TestStoppedPoolStaysStoppedWhenWorkArrives(t *testing.T) {
	t.Parallel()
	wm, tm, fake := newFakePool(t, 1)
	if !wm.Start() {
		t.Fatal("Start() = false")
	}
	ids := addTasks(t, wm, 1)
	waitFor(t, "the task to start", func() bool { return fake.submittedCount() == 1 })
	wm.Stop()
	waitFor(t, "the stopped task to settle", func() bool {
		return allFinished(tm, ids) || taskProgress(t, tm, ids[0]).Status.Progress == model.TaskError
	})

	// An explicit Stop is the operator's decision: new work waits for Start,
	// exactly like the legacy pool.
	more := addTasks(t, wm, 1)
	if got := fake.submittedCount(); got != 1 {
		t.Errorf("submitted %d tasks after Stop, want 1", got)
	}
	if wm.IsRunning() {
		t.Error("IsRunning() = true after Stop")
	}
	if !wm.Start() {
		t.Fatal("Start() = false after Stop")
	}
	waitFor(t, "the waiting task to start", func() bool { return fake.submittedCount() == 2 })
	fake.FinishAll()
	waitFor(t, "the waiting task to finish", func() bool { return allFinished(tm, more) })
}

func TestWorkerSatisfiesTheExecutorBoundary(t *testing.T) {
	t.Parallel()
	// The pool must work with any Executor, which is the whole point of the
	// boundary (CLUSTER.md §2, reservation 1). A local executor with a trivial
	// run function is enough to prove the pool never reaches past the
	// interface.
	exec := NewLocalExecutor(testCaps(), func(_ context.Context, _ *model.Task, events chan<- model.StatusEvent) error {
		events <- model.StatusEvent{Progress: model.TaskRunning, Step: "vspipe", Percent: 50}
		return nil
	})
	tm := newTestManager(t, Options{})
	wm := NewWorkerManager(exec, tm, platform.NewNumaWithCount(1))
	wm.AddWorker(1)
	if !wm.Start() {
		t.Fatal("Start() = false")
	}
	ids := addTasks(t, wm, 2)
	waitFor(t, "both tasks to finish", func() bool { return allFinished(tm, ids) })
	exec.Wait()
}

func TestConcurrentAddTask(t *testing.T) {
	t.Parallel()
	const (
		workers   = 3
		producers = 6
		perWorker = 5
		total     = producers * perWorker
	)
	wm, tm, fake := newFakePool(t, workers)
	if !wm.Start() {
		t.Fatal("Start() = false")
	}

	var wg sync.WaitGroup
	errs := make(chan error, total)
	for p := range producers {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			for i := range perWorker {
				task := &model.Task{
					ID:     model.NewTaskID(),
					Name:   "concurrent",
					Inputs: []model.FileRef{model.NewFileRef("/media/c" + string(rune('0'+p)) + string(rune('0'+i)) + ".m2ts")},
				}
				if _, err := wm.AddTask(task, "/cfg/concurrent.json"); err != nil {
					errs <- err
				}
			}
		}(p)
	}

	// A concurrent consumer keeps finishing whatever has started, so the queue
	// actually drains.
	done := make(chan struct{})
	var consumer sync.WaitGroup
	consumer.Add(1)
	go func() {
		defer consumer.Done()
		for {
			select {
			case <-done:
				return
			default:
				fake.FinishAll()
				time.Sleep(50 * time.Microsecond)
			}
		}
	}()

	wg.Wait()
	waitFor(t, "all concurrent tasks to finish", func() bool {
		fake.FinishAll()
		return tm.GetTaskCount() == total && settled(tm)
	})
	close(done)
	consumer.Wait()

	select {
	case err := <-errs:
		t.Fatalf("AddTask error = %v", err)
	default:
	}
	if got := fake.submittedCount(); got != total {
		t.Errorf("submitted %d tasks, want %d", got, total)
	}
	if fake.submittedTwice() {
		t.Error("a task was submitted twice")
	}
	if got := fake.maxActiveCount(); got > workers {
		t.Errorf("max concurrent tasks = %d, want at most %d", got, workers)
	}
	if !tm.AllSuccess() {
		t.Error("AllSuccess() = false, want true")
	}
}

func TestConcurrentWorkersDoNotStartTheSameTask(t *testing.T) {
	t.Parallel()
	wm, tm, fake := newFakePool(t, 4)
	if !wm.Start() {
		t.Fatal("Start() = false")
	}
	ids := addTasks(t, wm, 12)

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 40 {
				fake.FinishAll()
				wm.TryStartNewWorker()
			}
		}()
	}
	wg.Wait()

	waitFor(t, "all tasks to finish", func() bool {
		fake.FinishAll()
		return allFinished(tm, ids)
	})
	if fake.submittedTwice() {
		t.Error("a task was submitted twice")
	}
	if got := fake.submittedCount(); got != 12 {
		t.Errorf("submitted %d tasks, want 12", got)
	}
}
