package engine

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/node"
)

func testCaps() node.Capabilities {
	caps := node.NewCapabilities(node.RoleStandalone)
	caps.Tools["x265"] = node.ToolInfo{Path: "/tools/x265"}
	return caps
}

func TestLocalExecutorReportsCompletion(t *testing.T) {
	t.Parallel()
	exec := NewLocalExecutor(testCaps(), func(_ context.Context, _ *model.Task, events chan<- model.StatusEvent) error {
		events <- model.StatusEvent{Progress: model.TaskRunning, Percent: 50, Speed: "10.00 fps"}
		return nil
	})

	task := &model.Task{ID: model.NewTaskID(), Name: "ep01"}
	events, err := exec.Submit(t.Context(), task)
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}

	var got []model.StatusEvent
	for ev := range events {
		got = append(got, ev)
	}
	if len(got) < 2 {
		t.Fatalf("got %d events, want at least 2", len(got))
	}
	if got[0].Percent != 50 {
		t.Errorf("first event percent = %v, want 50", got[0].Percent)
	}
	final := got[len(got)-1]
	if final.Progress != model.TaskFinished {
		t.Errorf("final progress = %v, want FINISHED", final.Progress)
	}
	if final.Percent != 100 {
		t.Errorf("final percent = %v, want 100", final.Percent)
	}
}

func TestLocalExecutorReportsFailure(t *testing.T) {
	t.Parallel()
	exec := NewLocalExecutor(testCaps(), func(context.Context, *model.Task, chan<- model.StatusEvent) error {
		return errors.New("encoder exploded")
	})

	events, err := exec.Submit(t.Context(), &model.Task{ID: model.NewTaskID()})
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	var final model.StatusEvent
	for ev := range events {
		final = ev
	}
	if final.Progress != model.TaskError {
		t.Errorf("progress = %v, want ERROR", final.Progress)
	}
	if final.Error == nil {
		t.Fatal("Error is nil, want a structured error")
	}
	if final.Error.Summary == "" {
		t.Error("Error.Summary is empty")
	}
}

func TestLocalExecutorRejectsDuplicateSubmit(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	exec := NewLocalExecutor(testCaps(), func(context.Context, *model.Task, chan<- model.StatusEvent) error {
		<-release
		return nil
	})

	task := &model.Task{ID: model.NewTaskID()}
	if _, err := exec.Submit(t.Context(), task); err != nil {
		t.Fatalf("first Submit() error = %v", err)
	}
	if _, err := exec.Submit(t.Context(), task); err == nil {
		t.Error("second Submit() = nil error, want a duplicate rejection")
	}
	close(release)
	exec.Wait()
}

func TestLocalExecutorRejectsNilTask(t *testing.T) {
	t.Parallel()
	exec := NewLocalExecutor(testCaps(), func(context.Context, *model.Task, chan<- model.StatusEvent) error {
		return nil
	})
	if _, err := exec.Submit(t.Context(), nil); err == nil {
		t.Error("Submit(nil) = nil error, want a rejection")
	}
}

func TestLocalExecutorCancelStopsTask(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	var finished atomic.Bool
	exec := NewLocalExecutor(testCaps(), func(ctx context.Context, _ *model.Task, _ chan<- model.StatusEvent) error {
		close(started)
		<-ctx.Done()
		finished.Store(true)
		return ctx.Err()
	})

	task := &model.Task{ID: model.NewTaskID()}
	events, err := exec.Submit(t.Context(), task)
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	<-started
	if err := exec.Cancel(t.Context(), task.ID); err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	for range events {
	}
	if !finished.Load() {
		t.Error("the task function did not observe cancellation")
	}
}

func TestLocalExecutorCancelUnknownTaskIsNoop(t *testing.T) {
	t.Parallel()
	exec := NewLocalExecutor(testCaps(), func(context.Context, *model.Task, chan<- model.StatusEvent) error {
		return nil
	})
	if err := exec.Cancel(t.Context(), model.NewTaskID()); err != nil {
		t.Errorf("Cancel(unknown) error = %v, want nil", err)
	}
}

func TestLocalExecutorTracksRunningCount(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	exec := NewLocalExecutor(testCaps(), func(context.Context, *model.Task, chan<- model.StatusEvent) error {
		<-release
		return nil
	})

	for range 3 {
		if _, err := exec.Submit(t.Context(), &model.Task{ID: model.NewTaskID()}); err != nil {
			t.Fatalf("Submit() error = %v", err)
		}
	}
	deadline := time.After(5 * time.Second)
	for exec.Running() != 3 {
		select {
		case <-deadline:
			t.Fatalf("Running() = %d, want 3", exec.Running())
		default:
			time.Sleep(time.Millisecond)
		}
	}
	close(release)
	exec.Wait()
	if got := exec.Running(); got != 0 {
		t.Errorf("Running() = %d after Wait, want 0", got)
	}
}

func TestLocalExecutorCapabilities(t *testing.T) {
	t.Parallel()
	caps := testCaps()
	exec := NewLocalExecutor(caps, func(context.Context, *model.Task, chan<- model.StatusEvent) error {
		return nil
	})
	got, err := exec.Capabilities(t.Context())
	if err != nil {
		t.Fatalf("Capabilities() error = %v", err)
	}
	if !got.HasTool("x265") {
		t.Error("Capabilities() lost the tool list")
	}
}

func TestLocalExecutorSatisfiesInterface(t *testing.T) {
	t.Parallel()
	// Compile-time proof that the local implementation honours the boundary the
	// future RemoteExecutor will also honour.
	var _ Executor = (*LocalExecutor)(nil)
	var _ Executor = ExecutorFuncs{}
}

func TestExecutorFuncsDefaults(t *testing.T) {
	t.Parallel()
	f := ExecutorFuncs{
		SubmitFunc: func(context.Context, *model.Task) (<-chan model.StatusEvent, error) {
			ch := make(chan model.StatusEvent)
			close(ch)
			return ch, nil
		},
	}
	caps, err := f.Capabilities(t.Context())
	if err != nil {
		t.Fatalf("Capabilities() error = %v", err)
	}
	if caps.Role != node.RoleStandalone {
		t.Errorf("Role = %v, want standalone", caps.Role)
	}
	if err := f.Cancel(t.Context(), model.NewTaskID()); err != nil {
		t.Errorf("Cancel() error = %v, want nil", err)
	}
}
