// Package api serves the REST interface that the CLI, the Web UI and a future
// cluster coordinator talk to.
//
// The package is deliberately thin: a handler decodes a request, calls the
// component that already owns the behaviour (engine for the queue and the
// worker pool, profile for validation, platform for the settings) and encodes
// the answer. Nothing is reimplemented here, so the CLI reaches the same
// behaviour without going through HTTP.
//
// Three constraints shape the design (CLUSTER.md §4):
//
//   - every route lives under /api/v1, so a later protocol can be served next
//     to this one instead of replacing it;
//   - authentication is out of scope for this rewrite, so all that exists is
//     the mount point for it (Server.auth), with a comment saying what goes
//     there;
//   - net/http and encoding/json are enough, so no router dependency is added.
package api

import (
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/KiritakeKumi/OKEGuiDX/internal/engine"
	"github.com/KiritakeKumi/OKEGuiDX/internal/log"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
	"github.com/KiritakeKumi/OKEGuiDX/internal/platform"
)

// APIPrefix is the versioned prefix every route lives under.
const APIPrefix = "/api/v1"

// maxBodyBytes bounds a request body. A profile is a few kilobytes, so anything
// larger is a mistake or an attack.
const maxBodyBytes = 1 << 20

// DefaultStopTimeout bounds how long POST /api/v1/pool/stop waits for the
// workers to become quiescent before answering 202. A worker only exits once
// its executor closes the event channel, and an executor that never does would
// otherwise hold the request open forever.
const DefaultStopTimeout = 30 * time.Second

// Server is the REST interface. It is safe for concurrent use: New fills every
// field and nothing writes them afterwards, so all mutable state belongs to the
// task manager and the worker pool, which have their own locks.
type Server struct {
	tasks          *engine.TaskManager
	pool           *engine.WorkerManager
	exec           engine.Executor
	config         ConfigStore
	onConfigChange func(platform.Config)
	stopTimeout    time.Duration
	handler        http.Handler
}

// Options configures a Server. Tasks, Pool and Exec are required.
type Options struct {
	// Tasks is the task queue the endpoints read and mutate.
	Tasks *engine.TaskManager
	// Pool is the worker pool. It is what decides whether a newly added task
	// starts running, and what the pool endpoints drive.
	Pool *engine.WorkerManager
	// Exec reports what this node can run. GET /api/v1/status asks it for
	// Capabilities, which is the same value a future scheduler would use.
	Exec engine.Executor
	// Config reads and writes the application settings. Nil means the default
	// store: the per-user OKEGuiConfig.json that internal/platform owns.
	Config ConfigStore
	// OnConfigChange, when set, is called with the stored settings after a
	// successful PUT /api/v1/config, so the daemon can apply them (log level,
	// tool paths). It runs outside any lock.
	OnConfigChange func(platform.Config)
	// StopTimeout bounds POST /api/v1/pool/stop. Zero means DefaultStopTimeout.
	StopTimeout time.Duration
}

// New returns a server for the given components.
func New(opts Options) (*Server, error) {
	if opts.Tasks == nil {
		return nil, okerr.New(okerr.KindUnknown, "无法启动接口服务", "必须提供任务队列。")
	}
	if opts.Pool == nil {
		return nil, okerr.New(okerr.KindUnknown, "无法启动接口服务", "必须提供工作单元池。")
	}
	if opts.Exec == nil {
		return nil, okerr.New(okerr.KindUnknown, "无法启动接口服务", "必须提供执行器。")
	}

	s := &Server{
		tasks:          opts.Tasks,
		pool:           opts.Pool,
		exec:           opts.Exec,
		config:         opts.Config,
		onConfigChange: opts.OnConfigChange,
		stopTimeout:    opts.StopTimeout,
	}
	if s.config == nil {
		s.config = platformConfigStore{}
	}
	if s.stopTimeout <= 0 {
		s.stopTimeout = DefaultStopTimeout
	}
	s.handler = s.observe(s.auth(s.routes()))
	return s, nil
}

// Handler returns the fully wrapped HTTP handler.
//
// It is a complete handler on its own: mounting it on an http.Server serves the
// whole API. Use Mount instead when another handler has to share the /api/v1
// prefix, which is how the WebSocket progress stream (E3) is served next to
// this package.
func (s *Server) Handler() http.Handler { return s.handler }

// Mount registers the API's routes on a caller-provided mux, so a daemon can
// serve this package and another handler (the WebSocket stream) from one
// listener. The routes are relative to APIPrefix; a more specific sibling
// pattern such as "/api/v1/events" wins over the catch-all this registers, so
// the caller can add one after mounting.
//
// The wrapping Handler applies — the access log, the panic guard and the auth
// mount point — is not installed here: it belongs to the handler the daemon
// passes to http.Server, and installing it twice would log every request twice.
// A caller that mounts the routes by hand should wrap the result the same way
// Handler does, or use Handler and let E3 register its route on its own mux.
//
// A mux that already has a conflicting pattern panics, which is net/http's own
// behaviour for a double registration; that is a programming error, not a
// runtime condition.
func (s *Server) Mount(mux *http.ServeMux) {
	mux.Handle(APIPrefix+"/status", methods{
		http.MethodGet: s.handleStatus,
	})
	mux.Handle(APIPrefix+"/tasks", methods{
		http.MethodGet:  s.handleTaskList,
		http.MethodPost: s.handleTaskAdd,
	})
	mux.Handle(APIPrefix+"/tasks/{id}", methods{
		http.MethodGet:    s.handleTaskGet,
		http.MethodPatch:  s.handleTaskPatch,
		http.MethodDelete: s.handleTaskDelete,
	})
	mux.Handle(APIPrefix+"/pool/start", methods{
		http.MethodPost: s.handlePoolStart,
	})
	mux.Handle(APIPrefix+"/pool/stop", methods{
		http.MethodPost: s.handlePoolStop,
	})
	mux.Handle(APIPrefix+"/config", methods{
		http.MethodGet: s.handleConfigGet,
		http.MethodPut: s.handleConfigPut,
	})

	// Anything else under the prefix gets the same error object as every other
	// failure. The exact prefix is registered as well so that /api/v1 is not
	// answered with ServeMux's redirect to /api/v1/.
	mux.Handle(APIPrefix+"/", http.HandlerFunc(s.handleUnknownRoute))
	mux.Handle(APIPrefix, http.HandlerFunc(s.handleUnknownRoute))
}

// ServeHTTP implements http.Handler, so a Server can be handed straight to
// http.Server{Handler: ...}.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

// routes builds the versioned routing table. It is used by Handler; Mount
// registers the same routes on a caller's mux so a second handler can share the
// prefix.
//
// The patterns carry no method because methods (below) dispatches on the
// request itself: ServeMux's method patterns answer a wrong method with a
// plain-text body, and every failure this API returns has to be the same JSON
// object (see writeError).
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	s.Mount(mux)

	// The pattern is "/" rather than "/api/v1/" so that a request outside the
	// versioned prefix is also answered in the API's format: a client that
	// guesses a path should get the same object it gets everywhere else.
	mux.Handle("/", http.HandlerFunc(s.handleUnknownRoute))
	return mux
}

// observe logs every request and turns a panic into a JSON 500. It wraps the
// response writer so it knows whether a handler already started writing, which
// is what makes the panic answer safe: once the header is out, a second one
// cannot be sent.
func (s *Server) observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		defer func() {
			if rec := recover(); rec != nil {
				log.Error("接口处理时发生异常", "method", r.Method, "path", r.URL.Path, "panic", rec)
				if !sw.wrote {
					writeError(sw, http.StatusInternalServerError,
						okerr.New(okerr.KindUnknown, "内部错误", "处理请求时发生异常：%v", rec))
				}
			}
			log.Debug("接口请求", "method", r.Method, "path", r.URL.Path,
				"status", sw.code(), "duration_ms", time.Since(start).Milliseconds())
		}()
		next.ServeHTTP(sw, r)
	})
}

// auth is the reserved mount point for authentication (CLUSTER.md §4).
//
// Authentication is deliberately not implemented in this rewrite: the daemon
// serves a local process over the loopback interface, and a half-built scheme
// would be worse than none. What is reserved is the place to put it, because
// that is the part that is expensive to add later. A future implementation
// wraps the routes exactly like this:
//
//	func (s *Server) auth(next http.Handler) http.Handler {
//		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
//			if !s.authorized(r) {
//				w.Header().Set("WWW-Authenticate", `Bearer realm="okeguidx"`)
//				writeError(w, http.StatusUnauthorized, errUnauthorized)
//				return
//			}
//			next.ServeHTTP(w, r)
//		})
//	}
//
// It runs after the request has been observed and before any route matches, so
// an unauthenticated request never reaches a handler. Nothing else in this
// package has to change: every route is already relative to APIPrefix, so the
// middleware applies to all of them.
func (s *Server) auth(next http.Handler) http.Handler { return next }

// handleUnknownRoute answers a path under the prefix that no route matched.
func (s *Server) handleUnknownRoute(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound, okerr.New(okerr.KindNotFound,
		"找不到接口", "没有 %s %s 这个接口。", r.Method, r.URL.Path))
}

// methods dispatches one path by request method.
type methods map[string]http.HandlerFunc

// ServeHTTP implements http.Handler. HEAD is served by the GET handler when
// there is one, which is what ServeMux's method patterns do too; net/http
// discards the body of a HEAD response.
func (m methods) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h, ok := m[r.Method]; ok {
		h(w, r)
		return
	}
	if r.Method == http.MethodHead {
		if h, ok := m[http.MethodGet]; ok {
			h(w, r)
			return
		}
	}

	allow := make([]string, 0, len(m)+1)
	for name := range m {
		allow = append(allow, name)
	}
	if _, ok := m[http.MethodGet]; ok {
		allow = append(allow, http.MethodHead)
	}
	slices.Sort(allow)
	list := strings.Join(allow, ", ")

	w.Header().Set("Allow", list)
	writeError(w, http.StatusMethodNotAllowed, okerr.New(okerr.KindUnknown,
		"方法不允许", "%s 不支持 %s 请求，可用方法：%s。", r.URL.Path, r.Method, list))
}

// statusWriter records the status code a handler sent, so the access log can
// report it and the panic handler can tell whether a response was started.
type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

// WriteHeader implements http.ResponseWriter. A second call is ignored, which
// is what net/http would only warn about.
func (w *statusWriter) WriteHeader(code int) {
	if w.wrote {
		return
	}
	w.wrote = true
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Write implements http.ResponseWriter.
func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.wrote = true
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap exposes the wrapped writer to http.ResponseController, so streaming
// responses keep working if one is added later.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// code returns the status to log. A handler that wrote nothing still produced a
// 200 with an empty body, so that is what it reports.
func (w *statusWriter) code() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}
