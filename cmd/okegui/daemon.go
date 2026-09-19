package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/KiritakeKumi/OKEGuiDX/internal/api"
	"github.com/KiritakeKumi/OKEGuiDX/internal/api/ws"
	"github.com/KiritakeKumi/OKEGuiDX/internal/log"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
	"github.com/KiritakeKumi/OKEGuiDX/internal/platform"
)

// daemonOptions are the flags of `okegui daemon` and, through it, `okegui gui`.
type daemonOptions struct {
	options

	// addr is the listen address of the HTTP API.
	addr string
	// shutdownGrace bounds how long a graceful shutdown waits for running
	// tasks before giving up on them.
	shutdownGrace time.Duration
	// pidFile, when set, receives the daemon's process id.
	pidFile string

	// listener, when set, replaces addr. Tests use it to bind a free port and
	// learn it afterwards, which keeps two test runs from colliding.
	listener net.Listener
}

// daemonCommand starts the resident service.
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
// up the engine and the HTTP front end, then shuts both down when the context
// is cancelled.
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

	svc, err := newService(s)
	if err != nil {
		return err
	}
	return a.serve(o, svc)
}

// bindDaemon registers the flags daemon and gui share.
func (o *daemonOptions) bindDaemon(fs *flag.FlagSet) {
	fs.StringVar(&o.addr, "addr", defaultAddr,
		"HTTP 接口监听地址（必须是回环地址）")
	fs.DurationVar(&o.shutdownGrace, "shutdown-grace", 30*time.Second,
		"优雅关闭等待在跑任务的最长时间")
	fs.StringVar(&o.pidFile, "pid-file", "",
		"写入进程号的路径，留空则不写")
}

// defaultAddr is where the Web UI is served. It is a loopback address on
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

// service is the resident half of the daemon: the engine plus the HTTP front
// end, with the shutdown order that keeps them consistent.
type service struct {
	settings *settings
	parts    *engineParts
	api      *api.Server
	hub      *ws.Hub
	stream   *ws.Stream

	closeOnce sync.Once
	closeErr  error
}

// newService brings up the engine, the event hub and the REST server.
//
// The hub is installed as the worker pool's event sink before anything can
// start, so the first progress line of the first task is already broadcast: a
// sink installed later would lose the opening events.
func newService(s *settings) (*service, error) {
	parts, err := assemble(assembleOptions{Settings: s})
	if err != nil {
		return nil, err
	}

	hub := ws.NewHub(eventBuffer)
	parts.Workers.SetEventSink(hub.Publish)

	srv, err := api.New(api.Options{
		Tasks: parts.Tasks,
		Pool:  parts.Workers,
		Exec:  parts.Exec,
		OnConfigChange: func(cfg platform.Config) {
			// The settings panel can change the log level; the tool paths only
			// take effect on the next start, which is what the legacy program
			// did too.
			log.SetLevel(cfg.LogLevel)
		},
	})
	if err != nil {
		return nil, err
	}

	return &service{
		settings: s,
		parts:    parts,
		api:      srv,
		hub:      hub,
		stream:   ws.New(ws.Options{Hub: hub}),
	}, nil
}

// Start begins processing whatever the queue already holds. Tasks recovered
// from a previous run were reset to waiting by the queue loader, so they are
// picked up here.
func (svc *service) Start() {
	if !svc.parts.Workers.Start() {
		log.Warn("没有注册工作单元，任务不会自动开始")
	}
}

// serve runs the daemon until the context is cancelled, then shuts down.
func (a *application) serve(o *daemonOptions, svc *service) error {
	checkToolchain(svc.settings.caps)

	if o.listener == nil && !loopbackAddr(o.addr) {
		// The API has no authentication (CLUSTER.md §4), so a non-loopback
		// address is refused rather than warned about: a warning in a log file
		// nobody reads is not a security control.
		return fail(exitUsage,
			"--addr %s 不是回环地址：本版本接口没有认证，只能监听回环地址", o.addr)
	}

	ln := o.listener
	if ln == nil {
		var err error
		ln, err = net.Listen("tcp", o.addr)
		if err != nil {
			return wrapExit(exitFailure, okerr.Wrap(err, okerr.KindIO,
				"无法监听 HTTP 地址", "%s: %v", o.addr, err))
		}
	}
	srv, done := startServer(ln, daemonHandler(svc.api, svc.stream))
	bound := ln.Addr().String()

	log.Info("OKEGuiDX 服务已启动",
		"version", version,
		"node", svc.settings.caps.NodeID,
		"role", string(svc.settings.role),
		"addr", "http://"+bound+"/",
		"queue", svc.settings.queuePath)
	fmt.Fprintf(a.stdout, "HTTP 接口: http://%s/api/v1\n", bound)
	fmt.Fprintf(a.stdout, "界面地址: http://%s/\n", bound)

	svc.Start()

	select {
	case <-a.ctx.Done():
		log.Info("收到终止信号，开始优雅关闭")
	case err := <-done:
		// The server stopped on its own, which only happens on a failure: the
		// graceful path closes it from here.
		if err != nil {
			return wrapExit(exitFailure, err)
		}
		log.Warn("HTTP 服务意外停止，开始关闭")
	}

	// The shutdown deadline cannot come from a.ctx: the signal that got us
	// here has already cancelled it.
	ctx, cancel := context.WithTimeout(context.Background(), o.shutdownGrace)
	defer cancel()
	if err := svc.Shutdown(ctx, srv); err != nil {
		return err
	}
	log.Info("服务已停止")
	return nil
}

// Shutdown stops the daemon in the order that keeps clients informed:
//
//  1. the HTTP server, so no new request reaches the queue and no new
//     WebSocket client attaches while the engine is winding down;
//  2. the hub, so every open stream receives a going-away close instead of
//     being dropped when the process exits;
//  3. the worker pool, bounded by ctx, which cancels the running tasks and
//     waits for the workers to wind down;
//  4. the queue, written back once the pool is quiet so a restart sees the
//     terminal state of every task instead of a stale "running".
//
// It is safe to call more than once; only the first call does the work and its
// error is reported to every caller.
func (svc *service) Shutdown(ctx context.Context, srv *http.Server) error {
	svc.closeOnce.Do(func() {
		if err := shutdownServer(ctx, srv); err != nil {
			log.Warn("HTTP 服务未能及时停止", "err", err)
			svc.closeErr = err
		}

		// Closing the hub refuses new subscribers and ends every open
		// connection, so a client learns the daemon is going away instead of
		// losing the socket.
		if svc.hub != nil {
			svc.hub.Close()
		}

		log.Info("正在停止工作单元")
		if err := svc.parts.Workers.StopContext(ctx); err != nil {
			log.Warn("有任务未在期限内结束", "err", err)
			if svc.closeErr == nil {
				svc.closeErr = err
			}
			return
		}
		if err := svc.parts.Tasks.Save(); err != nil && svc.closeErr == nil {
			svc.closeErr = err
		}
	})
	return svc.closeErr
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

启动常驻服务：加载配置、发现工具链、打开任务队列与工作单元池，
在 HTTP 上提供 REST 接口（/api/v1）与 WebSocket 进度流（/api/v1/events），
并在 "/" 上提供内嵌的 Web 界面。收到 SIGINT/SIGTERM 时优雅关闭。

选项:
  --addr HOST:PORT       HTTP 接口监听地址（默认 127.0.0.1:8090，必须是回环地址）
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
