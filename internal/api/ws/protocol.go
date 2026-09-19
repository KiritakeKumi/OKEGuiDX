// Package ws implements the server side of the WebSocket progress stream.
//
// It is a hand-written subset of RFC 6455 rather than a dependency, because the
// project forbids third-party modules and the stream needs little of the
// protocol:
//
//   - the opening handshake, including Sec-WebSocket-Accept;
//   - unmasked server-to-client text frames;
//   - masked client frames, including fragmented messages;
//   - the ping, pong and close control frames;
//   - the limits that keep a hostile client from making the server allocate.
//
// Not supported, and refused instead of ignored: permessage-deflate (the
// extension is never negotiated), and fragmented *outgoing* messages (every
// message the server sends fits one frame). Binary messages are accepted on the
// wire but carry no meaning for this stream.
//
// The package does not depend on internal/engine: a Hub receives
// model.StatusEvent values, and the daemon wires it up with
// WorkerManager.SetEventSink(hub.Publish).
package ws

import (
	"bufio"
	"crypto/sha1" //nolint:gosec // RFC 6455 §4.2.2 mandates SHA-1 for the handshake
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// Opcode is a WebSocket frame opcode (RFC 6455 §5.2).
type Opcode byte

// Frame opcodes. Only the ones this server understands are named.
const (
	// OpContinuation continues a fragmented message.
	OpContinuation Opcode = 0x0
	// OpText is a UTF-8 text message.
	OpText Opcode = 0x1
	// OpBinary is a binary message.
	OpBinary Opcode = 0x2
	// OpClose starts the closing handshake.
	OpClose Opcode = 0x8
	// OpPing is a liveness check.
	OpPing Opcode = 0x9
	// OpPong answers a ping.
	OpPong Opcode = 0xA
)

// String implements fmt.Stringer.
func (o Opcode) String() string {
	switch o {
	case OpContinuation:
		return "continuation"
	case OpText:
		return "text"
	case OpBinary:
		return "binary"
	case OpClose:
		return "close"
	case OpPing:
		return "ping"
	case OpPong:
		return "pong"
	default:
		return fmt.Sprintf("opcode(0x%X)", byte(o))
	}
}

// IsControl reports whether the opcode names a control frame (§5.5).
func (o Opcode) IsControl() bool { return o&0x8 != 0 }

// valid reports whether the opcode is defined by RFC 6455.
func (o Opcode) valid() bool {
	switch o {
	case OpContinuation, OpText, OpBinary, OpClose, OpPing, OpPong:
		return true
	default:
		return false
	}
}

// Close status codes used by this server. The full list is in RFC 6455 §7.4.1.
const (
	// CloseNormalClosure ends a connection that did its job.
	CloseNormalClosure uint16 = 1000
	// CloseGoingAway ends a connection because the server is shutting down.
	CloseGoingAway uint16 = 1001
	// CloseProtocolError reports a frame that violates RFC 6455.
	CloseProtocolError uint16 = 1002
	// CloseUnsupportedData reports a frame this server does not accept.
	CloseUnsupportedData uint16 = 1003
	// CloseInvalidPayload reports a text frame that is not valid UTF-8.
	CloseInvalidPayload uint16 = 1007
	// CloseMessageTooBig reports a frame or message above the size limit.
	CloseMessageTooBig uint16 = 1009
)

// Limits. The stream carries small JSON documents, so the limits are generous
// for the intended traffic and still small enough that a client cannot make the
// server buffer megabytes by sending one length header.
const (
	// MaxFramePayload is the largest frame payload the server accepts.
	MaxFramePayload = 1 << 20
	// MaxMessageBytes bounds a fragmented message after reassembly.
	MaxMessageBytes = 1 << 20
	// maxControlPayload is the §5.5 limit on control frame payloads.
	maxControlPayload = 125
	// finBit marks the last frame of a message.
	finBit = 0x80
)

// wsGUID is the fixed value RFC 6455 §4.2.2 appends to the client key.
const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// ErrClosing is returned by the write path once a close frame has been sent:
// §5.5.1 forbids sending data frames after that.
var ErrClosing = errors.New("ws: close frame already sent")

// Frame is one decoded frame. The payload is always unmasked: masking is a
// transport detail that never reaches the caller.
type Frame struct {
	// Fin is false when the frame is one fragment of a longer message.
	Fin bool
	// Opcode is the frame type.
	Opcode Opcode
	// Payload is the unmasked payload.
	Payload []byte
}

// ProtocolError reports a violation of RFC 6455 together with the close code
// the peer should be told about (§7.4.1). ReadFrame and ReadMessage return it;
// the handler answers with a close frame carrying Code, so a client can tell a
// malformed frame from a clean shutdown.
type ProtocolError struct {
	// Code is the close status code to send, one of the Close* constants.
	Code uint16
	// Reason is the diagnostic text; it is also what Error returns.
	Reason string
}

// Error implements error.
func (e *ProtocolError) Error() string { return e.Reason }

// protoErrorf builds a ProtocolError.
func protoErrorf(code uint16, format string, args ...any) *ProtocolError {
	return &ProtocolError{Code: code, Reason: fmt.Sprintf(format, args...)}
}

// HandshakeError reports why an upgrade request was refused. Status is the HTTP
// status the handler should answer with.
type HandshakeError struct {
	// Status is the HTTP status to answer with.
	Status int
	// Reason is the operator-facing explanation.
	Reason string
}

// Error implements error.
func (e *HandshakeError) Error() string {
	return fmt.Sprintf("ws: handshake refused (%d): %s", e.Status, e.Reason)
}

// IsUpgradeRequest reports whether r is a well-formed RFC 6455 upgrade request.
// It never panics on hostile header contents.
func IsUpgradeRequest(r *http.Request) bool {
	if r == nil || r.Method != http.MethodGet || !r.ProtoAtLeast(1, 1) {
		return false
	}
	return headerContainsToken(r.Header, "Connection", "upgrade") &&
		headerContainsToken(r.Header, "Upgrade", "websocket")
}

// Accept computes the Sec-WebSocket-Accept value for a client key (§4.2.2).
func Accept(key string) string {
	//nolint:gosec // SHA-1 is what the RFC requires; it is not a security choice.
	h := sha1.New()
	_, _ = h.Write([]byte(key + wsGUID))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// validKey reports whether key is a base64 encoded 16 byte nonce (§4.1).
func validKey(key string) bool {
	raw, err := base64.StdEncoding.DecodeString(key)
	return err == nil && len(raw) == 16
}

// headerContainsToken reports whether one comma-separated element of a header
// equals token, ignoring case.
func headerContainsToken(h http.Header, name, token string) bool {
	for _, value := range h.Values(name) {
		for part := range strings.SplitSeq(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}

// Upgrade completes the opening handshake and takes over the connection. The
// 101 response is written here; on failure nothing has been written and the
// caller answers with the status carried by *HandshakeError, or with 500 when
// the error is not one.
//
// The returned Conn is owned by the caller, which must close it.
func Upgrade(w http.ResponseWriter, r *http.Request) (*Conn, error) {
	if !IsUpgradeRequest(r) {
		return nil, &HandshakeError{Status: http.StatusUpgradeRequired, Reason: "此接口只接受 WebSocket 升级请求。"}
	}
	if r.Header.Get("Sec-WebSocket-Version") != "13" {
		return nil, &HandshakeError{Status: http.StatusUpgradeRequired, Reason: "只支持 WebSocket 版本 13。"}
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if !validKey(key) {
		return nil, &HandshakeError{Status: http.StatusBadRequest, Reason: "Sec-WebSocket-Key 缺失或不是 16 字节的 base64 值。"}
	}

	conn, rw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return nil, fmt.Errorf("ws: cannot take over the connection: %w", err)
	}
	// rw.Reader may already hold bytes the client pipelined behind the
	// handshake, so it becomes the connection's reader instead of a fresh one.
	_, err = fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\n"+
		"Upgrade: websocket\r\n"+
		"Connection: Upgrade\r\n"+
		"Sec-WebSocket-Accept: %s\r\n\r\n", Accept(key))
	if err == nil {
		err = rw.Flush()
	}
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("ws: cannot write the handshake response: %w", err)
	}
	return newConn(conn, rw, true), nil
}

// Conn is one hijacked WebSocket connection.
//
// Writes are serialized internally, so a control frame may be sent from a
// different goroutine than the one writing messages; reads must come from a
// single goroutine, because a WebSocket connection is a byte stream.
type Conn struct {
	conn net.Conn
	br   *bufio.Reader
	bw   *bufio.Writer

	// server reports which end of the connection this is. Masking is
	// directional (§5.1): client frames are masked, server frames are not. The
	// package only ever creates server connections; the flag lets the tests
	// drive the same codec from the other side instead of growing a second,
	// independently written decoder.
	server bool

	// fragOp and frag hold the fragmented message being reassembled.
	fragOp Opcode
	frag   []byte

	// closing is set once a close frame has been sent.
	closing atomic.Bool

	wmu sync.Mutex

	// scratch is reused by writeFrame, so the common small text message does
	// not allocate a header slice per event. Callers must hold wmu.
	scratch [14]byte
	// maskKey is the client-side masking key. Only test connections use it, so
	// a counter is enough to keep it varying between frames.
	maskKey [4]byte
}

// newConn wraps a hijacked connection. rw is the buffer pair the HTTP server
// handed over, so bytes it already read are not lost.
func newConn(conn net.Conn, rw *bufio.ReadWriter, server bool) *Conn {
	return &Conn{conn: conn, br: rw.Reader, bw: rw.Writer, server: server}
}

// RemoteAddr returns the peer address, for log messages.
func (c *Conn) RemoteAddr() net.Addr { return c.conn.RemoteAddr() }

// SetReadDeadline bounds the next read, including a read already in progress.
func (c *Conn) SetReadDeadline(t time.Time) error { return c.conn.SetReadDeadline(t) }

// SetWriteDeadline bounds the next write, including a write already in progress.
func (c *Conn) SetWriteDeadline(t time.Time) error { return c.conn.SetWriteDeadline(t) }

// Close closes the underlying connection without a closing handshake.
func (c *Conn) Close() error { return c.conn.Close() }

// ReadFrame returns the next frame. Masking is undone, so the payload is what
// the peer sent. A protocol violation is returned as a *ProtocolError carrying
// the close code the peer should be told about.
func (c *Conn) ReadFrame() (Frame, error) {
	var head [2]byte
	if _, err := io.ReadFull(c.br, head[:]); err != nil {
		return Frame{}, err
	}
	fin := head[0]&finBit != 0
	opcode := Opcode(head[0] & 0x0F)
	masked := head[1]&0x80 != 0
	length := uint64(head[1] & 0x7F)

	if head[0]&0x70 != 0 {
		return Frame{}, protoErrorf(CloseProtocolError, "ws: reserved frame bits are set")
	}
	if !opcode.valid() {
		return Frame{}, protoErrorf(CloseProtocolError, "ws: unknown opcode 0x%X", byte(opcode))
	}
	if opcode.IsControl() {
		if !fin {
			return Frame{}, protoErrorf(CloseProtocolError, "ws: %s frame is fragmented", opcode)
		}
		if length > maxControlPayload {
			return Frame{}, protoErrorf(CloseProtocolError, "ws: %s frame carries %d bytes, the limit is %d", opcode, length, maxControlPayload)
		}
	}

	switch length {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(c.br, ext[:]); err != nil {
			return Frame{}, err
		}
		length = uint64(binary.BigEndian.Uint16(ext[:]))
		if length < 126 {
			return Frame{}, protoErrorf(CloseProtocolError, "ws: frame length is not encoded in the minimal number of bytes")
		}
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(c.br, ext[:]); err != nil {
			return Frame{}, err
		}
		length = binary.BigEndian.Uint64(ext[:])
		if length>>63 != 0 {
			return Frame{}, protoErrorf(CloseProtocolError, "ws: frame length has the most significant bit set")
		}
		if length <= 0xFFFF {
			return Frame{}, protoErrorf(CloseProtocolError, "ws: frame length is not encoded in the minimal number of bytes")
		}
	}
	if length > MaxFramePayload {
		// The payload is not read: the header alone is the attack.
		return Frame{}, protoErrorf(CloseMessageTooBig, "ws: frame of %d bytes exceeds the %d byte limit", length, MaxFramePayload)
	}
	if masked != c.server {
		// §5.1: a client masks every frame it sends, a server none. A server
		// connection therefore requires the mask bit; a test connection acting
		// as a client requires its absence.
		if c.server {
			return Frame{}, protoErrorf(CloseProtocolError, "ws: client frame is not masked")
		}
		return Frame{}, protoErrorf(CloseProtocolError, "ws: server frame is masked")
	}

	payload, err := c.readPayload(masked, length)
	if err != nil {
		return Frame{}, err
	}
	if opcode == OpClose {
		// A close frame's payload is part of the protocol, so it is validated
		// before the caller sees it (§5.5.1, §7.4.1).
		if err := validateClosePayload(payload); err != nil {
			return Frame{}, err
		}
	}
	return Frame{Fin: fin, Opcode: opcode, Payload: payload}, nil
}

// readPayload reads a payload of the given length and removes the mask.
func (c *Conn) readPayload(masked bool, length uint64) ([]byte, error) {
	payload := make([]byte, length)
	if masked {
		var mask [4]byte
		if _, err := io.ReadFull(c.br, mask[:]); err != nil {
			return nil, err
		}
		if _, err := io.ReadFull(c.br, payload); err != nil {
			return nil, err
		}
		for i := range payload {
			payload[i] ^= mask[i&3]
		}
		return payload, nil
	}
	if _, err := io.ReadFull(c.br, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

// validateClosePayload checks the code and reason of a close frame. An empty
// payload is the "no status code" form and is valid; a one byte payload is not.
func validateClosePayload(payload []byte) error {
	if len(payload) == 0 {
		return nil
	}
	if len(payload) == 1 {
		return protoErrorf(CloseProtocolError, "ws: close frame carries a one byte payload")
	}
	code := uint16(payload[0])<<8 | uint16(payload[1])
	switch code {
	case 1004, 1005, 1006, 1015:
		// 1005/1006/1015 must never appear on the wire, and 1004 is reserved.
		return protoErrorf(CloseProtocolError, "ws: close code %d must not be sent on the wire", code)
	}
	if code < 1000 || code >= 5000 {
		return protoErrorf(CloseProtocolError, "ws: close code %d is out of range", code)
	}
	if !utf8.Valid(payload[2:]) {
		return protoErrorf(CloseInvalidPayload, "ws: close reason is not valid UTF-8")
	}
	return nil
}

// ReadMessage returns the next message, reassembling fragments. Control frames
// are returned as soon as they arrive, even in the middle of a fragmented
// message; the caller answers them and calls ReadMessage again to continue that
// message, whose assembly state stays in the Conn.
func (c *Conn) ReadMessage() (Frame, error) {
	for {
		frame, err := c.ReadFrame()
		if err != nil {
			return Frame{}, err
		}
		if frame.Opcode.IsControl() {
			return frame, nil
		}

		continuation := frame.Opcode == OpContinuation
		switch {
		case continuation && c.fragOp == 0:
			return Frame{}, protoErrorf(CloseProtocolError, "ws: continuation frame without a message in progress")
		case !continuation && c.fragOp != 0:
			return Frame{}, protoErrorf(CloseProtocolError, "ws: %s frame while a fragmented message is in progress", frame.Opcode)
		}
		if len(c.frag)+len(frame.Payload) > MaxMessageBytes {
			return Frame{}, protoErrorf(CloseMessageTooBig, "ws: message of more than %d bytes", MaxMessageBytes)
		}
		if !continuation && frame.Fin {
			// The common case: one frame carries the whole message, and its
			// payload already belongs to the caller.
			return frame, nil
		}
		if !continuation {
			c.fragOp = frame.Opcode
		}
		c.frag = append(c.frag, frame.Payload...)
		if !frame.Fin {
			continue
		}
		message := Frame{Fin: true, Opcode: c.fragOp, Payload: c.frag}
		c.frag, c.fragOp = nil, 0
		return message, nil
	}
}

// WriteText sends one text message in a single frame.
func (c *Conn) WriteText(payload []byte) error { return c.writeFrame(OpText, payload) }

// WritePing sends a ping frame. An empty payload is the normal case.
func (c *Conn) WritePing(payload []byte) error { return c.writeFrame(OpPing, payload) }

// WritePong answers a ping. The payload should be the one that was received.
func (c *Conn) WritePong(payload []byte) error { return c.writeFrame(OpPong, payload) }

// WriteClose sends a close frame with a status code and an optional reason, and
// marks the connection as closing. Calling it again is a no-op, so the reader
// and the writer can both reach it while shutting down.
func (c *Conn) WriteClose(code uint16, reason string) error {
	if c.closing.Swap(true) {
		return nil
	}
	payload := make([]byte, 0, 2+len(reason))
	var codeBytes [2]byte
	binary.BigEndian.PutUint16(codeBytes[:], code)
	payload = append(payload, codeBytes[:]...)
	payload = append(payload, reason...)
	// A control frame carries at most 125 bytes; keep the reason on a rune
	// boundary so the payload stays valid UTF-8.
	for len(payload) > maxControlPayload {
		_, size := utf8.DecodeLastRune(payload)
		payload = payload[:len(payload)-size]
	}
	return c.writeFrame(OpClose, payload)
}

// writeFrame writes one frame. Server connections never fragment and never
// mask; a test connection acting as a client masks, as §5.1 requires.
func (c *Conn) writeFrame(opcode Opcode, payload []byte) error {
	if opcode.IsControl() && len(payload) > maxControlPayload {
		return fmt.Errorf("ws: control frame payload of %d bytes exceeds %d", len(payload), maxControlPayload)
	}
	if !opcode.IsControl() && c.closing.Load() {
		return ErrClosing
	}

	c.wmu.Lock()
	defer c.wmu.Unlock()

	head := c.scratch[:0]
	head = append(head, byte(opcode)|finBit)
	maskBit := byte(0)
	if !c.server {
		maskBit = 0x80
	}
	switch n := len(payload); {
	case n < 126:
		head = append(head, maskBit|byte(n))
	case n <= 0xFFFF:
		//nolint:gosec // the case guard proves both bytes are in range
		head = append(head, maskBit|126, byte(n>>8), byte(n))
	default:
		head = append(head, maskBit|127)
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(n))
		head = append(head, ext[:]...)
	}

	var mask [4]byte
	if maskBit != 0 {
		// A client must mask, and RFC 6455 asks for an unpredictable key so a
		// proxy cannot be fooled. Only test connections reach this branch; the
		// server never masks, so varying the key is enough here.
		c.maskKey[0]++
		mask = c.maskKey
		head = append(head, mask[:]...)
	}

	if _, err := c.bw.Write(head); err != nil {
		return err
	}
	if maskBit != 0 {
		masked := make([]byte, len(payload))
		for i := range payload {
			masked[i] = payload[i] ^ mask[i&3]
		}
		if _, err := c.bw.Write(masked); err != nil {
			return err
		}
	} else if _, err := c.bw.Write(payload); err != nil {
		return err
	}
	return c.bw.Flush()
}
