package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/KiritakeKumi/OKEGuiDX/internal/engine"
	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/node"
	"github.com/KiritakeKumi/OKEGuiDX/internal/platform"
	"github.com/KiritakeKumi/OKEGuiDX/internal/toolchain"
)

// testEnv is the harness: the real task queue and worker pool over a fake
// executor, so a test never starts a process.
type testEnv struct {
	t      *testing.T
	server *Server
	tasks  *engine.TaskManager
	pool   *engine.WorkerManager
	config *fakeConfigStore
	fix    *fixture
	caps   node.Capabilities
}

// newTestEnv builds a server with a running-capable worker pool.
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()

	fix := newFixture(t)
	caps := fix.capabilities()

	tm, err := engine.New(engine.Options{})
	if err != nil {
		t.Fatalf("engine.New: %v", err)
	}
	exec := engine.ExecutorFuncs{
		CapabilitiesFunc: func(context.Context) (node.Capabilities, error) { return caps, nil },
		SubmitFunc: func(context.Context, *model.Task) (<-chan model.StatusEvent, error) {
			ch := make(chan model.StatusEvent)
			close(ch)
			return ch, nil
		},
	}
	pool := engine.NewWorkerManager(exec, tm, platform.NewNumaWithCount(1))
	pool.AddWorker(1)

	store := &fakeConfigStore{cfg: platform.DefaultConfig()}
	srv, err := New(Options{Tasks: tm, Pool: pool, Exec: exec, Config: store})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return &testEnv{t: t, server: srv, tasks: tm, pool: pool, config: store, fix: fix, caps: caps}
}

// do runs one request against the server.
func (e *testEnv) do(method, path string, body any) *httptest.ResponseRecorder {
	e.t.Helper()

	var reader io.Reader
	switch v := body.(type) {
	case nil:
	case string:
		reader = strings.NewReader(v)
	case []byte:
		reader = bytes.NewReader(v)
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			e.t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}

	req := httptest.NewRequest(method, path, reader)
	rec := httptest.NewRecorder()
	e.server.ServeHTTP(rec, req)
	return rec
}

// addTask creates one task through the API and returns it.
func (e *testEnv) addTask(profilePath, input string) model.Task {
	e.t.Helper()

	body := addTaskRequest{ProfilePath: profilePath, Inputs: []string{input}}
	rec := e.do(http.MethodPost, APIPrefix+"/tasks", body)
	if rec.Code != http.StatusCreated {
		e.t.Fatalf("POST /tasks = %d, want 201: %s", rec.Code, rec.Body)
	}
	var resp taskResponse
	decodeJSON(e.t, rec, &resp)
	return resp.Task
}

// fixture is a temporary directory holding everything a profile needs: a
// script with the input tag, a source file, a VapourSynth VERSION file and a
// profile.
type fixture struct {
	dir     string
	vspipe  string
	x265    string
	vpy     string
	input   string
	profile string
}

// fixtureProfile is the profile text. It is written rather than embedded so the
// paths can be filled in; the shape matches dist/windows/examples/demo.json.
const fixtureProfile = `{
    "Version" : 3,
    "VSVersion" : "2024H1",
    "ProjectName" : "ep01",
    "EncoderType" : "x265",
    "EncoderParam" : "--crf 16",
    "ContainerFormat" : "mkv",
    "AudioTracks" : [{
        "OutputCodec" : "flac"
    }],
    "InputScript" : "demo.vpy",
    "InputFiles" : [
        "00001.m2ts",
    ],
    "Fps" : 23.976,
}
`

// fixtureVpy is a script the validator accepts: it carries the INPUTFILE tag.
const fixtureVpy = "#OKE:INPUTFILE\na = \"00001.m2ts\"\nsrc = core.lsmas.LWLibavSource(a)\nsrc.set_output()\n"

func newFixture(t *testing.T) *fixture {
	t.Helper()

	dir := t.TempDir()
	f := &fixture{
		dir:     dir,
		vspipe:  filepath.Join(dir, "vspipe.exe"),
		x265:    filepath.Join(dir, "x265.exe"),
		vpy:     filepath.Join(dir, "demo.vpy"),
		input:   filepath.Join(dir, "00001.m2ts"),
		profile: filepath.Join(dir, "demo.json"),
	}
	writeFile(t, f.vspipe, "")
	writeFile(t, f.x265, "")
	writeFile(t, filepath.Join(dir, "VERSION"), "2024H1\n")
	writeFile(t, f.vpy, fixtureVpy)
	writeFile(t, f.input, "")
	writeFile(t, f.profile, fixtureProfile)
	return f
}

// capabilities describes the fake node: vspipe beside its VERSION file and the
// x265 the profile's EncoderType resolves to.
func (f *fixture) capabilities() node.Capabilities {
	caps := node.NewCapabilities(node.RoleStandalone)
	caps.Tools[toolchain.ToolVSPipe] = node.ToolInfo{Path: f.vspipe, Version: "test"}
	caps.Tools[toolchain.ToolX265] = node.ToolInfo{Path: f.x265, Variant: toolchain.VariantKyouko}
	caps.AddFeature(node.FeatureAAC)
	return caps
}

// inlineProfileJSON returns the fixture profile with absolute paths and its
// trailing commas removed, so it can be embedded in a strict JSON request body.
func (f *fixture) strictProfileJSON() string {
	raw := strings.ReplaceAll(fixtureProfile, `"demo.vpy"`, strconv.Quote(f.vpy))
	raw = strings.ReplaceAll(raw, `"00001.m2ts"`, strconv.Quote(f.input))
	// Drop the trailing commas the fixture keeps for realism.
	raw = strings.ReplaceAll(raw, "},\n    ]", "}\n    ]")
	raw = strings.ReplaceAll(raw, ",\n}", "\n}")
	raw = strings.ReplaceAll(raw, ",\n    ]", "\n    ]")
	return raw
}

// profileText returns the exact bytes of the fixture's profile file, trailing
// commas and all.
func (f *fixture) profileText() string { return fixtureProfile }

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// decodeJSON decodes a recorded response, failing the test on a malformed body.
func decodeJSON(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
}

// errorOf asserts the response is the standard error object and returns it.
//
// The shape is asserted here rather than in every test: {"error": {...}} with
// exactly three keys, so a handler cannot invent its own failure format.
func errorOf(t *testing.T, rec *httptest.ResponseRecorder) errorInfo {
	t.Helper()

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("error body is not JSON: %q: %v", rec.Body.String(), err)
	}
	if len(raw) != 1 {
		t.Fatalf("error body has %d top-level keys, want 1: %q", len(raw), rec.Body.String())
	}
	inner, ok := raw["error"]
	if !ok {
		t.Fatalf("error body has no \"error\" key: %q", rec.Body.String())
	}

	var info errorInfo
	if err := json.Unmarshal(inner, &info); err != nil {
		t.Fatalf("decode error object: %v", err)
	}
	if info.Summary == "" {
		t.Errorf("error summary is empty: %q", rec.Body.String())
	}

	// Only summary, detail and field may appear.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(inner, &fields); err != nil {
		t.Fatalf("decode error fields: %v", err)
	}
	for name := range fields {
		switch name {
		case "summary", "detail", "field":
		default:
			t.Errorf("unexpected error field %q in %q", name, rec.Body.String())
		}
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	return info
}

// wantStatus asserts the status code and, on a failure, the error shape.
func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, status int) {
	t.Helper()
	if rec.Code == status {
		return
	}
	if rec.Code >= 400 {
		info := errorOf(t, rec)
		t.Fatalf("status = %d, want %d (error: %s / %s)", rec.Code, status, info.Summary, info.Detail)
	}
	t.Fatalf("status = %d, want %d (body: %s)", rec.Code, status, rec.Body.String())
}

// fakeConfigStore keeps the settings in memory.
type fakeConfigStore struct {
	mu    sync.Mutex
	cfg   platform.Config
	err   error
	saves int
}

// Load implements ConfigStore.
func (s *fakeConfigStore) Load() (platform.Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg, s.err
}

// Save implements ConfigStore.
func (s *fakeConfigStore) Save(cfg platform.Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.cfg = cfg
	s.saves++
	return nil
}

// stored returns the settings the store holds.
func (s *fakeConfigStore) stored() platform.Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg
}
