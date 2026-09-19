package ws

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/KiritakeKumi/OKEGuiDX/internal/engine"
	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/node"
)

// TestHubAsWorkerEventSink is the integration the daemon performs at startup:
// WorkerManager.SetEventSink(hub.Publish). It runs a real worker pool with a
// stub executor and checks that every applied event reaches a subscriber.
func TestHubAsWorkerEventSink(t *testing.T) {
	t.Parallel()
	hub := NewHub(64)
	sub := hub.Subscribe()
	t.Cleanup(sub.Unsubscribe)

	exec := engine.NewLocalExecutor(node.NewCapabilities(node.RoleStandalone),
		func(_ context.Context, task *model.Task, events chan<- model.StatusEvent) error {
			// The pool drops an event whose task id does not match the task it
			// claimed, so a real RunFunc always stamps the id.
			events <- model.StatusEvent{TaskID: task.ID, Progress: model.TaskRunning, Step: "x265", Percent: 1}
			events <- model.StatusEvent{TaskID: task.ID, Progress: model.TaskRunning, Step: "x265", Percent: 50}
			return nil
		})
	tm, err := engine.New(engine.Options{})
	if err != nil {
		t.Fatalf("engine.New() error = %v", err)
	}
	pool := engine.NewWorkerManager(exec, tm, nil)
	pool.SetEventSink(hub.Publish)
	if !pool.AddWorker(1) {
		t.Fatal("AddWorker(1) reported a duplicate worker")
	}

	task := &model.Task{ID: model.NewTaskID(), Name: "ep01"}
	if _, err := pool.AddTask(task, ""); err != nil {
		t.Fatalf("AddTask() error = %v", err)
	}
	if !pool.Start() {
		t.Fatal("Start() reported no worker")
	}
	t.Cleanup(pool.Stop)

	// The executor emits two progress events and the pool adds the terminal
	// event, so three events must arrive in order.
	var got []model.StatusEvent
	deadline := time.After(10 * time.Second)
	for len(got) < 3 {
		select {
		case msg := <-sub.Send():
			if msg.Type != MessageStatus || msg.Event == nil {
				t.Fatalf("message = %+v, want a status event", msg)
			}
			got = append(got, *msg.Event)
		case <-deadline:
			t.Fatalf("received %d events, want 3: %+v", len(got), got)
		}
	}
	if got[0].Percent != 1 || got[1].Percent != 50 {
		t.Errorf("progress events = %v, %v; want 1 and 50", got[0].Percent, got[1].Percent)
	}
	if got[2].Progress != model.TaskFinished {
		t.Errorf("terminal event = %v, want FINISHED", got[2].Progress)
	}
	for i, ev := range got {
		if ev.TaskID != task.ID {
			t.Errorf("event %d: task id = %q, want %q", i, ev.TaskID, task.ID)
		}
	}
}

// TestHubAsWorkerEventSinkReportsFailure checks the error path of the same
// wiring: the structured error must survive the round trip.
func TestHubAsWorkerEventSinkReportsFailure(t *testing.T) {
	t.Parallel()
	hub := NewHub(16)
	sub := hub.Subscribe()
	t.Cleanup(sub.Unsubscribe)

	exec := engine.NewLocalExecutor(node.NewCapabilities(node.RoleStandalone),
		func(context.Context, *model.Task, chan<- model.StatusEvent) error {
			return errors.New("encoder exploded")
		})
	tm, err := engine.New(engine.Options{})
	if err != nil {
		t.Fatalf("engine.New() error = %v", err)
	}
	pool := engine.NewWorkerManager(exec, tm, nil)
	pool.SetEventSink(hub.Publish)
	pool.AddWorker(1)
	if _, err := pool.AddTask(&model.Task{ID: model.NewTaskID(), Name: "ep02"}, ""); err != nil {
		t.Fatalf("AddTask() error = %v", err)
	}
	if !pool.Start() {
		t.Fatal("Start() reported no worker")
	}
	t.Cleanup(pool.Stop)

	select {
	case msg := <-sub.Send():
		if msg.Event == nil || msg.Event.Progress != model.TaskError {
			t.Fatalf("event = %+v, want an ERROR event", msg.Event)
		}
		if msg.Event.Error == nil || msg.Event.Error.Summary == "" {
			t.Fatalf("error info = %+v, want a summary", msg.Event.Error)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no error event arrived")
	}
}
