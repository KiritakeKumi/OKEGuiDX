package ws

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestAcceptMatchesRFC6455Example(t *testing.T) {
	t.Parallel()
	// The worked example of RFC 6455 §1.3.
	const key = "dGhlIHNhbXBsZSBub25jZQ=="
	const want = "s3pPLMBiTxaQ9kYGzzhZRbK+xOo="
	if got := Accept(key); got != want {
		t.Fatalf("Accept(%q) = %q, want %q", key, got, want)
	}
}

func TestIsUpgradeRequest(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*http.Request)
		want   bool
	}{
		{name: "well formed", mutate: func(*http.Request) {}, want: true},
		{
			name:   "connection header is a list",
			mutate: func(r *http.Request) { r.Header.Set("Connection", "keep-alive, Upgrade") },
			want:   true,
		},
		{
			name:   "case insensitive tokens",
			mutate: func(r *http.Request) { r.Header.Set("Upgrade", "WebSocket") },
			want:   true,
		},
		{name: "post is not an upgrade", mutate: func(r *http.Request) { r.Method = http.MethodPost }, want: false},
		{
			name: "http 1.0 is not an upgrade",
			mutate: func(r *http.Request) {
				r.Proto, r.ProtoMajor, r.ProtoMinor = "HTTP/1.0", 1, 0
			},
			want: false,
		},
		{name: "missing upgrade header", mutate: func(r *http.Request) { r.Header.Del("Upgrade") }, want: false},
		{name: "missing connection header", mutate: func(r *http.Request) { r.Header.Del("Connection") }, want: false},
		{name: "wrong upgrade token", mutate: func(r *http.Request) { r.Header.Set("Upgrade", "h2c") }, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil)
			r.Header.Set("Connection", "Upgrade")
			r.Header.Set("Upgrade", "websocket")
			tt.mutate(r)
			if got := IsUpgradeRequest(r); got != tt.want {
				t.Fatalf("IsUpgradeRequest() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		key  string
		want bool
	}{
		{name: "16 bytes", key: base64.StdEncoding.EncodeToString(make([]byte, 16)), want: true},
		{name: "empty", key: "", want: false},
		{name: "not base64", key: "!!!!", want: false},
		{name: "too short", key: base64.StdEncoding.EncodeToString(make([]byte, 8)), want: false},
		{name: "too long", key: base64.StdEncoding.EncodeToString(make([]byte, 32)), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := validKey(tt.key); got != tt.want {
				t.Fatalf("validKey(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

func TestUpgradeRejectsBadRequests(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		mutate     func(*http.Request)
		wantStatus int
	}{
		{
			name:       "not an upgrade",
			mutate:     func(r *http.Request) { r.Header.Del("Upgrade") },
			wantStatus: http.StatusUpgradeRequired,
		},
		{
			name:       "wrong version",
			mutate:     func(r *http.Request) { r.Header.Set("Sec-WebSocket-Version", "8") },
			wantStatus: http.StatusUpgradeRequired,
		},
		{
			name:       "bad key",
			mutate:     func(r *http.Request) { r.Header.Set("Sec-WebSocket-Key", "not-a-nonce") },
			wantStatus: http.StatusBadRequest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := upgradeRequest()
			tt.mutate(r)

			_, err := Upgrade(httptest.NewRecorder(), r)
			var herr *HandshakeError
			if !errors.As(err, &herr) {
				t.Fatalf("Upgrade() error = %v, want *HandshakeError", err)
			}
			if herr.Status != tt.wantStatus {
				t.Fatalf("HandshakeError.Status = %d, want %d", herr.Status, tt.wantStatus)
			}
		})
	}
}

// upgradeRequest builds a well-formed handshake request that the table above
// then breaks.
func upgradeRequest() *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil)
	r.Header.Set("Connection", "Upgrade")
	r.Header.Set("Upgrade", "websocket")
	r.Header.Set("Sec-WebSocket-Version", "13")
	r.Header.Set("Sec-WebSocket-Key", base64.StdEncoding.EncodeToString([]byte("0123456789abcdef")))
	return r
}

func TestReadFrame(t *testing.T) {
	t.Parallel()
	payload16 := bytes.Repeat([]byte("a"), 300)
	payload64 := bytes.Repeat([]byte("b"), 70000)

	tests := []struct {
		name    string
		data    []byte
		want    Frame
		wantErr error
	}{
		{
			name: "small masked text",
			data: rawFrame(true, OpText, true, 5, []byte("hello")),
			want: Frame{Fin: true, Opcode: OpText, Payload: []byte("hello")},
		},
		{
			name: "empty masked ping",
			data: rawFrame(true, OpPing, true, 0, nil),
			want: Frame{Fin: true, Opcode: OpPing, Payload: []byte{}},
		},
		{
			name: "binary payload is preserved",
			data: rawFrame(true, OpBinary, true, 4, []byte{0x00, 0xFF, 0x10, 0x20}),
			want: Frame{Fin: true, Opcode: OpBinary, Payload: []byte{0x00, 0xFF, 0x10, 0x20}},
		},
		{
			name: "16 bit length",
			data: rawFrame(true, OpBinary, true, len(payload16), payload16),
			want: Frame{Fin: true, Opcode: OpBinary, Payload: payload16},
		},
		{
			name: "64 bit length",
			data: rawFrame(true, OpBinary, true, len(payload64), payload64),
			want: Frame{Fin: true, Opcode: OpBinary, Payload: payload64},
		},
		{
			name: "non final fragment",
			data: rawFrame(false, OpText, true, 2, []byte("ab")),
			want: Frame{Fin: false, Opcode: OpText, Payload: []byte("ab")},
		},
		{
			name:    "unmasked client frame",
			data:    rawFrame(true, OpText, false, 2, []byte("hi")),
			wantErr: protocolViolation(CloseProtocolError),
		},
		{
			name:    "reserved bits",
			data:    append([]byte{0xC1, 0x80, 0, 0, 0, 0}, []byte("hi")...),
			wantErr: protocolViolation(CloseProtocolError),
		},
		{
			name:    "unknown opcode",
			data:    rawFrame(true, Opcode(0x3), true, 0, nil),
			wantErr: protocolViolation(CloseProtocolError),
		},
		{
			name:    "fragmented control frame",
			data:    rawFrame(false, OpPing, true, 0, nil),
			wantErr: protocolViolation(CloseProtocolError),
		},
		{
			name:    "control frame too large",
			data:    rawFrame(true, OpPing, true, 126, bytes.Repeat([]byte("x"), 126)),
			wantErr: protocolViolation(CloseProtocolError),
		},
		{
			name:    "length above the limit",
			data:    rawFrame(true, OpBinary, true, MaxFramePayload+1, nil),
			wantErr: protocolViolation(CloseMessageTooBig),
		},
		{
			name:    "negative 64 bit length",
			data:    highBitLengthFrame(),
			wantErr: protocolViolation(CloseProtocolError),
		},
		{
			name:    "non minimal 16 bit length",
			data:    nonMinimalLengthFrame(),
			wantErr: protocolViolation(CloseProtocolError),
		},
		{
			name:    "one byte close payload",
			data:    rawFrame(true, OpClose, true, 1, []byte{0x03}),
			wantErr: protocolViolation(CloseProtocolError),
		},
		{
			name:    "reserved close code",
			data:    rawFrame(true, OpClose, true, 2, []byte{0x03, 0xEC}), // 1004
			wantErr: protocolViolation(CloseProtocolError),
		},
		{
			name:    "close code 1005 on the wire",
			data:    rawFrame(true, OpClose, true, 2, []byte{0x03, 0xED}), // 1005
			wantErr: protocolViolation(CloseProtocolError),
		},
		{
			name:    "close code out of range",
			data:    rawFrame(true, OpClose, true, 2, []byte{0x00, 0x01}), // 1
			wantErr: protocolViolation(CloseProtocolError),
		},
		{
			name:    "close reason is not UTF-8",
			data:    rawFrame(true, OpClose, true, 4, []byte{0x03, 0xE8, 0xFF, 0xFE}),
			wantErr: protocolViolation(CloseInvalidPayload),
		},
		{
			name: "valid close with reason",
			data: rawFrame(true, OpClose, true, 5, append([]byte{0x03, 0xE8}, "bye"...)),
			want: Frame{Fin: true, Opcode: OpClose, Payload: append([]byte{0x03, 0xE8}, "bye"...)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := readRawFrame(t, tt.data)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("ReadFrame() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ReadFrame() error = %v", err)
			}
			if got.Fin != tt.want.Fin || got.Opcode != tt.want.Opcode || !bytes.Equal(got.Payload, tt.want.Payload) {
				t.Fatalf("ReadFrame() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestReadFrameTruncatedInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		data []byte
	}{
		{name: "header only", data: []byte{0x81}},
		{name: "length without payload", data: rawFrame(true, OpText, true, 5, nil)},
		{name: "payload shorter than declared", data: rawFrame(true, OpText, true, 5, []byte("ab"))},
		{name: "mask key truncated", data: []byte{0x81, 0x85, 0x11}},
		{name: "extended length truncated", data: []byte{0x82, 0xFE, 0x00}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := readRawFrame(t, tt.data); err == nil {
				t.Fatal("ReadFrame() accepted a truncated frame")
			}
		})
	}
}

func TestReadMessageReassemblesFragments(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		frames  [][]byte
		want    string
		wantErr error
	}{
		{
			name:   "single frame",
			frames: [][]byte{rawFrame(true, OpText, true, 5, []byte("hello"))},
			want:   "hello",
		},
		{
			name: "two fragments",
			frames: [][]byte{
				rawFrame(false, OpText, true, 3, []byte("hel")),
				rawFrame(true, OpContinuation, true, 2, []byte("lo")),
			},
			want: "hello",
		},
		{
			name: "three fragments",
			frames: [][]byte{
				rawFrame(false, OpText, true, 1, []byte("h")),
				rawFrame(false, OpContinuation, true, 1, []byte("e")),
				rawFrame(true, OpContinuation, true, 3, []byte("llo")),
			},
			want: "hello",
		},
		{
			name:    "continuation without a start",
			frames:  [][]byte{rawFrame(true, OpContinuation, true, 1, []byte("x"))},
			wantErr: protocolViolation(CloseProtocolError),
		},
		{
			name: "new text frame while fragmenting",
			frames: [][]byte{
				rawFrame(false, OpText, true, 1, []byte("h")),
				rawFrame(true, OpText, true, 1, []byte("e")),
			},
			wantErr: protocolViolation(CloseProtocolError),
		},
		{
			name: "binary message",
			frames: [][]byte{
				rawFrame(false, OpBinary, true, 2, []byte{0x01, 0x02}),
				rawFrame(true, OpContinuation, true, 2, []byte{0x03, 0x04}),
			},
			want: "\x01\x02\x03\x04",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server, client := pair(t)
			go func() {
				for _, frame := range tt.frames {
					if err := writeRaw(client, frame); err != nil {
						return
					}
				}
			}()
			// The writes above block until the server reads, so the read needs
			// a deadline of its own; a stalled test must not hang the suite.
			_ = server.SetReadDeadline(time.Now().Add(5 * time.Second))

			got, err := server.ReadMessage()
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("ReadMessage() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ReadMessage() error = %v", err)
			}
			if string(got.Payload) != tt.want {
				t.Fatalf("ReadMessage() payload = %q, want %q", got.Payload, tt.want)
			}
		})
	}
}

func TestReadMessageReturnsControlFramesMidMessage(t *testing.T) {
	t.Parallel()
	server, client := pair(t)
	frames := [][]byte{
		rawFrame(false, OpText, true, 3, []byte("hel")),
		rawFrame(true, OpPing, true, 4, []byte("ping")),
		rawFrame(true, OpContinuation, true, 2, []byte("lo")),
	}
	go func() {
		for _, frame := range frames {
			if err := writeRaw(client, frame); err != nil {
				return
			}
		}
	}()
	_ = server.SetReadDeadline(time.Now().Add(5 * time.Second))

	first, err := server.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage() error = %v", err)
	}
	if first.Opcode != OpPing || string(first.Payload) != "ping" {
		t.Fatalf("control frame = %+v, want ping", first)
	}
	second, err := server.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage() error = %v", err)
	}
	if string(second.Payload) != "hello" {
		t.Fatalf("reassembled payload = %q, want hello", second.Payload)
	}
}

func TestReadMessageRejectsOversizedMessage(t *testing.T) {
	t.Parallel()
	server, client := pair(t)
	// Four fragments of 400 KiB each stay below the per-frame limit but exceed
	// the per-message limit once reassembled.
	chunk := bytes.Repeat([]byte("x"), 400<<10)
	go func() {
		for i := range 4 {
			opcode := OpContinuation
			if i == 0 {
				opcode = OpText
			}
			if err := writeRaw(client, rawFrame(i == 3, opcode, true, len(chunk), chunk)); err != nil {
				return
			}
		}
	}()
	_ = server.SetReadDeadline(time.Now().Add(5 * time.Second))

	if _, err := server.ReadMessage(); !errors.Is(err, protocolViolation(CloseMessageTooBig)) {
		t.Fatalf("ReadMessage() error = %v, want message-too-big", err)
	}
}

func TestWriteFrameEncoding(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		opcode  Opcode
		payload []byte
		want    []byte
	}{
		{name: "short text", opcode: OpText, payload: []byte("hi"), want: []byte{0x81, 0x02, 'h', 'i'}},
		{name: "empty close", opcode: OpClose, payload: nil, want: []byte{0x88, 0x00}},
		{
			name:    "16 bit length",
			opcode:  OpBinary,
			payload: bytes.Repeat([]byte("a"), 126),
			want:    append([]byte{0x82, 126, 0x00, 126}, bytes.Repeat([]byte("a"), 126)...),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server, client := pair(t)
			if err := server.writeFrame(tt.opcode, tt.payload); err != nil {
				t.Fatalf("writeFrame() error = %v", err)
			}
			raw := make([]byte, len(tt.want))
			if _, err := io.ReadFull(client.br, raw); err != nil {
				t.Fatalf("reading the encoded frame: %v", err)
			}
			if !bytes.Equal(raw, tt.want) {
				t.Fatalf("encoded frame = %x, want %x", raw, tt.want)
			}
		})
	}
}

func TestWriteFrameRejectsOversizedControlPayload(t *testing.T) {
	t.Parallel()
	server, _ := pair(t)
	if err := server.writeFrame(OpPing, bytes.Repeat([]byte("x"), 126)); err == nil {
		t.Fatal("writeFrame() accepted a 126 byte ping payload")
	}
}

func TestWriteCloseTruncatesOnRuneBoundary(t *testing.T) {
	t.Parallel()
	server, client := pair(t)
	if err := server.WriteClose(CloseProtocolError, strings.Repeat("な", 100)); err != nil {
		t.Fatalf("WriteClose() error = %v", err)
	}
	frame, err := client.ReadMessage()
	if err != nil {
		t.Fatalf("client ReadMessage() error = %v", err)
	}
	if frame.Opcode != OpClose {
		t.Fatalf("opcode = %v, want close", frame.Opcode)
	}
	if len(frame.Payload) > maxControlPayload {
		t.Fatalf("close payload is %d bytes, the limit is %d", len(frame.Payload), maxControlPayload)
	}
	if code, _ := parseClosePayload(frame.Payload); code != CloseProtocolError {
		t.Fatalf("close code = %d, want %d", code, CloseProtocolError)
	}
	if !utf8.Valid(frame.Payload[2:]) {
		t.Fatal("close reason was truncated in the middle of a rune")
	}
}

func TestWriteCloseIsIdempotent(t *testing.T) {
	t.Parallel()
	server, client := pair(t)
	if err := server.WriteClose(CloseNormalClosure, "bye"); err != nil {
		t.Fatalf("first WriteClose() error = %v", err)
	}
	if err := server.WriteClose(CloseGoingAway, "again"); err != nil {
		t.Fatalf("second WriteClose() error = %v", err)
	}
	if _, err := client.ReadMessage(); err != nil {
		t.Fatalf("reading the first close frame: %v", err)
	}
	// The second call must not have written a second frame.
	_ = client.conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	if _, err := client.ReadMessage(); err == nil {
		t.Fatal("WriteClose() wrote a second close frame")
	}
}

func TestWriteTextAfterCloseIsRefused(t *testing.T) {
	t.Parallel()
	server, _ := pair(t)
	if err := server.WriteClose(CloseNormalClosure, ""); err != nil {
		t.Fatalf("WriteClose() error = %v", err)
	}
	if err := server.WriteText([]byte("late")); !errors.Is(err, ErrClosing) {
		t.Fatalf("WriteText() error = %v, want ErrClosing", err)
	}
}

func TestParseClosePayload(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		payload    []byte
		wantCode   uint16
		wantReason string
	}{
		{name: "empty", payload: nil, wantCode: CloseNormalClosure, wantReason: ""},
		{name: "code only", payload: []byte{0x03, 0xE8}, wantCode: 1000, wantReason: ""},
		{name: "code and reason", payload: append([]byte{0x03, 0xEA}, "bye"...), wantCode: 1002, wantReason: "bye"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			code, reason := parseClosePayload(tt.payload)
			if code != tt.wantCode || reason != tt.wantReason {
				t.Fatalf("parseClosePayload() = (%d, %q), want (%d, %q)", code, reason, tt.wantCode, tt.wantReason)
			}
		})
	}
}

func TestOpcodeClassification(t *testing.T) {
	t.Parallel()
	tests := []struct {
		opcode  Opcode
		control bool
		valid   bool
	}{
		{opcode: OpContinuation, control: false, valid: true},
		{opcode: OpText, control: false, valid: true},
		{opcode: OpBinary, control: false, valid: true},
		{opcode: OpClose, control: true, valid: true},
		{opcode: OpPing, control: true, valid: true},
		{opcode: OpPong, control: true, valid: true},
		{opcode: Opcode(0x3), control: false, valid: false},
		{opcode: Opcode(0xB), control: true, valid: false},
	}
	for _, tt := range tests {
		t.Run(tt.opcode.String(), func(t *testing.T) {
			t.Parallel()
			if got := tt.opcode.IsControl(); got != tt.control {
				t.Fatalf("IsControl() = %v, want %v", got, tt.control)
			}
			if got := tt.opcode.valid(); got != tt.valid {
				t.Fatalf("valid() = %v, want %v", got, tt.valid)
			}
		})
	}
}

// protocolViolation returns an error that errors.Is matches against a
// ProtocolError carrying the given close code.
func protocolViolation(code uint16) error {
	return &ProtocolError{Code: code}
}

// nonMinimalLengthFrame encodes a 5 byte payload with the 16-bit length form,
// which §5.2 forbids: the shortest form must be used.
func nonMinimalLengthFrame() []byte {
	frame := rawFrame(true, OpBinary, true, 5, []byte("hello"))
	out := append([]byte{}, frame[:2]...)
	out[1] = 0xFE // masked, 16-bit length follows
	out = append(out, 0x00, 0x05)
	return append(out, frame[2:]...)
}

// highBitLengthFrame encodes a 64-bit length with the most significant bit set,
// which §5.2 forbids.
func highBitLengthFrame() []byte {
	return []byte{0x82, 0xFF, 0x80, 0, 0, 0, 0, 0, 0, 0, 0x11, 0x22, 0x33, 0x44}
}

// Is reports a match when the close codes are equal, so a test can compare
// against a bare code without knowing the message.
func (e *ProtocolError) Is(target error) bool {
	var other *ProtocolError
	if errors.As(target, &other) {
		return e.Code == other.Code
	}
	return false
}
