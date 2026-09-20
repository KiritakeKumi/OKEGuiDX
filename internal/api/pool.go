package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/KiritakeKumi/OKEGuiDX/internal/log"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
)

// poolResponse is the payload of the pool endpoints. It always carries the
// pool's state after the call, so a client never has to guess what happened.
type poolResponse struct {
	Pool poolStatus `json:"pool"`
	// Stopped reports whether the stop request finished within the timeout.
	// It is absent from a start response.
	Stopped *bool `json:"stopped,omitempty"`
	// TimeoutSeconds echoes the bound that applied to this stop.
	TimeoutSeconds *int `json:"timeout_seconds,omitempty"`
}

// handlePoolStart implements POST /api/v1/pool/start.
//
// Start reports false when no worker is registered, which is a real
// configuration problem (the daemon registers one worker per NUMA node at
// startup), so it is reported as a 409 rather than a silent success.
func (s *Server) handlePoolStart(w http.ResponseWriter, r *http.Request) {
	if !s.pool.Start() {
		writeError(w, http.StatusConflict, okerr.New(okerr.KindConfig,
			"没有可用的工作单元", "工作单元池是空的，无法启动。请先配置工作单元数量。"))
		return
	}
	log.Info("工作单元池已启动", "workers", s.pool.GetWorkerCount())
	writeJSON(w, http.StatusOK, poolResponse{Pool: s.poolStatus()})
}

// handlePoolStop implements POST /api/v1/pool/stop.
//
// The pool's Stop waits until every worker goroutine has exited, and a worker
// only exits once its executor closes the event channel. Bounding that wait is
// therefore part of the endpoint, not a nicety: a client must get an answer
// even when a task refuses to die.
//
// Waiting tasks are cancelled too (WorkerManager.CancelAll): a client that
// pressed stop expects the whole run to be over, and the legacy UI did the same
// from the outside by disabling every task that had not started. An individual
// task is cancelled with POST /tasks/{id}/cancel instead.
//
//   - 200: the pool is quiescent, every running task reached its terminal
//     state ("已终止" for the ones the stop cancelled).
//   - 202: the deadline passed first. The stop is still in progress; the
//     response says so instead of pretending the pool is idle.
func (s *Server) handlePoolStop(w http.ResponseWriter, r *http.Request) {
	timeout := s.stopTimeout
	if raw := r.URL.Query().Get("timeout_seconds"); raw != "" {
		seconds, err := parseSeconds(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		timeout = seconds
	}

	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	stopped := true
	if err := s.pool.CancelAll(ctx); err != nil {
		if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			writeError(w, errorStatus(err), err)
			return
		}
		// The pool is off — beginStop flips the state before it waits — but a
		// worker goroutine is still winding down, so "stopped" is not true yet.
		// Reporting it as false is the whole point of the 202: a caller that
		// wants to know when the tasks really ended must poll, not assume.
		stopped = false
		log.Warn("工作单元未在期限内停止", "timeout", timeout, "err", err)
	} else {
		log.Info("工作单元池已停止")
	}

	status := http.StatusAccepted
	if stopped {
		status = http.StatusOK
	}
	seconds := int(timeout.Seconds())
	writeJSON(w, status, poolResponse{
		Pool:           s.poolStatus(),
		Stopped:        &stopped,
		TimeoutSeconds: &seconds,
	})
}

// parseSeconds parses a query-string timeout. It rejects a negative or absurd
// value rather than letting it reach context.WithTimeout, where a negative
// duration means "already expired" and would turn every stop into an immediate
// timeout.
func parseSeconds(raw string) (time.Duration, error) {
	seconds, err := time.ParseDuration(raw + "s")
	if err != nil || seconds < 0 {
		return 0, okerr.New(okerr.KindConfig, "参数不合法",
			"timeout_seconds 必须是非负的秒数（当前 %q）。", raw)
	}
	if seconds > maxStopTimeout {
		return 0, okerr.New(okerr.KindConfig, "参数不合法",
			"timeout_seconds 不能超过 %d 秒。", int(maxStopTimeout.Seconds()))
	}
	return seconds, nil
}

// maxStopTimeout bounds a client-supplied stop timeout, so a request cannot
// hold a connection open indefinitely.
const maxStopTimeout = time.Hour
