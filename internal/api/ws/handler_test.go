package ws

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
)

// newServer mounts the stream on the same /api/v1 prefix the REST API uses and
// returns the running test server.
func newServer(t *testing.T, hub *Hub) *httptest.Server {
	t.Helper()
	stream := New(Options{
		Hub:          hub,
		Path:         "/api/v1/events",
		PingInterval: time.Hour, // tests drive the control frames themselves
		PongWait:     2 * time.Hour,
		WriteWait:    5 * time.Second,
	})
	mux := http.NewServeMux()
	mux.Handle(stream.Path(), stream)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// readMessage decodes one JSON envelope from a client connection.
func readMessage(t *testing.T, client *Conn) Message {
	t.Helper()
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	frame, err := client.ReadMessage()
	if err != nil {
		t.Fatalf("reading a message: %v", err)
	}
	if frame.Opcode != OpText {
		t.Fatalf("opcode = %v, want text", frame.Opcode)
	}
	var msg Message
	if err := json.Unmarshal(frame.Payload, &msg); err != nil {
		t.Fatalf("decoding %q: %v", frame.Payload, err)
	}
	return msg
}

func TestStreamHandshakeAndHello(t *testing.T) {
	t.Parallel()
	hub := NewHub(4)
	srv := newServer(t, hub)
	client := clientHandshake(t, strings.TrimPrefix(srv.URL, "http://"))

	hello := readMessage(t, client)
	if hello.Type != MessageHello {
		t.Fatalf("first message type = %q, want %q", hello.Type, MessageHello)
	}
	waitFor(t, "the hub to register the connection", func() bool { return hub.Subscribers() == 1 })
}

func TestStreamRejectsPlainHTTP(t *testing.T) {
	t.Parallel()
	hub := NewHub(1)
	srv := newServer(t, hub)

	resp, err := srv.Client().Get(srv.URL + "/api/v1/events")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUpgradeRequired {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUpgradeRequired)
	}
	if got := hub.Subscribers(); got != 0 {
		t.Fatalf("Subscribers() = %d after a refused request, want 0", got)
	}
}

func TestStreamDeliversPublishedEvents(t *testing.T) {
	t.Parallel()
	hub := NewHub(4)
	srv := newServer(t, hub)
	client := clientHandshake(t, strings.TrimPrefix(srv.URL, "http://"))
	if got := readMessage(t, client); got.Type != MessageHello {
		t.Fatalf("first message type = %q, want %q", got.Type, MessageHello)
	}
	waitFor(t, "the hub to register the connection", func() bool { return hub.Subscribers() == 1 })

	taskID := model.NewTaskID()
	hub.Publish(model.StatusEvent{TaskID: taskID, Progress: model.TaskRunning, Step: "x265", Percent: 10.5})
	hub.Publish(model.StatusEvent{TaskID: taskID, Progress: model.TaskFinished, Percent: 100})

	first := readMessage(t, client)
	if first.Type != MessageStatus || first.Event == nil {
		t.Fatalf("message = %+v, want a status event", first)
	}
	if first.Event.TaskID != taskID || first.Event.Step != "x265" || first.Event.Percent != 10.5 {
		t.Fatalf("event = %+v, want the published progress event", first.Event)
	}
	if first.Dropped != 0 {
		t.Fatalf("dropped = %d, want 0", first.Dropped)
	}
	second := readMessage(t, client)
	if second.Event == nil || second.Event.Progress != model.TaskFinished {
		t.Fatalf("second event = %+v, want the terminal event", second.Event)
	}
}

func TestStreamFansOutToEveryConnection(t *testing.T) {
	t.Parallel()
	hub := NewHub(4)
	srv := newServer(t, hub)
	addr := strings.TrimPrefix(srv.URL, "http://")

	const connections = 3
	clients := make([]*Conn, connections)
	for i := range clients {
		clients[i] = clientHandshake(t, addr)
		if got := readMessage(t, clients[i]); got.Type != MessageHello {
			t.Fatalf("client %d: first message = %q, want hello", i, got.Type)
		}
	}
	waitFor(t, "every connection to register", func() bool { return hub.Subscribers() == connections })

	hub.Publish(model.StatusEvent{Step: "x265", Percent: 7})
	for i, client := range clients {
		msg := readMessage(t, client)
		if msg.Event == nil || msg.Event.Percent != 7 {
			t.Fatalf("client %d: event = %+v, want the published event", i, msg.Event)
		}
	}
}

func TestStreamReportsDroppedEvents(t *testing.T) {
	t.Parallel()
	// A buffer of one, and the client does not read while the events are
	// published, so everything after the first event is dropped.
	hub := NewHub(1)
	srv := newServer(t, hub)
	client := clientHandshake(t, strings.TrimPrefix(srv.URL, "http://"))
	if got := readMessage(t, client); got.Type != MessageHello {
		t.Fatalf("first message = %q, want hello", got.Type)
	}
	waitFor(t, "the hub to register the connection", func() bool { return hub.Subscribers() == 1 })

	const published = 20
	for i := range published {
		hub.Publish(model.StatusEvent{Percent: float64(i)})
	}
	// The connection loop writes as fast as the socket accepts, so give the
	// drops a moment to accumulate. The subscriber queue holds one event, so
	// everything after the first publish is dropped while the client is not
	// reading.
	deadline := time.Now().Add(5 * time.Second)
	var reported uint64
	for time.Now().Before(deadline) {
		hub.Publish(model.StatusEvent{Percent: 1})
		reported = firstDrop(t, client)
		if reported > 0 {
			break
		}
	}
	if reported == 0 {
		t.Fatal("no message reported a dropped event count")
	}
	if reported > published {
		t.Fatalf("reported %d dropped events, but only %d were published", reported, published)
	}
}

// firstDrop reads messages until one carries a drop count, or the read
// deadline passes. It returns 0 when no such message arrived.
func firstDrop(t *testing.T, client *Conn) uint64 {
	t.Helper()
	for range 64 {
		_ = client.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		frame, err := client.ReadMessage()
		if err != nil {
			return 0
		}
		var msg Message
		if err := json.Unmarshal(frame.Payload, &msg); err != nil {
			t.Fatalf("decoding %q: %v", frame.Payload, err)
		}
		if msg.Dropped > 0 {
			return msg.Dropped
		}
	}
	return 0
}

func TestStreamAnswersPing(t *testing.T) {
	t.Parallel()
	hub := NewHub(4)
	srv := newServer(t, hub)
	client := clientHandshake(t, strings.TrimPrefix(srv.URL, "http://"))
	if got := readMessage(t, client); got.Type != MessageHello {
		t.Fatalf("first message = %q, want hello", got.Type)
	}

	if err := client.WritePing([]byte("are you there")); err != nil {
		t.Fatalf("WritePing() error = %v", err)
	}
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	frame, err := client.ReadMessage()
	if err != nil {
		t.Fatalf("reading the pong: %v", err)
	}
	if frame.Opcode != OpPong {
		t.Fatalf("opcode = %v, want pong", frame.Opcode)
	}
	if string(frame.Payload) != "are you there" {
		t.Fatalf("pong payload = %q, want the ping payload", frame.Payload)
	}
}

func TestStreamCloseHandshake(t *testing.T) {
	t.Parallel()
	hub := NewHub(4)
	srv := newServer(t, hub)
	client := clientHandshake(t, strings.TrimPrefix(srv.URL, "http://"))
	if got := readMessage(t, client); got.Type != MessageHello {
		t.Fatalf("first message = %q, want hello", got.Type)
	}

	if err := client.WriteClose(CloseNormalClosure, "done"); err != nil {
		t.Fatalf("WriteClose() error = %v", err)
	}
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	frame, err := client.ReadMessage()
	if err != nil {
		t.Fatalf("reading the close reply: %v", err)
	}
	if frame.Opcode != OpClose {
		t.Fatalf("opcode = %v, want close", frame.Opcode)
	}
	if code, _ := parseClosePayload(frame.Payload); code != CloseNormalClosure {
		t.Fatalf("close code = %d, want %d", code, CloseNormalClosure)
	}
	// The server must remove the subscriber once the connection is gone.
	waitFor(t, "the hub to drop the connection", func() bool { return hub.Subscribers() == 0 })
}

func TestStreamAnswersProtocolViolationWithClose(t *testing.T) {
	t.Parallel()
	hub := NewHub(4)
	srv := newServer(t, hub)
	client := clientHandshake(t, strings.TrimPrefix(srv.URL, "http://"))
	if got := readMessage(t, client); got.Type != MessageHello {
		t.Fatalf("first message = %q, want hello", got.Type)
	}

	// An unmasked client frame is a §5.1 violation.
	if err := writeRaw(client, rawFrame(true, OpText, false, 2, []byte("hi"))); err != nil {
		t.Fatalf("writeRaw() error = %v", err)
	}
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	frame, err := client.ReadMessage()
	if err != nil {
		t.Fatalf("reading the close frame: %v", err)
	}
	if frame.Opcode != OpClose {
		t.Fatalf("opcode = %v, want close", frame.Opcode)
	}
	if code, _ := parseClosePayload(frame.Payload); code != CloseProtocolError {
		t.Fatalf("close code = %d, want %d", code, CloseProtocolError)
	}
	waitFor(t, "the hub to drop the connection", func() bool { return hub.Subscribers() == 0 })
}

func TestStreamIgnoresClientMessages(t *testing.T) {
	t.Parallel()
	hub := NewHub(4)
	srv := newServer(t, hub)
	client := clientHandshake(t, strings.TrimPrefix(srv.URL, "http://"))
	if got := readMessage(t, client); got.Type != MessageHello {
		t.Fatalf("first message = %q, want hello", got.Type)
	}
	waitFor(t, "the hub to register the connection", func() bool { return hub.Subscribers() == 1 })

	if err := client.WriteText([]byte(`{"type":"ping"}`)); err != nil {
		t.Fatalf("WriteText() error = %v", err)
	}
	hub.Publish(model.StatusEvent{Percent: 3})
	msg := readMessage(t, client)
	if msg.Event == nil || msg.Event.Percent != 3 {
		t.Fatalf("message = %+v, want the event published after the client message", msg)
	}
}

func TestStreamRejectsInvalidUTF8Text(t *testing.T) {
	t.Parallel()
	hub := NewHub(4)
	srv := newServer(t, hub)
	client := clientHandshake(t, strings.TrimPrefix(srv.URL, "http://"))
	if got := readMessage(t, client); got.Type != MessageHello {
		t.Fatalf("first message = %q, want hello", got.Type)
	}

	// A masked text frame whose payload is not valid UTF-8.
	if err := writeRaw(client, rawFrame(true, OpText, true, 2, []byte{0xFF, 0xFE})); err != nil {
		t.Fatalf("writeRaw() error = %v", err)
	}
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	frame, err := client.ReadMessage()
	if err != nil {
		t.Fatalf("reading the close frame: %v", err)
	}
	if code, _ := parseClosePayload(frame.Payload); code != CloseInvalidPayload {
		t.Fatalf("close code = %d, want %d", code, CloseInvalidPayload)
	}
	waitFor(t, "the hub to drop the connection", func() bool { return hub.Subscribers() == 0 })
}

func TestStreamEndsWhenHubCloses(t *testing.T) {
	t.Parallel()
	hub := NewHub(4)
	srv := newServer(t, hub)
	client := clientHandshake(t, strings.TrimPrefix(srv.URL, "http://"))
	if got := readMessage(t, client); got.Type != MessageHello {
		t.Fatalf("first message = %q, want hello", got.Type)
	}
	waitFor(t, "the hub to register the connection", func() bool { return hub.Subscribers() == 1 })

	hub.Close()
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	frame, err := client.ReadMessage()
	if err != nil {
		t.Fatalf("reading the close frame: %v", err)
	}
	if frame.Opcode != OpClose {
		t.Fatalf("opcode = %v, want close", frame.Opcode)
	}
	if code, _ := parseClosePayload(frame.Payload); code != CloseNormalClosure {
		t.Fatalf("close code = %d, want %d", code, CloseNormalClosure)
	}
}

func TestStreamPingsIdleConnection(t *testing.T) {
	t.Parallel()
	hub := NewHub(4)
	stream := New(Options{
		Hub:          hub,
		Path:         "/api/v1/events",
		PingInterval: 20 * time.Millisecond,
		PongWait:     time.Second,
	})
	mux := http.NewServeMux()
	mux.Handle(stream.Path(), stream)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := clientHandshake(t, strings.TrimPrefix(srv.URL, "http://"))
	if got := readMessage(t, client); got.Type != MessageHello {
		t.Fatalf("first message = %q, want hello", got.Type)
	}
	// The ping interval is 20ms, so a ping must arrive well within this window.
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	frame, err := client.ReadMessage()
	if err != nil {
		t.Fatalf("reading the ping: %v", err)
	}
	if frame.Opcode != OpPing {
		t.Fatalf("opcode = %v, want ping", frame.Opcode)
	}
}

// TestStreamEndsOnDisconnect checks that a client that vanishes without a close
// handshake does not leave a subscriber behind. The server notices when a write
// fails, which is the earliest signal a TCP peer is gone.
func TestStreamEndsOnDisconnect(t *testing.T) {
	t.Parallel()
	hub := NewHub(4)
	srv := newServer(t, hub)
	client := clientHandshake(t, strings.TrimPrefix(srv.URL, "http://"))
	if got := readMessage(t, client); got.Type != MessageHello {
		t.Fatalf("first message = %q, want hello", got.Type)
	}
	waitFor(t, "the hub to register the connection", func() bool { return hub.Subscribers() == 1 })

	if err := client.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	// Publishing gives the server a reason to write, which fails and ends the
	// connection loop.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		hub.Publish(model.StatusEvent{Percent: 1})
		if hub.Subscribers() == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("Subscribers() = %d after the client vanished, want 0", hub.Subscribers())
}

// TestStreamBackpressureDisconnectsSlowClient pins the write-deadline half of
// the slow-consumer policy: a client that never reads is cut off rather than
// allowed to hold a goroutine forever.
func TestStreamBackpressureDisconnectsSlowClient(t *testing.T) {
	t.Parallel()
	hub := NewHub(1)
	stream := New(Options{
		Hub:          hub,
		Path:         "/api/v1/events",
		PingInterval: time.Hour,
		PongWait:     2 * time.Hour,
		WriteWait:    50 * time.Millisecond,
	})
	mux := http.NewServeMux()
	mux.Handle(stream.Path(), stream)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := clientHandshake(t, strings.TrimPrefix(srv.URL, "http://"))
	if got := readMessage(t, client); got.Type != MessageHello {
		t.Fatalf("first message = %q, want hello", got.Type)
	}
	waitFor(t, "the hub to register the connection", func() bool { return hub.Subscribers() == 1 })

	// The client stops reading. The kernel buffers a little, so the messages
	// have to be large enough to fill it before the write blocks.
	payload := strings.Repeat("x", 64<<10)
	var sent atomic.Int64
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		hub.Publish(model.StatusEvent{Step: payload, Percent: float64(sent.Add(1))})
		if hub.Subscribers() == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("Subscribers() = %d after a client stopped reading, want 0", hub.Subscribers())
}
