package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KiritakeKumi/OKEGuiDX/internal/engine"
	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/node"
	"github.com/KiritakeKumi/OKEGuiDX/internal/platform"
	"github.com/KiritakeKumi/OKEGuiDX/internal/proc"
	"github.com/KiritakeKumi/OKEGuiDX/internal/profile"
	"github.com/KiritakeKumi/OKEGuiDX/internal/toolchain"
)

// capsFor builds the capabilities the daemon would discover for a fixture's
// tools tree. It skips the version probe: the stub tools are text files, and
// nothing in the wiring path needs their banner.
func capsFor(t *testing.T, toolsRoot string) node.Capabilities {
	t.Helper()
	caps, err := toolchain.Discover(toolchain.Options{
		Root:       toolsRoot,
		Role:       node.RoleStandalone,
		SingleNUMA: true,
		SkipProbe:  true,
	})
	if err != nil {
		t.Fatalf("toolchain.Discover() error = %v", err)
	}
	if _, ok := caps.Tool(toolchain.ToolX265); !ok {
		t.Fatalf("the fixture's tools tree did not yield an x265: %+v", caps.Tools)
	}
	return caps
}

// daemonTestSettings returns settings pinned to a temporary directory, so a
// test never touches the per-user configuration directory.
func daemonTestSettings(t *testing.T, caps node.Capabilities) *settings {
	t.Helper()
	dir := t.TempDir()
	return &settings{
		role:      node.RoleStandalone,
		caps:      caps,
		queuePath: filepath.Join(dir, "queue.json"),
		configDir: dir,
	}
}

// TestAssembleWiresTheEngineTogether is the wiring contract: one call must
// produce a queue, an executor, a pipeline and a pool, with the pool holding
// one worker per NUMA node by default.
func TestAssembleWiresTheEngineTogether(t *testing.T) {
	caps := node.NewCapabilities(node.RoleStandalone)
	caps.NUMANodes = 3
	s := daemonTestSettings(t, caps)

	parts, err := assemble(assembleOptions{Settings: s})
	if err != nil {
		t.Fatalf("assemble() error = %v", err)
	}
	if parts.Tasks == nil || parts.Exec == nil || parts.Pipeline == nil || parts.Workers == nil {
		t.Fatalf("assemble() left a component nil: %+v", parts)
	}
	if got := parts.Workers.GetWorkerCount(); got != 3 {
		t.Errorf("worker count = %d, want 3 (one per NUMA node)", got)
	}
}

// TestAssembleWorkerCountOverridesTheDefault pins the flag the command line
// uses: --workers must win over the NUMA default.
func TestAssembleWorkerCountOverridesTheDefault(t *testing.T) {
	caps := node.NewCapabilities(node.RoleStandalone)
	caps.NUMANodes = 4
	s := daemonTestSettings(t, caps)

	parts, err := assemble(assembleOptions{Settings: s, WorkerCount: 2})
	if err != nil {
		t.Fatalf("assemble() error = %v", err)
	}
	if got := parts.Workers.GetWorkerCount(); got != 2 {
		t.Errorf("worker count = %d, want the requested 2", got)
	}
}

// TestPipelineHooksReadTheQueue is the reason the pipeline is assembled after
// the queue: a task recovered from disk carries only a config path, and the
// pipeline's LoadProfile hook is what turns that back into a profile.
func TestPipelineHooksReadTheQueue(t *testing.T) {
	s := daemonTestSettings(t, node.NewCapabilities(node.RoleStandalone))
	parts, err := assemble(assembleOptions{Settings: s})
	if err != nil {
		t.Fatalf("assemble() error = %v", err)
	}

	// An unknown task id must be reported rather than silently yielding a zero
	// profile, which would fail much later and much less clearly.
	err = parts.Pipeline.Run(context.Background(), &model.Task{ID: model.NewTaskID()},
		make(chan model.StatusEvent, 1))
	if err == nil {
		t.Fatal("Run() of a task outside the queue returned nil, want an error")
	}
	if !strings.Contains(err.Error(), "不在队列中") {
		t.Errorf("Run() error = %v, want it to name the missing queue entry", err)
	}
}

// TestNewServiceUsesOneWorkerPerNUMANode pins the default concurrency the
// legacy MainWindow established.
func TestNewServiceUsesOneWorkerPerNUMANode(t *testing.T) {
	caps := node.NewCapabilities(node.RoleStandalone)
	caps.NUMANodes = 3
	svc, err := newService(daemonTestSettings(t, caps))
	if err != nil {
		t.Fatalf("newService() error = %v", err)
	}
	if got := svc.parts.Workers.GetWorkerCount(); got != 3 {
		t.Errorf("worker count = %d, want 3 (one per NUMA node)", got)
	}
}

// TestNewServiceToleratesADamagedQueue asserts the daemon comes up even when
// the recovery file is unusable: an operator needs the service running in
// order to look at the problem.
func TestNewServiceToleratesADamagedQueue(t *testing.T) {
	s := daemonTestSettings(t, node.NewCapabilities(node.RoleStandalone))
	if err := os.WriteFile(s.queuePath, []byte("{ not json"), 0o600); err != nil {
		t.Fatalf("write queue: %v", err)
	}

	svc, err := newService(s)
	if err != nil {
		t.Fatalf("newService() error = %v", err)
	}
	if got := svc.parts.Tasks.GetTaskCount(); got != 0 {
		t.Errorf("task count = %d, want 0 for an unreadable queue", got)
	}
}

// TestDaemonResumesARecoveredQueue asserts the crash-recovery contract: a task
// left running by a dead process is reset to waiting and picked up on start.
func TestDaemonResumesARecoveredQueue(t *testing.T) {
	s := daemonTestSettings(t, node.NewCapabilities(node.RoleStandalone))
	writeStaleQueue(t, s.queuePath)

	svc, err := newService(s)
	if err != nil {
		t.Fatalf("newService() error = %v", err)
	}
	if got := svc.parts.Tasks.GetActiveTaskCount(); got != 1 {
		t.Fatalf("active task count = %d, want 1 after recovery", got)
	}

	svc.Start()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := svc.Shutdown(ctx, nil); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	// The recovered task cannot succeed (its profile path does not exist), so
	// it must end as an error rather than stay running forever.
	if got := svc.parts.Tasks.GetRunningTasks(); len(got) != 0 {
		t.Errorf("%d tasks are still marked running after shutdown", len(got))
	}
}

// TestDaemonShutdownIsIdempotent asserts the second call is a no-op, which the
// signal path relies on: a second Ctrl-C must not re-run the shutdown.
func TestDaemonShutdownIsIdempotent(t *testing.T) {
	svc, err := newService(daemonTestSettings(t, node.NewCapabilities(node.RoleStandalone)))
	if err != nil {
		t.Fatalf("newService() error = %v", err)
	}

	ctx := context.Background()
	if err := svc.Shutdown(ctx, nil); err != nil {
		t.Fatalf("first Shutdown() error = %v", err)
	}
	if err := svc.Shutdown(ctx, nil); err != nil {
		t.Fatalf("second Shutdown() error = %v", err)
	}
}

// TestServiceShutdownRespectsDeadline asserts a task that never finishes does
// not hold the daemon open past its grace period.
func TestServiceShutdownRespectsDeadline(t *testing.T) {
	dir := t.TempDir()
	block := make(chan struct{})
	defer close(block)

	caps := node.NewCapabilities(node.RoleStandalone)
	tm, err := engine.New(engine.Options{QueuePath: filepath.Join(dir, "queue.json")})
	if err != nil {
		t.Fatalf("engine.New() error = %v", err)
	}
	exec := engine.NewLocalExecutor(caps, func(ctx context.Context, _ *model.Task, _ chan<- model.StatusEvent) error {
		select {
		case <-block:
		case <-ctx.Done():
		}
		return ctx.Err()
	})
	parts := &engineParts{
		Tasks:   tm,
		Exec:    exec,
		Workers: engine.NewWorkerManager(exec, tm, platform.NewNumaWithCount(1)),
	}
	parts.Workers.AddWorker(1)
	svc := &service{settings: daemonTestSettings(t, caps), parts: parts}

	svc.Start()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := svc.Shutdown(ctx, nil); err != nil {
		t.Fatalf("Shutdown() error = %v, want nil once the pool is quiet", err)
	}
}

// TestDaemonShutdownClosesTheWebSocket asserts the shutdown order reaches a
// connected client: the hub closes, so the stream ends with a close frame
// instead of a dropped socket.
func TestDaemonShutdownClosesTheWebSocket(t *testing.T) {
	svc, err := newService(daemonTestSettings(t, node.NewCapabilities(node.RoleStandalone)))
	if err != nil {
		t.Fatalf("newService() error = %v", err)
	}
	ln, addr := listenLoopback(t)
	srv, done := startServer(ln, daemonHandler(svc.api, svc.stream))
	t.Cleanup(func() { _ = srv.Close() })

	conn := dialWebSocket(t, addr)
	readServerFrame(t, conn) // the hello

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := svc.Shutdown(ctx, srv); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}

	// The hub close ends the connection loop, which sends a close frame.
	frame, err := readFrameWithin(conn, 5*time.Second)
	if err != nil {
		t.Fatalf("read after shutdown: %v", err)
	}
	if frame.opcode != 0x8 {
		t.Errorf("opcode = %#x, want a close frame (0x8)", frame.opcode)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve() error = %v, want nil after Shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("the HTTP server did not return after Shutdown")
	}
}

// TestServeRefusesANonLoopbackAddress pins the security decision: the API has
// no authentication, so a non-loopback --addr must be a usage error rather
// than a warning.
func TestServeRefusesANonLoopbackAddress(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want bool
	}{
		{name: "IPv4 loopback", addr: "127.0.0.1:8090", want: true},
		{name: "IPv6 loopback", addr: "[::1]:8090", want: true},
		{name: "localhost", addr: "localhost:8090", want: true},
		{name: "all interfaces", addr: ":8090", want: false},
		{name: "explicit all interfaces", addr: "0.0.0.0:8090", want: false},
		{name: "routable address", addr: "192.168.1.10:8090", want: false},
		{name: "no port", addr: "127.0.0.1", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := loopbackAddr(tc.addr); got != tc.want {
				t.Errorf("loopbackAddr(%q) = %v, want %v", tc.addr, got, tc.want)
			}
		})
	}

	// The command line must reject it, and with the usage exit code.
	got := runCLI(t, "daemon", "--addr", "0.0.0.0:8090")
	if got.code != exitUsage {
		t.Errorf("exit code = %d, want %d (stderr: %s)", got.code, exitUsage, got.stderr)
	}
	if !strings.Contains(got.stderr, "回环地址") {
		t.Errorf("stderr = %q, want it to explain the loopback restriction", got.stderr)
	}
}

// TestPIDFileRoundTrip covers the helper the daemon uses to advertise itself.
func TestPIDFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "okegui.pid")

	if err := writePIDFile(path); err != nil {
		t.Fatalf("writePIDFile() error = %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read pid file: %v", err)
	}
	want := strings.TrimSpace(string(raw))
	if want == "" {
		t.Fatal("pid file is empty")
	}
	for _, r := range want {
		if r < '0' || r > '9' {
			t.Fatalf("pid file contains %q, want digits only", want)
		}
	}

	removePIDFile(path)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("pid file still exists after removePIDFile: %v", err)
	}
	// Removing a missing file is not an error.
	removePIDFile(path)
}

// TestAssembleIsolatesTheProfileFromTheQueue is a regression test for a data
// race the wiring round introduced: the queue stores a task's profile as a
// shared pointer and the frozen pipeline mutates it (IsReEncode, the re-encode
// slice array), so a REST client reading the queue raced with the worker
// running the task. assemble must hand the pipeline a private copy.
//
// The assertion is on the sharing itself rather than on the race detector, so
// the test fails deterministically.
func TestAssembleIsolatesTheProfileFromTheQueue(t *testing.T) {
	original := &profile.Profile{
		ProjectName:    "ep01",
		ConfigFilePath: "test.json",
		InputFiles:     []string{"in.m2ts"},
		AudioTracks:    []profile.AudioTrackSpec{{OutputCodec: "flac"}},
	}
	cfg := &profile.EpisodeConfig{
		EnableReEncode:     true,
		VspipeArgs:         []string{"--arg", "a=b"},
		ReEncodeSliceArray: []model.SliceInfo{{Begin: 1, End: 2}},
	}
	queued := &model.Task{ID: model.NewTaskID(), Name: "ep01", Profile: original, Config: cfg}

	private := isolatedTask(queued)

	// The profile must be a distinct value, or the run races with the queue.
	copied, ok := private.Profile.(*profile.Profile)
	if !ok {
		t.Fatal("isolatedTask did not keep a *profile.Profile")
	}
	if copied == original {
		t.Fatal("isolatedTask shared the profile pointer, so the run would race with the queue")
	}
	if copied.ProjectName != original.ProjectName || copied.ConfigFilePath != original.ConfigFilePath {
		t.Errorf("the copy lost fields: %+v", copied)
	}

	// Mutating the copy must not touch the queue's value.
	copied.IsReEncode = true
	copied.ConfigFilePath = "changed.json"
	copied.InputFiles[0] = "changed.m2ts"
	copied.AudioTracks[0].OutputCodec = "aac"
	if original.IsReEncode || original.ConfigFilePath != "test.json" ||
		original.InputFiles[0] != "in.m2ts" || original.AudioTracks[0].OutputCodec != "flac" {
		t.Errorf("mutating the copy changed the original: %+v", original)
	}

	// The episode config needs the same treatment: the pipeline rewrites its
	// slice array.
	copiedCfg, ok := private.Config.(*profile.EpisodeConfig)
	if !ok {
		t.Fatal("isolatedTask did not keep a *profile.EpisodeConfig")
	}
	if copiedCfg == cfg {
		t.Fatal("isolatedTask shared the episode config pointer")
	}
	copiedCfg.ReEncodeSliceArray[0].Begin = 99
	copiedCfg.VspipeArgs[0] = "changed"
	if cfg.ReEncodeSliceArray[0].Begin != 1 || cfg.VspipeArgs[0] != "--arg" {
		t.Errorf("mutating the copy changed the original config: %+v", cfg)
	}

	// The profile's own Config pointer is the one the pipeline reads through,
	// so it must be isolated too.
	if copied.Config == cfg {
		t.Error("the copied profile still points at the queue's episode config")
	}

	// A nil task is passed through rather than dereferenced.
	if got := isolatedTask(nil); got != nil {
		t.Errorf("isolatedTask(nil) = %v, want nil", got)
	}
}

// TestCloneHelpersPassThroughUntypedValues asserts the JSON-round-trip case:
// a task recovered from disk carries generic values, and the clone helpers must
// leave them alone rather than replacing them with nil.
func TestCloneHelpersPassThroughUntypedValues(t *testing.T) {
	if got := cloneProfile(nil); got != nil {
		t.Errorf("cloneProfile(nil) = %v, want nil", got)
	}
	if got := cloneProfile("generic"); got != "generic" {
		t.Errorf("cloneProfile(string) = %v, want it unchanged", got)
	}
	if got := cloneEpisodeConfig(nil); got != nil {
		t.Errorf("cloneEpisodeConfig(nil) = %v, want nil", got)
	}
	if got := cloneEpisodeConfig(map[string]any{"a": 1}); got != nil {
		t.Errorf("cloneEpisodeConfig(map) = %v, want nil so the caller keeps the original", got)
	}
	// An untyped profile must keep an untyped config: replacing it with nil
	// would lose a value the pipeline might still read.
	untyped := isolatedTask(&model.Task{ID: model.NewTaskID(), Profile: "generic", Config: "generic"})
	if untyped.Profile != "generic" || untyped.Config != "generic" {
		t.Errorf("isolatedTask changed untyped values: %+v", untyped)
	}
}

// TestDaemonServesTheEndToEndSmoke is the wiring smoke test: it starts the real
// daemon service on a loopback port and drives it the way a client does —
// GET /api/v1/status, GET /api/v1/tasks, then a WebSocket connection that must
// receive its hello before the service shuts down.
func TestDaemonServesTheEndToEndSmoke(t *testing.T) {
	svc, err := newService(daemonTestSettings(t, node.NewCapabilities(node.RoleStandalone)))
	if err != nil {
		t.Fatalf("newService() error = %v", err)
	}
	ln, addr := listenLoopback(t)
	srv, done := startServer(ln, daemonHandler(svc.api, svc.stream))
	svc.Start()

	base := "http://" + addr
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = svc.Shutdown(ctx, srv)
	})

	// 1. GET /api/v1/status: the node, the pool and the queue in one payload.
	body := getJSON(t, base+"/api/v1/status")
	var status struct {
		Node struct {
			Role string `json:"role"`
		} `json:"node"`
		Pool struct {
			Running bool `json:"running"`
			Workers int  `json:"workers"`
		} `json:"pool"`
		Queue struct {
			Total int `json:"total"`
		} `json:"queue"`
	}
	decodeInto(t, body, &status)
	if status.Node.Role != string(node.RoleStandalone) {
		t.Errorf("status node role = %q, want standalone", status.Node.Role)
	}
	if !status.Pool.Running {
		t.Error("status reports the pool as stopped after Start()")
	}
	if status.Pool.Workers != 1 {
		t.Errorf("status worker count = %d, want 1", status.Pool.Workers)
	}
	if status.Queue.Total != 0 {
		t.Errorf("status queue total = %d, want 0", status.Queue.Total)
	}

	// 2. GET /api/v1/tasks: an empty queue is a valid, well-formed answer.
	body = getJSON(t, base+"/api/v1/tasks")
	var list struct {
		Tasks []model.Task `json:"tasks"`
		Count int          `json:"count"`
	}
	decodeInto(t, body, &list)
	if list.Count != 0 || len(list.Tasks) != 0 {
		t.Errorf("task list = %+v, want an empty queue", list)
	}

	// 3. The WebSocket stream: the first message must be the hello, and an
	// event published on the hub must reach the same connection.
	conn := dialWebSocket(t, addr)
	hello := readServerFrame(t, conn)
	if hello.opcode != 0x1 {
		t.Fatalf("hello opcode = %#x, want a text frame", hello.opcode)
	}
	var msg struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	decodeInto(t, hello.payload, &msg)
	if msg.Type != "hello" {
		t.Errorf("first message type = %q, want hello", msg.Type)
	}

	// The hub is the pool's event sink, so publishing on it is exactly what a
	// worker does when a task reports progress.
	want := model.StatusEvent{TaskID: model.NewTaskID(), Progress: model.TaskRunning, Step: "x265", Percent: 42}
	svc.hub.Publish(want)
	event := readServerFrame(t, conn)
	var envelope struct {
		Type  string             `json:"type"`
		Event *model.StatusEvent `json:"event"`
	}
	decodeInto(t, event.payload, &envelope)
	if envelope.Type != "status" || envelope.Event == nil {
		t.Fatalf("second message = %s, want a status event", event.payload)
	}
	if envelope.Event.TaskID != want.TaskID || envelope.Event.Percent != want.Percent {
		t.Errorf("event = %+v, want %+v", *envelope.Event, want)
	}

	// 4. Closing: the client asks for the close handshake and the server
	// answers it.
	writeClientClose(t, conn)
	closeFrame, err := readFrameWithin(conn, 5*time.Second)
	if err != nil {
		t.Fatalf("read close answer: %v", err)
	}
	if closeFrame.opcode != 0x8 {
		t.Errorf("close answer opcode = %#x, want a close frame", closeFrame.opcode)
	}
	_ = conn.Close()

	// 5. Shutdown must be prompt and the server must return.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := svc.Shutdown(ctx, srv); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve() error = %v, want nil after Shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the HTTP server did not return after Shutdown")
	}
}

// TestDaemonRunsATaskEndToEnd is the full wiring cycle: a task is added over
// HTTP, the worker pool picks it up, the pipeline runs it, every status event
// reaches a WebSocket client, and the terminal state is visible on the queue.
//
// The fixture's source is a text file, so the pipeline refuses it. That is the
// point: the test asserts the plumbing, not an encode, and a refusing task
// exercises every hop (executor, pipeline, pool, hub, REST) exactly like a
// successful one does.
func TestDaemonRunsATaskEndToEnd(t *testing.T) {
	f := newFixture(t, "MKV", 1)

	s := &settings{
		role:      node.RoleStandalone,
		caps:      capsFor(t, f.toolsDir),
		queuePath: filepath.Join(f.dir, "queue.json"),
		configDir: f.dir,
	}
	svc, err := newService(s)
	if err != nil {
		t.Fatalf("newService() error = %v", err)
	}
	ln, addr := listenLoopback(t)
	srv, _ := startServer(ln, daemonHandler(svc.api, svc.stream))
	svc.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = svc.Shutdown(ctx, srv)
	})

	// Attach the stream before the task exists, so no event can be missed.
	conn := dialWebSocket(t, addr)
	if frame := readServerFrame(t, conn); !strings.Contains(string(frame.payload), "hello") {
		t.Fatalf("first frame = %s, want the hello", frame.payload)
	}

	body := `{"profile_path":` + strconv.Quote(f.profile) + `}`
	resp := postJSON(t, "http://"+addr+"/api/v1/tasks", body)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read add response: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/v1/tasks = %d, want 201: %s", resp.StatusCode, raw)
	}
	var added struct {
		Task model.Task `json:"task"`
	}
	decodeInto(t, raw, &added)
	if added.Task.ID.IsZero() {
		t.Fatal("the created task has no id")
	}

	// The pipeline reports its steps and then fails, so the stream must carry
	// at least one running event and exactly one terminal event.
	var (
		sawRunning  bool
		sawTerminal bool
	)
	deadline := time.Now().Add(30 * time.Second)
	for !sawTerminal && time.Now().Before(deadline) {
		frame, err := readFrameWithin(conn, 10*time.Second)
		if err != nil {
			t.Fatalf("read event: %v", err)
		}
		if frame.opcode != 0x1 {
			continue
		}
		var envelope struct {
			Type  string             `json:"type"`
			Event *model.StatusEvent `json:"event"`
		}
		decodeInto(t, frame.payload, &envelope)
		if envelope.Type != "status" || envelope.Event == nil {
			continue
		}
		if envelope.Event.TaskID != added.Task.ID {
			t.Errorf("event for task %s, want %s", envelope.Event.TaskID, added.Task.ID)
		}
		switch envelope.Event.Progress {
		case model.TaskRunning:
			sawRunning = true
		case model.TaskError, model.TaskFinished:
			sawTerminal = true
		}
	}
	if !sawRunning {
		t.Error("no running event reached the WebSocket client")
	}
	if !sawTerminal {
		t.Fatal("no terminal event reached the WebSocket client")
	}

	// The queue is the other half of the contract: the REST view must agree
	// with what the stream reported.
	fetchedBody := getJSON(t, "http://"+addr+"/api/v1/tasks/"+added.Task.ID.String())
	var fetched struct {
		Task model.Task `json:"task"`
	}
	decodeInto(t, fetchedBody, &fetched)
	if fetched.Task.Status.Progress != model.TaskError {
		t.Errorf("queued progress = %v, want ERROR", fetched.Task.Status.Progress)
	}
	if fetched.Task.Status.Status == "" {
		t.Error("the failed task carries no status message")
	}
	// The fixture profile is deliberately missing the working-path prefix, so
	// the pipeline must say exactly that: it is the one profile field a
	// headless front end cannot derive (DECISIONS-NEEDED.md A5).
	if !strings.Contains(string(fetchedBody), "工作路径前缀") {
		t.Errorf("task status does not name the missing profile field: %s", fetchedBody)
	}
}

// TestDaemonServesTheWebUI asserts the embedded front end is reachable and that
// it does not shadow the API: the catch-all must lose to the explicit routes.
func TestDaemonServesTheWebUI(t *testing.T) {
	svc, err := newService(daemonTestSettings(t, node.NewCapabilities(node.RoleStandalone)))
	if err != nil {
		t.Fatalf("newService() error = %v", err)
	}
	ln, addr := listenLoopback(t)
	srv, _ := startServer(ln, daemonHandler(svc.api, svc.stream))
	t.Cleanup(func() { _ = srv.Close() })
	base := "http://" + addr

	tests := []struct {
		name        string
		path        string
		wantStatus  int
		wantType    string
		wantContent string
	}{
		{name: "index", path: "/", wantStatus: http.StatusOK, wantType: "text/html", wantContent: "OKEGuiDX"},
		{name: "asset", path: "/static/app.js", wantStatus: http.StatusOK, wantType: "javascript", wantContent: "api/v1"},
		{name: "stylesheet", path: "/static/style.css", wantStatus: http.StatusOK, wantType: "text/css"},
		{name: "unknown path falls back to the shell", path: "/tasks", wantStatus: http.StatusOK, wantType: "text/html"},
		{name: "api status wins over the shell", path: "/api/v1/status", wantStatus: http.StatusOK, wantType: "application/json"},
		{name: "api tasks wins over the shell", path: "/api/v1/tasks", wantStatus: http.StatusOK, wantType: "application/json"},
		{name: "api prefix is the API's", path: "/api/v1/nope", wantStatus: http.StatusNotFound, wantType: "application/json"},
		{name: "api root is the API's", path: "/api/v1", wantStatus: http.StatusNotFound, wantType: "application/json"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := doGet(t, base+tc.path)
			defer resp.Body.Close()
			if resp.StatusCode != tc.wantStatus {
				t.Errorf("GET %s = %d, want %d", tc.path, resp.StatusCode, tc.wantStatus)
			}
			if got := resp.Header.Get("Content-Type"); !strings.Contains(got, tc.wantType) {
				t.Errorf("GET %s Content-Type = %q, want it to contain %q", tc.path, got, tc.wantType)
			}
			if tc.wantContent != "" {
				raw, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Fatalf("read body: %v", err)
				}
				if !strings.Contains(string(raw), tc.wantContent) {
					t.Errorf("GET %s body does not contain %q", tc.path, tc.wantContent)
				}
			}
		})
	}

	// The WebSocket route is not a REST route, so it must not answer a plain
	// GET with a JSON 404: it belongs to the stream handler, which refuses a
	// non-upgrade request with 426.
	resp := doGet(t, base+"/api/v1/events")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUpgradeRequired {
		t.Errorf("GET /api/v1/events = %d, want %d", resp.StatusCode, http.StatusUpgradeRequired)
	}
}

// TestDaemonLogsAndRecoversAPanic asserts the guard the daemon installs around
// the shared mux: a panic in any handler becomes a 500 for that one client.
func TestDaemonLogsAndRecoversAPanic(t *testing.T) {
	ln, addr := listenLoopback(t)
	panicking := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("handler exploded")
	})
	srv, _ := startServer(ln, guard(panicking))
	t.Cleanup(func() { _ = srv.Close() })

	resp := doGet(t, "http://"+addr+"/")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("panicking handler = %d, want 500", resp.StatusCode)
	}
}

// writeStaleQueue writes a queue that looks like a process died mid-task.
func writeStaleQueue(t *testing.T, path string) {
	t.Helper()
	const stale = `{
  "version": 1,
  "created_count": 1,
  "tasks": [
    {
      "config_file_path": "C:\\work\\test.json",
      "task": {
        "id": "11111111-1111-4111-8111-111111111111",
        "name": "recovered",
        "status": {
          "id": "11111111-1111-4111-8111-111111111111",
          "name": "recovered",
          "enabled": false,
          "progress": "RUNNING",
          "status": "压制中",
          "progress_value": 42,
          "speed": "10.0 fps",
          "worker_name": "工作单元-1"
        }
      }
    }
  ]
}`
	if err := os.WriteFile(path, []byte(stale), 0o600); err != nil {
		t.Fatalf("write stale queue: %v", err)
	}
}

// listenLoopback binds a loopback port and returns the listener together with
// its address, so a test never has to guess a free port.
func listenLoopback(t *testing.T) (net.Listener, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln, ln.Addr().String()
}

// doGet performs a GET and fails the test on a transport error.
func doGet(t *testing.T, url string) *http.Response {
	t.Helper()
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return resp
}

// getJSON performs a GET that must answer 200 and returns the body.
func getJSON(t *testing.T, url string) []byte {
	t.Helper()
	resp := doGet(t, url)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200: %s", url, resp.StatusCode, raw)
	}
	return raw
}

// postJSON performs a POST with a JSON body.
func postJSON(t *testing.T, url, body string) *http.Response {
	t.Helper()
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	return resp
}

// decodeInto decodes a JSON body, failing the test on malformed input.
func decodeInto(t *testing.T, raw []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
}

// wsFrame is one frame as the test client sees it.
type wsFrame struct {
	opcode  byte
	payload []byte
}

// dialWebSocket performs the RFC 6455 opening handshake against addr.
func dialWebSocket(t *testing.T, addr string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	const nonce = "MDEyMzQ1Njc4OWFiY2RlZg=="
	req := "GET /api/v1/events HTTP/1.1\r\n" +
		"Host: " + addr + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Version: 13\r\n" +
		"Sec-WebSocket-Key: " + nonce + "\r\n\r\n"
	if _, err := io.WriteString(conn, req); err != nil {
		t.Fatalf("write handshake: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("read handshake response: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("handshake status = %d, want 101", resp.StatusCode)
	}
	// The reader holds whatever the server already sent behind the handshake,
	// so it becomes the connection the frame reader uses. A wrapper keeps the
	// buffered bytes.
	return &bufferedConn{Conn: conn, br: br}
}

// bufferedConn is a net.Conn whose reads come from a buffer that already holds
// bytes read during the handshake.
type bufferedConn struct {
	net.Conn
	br *bufio.Reader
}

// Read implements net.Conn.
func (c *bufferedConn) Read(p []byte) (int, error) { return c.br.Read(p) }

// readServerFrame reads one unmasked frame from the server.
func readServerFrame(t *testing.T, conn net.Conn) wsFrame {
	t.Helper()
	frame, err := readFrameWithin(conn, 10*time.Second)
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	return frame
}

// readFrameWithin reads one unmasked server frame, bounded by timeout.
func readFrameWithin(conn net.Conn, timeout time.Duration) (wsFrame, error) {
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	var head [2]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		return wsFrame{}, err
	}
	opcode := head[0] & 0x0F
	if head[1]&0x80 != 0 {
		return wsFrame{}, errors.New("server frame is masked")
	}
	length := uint64(head[1] & 0x7F)
	switch length {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(conn, ext[:]); err != nil {
			return wsFrame{}, err
		}
		length = uint64(ext[0])<<8 | uint64(ext[1])
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(conn, ext[:]); err != nil {
			return wsFrame{}, err
		}
		length = 0
		for _, b := range ext {
			length = length<<8 | uint64(b)
		}
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(conn, payload); err != nil {
		return wsFrame{}, err
	}
	return wsFrame{opcode: opcode, payload: payload}, nil
}

// writeClientClose sends a masked close frame, which is what a browser sends
// when the page goes away.
func writeClientClose(t *testing.T, conn net.Conn) {
	t.Helper()
	var mask [4]byte
	payload := []byte{0x03, 0xE8} // 1000, normal closure
	frame := make([]byte, 0, 2+len(mask)+len(payload))
	frame = append(frame, 0x88, 0x80|byte(len(payload)))
	frame = append(frame, mask[:]...)
	for i, b := range payload {
		frame = append(frame, b^mask[i&3])
	}
	if _, err := conn.Write(frame); err != nil {
		t.Fatalf("write close frame: %v", err)
	}
}

// TestAssemblyIgnoresTheAsmSwitchWhenOff pins the mapping of the legacy avx512
// option onto the pipeline's Asm value.
func TestAssemblyIgnoresTheAsmSwitchWhenOff(t *testing.T) {
	if got := asmFor(false); got != "" {
		t.Errorf("asmFor(false) = %q, want an empty value", got)
	}
	if got := asmFor(true); got != "avx512" {
		t.Errorf("asmFor(true) = %q, want avx512", got)
	}
}

// TestDaemonPublishesEventsToEveryStreamClient covers the reason the hub is the
// pool's event sink: two clients attached to one daemon both see every event.
func TestDaemonPublishesEventsToEveryStreamClient(t *testing.T) {
	svc, err := newService(daemonTestSettings(t, node.NewCapabilities(node.RoleStandalone)))
	if err != nil {
		t.Fatalf("newService() error = %v", err)
	}
	ln, addr := listenLoopback(t)
	srv, _ := startServer(ln, daemonHandler(svc.api, svc.stream))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = svc.Shutdown(ctx, srv)
	})

	first := dialWebSocket(t, addr)
	second := dialWebSocket(t, addr)
	readServerFrame(t, first)  // hello
	readServerFrame(t, second) // hello

	want := model.StatusEvent{TaskID: model.NewTaskID(), Progress: model.TaskRunning, Step: "ffmpeg", Percent: 7}
	svc.hub.Publish(want)
	for i, conn := range []net.Conn{first, second} {
		frame, err := readFrameWithin(conn, 10*time.Second)
		if err != nil {
			t.Fatalf("client %d: read: %v", i, err)
		}
		var envelope struct {
			Type  string             `json:"type"`
			Event *model.StatusEvent `json:"event"`
		}
		decodeInto(t, frame.payload, &envelope)
		if envelope.Event == nil || envelope.Event.TaskID != want.TaskID {
			t.Errorf("client %d got %s, want the published event", i, frame.payload)
		}
	}
}

// TestProcDefaultPriorityIsWired documents the priority the pipeline runs child
// processes with, so a change to the default is visible here.
func TestProcDefaultPriorityIsWired(t *testing.T) {
	svc, err := newService(daemonTestSettings(t, node.NewCapabilities(node.RoleStandalone)))
	if err != nil {
		t.Fatalf("newService() error = %v", err)
	}
	opts := pipelineOptions(svc.settings, svc.parts.Tasks)
	if opts.Priority != proc.DefaultPriority {
		t.Errorf("pipeline priority = %v, want %v", opts.Priority, proc.DefaultPriority)
	}
	if opts.LoadProfile == nil || opts.UpdateTask == nil {
		t.Error("pipeline queue hooks are not both wired")
	}
}
