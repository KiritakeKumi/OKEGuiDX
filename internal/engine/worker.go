package engine

import (
	"cmp"
	"context"
	"slices"
	"strconv"
	"sync"

	"github.com/KiritakeKumi/OKEGuiDX/internal/log"
	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
	"github.com/KiritakeKumi/OKEGuiDX/internal/platform"
)

// WorkerType mirrors WorkerType in Worker/WorkerManager.cs.
type WorkerType int

// Worker kinds. The legacy enum started at Normal.
const (
	// WorkerNormal is a permanent worker, added by the operator.
	WorkerNormal WorkerType = iota
	// WorkerTemporary is created for a single task and released afterwards.
	WorkerTemporary
)

// String implements fmt.Stringer.
func (t WorkerType) String() string {
	if t == WorkerTemporary {
		return "Temporary"
	}
	return "Normal"
}

// statusTerminated is the text the legacy UI wrote when it stopped a task
// (MainWindow.BtnStop_Click: task.CurrentStatus = "已终止"). The pool writes it
// now that there is no UI.
const statusTerminated = "已终止"

// Worker is one slot in the pool: an identity, not an execution context. The
// legacy class carried exactly these three fields.
type Worker struct {
	// Wid is the worker number the legacy UI counted up from one.
	Wid int
	// Name is the display name, "工作单元-N" for a permanent worker and
	// "Temp-N" for a temporary one.
	Name string
	// WType tells a permanent worker from a temporary one.
	WType WorkerType
}

// poolState is the lifecycle state of the pool. The legacy code expressed the
// same situations with IsRunning plus the contents of bgworkerlist; the explicit
// state is what makes "the queue emptied" distinguishable from "the operator
// stopped the run", which matters because only the former may resume by itself
// when new work arrives.
type poolState int

const (
	// poolIdle is before the first Start, and after an explicit Stop.
	poolIdle poolState = iota
	// poolRunning means workers may take tasks.
	poolRunning
	// poolDrained means the last worker found an empty queue. IsRunning is
	// false, as in the legacy code, but a task added now resumes the pool
	// instead of stranding the queue.
	poolDrained
	// poolStopping means Stop was called; nothing new starts until Start.
	poolStopping
)

// String implements fmt.Stringer.
func (s poolState) String() string {
	switch s {
	case poolRunning:
		return "RUNNING"
	case poolDrained:
		return "DRAINED"
	case poolStopping:
		return "STOPPING"
	default:
		return "IDLE"
	}
}

// workerRun is the live part of a worker: one goroutine that keeps taking tasks
// until the queue drains or the pool stops. Its fields are guarded by
// WorkerManager.mu unless noted as immutable.
type workerRun struct {
	key    string
	worker Worker
	// ctx is the parent of the context handed to the Executor; cancelling it
	// cancels whatever task the worker is running.
	ctx    context.Context
	cancel context.CancelFunc
	// taskID and taskCancel describe the task the worker currently runs, empty
	// while it is between tasks.
	taskID     model.TaskID
	taskCancel context.CancelFunc
	// cancelled records that the pool stopped this task on purpose, in which
	// case the executor's own cancel event must not overwrite the "已终止"
	// state the pool wrote.
	cancelled bool
	// stopped is set by StopWorker: the worker finishes its current task and
	// then leaves the pool instead of taking another one.
	stopped bool
	// done is set by workerExit so the slot is released exactly once.
	done bool
}

// WorkerManager owns the worker pool: it decides how many tasks run at once and
// hands each one to an Executor. It is a port of Worker/WorkerManager.cs with
// the two legacy couplings removed (INVENTORY.md §2 #15, CLUSTER.md §2
// reservation 1):
//
//   - the pool holds an Executor rather than BackgroundWorkers, so a task may
//     run in this process today and on another node later without changing this
//     file. The pool never starts a process and never runs pipeline code;
//   - the MainWindow field is gone. The UI-facing half of the legacy class
//     (button states, the worker-count label, the AfterFinish delegate) is
//     reduced to optional callbacks the caller installs, and the pool no longer
//     reads the UI's WorkerCount to decide whether a worker still exists.
//
// All pool state is guarded by one mutex, like the legacy `lock (o)`. Neither
// the Executor nor a caller callback is ever invoked while that mutex is held.
type WorkerManager struct {
	exec Executor
	tm   *TaskManager
	numa *platform.Numa

	mu sync.Mutex
	// workers is keyed by Name. The legacy code used two key spaces, which is
	// why workerKey exists; see its comment.
	workers map[string]Worker
	// runs is one goroutine per worker name that is allowed to take work. It is
	// the equivalent of the legacy bgworkerlist: StopWorker removes an entry
	// here, which is what frees the slot for a replacement.
	runs map[string]*workerRun
	// liveRuns is every goroutine that exists, including ones removed from runs
	// by StopWorker. Shutdown waits for this set to empty.
	liveRuns map[*workerRun]struct{}
	// state is the lifecycle state; it replaces the legacy IsRunning field.
	state poolState
	// waiters are the channels Stop blocks on. Each is closed once the last
	// worker goroutine has finished.
	waiters []chan struct{}

	tempCounter int

	sink        func(model.StatusEvent)
	afterFinish func()
}

// NewWorkerManager returns a pool bound to an executor, a queue and a NUMA
// allocator. A nil allocator means this machine's NUMA nodes, matching the
// legacy NumaNode static; tests inject an explicit one so the assignment is
// deterministic. The pool is idle until Start.
func NewWorkerManager(exec Executor, tm *TaskManager, numa *platform.Numa) *WorkerManager {
	if numa == nil {
		numa = platform.NewNuma(false)
	}
	return &WorkerManager{
		exec:     exec,
		tm:       tm,
		numa:     numa,
		workers:  make(map[string]Worker),
		runs:     make(map[string]*workerRun),
		liveRuns: make(map[*workerRun]struct{}),
	}
}

// SetEventSink installs a callback that receives every status event the pool
// applies. It is what the WebSocket progress stream (E3) subscribes to; the
// legacy class had no such hook because the UI bound to the task objects
// directly. The callback runs on the worker goroutine, outside the pool lock,
// and must not block for long.
func (m *WorkerManager) SetEventSink(fn func(model.StatusEvent)) {
	m.mu.Lock()
	m.sink = fn
	m.mu.Unlock()
}

// SetAfterFinish installs the command the pool runs when the last worker finds
// the queue empty and every task finished. The legacy delegate took a MainWindow
// and was dispatched onto the UI thread; the callback now runs on the worker
// goroutine and must not assume a UI.
func (m *WorkerManager) SetAfterFinish(fn func()) {
	m.mu.Lock()
	m.afterFinish = fn
	m.mu.Unlock()
}

// AddTask appends a task to the queue and tries to start a worker for it.
// Ported from WorkerManager.AddTask; the queue call is the TaskManager port, so
// it returns the queue length and any persistence error instead of throwing.
//
// A task added after an explicit Stop stays waiting until Start is called
// again, exactly like the legacy code. A task added after the queue merely
// drained resumes the pool by itself; see TryStartNewWorker.
func (m *WorkerManager) AddTask(task *model.Task, configFilePath string) (int, error) {
	count, err := m.tm.AddTask(task, configFilePath)
	m.TryStartNewWorker()
	return count, err
}

// Start marks the pool as running and starts a worker for every waiting task,
// up to the number of registered workers. It reports false when no worker has
// been added, exactly like the legacy Start.
func (m *WorkerManager) Start() bool {
	m.mu.Lock()
	if len(m.workers) == 0 {
		m.mu.Unlock()
		return false
	}
	m.state = poolRunning
	m.mu.Unlock()

	m.TryStartNewWorker()
	return true
}

// Stop cancels every running task and waits for the worker goroutines to
// finish. The legacy Stop called BackgroundWorker.CancelAsync and returned
// immediately, leaving the actual processes to SubProcessService.KillAll() from
// the UI; a daemon has no such safety net, so this port waits until the pool is
// quiescent before returning.
//
// The wait is unbounded: a worker only exits once its Executor closes the event
// channel, which LocalExecutor always does. Use StopContext when a shutdown
// deadline is required.
func (m *WorkerManager) Stop() {
	if err := m.StopContext(context.Background()); err != nil {
		log.Warn("停止工作单元失败", "err", err)
	}
}

// StopContext is Stop with a deadline. It returns nil once every worker has
// exited, or ctx.Err() when the deadline passes first; workers whose executor
// never closes its event channel are then left behind, still winding down. A
// later Start brings the pool back regardless.
func (m *WorkerManager) StopContext(ctx context.Context) error {
	wait := m.beginStop()
	select {
	case <-wait:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// beginStop flips the pool off and cancels every worker, returning a channel
// closed once the last goroutine has exited. Cancellation happens outside the
// lock because a remote executor may take its time to answer.
func (m *WorkerManager) beginStop() <-chan struct{} {
	type target struct {
		taskID     model.TaskID
		taskCancel context.CancelFunc
		cancel     context.CancelFunc
	}

	m.mu.Lock()
	m.state = poolStopping
	wait := make(chan struct{})
	if len(m.liveRuns) == 0 {
		close(wait)
	} else {
		m.waiters = append(m.waiters, wait)
	}
	targets := make([]target, 0, len(m.liveRuns))
	for run := range m.liveRuns {
		run.cancelled = true
		targets = append(targets, target{run.taskID, run.taskCancel, run.cancel})
	}
	m.mu.Unlock()

	for _, t := range targets {
		if t.taskID.IsZero() {
			continue
		}
		m.cancelExecutorTask(t.taskID)
		// The legacy UI wrote "已终止" for every running task right after
		// calling Stop (MainWindow.BtnStop_Click); the pool owns that now.
		m.cancelTaskState(t.taskID)
		if t.taskCancel != nil {
			t.taskCancel()
		}
		t.cancel()
	}
	return wait
}

// IsRunning reports whether the pool is running. Like the legacy property it is
// false before the first Start, after an explicit Stop and after the queue has
// drained.
func (m *WorkerManager) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state == poolRunning
}

// TryStartNewWorker starts an idle worker for each waiting task, up to the
// number of registered workers, and reports whether it started any. Ported from
// WorkerManager.TryStartNewWorker: the active count is read once and decremented
// locally, so a task added concurrently is picked up by its own AddTask call.
//
// One deliberate difference: a pool that drained resumes here when work has
// arrived again. The legacy code returned false in that situation and left the
// queue waiting for the operator to press Run, which is right for a dialog but
// would strand a daemon that receives tasks over the API.
func (m *WorkerManager) TryStartNewWorker() bool {
	active := m.tm.GetActiveTaskCount()
	if active == 0 {
		return false
	}
	if m.resumeIfDrained() {
		log.Debug("队列中有新任务，工作单元继续运行")
	}

	started := false
	for active > 0 {
		run, ok := m.reserveWorker()
		if !ok {
			break
		}
		m.launchWorker(run)
		started = true
		active--
	}
	return started
}

// resumeIfDrained moves a drained pool back to running when a task is waiting.
// It reports whether the state changed.
func (m *WorkerManager) resumeIfDrained() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state != poolDrained {
		return false
	}
	m.state = poolRunning
	return true
}

// reserveWorker claims an idle worker slot for one task. It reports false when
// the pool is not running or every registered worker already has a goroutine.
func (m *WorkerManager) reserveWorker() (*workerRun, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state != poolRunning {
		return nil, false
	}
	for name, w := range m.workers {
		if _, busy := m.runs[name]; busy {
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		run := &workerRun{key: name, worker: w, ctx: ctx, cancel: cancel}
		m.runs[name] = run
		m.liveRuns[run] = struct{}{}
		return run, true
	}
	return nil, false
}

// launchWorker allocates the worker's NUMA node and starts its goroutine. The
// node assignment lives here because StartWorker did it (WorkerManager.cs:147)
// and because it is a property of the worker, not of the task it happens to
// pick up first.
func (m *WorkerManager) launchWorker(run *workerRun) {
	node := m.numa.NextNuma()
	log.Trace(run.worker.Name+"所拥有的Numa Node编号是"+strconv.Itoa(node),
		"worker", run.worker.Name, "numa_node", node)
	go m.runWorker(run, WithNUMANode(run.ctx, node))
}

// runWorker is the port of WorkerDoWork's loop: take a task, hand it to the
// executor, consume its events, repeat until the queue is empty or the pool
// stops. The task-specific work (the legacy ExecuteTaskService) is not here; it
// is the Executor's RunFunc.
func (m *WorkerManager) runWorker(run *workerRun, ctx context.Context) {
	reason := ""
	defer func() { m.workerExit(run, reason) }()

	for {
		if !m.workerShouldRun(run) {
			return
		}

		task, err := m.tm.GetNextTask()
		if err != nil {
			// GetNextTask returns the claimed task together with the error:
			// refusing to run it because the queue file is stale would stop the
			// run for no good reason.
			log.Warn("任务队列无法写入，任务仍继续执行", "worker", run.worker.Name, "err", err)
		}
		if task == nil {
			reason = "没有待运行的任务"
			return
		}

		m.processTask(ctx, run, task)

		if run.worker.WType == WorkerTemporary {
			// "临时Worker只运行一次任务" (WorkerManager.cs:171).
			m.releaseTempWorker(run)
			reason = "临时工作单元已释放"
			return
		}
	}
}

// workerShouldRun reports whether the loop may take another task.
func (m *WorkerManager) workerShouldRun(run *workerRun) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state != poolRunning || run.stopped {
		return false
	}
	// The legacy loop detected a deleted worker by comparing its Wid with
	// MainWindow.WorkerCount (ExecuteTaskService.cs:43). The pool is now the
	// authority, so it asks whether the worker still exists.
	return m.workers[run.key].Name != ""
}

// processTask hands one claimed task to the executor and consumes its status
// stream until the channel closes.
func (m *WorkerManager) processTask(ctx context.Context, run *workerRun, task *model.Task) {
	name := run.worker.Name
	m.markWorker(task.ID, name)

	taskCtx, taskCancel := context.WithCancel(ctx)
	if !m.beginTask(run, task.ID, taskCancel) {
		// StopWorker ran between claiming the task and registering it; the
		// worker is on its way out, so the task goes straight to the state a
		// stopped task has instead of being submitted.
		taskCancel()
		m.endTask(run)
		m.cancelTaskState(task.ID)
		if err := m.tm.ReleaseInput(task); err != nil {
			log.Warn("无法释放输入文件", "task", task.ID, "err", err)
		}
		return
	}

	events, err := m.exec.Submit(taskCtx, task)
	if err != nil {
		taskCancel()
		m.endTask(run)
		summary := okerr.AsError(err).Summary
		log.Error("无法提交任务", "worker", name, "task", task.ID, "err", err)
		m.failTask(task, summary)
		return
	}
	for ev := range events {
		m.handleEvent(run, ev)
	}
	taskCancel()
	m.endTask(run)

	// The legacy WorkerDoWork released the input file on every exit path, so a
	// failed task does not block the next task on the same source.
	if err := m.tm.ReleaseInput(task); err != nil {
		log.Warn("无法释放输入文件", "task", task.ID, "err", err)
	}
}

// beginTask records which task the worker is running and how to cancel it. It
// reports false when the pool stopped the worker while the task was being
// claimed, in which case the caller must not submit it: Stop cancels the
// worker's context, and a task submitted after that would never report back.
func (m *WorkerManager) beginTask(run *workerRun, id model.TaskID, cancel context.CancelFunc) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	run.taskID = id
	run.taskCancel = cancel
	if run.stopped || run.ctx.Err() != nil {
		run.cancelled = true
		return false
	}
	run.cancelled = false
	return true
}

// endTask clears the worker's current task.
func (m *WorkerManager) endTask(run *workerRun) {
	m.mu.Lock()
	run.taskID = ""
	run.taskCancel = nil
	m.mu.Unlock()
}

// markWorker records which worker owns the task, which is what the legacy code
// did with `task.WorkerName = args.Name` before running it.
func (m *WorkerManager) markWorker(id model.TaskID, name string) {
	err := m.tm.Update(id, func(t *model.Task) { t.Status.WorkerName = name })
	if err != nil {
		log.Debug("无法记录工作单元名称", "task", id, "err", err)
	}
}

// failTask returns a task the executor refused to its error state and releases
// its input, mirroring the legacy catch blocks.
func (m *WorkerManager) failTask(task *model.Task, summary string) {
	if err := m.tm.ReleaseInput(task); err != nil {
		log.Warn("无法释放输入文件", "task", task.ID, "err", err)
	}
	err := m.tm.Update(task.ID, func(t *model.Task) {
		t.Status.Progress = model.TaskError
		t.Status.Status = summary
	})
	if err != nil {
		log.Debug("无法更新任务状态", "task", task.ID, "err", err)
	}
}

// handleEvent applies one status event to the queue and forwards it to the sink.
func (m *WorkerManager) handleEvent(run *workerRun, ev model.StatusEvent) {
	if !m.applyEvent(run, ev) {
		return
	}
	m.mu.Lock()
	sink := m.sink
	m.mu.Unlock()
	if sink != nil {
		sink(ev)
	}
}

// applyEvent writes a status event into the queue and reports whether it was
// applied. Progress updates skip the disk write (UpdateProgress); terminal
// events persist, so a crash right after a task finished does not resurrect it
// as running.
//
// Every event of a task the pool cancelled itself is dropped, including ones
// the executor had already buffered: Stop and StopWorker wrote the legacy
// "已终止" state, and a late progress line must not turn the task back into a
// running one.
func (m *WorkerManager) applyEvent(run *workerRun, ev model.StatusEvent) bool {
	terminal := ev.Progress == model.TaskFinished || ev.Progress == model.TaskError

	m.mu.Lock()
	stale := !run.taskID.IsZero() && ev.TaskID != run.taskID
	suppressed := run.cancelled
	m.mu.Unlock()
	if stale || suppressed {
		return false
	}

	fn := func(t *model.Task) { applyStatus(t, ev) }
	var err error
	if terminal {
		err = m.tm.Update(ev.TaskID, fn)
	} else {
		err = m.tm.UpdateProgress(ev.TaskID, fn)
	}
	if err != nil {
		// A worker can outlive its task in the queue: the operator may delete a
		// finished task while the worker is still winding down.
		log.Debug("无法更新任务状态", "task", ev.TaskID, "err", err)
	}
	return true
}

// applyStatus copies an event onto a task's observable status. It is the
// mapping the legacy job classes performed directly on TaskStatus.
//
// The display fields are sticky: the terminal event an executor emits carries
// no speed or bitrate, and the legacy TaskStatus kept the last value that was
// set, so an empty string must not erase it.
func applyStatus(t *model.Task, ev model.StatusEvent) {
	t.Status.ID = ev.TaskID
	t.Status.Progress = ev.Progress
	switch {
	case ev.Error != nil && ev.Error.Summary != "":
		t.Status.Status = ev.Error.Summary
	case ev.Step != "":
		t.Status.Status = ev.Step
	}
	switch {
	case ev.Progress == model.TaskError && ev.Percent <= 0:
		// The legacy exception handler kept the last reported value when the
		// failure carried no progress of its own
		// (ex.progress.GetValueOrDefault(task.ProgressValue)), so a failed or
		// cancelled task does not fall back to 0.00%.
	case ev.Percent < 0:
		t.Status.ProgressValue = -1
		t.Status.ProgressUnknown = true
	default:
		t.Status.ProgressValue = ev.Percent
		t.Status.ProgressUnknown = false
	}
	if ev.Speed != "" {
		t.Status.Speed = ev.Speed
	}
	if ev.BitRate != "" {
		t.Status.BitRate = ev.BitRate
	}
	if ev.TimeRemainSeconds > 0 {
		t.Status.TimeRemainSeconds = ev.TimeRemainSeconds
	}
}

// workerExit releases a worker slot and lets the pool react to the exit. It is
// idempotent because the drain path and the deferred call can both reach it.
func (m *WorkerManager) workerExit(run *workerRun, reason string) {
	m.mu.Lock()
	if run.done {
		m.mu.Unlock()
		return
	}
	run.done = true
	delete(m.liveRuns, run)
	if current, ok := m.runs[run.key]; ok && current == run {
		delete(m.runs, run.key)
	}
	m.releaseWaitersLocked()
	last := len(m.runs) == 0
	m.mu.Unlock()

	run.cancel()
	if reason != "" {
		log.Debug(reason, "worker", run.worker.Name)
	}
	if last {
		m.finishRunIfIdle()
	}
}

// finishRunIfIdle ends the run when the last worker has left and nothing is
// left to do. The queue is consulted outside the pool lock, so a task that
// arrives concurrently is either seen here or started by its own AddTask call;
// the final transition re-checks the worker set under the lock, which is what
// closes the window between the two.
func (m *WorkerManager) finishRunIfIdle() {
	m.mu.Lock()
	if m.state != poolRunning || len(m.runs) > 0 {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()

	// A task may have arrived while the last worker was on its way out, in
	// which case restarting is exactly what AddTask would have done had the
	// worker still been registered. When nothing can be started — no free
	// worker is left — the run ends, as it did when the legacy bgworkerlist
	// emptied.
	if m.tm.GetActiveTaskCount() > 0 && m.TryStartNewWorker() {
		return
	}

	m.mu.Lock()
	if m.state != poolRunning || len(m.runs) > 0 {
		m.mu.Unlock()
		return
	}
	m.state = poolDrained
	after := m.afterFinish
	m.mu.Unlock()

	m.runAfterFinish(after)
}

// releaseWaitersLocked wakes every Stop that is waiting for the pool to become
// quiescent. Callers must hold m.mu.
func (m *WorkerManager) releaseWaitersLocked() {
	if len(m.liveRuns) > 0 {
		return
	}
	for _, w := range m.waiters {
		close(w)
	}
	m.waiters = nil
}

// runAfterFinish reports the end of a run and invokes the after-finish command
// only when every task finished.
func (m *WorkerManager) runAfterFinish(after func()) {
	if !m.tm.AllSuccess() {
		log.Info("有些任务未正常结束，不执行完结命令。")
		return
	}
	if after == nil {
		log.Info("全部任务正常结束；没有完结命令。")
		return
	}
	log.Info("全部任务正常结束；准备执行完结命令。")
	after()
}

// releaseTempWorker drops a temporary worker from the pool once its task is
// done. The legacy code left it in workerList forever and only stopped
// restarting it through the MainWindow.WorkerCount check, so every
// AddTempWorker leaked one entry.
func (m *WorkerManager) releaseTempWorker(run *workerRun) {
	m.mu.Lock()
	delete(m.workers, run.key)
	m.mu.Unlock()
}

// GetWorkerCount returns how many workers are registered, running or not.
func (m *WorkerManager) GetWorkerCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.workers)
}

// GetBGWorkerCount returns how many workers are currently allowed to take work.
// It mirrors the legacy method of the same name, which counted bgworkerlist,
// and is what the UI displayed in parentheses.
func (m *WorkerManager) GetBGWorkerCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.runs)
}

// Workers returns a snapshot of the registered workers, ordered by number.
func (m *WorkerManager) Workers() []Worker {
	m.mu.Lock()
	out := make([]Worker, 0, len(m.workers))
	for _, w := range m.workers {
		out = append(out, w)
	}
	m.mu.Unlock()

	slices.SortFunc(out, func(a, b Worker) int {
		if c := cmp.Compare(a.Wid, b.Wid); c != 0 {
			return c
		}
		return cmp.Compare(a.Name, b.Name)
	})
	return out
}

// AddWorker registers a permanent worker and tries to start it. It reports
// false when a worker with that number already exists, like the legacy method.
func (m *WorkerManager) AddWorker(wid int) bool {
	w := Worker{Wid: wid, Name: normalWorkerName(wid), WType: WorkerNormal}

	m.mu.Lock()
	if _, exists := m.workers[w.Name]; exists {
		m.mu.Unlock()
		return false
	}
	m.workers[w.Name] = w
	m.mu.Unlock()

	m.TryStartNewWorker()
	return true
}

// AddTempWorker registers a worker that runs one task and is then released, and
// returns its name. Ported from WorkerManager.AddTempWorker, with one
// deliberate difference: the legacy code started the new worker even when no
// task was waiting, and that worker immediately found an empty queue, removed
// itself and — if it had been the only live worker — ended the whole run and
// fired the after-finish command. Here the start goes through
// TryStartNewWorker, so an idle temporary worker waits for work instead of
// ending the run.
func (m *WorkerManager) AddTempWorker() string {
	m.mu.Lock()
	m.tempCounter++
	name := "Temp-" + strconv.Itoa(m.tempCounter)
	m.workers[name] = Worker{Wid: m.tempCounter, Name: name, WType: WorkerTemporary}
	m.mu.Unlock()

	m.TryStartNewWorker()
	return name
}

// DeleteWorker removes a worker from the pool. A worker that is running at that
// moment finishes its current task and is not started again, which is the
// closest safe equivalent of the legacy behaviour: the C# loop only noticed a
// deletion by comparing the worker number with MainWindow.WorkerCount.
//
// An unknown worker reports success, matching the legacy idempotent delete.
func (m *WorkerManager) DeleteWorker(name string) bool {
	key := workerKey(name)
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.workers, key)
	return true
}

// StopWorker cancels the task a worker is running and removes the worker from
// the schedulable set, so its slot is free for a replacement; the worker itself
// stays registered. Ported from WorkerManager.StopWorker.
//
// The legacy method only cancelled the BackgroundWorker; the pool also writes
// the "已终止" state the UI used to write after calling Stop.
func (m *WorkerManager) StopWorker(name string) {
	key := workerKey(name)

	m.mu.Lock()
	run, ok := m.runs[key]
	if ok {
		delete(m.runs, key)
		run.cancelled = true
		run.stopped = true
	}
	var taskID model.TaskID
	var taskCancel context.CancelFunc
	if ok {
		taskID, taskCancel = run.taskID, run.taskCancel
	}
	m.mu.Unlock()

	if !ok {
		return
	}
	if !taskID.IsZero() {
		m.cancelExecutorTask(taskID)
		m.cancelTaskState(taskID)
	}
	if taskCancel != nil {
		taskCancel()
	}
	log.Debug("已终止"+name, "worker", name)
}

// StopAllWorker cancels every worker that is allowed to take work, mirroring
// WorkerManager.StopAllWorker. The legacy method left the pool state alone;
// here the run also drains once the last cancelled worker has left, because
// nothing can be scheduled without a worker. The only legacy caller was Stop,
// which cleared IsRunning immediately afterwards anyway.
func (m *WorkerManager) StopAllWorker() {
	m.mu.Lock()
	names := make([]string, 0, len(m.runs))
	for name := range m.runs {
		names = append(names, name)
	}
	m.mu.Unlock()

	for _, name := range names {
		m.StopWorker(name)
	}
}

// cancelExecutorTask asks the executor to stop a task, logging a failure. The
// context handed to Submit is cancelled separately, so an executor that ignores
// Cancel still loses the task.
func (m *WorkerManager) cancelExecutorTask(id model.TaskID) {
	if err := m.exec.Cancel(context.Background(), id); err != nil {
		log.Warn("无法终止任务", "task", id, "err", err)
	}
}

// cancelTaskState writes the state the legacy UI applied after a stop: the task
// is an error whose status is "已终止".
func (m *WorkerManager) cancelTaskState(id model.TaskID) {
	err := m.tm.Update(id, func(t *model.Task) {
		t.Status.Progress = model.TaskError
		t.Status.Status = statusTerminated
	})
	if err != nil {
		log.Debug("无法更新任务状态", "task", id, "err", err)
	}
}

// normalWorkerName is the display name the legacy code built with
// $"工作单元-{Wid}" (WorkerManager.cs:199).
func normalWorkerName(wid int) string {
	return "工作单元-" + strconv.Itoa(wid)
}

// workerKey maps every identifier the legacy API accepted onto the pool's one
// key space. WorkerManager kept two: workerList was keyed by Wid.ToString() for
// permanent workers but by Name for temporary ones (WorkerManager.cs:183 vs
// :209), while bgworkerlist — and therefore StopWorker — was always keyed by
// Name. Keeping one key removes the trap where DeleteWorker("工作单元-1")
// silently succeeded without deleting anything; the numeric form the UI used,
// DeleteWorker(WorkerCount.ToString()), still resolves.
func workerKey(id string) string {
	if wid, err := strconv.Atoi(id); err == nil {
		return normalWorkerName(wid)
	}
	return id
}

// numaNodeKey is the context key carrying the NUMA node a worker was assigned.
type numaNodeKey struct{}

// WithNUMANode returns a context carrying the NUMA node assigned to a worker.
//
// The legacy WorkerArgs carried numaNode into the video job, which used it for
// x265 --pools. The Executor boundary is frozen and takes only a task, so the
// context is the channel that survives it: an Executor's RunFunc, and through
// it the pipeline, reads the node back with NUMANodeFrom.
func WithNUMANode(ctx context.Context, node int) context.Context {
	return context.WithValue(ctx, numaNodeKey{}, node)
}

// NUMANodeFrom returns the NUMA node assigned to the worker running this task.
// ok is false when the context did not come from the worker pool, in which case
// callers should fall back to their own default. This is what replaces the
// legacy WorkerArgs.numaNode field across the Executor boundary.
func NUMANodeFrom(ctx context.Context) (int, bool) {
	node, ok := ctx.Value(numaNodeKey{}).(int)
	return node, ok
}
