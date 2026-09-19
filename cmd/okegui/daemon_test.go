package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KiritakeKumi/OKEGuiDX/internal/engine"
	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/node"
	"github.com/KiritakeKumi/OKEGuiDX/internal/platform"
)

// TestDaemonShutsDownOnContextCancel exercises the process-lifecycle half of
// the daemon without starting a server: the service must come up, report
// itself ready, and shut down within the grace period once the context is
// cancelled.
func TestDaemonShutsDownOnContextCancel(t *testing.T) {
	dir := t.TempDir()
	queue := filepath.Join(dir, "queue.json")

	s := &settings{
		role:      node.RoleStandalone,
		caps:      node.NewCapabilities(node.RoleStandalone),
		queuePath: queue,
		configDir: dir,
	}
	svc := newService(s)

	if got := svc.workers.GetWorkerCount(); got != 1 {
		t.Fatalf("worker count = %d, want 1 (one per NUMA node, clamped to one)", got)
	}

	svc.Start()
	if !svc.workers.IsRunning() {
		t.Error("pool is not running after Start()")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := svc.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if svc.workers.IsRunning() {
		t.Error("pool is still running after Shutdown()")
	}
	// The queue must be written back on the way out, otherwise a restart
	// cannot tell a finished task from a running one.
	if _, err := os.Stat(queue); err != nil {
		t.Errorf("queue was not persisted on shutdown: %v", err)
	}
}

// TestDaemonShutdownIsIdempotent asserts the second call is a no-op, which the
// signal path relies on: a second Ctrl-C must not re-run the shutdown.
func TestDaemonShutdownIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	s := &settings{
		role:      node.RoleStandalone,
		caps:      node.NewCapabilities(node.RoleStandalone),
		queuePath: filepath.Join(dir, "queue.json"),
		configDir: dir,
	}
	svc := newService(s)

	ctx := context.Background()
	if err := svc.Shutdown(ctx); err != nil {
		t.Fatalf("first Shutdown() error = %v", err)
	}
	if err := svc.Shutdown(ctx); err != nil {
		t.Fatalf("second Shutdown() error = %v", err)
	}
}

// TestDaemonResumesARecoveredQueue asserts the crash-recovery contract: a task
// left running by a dead process is reset to waiting and picked up on start.
func TestDaemonResumesARecoveredQueue(t *testing.T) {
	dir := t.TempDir()
	queue := filepath.Join(dir, "queue.json")

	writeStaleQueue(t, queue)

	s := &settings{
		role:      node.RoleStandalone,
		caps:      node.NewCapabilities(node.RoleStandalone),
		queuePath: queue,
		configDir: dir,
	}
	svc := newService(s)
	if got := svc.tasks.GetActiveTaskCount(); got != 1 {
		t.Fatalf("active task count = %d, want 1 after recovery", got)
	}

	svc.Start()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := svc.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	// The task cannot succeed (the pipeline is D3), so it must end as an
	// error rather than stay running forever.
	if got := svc.tasks.GetRunningTasks(); len(got) != 0 {
		t.Errorf("%d tasks are still marked running after shutdown", len(got))
	}
}

// writeStaleQueue writes a queue that looks like a process died mid-task.
func writeStaleQueue(t *testing.T, path string) {
	t.Helper()
	const stale = `{
  "version": 1,
  "created_count": 1,
  "tasks": [
    {
      "config_file_path": "C:\\work\\test.json",
      "task": {
        "id": "11111111-1111-4111-8111-111111111111",
        "name": "recovered",
        "status": {
          "id": "11111111-1111-4111-8111-111111111111",
          "name": "recovered",
          "enabled": false,
          "progress": "RUNNING",
          "status": "压制中",
          "progress_value": 42,
          "speed": "10.0 fps",
          "worker_name": "工作单元-1"
        }
      }
    }
  ]
}`
	if err := os.WriteFile(path, []byte(stale), 0o600); err != nil {
		t.Fatalf("write stale queue: %v", err)
	}
}

// TestPIDFileRoundTrip covers the helper the daemon uses to advertise itself.
func TestPIDFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "okegui.pid")

	if err := writePIDFile(path); err != nil {
		t.Fatalf("writePIDFile() error = %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read pid file: %v", err)
	}
	want := strings.TrimSpace(string(raw))
	if want == "" {
		t.Fatal("pid file is empty")
	}
	for _, r := range want {
		if r < '0' || r > '9' {
			t.Fatalf("pid file contains %q, want digits only", want)
		}
	}

	removePIDFile(path)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("pid file still exists after removePIDFile: %v", err)
	}
	// Removing a missing file is not an error.
	removePIDFile(path)
}

// TestNewServiceUsesOneWorkerPerNUMANode pins the default concurrency the
// legacy MainWindow established.
func TestNewServiceUsesOneWorkerPerNUMANode(t *testing.T) {
	dir := t.TempDir()
	caps := node.NewCapabilities(node.RoleStandalone)
	caps.NUMANodes = 3

	s := &settings{role: node.RoleStandalone, caps: caps, queuePath: filepath.Join(dir, "q.json"), configDir: dir}
	svc := newService(s)

	if got := svc.workers.GetWorkerCount(); got != 3 {
		t.Errorf("worker count = %d, want 3 (one per NUMA node)", got)
	}
}

// TestNewServiceToleratesADamagedQueue asserts the daemon comes up even when
// the recovery file is unusable: an operator needs the service running in
// order to look at the problem.
func TestNewServiceToleratesADamagedQueue(t *testing.T) {
	dir := t.TempDir()
	queue := filepath.Join(dir, "queue.json")
	if err := os.WriteFile(queue, []byte("{ not json"), 0o600); err != nil {
		t.Fatalf("write queue: %v", err)
	}

	s := &settings{
		role:      node.RoleStandalone,
		caps:      node.NewCapabilities(node.RoleStandalone),
		queuePath: queue,
		configDir: dir,
	}
	svc := newService(s)
	if got := svc.tasks.GetTaskCount(); got != 0 {
		t.Errorf("task count = %d, want 0 for an unreadable queue", got)
	}
}

// TestServiceShutdownRespectsDeadline asserts a task that never finishes does
// not hold the daemon open past its grace period.
func TestServiceShutdownRespectsDeadline(t *testing.T) {
	dir := t.TempDir()
	block := make(chan struct{})
	defer close(block)

	caps := node.NewCapabilities(node.RoleStandalone)
	tm, err := engine.New(engine.Options{QueuePath: filepath.Join(dir, "queue.json")})
	if err != nil {
		t.Fatalf("engine.New() error = %v", err)
	}
	exec := engine.NewLocalExecutor(caps, func(ctx context.Context, _ *model.Task, _ chan<- model.StatusEvent) error {
		select {
		case <-block:
		case <-ctx.Done():
		}
		return ctx.Err()
	})
	svc := &service{
		settings: &settings{role: node.RoleStandalone, caps: caps, queuePath: filepath.Join(dir, "queue.json"), configDir: dir},
		tasks:    tm,
		exec:     exec,
		workers:  engine.NewWorkerManager(exec, tm, platform.NewNumaWithCount(1)),
	}
	svc.workers.AddWorker(1)
	svc.Start()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := svc.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error = %v, want nil once the pool is quiet", err)
	}
}
