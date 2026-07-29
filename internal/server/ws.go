package server

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// A hand-rolled WebSocket server. The only client is our own page on
// loopback, so this is deliberately minimal: no compression, no subprotocol
// negotiation, just enough of RFC 6455 to move JSON text frames and binary
// grid frames both ways.

const (
	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xA
)

// websocketGUID is fixed by RFC 6455 §1.3.
const websocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// ErrClosed is returned by ReadMessage once the peer has closed the
// connection.
var ErrClosed = errors.New("websocket: connection closed")

// Conn is one upgraded WebSocket connection.
type Conn struct {
	rwc net.Conn
	br  *bufio.Reader
	bw  *bufio.Writer

	writeMu sync.Mutex
}

// checkOrigin enforces the loopback-only rule: without it, any website the
// user has open in another tab could open a WebSocket to this port and
// drive their lights. localAddr is the server's own bound address, used to
// additionally allow an Origin that names our own port explicitly.
func checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	host := u.Hostname()
	return host == "127.0.0.1" || host == "localhost" || host == "[::1]" || host == "::1"
}

// Upgrade performs the HTTP -> WebSocket handshake and hijacks the
// underlying connection.
func Upgrade(w http.ResponseWriter, r *http.Request) (*Conn, error) {
	if !checkOrigin(r) {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return nil, fmt.Errorf("rejected websocket upgrade from origin %q", r.Header.Get("Origin"))
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "expected websocket upgrade", http.StatusBadRequest)
		return nil, errors.New("not a websocket upgrade request")
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		http.Error(w, "missing Sec-WebSocket-Key", http.StatusBadRequest)
		return nil, errors.New("missing Sec-WebSocket-Key")
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack not supported", http.StatusInternalServerError)
		return nil, errors.New("ResponseWriter does not support hijacking")
	}
	rwc, brw, err := hj.Hijack()
	if err != nil {
		return nil, fmt.Errorf("hijack: %w", err)
	}

	accept := acceptKey(key)
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
	if _, err := brw.WriteString(resp); err != nil {
		rwc.Close()
		return nil, fmt.Errorf("write handshake response: %w", err)
	}
	if err := brw.Flush(); err != nil {
		rwc.Close()
		return nil, fmt.Errorf("flush handshake response: %w", err)
	}

	return &Conn{rwc: rwc, br: brw.Reader, bw: bufio.NewWriter(rwc)}, nil
}

func acceptKey(key string) string {
	h := sha1.New() //nolint:gosec // RFC 6455 mandates SHA-1 here; it is not used for anything security-relevant
	h.Write([]byte(key))
	h.Write([]byte(websocketGUID))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// ReadMessage reads one complete message (following continuation frames as
// needed) and returns its opcode (opText or opBinary) and payload. Ping and
// close frames are handled internally: pings are answered with a pong and
// the read loop continues; a close frame is answered with a close frame and
// ErrClosed is returned.
func (c *Conn) ReadMessage() (byte, []byte, error) {
	var payload []byte
	var msgType byte

	for {
		fin, opcode, data, err := c.readFrame()
		if err != nil {
			return 0, nil, err
		}

		switch opcode {
		case opPing:
			_ = c.writeFrame(opPong, data)
			continue
		case opPong:
			continue
		case opClose:
			_ = c.writeFrame(opClose, nil)
			return 0, nil, ErrClosed
		case opContinuation:
			payload = append(payload, data...)
		default:
			msgType = opcode
			payload = append(payload[:0], data...)
		}

		if fin {
			return msgType, payload, nil
		}
	}
}

func (c *Conn) readFrame() (fin bool, opcode byte, payload []byte, err error) {
	head := make([]byte, 2)
	if _, err = io.ReadFull(c.br, head); err != nil {
		return false, 0, nil, err
	}
	fin = head[0]&0x80 != 0
	opcode = head[0] & 0x0F
	masked := head[1]&0x80 != 0
	length := uint64(head[1] & 0x7F)

	switch length {
	case 126:
		ext := make([]byte, 2)
		if _, err = io.ReadFull(c.br, ext); err != nil {
			return false, 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(ext))
	case 127:
		ext := make([]byte, 8)
		if _, err = io.ReadFull(c.br, ext); err != nil {
			return false, 0, nil, err
		}
		length = binary.BigEndian.Uint64(ext)
	}

	var maskKey [4]byte
	if masked {
		if _, err = io.ReadFull(c.br, maskKey[:]); err != nil {
			return false, 0, nil, err
		}
	}

	payload = make([]byte, length)
	if _, err = io.ReadFull(c.br, payload); err != nil {
		return false, 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= maskKey[i%4]
		}
	}
	return fin, opcode, payload, nil
}

// WriteMessage sends one unfragmented message of the given opcode.
func (c *Conn) WriteMessage(opcode byte, payload []byte) error {
	return c.writeFrame(opcode, payload)
}

func (c *Conn) writeFrame(opcode byte, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	head := []byte{0x80 | opcode} // FIN=1, no fragmentation on the way out
	n := len(payload)
	switch {
	case n <= 125:
		head = append(head, byte(n))
	case n <= 65535:
		ext := make([]byte, 2)
		binary.BigEndian.PutUint16(ext, uint16(n))
		head = append(head, 126)
		head = append(head, ext...)
	default:
		ext := make([]byte, 8)
		binary.BigEndian.PutUint64(ext, uint64(n))
		head = append(head, 127)
		head = append(head, ext...)
	}
	// Server-to-client frames are never masked (RFC 6455 §5.1).
	if _, err := c.bw.Write(head); err != nil {
		return err
	}
	if _, err := c.bw.Write(payload); err != nil {
		return err
	}
	return c.bw.Flush()
}

// Close sends a close frame (best-effort) and closes the underlying socket.
func (c *Conn) Close() error {
	_ = c.writeFrame(opClose, nil)
	return c.rwc.Close()
}
