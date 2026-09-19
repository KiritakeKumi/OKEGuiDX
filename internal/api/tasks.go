package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/KiritakeKumi/OKEGuiDX/internal/engine"
	"github.com/KiritakeKumi/OKEGuiDX/internal/log"
	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/node"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
	"github.com/KiritakeKumi/OKEGuiDX/internal/profile"
	"github.com/KiritakeKumi/OKEGuiDX/internal/toolchain"
)

// taskListResponse is the payload of GET /api/v1/tasks.
type taskListResponse struct {
	Tasks []model.Task `json:"tasks"`
	Count int          `json:"count"`
}

// taskResponse is the payload of the endpoints that return one task.
type taskResponse struct {
	Task model.Task `json:"task"`
}

// addTaskRequest is the body of POST /api/v1/tasks.
//
// A task is one source turned into one output (CLUSTER.md §3), so one request
// adds exactly one task. A profile that lists several inputs is therefore
// rejected until the caller names the one it wants: the legacy wizard did the
// same thing by looping over the inputs and creating a task per file, and
// keeping that decision on the caller's side is what makes the response
// unambiguous and a partial failure representable.
type addTaskRequest struct {
	// ProfilePath is the path of a task profile file (.json). It is what the
	// queue stores next to the task, and it is how the pipeline re-reads the
	// profile after a restart, so it is the durable form.
	ProfilePath string `json:"profile_path"`
	// Profile is a task profile sent inline as a JSON object. It has no file
	// of its own: relative paths in it resolve against BaseDir (or the
	// daemon's working directory when that is empty), and a task recovered
	// from the queue file can no longer be reloaded from disk. Prefer
	// profile_path for anything that must survive a restart.
	//
	// The object form is for a client that builds a profile programmatically.
	// A client that has the bytes of an existing file wants ProfileText
	// instead, because those bytes are usually not strict JSON.
	Profile json.RawMessage `json:"profile"`
	// ProfileText is a task profile sent inline as the exact text of a profile
	// file. It goes through the same tolerant parser a file does, so the
	// trailing commas every real profile contains are accepted — which the
	// object form cannot do, since the request body itself has to be strict
	// JSON for the object to be parsed at all.
	ProfileText string `json:"profile_text"`
	// Inputs overrides the profile's InputFiles. Exactly one entry is
	// accepted. This is the wizard's "choose the source" step: one profile,
	// one request per episode.
	Inputs []string `json:"inputs"`
	// BaseDir is the directory an inline profile's relative paths resolve
	// against. It stands in for the profile file's own directory, which is
	// what a profile loaded from disk uses.
	//
	// It is required in practice for an inline profile: without it, "demo.vpy"
	// means whatever the daemon's working directory happens to be. It is
	// ignored when ProfilePath is given, because then the file's directory is
	// the answer.
	BaseDir string `json:"base_dir"`

	// InputScript, WorkingPathPrefix and OutputPathPrefix are the three fields
	// the legacy wizard filled in and the pipeline requires (see
	// engine.validateForRun). They are profile fields, not request fields: a
	// profile file usually does not carry them, because the wizard derived
	// them from the project directory and the source path.
	//
	// The derivation — the PROJECTDIR/DEBUG tag rewriting, the per-source .vpy
	// name, the reducePath shortening, the output directory rule — belongs to
	// the new-task wizard (WORKSTREAMS.md F2), not to the HTTP layer, so it is
	// not reimplemented here. A caller that already knows the answers passes
	// them in; a caller that does not gets a task that is queued but cannot
	// run, and the pipeline says exactly which field is missing.
	InputScript       string `json:"input_script"`
	WorkingPathPrefix string `json:"working_path_prefix"`
	OutputPathPrefix  string `json:"output_path_prefix"`

	// Name overrides the generated task name.
	Name string `json:"name"`
}

// handleTaskList implements GET /api/v1/tasks.
func (s *Server) handleTaskList(w http.ResponseWriter, r *http.Request) {
	tasks := s.tasks.Snapshot()
	writeJSON(w, http.StatusOK, taskListResponse{Tasks: tasks, Count: len(tasks)})
}

// handleTaskGet implements GET /api/v1/tasks/{id}.
func (s *Server) handleTaskGet(w http.ResponseWriter, r *http.Request) {
	id, ok := s.lookupTask(w, r)
	if !ok {
		return
	}
	task, ok := s.getTask(w, id)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, taskResponse{Task: task})
}

// handleTaskAdd implements POST /api/v1/tasks.
//
// The handler itself does no queue arithmetic: it builds a validated task and
// hands it to the pool, which is what decides whether the new task also starts
// running (WorkerManager.AddTask → TryStartNewWorker).
func (s *Server) handleTaskAdd(w http.ResponseWriter, r *http.Request) {
	var req addTaskRequest
	if err := decodeBody(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	built, err := s.buildTask(r.Context(), &req)
	if err != nil {
		writeError(w, errorStatus(err), err)
		return
	}

	// Mirrors TaskManager.HasActiveTask: the same profile and the same source
	// must not be queued twice while the first one is still waiting or running.
	if s.tasks.HasActiveTask(built.ConfigPath, built.Input) {
		writeError(w, http.StatusConflict, okerr.New(okerr.KindConfig,
			"任务已经存在", "输入文件 %s 已经使用此配置添加到任务列表。", built.Input))
		return
	}

	count, err := s.pool.AddTask(built.Task, built.ConfigPath)
	if err != nil {
		writeError(w, errorStatus(err), err)
		return
	}
	log.Info("新增任务", "task", built.Task.ID, "input", built.Task.Status.Input, "queue", count)

	// Re-reading the task is what makes the response carry the queue's own
	// view (the generated name, the derived display input) rather than the
	// request's.
	stored, ok := s.tasks.Task(built.Task.ID)
	if !ok {
		writeError(w, http.StatusInternalServerError,
			okerr.New(okerr.KindUnknown, "任务添加失败", "任务 %s 添加后立即消失。", built.Task.ID))
		return
	}
	writeJSON(w, http.StatusCreated, taskResponse{Task: stored})
}

// handleTaskDelete implements DELETE /api/v1/tasks/{id}.
func (s *Server) handleTaskDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := s.lookupTask(w, r)
	if !ok {
		return
	}
	task, ok := s.getTask(w, id)
	if !ok {
		return
	}
	// The queue refuses to delete a running task (TaskManager.DeleteTask), and
	// reporting that as 409 lets the UI explain it instead of pretending the
	// task is gone.
	if task.Status.Progress == model.TaskRunning {
		writeError(w, http.StatusConflict, okerr.New(okerr.KindConfig,
			"无法删除正在运行的任务", "任务 %s 正在运行，请先停止工作单元。", id))
		return
	}

	removed, err := s.tasks.DeleteTask(id)
	if err != nil {
		writeError(w, errorStatus(err), err)
		return
	}
	if !removed {
		writeError(w, http.StatusNotFound, notFoundTask(id))
		return
	}
	log.Info("删除任务", "task", id)
	writeJSON(w, http.StatusOK, deleteTaskResponse{ID: id, Deleted: true})
}

// deleteTaskResponse is the payload of a successful DELETE.
type deleteTaskResponse struct {
	ID      model.TaskID `json:"id"`
	Deleted bool         `json:"deleted"`
}

// patchTaskRequest is the body of PATCH /api/v1/tasks/{id}. Every field is a
// pointer so that "absent" and "false" are distinguishable, and a body that
// changes nothing can be rejected instead of silently succeeding.
type patchTaskRequest struct {
	// Enabled ticks or unticks the task's checkbox. The queue ignores the
	// change while the task runs, which is the legacy IsEnabled behaviour.
	Enabled *bool `json:"enabled"`
	// Position moves a waiting task: "top", "up" or "down". These are the
	// legacy buttons (MoveTaskTop / MoveTaskUp / MoveTaskDown).
	Position *string `json:"position"`
	// Name renames the task.
	Name *string `json:"name"`
}

// Position values accepted by PATCH.
const (
	positionTop  = "top"
	positionUp   = "up"
	positionDown = "down"
)

// handleTaskPatch implements PATCH /api/v1/tasks/{id}.
func (s *Server) handleTaskPatch(w http.ResponseWriter, r *http.Request) {
	id, ok := s.lookupTask(w, r)
	if !ok {
		return
	}
	if _, ok := s.getTask(w, id); !ok {
		return
	}

	var req patchTaskRequest
	if err := decodeBody(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Enabled == nil && req.Position == nil && req.Name == nil {
		writeError(w, http.StatusBadRequest, okerr.New(okerr.KindConfig,
			"请求内容不合法", "至少要指定 enabled、position 或 name 之一。"))
		return
	}

	if req.Enabled != nil {
		if err := s.tasks.SetEnabled(id, *req.Enabled); err != nil {
			writeError(w, errorStatus(err), err)
			return
		}
	}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			writeError(w, http.StatusBadRequest, okerr.New(okerr.KindConfig,
				"任务名不合法", "任务名不能为空。"))
			return
		}
		if err := s.tasks.Update(id, func(t *model.Task) {
			t.Name = name
			t.Status.Name = name
		}); err != nil {
			writeError(w, errorStatus(err), err)
			return
		}
	}
	if req.Position != nil {
		moved, err := s.moveTask(id, *req.Position)
		if err != nil {
			writeError(w, errorStatus(err), err)
			return
		}
		if !moved {
			// The task exists but the queue will not reorder it: it is running
			// or already at the end it was asked to leave. That is a conflict
			// with the queue's state, not a malformed request.
			writeError(w, http.StatusConflict, notMovable(id))
			return
		}
	}

	task, ok := s.getTask(w, id)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, taskResponse{Task: task})
}

// moveTask applies a position change. It reports false when the queue declined
// to move the task, which is the caller's cue to answer 409; a bad position
// name is a real error.
func (s *Server) moveTask(id model.TaskID, position string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(position)) {
	case positionTop:
		result, err := s.tasks.MoveTaskTop(id)
		if err != nil {
			return false, err
		}
		// Already at the top is the requested state, so it is not a conflict;
		// the response shows the queue either way.
		if result == engine.MoveTopOK || result == engine.MoveTopAlready {
			return true, nil
		}
		return false, nil
	case positionUp:
		moved, err := s.tasks.MoveTaskUp(id)
		return moved, err
	case positionDown:
		moved, err := s.tasks.MoveTaskDown(id)
		return moved, err
	default:
		return false, okerr.New(okerr.KindConfig, "位置参数不合法",
			"position 只能是 top、up 或 down（当前 %q）。", position)
	}
}

// notMovable is the error for a task the queue will not reorder: unknown, or
// not waiting.
func notMovable(id model.TaskID) error {
	return okerr.New(okerr.KindConfig, "无法移动任务",
		"任务 %s 不在等待中，无法移动。", id)
}

// builtTask is a task plus the profile path it came from. The manager stores
// the path beside the task because model.Task is profile-agnostic; it is also
// what the duplicate check compares.
type builtTask struct {
	Task       *model.Task
	ConfigPath string
	Input      model.FileRef
}

// buildTask turns a request into a validated task. It performs no queue
// mutation, so the caller can still answer 409 or 500 without having changed
// anything.
func (s *Server) buildTask(ctx context.Context, req *addTaskRequest) (*builtTask, error) {
	hasObject := profileObjectGiven(req.Profile)
	forms := 0
	for _, given := range []bool{req.ProfilePath != "", hasObject, req.ProfileText != ""} {
		if given {
			forms++
		}
	}
	if forms == 0 {
		return nil, okerr.New(okerr.KindConfig, "请求内容不合法",
			"必须提供 profile_path、profile 或 profile_text 之一。")
	}
	if forms > 1 {
		return nil, okerr.New(okerr.KindConfig, "请求内容不合法",
			"profile_path、profile 和 profile_text 只能提供其中一个。")
	}

	var (
		prof   *profile.Profile
		config string
		err    error
	)
	switch {
	case req.ProfilePath != "":
		config, err = filepath.Abs(req.ProfilePath)
		if err != nil {
			return nil, okerr.Wrap(err, okerr.KindConfig, "配置文件路径不合法",
				"%q: %v", req.ProfilePath, err)
		}
		prof, err = profile.Load(config)
	case hasObject:
		// An inline profile has no file, so relative paths resolve against
		// BaseDir, or the process working directory when that is empty.
		prof, err = profile.Parse(string(req.Profile), "")
	case req.ProfileText != "":
		// The text form goes through the same tolerant parser a file does.
		prof, err = profile.Parse(req.ProfileText, "")
	}
	if err != nil {
		return nil, err
	}

	dir := ""
	if config != "" {
		if dir, err = profile.DirOf(config); err != nil {
			return nil, okerr.Wrap(err, okerr.KindConfig, "配置文件路径不合法",
				"%q 没有上级目录。", config)
		}
	} else if req.BaseDir != "" {
		dir, err = filepath.Abs(req.BaseDir)
		if err != nil {
			return nil, okerr.Wrap(err, okerr.KindConfig, "目录路径不合法",
				"%q: %v", req.BaseDir, err)
		}
	}

	input, err := s.selectInput(prof, dir, req.Inputs)
	if err != nil {
		return nil, err
	}
	applyPaths(prof, req)

	// Validation needs the outside world, which only the daemon has: the
	// installed VapourSynth version, the script text and the toolchain. It
	// runs before anything reaches the queue, so a bad profile is a 400.
	if err := profile.Validate(prof, s.validationInputs(ctx, prof, dir)); err != nil {
		return nil, err
	}

	task := profile.ToModel(prof, prof.Config)
	ref := model.NewFileRef(input)
	task.Inputs = []model.FileRef{ref}
	task.Status.Input = ref
	if name := strings.TrimSpace(req.Name); name != "" {
		task.Name = name
		task.Status.Name = name
	}
	return &builtTask{Task: task, ConfigPath: config, Input: ref}, nil
}

// applyPaths copies the three paths the wizard normally fills in onto the
// profile. The pipeline refuses to run without them (engine.validateForRun), so
// a request may supply them; an empty value leaves whatever the profile already
// had.
//
// This is the seam between the API and the new-task wizard (F2): the derivation
// of these paths from the project directory and the source file is wizard
// logic, and it is deliberately not duplicated here.
func applyPaths(prof *profile.Profile, req *addTaskRequest) {
	if req.InputScript != "" {
		prof.InputScript = req.InputScript
	}
	if req.WorkingPathPrefix != "" {
		prof.WorkingPathPrefix = req.WorkingPathPrefix
	}
	if req.OutputPathPrefix != "" {
		prof.OutputPathPrefix = req.OutputPathPrefix
	}
}

// selectInput picks the single source of the task: the request's override when
// it has one, the profile's own list otherwise.
//
// The checks run in the order AddTaskService.LoadInputFiles ran them, because
// the operator's error messages come from there: an empty entry and a duplicate
// are reported before the list is judged for length. A profile that names
// several files is the case the legacy wizard handled by looping over them and
// creating one task per file; a request has to say which one it wants, and the
// candidates are named so the UI can offer them.
func (s *Server) selectInput(prof *profile.Profile, dir string, override []string) (string, error) {
	list := override
	field := "inputs"
	if len(list) == 0 {
		list = prof.InputFiles
		field = "InputFiles"
	}
	if len(list) == 0 {
		return "", &profile.ValidationError{
			Summary: "没有输入文件",
			Detail:  "profile 里没有输入文件，也没有通过 inputs 指定。",
			Field:   "InputFiles",
		}
	}

	resolved := make([]string, 0, len(list))
	seen := make(map[string]struct{}, len(list))
	for _, raw := range list {
		if strings.TrimSpace(raw) == "" {
			return "", &profile.ValidationError{
				Summary: "输入文件不合法",
				Detail:  "输入文件路径为空。",
				Field:   field,
			}
		}
		path := resolveRelative(raw, dir)
		if _, dup := seen[path]; dup {
			return "", &profile.ValidationError{
				Summary: "输入文件有重复",
				Detail:  "指定的文件(" + path + ")重复了，请复查输入文件列表。",
				Field:   field,
			}
		}
		seen[path] = struct{}{}
		resolved = append(resolved, path)
	}

	if len(resolved) > 1 {
		return "", &profile.ValidationError{
			Summary: "需要指定输入文件",
			Detail: "一个任务只能有一个输入文件，请用 inputs 指定其中一个：" +
				strings.Join(resolved, "、") + "。",
			Field: field,
		}
	}
	return resolved[0], nil
}

// validationInputs gathers what profile.Validate needs from this node.
//
// The VapourSynth version and the script text are read from the profile's own
// directory, exactly as AddTaskService did. A file that cannot be read is left
// unreported here: the corresponding check then fails with the legacy wording,
// which is the message the operator expects.
func (s *Server) validationInputs(ctx context.Context, prof *profile.Profile, dir string) profile.Inputs {
	in := profile.Inputs{
		InputExists: func(rel string) bool {
			_, statErr := os.Stat(resolveRelative(rel, dir))
			return statErr == nil
		},
		ResolveEncoder: func(rel string) (string, bool) {
			path := resolveRelative(rel, dir)
			_, statErr := os.Stat(path)
			return path, statErr == nil
		},
	}

	// The toolchain is read once and shared by both lookups below.
	var caps node.Capabilities
	if c, err := s.exec.Capabilities(ctx); err == nil {
		caps = c
		in.ToolchainEncoder = func(t profile.EncoderType) (string, bool) {
			return encoderFor(caps, t)
		}
		if prof.Version >= 3 {
			in.InstalledVSVersion = vsVersion(caps)
		}
	}

	if prof.InputScript != "" {
		path := resolveRelative(prof.InputScript, dir)
		if raw, err := os.ReadFile(path); err == nil {
			in.VpyText = string(raw)
			in.VpyRead = true
			prof.InputScript = path
		}
	}
	return in
}

// profileObjectGiven reports whether the profile field actually carries an
// object. json.RawMessage keeps the literal bytes, so an absent field and an
// explicit null both arrive as nil or "null"; neither is a profile.
func profileObjectGiven(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

// resolveRelative resolves a path from a profile against the profile's
// directory, mirroring PathUtils.GetFullPath: an absolute path is kept as-is.
func resolveRelative(rel, dir string) string {
	if rel == "" {
		return ""
	}
	if filepath.IsAbs(rel) {
		return filepath.Clean(rel)
	}
	if dir == "" {
		return filepath.Clean(rel)
	}
	return filepath.Join(dir, rel)
}

// encoderFor resolves the platform's default encoder for a type from the
// node's capabilities, so validation sees the toolchain the engine will use.
func encoderFor(caps node.Capabilities, t profile.EncoderType) (string, bool) {
	var name string
	switch t {
	case profile.EncoderX264:
		name = toolchain.ToolX264
	case profile.EncoderX265:
		name = toolchain.ToolX265
	case profile.EncoderSVTAV1:
		name = toolchain.ToolSVTAV1
	default:
		return "", false
	}
	info, ok := caps.Tool(name)
	if !ok || info.Path == "" {
		return "", false
	}
	return info.Path, true
}

// vsVersion reads the VapourSynth version from the VERSION file next to
// vspipe. It is what AddTaskService compared a v3 profile's VSVersion against,
// so an empty result simply skips that check.
func vsVersion(caps node.Capabilities) string {
	info, ok := caps.Tool(toolchain.ToolVSPipe)
	if !ok || info.Path == "" {
		return ""
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(info.Path), "VERSION"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}
