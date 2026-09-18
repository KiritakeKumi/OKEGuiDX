// Package engine schedules tasks. This file defines the Executor boundary.
//
// The worker pool is written against Executor rather than against local process
// execution, so that a future cluster build only has to add a RemoteExecutor
// implementation instead of touching the scheduler (CLUSTER.md §2, reservation
// 1). The cost of the indirection is one interface; the cost of not having it is
// a rewrite of engine/.
package engine

import (
	"context"

	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/node"
)

// Executor runs tasks and reports their progress.
//
// Implementations must be safe for concurrent use: the worker pool calls Submit
// from several goroutines.
type Executor interface {
	// Capabilities reports what this executor is able to run. The scheduler
	// uses it to refuse impossible work early (for example an AAC task on a
	// node without qaac).
	Capabilities(ctx context.Context) (node.Capabilities, error)

	// Submit starts a task and returns a channel of status events. The channel
	// is closed when the task terminates. Submit returns as soon as the task is
	// accepted; it does not block until completion.
	Submit(ctx context.Context, t *model.Task) (<-chan model.StatusEvent, error)

	// Cancel stops a running task. Cancelling an unknown or finished task is
	// not an error.
	Cancel(ctx context.Context, taskID model.TaskID) error
}

// ExecutorFuncs adapts plain functions to an Executor, which keeps tests free of
// boilerplate.
type ExecutorFuncs struct {
	CapabilitiesFunc func(context.Context) (node.Capabilities, error)
	SubmitFunc       func(context.Context, *model.Task) (<-chan model.StatusEvent, error)
	CancelFunc       func(context.Context, model.TaskID) error
}

// Capabilities implements Executor.
func (f ExecutorFuncs) Capabilities(ctx context.Context) (node.Capabilities, error) {
	if f.CapabilitiesFunc == nil {
		return node.NewCapabilities(node.RoleStandalone), nil
	}
	return f.CapabilitiesFunc(ctx)
}

// Submit implements Executor.
func (f ExecutorFuncs) Submit(ctx context.Context, t *model.Task) (<-chan model.StatusEvent, error) {
	return f.SubmitFunc(ctx, t)
}

// Cancel implements Executor.
func (f ExecutorFuncs) Cancel(ctx context.Context, id model.TaskID) error {
	if f.CancelFunc == nil {
		return nil
	}
	return f.CancelFunc(ctx, id)
}

// RunFunc is the work an Executor performs for one task. It reports progress by
// sending on the channel and returns when the task is finished or ctx is
// canceled.
type RunFunc func(ctx context.Context, t *model.Task, events chan<- model.StatusEvent) error
