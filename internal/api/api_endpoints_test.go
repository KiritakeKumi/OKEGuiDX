package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KiritakeKumi/OKEGuiDX/internal/engine"
	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/node"
	"github.com/KiritakeKumi/OKEGuiDX/internal/platform"
)

// TestStatusSuccess covers GET /api/v1/status: the node's capabilities plus the
// queue and pool summary.
func TestStatusSuccess(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	// One waiting task, one running task: the counts must be computed from the
	// queue, not assumed.
	waiting := env.addTask(env.fix.profile, env.fix.input)
	running := env.addTask(env.fix.profile, env.fix.input+".other")
	if err := env.tasks.Update(running.ID, func(t *model.Task) {
		t.Status.Progress = model.TaskRunning
	}); err != nil {
		t.Fatalf("mark running: %v", err)
	}

	rec := env.do(http.MethodGet, APIPrefix+"/status", nil)
	wantStatus(t, rec, http.StatusOK)

	var resp statusResponse
	decodeJSON(t, rec, &resp)

	if resp.Node.NodeID != env.caps.NodeID {
		t.Errorf("node_id = %q, want %q", resp.Node.NodeID, env.caps.NodeID)
	}
	if resp.Node.Role != node.RoleStandalone {
		t.Errorf("role = %q, want standalone", resp.Node.Role)
	}
	if !resp.Node.HasFeature(node.FeatureAAC) {
		t.Error("capabilities lost the aac feature")
	}
	if _, ok := resp.Node.Tool("x265"); !ok {
		t.Error("capabilities lost the x265 tool")
	}

	if resp.Queue.Total != 2 || resp.Queue.Waiting != 1 || resp.Queue.Running != 1 {
		t.Errorf("queue = %+v, want total 2 waiting 1 running 1", resp.Queue)
	}
	if resp.Queue.Enabled != 2 {
		t.Errorf("queue enabled = %d, want 2", resp.Queue.Enabled)
	}
	if resp.Pool.Workers != 1 {
		t.Errorf("pool workers = %d, want 1", resp.Pool.Workers)
	}
	if len(resp.Pool.WorkerList) != 1 || resp.Pool.WorkerList[0].Name == "" {
		t.Errorf("worker list = %+v, want one named worker", resp.Pool.WorkerList)
	}
	if resp.Pool.StopTimeoutSeconds != int(DefaultStopTimeout.Seconds()) {
		t.Errorf("stop timeout = %d, want %d", resp.Pool.StopTimeoutSeconds, int(DefaultStopTimeout.Seconds()))
	}
	_ = waiting
}

// TestStatusExecutorFailure covers the error path of GET /api/v1/status: an
// executor that cannot report capabilities is a 500, not an empty 200.
func TestStatusExecutorFailure(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	broken := engineWithFailingCapabilities(t, env)
	rec := env.doVia(broken, http.MethodGet, APIPrefix+"/status", "")
	wantStatus(t, rec, http.StatusInternalServerError)
	info := errorOf(t, rec)
	if info.Summary == "" {
		t.Error("error summary is empty")
	}
}

// TestTaskListAndGet covers GET /api/v1/tasks and GET /api/v1/tasks/{id}.
func TestTaskListAndGet(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	rec := env.do(http.MethodGet, APIPrefix+"/tasks", nil)
	wantStatus(t, rec, http.StatusOK)
	var empty taskListResponse
	decodeJSON(t, rec, &empty)
	if empty.Count != 0 || len(empty.Tasks) != 0 {
		t.Errorf("empty queue = %+v, want count 0", empty)
	}

	added := env.addTask(env.fix.profile, env.fix.input)

	rec = env.do(http.MethodGet, APIPrefix+"/tasks", nil)
	wantStatus(t, rec, http.StatusOK)
	var list taskListResponse
	decodeJSON(t, rec, &list)
	if list.Count != 1 || len(list.Tasks) != 1 {
		t.Fatalf("list = %+v, want one task", list)
	}
	if list.Tasks[0].ID != added.ID {
		t.Errorf("listed id = %q, want %q", list.Tasks[0].ID, added.ID)
	}

	rec = env.do(http.MethodGet, APIPrefix+"/tasks/"+added.ID.String(), nil)
	wantStatus(t, rec, http.StatusOK)
	var one taskResponse
	decodeJSON(t, rec, &one)
	if one.Task.ID != added.ID {
		t.Errorf("got id = %q, want %q", one.Task.ID, added.ID)
	}
	if one.Task.Status.Input.IsZero() {
		t.Error("task lost its display input")
	}
}

// TestTaskGetErrors covers the 400 and 404 paths of GET /tasks/{id}.
func TestTaskGetErrors(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	cases := []struct {
		name string
		id   string
		want int
	}{
		{"not a uuid", "not-a-uuid", http.StatusBadRequest},
		{"empty-ish uuid", "0000", http.StatusBadRequest},
		{"unknown uuid", "11111111-2222-4333-8444-555555555555", http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := env.do(http.MethodGet, APIPrefix+"/tasks/"+tc.id, nil)
			wantStatus(t, rec, tc.want)
			if info := errorOf(t, rec); info.Summary == "" {
				t.Error("error summary is empty")
			}
		})
	}
}

// TestTaskAddFromPath covers the success path of POST /api/v1/tasks with a
// profile path: 201, the queue's own view of the task, and a real task in the
// queue.
func TestTaskAddFromPath(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	body := addTaskRequest{ProfilePath: env.fix.profile}
	rec := env.do(http.MethodPost, APIPrefix+"/tasks", body)
	wantStatus(t, rec, http.StatusCreated)

	var resp taskResponse
	decodeJSON(t, rec, &resp)
	if resp.Task.ID.IsZero() {
		t.Fatal("added task has no id")
	}
	if resp.Task.Name != "ep01" {
		t.Errorf("name = %q, want ep01 (from ProjectName)", resp.Task.Name)
	}
	if resp.Task.Status.Progress != model.TaskWaiting {
		t.Errorf("progress = %v, want WAITING", resp.Task.Status.Progress)
	}
	if !resp.Task.Status.Enabled {
		t.Error("a new task must be enabled")
	}
	if got := resp.Task.Status.Input.Rel; !strings.HasSuffix(got, "/00001.m2ts") {
		t.Errorf("display input = %q, want the profile's first input", got)
	}
	if len(resp.Task.AudioTracks) != 1 {
		t.Errorf("audio tracks = %d, want 1 (from the profile)", len(resp.Task.AudioTracks))
	}
	if _, ok := env.tasks.Task(resp.Task.ID); !ok {
		t.Error("the task is not in the queue")
	}
	if got, _ := env.tasks.ConfigPath(resp.Task.ID); got != env.fix.profile {
		t.Errorf("stored config path = %q, want %q", got, env.fix.profile)
	}
}

// TestTaskAddInlineProfile covers the inline-body forms of POST /tasks: the
// object form and the raw-text form.
func TestTaskAddInlineProfile(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	// The text form carries the exact bytes of a profile file, including the
	// trailing commas a strict JSON body could not contain.
	rec := env.do(http.MethodPost, APIPrefix+"/tasks", addTaskRequest{
		ProfileText: env.fix.profileText(),
		BaseDir:     env.fix.dir,
	})
	wantStatus(t, rec, http.StatusCreated)

	var resp taskResponse
	decodeJSON(t, rec, &resp)
	if resp.Task.Name != "ep01" {
		t.Errorf("name = %q, want ep01", resp.Task.Name)
	}
	if got, _ := env.tasks.ConfigPath(resp.Task.ID); got != "" {
		t.Errorf("config path = %q, want empty for an inline profile", got)
	}
	if got := resp.Task.Inputs[0].Rel; !strings.HasSuffix(got, "/00001.m2ts") {
		t.Errorf("input = %q, want it resolved against base_dir", got)
	}

	// The object form is for a client that builds a profile in code. It has to
	// be strict JSON, so the trailing commas are dropped. A second source keeps
	// the request out of the duplicate check.
	other := filepath.Join(env.fix.dir, "00002.m2ts")
	writeFile(t, other, "")
	objectBody := `{"profile":` + env.fix.strictProfileJSON() + `,"base_dir":` +
		strconvQuote(env.fix.dir) + `,"inputs":[` + strconvQuote(other) + `]}`
	rec = env.do(http.MethodPost, APIPrefix+"/tasks", objectBody)
	wantStatus(t, rec, http.StatusCreated)
	decodeJSON(t, rec, &resp)
	if resp.Task.Name != "ep01" {
		t.Errorf("object form name = %q, want ep01", resp.Task.Name)
	}
}

// TestTaskAddRequestErrors covers the 400 paths of POST /tasks.
func TestTaskAddRequestErrors(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "no profile at all",
			body: `{}`,
			want: "必须提供 profile_path、profile 或 profile_text 之一",
		},
		{
			name: "both forms",
			body: `{"profile_path":"a.json","profile":{"Version":3}}`,
			want: "只能提供其中一个",
		},
		{
			name: "malformed json",
			body: `{"profile_path":`,
			want: "请求内容不合法",
		},
		{
			name: "unknown field",
			body: `{"profile_path":"a.json","profil_path":"typo"}`,
			want: "请求内容不合法",
		},
		{
			name: "two json values",
			body: `{"profile_path":"a.json"} {}`,
			want: "请求内容不合法",
		},
		{
			name: "profile path does not exist",
			body: `{"profile_path":"` + strings.ReplaceAll(env.fix.dir, `\`, `\\`) + `\\missing.json"}`,
			want: "无法读取json文件",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := env.do(http.MethodPost, APIPrefix+"/tasks", tc.body)
			wantStatus(t, rec, http.StatusBadRequest)
			info := errorOf(t, rec)
			// The reason is in the detail; the summary is the generic title.
			if !strings.Contains(info.Detail, tc.want) && !strings.Contains(info.Summary, tc.want) {
				t.Errorf("summary/detail = %q / %q, want one to contain %q",
					info.Summary, info.Detail, tc.want)
			}
		})
	}
}

// TestTaskAddValidationErrors covers the profile-validation 400 paths, which is
// where the legacy MessageBox texts now surface.
func TestTaskAddValidationErrors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		profile string
		field   string
		summary string
	}{
		{
			name: "wrong version",
			profile: `{
				"Version" : 1, "EncoderType" : "x265", "ContainerFormat" : "mkv",
				"Fps" : 23.976, "InputScript" : "demo.vpy", "InputFiles" : ["00001.m2ts"]
			}`,
			field:   "Version",
			summary: "版本不对",
		},
		{
			name: "unknown encoder",
			profile: `{
				"Version" : 3, "VSVersion" : "2024H1", "EncoderType" : "x266",
				"ContainerFormat" : "mkv", "Fps" : 23.976,
				"InputScript" : "demo.vpy", "InputFiles" : ["00001.m2ts"]
			}`,
			field:   "EncoderType",
			summary: "编码器版本错误",
		},
		{
			name: "unknown container",
			profile: `{
				"Version" : 3, "VSVersion" : "2024H1", "EncoderType" : "x265",
				"ContainerFormat" : "avi", "Fps" : 23.976,
				"InputScript" : "demo.vpy", "InputFiles" : ["00001.m2ts"]
			}`,
			field:   "ContainerFormat",
			summary: "封装格式指定的有问题",
		},
		{
			name: "missing frame rate",
			profile: `{
				"Version" : 3, "VSVersion" : "2024H1", "EncoderType" : "x265",
				"ContainerFormat" : "mkv", "InputScript" : "demo.vpy",
				"InputFiles" : ["00001.m2ts"]
			}`,
			field:   "Fps",
			summary: "帧率没有指定诶",
		},
		{
			name: "unknown frame rate",
			profile: `{
				"Version" : 3, "VSVersion" : "2024H1", "EncoderType" : "x265",
				"ContainerFormat" : "mkv", "Fps" : 12.345,
				"InputScript" : "demo.vpy", "InputFiles" : ["00001.m2ts"]
			}`,
			field:   "Fps",
			summary: "不知道的帧率诶",
		},
		{
			name: "mp4 with timecode",
			profile: `{
				"Version" : 3, "VSVersion" : "2024H1", "EncoderType" : "x265",
				"ContainerFormat" : "mp4", "Fps" : 23.976, "TimeCode" : true,
				"InputScript" : "demo.vpy", "InputFiles" : ["00001.m2ts"]
			}`,
			field:   "TimeCode",
			summary: "MP4暂不支持VFR封装",
		},
		{
			name: "flac in mp4",
			profile: `{
				"Version" : 3, "VSVersion" : "2024H1", "EncoderType" : "x265",
				"ContainerFormat" : "mp4", "Fps" : 23.976,
				"AudioTracks" : [{"OutputCodec" : "flac"}],
				"InputScript" : "demo.vpy", "InputFiles" : ["00001.m2ts"]
			}`,
			field:   "AudioTracks[0].OutputCodec",
			summary: "音轨格式不支持",
		},
		{
			name: "deprecated option",
			profile: `{
				"Version" : 3, "VSVersion" : "2024H1", "EncoderType" : "x265",
				"ContainerFormat" : "mkv", "Fps" : 23.976, "SkipMuxing" : false,
				"InputScript" : "demo.vpy", "InputFiles" : ["00001.m2ts"]
			}`,
			field:   "SkipMuxing",
			summary: "json文件版本太老了",
		},
		{
			name: "script has no input tag",
			profile: `{
				"Version" : 3, "VSVersion" : "2024H1", "EncoderType" : "x265",
				"ContainerFormat" : "mkv", "Fps" : 23.976,
				"InputScript" : "plain.vpy", "InputFiles" : ["00001.m2ts"]
			}`,
			field:   "InputScript",
			summary: "vpy没有为OKEGui设计",
		},
		{
			name: "input file missing",
			profile: `{
				"Version" : 3, "VSVersion" : "2024H1", "EncoderType" : "x265",
				"ContainerFormat" : "mkv", "Fps" : 23.976,
				"InputScript" : "demo.vpy", "InputFiles" : ["nope.m2ts"]
			}`,
			field:   "InputFiles",
			summary: "找不到输入文件啊",
		},
		{
			name: "duplicate input file",
			profile: `{
				"Version" : 3, "VSVersion" : "2024H1", "EncoderType" : "x265",
				"ContainerFormat" : "mkv", "Fps" : 23.976,
				"InputScript" : "demo.vpy", "InputFiles" : ["00001.m2ts", "00001.m2ts"]
			}`,
			field:   "InputFiles",
			summary: "输入文件有重复",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t)
			if tc.name == "script has no input tag" {
				writeFile(t, filepath.Join(env.fix.dir, "plain.vpy"), "# nothing to see here\n")
			}
			body := addTaskRequest{ProfileText: tc.profile, BaseDir: env.fix.dir}
			rec := env.do(http.MethodPost, APIPrefix+"/tasks", body)
			wantStatus(t, rec, http.StatusBadRequest)
			info := errorOf(t, rec)
			if info.Summary != tc.summary {
				t.Errorf("summary = %q, want %q (detail: %s)", info.Summary, tc.summary, info.Detail)
			}
			if info.Field != tc.field {
				t.Errorf("field = %q, want %q", info.Field, tc.field)
			}
			if info.Detail == "" {
				t.Error("detail is empty")
			}
			if env.tasks.GetTaskCount() != 0 {
				t.Error("a rejected task reached the queue")
			}
		})
	}
}

// TestTaskAddDuplicate covers the 409 path: the same profile and input may not
// be queued twice while the first one is still active.
func TestTaskAddDuplicate(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	env.addTask(env.fix.profile, env.fix.input)

	rec := env.do(http.MethodPost, APIPrefix+"/tasks", addTaskRequest{
		ProfilePath: env.fix.profile,
		Inputs:      []string{env.fix.input},
	})
	wantStatus(t, rec, http.StatusConflict)
	info := errorOf(t, rec)
	if !strings.Contains(info.Summary, "已经存在") {
		t.Errorf("summary = %q, want it to mention the duplicate", info.Summary)
	}
	if env.tasks.GetTaskCount() != 1 {
		t.Errorf("queue length = %d, want 1", env.tasks.GetTaskCount())
	}

	// A finished task no longer blocks the same input, which is what
	// HasActiveTask decides.
	tasks := env.tasks.Snapshot()
	if err := env.tasks.Update(tasks[0].ID, func(task *model.Task) {
		task.Status.Progress = model.TaskFinished
	}); err != nil {
		t.Fatalf("finish task: %v", err)
	}
	rec = env.do(http.MethodPost, APIPrefix+"/tasks", addTaskRequest{
		ProfilePath: env.fix.profile,
		Inputs:      []string{env.fix.input},
	})
	wantStatus(t, rec, http.StatusCreated)
}

// TestTaskAddNeedsOneInput covers the "one task is one source" rule: a profile
// with several inputs has to be told which one to use.
func TestTaskAddNeedsOneInput(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	// A second source beside the first, so the profile can list both.
	second := filepath.Join(env.fix.dir, "00002.m2ts")
	writeFile(t, second, "")
	raw := strings.ReplaceAll(env.fix.profileText(), `"00001.m2ts"`,
		`"00001.m2ts", "00002.m2ts"`)

	rec := env.do(http.MethodPost, APIPrefix+"/tasks", addTaskRequest{
		ProfileText: raw,
		BaseDir:     env.fix.dir,
	})
	wantStatus(t, rec, http.StatusBadRequest)
	info := errorOf(t, rec)
	if info.Summary != "需要指定输入文件" {
		t.Errorf("summary = %q, want 需要指定输入文件", info.Summary)
	}
	if !strings.Contains(info.Detail, second) {
		t.Errorf("detail %q should name both candidates", info.Detail)
	}
	if info.Field != "InputFiles" {
		t.Errorf("field = %q, want InputFiles", info.Field)
	}

	// Naming one of them works.
	rec = env.do(http.MethodPost, APIPrefix+"/tasks", addTaskRequest{
		ProfileText: raw,
		BaseDir:     env.fix.dir,
		Inputs:      []string{second},
	})
	wantStatus(t, rec, http.StatusCreated)
}

// TestTaskDelete covers the success and error paths of DELETE /tasks/{id}.
func TestTaskDelete(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	added := env.addTask(env.fix.profile, env.fix.input)

	rec := env.do(http.MethodDelete, APIPrefix+"/tasks/"+added.ID.String(), nil)
	wantStatus(t, rec, http.StatusOK)
	var resp deleteTaskResponse
	decodeJSON(t, rec, &resp)
	if !resp.Deleted || resp.ID != added.ID {
		t.Errorf("delete response = %+v, want the task marked deleted", resp)
	}
	if env.tasks.GetTaskCount() != 0 {
		t.Error("the task is still in the queue")
	}

	rec = env.do(http.MethodDelete, APIPrefix+"/tasks/"+added.ID.String(), nil)
	wantStatus(t, rec, http.StatusNotFound)
	errorOf(t, rec)

	rec = env.do(http.MethodDelete, APIPrefix+"/tasks/bogus", nil)
	wantStatus(t, rec, http.StatusBadRequest)
	errorOf(t, rec)
}

// TestTaskDeleteRunning covers the 409 path: a running task cannot be deleted,
// which is the legacy rule.
func TestTaskDeleteRunning(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	added := env.addTask(env.fix.profile, env.fix.input)
	if err := env.tasks.Update(added.ID, func(t *model.Task) {
		t.Status.Progress = model.TaskRunning
	}); err != nil {
		t.Fatalf("mark running: %v", err)
	}

	rec := env.do(http.MethodDelete, APIPrefix+"/tasks/"+added.ID.String(), nil)
	wantStatus(t, rec, http.StatusConflict)
	info := errorOf(t, rec)
	if !strings.Contains(info.Summary, "正在运行") {
		t.Errorf("summary = %q, want it to mention the running task", info.Summary)
	}
	if env.tasks.GetTaskCount() != 1 {
		t.Error("a running task was deleted")
	}
}

// TestTaskPatchEnabled covers the enabled flag, including the queue's rule that
// a running task ignores it.
func TestTaskPatchEnabled(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	added := env.addTask(env.fix.profile, env.fix.input)

	rec := env.do(http.MethodPatch, APIPrefix+"/tasks/"+added.ID.String(),
		patchTaskRequest{Enabled: ptr(false)})
	wantStatus(t, rec, http.StatusOK)
	var resp taskResponse
	decodeJSON(t, rec, &resp)
	if resp.Task.Status.Enabled {
		t.Error("task is still enabled after enabled=false")
	}

	rec = env.do(http.MethodPatch, APIPrefix+"/tasks/"+added.ID.String(),
		patchTaskRequest{Enabled: ptr(true)})
	wantStatus(t, rec, http.StatusOK)
	decodeJSON(t, rec, &resp)
	if !resp.Task.Status.Enabled {
		t.Error("task is still disabled after enabled=true")
	}

	// A running task ignores the change, exactly like TaskStatus.IsEnabled.
	// The pool unticked it when it claimed the task, so it is already disabled
	// and stays that way.
	if err := env.tasks.Update(added.ID, func(t *model.Task) {
		t.Status.Progress = model.TaskRunning
		t.Status.Enabled = false
	}); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	rec = env.do(http.MethodPatch, APIPrefix+"/tasks/"+added.ID.String(),
		patchTaskRequest{Enabled: ptr(true)})
	wantStatus(t, rec, http.StatusOK)
	decodeJSON(t, rec, &resp)
	if resp.Task.Status.Enabled {
		t.Error("a running task changed its enabled flag")
	}
}

// TestTaskPatchPosition covers top/up/down and the 409 the queue answers for a
// task that cannot move.
func TestTaskPatchPosition(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	first := env.addTask(env.fix.profile, env.fix.input)
	second := env.addTask(env.fix.profile, env.fix.input+".b")
	third := env.addTask(env.fix.profile, env.fix.input+".c")

	order := func() []model.TaskID {
		tasks := env.tasks.Snapshot()
		out := make([]model.TaskID, 0, len(tasks))
		for i := range tasks {
			out = append(out, tasks[i].ID)
		}
		return out
	}

	// "up" on the second task swaps it with the first.
	rec := env.do(http.MethodPatch, APIPrefix+"/tasks/"+second.ID.String(),
		patchTaskRequest{Position: ptr(positionUp)})
	wantStatus(t, rec, http.StatusOK)
	if got := order(); got[0] != second.ID || got[1] != first.ID {
		t.Errorf("after up: %v, want %s then %s", got, second.ID, first.ID)
	}

	// "down" on the first task swaps it back.
	rec = env.do(http.MethodPatch, APIPrefix+"/tasks/"+first.ID.String(),
		patchTaskRequest{Position: ptr(positionDown)})
	wantStatus(t, rec, http.StatusOK)
	if got := order(); got[0] != second.ID {
		t.Errorf("after down: %v, want %s first", got, second.ID)
	}

	// "top" on the third task moves it to the front.
	rec = env.do(http.MethodPatch, APIPrefix+"/tasks/"+third.ID.String(),
		patchTaskRequest{Position: ptr(positionTop)})
	wantStatus(t, rec, http.StatusOK)
	if got := order(); got[0] != third.ID {
		t.Errorf("after top: %v, want %s first", got, third.ID)
	}

	// "top" again is the desired state, not an error.
	rec = env.do(http.MethodPatch, APIPrefix+"/tasks/"+third.ID.String(),
		patchTaskRequest{Position: ptr(positionTop)})
	wantStatus(t, rec, http.StatusOK)

	// Moving the first task up is a 409: there is nothing above it.
	rec = env.do(http.MethodPatch, APIPrefix+"/tasks/"+third.ID.String(),
		patchTaskRequest{Position: ptr(positionUp)})
	wantStatus(t, rec, http.StatusConflict)
	info := errorOf(t, rec)
	if !strings.Contains(info.Summary, "无法移动") {
		t.Errorf("summary = %q, want 无法移动任务", info.Summary)
	}

	// A running task cannot be reordered either.
	if err := env.tasks.Update(second.ID, func(t *model.Task) {
		t.Status.Progress = model.TaskRunning
	}); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	rec = env.do(http.MethodPatch, APIPrefix+"/tasks/"+second.ID.String(),
		patchTaskRequest{Position: ptr(positionTop)})
	wantStatus(t, rec, http.StatusConflict)
	errorOf(t, rec)

	// An unknown position is a 400.
	rec = env.do(http.MethodPatch, APIPrefix+"/tasks/"+third.ID.String(),
		patchTaskRequest{Position: ptr("sideways")})
	wantStatus(t, rec, http.StatusBadRequest)
	info = errorOf(t, rec)
	if !strings.Contains(info.Summary, "位置参数不合法") {
		t.Errorf("summary = %q, want 位置参数不合法", info.Summary)
	}
}

// TestTaskPatchName covers renaming and the empty-name 400.
func TestTaskPatchName(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	added := env.addTask(env.fix.profile, env.fix.input)

	rec := env.do(http.MethodPatch, APIPrefix+"/tasks/"+added.ID.String(),
		patchTaskRequest{Name: ptr("  renamed  ")})
	wantStatus(t, rec, http.StatusOK)
	var resp taskResponse
	decodeJSON(t, rec, &resp)
	if resp.Task.Name != "renamed" {
		t.Errorf("name = %q, want renamed", resp.Task.Name)
	}
	if resp.Task.Status.Name != "renamed" {
		t.Errorf("status name = %q, want renamed", resp.Task.Status.Name)
	}

	rec = env.do(http.MethodPatch, APIPrefix+"/tasks/"+added.ID.String(),
		patchTaskRequest{Name: ptr("   ")})
	wantStatus(t, rec, http.StatusBadRequest)
	errorOf(t, rec)
}

// TestTaskPatchErrors covers the 400/404 paths of PATCH /tasks/{id}.
func TestTaskPatchErrors(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	added := env.addTask(env.fix.profile, env.fix.input)

	cases := []struct {
		name string
		path string
		body string
		want int
	}{
		{"unknown task", APIPrefix + "/tasks/11111111-2222-4333-8444-555555555555", `{"enabled":false}`, http.StatusNotFound},
		{"bad id", APIPrefix + "/tasks/nope", `{"enabled":false}`, http.StatusBadRequest},
		{"empty body", APIPrefix + "/tasks/" + added.ID.String(), `{}`, http.StatusBadRequest},
		{"unknown field", APIPrefix + "/tasks/" + added.ID.String(), `{"enable":false}`, http.StatusBadRequest},
		{"malformed", APIPrefix + "/tasks/" + added.ID.String(), `{"enabled":`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := env.do(http.MethodPatch, tc.path, tc.body)
			wantStatus(t, rec, tc.want)
			errorOf(t, rec)
		})
	}
}

// TestPoolStartStop covers the pool endpoints, including the 202 the stop
// answers when the deadline passes first.
func TestPoolStartStop(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	rec := env.do(http.MethodPost, APIPrefix+"/pool/start", nil)
	wantStatus(t, rec, http.StatusOK)
	var resp poolResponse
	decodeJSON(t, rec, &resp)
	if !resp.Pool.Running {
		t.Errorf("pool is not running after start: %+v", resp.Pool)
	}
	if resp.Stopped != nil {
		t.Errorf("a start response must not carry stopped: %+v", resp)
	}

	rec = env.do(http.MethodPost, APIPrefix+"/pool/stop", nil)
	wantStatus(t, rec, http.StatusOK)
	decodeJSON(t, rec, &resp)
	if resp.Pool.Running {
		t.Errorf("pool is still running after stop: %+v", resp.Pool)
	}
	if resp.Stopped == nil || !*resp.Stopped {
		t.Errorf("stopped = %v, want true", resp.Stopped)
	}
	if resp.TimeoutSeconds == nil {
		t.Error("a stop response must echo the timeout")
	}
}

// TestPoolStartWithoutWorkers covers the 409 path: a pool with no worker cannot
// start, and silently reporting success would strand every task in the queue.
func TestPoolStartWithoutWorkers(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	env.pool.DeleteWorker("工作单元-1")

	rec := env.do(http.MethodPost, APIPrefix+"/pool/start", nil)
	wantStatus(t, rec, http.StatusConflict)
	info := errorOf(t, rec)
	if !strings.Contains(info.Summary, "工作单元") {
		t.Errorf("summary = %q, want it to mention the workers", info.Summary)
	}
}

// TestPoolStopTimeout covers the 202 answer and the timeout_seconds parameter.
func TestPoolStopTimeout(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	rec := env.do(http.MethodPost, APIPrefix+"/pool/stop?timeout_seconds=-1", nil)
	wantStatus(t, rec, http.StatusBadRequest)
	errorOf(t, rec)

	rec = env.do(http.MethodPost, APIPrefix+"/pool/stop?timeout_seconds=abc", nil)
	wantStatus(t, rec, http.StatusBadRequest)
	errorOf(t, rec)

	rec = env.do(http.MethodPost, APIPrefix+"/pool/stop?timeout_seconds=999999", nil)
	wantStatus(t, rec, http.StatusBadRequest)
	errorOf(t, rec)

	// A positive timeout on an idle pool stops immediately. Zero is not used
	// here: a zero deadline is already expired, so the result would depend on
	// which of two ready channels select picks.
	rec = env.do(http.MethodPost, APIPrefix+"/pool/stop?timeout_seconds=5", nil)
	wantStatus(t, rec, http.StatusOK)
	var resp poolResponse
	decodeJSON(t, rec, &resp)
	if resp.TimeoutSeconds == nil || *resp.TimeoutSeconds != 5 {
		t.Errorf("timeout_seconds = %v, want 5", resp.TimeoutSeconds)
	}
}

// TestPoolStopTimeoutExpires covers the 202 answer: an executor that ignores the
// cancelled context — the case the endpoint exists for — must not hold the
// request open forever.
func TestPoolStopTimeoutExpires(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	// release keeps the event channel open. The executor deliberately ignores
	// ctx.Done, which is what a stuck child process looks like from here: the
	// pool cancels the context, nothing reacts, and Stop has to time out.
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	submitted := make(chan struct{})
	exec := engine.ExecutorFuncs{
		CapabilitiesFunc: env.server.exec.Capabilities,
		SubmitFunc: func(_ context.Context, _ *model.Task) (<-chan model.StatusEvent, error) {
			ch := make(chan model.StatusEvent)
			go func() {
				defer close(ch)
				close(submitted)
				<-release
			}()
			return ch, nil
		},
	}
	pool := engine.NewWorkerManager(exec, env.tasks, platform.NewNumaWithCount(1))
	pool.AddWorker(1)
	if !pool.Start() {
		t.Fatal("pool did not start")
	}

	srv, err := New(Options{
		Tasks: env.tasks, Pool: pool, Exec: exec,
		StopTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Add a task so the worker picks it up and blocks in the executor.
	rec := env.doVia(srv, http.MethodPost, APIPrefix+"/tasks",
		mustJSON(t, addTaskRequest{ProfilePath: env.fix.profile}))
	wantStatus(t, rec, http.StatusCreated)
	select {
	case <-submitted:
	case <-time.After(5 * time.Second):
		t.Fatal("the executor was never entered")
	}

	rec = env.doVia(srv, http.MethodPost, APIPrefix+"/pool/stop", "")
	wantStatus(t, rec, http.StatusAccepted)

	var resp poolResponse
	decodeJSON(t, rec, &resp)
	if resp.Stopped == nil || *resp.Stopped {
		t.Errorf("stopped = %v, want false (the deadline expired)", resp.Stopped)
	}
	if resp.TimeoutSeconds == nil || *resp.TimeoutSeconds != 1 {
		t.Errorf("timeout_seconds = %v, want 1", resp.TimeoutSeconds)
	}
	if resp.Pool.Running {
		t.Error("a timed-out stop must still report the pool as not running")
	}
}

// TestPoolStopWaitsForAWellBehavedExecutor is the other half of the previous
// test: an executor that reacts to the cancelled context lets the stop finish,
// and the endpoint answers 200 rather than the 202 a stuck one gets.
func TestPoolStopWaitsForAWellBehavedExecutor(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	submitted := make(chan struct{})
	exec := engine.ExecutorFuncs{
		CapabilitiesFunc: env.server.exec.Capabilities,
		SubmitFunc: func(ctx context.Context, _ *model.Task) (<-chan model.StatusEvent, error) {
			ch := make(chan model.StatusEvent)
			go func() {
				defer close(ch)
				close(submitted)
				// Respecting the context is what a working processor does.
				<-ctx.Done()
			}()
			return ch, nil
		},
	}
	pool := engine.NewWorkerManager(exec, env.tasks, platform.NewNumaWithCount(1))
	pool.AddWorker(1)
	if !pool.Start() {
		t.Fatal("pool did not start")
	}

	srv, err := New(Options{Tasks: env.tasks, Pool: pool, Exec: exec, StopTimeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rec := env.doVia(srv, http.MethodPost, APIPrefix+"/tasks",
		mustJSON(t, addTaskRequest{ProfilePath: env.fix.profile}))
	wantStatus(t, rec, http.StatusCreated)
	select {
	case <-submitted:
	case <-time.After(5 * time.Second):
		t.Fatal("the executor was never entered")
	}

	rec = env.doVia(srv, http.MethodPost, APIPrefix+"/pool/stop", "")
	wantStatus(t, rec, http.StatusOK)

	var resp poolResponse
	decodeJSON(t, rec, &resp)
	if resp.Stopped == nil || !*resp.Stopped {
		t.Errorf("stopped = %v, want true", resp.Stopped)
	}

	// The cancelled task reached its terminal state, which is what the legacy
	// stop wrote ("已终止").
	task := env.tasks.Snapshot()[0]
	if task.Status.Progress != model.TaskError {
		t.Errorf("progress = %v, want ERROR", task.Status.Progress)
	}
}

// TestConfigGetPut covers the settings endpoints.
func TestConfigGetPut(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	rec := env.do(http.MethodGet, APIPrefix+"/config", nil)
	wantStatus(t, rec, http.StatusOK)
	var resp configResponse
	decodeJSON(t, rec, &resp)
	if resp.Config.LogLevel != platform.DefaultConfig().LogLevel {
		t.Errorf("log level = %q, want the default %q", resp.Config.LogLevel, platform.DefaultConfig().LogLevel)
	}

	want := platform.Config{
		VSPipePath:    env.fix.vspipe,
		LogLevel:      "INFO",
		SingleNUMA:    true,
		RPCheckerPath: env.fix.dir + "/rpchecker.exe",
		AVX512:        true,
		ReducePath:    false,
	}
	rec = env.do(http.MethodPut, APIPrefix+"/config", configResponse{Config: want})
	wantStatus(t, rec, http.StatusOK)
	decodeJSON(t, rec, &resp)
	if resp.Config != want {
		t.Errorf("response config = %+v, want %+v", resp.Config, want)
	}
	if got := env.config.stored(); got != want {
		t.Errorf("stored config = %+v, want %+v", got, want)
	}
	if env.config.saves != 1 {
		t.Errorf("saves = %d, want 1", env.config.saves)
	}

	rec = env.do(http.MethodGet, APIPrefix+"/config", nil)
	wantStatus(t, rec, http.StatusOK)
	decodeJSON(t, rec, &resp)
	if resp.Config != want {
		t.Errorf("GET after PUT = %+v, want %+v", resp.Config, want)
	}
}

// TestConfigPutErrors covers the error paths of PUT /api/v1/config.
func TestConfigPutErrors(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	cases := []struct {
		name string
		body string
		want int
	}{
		{"malformed", `{"config":`, http.StatusBadRequest},
		{"unknown field", `{"config":{"logLevel":"INFO","nope":1}}`, http.StatusBadRequest},
		{"bad log level", `{"config":{"logLevel":"LOUD"}}`, http.StatusBadRequest},
		// A null body must not be decoded into a zero-valued settings object:
		// that would silently wipe every setting.
		{"null config", `{"config":null}`, http.StatusBadRequest},
		{"no config key", `{}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := env.do(http.MethodPut, APIPrefix+"/config", tc.body)
			wantStatus(t, rec, tc.want)
			errorOf(t, rec)
		})
	}
	if env.config.saves != 0 {
		t.Errorf("a rejected config was saved %d times", env.config.saves)
	}
}

// TestConfigPutAppliesCallback covers OnConfigChange: the daemon needs the new
// settings to reach the log level and the tool paths.
func TestConfigPutAppliesCallback(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	var got platform.Config
	seen := 0
	srv, err := New(Options{
		Tasks: env.tasks,
		Pool:  env.pool,
		Exec:  env.server.exec,
		Config: &fakeConfigStore{
			cfg: platform.DefaultConfig(),
		},
		OnConfigChange: func(cfg platform.Config) {
			seen++
			got = cfg
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	want := platform.Config{LogLevel: "ERROR", ReducePath: true}
	req := httptest.NewRequest(http.MethodPut, APIPrefix+"/config",
		strings.NewReader(`{"config":{"logLevel":"ERROR","reducePath":true}}`))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	wantStatus(t, rec, http.StatusOK)
	if seen != 1 {
		t.Fatalf("callback ran %d times, want 1", seen)
	}
	if got != want {
		t.Errorf("callback config = %+v, want %+v", got, want)
	}
}

// TestConfigStoreFailure covers the 500 path of the settings endpoints.
func TestConfigStoreFailure(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	broken := &fakeConfigStore{err: errBoom}

	srv, err := New(Options{Tasks: env.tasks, Pool: env.pool, Exec: env.server.exec, Config: broken})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rec := env.doVia(srv, http.MethodGet, APIPrefix+"/config", "")
	wantStatus(t, rec, http.StatusInternalServerError)
	errorOf(t, rec)

	rec = env.doVia(srv, http.MethodPut, APIPrefix+"/config", `{"config":{"logLevel":"INFO"}}`)
	wantStatus(t, rec, http.StatusInternalServerError)
	errorOf(t, rec)
}

// TestRouting covers the routes that no handler owns: unknown paths, wrong
// methods and the prefix itself.
func TestRouting(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	cases := []struct {
		name   string
		method string
		path   string
		want   int
	}{
		{"unknown path", http.MethodGet, APIPrefix + "/nope", http.StatusNotFound},
		{"prefix itself", http.MethodGet, APIPrefix, http.StatusNotFound},
		{"prefix with slash", http.MethodGet, APIPrefix + "/", http.StatusNotFound},
		{"trailing slash on tasks", http.MethodGet, APIPrefix + "/tasks/", http.StatusNotFound},
		{"wrong method on tasks", http.MethodPut, APIPrefix + "/tasks", http.StatusMethodNotAllowed},
		{"wrong method on status", http.MethodPost, APIPrefix + "/status", http.StatusMethodNotAllowed},
		{"wrong method on pool", http.MethodGet, APIPrefix + "/pool/start", http.StatusMethodNotAllowed},
		{"wrong method on config", http.MethodDelete, APIPrefix + "/config", http.StatusMethodNotAllowed},
		{"wrong method on one task", http.MethodPost, APIPrefix + "/tasks/11111111-2222-4333-8444-555555555555", http.StatusMethodNotAllowed},
		{"outside the prefix", http.MethodGet, "/", http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := env.do(tc.method, tc.path, nil)
			wantStatus(t, rec, tc.want)
			errorOf(t, rec)
			if tc.want == http.StatusMethodNotAllowed {
				if allow := rec.Header().Get("Allow"); allow == "" {
					t.Error("405 without an Allow header")
				}
			}
		})
	}
}

// TestHeadOnGetRoutes covers HEAD, which must be answered like GET.
func TestHeadOnGetRoutes(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	added := env.addTask(env.fix.profile, env.fix.input)

	for _, path := range []string{
		APIPrefix + "/status",
		APIPrefix + "/tasks",
		APIPrefix + "/tasks/" + added.ID.String(),
		APIPrefix + "/config",
	} {
		rec := env.do(http.MethodHead, path, nil)
		if rec.Code != http.StatusOK {
			t.Errorf("HEAD %s = %d, want 200", path, rec.Code)
		}
	}
}

// TestAccessLogAndPanic covers the observation wrapper: a handler that panics
// still answers the standard error object.
func TestAccessLogAndPanic(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	panicking := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})
	rec := httptest.NewRecorder()
	env.server.observe(panicking).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	wantStatus(t, rec, http.StatusInternalServerError)
	info := errorOf(t, rec)
	if !strings.Contains(info.Detail, "boom") {
		t.Errorf("detail = %q, want it to carry the panic", info.Detail)
	}
}

// TestToErrorInfo covers the error mapping itself, including the two types that
// carry a summary.
func TestToErrorInfo(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("x", maxErrorDetail+10)
	cases := []struct {
		name  string
		err   error
		field string
		want  string
	}{
		{"okerr", okerrNew("标题", "细节"), "", "标题"},
		{"validation", validationError("标题", "细节", "Field"), "Field", "标题"},
		{"nil", nil, "", "未知错误"},
		{"detail clamped", okerrNew("长", long), "", "长"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info := toErrorInfo(tc.err)
			if info.Summary != tc.want {
				t.Errorf("summary = %q, want %q", info.Summary, tc.want)
			}
			if info.Field != tc.field {
				t.Errorf("field = %q, want %q", info.Field, tc.field)
			}
			if len(info.Detail) > maxErrorDetail+len("...") {
				t.Errorf("detail is %d bytes, want it clamped", len(info.Detail))
			}
		})
	}
}

// TestErrorStatus covers the status mapping.
func TestErrorStatus(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, http.StatusOK},
		{"validation", validationError("标题", "细节", ""), http.StatusBadRequest},
		{"not found", notFoundTask("11111111-2222-4333-8444-555555555555"), http.StatusNotFound},
		{"config", okerrConfig(), http.StatusBadRequest},
		{"unsupported", okerrUnsupported(), http.StatusBadRequest},
		{"mismatch", okerrMismatch(), http.StatusBadRequest},
		{"io", okerrIO(), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := errorStatus(tc.err); got != tc.want {
				t.Errorf("errorStatus(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

// TestDecodeBodyLimits covers the body bound and the single-value rule.
func TestDecodeBodyLimits(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	huge := `{"profile_path":"` + strings.Repeat("a", maxBodyBytes+1) + `"}`
	rec := env.do(http.MethodPost, APIPrefix+"/tasks", huge)
	wantStatus(t, rec, http.StatusBadRequest)
	errorOf(t, rec)

	rec = env.do(http.MethodPost, APIPrefix+"/tasks", `{"profile_path":"a.json"}`+"\n\n\t ")
	wantStatus(t, rec, http.StatusBadRequest) // the file does not exist
	info := errorOf(t, rec)
	if !strings.Contains(info.Summary, "无法读取json文件") {
		t.Errorf("trailing whitespace after a value must still decode: %q", info.Summary)
	}
}

// TestNewValidation covers the constructor's requirements.
func TestNewValidation(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	cases := []struct {
		name string
		opts Options
	}{
		{"no tasks", Options{Pool: env.pool, Exec: env.server.exec}},
		{"no pool", Options{Tasks: env.tasks, Exec: env.server.exec}},
		{"no executor", Options{Tasks: env.tasks, Pool: env.pool}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.opts); err == nil {
				t.Error("New accepted an incomplete configuration")
			}
		})
	}
}

// TestDefaultConfigStore covers the production store: with no configuration
// file it must return the defaults rather than an error.
func TestDefaultConfigStore(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)

	store := platformConfigStore{}
	cfg, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg != platform.DefaultConfig() {
		t.Errorf("config = %+v, want the defaults %+v", cfg, platform.DefaultConfig())
	}

	want := platform.Config{LogLevel: "WARN", ReducePath: true}
	if err := store.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load after Save: %v", err)
	}
	if got != want {
		t.Errorf("config = %+v, want %+v", got, want)
	}
}
