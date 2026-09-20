package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/KiritakeKumi/OKEGuiDX/internal/engine"
	"github.com/KiritakeKumi/OKEGuiDX/internal/log"
	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/node"
	"github.com/KiritakeKumi/OKEGuiDX/internal/profile"
	"github.com/KiritakeKumi/OKEGuiDX/internal/textfile"
	"github.com/KiritakeKumi/OKEGuiDX/internal/toolchain"
	"github.com/KiritakeKumi/OKEGuiDX/internal/wizard"
)

// runOptions are the flags of `okegui run`.
type runOptions struct {
	options

	// workers is how many tasks run at once. Zero means one per NUMA node,
	// which is what the legacy MainWindow did: it created NumaNode.NumaCount
	// workers at startup.
	workers int
	// dryRun validates the profiles and stops before any tool is started.
	dryRun bool
}

// loadedTask is one queue entry: the runtime task plus the profile it came
// from. The queue stores the profile path so a task can be traced back to its
// configuration after a restart.
type loadedTask struct {
	task       *model.Task
	configPath string
}

// runCommand loads every profile, queues the tasks and runs them. The exit
// code separates the two failure modes: a bad profile is exitUsage (the
// operator has to fix a file), a failed encode is exitTaskFailed.
func (a *application) runCommand(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	var o runOptions
	o.bind(fs)
	fs.IntVar(&o.workers, "workers", 0, "并发任务数，默认为 NUMA 节点数")
	fs.BoolVar(&o.dryRun, "dry-run", false, "只校验配置，不执行任务")
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelp) {
			printRunUsage(a.stdout)
		}
		return err
	}

	paths := fs.Args()
	if len(paths) == 0 {
		return fail(exitUsage, "缺少参数：至少要指定一个 profile.json")
	}
	if o.workers < 0 {
		return fail(exitUsage, "--workers 不能为负数：%d", o.workers)
	}

	s, err := a.bootstrap(&o.options)
	if err != nil {
		return err
	}
	defer a.closeLog(s)

	tasks, err := loadTasks(paths, s.caps)
	if err != nil {
		return err
	}

	if o.dryRun {
		// The point of a dry run is to check the profiles without starting a
		// tool; it reports exactly the errors a real run would.
		fmt.Fprintf(a.stdout, "配置校验通过：%d 个任务\n", len(tasks))
		for _, lt := range tasks {
			fmt.Fprintf(a.stdout, "  %s  %s\n", lt.task.Name, lt.task.Status.Input)
		}
		return nil
	}

	// The wizard's finishing step, which a headless run has to do for itself:
	// every source gets its own generated .vpy and the two path prefixes the
	// pipeline requires. It runs after the dry-run branch so a validation-only
	// run still touches nothing.
	tasks, err = assembleTasks(tasks, s.appCfg.ReducePath)
	if err != nil {
		return err
	}

	return a.execute(s, &o, tasks)
}

// drainPoll is how often the run loop asks the pool whether it is still busy.
const drainPoll = 100 * time.Millisecond

// execute queues the tasks and runs the worker pool until the queue drains or
// the context is cancelled.
func (a *application) execute(s *settings, o *runOptions, tasks []loadedTask) error {
	parts, err := assemble(assembleOptions{Settings: s, WorkerCount: o.workers})
	if err != nil {
		return err
	}

	// The sink is installed before the first task can start, otherwise the
	// opening progress lines would be lost.
	report := newProgressReporter(a.stderr)
	parts.Workers.SetEventSink(report.report)

	for _, lt := range tasks {
		if _, err := parts.Workers.AddTask(lt.task, lt.configPath); err != nil {
			return err
		}
	}

	count := o.workerCount(s.caps)
	if !parts.Workers.Start() {
		return fail(exitFailure, "没有可用的工作单元，任务未执行")
	}
	log.Info("开始处理任务", "tasks", len(tasks), "workers", count)

	// A pool that started with nothing runnable never launches a worker, so
	// nothing would ever signal completion. The queue cannot be empty here
	// (AddTask enables every task), but waiting forever is the worst possible
	// failure mode for a command line, so it is checked rather than assumed.
	if parts.Tasks.GetActiveTaskCount() == 0 && parts.Workers.GetBGWorkerCount() == 0 {
		report.finish()
		return tally(parts.Tasks, len(tasks))
	}

	// The pool exposes no completion channel, and SetAfterFinish only fires
	// when every task succeeded, so the end of the run is detected through
	// IsRunning: it turns false exactly when the last worker leaves an empty
	// queue. The poll costs nothing next to an encode.
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(drainPoll)
		defer ticker.Stop()
		for range ticker.C {
			if !parts.Workers.IsRunning() {
				return
			}
		}
	}()

	select {
	case <-done:
		report.finish()
	case <-a.ctx.Done():
		// Stop cancels the running tasks and waits for the workers to wind
		// down, so no child process is left behind on the way out.
		log.Info("收到终止信号，正在停止任务")
		parts.Workers.Stop()
		report.finish()
		return a.ctx.Err()
	}
	return tally(parts.Tasks, len(tasks))
}

// workerCount resolves how many workers to register.
func (o *runOptions) workerCount(caps node.Capabilities) int {
	return workerCount(o.workers, caps.NUMANodes)
}

// tally turns the final queue state into an exit code. A run where every task
// finished is success; anything else is a task failure.
func tally(tm *engine.TaskManager, submitted int) error {
	var failed, unfinished []string
	for _, t := range tm.Snapshot() {
		switch t.Status.Progress {
		case model.TaskFinished:
		case model.TaskError:
			failed = append(failed, fmt.Sprintf("%s（%s）", displayName(t), t.Status.Status))
		default:
			unfinished = append(unfinished, displayName(t))
		}
	}

	if len(failed) == 0 && len(unfinished) == 0 {
		log.Info("全部任务已完成", "tasks", submitted)
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d/%d 个任务未成功", len(failed)+len(unfinished), submitted)
	if len(failed) > 0 {
		fmt.Fprintf(&b, "；失败：%s", strings.Join(failed, ", "))
	}
	if len(unfinished) > 0 {
		fmt.Fprintf(&b, "；未完成：%s", strings.Join(unfinished, ", "))
	}
	return fail(exitTaskFailed, "%s", b.String())
}

// displayName prefers the profile's project name, falling back to the input.
func displayName(t model.Task) string {
	if t.Name != "" {
		return t.Name
	}
	return t.Status.Input.Base()
}

// printRunUsage writes the help text of `okegui run`.
func printRunUsage(w io.Writer) {
	fmt.Fprint(w, `用法: okegui run [选项] <profile.json>...

读取一个或多个任务配置，校验后加入任务队列并执行。
进度输出到 stderr，详细程度由 --log-level 决定。

选项:
  --workers N     并发任务数，默认为 NUMA 节点数
  --dry-run       只校验配置，不执行任务
  --queue PATH    任务队列文件，默认为配置目录下的 queue.json
  --tools DIR     tools 目录所在位置，默认为可执行文件所在目录
  --config PATH   OKEGuiConfig.json 的路径
  --role NAME     节点角色，只接受 standalone
  --log-level LV  日志级别: TRACE / DEBUG / INFO / WARN / ERROR
  --log-dir DIR   日志文件目录，留空则只写 stderr

退出码:
  0  全部任务成功
  1  内部错误
  2  有任务失败
  3  参数或配置错误
`)
}

// printRunUsage writes the help text of `okegui run`.

// loadTasks reads, validates and converts every profile. All profiles are
// checked before anything runs, so a typo in the last file does not leave the
// first ones half processed.
func loadTasks(paths []string, caps node.Capabilities) ([]loadedTask, error) {
	var tasks []loadedTask
	seen := make(map[string]struct{}, len(paths))

	for _, path := range paths {
		p, err := loadOne(path, caps)
		if err != nil {
			return nil, err
		}
		if _, dup := seen[p.ConfigFilePath]; dup {
			log.Warn("同一个 profile 被指定了多次，只保留第一个", "path", p.ConfigFilePath)
			continue
		}
		seen[p.ConfigFilePath] = struct{}{}

		cfg, err := episodeConfigFor(p)
		if err != nil {
			return nil, err
		}
		base := profile.ToModel(p, cfg)

		// The queue holds one row per source file, which is what the legacy
		// wizard produced: one TaskDetail per InputFile, each with its own
		// generated .vpy and its own output name.
		//
		// Each row needs its own id: profile.ToModel assigned one to the base,
		// and the queue rejects a second task with an id it already holds, so a
		// profile with several sources would otherwise queue only its first.
		//
		// A profile keeps its InputFiles as written, which is usually relative
		// to the profile (LoadInputFiles did the same). They are resolved here,
		// because everything downstream — the working tree, the timecode, the
		// demuxer — needs an absolute path.
		dir := filepath.Dir(p.ConfigFilePath)
		for _, raw := range p.InputFiles {
			input := resolveFrom(dir, raw)
			task := *base
			task.ID = model.NewTaskID()
			task.Inputs = []model.FileRef{model.NewFileRef(input)}
			task.Status.Input = task.Inputs[0]
			task.Status.Output = outputRef(p, input)
			if cfg != nil && cfg.EnableReEncode {
				task.IsReEncode = true
			}
			tasks = append(tasks, loadedTask{task: &task, configPath: p.ConfigFilePath})
		}
	}
	if len(tasks) == 0 {
		return nil, fail(exitUsage, "没有可执行的任务：profile 里没有指定输入文件")
	}
	return tasks, nil
}

// assembleTasks runs the wizard's finishing step for every profile a run was
// given: the per-source .vpy is generated and written, and the two path
// prefixes are filled in on the profile the task carries.
//
// Without it a headless run consumes only profiles that were assembled by hand
// (DECISIONS-NEEDED.md B4): the pipeline refuses a profile without InputScript,
// WorkingPathPrefix and OutputPathPrefix, and the wizard that used to fill them
// is a GUI step. It is the same call the API's write_vpy makes, so the two
// front ends assemble a task identically.
//
// Each task takes the wizard's own per-source profile — a private copy with the
// three fields filled in — so two tasks of one profile do not share a value.
// loadTasks built the tasks in profile order and Derive derives in profile
// order, so the two lists line up position by position.
func assembleTasks(tasks []loadedTask, reducePath bool) ([]loadedTask, error) {
	// derived is keyed by config path, holding one profile per source in
	// profile order.
	derived := make(map[string][]*profile.Profile, len(tasks))
	for _, lt := range tasks {
		path := lt.configPath
		if path == "" {
			return nil, fail(exitFailure, "任务 %s 没有关联的 profile 路径，无法装配", lt.task.ID)
		}
		if _, done := derived[path]; done {
			continue
		}
		prof, ok := lt.task.Profile.(*profile.Profile)
		if !ok || prof == nil {
			return nil, fail(exitFailure, "任务 %s 没有可用的配置", lt.task.ID)
		}

		// Assemble derives and writes: the generated scripts land next to the
		// working prefixes and ReducePathMap.log is appended to, which is what
		// the legacy wizard did before it queued the tasks.
		result, err := wizard.Assemble(prof, wizard.Options{
			ProjectFile: path,
			ReducePath:  reducePath,
		})
		if err != nil {
			return nil, wrapExit(exitUsage, err)
		}
		if len(result.Tasks) == 0 {
			return nil, fail(exitUsage, "profile %s 里没有指定输入文件", path)
		}
		perSource := make([]*profile.Profile, 0, len(result.Tasks))
		for i := range result.Tasks {
			perSource = append(perSource, result.Tasks[i].Profile)
		}
		derived[path] = perSource
	}

	next := make(map[string]int, len(derived))
	for i := range tasks {
		path := tasks[i].configPath
		n := next[path]
		perSource := derived[path]
		if n >= len(perSource) {
			return nil, fail(exitFailure, "profile %s 的装配结果比任务数少", path)
		}
		tasks[i].task.Profile = perSource[n]
		next[path] = n + 1
	}
	return tasks, nil
}

// loadOne reads and validates a single profile. Validation needs facts only
// this layer can gather: the installed VapourSynth version, the .vpy script and
// the toolchain's encoder choice.
func loadOne(path string, caps node.Capabilities) (*profile.Profile, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, wrapExit(exitUsage, err)
	}
	p, err := profile.Load(abs)
	if err != nil {
		return nil, wrapExit(exitUsage, err)
	}

	dir := filepath.Dir(abs)
	inputs := profile.Inputs{
		InstalledVSVersion: installedVSVersion(caps),
		InputExists:        func(rel string) bool { return fileExists(resolveFrom(dir, rel)) },
		ResolveEncoder:     func(rel string) (string, bool) { return resolveExisting(dir, rel) },
		ToolchainEncoder:   encoderFor(caps),
	}
	if raw, readErr := textfile.Read(resolveFrom(dir, p.InputScript)); readErr == nil {
		inputs.VpyText = string(raw)
		inputs.VpyRead = true
	}

	if err := profile.Validate(p, inputs); err != nil {
		return nil, wrapExit(exitUsage, err)
	}
	return p, nil
}

// episodeConfigFor validates the per-episode configuration a re-encode task
// needs. It is optional: a normal task has none.
func episodeConfigFor(p *profile.Profile) (*profile.EpisodeConfig, error) {
	if p.Config == nil {
		return nil, nil
	}
	cfg := *p.Config
	if err := profile.ValidateEpisodeConfig(&cfg); err != nil {
		return nil, wrapExit(exitUsage, err)
	}
	return &cfg, nil
}

// installedVSVersion reads the VERSION file next to vspipe, as
// AddTaskService.ProcessJsonProfile did. A missing file means "cannot verify",
// which is why an unknown version is not an error.
func installedVSVersion(caps node.Capabilities) string {
	info, ok := caps.Tool(toolchain.ToolVSPipe)
	if !ok || info.Path == "" {
		return ""
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(info.Path), "VERSION"))
	if err != nil {
		log.Debug("无法读取 VapourSynth 版本文件", "err", err)
		return ""
	}
	return strings.TrimRight(string(raw), "\r\n\t ")
}

// encoderFor returns the platform's default encoder for a profile that does
// not name one.
func encoderFor(caps node.Capabilities) func(profile.EncoderType) (string, bool) {
	return func(et profile.EncoderType) (string, bool) {
		name := string(et)
		switch et {
		case profile.EncoderX264:
			name = toolchain.ToolX264
		case profile.EncoderX265:
			name = toolchain.ToolX265
		case profile.EncoderSVTAV1:
			name = toolchain.ToolSVTAV1
		}
		info, ok := caps.Tool(name)
		if !ok || info.Path == "" {
			return "", false
		}
		return info.Path, true
	}
}

// resolveFrom resolves a profile-relative path. An absolute path is returned
// unchanged, mirroring PathUtils.GetFullPath.
func resolveFrom(dir, rel string) string {
	if rel == "" {
		return ""
	}
	if filepath.IsAbs(rel) {
		return rel
	}
	return filepath.Join(dir, rel)
}

// resolveExisting is resolveFrom plus an existence check.
func resolveExisting(dir, rel string) (string, bool) {
	p := resolveFrom(dir, rel)
	return p, fileExists(p)
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// outputRef derives the output file name the legacy UpdateOutputFileName built:
// the input's full name plus the container extension, in the input's own
// directory. "00000.m2ts" therefore becomes "00000.m2ts.mkv".
func outputRef(p *profile.Profile, input string) model.FileRef {
	ref := model.NewFileRef(input)
	name := ref.Base() + "." + strings.ToLower(p.ContainerFormat)
	return ref.Dir().Join(name)
}

// progressReporter renders model.StatusEvent as one human-readable line. While
// a task reports progress it rewrites the current line; when a task finishes it
// starts a new one. A long encode therefore produces a handful of lines instead
// of thousands.
//
// The line rewriting uses ANSI escapes, which only a terminal understands, so
// the reporter checks the destination first. A redirected log gets plain lines.
type progressReporter struct {
	w    io.Writer
	tty  bool
	mu   sync.Mutex
	last map[model.TaskID]string
}

// newProgressReporter returns a reporter writing to w.
func newProgressReporter(w io.Writer) *progressReporter {
	return &progressReporter{w: w, tty: isTerminal(w), last: map[model.TaskID]string{}}
}

// isTerminal reports whether w is a character device, which is how both
// Windows consoles and Unix terminals present themselves. It needs no
// dependency and never returns an error: an unknown destination is treated as
// a file, which is the safe choice.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}

// report renders one event.
func (r *progressReporter) report(ev model.StatusEvent) {
	line := formatEvent(ev)
	if line == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.last[ev.TaskID] == line {
		return
	}
	r.last[ev.TaskID] = line

	if ev.Progress == model.TaskFinished || ev.Progress == model.TaskError {
		// A terminal line must survive the next task's progress output.
		fmt.Fprintf(r.w, "%s%s\n", r.clear(), line)
		delete(r.last, ev.TaskID)
		return
	}
	fmt.Fprintf(r.w, "%s%s", r.clear(), line)
}

// clear returns the escape sequence that erases the current line, or nothing
// when the destination is not a terminal. Callers must hold r.mu.
func (r *progressReporter) clear() string {
	if !r.tty {
		return ""
	}
	return "\r\033[K"
}

// finish clears the progress line so the shell prompt does not land on it.
func (r *progressReporter) finish() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.last) == 0 {
		return
	}
	fmt.Fprint(r.w, r.clear())
	r.last = map[model.TaskID]string{}
}

// formatEvent renders one status event. It returns "" for events that carry
// nothing worth showing.
func formatEvent(ev model.StatusEvent) string {
	id := shortID(ev.TaskID)
	switch ev.Progress {
	case model.TaskFinished:
		return fmt.Sprintf("[%s] 完成", id)
	case model.TaskError:
		summary := "任务失败"
		if ev.Error != nil && ev.Error.Summary != "" {
			summary = ev.Error.Summary
		}
		if ev.Error != nil && ev.Error.Detail != "" {
			return fmt.Sprintf("[%s] 失败：%s（%s）", id, summary, ev.Error.Detail)
		}
		return fmt.Sprintf("[%s] 失败：%s", id, summary)
	}

	parts := make([]string, 0, 5)
	if ev.Step != "" {
		parts = append(parts, ev.Step)
	}
	if percent, unknown := engine.FormatPercent(ev.Percent); !unknown {
		parts = append(parts, percent)
	}
	if ev.FramesTotal > 0 {
		parts = append(parts, fmt.Sprintf("%d/%d 帧", ev.FramesDone, ev.FramesTotal))
	}
	if ev.Speed != "" {
		parts = append(parts, ev.Speed)
	}
	if ev.TimeRemainSeconds > 0 {
		remain := time.Duration(ev.TimeRemainSeconds * float64(time.Second))
		parts = append(parts, "剩余 "+engine.FormatTimeRemain(remain))
	}
	if len(parts) == 0 {
		return ""
	}
	// A running event without a step name has no context: "[id] 0.00%" tells
	// the operator nothing about what is running, so it is not printed.
	if ev.Step == "" {
		return ""
	}
	return fmt.Sprintf("[%s] %s", id, strings.Join(parts, "  "))
}

// shortID trims a UUID to its first block, which is enough to tell concurrent
// tasks apart in a progress line.
func shortID(id model.TaskID) string {
	s := id.String()
	if i := strings.IndexByte(s, '-'); i > 0 {
		return s[:i]
	}
	if s == "" {
		return "task"
	}
	return s
}
