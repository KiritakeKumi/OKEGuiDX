package ws

import (
	"encoding/json"
	"sync"

	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
)

// Message is the envelope the stream sends. The progress stream carries only
// increments: the full queue is served by the REST API (GET /api/v1/tasks), so a
// client that wants a complete picture reads that and then applies events to it.
type Message struct {
	// Type is the discriminator: "status" for a task event, "hello" for the
	// first message of a connection and "error" for a stream-level problem.
	Type string `json:"type"`
	// Event is set when Type is "status".
	Event *model.StatusEvent `json:"event,omitempty"`
	// Message explains a "hello" or "error" message.
	Message string `json:"message,omitempty"`
	// Dropped counts events this subscriber lost since the previous message it
	// received. It is set on every message, so a client can tell a gap from a
	// quiet period. Events are dropped, never coalesced: each one is a
	// snapshot, and replaying only the newest would silently lose a task's
	// terminal state.
	Dropped uint64 `json:"dropped,omitempty"`
}

// Message type names.
const (
	// MessageHello is sent once, immediately after a connection is accepted.
	MessageHello = "hello"
	// MessageStatus carries a model.StatusEvent.
	MessageStatus = "status"
	// MessageError reports a problem with the stream itself.
	MessageError = "error"
)

// Subscriber is one connected client: an outbound queue and the counters the
// connection loop uses. A subscriber is created by Hub.Subscribe and must be
// cancelled when the connection ends.
type Subscriber struct {
	hub  *Hub
	id   uint64
	send chan Message
	once sync.Once

	mu      sync.Mutex
	dropped uint64
}

// Send returns the channel the connection loop reads messages from. It is
// closed by Unsubscribe, which is what lets the loop end.
func (s *Subscriber) Send() <-chan Message { return s.send }

// ID returns the connection's sequence number, for log messages.
func (s *Subscriber) ID() uint64 { return s.id }

// Unsubscribe removes the subscriber from the hub and closes its channel. It is
// safe to call more than once and from any goroutine.
func (s *Subscriber) Unsubscribe() {
	s.once.Do(func() {
		s.hub.remove(s)
		close(s.send)
	})
}

// Hub fans status events out to every subscriber.
//
// Its concurrency model is one mutex around a subscriber map plus one buffered
// channel per subscriber. Publish holds the lock only long enough to enqueue,
// so a slow or dead client can never stall the worker goroutine that produced
// the event; the price is that the enqueue is a non-blocking send, and an event
// that does not fit in the subscriber's buffer is dropped and counted.
type Hub struct {
	mu     sync.Mutex
	subs   map[uint64]*Subscriber
	nextID uint64
	closed bool
	// buffer is the outbound queue size per subscriber.
	buffer int
}

// NewHub returns a hub with the given per-subscriber buffer size. A buffer below
// one is raised to one, because a zero-length queue would drop every event.
func NewHub(buffer int) *Hub {
	if buffer < 1 {
		buffer = 1
	}
	return &Hub{subs: make(map[uint64]*Subscriber), buffer: buffer}
}

// Subscribe registers a connection and returns its subscriber. After the hub is
// closed it returns nil: the caller should answer with a going-away close.
func (h *Hub) Subscribe() *Subscriber {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil
	}
	h.nextID++
	sub := &Subscriber{hub: h, id: h.nextID, send: make(chan Message, h.buffer)}
	h.subs[sub.id] = sub
	return sub
}

// Publish sends an event to every subscriber. It never blocks: a subscriber
// whose queue is full loses the event and has its drop counter raised, which the
// connection loop reports to that client. It is safe to call from several
// goroutines, and it is what WorkerManager.SetEventSink is wired to.
func (h *Hub) Publish(ev model.StatusEvent) {
	msg := Message{Type: MessageStatus, Event: &ev}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	for _, sub := range h.subs {
		select {
		case sub.send <- msg:
		default:
			sub.countDrop()
		}
	}
}

// PublishMessage sends an arbitrary envelope, for tests and for server-side
// notices. It uses the same drop policy as Publish.
func (h *Hub) PublishMessage(msg Message) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	for _, sub := range h.subs {
		select {
		case sub.send <- msg:
		default:
			sub.countDrop()
		}
	}
}

// Subscribers reports how many connections are attached.
func (h *Hub) Subscribers() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// Close drops every subscriber. The connection loops see their channel close
// and send a close frame. The hub refuses new subscriptions afterwards.
func (h *Hub) Close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	subs := make([]*Subscriber, 0, len(h.subs))
	for _, sub := range h.subs {
		subs = append(subs, sub)
	}
	h.subs = make(map[uint64]*Subscriber)
	h.mu.Unlock()

	for _, sub := range subs {
		sub.Unsubscribe()
	}
}

// remove deletes one subscriber. It is called by Subscriber.Unsubscribe, which
// closes the channel afterwards.
func (h *Hub) remove(sub *Subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if current, ok := h.subs[sub.id]; ok && current == sub {
		delete(h.subs, sub.id)
	}
}

// countDrop raises the subscriber's drop counter.
func (s *Subscriber) countDrop() {
	s.mu.Lock()
	s.dropped++
	s.mu.Unlock()
}

// drainDropped reads and clears the drop counter, so a reported gap is not
// reported twice.
func (s *Subscriber) drainDropped() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	dropped := s.dropped
	s.dropped = 0
	return dropped
}

// encodeMessage marshals one envelope for the wire. A marshal failure is
// reported to the caller, which answers with an error message; the stream never
// sends a half-written message.
func encodeMessage(msg Message) ([]byte, error) {
	data, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return data, nil
}
