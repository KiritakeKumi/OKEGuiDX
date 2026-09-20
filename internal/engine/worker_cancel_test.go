package engine

import (
	"context"
	"testing"
	"time"

	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/platform"
)

// These tests pin the single-task cancellation the API's
// POST /api/v1/tasks/{id}/cancel needs. The legacy equivalent was
// WorkerManager.StopWorker, which is keyed by worker; a REST client only knows
// a task id, so the pool gained CancelTask.

// TestCancelTaskStopsARunningTask covers the running case: the executor is
// asked to stop the task, its context is cancelled, the worker writes the
// legacy "已终止" state, and the worker itself stays in the pool so the rest of
// the queue keeps running.
func TestCancelTaskStopsARunningTask(t *testing.T) {
	t.Parallel()
	wm, tm, fake := newFakePool(t, 2)
	if !wm.Start() {
		t.Fatal("Start() = false")
	}
	// Three tasks for two workers, so the freed worker has something to pick
	// up and the test can tell "the worker stayed" from "the pool drained".
	ids := addTasks(t, wm, 3)
	waitFor(t, "two tasks to start", func() bool { return fake.submittedCount() == 2 })

	// Which worker got which task is not deterministic, so ask the queue.
	victim, other := ids[0], ids[1]
	waitFor(t, "the worker names to be recorded", func() bool {
		return workerOfTask(tm, victim) != "" && workerOfTask(tm, other) != ""
	})
	if cancelled, err := wm.CancelTask(victim); err != nil || !cancelled {
		t.Fatalf("CancelTask(%s) = %v, %v; want true, nil", victim, cancelled, err)
	}

	waitFor(t, "the cancelled task to reach its error state", func() bool {
		return taskProgress(t, tm, victim).Status.Progress == model.TaskError
	})
	if got := taskProgress(t, tm, victim).Status.Status; got != statusTerminated {
		t.Errorf("Status = %q, want %q", got, statusTerminated)
	}
	if got := fake.cancelCount(); got != 1 {
		t.Errorf("Cancel was called %d times, want 1", got)
	}
	// The worker stays registered, unlike StopWorker, which removes it from the
	// schedulable set: a client asked for one task to stop, not for a worker.
	if got := wm.GetWorkerCount(); got != 2 {
		t.Errorf("GetWorkerCount() = %d, want 2", got)
	}
	if got := taskProgress(t, tm, other).Status.Progress; got != model.TaskRunning {
		t.Errorf("the other task progress = %v, want RUNNING", got)
	}

	// The freed worker takes the next task, so the pool is still usable.
	waitFor(t, "the third task to start on the freed worker", func() bool { return fake.submittedCount() == 3 })
	fake.FinishAll()
	waitFor(t, "the remaining tasks to finish", func() bool { return allFinished(tm, ids[1:]) })
	if got := taskProgress(t, tm, ids[2]).Status.Progress; got != model.TaskFinished {
		t.Errorf("third task progress = %v, want FINISHED", got)
	}
}

// TestCancelWaitingTask covers the waiting case: the queue itself writes the
// terminal state, and the task is never handed to the executor.
func TestCancelWaitingTask(t *testing.T) {
	t.Parallel()
	wm, tm, fake := newFakePool(t, 1)
	if !wm.Start() {
		t.Fatal("Start() = false")
	}
	ids := addTasks(t, wm, 2)
	waitFor(t, "the first task to start", func() bool { return fake.submittedCount() == 1 })

	// One worker, so exactly one task is running and the other is waiting.
	var waiting model.TaskID
	for _, id := range ids {
		if taskProgress(t, tm, id).Status.Progress == model.TaskWaiting {
			waiting = id
		}
	}
	if waiting.IsZero() {
		t.Fatalf("no task is waiting: %+v", tm.Snapshot())
	}

	if cancelled, err := wm.CancelTask(waiting); err != nil || !cancelled {
		t.Fatalf("CancelTask(%s) = %v, %v; want true, nil", waiting, cancelled, err)
	}
	task := taskProgress(t, tm, waiting)
	if task.Status.Progress != model.TaskError {
		t.Errorf("progress = %v, want ERROR", task.Status.Progress)
	}
	if task.Status.Status != statusTerminated {
		t.Errorf("status = %q, want %q", task.Status.Status, statusTerminated)
	}
	if got := fake.submittedCount(); got != 1 {
		t.Errorf("submitted %d tasks, want 1: a cancelled waiting task must not run", got)
	}
	if got := fake.cancelCount(); got != 0 {
		t.Errorf("Cancel was called %d times, want 0: the task never reached the executor", got)
	}

	// The worker moves on to the queue's end without picking the cancelled
	// task up again.
	fake.FinishAll()
	waitFor(t, "the pool to drain", func() bool { return !wm.IsRunning() })
	if got := fake.submittedCount(); got != 1 {
		t.Errorf("submitted %d tasks after the queue drained, want 1", got)
	}
}

// TestCancelTaskRejectsSettledAndUnknownTasks pins the bool contract the API
// turns into 404 and 409: only a waiting or running task can be cancelled.
func TestCancelTaskRejectsSettledAndUnknownTasks(t *testing.T) {
	t.Parallel()
	wm, tm, _ := newFakePool(t, 1)

	if cancelled, err := wm.CancelTask(model.NewTaskID()); err != nil || cancelled {
		t.Errorf("CancelTask(unknown) = %v, %v; want false, nil", cancelled, err)
	}
	if cancelled, err := wm.CancelTask(""); err != nil || cancelled {
		t.Errorf("CancelTask(\"\") = %v, %v; want false, nil", cancelled, err)
	}

	ids := addTasks(t, wm, 2)
	if err := tm.Update(ids[0], func(t *model.Task) { t.Status.Progress = model.TaskFinished }); err != nil {
		t.Fatalf("mark finished: %v", err)
	}
	if err := tm.Update(ids[1], func(t *model.Task) { t.Status.Progress = model.TaskError }); err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	for _, id := range ids {
		if cancelled, err := wm.CancelTask(id); err != nil || cancelled {
			t.Errorf("CancelTask(settled %s) = %v, %v; want false, nil", id, cancelled, err)
		}
	}
	// A settled task keeps the state it had.
	if got := taskProgress(t, tm, ids[0]).Status.Progress; got != model.TaskFinished {
		t.Errorf("finished task progress = %v, want FINISHED", got)
	}
	if got := taskProgress(t, tm, ids[1]).Status.Progress; got != model.TaskError {
		t.Errorf("failed task progress = %v, want ERROR", got)
	}
	if got := taskProgress(t, tm, ids[1]).Status.Status; got == statusTerminated {
		t.Error("cancelling a failed task rewrote its status to the cancelled one")
	}
}

// TestCancelTaskIsIdempotent covers a client that presses the button twice: the
// second call finds the task settled and reports that, rather than cancelling
// something else.
func TestCancelTaskIsIdempotent(t *testing.T) {
	t.Parallel()
	wm, tm, fake := newFakePool(t, 1)
	if !wm.Start() {
		t.Fatal("Start() = false")
	}
	ids := addTasks(t, wm, 1)
	waitFor(t, "the task to start", func() bool { return fake.submittedCount() == 1 })

	if cancelled, err := wm.CancelTask(ids[0]); err != nil || !cancelled {
		t.Fatalf("first CancelTask = %v, %v; want true, nil", cancelled, err)
	}
	waitFor(t, "the task to settle", func() bool {
		return taskProgress(t, tm, ids[0]).Status.Progress == model.TaskError
	})
	if cancelled, err := wm.CancelTask(ids[0]); err != nil || cancelled {
		t.Errorf("second CancelTask = %v, %v; want false, nil", cancelled, err)
	}
	if got := fake.cancelCount(); got != 1 {
		t.Errorf("Cancel was called %d times, want 1", got)
	}
}

// TestCancelAllSettlesWaitingTasks pins the difference between Stop and
// CancelAll: Stop leaves the queue alone (the legacy behaviour), CancelAll is
// what the API's pool stop calls so nothing is left to run.
func TestCancelAllSettlesWaitingTasks(t *testing.T) {
	t.Parallel()
	wm, tm, fake := newFakePool(t, 1)
	if !wm.Start() {
		t.Fatal("Start() = false")
	}
	ids := addTasks(t, wm, 3)
	waitFor(t, "the first task to start", func() bool { return fake.submittedCount() == 1 })

	if err := wm.CancelAll(context.Background()); err != nil {
		t.Fatalf("CancelAll() error = %v", err)
	}
	waitFor(t, "every task to settle", func() bool { return settled(tm) })
	for _, id := range ids {
		task := taskProgress(t, tm, id)
		if task.Status.Progress != model.TaskError {
			t.Errorf("task %s progress = %v, want ERROR", id, task.Status.Progress)
		}
		if task.Status.Status != statusTerminated {
			t.Errorf("task %s status = %q, want %q", id, task.Status.Status, statusTerminated)
		}
	}
	if got := fake.submittedCount(); got != 1 {
		t.Errorf("submitted %d tasks, want 1: a cancelled pool must not start the waiting ones", got)
	}
	if got := tm.GetActiveTaskCount(); got != 0 {
		t.Errorf("active task count = %d after CancelAll, want 0", got)
	}
}

// TestCancelTaskBetweenWorkersAndTheQueue covers the seam the two paths share:
// a task cancelled while a worker is claiming it must end in exactly one
// terminal state, never running afterwards.
func TestCancelTaskBetweenWorkersAndTheQueue(t *testing.T) {
	t.Parallel()
	wm, tm, fake := newFakePool(t, 1)
	// The pool is not started, so every task stays waiting and the test can
	// drive the cancellation against the queue alone.
	ids := addTasks(t, wm, 2)

	for _, id := range ids {
		if cancelled, err := wm.CancelTask(id); err != nil || !cancelled {
			t.Fatalf("CancelTask(%s) = %v, %v; want true, nil", id, cancelled, err)
		}
	}
	if got := fake.submittedCount(); got != 0 {
		t.Errorf("submitted %d tasks, want 0", got)
	}

	// Starting the pool finds nothing runnable, so the cancelled tasks stay
	// cancelled.
	if !wm.Start() {
		t.Fatal("Start() = false")
	}
	wm.Stop()
	for _, id := range ids {
		if got := taskProgress(t, tm, id).Status.Progress; got != model.TaskError {
			t.Errorf("task %s progress = %v, want ERROR", id, got)
		}
	}
	if got := fake.submittedCount(); got != 0 {
		t.Errorf("submitted %d tasks after start, want 0", got)
	}
}

// TestCancelTaskWhileAWorkerIsClaimingIt drives the narrow window between
// GetNextTask and beginTask. The executor's onSubmit hook runs before the task
// is registered, so cancelling from there lands exactly in the gap; the task
// must be refused by the worker rather than run.
func TestCancelTaskWhileAWorkerIsClaimingIt(t *testing.T) {
	t.Parallel()

	tm := newTestManager(t, Options{})
	fake := newFakeExecutor()
	wm := NewWorkerManager(fake, tm, platform.NewNumaWithCount(1))
	fake.onSubmit = func(task *model.Task) {
		// The worker has claimed the task but has not registered it yet.
		cancelled, err := wm.CancelTask(task.ID)
		if err != nil {
			t.Errorf("CancelTask(%s) error = %v", task.ID, err)
		}
		if !cancelled {
			t.Errorf("CancelTask(%s) = false, want true for a claimed task", task.ID)
		}
	}
	wm.AddWorker(1)
	if !wm.Start() {
		t.Fatal("Start() = false")
	}
	ids := addTasks(t, wm, 1)

	// The task reaches its terminal state without ever being submitted.
	waitFor(t, "the task to settle", func() bool { return settled(tm) })
	if got := taskProgress(t, tm, ids[0]).Status.Progress; got != model.TaskError {
		t.Errorf("progress = %v, want ERROR", got)
	}
	if got := taskProgress(t, tm, ids[0]).Status.Status; got != statusTerminated {
		t.Errorf("status = %q, want %q", got, statusTerminated)
	}
	if got := fake.submittedCount(); got != 1 {
		t.Errorf("submitted %d tasks, want 1 (Submit was entered once)", got)
	}
	// The worker itself has left: with the only task settled the queue is
	// empty, so the loop drains, which is the pool's normal end of run.
	waitFor(t, "the pool to drain", func() bool { return !wm.IsRunning() })
}

// TestCancelRequestedMapDoesNotGrow pins the bookkeeping of the claim-window
// fix: a cancel that no worker ever sees is dropped instead of accumulating.
func TestCancelRequestedMapDoesNotGrow(t *testing.T) {
	t.Parallel()
	wm, tm, _ := newFakePool(t, 1)
	ids := addTasks(t, wm, 1)
	if err := tm.Update(ids[0], func(t *model.Task) { t.Status.Progress = model.TaskFinished }); err != nil {
		t.Fatalf("mark finished: %v", err)
	}

	// The task settled, so nothing records a request for it.
	if cancelled, err := wm.CancelTask(ids[0]); err != nil || cancelled {
		t.Fatalf("CancelTask(settled) = %v, %v; want false, nil", cancelled, err)
	}
	wm.mu.Lock()
	pending := len(wm.cancelRequested)
	wm.mu.Unlock()
	if pending != 0 {
		t.Errorf("cancelRequested holds %d entries, want 0", pending)
	}
}

// TestCancelTaskReleasesTheInputOnTheRunningPath pins the queue bookkeeping: a
// cancelled running task frees its input file, so the same source can be queued
// again.
func TestCancelTaskReleasesTheInputOnTheRunningPath(t *testing.T) {
	t.Parallel()
	wm, tm, fake := newFakePool(t, 1)
	if !wm.Start() {
		t.Fatal("Start() = false")
	}
	ids := addTasks(t, wm, 1)
	waitFor(t, "the task to start", func() bool { return fake.submittedCount() == 1 })

	if cancelled, err := wm.CancelTask(ids[0]); err != nil || !cancelled {
		t.Fatalf("CancelTask = %v, %v; want true, nil", cancelled, err)
	}
	waitFor(t, "the task to settle", func() bool { return settled(tm) })

	// The same input may be queued again: ReleaseInput ran on the exit path.
	task := taskProgress(t, tm, ids[0])
	if got := tm.HasActiveTask("/cfg/project.json", task.Status.Input); got {
		t.Error("a cancelled task still holds its input file")
	}
	if _, err := wm.AddTask(&model.Task{
		ID:     model.NewTaskID(),
		Inputs: []model.FileRef{task.Status.Input},
	}, "/cfg/project.json"); err != nil {
		t.Fatalf("AddTask after cancel error = %v", err)
	}
	waitFor(t, "the requeued task to start", func() bool { return fake.submittedCount() == 2 })
	fake.FinishAll()
	waitFor(t, "the pool to drain", func() bool { return !wm.IsRunning() })
}

// TestCancelTaskDoesNotDisturbThePoolState pins that a single cancellation is
// not a pool stop: the pool keeps running and its state is unchanged.
func TestCancelTaskDoesNotDisturbThePoolState(t *testing.T) {
	t.Parallel()
	wm, tm, fake := newFakePool(t, 2)
	if !wm.Start() {
		t.Fatal("Start() = false")
	}
	ids := addTasks(t, wm, 2)
	waitFor(t, "both tasks to start", func() bool { return fake.submittedCount() == 2 })

	if cancelled, err := wm.CancelTask(ids[0]); err != nil || !cancelled {
		t.Fatalf("CancelTask = %v, %v; want true, nil", cancelled, err)
	}
	waitFor(t, "the cancelled task to settle", func() bool {
		return taskProgress(t, tm, ids[0]).Status.Progress == model.TaskError
	})
	if !wm.IsRunning() {
		t.Error("cancelling one task stopped the whole pool")
	}

	// A task added afterwards still runs, which is the contract the Web UI
	// needs: cancelling is not stopping.
	fake.FinishAll()
	waitFor(t, "the other task to finish", func() bool { return allFinished(tm, ids[1:]) })
	third := addTasks(t, wm, 1)
	waitFor(t, "the next task to start", func() bool { return fake.submittedCount() == 3 })
	fake.FinishAll()
	waitFor(t, "the pool to drain", func() bool { return !wm.IsRunning() })
	if got := taskProgress(t, tm, third[0]).Status.Progress; got != model.TaskFinished {
		t.Errorf("task added after a cancel = %v, want FINISHED", got)
	}
}

// TestCancelTaskContextReachesTheExecutor pins the mechanism rather than the
// bookkeeping: a real LocalExecutor task that waits on its context must observe
// the cancellation, which is what stops the child processes in a real run.
func TestCancelTaskContextReachesTheExecutor(t *testing.T) {
	t.Parallel()
	tm := newTestManager(t, Options{})
	started := make(chan struct{})
	observed := make(chan struct{})
	exec := NewLocalExecutor(testCaps(), func(ctx context.Context, _ *model.Task, events chan<- model.StatusEvent) error {
		close(started)
		<-ctx.Done()
		close(observed)
		return ctx.Err()
	})
	wm := NewWorkerManager(exec, tm, platform.NewNumaWithCount(1))
	wm.AddWorker(1)
	if !wm.Start() {
		t.Fatal("Start() = false")
	}
	ids := addTasks(t, wm, 1)

	select {
	case <-started:
	case <-time.After(testTimeout):
		t.Fatal("the executor never started the task")
	}
	if cancelled, err := wm.CancelTask(ids[0]); err != nil || !cancelled {
		t.Fatalf("CancelTask = %v, %v; want true, nil", cancelled, err)
	}
	select {
	case <-observed:
	case <-time.After(testTimeout):
		t.Fatal("the task function never observed the cancellation")
	}
	waitFor(t, "the task to settle", func() bool { return settled(tm) })
	if got := taskProgress(t, tm, ids[0]).Status.Status; got != statusTerminated {
		t.Errorf("status = %q, want %q", got, statusTerminated)
	}
	wm.Stop()
	exec.Wait()
}
