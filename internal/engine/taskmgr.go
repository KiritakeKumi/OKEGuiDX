package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KiritakeKumi/OKEGuiDX/internal/log"
	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
)

// TaskManager is the task queue. Its behaviour is defined by the legacy
// Task/TaskManager.cs; the differences are structural:
//
//   - the MTObservableCollection is gone. The queue is a slice guarded by a
//     mutex, and observers are notified through Options.OnChange, which runs
//     outside the lock;
//   - a task is identified by its model.TaskID (a UUID) rather than by an
//     object reference, so lookups survive a restart;
//   - the queue is persisted as JSON, which is what gives the standalone engine
//     crash recovery (CLUSTER.md §2, reservation 3).
//
// The manager owns state only. It never runs anything: the worker pool picks a
// task with GetNextTask and hands it to an Executor (executor.go).
type TaskManager struct {
	path     string
	onChange func([]model.Task)

	mu            sync.Mutex
	notifyMu      sync.Mutex
	tasks         []queueEntry
	runningInputs map[string]struct{}
	createdCount  int
}

// Options configures a TaskManager.
type Options struct {
	// QueuePath is the JSON file the queue is persisted to. Empty disables
	// persistence, which is useful for tests and for a purely in-memory run.
	QueuePath string
	// OnChange, when set, is called after every mutation with a snapshot of the
	// queue. It runs outside the queue lock, but callbacks are serialized, so a
	// callback must not call back into the manager (that would deadlock).
	OnChange func([]model.Task)
}

// MoveTopResult is the outcome of MoveTaskTop. It mirrors the legacy
// TaskManager.MoveTaskTopResult enum.
type MoveTopResult int

// Move results.
const (
	// MoveTopOK means the task was moved to the first waiting slot.
	MoveTopOK MoveTopResult = iota
	// MoveTopAlready means the task already occupied the first waiting slot.
	MoveTopAlready
	// MoveTopFailure means the task is unknown or not waiting.
	MoveTopFailure
)

// String implements fmt.Stringer.
func (r MoveTopResult) String() string {
	switch r {
	case MoveTopOK:
		return "OK"
	case MoveTopAlready:
		return "Already"
	default:
		return "Failure"
	}
}

// Values applied by AddTask, copied from TaskManager.AddTask.
const (
	statusWaiting = "等待中"
	defaultSpeed  = "0.0 fps"
	// defaultTimeRemainSeconds is TimeSpan.FromDays(30) from the legacy code.
	defaultTimeRemainSeconds = 30 * 24 * 60 * 60
	// queueFormatVersion is the on-disk queue schema version.
	queueFormatVersion = 1
)

// queueEntry is one task in the persisted queue. The legacy TaskDetail carried
// the configuration path inside Taskfile.ConfigFilePath; model.Task is
// profile-agnostic, so the manager keeps the path next to the task.
type queueEntry struct {
	ConfigFilePath string     `json:"config_file_path"`
	Task           model.Task `json:"task"`
}

// queueFile is the on-disk format. The version is checked on load so a future
// layout cannot be silently misread.
type queueFile struct {
	Version      int          `json:"version"`
	CreatedCount int          `json:"created_count"`
	Tasks        []queueEntry `json:"tasks"`
}

// New creates a task manager. When opts.QueuePath is set and the file exists,
// the queue is read back, which is what gives the engine crash recovery.
//
// A corrupt or unreadable queue file is reported as an error, but the returned
// manager is still usable and simply starts empty. Callers that want to keep a
// damaged file should copy it away before the next mutation overwrites it.
func New(opts Options) (*TaskManager, error) {
	m := &TaskManager{
		path:          opts.QueuePath,
		onChange:      opts.OnChange,
		runningInputs: make(map[string]struct{}),
	}
	if m.path == "" {
		return m, nil
	}
	if err := m.load(); err != nil {
		return m, err
	}
	return m, nil
}

// load reads the persisted queue. Tasks that were running when the previous
// process died are reset to waiting: the process that owned them is gone, so
// the only useful recovery is to make them runnable again. The legacy version
// lost the whole queue on exit, so any recovery at all is an improvement.
func (m *TaskManager) load() error {
	raw, err := os.ReadFile(m.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return okerr.Wrap(err, okerr.KindIO, "无法读取任务队列", "%s: %v", m.path, err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}

	var qf queueFile
	if err := json.Unmarshal(raw, &qf); err != nil {
		return okerr.Wrap(err, okerr.KindIO, "任务队列文件损坏", "%s: %v", m.path, err)
	}
	if qf.Version > queueFormatVersion {
		return okerr.New(okerr.KindConfig, "任务队列版本过新",
			"%s 的格式版本为 %d，本程序只支持到 %d。", m.path, qf.Version, queueFormatVersion)
	}

	m.tasks = qf.Tasks
	m.createdCount = max(qf.CreatedCount, len(m.tasks))
	if repaired := m.repair(); repaired > 0 {
		log.Warn("任务队列包含无效状态，已自动修复", "path", m.path, "tasks", repaired)
		if err := m.saveLocked(); err != nil {
			return err
		}
	}
	return nil
}

// repair fixes entries that cannot be used as-is: missing or duplicated task
// ids, a status id that does not match the task id, and tasks that were running
// when the previous process died. It returns how many tasks it touched.
func (m *TaskManager) repair() int {
	repaired := 0
	seen := make(map[model.TaskID]struct{}, len(m.tasks))
	for i := range m.tasks {
		t := &m.tasks[i].Task
		fixed := false
		if t.ID.IsZero() {
			t.ID = model.NewTaskID()
			fixed = true
		}
		if _, dup := seen[t.ID]; dup {
			t.ID = model.NewTaskID()
			fixed = true
		}
		seen[t.ID] = struct{}{}
		if t.Status.ID != t.ID {
			t.Status.ID = t.ID
			fixed = true
		}
		if t.Status.Progress == model.TaskRunning {
			// The worker that owned this task is gone with the previous
			// process. Return it to the queue in the state a freshly added
			// task has, otherwise a stale "压制中 42%" would be shown until
			// the worker overwrites every field again.
			t.Status.Progress = model.TaskWaiting
			t.Status.Enabled = true
			t.Status.Status = statusWaiting
			t.Status.ProgressValue = 0
			t.Status.ProgressUnknown = false
			t.Status.Speed = defaultSpeed
			t.Status.BitRate = ""
			t.Status.WorkerName = ""
			t.Status.TimeRemainSeconds = defaultTimeRemainSeconds
			fixed = true
		}
		if fixed {
			repaired++
		}
	}
	return repaired
}

// Save writes the queue to disk atomically. It is a no-op when the manager was
// created without a queue path.
func (m *TaskManager) Save() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saveLocked()
}

// saveLocked serializes the queue and replaces the file in one rename, so a
// crash never leaves a half-written queue behind. Callers must hold m.mu.
func (m *TaskManager) saveLocked() error {
	if m.path == "" {
		return nil
	}
	qf := queueFile{
		Version:      queueFormatVersion,
		CreatedCount: m.createdCount,
		Tasks:        m.tasks,
	}
	data, err := json.MarshalIndent(qf, "", "  ")
	if err != nil {
		return okerr.Wrap(err, okerr.KindUnknown, "无法序列化任务队列", "%v", err)
	}
	return writeFileAtomic(m.path, append(data, '\n'))
}

// writeFileAtomic writes data to path through a temporary file in the same
// directory followed by a rename. Rename is atomic within one filesystem, so a
// reader sees either the old queue or the new one.
func writeFileAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return okerr.Wrap(err, okerr.KindIO, "无法写入任务队列", "%s: %v", path, err)
	}
	tmp := f.Name()
	// The temporary file is removed on every path that does not rename it.
	defer func() {
		if tmp != "" {
			_ = os.Remove(tmp)
		}
	}()

	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return okerr.Wrap(err, okerr.KindIO, "无法写入任务队列", "%s: %v", path, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return okerr.Wrap(err, okerr.KindIO, "无法写入任务队列", "%s: %v", path, err)
	}
	if err := f.Close(); err != nil {
		return okerr.Wrap(err, okerr.KindIO, "无法写入任务队列", "%s: %v", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return okerr.Wrap(err, okerr.KindIO, "无法写入任务队列", "%s: %v", path, err)
	}
	tmp = ""
	return nil
}

// notify delivers a snapshot to the change callback, if any. It deliberately
// runs outside the queue lock: a callback that renders a UI or writes to a
// WebSocket must not stall every queue operation.
func (m *TaskManager) notify() {
	if m.onChange == nil {
		return
	}
	m.notifyMu.Lock()
	defer m.notifyMu.Unlock()
	m.mu.Lock()
	snap := m.snapshotLocked()
	m.mu.Unlock()
	m.onChange(snap)
}

// commit publishes a mutation that has already been applied: observers are
// notified and the queue is written back to disk.
func (m *TaskManager) commit() error {
	m.notify()
	return m.Save()
}

// snapshotLocked returns independent copies of every queued task. Callers must
// hold m.mu.
func (m *TaskManager) snapshotLocked() []model.Task {
	out := make([]model.Task, 0, len(m.tasks))
	for i := range m.tasks {
		out = append(out, cloneTask(&m.tasks[i].Task))
	}
	return out
}

// cloneTask copies the task and the slices it owns. Profile and Config are
// shared as-is: they are opaque to this package and callers must treat them as
// read-only.
func cloneTask(t *model.Task) model.Task {
	out := *t
	out.Inputs = slices.Clone(t.Inputs)
	out.AudioTracks = slices.Clone(t.AudioTracks)
	out.SubtitleTracks = slices.Clone(t.SubtitleTracks)
	out.SliceParts = slices.Clone(t.SliceParts)
	for i := range out.AudioTracks {
		if q := t.AudioTracks[i].Quality; q != nil {
			v := *q
			out.AudioTracks[i].Quality = &v
		}
	}
	return out
}

// Snapshot returns copies of every task in queue order. Mutating the result
// does not affect the queue.
func (m *TaskManager) Snapshot() []model.Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshotLocked()
}

// Task returns a copy of one task and whether it is in the queue.
func (m *TaskManager) Task(id model.TaskID) (model.Task, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if idx := m.indexOfLocked(id); idx >= 0 {
		return cloneTask(&m.tasks[idx].Task), true
	}
	return model.Task{}, false
}

// ConfigPath returns the configuration file the task was created from. It is
// what a caller needs to re-read the profile after a restart, because Profile
// and Config come back from JSON as generic values.
func (m *TaskManager) ConfigPath(id model.TaskID) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if idx := m.indexOfLocked(id); idx >= 0 {
		return m.tasks[idx].ConfigFilePath, true
	}
	return "", false
}

// HasActiveTask reports whether a waiting or running task already uses the same
// configuration and input file. The comparison mirrors TaskIdentity: the config
// path is normalized like FileInfo.FullName and compared case-sensitively; the
// input is compared as a logical reference (volume plus cleaned relative path).
func (m *TaskManager) HasActiveTask(configFilePath string, input model.FileRef) bool {
	identity := taskIdentity(configFilePath, input)
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.tasks {
		t := &m.tasks[i].Task
		if t.Status.Progress != model.TaskWaiting && t.Status.Progress != model.TaskRunning {
			continue
		}
		if taskIdentity(m.tasks[i].ConfigFilePath, t.Status.Input) == identity {
			return true
		}
	}
	return false
}

// AddTask appends a task, applying the same defaults as the legacy method. The
// task is copied, so later changes to the caller's value do not affect the
// queue. The returned int is the queue length, matching TaskManager.AddTask.
//
// A task id supplied by the caller is preserved (that is what lets a recovered
// or remote task keep its identity); otherwise a fresh UUID is assigned. The
// legacy code always overwrote the id with a fresh counter value.
func (m *TaskManager) AddTask(task *model.Task, configFilePath string) (int, error) {
	if task == nil {
		return 0, okerr.New(okerr.KindUnknown, "无法添加任务", "任务为空。")
	}

	m.mu.Lock()
	if !task.ID.IsZero() {
		if m.indexOfLocked(task.ID) >= 0 {
			m.mu.Unlock()
			return 0, okerr.New(okerr.KindConfig, "无法添加任务", "任务 %s 已在队列中。", task.ID)
		}
	}
	m.createdCount++
	stored := cloneTask(task)
	if stored.ID.IsZero() {
		stored.ID = model.NewTaskID()
	}
	stored.Status.ID = stored.ID
	if stored.Status.Input.IsZero() && len(stored.Inputs) > 0 {
		// Mirrors profile.ToModel, which derives the display input from the
		// first input reference.
		stored.Status.Input = stored.Inputs[0]
	}
	if stored.Name == "" {
		stored.Name = "新建任务 - " + strconv.Itoa(m.createdCount)
	}
	stored.Status.Enabled = true
	stored.Status.Progress = model.TaskWaiting
	stored.Status.Status = statusWaiting
	stored.Status.ProgressValue = 0
	stored.Status.ProgressUnknown = false
	stored.Status.Speed = defaultSpeed
	stored.Status.TimeRemainSeconds = defaultTimeRemainSeconds
	stored.Status.WorkerName = ""
	if stored.CreatedAt == 0 {
		stored.CreatedAt = time.Now().Unix()
	}
	m.tasks = append(m.tasks, queueEntry{ConfigFilePath: configFilePath, Task: stored})
	count := len(m.tasks)
	m.mu.Unlock()

	if err := m.commit(); err != nil {
		return count, err
	}
	return count, nil
}

// DeleteTask removes a task by id. A running task cannot be deleted, mirroring
// the legacy check. The bool reports whether the task was removed.
func (m *TaskManager) DeleteTask(id model.TaskID) (bool, error) {
	m.mu.Lock()
	idx := m.indexOfLocked(id)
	if idx < 0 || m.tasks[idx].Task.Status.Progress == model.TaskRunning {
		m.mu.Unlock()
		return false, nil
	}
	m.removeLocked(idx)
	m.mu.Unlock()

	if err := m.commit(); err != nil {
		return true, err
	}
	return true, nil
}

// removeLocked deletes the entry at idx and clears its slot so the removed task
// is not kept alive by the backing array. Callers must hold m.mu.
func (m *TaskManager) removeLocked(idx int) {
	copy(m.tasks[idx:], m.tasks[idx+1:])
	last := len(m.tasks) - 1
	m.tasks[last] = queueEntry{}
	m.tasks = m.tasks[:last]
}

// SetEnabled turns a task on or off. Like the legacy IsEnabled property, the
// change is silently ignored while the task is running.
func (m *TaskManager) SetEnabled(id model.TaskID, enabled bool) error {
	m.mu.Lock()
	idx := m.indexOfLocked(id)
	if idx < 0 {
		m.mu.Unlock()
		return okerr.New(okerr.KindNotFound, "找不到任务", "任务 %s 不在队列中。", id)
	}
	t := &m.tasks[idx].Task
	if t.Status.Progress == model.TaskRunning || t.Status.Enabled == enabled {
		m.mu.Unlock()
		return nil
	}
	t.Status.Enabled = enabled
	m.mu.Unlock()
	return m.commit()
}

// canMoveLocked reports whether SwapTasksByIndex would accept the two
// positions: both must exist and both tasks must still be waiting.
func (m *TaskManager) canMoveLocked(from, to int) bool {
	if from == to || from < 0 || from >= len(m.tasks) || to < 0 || to >= len(m.tasks) {
		return false
	}
	return m.tasks[from].Task.Status.Progress == model.TaskWaiting &&
		m.tasks[to].Task.Status.Progress == model.TaskWaiting
}

// moveLocked moves the entry at from to position to, shifting the entries in
// between, exactly like ObservableCollection.Move.
func (m *TaskManager) moveLocked(from, to int) {
	e := m.tasks[from]
	if from < to {
		copy(m.tasks[from:to], m.tasks[from+1:to+1])
	} else {
		copy(m.tasks[to+1:from+1], m.tasks[to:from])
	}
	m.tasks[to] = e
}

// MoveTaskUp moves a waiting task one slot towards the front. It reports false
// when the task is unknown, already first, or not waiting.
func (m *TaskManager) MoveTaskUp(id model.TaskID) (bool, error) {
	m.mu.Lock()
	idx := m.indexOfLocked(id)
	if !m.canMoveLocked(idx, idx-1) {
		m.mu.Unlock()
		return false, nil
	}
	m.moveLocked(idx, idx-1)
	m.mu.Unlock()

	if err := m.commit(); err != nil {
		return true, err
	}
	return true, nil
}

// MoveTaskDown moves a waiting task one slot towards the back. It reports false
// when the task is unknown, already last, or not waiting.
func (m *TaskManager) MoveTaskDown(id model.TaskID) (bool, error) {
	m.mu.Lock()
	idx := m.indexOfLocked(id)
	if !m.canMoveLocked(idx, idx+1) {
		m.mu.Unlock()
		return false, nil
	}
	m.moveLocked(idx, idx+1)
	m.mu.Unlock()

	if err := m.commit(); err != nil {
		return true, err
	}
	return true, nil
}

// MoveTaskTop moves a waiting task to the first waiting slot, ahead of any
// running or finished tasks. Mirrors TaskManager.MoveTaskTop, except that an
// unknown task reports Failure instead of throwing.
func (m *TaskManager) MoveTaskTop(id model.TaskID) (MoveTopResult, error) {
	m.mu.Lock()
	idx := m.indexOfLocked(id)
	if idx < 0 || m.tasks[idx].Task.Status.Progress != model.TaskWaiting {
		m.mu.Unlock()
		return MoveTopFailure, nil
	}
	first := 0
	for first < len(m.tasks) && m.tasks[first].Task.Status.Progress != model.TaskWaiting {
		first++
	}
	if idx == first {
		m.mu.Unlock()
		return MoveTopAlready, nil
	}
	m.moveLocked(idx, first)
	m.mu.Unlock()

	if err := m.commit(); err != nil {
		return MoveTopOK, err
	}
	return MoveTopOK, nil
}

// GetNextTask claims the first enabled waiting task whose input file is not
// already being processed, marks it running and returns a copy. It returns
// (nil, nil) when nothing is runnable.
//
// When the queue file cannot be written the task is still claimed and returned
// together with the error: refusing to start work because the recovery file is
// stale would be worse.
func (m *TaskManager) GetNextTask() (*model.Task, error) {
	m.mu.Lock()
	var picked *model.Task
	for i := range m.tasks {
		t := &m.tasks[i].Task
		if !t.Status.Enabled || t.Status.Progress != model.TaskWaiting {
			continue
		}
		key := inputMutexKey(t.Status.Input)
		if key != "" {
			if _, busy := m.runningInputs[key]; busy {
				continue
			}
			m.runningInputs[key] = struct{}{}
		}
		t.Status.Enabled = false
		t.Status.Progress = model.TaskRunning
		cp := cloneTask(t)
		picked = &cp
		break
	}
	m.mu.Unlock()

	if picked == nil {
		return nil, nil
	}
	if err := m.commit(); err != nil {
		return picked, err
	}
	return picked, nil
}

// ReleaseInput frees the input file claimed by GetNextTask. It mirrors the
// legacy method: a nil task or an empty input is a no-op.
func (m *TaskManager) ReleaseInput(task *model.Task) error {
	if task == nil {
		return nil
	}
	key := inputMutexKey(task.Status.Input)
	if key == "" {
		return nil
	}

	m.mu.Lock()
	_, existed := m.runningInputs[key]
	delete(m.runningInputs, key)
	m.mu.Unlock()

	if !existed {
		return nil
	}
	return m.commit()
}

// Update replaces a task with the result of fn and persists the queue. fn runs
// outside the queue lock, on a copy of the task; the copy is written back when
// fn returns, so fn must not call back into the manager.
//
// The whole task is replaced, so two concurrent Update calls for the same task
// may lose one of the changes; in practice one worker owns one task.
func (m *TaskManager) Update(id model.TaskID, fn func(*model.Task)) error {
	return m.update(id, fn, true)
}

// UpdateProgress is Update without the disk write. Progress arrives on every
// encoder output line and rewriting the queue at that rate is pointless; call
// Save at checkpoints (task start, task end, shutdown).
func (m *TaskManager) UpdateProgress(id model.TaskID, fn func(*model.Task)) error {
	return m.update(id, fn, false)
}

func (m *TaskManager) update(id model.TaskID, fn func(*model.Task), persist bool) error {
	if fn == nil {
		return nil
	}
	m.mu.Lock()
	idx := m.indexOfLocked(id)
	if idx < 0 {
		m.mu.Unlock()
		return errTaskNotFound(id)
	}
	work := cloneTask(&m.tasks[idx].Task)
	m.mu.Unlock()

	fn(&work)

	m.mu.Lock()
	idx = m.indexOfLocked(id)
	if idx < 0 {
		m.mu.Unlock()
		return errTaskNotFound(id)
	}
	// The id is the queue's identity; a mutator must not change it.
	work.ID = id
	work.Status.ID = id
	m.tasks[idx].Task = work
	m.mu.Unlock()

	m.notify()
	if !persist {
		return nil
	}
	return m.Save()
}

func errTaskNotFound(id model.TaskID) error {
	return okerr.New(okerr.KindNotFound, "找不到任务", "任务 %s 不在队列中。", id)
}

// HasNextTask reports whether any enabled task is waiting. Like the legacy
// method it does not consider the per-input mutex, so it may be true while
// GetNextTask still returns nil.
func (m *TaskManager) HasNextTask() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.tasks {
		t := &m.tasks[i].Task
		if t.Status.Enabled && t.Status.Progress == model.TaskWaiting {
			return true
		}
	}
	return false
}

// GetActiveTaskCount counts enabled waiting tasks.
func (m *TaskManager) GetActiveTaskCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for i := range m.tasks {
		t := &m.tasks[i].Task
		if t.Status.Enabled && t.Status.Progress == model.TaskWaiting {
			n++
		}
	}
	return n
}

// GetEnabledTaskCount counts tasks whose checkbox is ticked.
func (m *TaskManager) GetEnabledTaskCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for i := range m.tasks {
		if m.tasks[i].Task.Status.Enabled {
			n++
		}
	}
	return n
}

// GetNotRunningTasks returns copies of the enabled tasks that are not running,
// which includes finished and failed ones. This is what the legacy "clear
// finished tasks" button deleted.
func (m *TaskManager) GetNotRunningTasks() []model.Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]model.Task, 0, len(m.tasks))
	for i := range m.tasks {
		t := &m.tasks[i].Task
		if t.Status.Enabled && t.Status.Progress != model.TaskRunning {
			out = append(out, cloneTask(t))
		}
	}
	return out
}

// GetRunningTasks returns copies of every running task, enabled or not.
func (m *TaskManager) GetRunningTasks() []model.Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]model.Task, 0, len(m.tasks))
	for i := range m.tasks {
		t := &m.tasks[i].Task
		if t.Status.Progress == model.TaskRunning {
			out = append(out, cloneTask(t))
		}
	}
	return out
}

// GetTaskCount returns the queue length.
func (m *TaskManager) GetTaskCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.tasks)
}

// GetTasksByInputFile returns copies of the tasks whose input is exactly ref.
// The legacy method compared raw path strings; here the logical reference is
// compared directly.
func (m *TaskManager) GetTasksByInputFile(ref model.FileRef) []model.Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]model.Task, 0, len(m.tasks))
	for i := range m.tasks {
		t := &m.tasks[i].Task
		if t.Status.Input == ref {
			out = append(out, cloneTask(t))
		}
	}
	return out
}

// AllSuccess reports whether the queue is non-empty and every task finished.
// The empty queue reports false, matching the legacy behaviour that gates the
// after-finish command.
func (m *TaskManager) AllSuccess() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.tasks) == 0 {
		return false
	}
	for i := range m.tasks {
		if m.tasks[i].Task.Status.Progress != model.TaskFinished {
			return false
		}
	}
	return true
}

func (m *TaskManager) indexOfLocked(id model.TaskID) int {
	for i := range m.tasks {
		if m.tasks[i].Task.ID == id {
			return i
		}
	}
	return -1
}

// taskIdentity mirrors TaskManager.TaskIdentity. The config path is normalized
// like FileInfo.FullName; the input is a logical reference, so its canonical
// form is the volume plus the cleaned relative path.
func taskIdentity(configFilePath string, input model.FileRef) string {
	return normalizePath(configFilePath) + "\x00" + refIdentity(input)
}

// normalizePath mirrors new FileInfo(path).FullName: it makes the path absolute
// and collapses "." and "..". Unlike the legacy code it never throws; an empty
// path stays empty.
func normalizePath(p string) string {
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	return abs
}

// refIdentity returns the canonical string form of a logical file reference.
// The empty reference and a reference to the volume root have no identity: the
// model turns a zero FileRef into "local/" on a JSON round trip, and both forms
// mean "no file".
func refIdentity(r model.FileRef) string {
	if r.IsZero() {
		return ""
	}
	volume := r.Volume
	if volume == "" {
		volume = model.LocalVolume
	}
	rel := strings.ReplaceAll(r.Rel, "\\", "/")
	if !strings.HasPrefix(rel, "/") {
		rel = "/" + rel
	}
	rel = path.Clean(rel)
	if rel == "/" {
		return ""
	}
	return volume + ":" + rel
}

// inputMutexKey is the runningInputs key for an input file. The legacy HashSet
// used StringComparer.OrdinalIgnoreCase, so the key is upper-cased. An empty
// input has no key, which pairs with ReleaseInput's early return.
func inputMutexKey(input model.FileRef) string {
	key := refIdentity(input)
	if key == "" {
		return ""
	}
	return strings.ToUpper(key)
}
