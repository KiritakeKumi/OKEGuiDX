package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
)

// newTestManager builds a manager and fails the test if construction fails.
func newTestManager(t *testing.T, opts Options) *TaskManager {
	t.Helper()
	m, err := New(opts)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return m
}

// addTestTask enqueues one task and returns a copy of what the queue stored.
func addTestTask(t *testing.T, m *TaskManager, name, input string) model.Task {
	t.Helper()
	return addTestTaskWithConfig(t, m, name, input, "D:/cfg/"+name+".json")
}

// addTestTaskWithConfig is addTestTask with an explicit configuration path.
func addTestTaskWithConfig(t *testing.T, m *TaskManager, name, input, config string) model.Task {
	t.Helper()
	task := &model.Task{
		ID:     model.NewTaskID(),
		Name:   name,
		Inputs: []model.FileRef{model.NewFileRef(input)},
		Status: model.TaskStatus{Input: model.NewFileRef(input)},
	}
	if _, err := m.AddTask(task, config); err != nil {
		t.Fatalf("AddTask(%q) error = %v", name, err)
	}
	got, ok := m.Task(task.ID)
	if !ok {
		t.Fatalf("task %s missing right after AddTask", task.ID)
	}
	return got
}

type taskState struct {
	name     string
	progress model.TaskProgress
	enabled  bool
	input    string
}

// buildQueue adds one task per state and then forces the requested state. The
// returned ids are in queue order.
func buildQueue(t *testing.T, m *TaskManager, states []taskState) []model.TaskID {
	t.Helper()
	ids := make([]model.TaskID, 0, len(states))
	for i, st := range states {
		input := st.input
		if input == "" {
			input = fmt.Sprintf("D:/media/%d.m2ts", i)
		}
		task := &model.Task{
			ID:     model.NewTaskID(),
			Name:   st.name,
			Status: model.TaskStatus{Input: model.NewFileRef(input)},
		}
		if _, err := m.AddTask(task, "D:/cfg/project.json"); err != nil {
			t.Fatalf("AddTask(%q) error = %v", st.name, err)
		}
		if err := m.Update(task.ID, func(x *model.Task) {
			x.Status.Progress = st.progress
			x.Status.Enabled = st.enabled
		}); err != nil {
			t.Fatalf("Update(%q) error = %v", st.name, err)
		}
		ids = append(ids, task.ID)
	}
	return ids
}

func queueNames(t *testing.T, m *TaskManager) []string {
	t.Helper()
	snap := m.Snapshot()
	if len(snap) == 0 {
		return nil
	}
	names := make([]string, 0, len(snap))
	for i := range snap {
		names = append(names, snap[i].Name)
	}
	return names
}

// taskEqual compares two tasks through their JSON form, which is the actual
// persistence contract. Comparing Go values directly would flag the model's
// pre-existing normalisation of a zero FileRef into the volume root.
func taskEqual(a, b model.Task) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return false
	}
	return bytes.Equal(ja, jb)
}

func TestNormalizePathMirrorsFileInfoFullName(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"absolute is kept", filepath.Join(dir, "a.json"), filepath.Join(dir, "a.json")},
		{"dots are collapsed", filepath.Join(dir, "sub", "..", "a.json"), filepath.Join(dir, "a.json")},
		{"empty stays empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := normalizePath(tc.in); got != tc.want {
				t.Errorf("normalizePath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRefIdentityCleansLogicalPaths(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   model.FileRef
		want string
	}{
		{"empty has no identity", model.FileRef{}, ""},
		{"local volume is implicit", model.FileRef{Rel: "/media/a.m2ts"}, "local:/media/a.m2ts"},
		{"dots are collapsed", model.FileRef{Volume: "local", Rel: "/media/x/../a.m2ts"}, "local:/media/a.m2ts"},
		{"missing leading slash", model.FileRef{Volume: "local", Rel: "media/a.m2ts"}, "local:/media/a.m2ts"},
		{"named volume", model.FileRef{Volume: "nas", Rel: "/share/a.m2ts"}, "nas:/share/a.m2ts"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := refIdentity(tc.in); got != tc.want {
				t.Errorf("refIdentity(%+v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestInputMutexKeyIsCaseInsensitive(t *testing.T) {
	t.Parallel()
	// The legacy HashSet used StringComparer.OrdinalIgnoreCase, so two spellings
	// of the same input must collide.
	lower := inputMutexKey(model.NewFileRef(`D:\media\ep01.m2ts`))
	upper := inputMutexKey(model.NewFileRef(`d:\MEDIA\EP01.M2TS`))
	if lower == "" || lower != upper {
		t.Errorf("inputMutexKey = %q and %q, want equal non-empty keys", lower, upper)
	}
	if got := inputMutexKey(model.FileRef{}); got != "" {
		t.Errorf("inputMutexKey(empty) = %q, want empty", got)
	}
}

func TestHasActiveTask(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfgA := filepath.Join(dir, "a.json")
	cfgB := filepath.Join(dir, "b.json")
	inputA := `D:\media\ep01.m2ts`
	inputB := `D:\media\ep02.m2ts`

	cases := []struct {
		name   string
		seed   func(t *testing.T, m *TaskManager) // runs before the query
		config string
		input  string
		want   bool
	}{
		{
			name: "waiting task matches",
			seed: func(t *testing.T, m *TaskManager) {
				t.Helper()
				addTestTaskWithConfig(t, m, "ep01", inputA, cfgA)
			},
			config: cfgA, input: inputA, want: true,
		},
		{
			name: "running task matches",
			seed: func(t *testing.T, m *TaskManager) {
				t.Helper()
				addTestTaskWithConfig(t, m, "ep01", inputA, cfgA)
				if _, err := m.GetNextTask(); err != nil {
					t.Fatalf("GetNextTask() error = %v", err)
				}
			},
			config: cfgA, input: inputA, want: true,
		},
		{
			name: "finished task does not match",
			seed: func(t *testing.T, m *TaskManager) {
				t.Helper()
				task := &model.Task{ID: model.NewTaskID(), Name: "ep01", Status: model.TaskStatus{Input: model.NewFileRef(inputA)}}
				if _, err := m.AddTask(task, cfgA); err != nil {
					t.Fatalf("AddTask() error = %v", err)
				}
				if err := m.Update(task.ID, func(x *model.Task) { x.Status.Progress = model.TaskFinished }); err != nil {
					t.Fatalf("Update() error = %v", err)
				}
			},
			config: cfgA, input: inputA, want: false,
		},
		{
			name: "failed task does not match",
			seed: func(t *testing.T, m *TaskManager) {
				t.Helper()
				task := &model.Task{ID: model.NewTaskID(), Name: "ep01", Status: model.TaskStatus{Input: model.NewFileRef(inputA)}}
				if _, err := m.AddTask(task, cfgA); err != nil {
					t.Fatalf("AddTask() error = %v", err)
				}
				if err := m.Update(task.ID, func(x *model.Task) { x.Status.Progress = model.TaskError }); err != nil {
					t.Fatalf("Update() error = %v", err)
				}
			},
			config: cfgA, input: inputA, want: false,
		},
		{
			name: "different config does not match",
			seed: func(t *testing.T, m *TaskManager) {
				t.Helper()
				task := &model.Task{ID: model.NewTaskID(), Name: "ep01", Status: model.TaskStatus{Input: model.NewFileRef(inputA)}}
				if _, err := m.AddTask(task, cfgB); err != nil {
					t.Fatalf("AddTask() error = %v", err)
				}
			},
			config: cfgA, input: inputA, want: false,
		},
		{
			name: "different input does not match",
			seed: func(t *testing.T, m *TaskManager) {
				t.Helper()
				addTestTaskWithConfig(t, m, "ep02", inputB, cfgA)
			},
			config: cfgA, input: inputA, want: false,
		},
		{
			name: "messy config path is normalized",
			seed: func(t *testing.T, m *TaskManager) {
				t.Helper()
				task := &model.Task{ID: model.NewTaskID(), Name: "ep01", Status: model.TaskStatus{Input: model.NewFileRef(inputA)}}
				if _, err := m.AddTask(task, cfgA); err != nil {
					t.Fatalf("AddTask() error = %v", err)
				}
			},
			config: filepath.Join(dir, "sub", "..", "a.json"), input: inputA, want: true,
		},
		{
			name:   "empty queue never matches",
			seed:   func(*testing.T, *TaskManager) {},
			config: cfgA, input: inputA, want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := newTestManager(t, Options{})
			tc.seed(t, m)
			if got := m.HasActiveTask(tc.config, model.NewFileRef(tc.input)); got != tc.want {
				t.Errorf("HasActiveTask(%q, %q) = %v, want %v", tc.config, tc.input, got, tc.want)
			}
		})
	}
}

func TestAddTaskDefaults(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, Options{})

	unnamed := &model.Task{Status: model.TaskStatus{Input: model.NewFileRef("D:/media/a.m2ts")}}
	if n, err := m.AddTask(unnamed, "D:/cfg/a.json"); err != nil || n != 1 {
		t.Fatalf("AddTask() = %d, %v; want 1, nil", n, err)
	}
	named := &model.Task{Name: "ep02", Status: model.TaskStatus{Input: model.NewFileRef("D:/media/b.m2ts")}}
	if n, err := m.AddTask(named, "D:/cfg/a.json"); err != nil || n != 2 {
		t.Fatalf("AddTask() = %d, %v; want 2, nil", n, err)
	}

	snap := m.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("queue length = %d, want 2", len(snap))
	}
	got := snap[0]
	if got.Name != "新建任务 - 1" {
		t.Errorf("unnamed task name = %q, want %q", got.Name, "新建任务 - 1")
	}
	if snap[1].Name != "ep02" {
		t.Errorf("named task name = %q, want %q", snap[1].Name, "ep02")
	}
	if got.ID.IsZero() {
		t.Error("AddTask did not assign a task id")
	}
	if _, err := model.ParseTaskID(string(got.ID)); err != nil {
		t.Errorf("task id %q is not a UUID: %v", got.ID, err)
	}
	if got.Status.ID != got.ID {
		t.Errorf("Status.ID = %q, want %q", got.Status.ID, got.ID)
	}
	if !got.Status.Enabled {
		t.Error("Enabled = false, want true")
	}
	if got.Status.Progress != model.TaskWaiting {
		t.Errorf("Progress = %v, want WAITING", got.Status.Progress)
	}
	if got.Status.Status != "等待中" {
		t.Errorf("Status = %q, want 等待中", got.Status.Status)
	}
	if got.Status.ProgressValue != 0 {
		t.Errorf("ProgressValue = %v, want 0", got.Status.ProgressValue)
	}
	if got.Status.ProgressUnknown {
		t.Error("ProgressUnknown = true, want false")
	}
	if got.Status.Speed != "0.0 fps" {
		t.Errorf("Speed = %q, want 0.0 fps", got.Status.Speed)
	}
	if want := float64(30 * 24 * 60 * 60); got.Status.TimeRemainSeconds != want {
		t.Errorf("TimeRemainSeconds = %v, want %v (30 days)", got.Status.TimeRemainSeconds, want)
	}
	if got.Status.WorkerName != "" {
		t.Errorf("WorkerName = %q, want empty", got.Status.WorkerName)
	}
	if got.CreatedAt <= 0 {
		t.Errorf("CreatedAt = %d, want a positive timestamp", got.CreatedAt)
	}
}

func TestAddTaskDerivesStatusInput(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, Options{})
	task := &model.Task{
		ID:     model.NewTaskID(),
		Name:   "ep01",
		Inputs: []model.FileRef{model.NewFileRef("D:/media/ep01.m2ts"), model.NewFileRef("D:/media/ep01.sup")},
	}
	if _, err := m.AddTask(task, "D:/cfg/a.json"); err != nil {
		t.Fatalf("AddTask() error = %v", err)
	}
	got, _ := m.Task(task.ID)
	if got.Status.Input != got.Inputs[0] {
		t.Errorf("Status.Input = %+v, want %+v (derived from Inputs[0])", got.Status.Input, got.Inputs[0])
	}

	// An explicit input wins over the derived one.
	explicit := &model.Task{
		ID:     model.NewTaskID(),
		Name:   "ep02",
		Inputs: []model.FileRef{model.NewFileRef("D:/media/ep02.m2ts")},
		Status: model.TaskStatus{Input: model.NewFileRef("D:/media/other.m2ts")},
	}
	if _, err := m.AddTask(explicit, "D:/cfg/a.json"); err != nil {
		t.Fatalf("AddTask() error = %v", err)
	}
	got, _ = m.Task(explicit.ID)
	if got.Status.Input != explicit.Status.Input {
		t.Errorf("Status.Input = %+v, want the explicit %+v", got.Status.Input, explicit.Status.Input)
	}
}

func TestAddTaskCopiesAndValidates(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, Options{})

	if _, err := m.AddTask(nil, "D:/cfg/a.json"); err == nil {
		t.Error("AddTask(nil) = nil error, want a rejection")
	}

	task := &model.Task{ID: model.NewTaskID(), Name: "ep01", Status: model.TaskStatus{Input: model.NewFileRef("D:/media/a.m2ts")}}
	if _, err := m.AddTask(task, "D:/cfg/a.json"); err != nil {
		t.Fatalf("AddTask() error = %v", err)
	}
	if _, err := m.AddTask(task, "D:/cfg/a.json"); err == nil {
		t.Error("AddTask(duplicate id) = nil error, want a rejection")
	}
	if m.GetTaskCount() != 1 {
		t.Errorf("queue length = %d, want 1 after the duplicate rejection", m.GetTaskCount())
	}

	// The caller's value must not alias the queue.
	task.Name = "mutated"
	if got, _ := m.Task(task.ID); got.Name != "ep01" {
		t.Errorf("stored name = %q, want ep01", got.Name)
	}

	// A caller-supplied id survives, which is what a recovered or remote task
	// needs.
	other := &model.Task{ID: model.NewTaskID(), Name: "ep02"}
	if _, err := m.AddTask(other, "D:/cfg/a.json"); err != nil {
		t.Fatalf("AddTask() error = %v", err)
	}
	if _, ok := m.Task(other.ID); !ok {
		t.Errorf("task %s lost its caller-supplied id", other.ID)
	}
}

func TestDeleteTask(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		seed func(t *testing.T, m *TaskManager) model.TaskID
		want bool
		// presentAfter is whether the targeted id must still be in the queue.
		presentAfter bool
	}{
		{
			name: "waiting task is deleted",
			seed: func(t *testing.T, m *TaskManager) model.TaskID {
				t.Helper()
				return addTestTask(t, m, "ep01", "D:/media/a.m2ts").ID
			},
			want: true, presentAfter: false,
		},
		{
			name: "finished task is deleted",
			seed: func(t *testing.T, m *TaskManager) model.TaskID {
				t.Helper()
				ids := buildQueue(t, m, []taskState{{name: "ep01", progress: model.TaskFinished, enabled: true}})
				return ids[0]
			},
			want: true, presentAfter: false,
		},
		{
			name: "running task is refused",
			seed: func(t *testing.T, m *TaskManager) model.TaskID {
				t.Helper()
				addTestTask(t, m, "ep01", "D:/media/a.m2ts")
				claimed, err := m.GetNextTask()
				if err != nil || claimed == nil {
					t.Fatalf("GetNextTask() = %v, %v", claimed, err)
				}
				return claimed.ID
			},
			want: false, presentAfter: true,
		},
		{
			name: "unknown task is refused",
			seed: func(t *testing.T, m *TaskManager) model.TaskID {
				t.Helper()
				addTestTask(t, m, "ep01", "D:/media/a.m2ts")
				return model.NewTaskID()
			},
			want: false, presentAfter: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := newTestManager(t, Options{})
			id := tc.seed(t, m)
			got, err := m.DeleteTask(id)
			if err != nil {
				t.Fatalf("DeleteTask() error = %v", err)
			}
			if got != tc.want {
				t.Errorf("DeleteTask() = %v, want %v", got, tc.want)
			}
			if _, stillThere := m.Task(id); stillThere != tc.presentAfter {
				t.Errorf("task present = %v, want %v", stillThere, tc.presentAfter)
			}
		})
	}
}

func TestSetEnabled(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		seed    func(t *testing.T, m *TaskManager) model.TaskID
		enabled bool
		want    bool
	}{
		{
			name: "waiting task can be disabled",
			seed: func(t *testing.T, m *TaskManager) model.TaskID {
				t.Helper()
				return addTestTask(t, m, "ep01", "D:/media/a.m2ts").ID
			},
			enabled: false, want: false,
		},
		{
			name: "running task ignores the change",
			seed: func(t *testing.T, m *TaskManager) model.TaskID {
				t.Helper()
				addTestTask(t, m, "ep01", "D:/media/a.m2ts")
				claimed, err := m.GetNextTask()
				if err != nil || claimed == nil {
					t.Fatalf("GetNextTask() = %v, %v", claimed, err)
				}
				return claimed.ID
			},
			enabled: true, want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := newTestManager(t, Options{})
			id := tc.seed(t, m)
			if err := m.SetEnabled(id, tc.enabled); err != nil {
				t.Fatalf("SetEnabled() error = %v", err)
			}
			got, _ := m.Task(id)
			if got.Status.Enabled != tc.want {
				t.Errorf("Enabled = %v, want %v", got.Status.Enabled, tc.want)
			}
		})
	}
	if err := newTestManager(t, Options{}).SetEnabled(model.NewTaskID(), false); err == nil {
		t.Error("SetEnabled(unknown) = nil error, want a not-found error")
	}
}

func TestGetNextTaskInputExclusion(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, Options{})

	// Same input spelled differently: the legacy HashSet compared it
	// case-insensitively, so the second task must stay blocked.
	first := addTestTask(t, m, "ep01", `D:\media\ep01.m2ts`)
	second := addTestTask(t, m, "ep01-again", `d:\MEDIA\EP01.M2TS`)

	claimed, err := m.GetNextTask()
	if err != nil {
		t.Fatalf("GetNextTask() error = %v", err)
	}
	if claimed == nil || claimed.ID != first.ID {
		t.Fatalf("first GetNextTask() = %v, want %s", claimed, first.ID)
	}
	if claimed.Status.Progress != model.TaskRunning || claimed.Status.Enabled {
		t.Errorf("claimed task state = %v/%v, want RUNNING and disabled",
			claimed.Status.Progress, claimed.Status.Enabled)
	}
	// The queue itself must show the same state.
	stored, _ := m.Task(first.ID)
	if stored.Status.Progress != model.TaskRunning || stored.Status.Enabled {
		t.Errorf("stored task state = %v/%v, want RUNNING and disabled",
			stored.Status.Progress, stored.Status.Enabled)
	}

	if got, err := m.GetNextTask(); err != nil || got != nil {
		t.Fatalf("second GetNextTask() = %v, %v; want nil, nil (input busy)", got, err)
	}
	if !m.HasNextTask() {
		t.Error("HasNextTask() = false, want true: the task is waiting but blocked by the input mutex")
	}

	if err := m.ReleaseInput(claimed); err != nil {
		t.Fatalf("ReleaseInput() error = %v", err)
	}
	again, err := m.GetNextTask()
	if err != nil {
		t.Fatalf("GetNextTask() after release error = %v", err)
	}
	if again == nil || again.ID != second.ID {
		t.Fatalf("GetNextTask() after release = %v, want %s", again, second.ID)
	}
	if err := m.ReleaseInput(again); err != nil {
		t.Fatalf("ReleaseInput() error = %v", err)
	}
	if got, err := m.GetNextTask(); err != nil || got != nil {
		t.Fatalf("GetNextTask() on drained queue = %v, %v; want nil, nil", got, err)
	}
}

func TestGetNextTaskAllowsDifferentInputs(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, Options{})
	addTestTask(t, m, "ep01", "D:/media/ep01.m2ts")
	addTestTask(t, m, "ep02", "D:/media/ep02.m2ts")

	for _, want := range []string{"ep01", "ep02"} {
		got, err := m.GetNextTask()
		if err != nil {
			t.Fatalf("GetNextTask() error = %v", err)
		}
		if got == nil || got.Name != want {
			t.Fatalf("GetNextTask() = %v, want %q", got, want)
		}
	}
	if got, err := m.GetNextTask(); err != nil || got != nil {
		t.Fatalf("GetNextTask() on drained queue = %v, %v; want nil, nil", got, err)
	}
}

func TestGetNextTaskOrderAndEligibility(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		state []taskState
		want  string
	}{
		{
			name: "disabled tasks are skipped",
			state: []taskState{
				{name: "a", progress: model.TaskWaiting, enabled: false},
				{name: "b", progress: model.TaskWaiting, enabled: true},
			},
			want: "b",
		},
		{
			name: "running tasks are skipped",
			state: []taskState{
				{name: "a", progress: model.TaskRunning, enabled: true},
				{name: "b", progress: model.TaskWaiting, enabled: true},
			},
			want: "b",
		},
		{
			name: "finished and failed tasks are skipped",
			state: []taskState{
				{name: "a", progress: model.TaskFinished, enabled: true},
				{name: "b", progress: model.TaskError, enabled: true},
				{name: "c", progress: model.TaskWaiting, enabled: true},
			},
			want: "c",
		},
		{
			name: "nothing runnable",
			state: []taskState{
				{name: "a", progress: model.TaskFinished, enabled: true},
				{name: "b", progress: model.TaskWaiting, enabled: false},
			},
			want: "",
		},
		{
			name:  "empty queue",
			state: nil,
			want:  "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := newTestManager(t, Options{})
			buildQueue(t, m, tc.state)
			got, err := m.GetNextTask()
			if err != nil {
				t.Fatalf("GetNextTask() error = %v", err)
			}
			if tc.want == "" {
				if got != nil {
					t.Fatalf("GetNextTask() = %q, want nil", got.Name)
				}
				return
			}
			if got == nil || got.Name != tc.want {
				t.Fatalf("GetNextTask() = %v, want %q", got, tc.want)
			}
		})
	}
}

func TestGetNextTaskWithEmptyInputIsClaimable(t *testing.T) {
	t.Parallel()
	// The legacy code threw on an empty input path (new FileInfo("") is
	// invalid); the queue here treats it as "no mutex" instead of crashing.
	m := newTestManager(t, Options{})
	task := &model.Task{ID: model.NewTaskID(), Name: "no-input"}
	if _, err := m.AddTask(task, "D:/cfg/a.json"); err != nil {
		t.Fatalf("AddTask() error = %v", err)
	}
	got, err := m.GetNextTask()
	if err != nil || got == nil {
		t.Fatalf("GetNextTask() = %v, %v; want the task", got, err)
	}
	if err := m.ReleaseInput(got); err != nil {
		t.Errorf("ReleaseInput() error = %v", err)
	}
}

func TestReleaseInputNoops(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, Options{})
	if err := m.ReleaseInput(nil); err != nil {
		t.Errorf("ReleaseInput(nil) error = %v", err)
	}
	if err := m.ReleaseInput(&model.Task{}); err != nil {
		t.Errorf("ReleaseInput(empty input) error = %v", err)
	}
	// Releasing an input that is not held is harmless.
	claimed := addTestTask(t, m, "ep01", "D:/media/a.m2ts")
	task := claimed
	task.Status.Input = model.NewFileRef("D:/media/other.m2ts")
	if err := m.ReleaseInput(&task); err != nil {
		t.Errorf("ReleaseInput(unheld) error = %v", err)
	}
}

func TestMoveTaskUpAndDown(t *testing.T) {
	t.Parallel()
	threeWaiting := []taskState{
		{name: "a", progress: model.TaskWaiting, enabled: true},
		{name: "b", progress: model.TaskWaiting, enabled: true},
		{name: "c", progress: model.TaskWaiting, enabled: true},
	}
	cases := []struct {
		name      string
		state     []taskState
		pick      string // task name the operation targets
		up        bool
		want      bool
		wantOrder []string
	}{
		{
			name: "up swaps two waiting tasks", state: threeWaiting,
			pick: "b", up: true, want: true,
			wantOrder: []string{"b", "a", "c"},
		},
		{
			name: "up at the head fails", state: threeWaiting,
			pick: "a", up: true, want: false,
			wantOrder: []string{"a", "b", "c"},
		},
		{
			name: "up next to a running task fails",
			state: []taskState{
				{name: "r", progress: model.TaskRunning, enabled: false},
				{name: "a", progress: model.TaskWaiting, enabled: true},
				{name: "b", progress: model.TaskWaiting, enabled: true},
			},
			pick: "a", up: true, want: false,
			wantOrder: []string{"r", "a", "b"},
		},
		{
			name: "down swaps two waiting tasks", state: threeWaiting,
			pick: "a", up: false, want: true,
			wantOrder: []string{"b", "a", "c"},
		},
		{
			name: "down at the tail fails", state: threeWaiting,
			pick: "c", up: false, want: false,
			wantOrder: []string{"a", "b", "c"},
		},
		{
			name: "down next to a finished task fails",
			state: []taskState{
				{name: "a", progress: model.TaskWaiting, enabled: true},
				{name: "b", progress: model.TaskWaiting, enabled: true},
				{name: "f", progress: model.TaskFinished, enabled: true},
			},
			pick: "b", up: false, want: false,
			wantOrder: []string{"a", "b", "f"},
		},
		{
			name: "non-waiting task cannot move",
			state: []taskState{
				{name: "r", progress: model.TaskRunning, enabled: false},
				{name: "a", progress: model.TaskWaiting, enabled: true},
			},
			pick: "r", up: true, want: false,
			wantOrder: []string{"r", "a"},
		},
		{
			name: "empty queue", state: nil,
			pick: "", up: true, want: false,
			wantOrder: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := newTestManager(t, Options{})
			ids := buildQueue(t, m, tc.state)
			var id model.TaskID
			if tc.pick != "" {
				for i, st := range tc.state {
					if st.name == tc.pick {
						id = ids[i]
					}
				}
			} else {
				id = model.NewTaskID()
			}

			var (
				got bool
				err error
			)
			if tc.up {
				got, err = m.MoveTaskUp(id)
			} else {
				got, err = m.MoveTaskDown(id)
			}
			if err != nil {
				t.Fatalf("move error = %v", err)
			}
			if got != tc.want {
				t.Errorf("move = %v, want %v", got, tc.want)
			}
			if names := queueNames(t, m); !reflect.DeepEqual(names, tc.wantOrder) {
				t.Errorf("order = %v, want %v", names, tc.wantOrder)
			}
		})
	}
}

func TestMoveTaskTop(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		state     []taskState
		pick      string
		want      MoveTopResult
		wantOrder []string
	}{
		{
			name: "moves above the other waiting tasks",
			state: []taskState{
				{name: "a", progress: model.TaskWaiting, enabled: true},
				{name: "b", progress: model.TaskWaiting, enabled: true},
				{name: "c", progress: model.TaskWaiting, enabled: true},
			},
			pick: "c", want: MoveTopOK,
			wantOrder: []string{"c", "a", "b"},
		},
		{
			name: "lands on the first waiting slot after running tasks",
			state: []taskState{
				{name: "r", progress: model.TaskRunning, enabled: false},
				{name: "a", progress: model.TaskWaiting, enabled: true},
				{name: "b", progress: model.TaskWaiting, enabled: true},
			},
			pick: "b", want: MoveTopOK,
			wantOrder: []string{"r", "b", "a"},
		},
		{
			name: "already at the first waiting slot",
			state: []taskState{
				{name: "r", progress: model.TaskRunning, enabled: false},
				{name: "a", progress: model.TaskWaiting, enabled: true},
				{name: "b", progress: model.TaskWaiting, enabled: true},
			},
			pick: "a", want: MoveTopAlready,
			wantOrder: []string{"r", "a", "b"},
		},
		{
			name: "already first",
			state: []taskState{
				{name: "a", progress: model.TaskWaiting, enabled: true},
				{name: "b", progress: model.TaskWaiting, enabled: true},
			},
			pick: "a", want: MoveTopAlready,
			wantOrder: []string{"a", "b"},
		},
		{
			name: "running task cannot be moved",
			state: []taskState{
				{name: "r", progress: model.TaskRunning, enabled: false},
				{name: "a", progress: model.TaskWaiting, enabled: true},
			},
			pick: "r", want: MoveTopFailure,
			wantOrder: []string{"r", "a"},
		},
		{
			name: "finished task cannot be moved",
			state: []taskState{
				{name: "f", progress: model.TaskFinished, enabled: true},
				{name: "a", progress: model.TaskWaiting, enabled: true},
			},
			pick: "f", want: MoveTopFailure,
			wantOrder: []string{"f", "a"},
		},
		{
			name:  "empty queue",
			state: nil, pick: "", want: MoveTopFailure, wantOrder: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := newTestManager(t, Options{})
			ids := buildQueue(t, m, tc.state)
			var id model.TaskID
			if tc.pick != "" {
				for i, st := range tc.state {
					if st.name == tc.pick {
						id = ids[i]
					}
				}
			} else {
				id = model.NewTaskID()
			}

			got, err := m.MoveTaskTop(id)
			if err != nil {
				t.Fatalf("MoveTaskTop() error = %v", err)
			}
			if got != tc.want {
				t.Errorf("MoveTaskTop() = %v, want %v", got, tc.want)
			}
			if names := queueNames(t, m); !reflect.DeepEqual(names, tc.wantOrder) {
				t.Errorf("order = %v, want %v", names, tc.wantOrder)
			}
		})
	}

	if got := MoveTopOK.String(); got != "OK" {
		t.Errorf("MoveTopOK.String() = %q", got)
	}
	if got := MoveTopAlready.String(); got != "Already" {
		t.Errorf("MoveTopAlready.String() = %q", got)
	}
	if got := MoveTopFailure.String(); got != "Failure" {
		t.Errorf("MoveTopFailure.String() = %q", got)
	}
}

func TestQueueCounts(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		state        []taskState
		active       int
		enabled      int
		total        int
		notRunning   int
		running      int
		hasNext      bool
		allSucceeded bool
	}{
		{
			name: "mixed queue",
			state: []taskState{
				{name: "a", progress: model.TaskWaiting, enabled: true},
				{name: "b", progress: model.TaskRunning, enabled: false},
				{name: "c", progress: model.TaskFinished, enabled: true},
				{name: "d", progress: model.TaskError, enabled: true},
				{name: "e", progress: model.TaskWaiting, enabled: false},
			},
			active: 1, enabled: 3, total: 5, notRunning: 3, running: 1,
			hasNext: true, allSucceeded: false,
		},
		{
			name: "all finished",
			state: []taskState{
				{name: "a", progress: model.TaskFinished, enabled: true},
				{name: "b", progress: model.TaskFinished, enabled: true},
			},
			active: 0, enabled: 2, total: 2, notRunning: 2, running: 0,
			hasNext: false, allSucceeded: true,
		},
		{
			name: "all disabled and waiting",
			state: []taskState{
				{name: "a", progress: model.TaskWaiting, enabled: false},
			},
			active: 0, enabled: 0, total: 1, notRunning: 0, running: 0,
			hasNext: false, allSucceeded: false,
		},
		{
			name:   "empty queue",
			state:  nil,
			active: 0, enabled: 0, total: 0, notRunning: 0, running: 0,
			hasNext: false, allSucceeded: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := newTestManager(t, Options{})
			buildQueue(t, m, tc.state)

			if got := m.GetActiveTaskCount(); got != tc.active {
				t.Errorf("GetActiveTaskCount() = %d, want %d", got, tc.active)
			}
			if got := m.GetEnabledTaskCount(); got != tc.enabled {
				t.Errorf("GetEnabledTaskCount() = %d, want %d", got, tc.enabled)
			}
			if got := m.GetTaskCount(); got != tc.total {
				t.Errorf("GetTaskCount() = %d, want %d", got, tc.total)
			}
			if got := len(m.GetNotRunningTasks()); got != tc.notRunning {
				t.Errorf("len(GetNotRunningTasks()) = %d, want %d", got, tc.notRunning)
			}
			if got := len(m.GetRunningTasks()); got != tc.running {
				t.Errorf("len(GetRunningTasks()) = %d, want %d", got, tc.running)
			}
			if got := m.HasNextTask(); got != tc.hasNext {
				t.Errorf("HasNextTask() = %v, want %v", got, tc.hasNext)
			}
			if got := m.AllSuccess(); got != tc.allSucceeded {
				t.Errorf("AllSuccess() = %v, want %v", got, tc.allSucceeded)
			}
		})
	}
}

func TestGetTasksByInputFile(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, Options{})
	ref := model.NewFileRef(`D:\media\ep01.m2ts`)
	for _, name := range []string{"one", "two"} {
		task := &model.Task{ID: model.NewTaskID(), Name: name, Status: model.TaskStatus{Input: ref}}
		if _, err := m.AddTask(task, "D:/cfg/"+name+".json"); err != nil {
			t.Fatalf("AddTask() error = %v", err)
		}
	}
	addTestTask(t, m, "other", "D:/media/ep02.m2ts")

	got := m.GetTasksByInputFile(ref)
	if len(got) != 2 {
		t.Fatalf("GetTasksByInputFile() returned %d tasks, want 2", len(got))
	}
	if got[0].Name != "one" || got[1].Name != "two" {
		t.Errorf("names = %q, %q; want one, two", got[0].Name, got[1].Name)
	}
	if got := m.GetTasksByInputFile(model.NewFileRef("D:/media/missing.m2ts")); len(got) != 0 {
		t.Errorf("GetTasksByInputFile(unknown) returned %d tasks, want 0", len(got))
	}

	// The result is a copy.
	got[0].Name = "mutated"
	if stored, _ := m.Task(got[0].ID); stored.Name != "one" {
		t.Errorf("stored name = %q, want one", stored.Name)
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "queue.json")
	m := newTestManager(t, Options{QueuePath: path})

	quality := 64
	first := &model.Task{
		ID:   model.NewTaskID(),
		Name: "ep01",
		Status: model.TaskStatus{
			Input:  model.NewFileRef(`D:\media\ep01.m2ts`),
			Output: model.NewFileRef(`D:\out\ep01.mkv`),
		},
		Inputs:          []model.FileRef{model.NewFileRef(`D:\media\ep01.m2ts`), model.NewFileRef(`D:\media\ep01.sup`)},
		Output:          model.NewFileRef(`D:\out\ep01.mkv`),
		Profile:         map[string]any{"ProjectName": "ep01"},
		AudioTracks:     []model.AudioInfo{{Info: model.NewInfo(), OutputCodec: "AAC", Bitrate: 192, Quality: &quality}},
		ChapterFile:     model.NewFileRef(`D:\media\ep01.txt`),
		WorkingDir:      model.NewFileRef(`D:\work\ep01`),
		OutputDir:       model.NewFileRef(`D:\out`),
		LengthMS:        1440000,
		Frames:          34560,
		IsReEncode:      true,
		SliceParts:      []model.SliceInfo{{Begin: 10, End: 200}, {Begin: 200, End: model.OpenEnded}},
		ChapterLanguage: "jpn",
	}
	second := &model.Task{
		ID:   model.NewTaskID(),
		Name: "ep02",
		Status: model.TaskStatus{
			Input:     model.NewFileRef(`D:\media\ep02.m2ts`),
			TaskType:  model.TaskTypeReEncode,
			Chapter:   model.ChapterMaybe,
			RPC:       model.RPCPassed,
			RPCOutput: "D:/out/ep02_rpc.txt",
		},
	}
	for _, task := range []*model.Task{first, second} {
		if _, err := m.AddTask(task, `D:\cfg\project.json`); err != nil {
			t.Fatalf("AddTask() error = %v", err)
		}
	}
	before := m.Snapshot()

	reopened := newTestManager(t, Options{QueuePath: path})
	after := reopened.Snapshot()
	if len(before) != len(after) {
		t.Fatalf("round trip length = %d, want %d", len(after), len(before))
	}
	for i := range before {
		if !taskEqual(before[i], after[i]) {
			t.Fatalf("task %d differs after round trip:\nbefore = %+v\nafter  = %+v", i, before[i], after[i])
		}
	}
	if after[0].ID != first.ID || after[1].ID != second.ID {
		t.Errorf("ids after reload = %q, %q; want %q, %q", after[0].ID, after[1].ID, first.ID, second.ID)
	}
	if after[1].Status.TaskType != model.TaskTypeReEncode {
		t.Errorf("TaskType after reload = %v, want ReEncode", after[1].Status.TaskType)
	}
	if after[1].Status.Chapter != model.ChapterMaybe {
		t.Errorf("Chapter after reload = %v, want Maybe", after[1].Status.Chapter)
	}
	if after[1].Status.RPC != model.RPCPassed {
		t.Errorf("RPC after reload = %v, want 通过", after[1].Status.RPC)
	}
	if after[0].AudioTracks[0].Quality == nil || *after[0].AudioTracks[0].Quality != 64 {
		t.Errorf("Quality after reload = %v, want 64", after[0].AudioTracks[0].Quality)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	for _, want := range []string{string(first.ID), string(second.ID), `"version": 1`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("queue file does not contain %q", want)
		}
	}
	var probe queueFile
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("queue file is not valid JSON: %v", err)
	}
	if probe.Version != queueFormatVersion {
		t.Errorf("version = %d, want %d", probe.Version, queueFormatVersion)
	}
}

func TestPersistenceResetsRunningTasks(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "queue.json")
	m := newTestManager(t, Options{QueuePath: path})
	addTestTask(t, m, "ep01", "D:/media/ep01.m2ts")
	addTestTask(t, m, "ep02", "D:/media/ep02.m2ts")

	claimed, err := m.GetNextTask()
	if err != nil || claimed == nil {
		t.Fatalf("GetNextTask() = %v, %v", claimed, err)
	}

	// Simulate a crash: reopen the queue without releasing the input.
	reopened := newTestManager(t, Options{QueuePath: path})
	got, ok := reopened.Task(claimed.ID)
	if !ok {
		t.Fatalf("task %s missing after reload", claimed.ID)
	}
	if got.Status.Progress != model.TaskWaiting {
		t.Errorf("Progress after reload = %v, want WAITING", got.Status.Progress)
	}
	if !got.Status.Enabled {
		t.Error("Enabled after reload = false, want true")
	}
	if got.Status.Status != "等待中" {
		t.Errorf("Status after reload = %q, want 等待中", got.Status.Status)
	}
	if got.Status.ID != got.ID {
		t.Errorf("Status.ID = %q, want %q", got.Status.ID, got.ID)
	}

	// The recovered task must be claimable again, with no stale input mutex.
	again, err := reopened.GetNextTask()
	if err != nil || again == nil {
		t.Fatalf("GetNextTask() after reload = %v, %v; want the recovered task", again, err)
	}
	if again.ID != claimed.ID {
		t.Errorf("recovered task = %s, want %s", again.ID, claimed.ID)
	}
}

func TestPersistenceRepairsBrokenEntries(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "queue.json")
	// A hand-written queue with a missing id, a status id that disagrees and a
	// duplicate id.
	raw := `{
  "version": 1,
  "created_count": 0,
  "tasks": [
    {"config_file_path": "a.json", "task": {"id": "", "name": "a", "status": {"id": "", "progress": "WAITING", "enabled": true}}},
    {"config_file_path": "a.json", "task": {"id": "11111111-2222-4333-8444-555555555555", "name": "b", "status": {"id": "99999999-2222-4333-8444-555555555555", "progress": "RUNNING", "enabled": false}}},
    {"config_file_path": "a.json", "task": {"id": "11111111-2222-4333-8444-555555555555", "name": "c", "status": {"id": "11111111-2222-4333-8444-555555555555", "progress": "FINISHED", "enabled": true}}}
  ]
}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	m := newTestManager(t, Options{QueuePath: path})
	snap := m.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("queue length = %d, want 3", len(snap))
	}
	seen := make(map[model.TaskID]struct{}, len(snap))
	for i := range snap {
		if snap[i].ID.IsZero() {
			t.Errorf("task %d still has no id", i)
		}
		if _, err := model.ParseTaskID(string(snap[i].ID)); err != nil {
			t.Errorf("task %d id %q is not a UUID", i, snap[i].ID)
		}
		if snap[i].Status.ID != snap[i].ID {
			t.Errorf("task %d Status.ID = %q, want %q", i, snap[i].Status.ID, snap[i].ID)
		}
		if _, dup := seen[snap[i].ID]; dup {
			t.Errorf("task %d duplicates id %q", i, snap[i].ID)
		}
		seen[snap[i].ID] = struct{}{}
	}
	if snap[1].Status.Progress != model.TaskWaiting || !snap[1].Status.Enabled {
		t.Errorf("recovered running task = %v/%v, want WAITING and enabled",
			snap[1].Status.Progress, snap[1].Status.Enabled)
	}
	if snap[2].Status.Progress != model.TaskFinished {
		t.Errorf("finished task = %v, want FINISHED", snap[2].Status.Progress)
	}

	// The repair is written back.
	reopened := newTestManager(t, Options{QueuePath: path})
	after := reopened.Snapshot()
	if len(after) != len(snap) {
		t.Fatalf("queue length after reload = %d, want %d", len(after), len(snap))
	}
	for i := range snap {
		if after[i].ID != snap[i].ID {
			t.Errorf("task %d id after reload = %q, want %q", i, after[i].ID, snap[i].ID)
		}
		if after[i].Status.ID != after[i].ID {
			t.Errorf("task %d Status.ID after reload = %q, want %q", i, after[i].Status.ID, after[i].ID)
		}
		if after[i].Status.Progress != snap[i].Status.Progress {
			t.Errorf("task %d progress after reload = %v, want %v", i, after[i].Status.Progress, snap[i].Status.Progress)
		}
	}
}

func TestPersistenceRejectsCorruptFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "queue.json")
	if err := os.WriteFile(path, []byte("{ this is not json"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	m, err := New(Options{QueuePath: path})
	if err == nil {
		t.Fatal("New() = nil error, want a corruption error")
	}
	if m == nil {
		t.Fatal("New() returned a nil manager, want an empty usable one")
	}
	if m.GetTaskCount() != 0 {
		t.Errorf("queue length = %d, want 0", m.GetTaskCount())
	}
	// The manager stays usable and a later save replaces the broken file.
	if _, err := m.AddTask(&model.Task{Name: "ep01"}, "D:/cfg/a.json"); err != nil {
		t.Fatalf("AddTask() after corruption error = %v", err)
	}
	if _, err := New(Options{QueuePath: path}); err != nil {
		t.Fatalf("New() after repair error = %v", err)
	}
}

func TestPersistenceRejectsNewerVersion(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "queue.json")
	if err := os.WriteFile(path, []byte(`{"version": 99, "tasks": []}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	m, err := New(Options{QueuePath: path})
	if err == nil {
		t.Fatal("New() = nil error, want a version error")
	}
	if m.GetTaskCount() != 0 {
		t.Errorf("queue length = %d, want 0", m.GetTaskCount())
	}
}

func TestPersistenceEmptyFileIsIgnored(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "queue.json")
	if err := os.WriteFile(path, []byte("\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	m := newTestManager(t, Options{QueuePath: path})
	if m.GetTaskCount() != 0 {
		t.Errorf("queue length = %d, want 0", m.GetTaskCount())
	}
}

func TestPersistenceCreatesFileAndLeavesNoTemporary(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "queue.json")
	m := newTestManager(t, Options{QueuePath: path})
	addTestTask(t, m, "ep01", "D:/media/ep01.m2ts")
	if err := m.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "queue.json" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory contains %v, want only queue.json", names)
	}
}

func TestPersistenceDisabledWithoutPath(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, Options{})
	addTestTask(t, m, "ep01", "D:/media/ep01.m2ts")
	if err := m.Save(); err != nil {
		t.Errorf("Save() without a path error = %v", err)
	}
	if _, err := m.GetNextTask(); err != nil {
		t.Errorf("GetNextTask() without a path error = %v", err)
	}
}

func TestUpdateAndUpdateProgress(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "queue.json")
	m := newTestManager(t, Options{QueuePath: path})
	task := addTestTask(t, m, "ep01", "D:/media/ep01.m2ts")

	if err := m.Update(task.ID, func(x *model.Task) {
		x.Status.Progress = model.TaskFinished
		x.Status.Status = "完成"
		x.Status.ProgressValue = 100
	}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	reopened := newTestManager(t, Options{QueuePath: path})
	got, _ := reopened.Task(task.ID)
	if got.Status.Progress != model.TaskFinished || got.Status.Status != "完成" || got.Status.ProgressValue != 100 {
		t.Errorf("persisted task = %+v, want the updated values", got.Status)
	}

	if err := m.UpdateProgress(task.ID, func(x *model.Task) { x.Status.Speed = "12.34 fps" }); err != nil {
		t.Fatalf("UpdateProgress() error = %v", err)
	}
	if got, _ := m.Task(task.ID); got.Status.Speed != "12.34 fps" {
		t.Errorf("in-memory speed = %q, want 12.34 fps", got.Status.Speed)
	}

	if err := m.Update(model.NewTaskID(), func(*model.Task) {}); err == nil {
		t.Error("Update(unknown) = nil error, want a not-found error")
	}
	if err := m.UpdateProgress(model.NewTaskID(), func(*model.Task) {}); err == nil {
		t.Error("UpdateProgress(unknown) = nil error, want a not-found error")
	}
	if err := m.Update(task.ID, nil); err != nil {
		t.Errorf("Update(nil mutator) error = %v", err)
	}
}

func TestOnChangeRunsOutsideTheQueueLock(t *testing.T) {
	t.Parallel()
	var (
		mu    sync.Mutex
		calls int
		seen  int
	)
	var m *TaskManager
	m = newTestManager(t, Options{OnChange: func(tasks []model.Task) {
		// Calling back into the manager from the callback must not deadlock:
		// notifications run outside the queue lock.
		snap := m.Snapshot()
		mu.Lock()
		calls++
		seen = len(snap)
		mu.Unlock()
		if len(tasks) != len(snap) {
			t.Errorf("callback got %d tasks, snapshot has %d", len(tasks), len(snap))
		}
	}})

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := m.AddTask(&model.Task{Name: "ep01"}, "D:/cfg/a.json"); err != nil {
			t.Errorf("AddTask() error = %v", err)
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("AddTask deadlocked: the callback ran under the queue lock")
	}

	mu.Lock()
	defer mu.Unlock()
	if calls == 0 {
		t.Fatal("OnChange was never called")
	}
	if seen != 1 {
		t.Errorf("callback saw %d tasks, want 1", seen)
	}
}

func TestSnapshotIsIndependent(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, Options{})
	task := addTestTask(t, m, "ep01", "D:/media/ep01.m2ts")

	// Compare against a reference built the same way, not against a path
	// spelled out here: the point is that the snapshot is a deep copy, and a
	// hardcoded expectation would also fail on an unrelated path change.
	wantInput := model.NewFileRef("D:/media/ep01.m2ts")
	snap := m.Snapshot()
	snap[0].Name = "mutated"
	snap[0].Inputs[0] = model.NewFileRef("D:/media/other.m2ts")
	got, _ := m.Task(task.ID)
	if got.Name != "ep01" {
		t.Errorf("Name was mutated through the snapshot: %q", got.Name)
	}
	if len(got.Inputs) != 1 || got.Inputs[0] != wantInput {
		t.Errorf("Inputs were mutated through the snapshot: %+v, want [%+v]", got.Inputs, wantInput)
	}
}

func TestConcurrentAddAndClaim(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "queue.json")
	m := newTestManager(t, Options{QueuePath: path})

	const (
		goroutines = 8
		perRoutine = 15
	)
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		seen  = make(map[model.TaskID]struct{})
		dupes int
	)
	for g := range goroutines {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := range perRoutine {
				task := &model.Task{
					ID:     model.NewTaskID(),
					Name:   fmt.Sprintf("g%d-%d", g, i),
					Status: model.TaskStatus{Input: model.NewFileRef(fmt.Sprintf("D:/media/%d.m2ts", i))},
				}
				if _, err := m.AddTask(task, "D:/cfg/project.json"); err != nil {
					t.Errorf("AddTask() error = %v", err)
					continue
				}
				claimed, err := m.GetNextTask()
				if err != nil {
					t.Errorf("GetNextTask() error = %v", err)
				}
				if claimed != nil {
					mu.Lock()
					if _, dup := seen[claimed.ID]; dup {
						dupes++
					}
					seen[claimed.ID] = struct{}{}
					mu.Unlock()
					if err := m.ReleaseInput(claimed); err != nil {
						t.Errorf("ReleaseInput() error = %v", err)
					}
				}
				_ = m.HasNextTask()
				_ = m.GetActiveTaskCount()
				_ = m.GetNotRunningTasks()
				_, _ = m.MoveTaskTop(task.ID)
				_, _ = m.MoveTaskUp(task.ID)
				_ = m.SetEnabled(task.ID, false)
			}
		}(g)
	}
	wg.Wait()

	if dupes != 0 {
		t.Errorf("%d tasks were claimed more than once", dupes)
	}
	if n := m.GetTaskCount(); n != goroutines*perRoutine {
		t.Errorf("queue length = %d, want %d", n, goroutines*perRoutine)
	}
	if _, err := New(Options{QueuePath: path}); err != nil {
		t.Errorf("the concurrently written queue is unreadable: %v", err)
	}
}

func TestConcurrentClaimIsExclusive(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, Options{})
	const tasks = 20
	for i := range tasks {
		addTestTask(t, m, fmt.Sprintf("ep%02d", i), fmt.Sprintf("D:/media/%02d.m2ts", i))
	}

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		claimed []model.TaskID
	)
	for range tasks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := m.GetNextTask()
			if err != nil {
				t.Errorf("GetNextTask() error = %v", err)
				return
			}
			if got == nil {
				return
			}
			mu.Lock()
			claimed = append(claimed, got.ID)
			mu.Unlock()
		}()
	}
	wg.Wait()

	if len(claimed) != tasks {
		t.Fatalf("%d tasks were claimed, want %d", len(claimed), tasks)
	}
	unique := make(map[model.TaskID]struct{}, len(claimed))
	for _, id := range claimed {
		if _, dup := unique[id]; dup {
			t.Errorf("task %s was claimed twice", id)
		}
		unique[id] = struct{}{}
	}
}
