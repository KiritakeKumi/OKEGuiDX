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
	"github.com/KiritakeKumi/OKEGuiDX/internal/textfile"
	"github.com/KiritakeKumi/OKEGuiDX/internal/toolchain"
	"github.com/KiritakeKumi/OKEGuiDX/internal/wizard"
)

// inlineProfileName stands in for the profile file of an inline profile when
// the wizard needs a project file. Only its directory is ever used, so the name
// is arbitrary; it is spelled out to keep that visible at the call site.
const inlineProfileName = "inline.json"

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
	// Two ways to fill them exist. A caller that already knows the answers
	// passes them in; a caller that does not sets WriteVpy and the server
	// derives and writes them with internal/wizard, which is the same
	// derivation POST /tasks/prepare previews. A value supplied here still
	// wins, so WriteVpy is an addition, not a replacement.
	InputScript       string `json:"input_script"`
	WorkingPathPrefix string `json:"working_path_prefix"`
	OutputPathPrefix  string `json:"output_path_prefix"`

	// WriteVpy makes the server assemble the task the way the legacy wizard
	// did: the per-source .vpy is generated and written, and the three path
	// fields above are filled in from the profile's directory and the source
	// path. Without it the caller is responsible for all four, which is what
	// the field comments above describe.
	//
	// The three paths are derived for every input of the profile, but only the
	// selected one is written, because a request creates exactly one task.
	WriteVpy bool `json:"write_vpy"`

	// Name overrides the generated task name.
	Name string `json:"name"`
}

// handleTaskList implements GET /api/v1/tasks.
func (s *Server) handleTaskList(w http.ResponseWriter, r *http.Request) {
	tasks := s.tasks.Snapshot()
	writeJSON(w, http.StatusOK, taskListResponse{Tasks: tasks, Count: len(tasks)})
}

// prepareRequest is the body of POST /api/v1/tasks/prepare. It is the same
// profile-carrying half of addTaskRequest, without the fields that only make
// sense once a task exists (name, the three paths, write_vpy).
//
// The request may name one input or none: the endpoint exists to show the
// operator what every source of the profile would become, which is the wizard's
// "preview" step. A request that names one gets that one task back.
type prepareRequest struct {
	// ProfilePath, Profile, ProfileText and BaseDir mean exactly what they mean
	// in addTaskRequest; see there for the three profile forms.
	ProfilePath string          `json:"profile_path"`
	Profile     json.RawMessage `json:"profile"`
	ProfileText string          `json:"profile_text"`
	BaseDir     string          `json:"base_dir"`
	// Inputs, when given, restricts the preview to those sources. The legacy
	// wizard let the operator pick a subset before finishing.
	Inputs []string `json:"inputs"`
}

// preparedTask is one source's assembly result: everything POST /tasks would
// write and store, without writing or storing anything.
type preparedTask struct {
	// Name is the task name the legacy wizard generated for this source.
	Name string `json:"name"`
	// InputFile is the source path, resolved against the profile's directory.
	InputFile string `json:"input_file"`
	// VpyFile is where the generated script would be written and what the
	// profile's InputScript would be set to.
	VpyFile string `json:"vpy_file"`
	// WorkingPathPrefix and OutputPathPrefix are the two path prefixes the
	// pipeline needs; the output prefix is the directory the deliverable goes
	// to.
	WorkingPathPrefix string `json:"working_path_prefix"`
	OutputPathPrefix  string `json:"output_path_prefix"`
	// Script is the generated .vpy text. A preview shows it and lets the
	// operator keep a copy before anything is written.
	Script string `json:"script"`
}

// prepareResponse is the payload of POST /api/v1/tasks/prepare.
type prepareResponse struct {
	// Tasks holds one entry per previewed source, in profile order.
	Tasks []preparedTask `json:"tasks"`
	Count int            `json:"count"`
	// MapFile is where ReducePathMap.log is kept. It is reported even when this
	// pass shortened nothing, because it names the output directory a client
	// may want to show.
	MapFile string `json:"map_file"`
}

// handleTaskPrepare implements POST /api/v1/tasks/prepare.
//
// It is the wizard's preview step: the profile is assembled the same way
// POST /tasks with write_vpy would assemble it, but nothing is written and
// nothing is queued. The split is the legacy WizardWindow's own: the paths were
// derived when the page was drawn and the files were written only when the
// operator pressed finish, so a client can show the result and let the operator
// change their mind.
//
// Nothing is validated beyond the derivation: the endpoint reports what the
// assembly would produce, not whether the sources exist. profile.Validate is
// what POST /tasks runs, and a preview that failed on a missing encoder would
// make the wizard unable to draw its page before the toolchain is installed.
func (s *Server) handleTaskPrepare(w http.ResponseWriter, r *http.Request) {
	var req prepareRequest
	if err := decodeBody(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	prof, config, dir, err := s.loadRequestProfile(&addTaskRequest{
		ProfilePath: req.ProfilePath,
		Profile:     req.Profile,
		ProfileText: req.ProfileText,
		BaseDir:     req.BaseDir,
	})
	if err != nil {
		writeError(w, errorStatus(err), err)
		return
	}

	projectFile, err := s.projectFileFor(config, dir)
	if err != nil {
		writeError(w, errorStatus(err), err)
		return
	}

	result, err := wizard.Derive(prof, wizard.Options{
		ProjectFile: projectFile,
		ReducePath:  s.reducePath(),
	})
	if err != nil {
		writeError(w, errorStatus(err), err)
		return
	}

	tasks := make([]preparedTask, 0, len(result.Tasks))
	selected := make(map[string]struct{}, len(req.Inputs))
	for _, raw := range req.Inputs {
		if strings.TrimSpace(raw) == "" {
			writeError(w, http.StatusBadRequest, &profile.ValidationError{
				Summary: "输入文件不合法",
				Detail:  "输入文件路径为空。",
				Field:   "inputs",
			})
			return
		}
		// Request paths resolve the same way the profile's own entries do.
		selected[resolveRelative(raw, dir)] = struct{}{}
	}
	for _, t := range result.Tasks {
		if len(selected) > 0 {
			if _, want := selected[t.InputFile]; !want {
				continue
			}
		}
		tasks = append(tasks, preparedTask{
			Name:              t.Name,
			InputFile:         t.InputFile,
			VpyFile:           t.VpyFile,
			WorkingPathPrefix: t.WorkingPathPrefix,
			OutputPathPrefix:  t.OutputPathPrefix,
			Script:            t.Script,
		})
	}
	if len(selected) > 0 && len(tasks) == 0 {
		writeError(w, http.StatusNotFound, okerr.New(okerr.KindNotFound,
			"找不到输入文件", "profile 里没有请求指定的输入文件。"))
		return
	}
	writeJSON(w, http.StatusOK, prepareResponse{
		Tasks:   tasks,
		Count:   len(tasks),
		MapFile: result.MapFile,
	})
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

// handleTaskCancel implements POST /api/v1/tasks/{id}/cancel.
//
// It is the single-task counterpart of POST /pool/stop, which is the only
// cancellation the API had: the legacy WorkerManager.StopWorker stops one
// worker, and a REST client knows a task id, not a worker name.
//
// The queue decides what cancelling means at each stage, and the response is
// the task's state afterwards:
//
//   - 200: the task was waiting or running and has been cancelled. A running
//     task is stopped through the executor and its context, so the child
//     processes die with it; the worker pool writes the "已终止" state once the
//     executor's event stream closes, exactly as a pool stop does.
//   - 404: no such task.
//   - 409: the task exists but is already finished or failed. Cancelling it
//     would rewrite a settled result, so the conflict is reported instead.
func (s *Server) handleTaskCancel(w http.ResponseWriter, r *http.Request) {
	id, ok := s.lookupTask(w, r)
	if !ok {
		return
	}
	if _, ok := s.getTask(w, id); !ok {
		return
	}

	cancelled, err := s.pool.CancelTask(id)
	if err != nil {
		writeError(w, errorStatus(err), err)
		return
	}
	if !cancelled {
		writeError(w, http.StatusConflict, notCancellable(id))
		return
	}

	log.Info("取消任务", "task", id)
	// Re-reading is what makes the response carry the queue's view: a waiting
	// task is already "已终止", while a running one is still RUNNING until its
	// worker consumes the cancelled event stream.
	task, ok := s.getTask(w, id)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, taskResponse{Task: task})
}

// notCancellable is the error for a task that exists but cannot be cancelled
// because it already reached a terminal state.
func notCancellable(id model.TaskID) error {
	return okerr.New(okerr.KindConfig, "无法取消任务",
		"任务 %s 已经结束，无法取消。", id)
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
//
// The order mirrors the legacy wizard: the profile is validated first, against
// the script the operator wrote, and only then is the task assembled. It has to
// be that way round, because the generated .vpy has its #OKE:INPUTFILE tag
// replaced by the source path and would no longer pass validateVpy.
func (s *Server) buildTask(ctx context.Context, req *addTaskRequest) (*builtTask, error) {
	prof, config, dir, err := s.loadRequestProfile(req)
	if err != nil {
		return nil, err
	}

	input, err := s.selectInput(prof, dir, req.Inputs)
	if err != nil {
		return nil, err
	}

	// The per-episode config for the source, whether it sits beside the source
	// as `<input>.json` or inline in the profile. It is attached here rather
	// than only inside assembleOne because it is not part of assembly: a caller
	// that supplies the three path fields itself still gets it, which is what
	// the legacy wizard did.
	if err := wizard.AttachEpisodeConfig(prof, input, dir); err != nil {
		return nil, err
	}

	// The root of the working tree is resolved before anything else runs, so a
	// request that asks for assembly without a directory to derive from fails
	// with that reason instead of a validation error about the script path.
	var projectFile string
	if req.WriteVpy {
		if projectFile, err = s.projectFileFor(config, dir); err != nil {
			return nil, err
		}
	}

	// Validation needs the outside world, which only the daemon has: the
	// installed VapourSynth version, the script text and the toolchain. It
	// runs before anything reaches the queue, so a bad profile is a 400.
	if err := profile.Validate(prof, s.validationInputs(ctx, prof, dir)); err != nil {
		return nil, err
	}

	// The wizard's assembly step, when the caller asked for it: it derives the
	// per-source script name and the two path prefixes and writes the generated
	// .vpy, which is what makes the three fields the pipeline requires exist.
	// An explicitly supplied field still wins (applyPaths below), because the
	// caller may have its own idea of where the work belongs.
	if req.WriteVpy {
		if err := s.assembleOne(prof, projectFile, input); err != nil {
			return nil, err
		}
	}
	applyPaths(prof, req)

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

// loadRequestProfile reads the profile a request carries, whichever of the
// three forms it used, and resolves the directory its relative paths are
// against.
//
// The forms are mutually exclusive and one of them is required, which is the
// E2 contract; both /tasks and /tasks/prepare accept all three, so a client can
// preview exactly what it is about to submit.
func (s *Server) loadRequestProfile(req *addTaskRequest) (*profile.Profile, string, string, error) {
	hasObject := profileObjectGiven(req.Profile)
	forms := 0
	for _, given := range []bool{req.ProfilePath != "", hasObject, req.ProfileText != ""} {
		if given {
			forms++
		}
	}
	if forms == 0 {
		return nil, "", "", okerr.New(okerr.KindConfig, "请求内容不合法",
			"必须提供 profile_path、profile 或 profile_text 之一。")
	}
	if forms > 1 {
		return nil, "", "", okerr.New(okerr.KindConfig, "请求内容不合法",
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
			return nil, "", "", okerr.Wrap(err, okerr.KindConfig, "配置文件路径不合法",
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
		return nil, "", "", err
	}

	dir := ""
	if config != "" {
		if dir, err = profile.DirOf(config); err != nil {
			return nil, "", "", okerr.Wrap(err, okerr.KindConfig, "配置文件路径不合法",
				"%q 没有上级目录。", config)
		}
	} else if req.BaseDir != "" {
		dir, err = filepath.Abs(req.BaseDir)
		if err != nil {
			return nil, "", "", okerr.Wrap(err, okerr.KindConfig, "目录路径不合法",
				"%q: %v", req.BaseDir, err)
		}
	}
	return prof, config, dir, nil
}

// projectFileFor resolves the project file a request's assembly pass runs
// against: the profile's own path when it has one, base_dir otherwise.
//
// An inline profile has no file, so base_dir stands in for it; without either
// there is no directory to derive from, which is reported rather than silently
// using the process working directory.
func (s *Server) projectFileFor(config, dir string) (string, error) {
	if config != "" {
		return config, nil
	}
	if dir == "" {
		return "", okerr.New(okerr.KindConfig, "找不到配置文件",
			"write_vpy 需要知道工作目录的根：请提供 profile_path 或 base_dir。")
	}
	return filepath.Join(dir, inlineProfileName), nil
}

// assembleOne runs the wizard's assembly step for a request that asked for it:
// the profile's inputs are derived into one task each and the derived fields of
// the selected source are copied onto the profile the request is building.
//
// The whole profile is derived even though one task is created, because the
// derivation is per pass: the generated script name carries a timestamp shared
// by the pass, and ReducePathMap.log records every shortening the pass
// performed — exactly what the legacy wizard wrote when it processed a profile.
func (s *Server) assembleOne(prof *profile.Profile, projectFile, input string) error {
	result, err := wizard.Assemble(prof, wizard.Options{
		ProjectFile: projectFile,
		ReducePath:  s.reducePath(),
	})
	if err != nil {
		return err
	}

	for i := range result.Tasks {
		if result.Tasks[i].InputFile != input {
			continue
		}
		prof.InputScript = result.Tasks[i].VpyFile
		prof.WorkingPathPrefix = result.Tasks[i].WorkingPathPrefix
		prof.OutputPathPrefix = result.Tasks[i].OutputPathPrefix
		log.Info("已生成vpy文件", "task", result.Tasks[i].Name, "vpy", result.Tasks[i].VpyFile)
		return nil
	}
	return okerr.New(okerr.KindNotFound, "找不到输入文件",
		"推导结果里没有输入文件 %s。", input)
}

// reducePath reads the installation-wide shortening switch. It mirrors
// Initializer.Config.reducePath, whose default is true, so a store that cannot
// be read leaves the feature on rather than silently changing every derived
// path.
func (s *Server) reducePath() bool {
	cfg, err := s.config.Load()
	if err != nil {
		return true
	}
	return cfg.ReducePath
}

// applyPaths copies the three paths onto the profile. The pipeline refuses to
// run without them (engine.validateForRun), so a request may supply them; an
// empty value leaves whatever the profile already had.
//
// It runs after assembleOne, so an explicit field overrides the derived one:
// write_vpy fills the three fields the wizard would have filled, and a caller
// that knows better still gets the last word.
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
		// The script is user-authored and may carry a mark; the legacy
		// AddTaskService read it with File.ReadAllText.
		if raw, err := textfile.Read(path); err == nil {
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
