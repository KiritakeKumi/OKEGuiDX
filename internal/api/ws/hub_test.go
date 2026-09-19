package ws

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
)

// drain reads up to n messages from a subscriber without blocking.
func drain(t *testing.T, sub *Subscriber, n int) []Message {
	t.Helper()
	out := make([]Message, 0, n)
	for len(out) < n {
		select {
		case msg, ok := <-sub.Send():
			if !ok {
				return out
			}
			out = append(out, msg)
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out after %d of %d messages", len(out), n)
		}
	}
	return out
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestHubFansOutToEverySubscriber(t *testing.T) {
	t.Parallel()
	hub := NewHub(4)
	subs := make([]*Subscriber, 3)
	for i := range subs {
		subs[i] = hub.Subscribe()
		if subs[i] == nil {
			t.Fatalf("Subscribe() returned nil for subscriber %d", i)
		}
	}

	want := model.StatusEvent{TaskID: model.NewTaskID(), Progress: model.TaskRunning, Step: "x265", Percent: 42}
	hub.Publish(want)

	for i, sub := range subs {
		got := drain(t, sub, 1)[0]
		if got.Type != MessageStatus {
			t.Errorf("subscriber %d: type = %q, want %q", i, got.Type, MessageStatus)
		}
		if got.Event == nil || got.Event.Percent != 42 || got.Event.Step != "x265" {
			t.Errorf("subscriber %d: event = %+v, want the published event", i, got.Event)
		}
	}
}

func TestHubSubscribeAfterCloseReturnsNil(t *testing.T) {
	t.Parallel()
	hub := NewHub(1)
	hub.Close()
	if sub := hub.Subscribe(); sub != nil {
		t.Fatal("Subscribe() succeeded on a closed hub")
	}
	if got := hub.Subscribers(); got != 0 {
		t.Fatalf("Subscribers() = %d, want 0", got)
	}
}

func TestHubCloseEndsSubscribers(t *testing.T) {
	t.Parallel()
	hub := NewHub(1)
	sub := hub.Subscribe()
	hub.Close()

	if _, ok := <-sub.Send(); ok {
		t.Fatal("the send channel is still open after Close")
	}
	if got := hub.Subscribers(); got != 0 {
		t.Fatalf("Subscribers() = %d, want 0", got)
	}
	// Publishing to a closed hub must not panic or block.
	hub.Publish(model.StatusEvent{})
	hub.Close()
}

func TestHubUnsubscribeRemovesOnlyOneSubscriber(t *testing.T) {
	t.Parallel()
	hub := NewHub(4)
	first, second := hub.Subscribe(), hub.Subscribe()
	first.Unsubscribe()

	if got := hub.Subscribers(); got != 1 {
		t.Fatalf("Subscribers() = %d, want 1", got)
	}
	if _, ok := <-first.Send(); ok {
		t.Fatal("the unsubscribed channel is still open")
	}
	hub.Publish(model.StatusEvent{Percent: 1})
	got := drain(t, second, 1)[0]
	if got.Event == nil || got.Event.Percent != 1 {
		t.Fatalf("remaining subscriber got %+v, want the event", got.Event)
	}
	// Unsubscribe is idempotent.
	first.Unsubscribe()
}

func TestHubSlowSubscriberDoesNotBlockPublish(t *testing.T) {
	t.Parallel()
	// A buffer of one: the second and later events cannot be queued.
	hub := NewHub(1)
	slow := hub.Subscribe()
	fast := hub.Subscribe()

	const published = 100
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range published {
			hub.Publish(model.StatusEvent{Percent: float64(i)})
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish blocked on a slow subscriber")
	}

	// The fast subscriber drains as it goes; the slow one never reads, so it
	// must have lost events and counted them.
	waitFor(t, "the fast subscriber to receive something", func() bool { return len(fast.Send()) > 0 })
	if got := drain(t, slow, 1); len(got) != 1 {
		t.Fatalf("slow subscriber has %d queued messages, want 1", len(got))
	}
	waitFor(t, "the drop counter to rise", func() bool {
		slow.mu.Lock()
		defer slow.mu.Unlock()
		return slow.dropped > 0
	})

	dropped := slow.drainDropped()
	if dropped != published-1 {
		t.Fatalf("dropped = %d, want %d", dropped, published-1)
	}
	if again := slow.drainDropped(); again != 0 {
		t.Fatalf("drainDropped() returned %d on the second call, want 0", again)
	}
}

// TestHubDropsEventsWhenSubscriberIsSlow pins the discard policy: Publish never
// blocks, and a full queue is counted rather than awaited. The subscriber's
// channel is filled directly, so the test does not race the hub's own dispatch.
func TestHubDropsEventsWhenSubscriberIsSlow(t *testing.T) {
	t.Parallel()
	hub := NewHub(2)
	slow := hub.Subscribe()

	hub.Publish(model.StatusEvent{Percent: 1})
	hub.Publish(model.StatusEvent{Percent: 2})
	hub.Publish(model.StatusEvent{Percent: 3}) // the queue is full: dropped

	slow.mu.Lock()
	dropped := slow.dropped
	slow.mu.Unlock()
	if dropped != 1 {
		t.Fatalf("dropped = %d after three publishes into a two slot queue, want 1", dropped)
	}
	if got := drain(t, slow, 2); got[0].Event.Percent != 1 || got[1].Event.Percent != 2 {
		t.Fatalf("queued events = %v, %v; want the first two publishes", got[0].Event.Percent, got[1].Event.Percent)
	}
	if again := slow.drainDropped(); again != 1 {
		t.Fatalf("drainDropped() = %d, want 1", again)
	}
	if again := slow.drainDropped(); again != 0 {
		t.Fatalf("drainDropped() = %d on the second call, want 0", again)
	}
}

func TestNewDefaultsPathAndTiming(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		opts         Options
		wantPath     string
		wantWrite    time.Duration
		wantPing     time.Duration
		wantPongWait time.Duration
	}{
		{
			name:         "defaults",
			opts:         Options{Hub: NewHub(1)},
			wantPath:     DefaultPath,
			wantWrite:    DefaultWriteWait,
			wantPing:     DefaultPingInterval,
			wantPongWait: DefaultPongWait,
		},
		{
			name:         "explicit values are kept",
			opts:         Options{Hub: NewHub(1), Path: "/ws", WriteWait: time.Second, PingInterval: 2 * time.Second, PongWait: 3 * time.Second},
			wantPath:     "/ws",
			wantWrite:    time.Second,
			wantPing:     2 * time.Second,
			wantPongWait: 3 * time.Second,
		},
		{
			name:         "non positive values fall back",
			opts:         Options{Hub: NewHub(1), WriteWait: -time.Second, PingInterval: -1, PongWait: -1},
			wantPath:     DefaultPath,
			wantWrite:    DefaultWriteWait,
			wantPing:     DefaultPingInterval,
			wantPongWait: DefaultPongWait,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := New(tt.opts)
			if s.Path() != tt.wantPath {
				t.Errorf("Path() = %q, want %q", s.Path(), tt.wantPath)
			}
			if s.writeWait != tt.wantWrite {
				t.Errorf("writeWait = %v, want %v", s.writeWait, tt.wantWrite)
			}
			if s.pingInterval != tt.wantPing {
				t.Errorf("pingInterval = %v, want %v", s.pingInterval, tt.wantPing)
			}
			if s.pongWait != tt.wantPongWait {
				t.Errorf("pongWait = %v, want %v", s.pongWait, tt.wantPongWait)
			}
		})
	}
}

func TestNewRequiresHub(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Fatal("New() accepted a nil hub")
		}
	}()
	New(Options{})
}

func TestHubPublishMessage(t *testing.T) {
	t.Parallel()
	hub := NewHub(4)
	sub := hub.Subscribe()
	t.Cleanup(sub.Unsubscribe)

	hub.PublishMessage(Message{Type: MessageError, Message: "queue file is corrupt"})
	got := drain(t, sub, 1)[0]
	if got.Type != MessageError || got.Message != "queue file is corrupt" {
		t.Fatalf("message = %+v, want the published notice", got)
	}
}

func TestHubBufferFloor(t *testing.T) {
	t.Parallel()
	// A zero or negative buffer would drop every event, so NewHub raises it.
	for _, buffer := range []int{0, -1} {
		hub := NewHub(buffer)
		sub := hub.Subscribe()
		hub.Publish(model.StatusEvent{Percent: 1})
		if got := drain(t, sub, 1)[0]; got.Event == nil || got.Event.Percent != 1 {
			t.Fatalf("NewHub(%d): the first event was not queued", buffer)
		}
		sub.Unsubscribe()
	}
}

func TestHubConcurrentPublishAndSubscribe(t *testing.T) {
	t.Parallel()
	hub := NewHub(8)
	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Publishers.
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					hub.Publish(model.StatusEvent{TaskID: model.NewTaskID(), Percent: 1})
				}
			}
		}()
	}
	// Subscribers that come and go.
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					sub := hub.Subscribe()
					if sub == nil {
						return
					}
					select {
					case <-sub.Send():
					default:
					}
					sub.Unsubscribe()
				}
			}
		}()
	}

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
	if got := hub.Subscribers(); got != 0 {
		t.Fatalf("Subscribers() = %d after every subscriber left, want 0", got)
	}
}

func TestMessageEnvelopeJSON(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		msg  Message
		want string
	}{
		{
			name: "status",
			msg: Message{Type: MessageStatus, Event: &model.StatusEvent{
				TaskID:   model.TaskID("11111111-2222-3333-4444-555555555555"),
				Progress: model.TaskRunning,
				Step:     "x265",
				Percent:  12.5,
			}},
			want: `{"type":"status","event":{"task_id":"11111111-2222-3333-4444-555555555555","progress":"RUNNING","step":"x265","percent":12.5,"speed":"","bit_rate":"","time_remain_seconds":0,"frames_done":0,"frames_total":0}}`,
		},
		{
			name: "hello",
			msg:  Message{Type: MessageHello, Message: "/api/v1/events"},
			want: `{"type":"hello","message":"/api/v1/events"}`,
		},
		{
			name: "dropped count",
			msg:  Message{Type: MessageStatus, Event: &model.StatusEvent{Percent: 1}, Dropped: 7},
			want: `{"type":"status","event":{"task_id":"","progress":"WAITING","step":"","percent":1,"speed":"","bit_rate":"","time_remain_seconds":0,"frames_done":0,"frames_total":0},"dropped":7}`,
		},
		{
			name: "error",
			msg:  Message{Type: MessageError, Message: "bad"},
			want: `{"type":"error","message":"bad"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := encodeMessage(tt.msg)
			if err != nil {
				t.Fatalf("encodeMessage() error = %v", err)
			}
			if string(got) != tt.want {
				t.Fatalf("encodeMessage() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestMessageEnvelopeRoundTrip(t *testing.T) {
	t.Parallel()
	want := model.StatusEvent{
		TaskID:            model.NewTaskID(),
		Progress:          model.TaskError,
		Step:              "mkvmerge",
		Percent:           -1,
		Speed:             "12.34 fps",
		BitRate:           "1234.56 kb/s",
		TimeRemainSeconds: 65.5,
		FramesDone:        100,
		FramesTotal:       200,
		Error:             &model.ErrorInfo{Summary: "失败", Detail: "详情"},
	}
	data, err := encodeMessage(Message{Type: MessageStatus, Event: &want})
	if err != nil {
		t.Fatalf("encodeMessage() error = %v", err)
	}
	var got Message
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got.Event == nil {
		t.Fatal("round trip lost the event")
	}
	gotData, err := json.Marshal(got.Event)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	wantData, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if string(gotData) != string(wantData) {
		t.Fatalf("round trip = %s, want %s", gotData, wantData)
	}
}
