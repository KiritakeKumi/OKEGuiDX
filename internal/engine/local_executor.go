package engine

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/node"
)

// LocalExecutor runs tasks in this process. It is the only implementation in
// this rewrite; RemoteExecutor is the reserved future addition (CLUSTER.md §2).
//
// The executor owns no scheduling policy: it accepts a task, runs it and streams
// events. Deciding which task runs where is the scheduler's job, which keeps the
// two concerns independently replaceable.
type LocalExecutor struct {
	caps node.Capabilities
	run  RunFunc

	mu      sync.Mutex
	running map[model.TaskID]context.CancelFunc
	wg      sync.WaitGroup
}

// NewLocalExecutor returns an executor bound to the given capabilities and work
// function.
func NewLocalExecutor(caps node.Capabilities, run RunFunc) *LocalExecutor {
	return &LocalExecutor{
		caps:    caps,
		run:     run,
		running: make(map[model.TaskID]context.CancelFunc),
	}
}

// Capabilities implements Executor.
func (e *LocalExecutor) Capabilities(context.Context) (node.Capabilities, error) {
	return e.caps, nil
}

// Submit implements Executor. The returned channel is buffered so a task that
// reports progress faster than the consumer can read does not block; when the
// buffer fills, further events are dropped rather than stalling the encode.
func (e *LocalExecutor) Submit(ctx context.Context, t *model.Task) (<-chan model.StatusEvent, error) {
	if t == nil {
		return nil, errors.New("engine: cannot submit a nil task")
	}

	e.mu.Lock()
	if _, dup := e.running[t.ID]; dup {
		e.mu.Unlock()
		return nil, errors.New("engine: task is already running: " + t.ID.String())
	}
	runCtx, cancel := context.WithCancel(ctx)
	e.running[t.ID] = cancel
	e.mu.Unlock()

	events := make(chan model.StatusEvent, 64)
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		defer close(events)
		defer func() {
			e.mu.Lock()
			delete(e.running, t.ID)
			e.mu.Unlock()
			cancel()
		}()

		err := e.run(runCtx, t, events)
		final := model.StatusEvent{TaskID: t.ID}
		switch {
		case err == nil:
			final.Progress = model.TaskFinished
			final.Percent = 100
		case errors.Is(err, context.Canceled):
			final.Progress = model.TaskError
			final.Error = &model.ErrorInfo{Summary: "任务已取消", Detail: err.Error()}
		default:
			final.Progress = model.TaskError
			final.Error = toErrorInfo(err)
		}
		// The final event is sent on a best-effort basis: if the consumer has
		// gone away, the task is already done and nothing is lost.
		select {
		case events <- final:
		case <-time.After(time.Second):
		}
	}()
	return events, nil
}

// Cancel implements Executor.
func (e *LocalExecutor) Cancel(_ context.Context, taskID model.TaskID) error {
	e.mu.Lock()
	cancel, ok := e.running[taskID]
	e.mu.Unlock()
	if !ok {
		return nil
	}
	cancel()
	return nil
}

// Running reports how many tasks are currently executing.
func (e *LocalExecutor) Running() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.running)
}

// Wait blocks until every submitted task has finished. It is used by shutdown
// and by tests.
func (e *LocalExecutor) Wait() { e.wg.Wait() }

// toErrorInfo converts any error into its serializable form.
func toErrorInfo(err error) *model.ErrorInfo {
	type structured interface {
		Error() string
	}
	var se structured
	if errors.As(err, &se) {
		return &model.ErrorInfo{Summary: se.Error()}
	}
	return &model.ErrorInfo{Summary: "未知错误", Detail: err.Error()}
}
