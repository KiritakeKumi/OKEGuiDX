package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KiritakeKumi/OKEGuiDX/internal/engine"
	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/node"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
	"github.com/KiritakeKumi/OKEGuiDX/internal/profile"
)

// errBoom is a store failure used by the settings tests.
var errBoom = errors.New("store is broken")

// ptr returns a pointer to v, for the request fields that are pointers so that
// "absent" and "false" differ.
func ptr[T any](v T) *T { return &v }

// strconvQuote renders s as a JSON string literal.
func strconvQuote(s string) string { return strconv.Quote(s) }

// doVia runs a request against a specific server rather than the harness's own.
func (e *testEnv) doVia(srv *Server, method, path, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

// engineWithFailingCapabilities returns a server whose executor cannot report
// what the node can run, which is the status endpoint's error path.
func engineWithFailingCapabilities(t *testing.T, env *testEnv) *Server {
	t.Helper()
	exec := engine.ExecutorFuncs{
		CapabilitiesFunc: func(context.Context) (node.Capabilities, error) {
			return node.Capabilities{}, errors.New("no capabilities")
		},
		SubmitFunc: func(context.Context, *model.Task) (<-chan model.StatusEvent, error) {
			ch := make(chan model.StatusEvent)
			close(ch)
			return ch, nil
		},
	}
	srv, err := New(Options{Tasks: env.tasks, Pool: env.pool, Exec: exec})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return srv
}

// okerrNew builds an okerr.Error for the mapping tests.
func okerrNew(summary, detail string) error {
	return okerr.New(okerr.KindUnknown, summary, "%s", detail)
}

// okerrConfig builds a KindConfig error.
func okerrConfig() error {
	return okerr.New(okerr.KindConfig, "配置错误", "")
}

// okerrUnsupported builds a KindUnsupported error.
func okerrUnsupported() error {
	return okerr.New(okerr.KindUnsupported, "不支持", "")
}

// okerrMismatch builds a KindMismatch error.
func okerrMismatch() error {
	return okerr.New(okerr.KindMismatch, "不匹配", "")
}

// okerrIO builds a KindIO error.
func okerrIO() error {
	return okerr.New(okerr.KindIO, "读写失败", "")
}

// validationError builds a profile.ValidationError.
func validationError(summary, detail, field string) error {
	return &profile.ValidationError{Summary: summary, Detail: detail, Field: field}
}

// mustJSON marshals a request body, failing the test on a value that cannot be
// encoded.
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	return string(raw)
}

// waitForRunning waits until the queue holds a running task, so a test can rely
// on a worker having claimed one.
func waitForRunning(t *testing.T, tm *engine.TaskManager) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, task := range tm.Snapshot() {
			if task.Status.Progress == model.TaskRunning {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no task reached the running state")
}
