package ws

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"
)

// pair returns a connected server/client pair of Conns, both running the
// package's own codec. server is the end under test, client the peer. The pair
// is a real TCP connection rather than net.Pipe, so a write that the peer has
// not read yet does not deadlock the test goroutine.
func pair(t *testing.T) (server, client *Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening for a test pair: %v", err)
	}
	defer func() { _ = ln.Close() }()

	type accepted struct {
		conn net.Conn
		err  error
	}
	ch := make(chan accepted, 1)
	go func() {
		conn, err := ln.Accept()
		ch <- accepted{conn: conn, err: err}
	}()
	clientNet, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dialing the test pair: %v", err)
	}
	got := <-ch
	if got.err != nil {
		t.Fatalf("accepting the test pair: %v", got.err)
	}
	serverNet := got.conn
	t.Cleanup(func() {
		_ = serverNet.Close()
		_ = clientNet.Close()
	})
	// Deadlines keep a stalled test from hanging the whole suite; they are far
	// longer than anything the tests wait for on purpose.
	deadline := time.Now().Add(30 * time.Second)
	_ = serverNet.SetDeadline(deadline)
	_ = clientNet.SetDeadline(deadline)

	return newConn(serverNet, bufio.NewReadWriter(bufio.NewReader(serverNet), bufio.NewWriter(serverNet)), true),
		newConn(clientNet, bufio.NewReadWriter(bufio.NewReader(clientNet), bufio.NewWriter(clientNet)), false)
}

// rawFrame builds a frame on the wire, bypassing the encoder so a test can
// produce the malformed input the encoder refuses to write. A negative length
// emits the length header with no payload.
func rawFrame(fin bool, opcode Opcode, masked bool, length int, payload []byte) []byte {
	var b []byte
	first := byte(opcode)
	if fin {
		first |= finBit
	}
	b = append(b, first)

	maskBit := byte(0)
	if masked {
		maskBit = 0x80
	}
	// The mask bit is chosen from the payload length, so the shortest frame
	// always uses the 7-bit form.
	switch {
	case length >= 0 && length < 126:
		b = append(b, maskBit|byte(length))
	case length < 0:
		b = append(b, maskBit|126, byte(uint16(length)>>8), byte(uint16(length)))
	case length <= 0xFFFF:
		b = append(b, maskBit|126, byte(length>>8), byte(length))
	default:
		b = append(b, maskBit|127)
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(length))
		b = append(b, ext[:]...)
	}

	var mask [4]byte
	if masked {
		mask = [4]byte{0x11, 0x22, 0x33, 0x44}
		b = append(b, mask[:]...)
	}
	if length < 0 {
		return b
	}
	if masked {
		maskedPayload := make([]byte, len(payload))
		for i := range payload {
			maskedPayload[i] = payload[i] ^ mask[i&3]
		}
		return append(b, maskedPayload...)
	}
	return append(b, payload...)
}

// readRawFrame decodes one frame straight off a byte slice as a server would,
// so the malformed input cases do not need a live connection.
func readRawFrame(t *testing.T, data []byte) (Frame, error) {
	t.Helper()
	r := bytes.NewReader(data)
	conn := newConn(&pipeNetConn{Reader: r},
		bufio.NewReadWriter(bufio.NewReader(r), bufio.NewWriter(io.Discard)), true)
	return conn.ReadFrame()
}

// writeRaw writes a frame the encoder refuses to produce, for the malformed
// input cases. It bypasses writeFrame on purpose.
func writeRaw(c *Conn, data []byte) error {
	if _, err := c.bw.Write(data); err != nil {
		return err
	}
	return c.bw.Flush()
}

// pipeNetConn adapts a reader to net.Conn for the decode-only cases.
type pipeNetConn struct{ io.Reader }

// Write implements net.Conn.
func (*pipeNetConn) Write(p []byte) (int, error) { return len(p), nil }

// Close implements net.Conn.
func (*pipeNetConn) Close() error { return nil }

// LocalAddr implements net.Conn.
func (*pipeNetConn) LocalAddr() net.Addr { return pipeAddr("local") }

// RemoteAddr implements net.Conn.
func (*pipeNetConn) RemoteAddr() net.Addr { return pipeAddr("remote") }

// SetDeadline implements net.Conn.
func (*pipeNetConn) SetDeadline(time.Time) error { return nil }

// SetReadDeadline implements net.Conn.
func (*pipeNetConn) SetReadDeadline(time.Time) error { return nil }

// SetWriteDeadline implements net.Conn.
func (*pipeNetConn) SetWriteDeadline(time.Time) error { return nil }

type pipeAddr string

// Network implements net.Addr.
func (a pipeAddr) Network() string { return "pipe" }

// String implements net.Addr.
func (a pipeAddr) String() string { return string(a) }

// clientNonce is the fixed Sec-WebSocket-Key the test client sends, so the
// expected accept value is known.
const clientNonce = "MDEyMzQ1Njc4OWFiY2RlZg=="

// clientHandshake performs the opening handshake against a live server and
// returns the connection, now speaking WebSocket. It asserts the accept value,
// which is the only part of the handshake a client can verify.
func clientHandshake(t testing.TB, addr string) *Conn {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dialing the test server: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	u := &url.URL{Scheme: "ws", Host: addr, Path: "/api/v1/events"}
	req := &http.Request{
		Method:     http.MethodGet,
		URL:        u,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Host:       addr,
		Header: http.Header{
			"Upgrade":                  {"websocket"},
			"Connection":               {"Upgrade"},
			"Sec-Websocket-Version":    {"13"},
			"Sec-Websocket-Key":        {clientNonce},
			"Sec-Websocket-Extensions": {"permessage-deflate"},
		},
	}
	if err := req.Write(conn); err != nil {
		t.Fatalf("writing the handshake request: %v", err)
	}

	br := bufio.NewReader(conn)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		t.Fatalf("reading the handshake response: %v", err)
	}
	// The response carries the reader the connection continues on, so closing
	// the body would close nothing here; the upgrade means there is no body.
	_ = resp.Body.Close()
	_ = conn.SetReadDeadline(time.Time{})
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("handshake status = %d, want %d", resp.StatusCode, http.StatusSwitchingProtocols)
	}
	if got, want := resp.Header.Get("Sec-WebSocket-Accept"), Accept(clientNonce); got != want {
		t.Fatalf("Sec-WebSocket-Accept = %q, want %q", got, want)
	}
	if got := resp.Header.Get("Sec-WebSocket-Extensions"); got != "" {
		t.Fatalf("server negotiated an extension: %q", got)
	}
	return newConn(conn, bufio.NewReadWriter(br, bufio.NewWriter(conn)), false)
}
