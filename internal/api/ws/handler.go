package ws

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/KiritakeKumi/OKEGuiDX/internal/log"
)

// DefaultPath is the route the stream is mounted on. It sits under the
// /api/v1 prefix the REST API uses, so one reverse-proxy rule or auth
// middleware covers both.
const DefaultPath = "/api/v1/events"

// Options configures the stream endpoint.
type Options struct {
	// Hub is the event source. It is required.
	Hub *Hub
	// Path is the route the handler is mounted on. It is used in the hello
	// message and in log lines, and it documents the mount point for the
	// caller: http.Handle(stream.Path(), stream). Empty means DefaultPath.
	Path string
	// WriteWait bounds one message write. A subscriber that cannot absorb a
	// message within this window is disconnected, which is the second half of
	// the slow-consumer policy (the first half is the drop counter in the Hub).
	// Zero means DefaultWriteWait.
	WriteWait time.Duration
	// PingInterval is how often the server pings an idle connection. The pong
	// that comes back, like any other frame, is what extends the read deadline.
	// Zero means DefaultPingInterval.
	PingInterval time.Duration
	// PongWait is how long a connection may stay silent before the server
	// declares it dead. Zero means DefaultPongWait; a value not longer than the
	// ping interval is raised, because a deadline that expires before the next
	// ping would disconnect every idle client.
	PongWait time.Duration
}

// Default timing. The values match what the common Go WebSocket libraries use,
// which is a good enough fit for a progress stream: a connection that says
// nothing for a minute is gone.
const (
	// DefaultWriteWait bounds one message write.
	DefaultWriteWait = 10 * time.Second
	// DefaultPingInterval is the server ping period.
	DefaultPingInterval = 30 * time.Second
	// DefaultPongWait is the read deadline.
	DefaultPongWait = 60 * time.Second
)

// Stream serves the WebSocket progress endpoint.
type Stream struct {
	hub          *Hub
	path         string
	writeWait    time.Duration
	pingInterval time.Duration
	pongWait     time.Duration

	// started guards a once-per-process warning about a nonsensical timing
	// configuration. See validateTiming.
	started sync.Once
}

// New returns the endpoint. It panics when opts.Hub is nil, because a stream
// without a hub can never deliver an event.
func New(opts Options) *Stream {
	if opts.Hub == nil {
		panic("ws: Options.Hub is required")
	}
	s := &Stream{
		hub:          opts.Hub,
		path:         opts.Path,
		writeWait:    opts.WriteWait,
		pingInterval: opts.PingInterval,
		pongWait:     opts.PongWait,
	}
	if s.path == "" {
		s.path = DefaultPath
	}
	if s.writeWait <= 0 {
		s.writeWait = DefaultWriteWait
	}
	if s.pingInterval <= 0 {
		s.pingInterval = DefaultPingInterval
	}
	if s.pongWait <= 0 {
		s.pongWait = DefaultPongWait
	}
	return s
}

// Path returns the route the stream expects to be mounted on.
func (s *Stream) Path() string { return s.path }

// ServeHTTP upgrades the request and pumps messages until the client goes away
// or the hub is closed.
func (s *Stream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.started.Do(func() {
		if s.pongWait <= s.pingInterval {
			log.Warn("WebSocket 保活间隔不小于读超时，空闲连接会被误判为断开",
				"ping_interval", s.pingInterval, "pong_wait", s.pongWait)
		}
	})

	conn, err := Upgrade(w, r)
	if err != nil {
		s.refuse(w, r, err)
		return
	}
	sub := s.hub.Subscribe()
	if sub == nil {
		// The daemon is shutting down; tell the client to come back later.
		_ = conn.WriteClose(CloseGoingAway, "server is shutting down")
		_ = conn.Close()
		return
	}
	s.serve(conn, sub, r.Context())
}

// refuse answers a failed handshake. When the response has not been written yet
// the status from the error is used; anything else is a 500.
func (s *Stream) refuse(w http.ResponseWriter, r *http.Request, err error) {
	var herr *HandshakeError
	if !errors.As(err, &herr) {
		herr = &HandshakeError{Status: http.StatusInternalServerError, Reason: err.Error()}
	}
	log.Warn("拒绝 WebSocket 连接", "path", r.URL.Path, "remote", r.RemoteAddr,
		"status", herr.Status, "reason", herr.Reason)
	http.Error(w, herr.Reason, herr.Status)
}

// serve runs one connection: a reader goroutine that answers control frames,
// and a writer loop on the calling goroutine. The reader is the one that
// notices a dead peer, because a client that vanished only shows up as a read
// error once the read deadline fires.
func (s *Stream) serve(conn *Conn, sub *Subscriber, ctx context.Context) {
	// The deferred order matters: the close frame goes out before the socket
	// dies, and Unsubscribe runs after it so no event is lost to the hub.
	defer conn.Close()
	defer sub.Unsubscribe()
	defer func() {
		// The close frame is best-effort: a peer that stopped reading must not
		// keep this goroutine alive, so the write gets the same deadline as
		// every other write.
		_ = conn.SetWriteDeadline(time.Now().Add(s.writeWait))
		_ = conn.WriteClose(CloseNormalClosure, "")
	}()

	log.Debug("WebSocket 客户端已连接", "subscriber", sub.ID(), "remote", conn.RemoteAddr())
	defer log.Debug("WebSocket 客户端已断开", "subscriber", sub.ID(), "remote", conn.RemoteAddr())

	// The handshake is complete, so the client may start sending immediately;
	// the hello is the first thing it sees.
	if err := s.write(conn, Message{Type: MessageHello, Message: "progress stream " + s.path}); err != nil {
		return
	}

	peerGone := make(chan struct{})
	go func() {
		defer close(peerGone)
		s.readLoop(conn, ctx)
	}()

	ticker := time.NewTicker(s.pingInterval)
	defer ticker.Stop()
	// One deadline covers both the client's frames and its pongs; every frame
	// the reader accepts pushes it back.
	_ = conn.SetReadDeadline(time.Now().Add(s.pongWait))

	for {
		select {
		case msg, ok := <-sub.Send():
			if !ok {
				return
			}
			if dropped := sub.drainDropped(); dropped > 0 {
				msg.Dropped = dropped
				log.Warn("WebSocket 订阅者过慢，已丢弃事件", "subscriber", sub.ID(), "dropped", dropped)
			}
			if err := s.write(conn, msg); err != nil {
				return
			}
		case <-ticker.C:
			if err := s.writePing(conn); err != nil {
				return
			}
		case <-peerGone:
			return
		case <-ctx.Done():
			return
		}
	}
}

// write sends one envelope as a text frame.
func (s *Stream) write(conn *Conn, msg Message) error {
	data, err := encodeMessage(msg)
	if err != nil {
		log.Warn("无法序列化 WebSocket 消息", "err", err)
		return err
	}
	_ = conn.SetWriteDeadline(time.Now().Add(s.writeWait))
	if err := conn.WriteText(data); err != nil {
		// An ordinary disconnect is not worth a warning; anything else on the
		// write path means a client was cut off mid-stream.
		log.Debug("WebSocket 写入失败", "remote", conn.RemoteAddr(), "err", err)
		return err
	}
	return nil
}

// writePing sends a keepalive ping.
func (s *Stream) writePing(conn *Conn) error {
	_ = conn.SetWriteDeadline(time.Now().Add(s.writeWait))
	return conn.WritePing(nil)
}

// readLoop consumes frames until the peer closes or the connection fails. It
// answers pings and close frames, and it is what extends the read deadline: any
// frame proves the peer is alive.
func (s *Stream) readLoop(conn *Conn, ctx context.Context) {
	for {
		frame, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() == nil {
				s.closeForReadError(conn, err)
			}
			return
		}
		if ctx.Err() != nil {
			return
		}
		// Any frame proves the peer is alive, so the read deadline is extended
		// before the frame is classified; a pong then needs no further work.
		_ = conn.SetReadDeadline(time.Now().Add(s.pongWait))

		switch frame.Opcode {
		case OpPing:
			_ = conn.SetWriteDeadline(time.Now().Add(s.writeWait))
			if err := conn.WritePong(frame.Payload); err != nil {
				return
			}
		case OpPong:
		case OpClose:
			code, reason := parseClosePayload(frame.Payload)
			log.Debug("WebSocket 客户端请求关闭", "remote", conn.RemoteAddr(), "code", code, "reason", reason)
			_ = conn.WriteClose(CloseNormalClosure, "")
			return
		case OpText, OpBinary:
			// The stream is server-to-client, so a client message is accepted
			// and ignored. Text is still validated: a client that sends
			// invalid UTF-8 has a broken encoder.
			if frame.Opcode == OpText && !utf8.Valid(frame.Payload) {
				_ = conn.WriteClose(CloseInvalidPayload, "text message is not valid UTF-8")
				return
			}
		default:
			_ = conn.WriteClose(CloseUnsupportedData, "opcode is not accepted")
			return
		}
	}
}

// closeForReadError answers a protocol failure with the close code the error
// carries, and does nothing for an ordinary disconnect.
func (s *Stream) closeForReadError(conn *Conn, err error) {
	var perr *ProtocolError
	if !errors.As(err, &perr) {
		return
	}
	_ = conn.SetWriteDeadline(time.Now().Add(s.writeWait))
	_ = conn.WriteClose(perr.Code, perr.Reason)
}

// parseClosePayload splits a close frame payload into its code and reason. An
// empty payload is the "no code" form of §5.5.1. The payload has already been
// validated by ReadFrame.
func parseClosePayload(payload []byte) (uint16, string) {
	if len(payload) < 2 {
		return CloseNormalClosure, ""
	}
	code := uint16(payload[0])<<8 | uint16(payload[1])
	return code, string(payload[2:])
}
