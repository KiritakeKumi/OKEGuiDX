package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/KiritakeKumi/OKEGuiDX/internal/engine"
	"github.com/KiritakeKumi/OKEGuiDX/internal/log"
	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
	"github.com/KiritakeKumi/OKEGuiDX/internal/platform"
)

// daemonOptions are the flags of `okegui daemon` and, through it, `okegui gui`.
type daemonOptions struct {
	options

	// addr is the listen address of the HTTP API. E2/E3 own the server; the
	// flag exists now so the command line stays stable across the hand-off.
	addr string
	// shutdownGrace bounds how long a graceful shutdown waits for running
	// tasks before giving up on them.
	shutdownGrace time.Duration
	// pidFile, when set, receives the daemon's process id.
	pidFile string
}

// daemonCommand starts the resident service.
//
// This is the process-lifecycle skeleton only: it loads the configuration,
// discovers the toolchain, brings up the task queue and the worker pool, and
// shuts all of it down on SIGINT/SIGTERM. The HTTP and WebSocket front ends are
// E2/E3 and are not wired in yet, so the command reports what it is waiting for
// instead of pretending to listen.
func (a *application) daemonCommand(args []string) error {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	var o daemonOptions
	o.bind(fs)
	o.bindDaemon(fs)
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelp) {
			printDaemonUsage(a.stdout)
		}
		return err
	}
	if fs.NArg() > 0 {
		return fail(exitUsage, "daemon 不接受位置参数：%v", fs.Args())
	}

	// A second signal is deliberately not intercepted: during a slow shutdown
	// the operator's Ctrl-C should still kill the process.
	return a.runService(&o)
}

// runService is the daemon's process lifecycle, shared with `gui`: it brings
// up the engine, reports what the HTTP layer still owes, and shuts everything
// down when the context is cancelled.
func (a *application) runService(o *daemonOptions) error {
	s, err := a.bootstrap(&o.options)
	if err != nil {
		return err
	}
	defer a.closeLog(s)

	// A resident process always writes a log file: nobody is watching its
	// stderr.
	if s.logFile == nil {
		a.enableFileLogging(s, &o.options)
	}

	if o.pidFile != "" {
		if err := writePIDFile(o.pidFile); err != nil {
			return err
		}
		defer removePIDFile(o.pidFile)
	}

	return a.serve(o, newService(s))
}

// bindDaemon registers the flags daemon and gui share.
func (o *daemonOptions) bindDaemon(fs *flag.FlagSet) {
	fs.StringVar(&o.addr, "addr", defaultAddr,
		"HTTP 接口监听地址（E2/E3 落地后生效）")
	fs.DurationVar(&o.shutdownGrace, "shutdown-grace", 30*time.Second,
		"优雅关闭等待在跑任务的最长时间")
	fs.StringVar(&o.pidFile, "pid-file", "",
		"写入进程号的路径，留空则不写")
}

// defaultAddr is where the Web UI will be served. It is a loopback address on
// purpose: the API is unauthenticated in this rewrite (CLUSTER.md §4), so it
// must not be reachable from the network until an auth middleware exists.
const defaultAddr = "127.0.0.1:8090"

// enableFileLogging points the logger at a file below the configuration
// directory, which is what makes a resident process diagnosable after the fact.
func (a *application) enableFileLogging(s *settings, o *options) {
	dir := o.logDir
	if dir == "" {
		// A resident process is the one case that writes a log file by
		// default: nobody is watching its stderr.
		configDir, err := a.config.Dir()
		if err != nil {
			log.Warn("无法确定日志目录", "err", err)
			return
		}
		dir = filepath.Join(configDir, logDirName)
	}
	path, f, err := platform.SetupLogging(dir, a.config.LogLevel())
	if err != nil {
		log.Warn("无法创建日志文件", "dir", dir, "err", err)
		return
	}
	s.logPath, s.logFile = path, f
	log.Info("日志文件", "path", path)
}

// service is the resident half of the daemon: the pieces that must exist for
// the process to be worth running, and that must be shut down in order.
//
// It is deliberately the object the HTTP layer will be handed once E2/E3 land:
// the queue, the worker pool and the executor. The daemon command owns the
// process lifecycle, not the API.
type service struct {
	settings *settings
	tasks    *engine.TaskManager
	exec     *engine.LocalExecutor
	workers  *engine.WorkerManager

	closeOnce sync.Once
	closeErr  error
}

// newService brings up the engine for a resident run. A damaged queue file is
// reported through the log rather than failing: an operator needs the service
// up in order to look at the problem.
func newService(s *settings) *service {
	tm, err := engine.New(engine.Options{QueuePath: s.queuePath})
	if err != nil {
		log.Warn("任务队列无法读取，将从空队列开始", "path", s.queuePath, "err", err)
	}

	exec := engine.NewLocalExecutor(s.caps, pipelineRunFunc())
	workers := engine.NewWorkerManager(exec, tm, platform.NewNumaWithCount(s.caps.NUMANodes))

	// The daemon keeps one worker per NUMA node, the same default the legacy
	// MainWindow used. The API will be able to change this later.
	count := max(s.caps.NUMANodes, 1)
	for i := range count {
		workers.AddWorker(i + 1)
	}

	return &service{settings: s, tasks: tm, exec: exec, workers: workers}
}

// Start begins processing whatever the queue already holds. Tasks recovered
// from a previous run were reset to waiting by the queue loader, so they are
// picked up here.
func (svc *service) Start() {
	if !svc.workers.Start() {
		log.Warn("没有注册工作单元，任务不会自动开始")
	}
}

// Shutdown stops the worker pool and waits for the running tasks, bounded by
// ctx. It is safe to call more than once; only the first call does the work and
// its error is reported to every caller.
func (svc *service) Shutdown(ctx context.Context) error {
	svc.closeOnce.Do(func() {
		log.Info("正在停止工作单元")
		if err := svc.workers.StopContext(ctx); err != nil {
			log.Warn("有任务未在期限内结束", "err", err)
			svc.closeErr = err
			return
		}
		// The queue is written back once the pool is quiet, so a restart sees
		// the terminal state of every task instead of a stale "running".
		svc.closeErr = svc.tasks.Save()
	})
	return svc.closeErr
}

// serve runs the daemon until the context is cancelled, then shuts down.
func (a *application) serve(o *daemonOptions, svc *service) error {
	log.Info("OKEGuiDX 服务已启动",
		"version", version,
		"node", svc.settings.caps.NodeID,
		"role", string(svc.settings.role),
		"queue", svc.settings.queuePath)
	reportAPIPending(o, a.stdout)

	svc.Start()

	<-a.ctx.Done()
	log.Info("收到终止信号，开始优雅关闭")

	// The shutdown deadline cannot come from a.ctx: the signal that got us
	// here has already cancelled it.
	ctx, cancel := context.WithTimeout(context.Background(), o.shutdownGrace)
	defer cancel()
	if err := svc.Shutdown(ctx); err != nil {
		return err
	}
	log.Info("服务已停止")
	return nil
}

// reportAPIPending states where the HTTP front end will live. Until E2
// provides a server constructor there is nothing to listen on, and silently
// pretending otherwise would be worse than saying so: the daemon would look
// healthy while no client could reach it.
func reportAPIPending(o *daemonOptions, w io.Writer) {
	fmt.Fprintf(w, "HTTP 接口尚未接入：等待 E2 提供 internal/api 的服务端构造。\n")
	fmt.Fprintf(w, "计划地址: http://%s/api/v1\n", o.addr)
}

// pipelineRunFunc is the work function of the resident executor.
//
// It is the same refusal as the CLI's task runner: D3 owns the pipeline, so
// until it lands a recovered task fails with a clear message instead of
// hanging.
func pipelineRunFunc() engine.RunFunc {
	return func(_ context.Context, t *model.Task, events chan<- model.StatusEvent) error {
		log.Error("任务流水线尚未实现（D3）", "task", t.ID, "name", t.Name)
		events <- model.StatusEvent{TaskID: t.ID, Progress: model.TaskRunning, Step: "pipeline", Percent: -1}
		return okerr.New(okerr.KindUnknown, "任务流水线尚未实现",
			"D3 engine/pipeline 尚未落地，当前版本无法真正执行任务（task %s）。", t.ID)
	}
}

// writePIDFile records the process id so an init script can find the daemon.
func writePIDFile(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return wrapExit(exitUsage, err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return wrapExit(exitFailure, err)
	}
	if err := os.WriteFile(abs, fmt.Appendf(nil, "%d\n", os.Getpid()), 0o600); err != nil {
		return wrapExit(exitFailure, err)
	}
	return nil
}

// removePIDFile deletes the pid file on the way out.
func removePIDFile(path string) {
	if abs, err := filepath.Abs(path); err == nil {
		_ = os.Remove(abs)
	}
}

// printDaemonUsage writes the help text of `okegui daemon`.
func printDaemonUsage(w io.Writer) {
	fmt.Fprint(w, `用法: okegui daemon [选项]

启动常驻服务。当前版本负责进程生命周期：加载配置、发现工具链、
打开任务队列与工作单元池，并在收到 SIGINT/SIGTERM 时优雅关闭。

HTTP/WebSocket 接口由 E2/E3 提供，尚未接入。

选项:
  --addr HOST:PORT       HTTP 接口监听地址（默认 127.0.0.1:8090）
  --shutdown-grace DUR   优雅关闭等待在跑任务的最长时间（默认 30s）
  --pid-file PATH        写入进程号的路径
  --queue PATH           任务队列文件，默认为配置目录下的 queue.json
  --tools DIR            tools 目录所在位置，默认为可执行文件所在目录
  --config PATH          OKEGuiConfig.json 的路径
  --role NAME            节点角色，只接受 standalone
  --log-level LV         日志级别: TRACE / DEBUG / INFO / WARN / ERROR
  --log-dir DIR          日志文件目录，留空则写入配置目录下的 log/

退出码:
  0   正常关闭
  1   内部错误
  3   参数或配置错误
  130 被中断
`)
}
