package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"github.com/KiritakeKumi/OKEGuiDX/internal/api"
	"github.com/KiritakeKumi/OKEGuiDX/internal/api/ws"
	"github.com/KiritakeKumi/OKEGuiDX/internal/engine"
	"github.com/KiritakeKumi/OKEGuiDX/internal/log"
	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/node"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
	"github.com/KiritakeKumi/OKEGuiDX/internal/platform"
	"github.com/KiritakeKumi/OKEGuiDX/internal/proc"
	"github.com/KiritakeKumi/OKEGuiDX/internal/profile"
	"github.com/KiritakeKumi/OKEGuiDX/internal/toolchain"
	"github.com/KiritakeKumi/OKEGuiDX/internal/wizard"
	"github.com/KiritakeKumi/OKEGuiDX/web"
)

// This file is the one place that builds the engine's object graph. `run` and
// `daemon` both go through assemble, so the two front ends cannot drift apart:
// whatever the daemon serves over HTTP is what `okegui run` executes, with the
// same queue, the same executor and the same pipeline.
//
// Dependency order:
//
//	settings ──┬─→ TaskManager ─────────────┐
//	           │                            ├─→ WorkerManager ─→ api.Server
//	           └─→ LocalExecutor ←── Pipeline┘        │
//	                    ↑                             └─→ ws.Hub (event sink)
//	                    └── PipelineOptions{LoadProfile: tm.ConfigPath,
//	                                        UpdateTask:    tm.Update}
//
// The pipeline is built before the executor because the executor's RunFunc is
// the pipeline's Run. The two queue hooks close over the TaskManager, which is
// what lets a task recovered from queue.json find its profile again and what
// lets the pipeline publish the state a model.StatusEvent cannot carry.

// engineParts is the assembled engine: the queue, the executor, the pipeline
// and the worker pool. Every field is a pointer to a shared component, so the
// struct must not be copied.
type engineParts struct {
	// Tasks is the persisted task queue.
	Tasks *engine.TaskManager
	// Exec is the executor the pool drives. It is also what the REST API asks
	// for this node's capabilities.
	Exec *engine.LocalExecutor
	// Pipeline is the work the executor performs for each task.
	Pipeline *engine.Pipeline
	// Workers is the pool that decides what runs when.
	Workers *engine.WorkerManager
}

// assembleOptions are the inputs assemble needs from the resolved settings and
// the command line.
type assembleOptions struct {
	// Settings is the resolved configuration of this invocation. Caps and
	// QueuePath are the only fields assemble reads.
	Settings *settings
	// WorkerCount is how many permanent workers to register. Zero means one
	// per NUMA node, which is what the legacy MainWindow did.
	WorkerCount int
}

// assemble builds the engine for one invocation.
func assemble(opts assembleOptions) (*engineParts, error) {
	s := opts.Settings
	if s == nil {
		return nil, okerr.New(okerr.KindUnknown, "无法启动引擎", "缺少已解析的设置。")
	}

	tm, err := engine.New(engine.Options{QueuePath: s.queuePath})
	if err != nil {
		// A damaged queue file is reported, but the engine still comes up on an
		// empty queue: refusing to work because a recovery file is stale would
		// be worse than losing it.
		log.Warn("任务队列无法读取，将从空队列开始", "path", s.queuePath, "err", err)
	}

	pipeline := engine.NewPipeline(pipelineOptions(s, tm))
	exec := engine.NewLocalExecutor(s.caps, isolatedRunFunc(pipeline))
	workers := engine.NewWorkerManager(exec, tm, platform.NewNumaWithCount(s.caps.NUMANodes))

	for i := range workerCount(opts.WorkerCount, s.caps.NUMANodes) {
		workers.AddWorker(i + 1)
	}

	return &engineParts{Tasks: tm, Exec: exec, Pipeline: pipeline, Workers: workers}, nil
}

// pipelineOptions wires the pipeline to the queue. The two hooks are the reason
// the pipeline is built after the TaskManager and before the executor:
//
//   - LoadProfile re-reads the profile a queued task came from. A task that
//     already carries typed values skips it, which is the in-process case; a
//     task recovered from queue.json only has the path, and the queue is the
//     only thing that knows it.
//   - UpdateTask writes back the state a model.StatusEvent cannot carry (the
//     chapter status and the RPC result). The pool hands the pipeline a copy of
//     the queued task, so a mutation of the task alone would never reach it.
func pipelineOptions(s *settings, tm *engine.TaskManager) engine.PipelineOptions {
	return engine.PipelineOptions{
		Caps: s.caps,
		LoadProfile: func(t *model.Task) (*profile.Profile, *profile.EpisodeConfig, error) {
			path, ok := tm.ConfigPath(t.ID)
			if !ok {
				return nil, nil, okerr.New(okerr.KindNotFound, "找不到任务",
					"任务 %s 不在队列中，无法读取它的配置。", t.ID)
			}
			p, cfg, err := engine.LoadProfileFromDisk(path)
			if err != nil {
				return nil, nil, err
			}
			// A task recovered from queue.json still carries its resolved
			// source, which is all the per-source `<input>.json` lookup needs:
			// the episode config was never part of the profile file, so it has
			// to be re-attached from the task rather than from the path. A task
			// with no sibling config comes back unchanged.
			if p != nil && len(t.Inputs) > 0 {
				input := t.Inputs[0].Resolve(s.caps.Volumes)
				if err := wizard.AttachEpisodeConfig(p, input, filepath.Dir(path)); err != nil {
					return nil, nil, err
				}
				cfg = p.Config
			}
			return p, cfg, nil
		},
		UpdateTask: tm.Update,
		Numa:       platform.NewNumaWithCount(s.caps.NUMANodes),
		Priority:   proc.DefaultPriority,
		// The legacy AVX-512 switch was applied to x265 only
		// (X265Encoder.cs:131). The frozen pipeline hands one Asm value to
		// every video encoder, so turning it on also gives x264 an --asm
		// argument the legacy code never passed. Recorded in the hand-off
		// notes rather than worked around here.
		Asm: asmFor(s.appCfg.AVX512),
		// A headless front end has no wizard, and the wizard is what ran the
		// chapter detection in the legacy code (ChapterService.
		// UpdateChapterStatus). Leaving it off would silently drop the
		// chapters of a Matroska source.
		DetectChapters: true,
	}
}

// isolatedRunFunc adapts the pipeline to the executor with one addition: it
// gives the run a private copy of the task's profile and episode config.
//
// The queue stores those two values as opaque `any` and shares them with every
// snapshot it hands out (TaskManager.cloneTask), while the pipeline mutates the
// profile it was given — it reconciles IsReEncode and rewrites the re-encode
// slice array. Without this copy, a REST client reading the queue (GET
// /api/v1/tasks) would race with the worker running the task, which is a real
// data race, not just a stale read: `go test -race` reports it on the first task
// that starts while the Web UI is polling.
//
// The copy is made per run rather than once at submission because the pipeline
// owns the value for the whole task and the queue must stay free to marshal its
// own copy at any time.
func isolatedRunFunc(p *engine.Pipeline) engine.RunFunc {
	run := p.RunFunc()
	return func(ctx context.Context, t *model.Task, events chan<- model.StatusEvent) error {
		return run(ctx, isolatedTask(t), events)
	}
}

// isolatedTask returns a copy of the task whose profile and episode config are
// private to the caller, or nil for a nil task.
func isolatedTask(t *model.Task) *model.Task {
	if t == nil {
		return nil
	}
	private := *t
	private.Profile = cloneProfile(t.Profile)
	if cfg := cloneEpisodeConfig(t.Config); cfg != nil {
		private.Config = cfg
	}
	return &private
}

// cloneProfile copies the typed profile a task carries, or returns the value
// unchanged when it is not a *profile.Profile (the zero value a JSON round trip
// leaves behind).
func cloneProfile(v any) any {
	prof, ok := v.(*profile.Profile)
	if !ok || prof == nil {
		return v
	}
	clone := *prof
	clone.AudioTracks = append([]profile.AudioTrackSpec(nil), prof.AudioTracks...)
	clone.SubtitleTracks = append([]profile.TrackSpec(nil), prof.SubtitleTracks...)
	clone.InputFiles = append([]string(nil), prof.InputFiles...)
	if cfg := cloneEpisodeConfig(prof.Config); cfg != nil {
		if typed, ok := cfg.(*profile.EpisodeConfig); ok {
			clone.Config = typed
		}
	}
	return &clone
}

// cloneEpisodeConfig copies the episode config, including the slice array the
// pipeline rewrites during a re-encode. It returns nil for a nil or untyped
// value, so the caller can leave the original in place.
func cloneEpisodeConfig(v any) any {
	cfg, ok := v.(*profile.EpisodeConfig)
	if !ok || cfg == nil {
		return nil
	}
	clone := *cfg
	clone.VspipeArgs = append([]string(nil), cfg.VspipeArgs...)
	clone.ReEncodeSliceArray = append([]model.SliceInfo(nil), cfg.ReEncodeSliceArray...)
	return &clone
}

// asmAVX512 is the assembly level the legacy avx512 switch selects.
const asmAVX512 = "avx512"

// asmFor maps the legacy avx512 configuration option onto the pipeline's Asm
// option.
func asmFor(enabled bool) string {
	if enabled {
		return asmAVX512
	}
	return ""
}

// workerCount resolves how many permanent workers to register.
func workerCount(requested, numaNodes int) int {
	if requested > 0 {
		return requested
	}
	if numaNodes > 0 {
		return numaNodes
	}
	return 1
}

// eventBuffer is the per-subscriber queue size of the progress hub. It is wider
// than the executor's own event buffer (64), so a client that is one event
// behind is not reported as dropping while a genuinely slow client still loses
// events instead of stalling a worker: Hub.Publish never blocks.
const eventBuffer = 128

// daemonHandler builds the HTTP handler the daemon serves.
//
// Route precedence is net/http's: the longest pattern wins, so the explicit
// /api/v1/... registrations and the WebSocket route always beat the "/"
// catch-all. The UI is registered last because that is the order that reads
// correctly, not because the order matters.
func daemonHandler(srv *api.Server, stream *ws.Stream) http.Handler {
	mux := http.NewServeMux()
	srv.Mount(mux)                    // /api/v1/status, /tasks, /pool, /config
	mux.Handle(stream.Path(), stream) // /api/v1/events
	mux.Handle("/", web.Handler())    // the embedded UI, everything else
	return guard(mux)
}

// guard recovers a panic in any handler and logs one line per request.
//
// The REST package installs the same wrapping when it owns the whole handler
// (Server.Handler), but a daemon that mounts the routes on a shared mux has to
// provide it: the mux also carries the WebSocket and UI handlers. Recovering is
// what turns a panic into a 500 for one client instead of a dropped connection.
func guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		gw := &guardedWriter{ResponseWriter: w}
		defer func() {
			if rec := recover(); rec != nil {
				log.Error("接口处理时发生异常", "method", r.Method, "path", r.URL.Path, "panic", rec)
				if !gw.wrote {
					http.Error(gw, "内部错误", http.StatusInternalServerError)
				}
			}
			log.Debug("接口请求", "method", r.Method, "path", r.URL.Path,
				"status", gw.status(), "duration_ms", time.Since(start).Milliseconds())
		}()
		next.ServeHTTP(gw, r)
	})
}

// guardedWriter records whether a handler already started a response, so the
// panic guard knows whether a status line can still be sent. Unwrap keeps
// http.ResponseController working, which is what the WebSocket upgrade needs to
// hijack the connection.
type guardedWriter struct {
	http.ResponseWriter
	code  int
	wrote bool
}

// WriteHeader implements http.ResponseWriter. A second call is ignored, which
// is what net/http would only warn about.
func (w *guardedWriter) WriteHeader(code int) {
	if w.wrote {
		return
	}
	w.wrote = true
	w.code = code
	w.ResponseWriter.WriteHeader(code)
}

// Write implements http.ResponseWriter.
func (w *guardedWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.wrote = true
		w.code = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap exposes the wrapped writer to http.ResponseController.
func (w *guardedWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// status returns the status to log. A handler that wrote nothing still produced
// a 200 with an empty body.
func (w *guardedWriter) status() int {
	if w.code == 0 {
		return http.StatusOK
	}
	return w.code
}

// startServer serves handler on an already open listener and returns the server
// plus the channel that reports its exit.
//
// The listener is opened by the caller, so an address that cannot be bound is a
// startup error instead of a message in the log a second later.
func startServer(ln net.Listener, handler http.Handler) (*http.Server, <-chan error) {
	srv := &http.Server{
		Handler: handler,
		// The API carries small JSON documents and the WebSocket stream is
		// hijacked, so a short header timeout is safe and keeps a stalled
		// client from holding a connection open.
		ReadHeaderTimeout: 10 * time.Second,
	}
	// net/http's own complaints (a superfluous WriteHeader, a panic it had to
	// catch) belong in the daemon's log like everything else. The handler is
	// captured here, so a later SetLevel/SetOutput does not move this stream.
	srv.ErrorLog = slog.NewLogLogger(log.L().Handler(), slog.LevelWarn)

	done := make(chan error, 1)
	go func() {
		err := srv.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		done <- err
	}()
	return srv, done
}

// shutdownServer stops the HTTP server, bounded by ctx. Shutdown closes the
// listener immediately and then waits for the handlers that are still running;
// hijacked WebSocket connections are deliberately not covered, which is why the
// caller closes the hub as well.
func shutdownServer(ctx context.Context, srv *http.Server) error {
	if srv == nil {
		return nil
	}
	if err := srv.Shutdown(ctx); err != nil {
		return okerr.Wrap(err, okerr.KindCanceled, "HTTP 服务未能及时停止", "%v", err)
	}
	return nil
}

// checkToolchain reports the mandatory tools that are missing.
//
// It is a warning rather than a failure: a node without tools can still serve
// the API and show its queue, and refusing to start would make the problem
// harder to diagnose than a log line.
func checkToolchain(caps node.Capabilities) {
	if err := toolchain.CheckRequired(caps); err != nil {
		log.Warn("必需工具缺失，任务无法执行", "err", err)
	}
}

// loopbackAddr reports whether addr only accepts connections from this machine.
// An empty host means every interface, which is not loopback.
func loopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
