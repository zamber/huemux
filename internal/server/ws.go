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

	"github.com/zamber/huemux/internal/appconfig"
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

// maxFrameSize bounds a single frame's payload allocation so a hostile 64-bit
// length field cannot OOM the process. The largest legitimate payload is a
// grid frame at 3 + 255*255*3 = 195,078 bytes — far below this.
const maxFrameSize = 16 << 20 // 16 MiB

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

// checkOrigin is what stops any website the user happens to have open from
// opening a WebSocket to this port and driving their lights. It is the single
// most load-bearing security check in the program.
//
// The allowlist is every name this server can legitimately be reached by:
//
//   - localhost, any loopback address, and this machine's own addresses —
//     derived, never configured, and safe by construction. A hostile page can
//     present a foreign *name*, because names are just whatever its author
//     registered; it can never make the browser write an address literal that
//     this machine holds into the Origin header. Accepting the machine's own
//     addresses is therefore not a widening of trust at all.
//   - allowedHost, the host the server was configured to listen on.
//   - extraHosts, the operator's allowed_hosts list. A name a browser reaches
//     the server by — a reverse proxy's vhost, a Tailscale MagicDNS name —
//     cannot be derived from the socket, so it has to be stated, once, by the
//     person who set the proxy up.
//
// Deliberately not a wildcard, and deliberately not "skip the check when a
// token is present": an attacker's page would happily send a token it had
// obtained, and the whole point of this check is that it holds even when
// something else has failed.
//
// Note the port is ignored. An attacker who can bind another port on the same
// host has already lost you the machine, so distinguishing ports buys nothing
// while breaking legitimate access on the auto-selected fallback port.
func checkOrigin(r *http.Request, allowedHost string, extraHosts []string) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		// A browser always sends Origin on a WebSocket handshake, so an
		// absent one means a non-browser client — which does not need the
		// protection this check provides, but also should not get a free pass.
		return false
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	// Hostname() strips brackets from an IPv6 literal, so comparing against
	// "[::1]" here could never match — that branch was dead code. Parse the
	// address instead, which also covers the rest of 127.0.0.0/8.
	//
	// Both sides go through the same normalizer, so an allowed_hosts entry
	// written as "https://lights.example:7654/" matches the bare
	// "lights.example" a browser puts in Origin. Normalizing only one side
	// would make the friendly spelling in app.json silently ineffective.
	host := appconfig.NormalizeHost(u.Hostname())
	if host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	// An address literal this machine holds is accepted unconditionally —
	// not only under a wildcard bind. Requiring the wildcard made the two
	// ways of reaching huemux mutually exclusive: binding the listen host to
	// a name (so the vhost's Origin matched) rejected the IP, and binding it
	// to the IP rejected the name. LAN and Tailscale clients use whichever of
	// the two their own DNS gives them, so both have to work at once.
	if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || isOwnAddress(ip)) {
		return true
	}
	if allowedHost != "" && host == appconfig.NormalizeHost(allowedHost) {
		return true
	}
	for _, extra := range extraHosts {
		if n := appconfig.NormalizeHost(extra); n != "" && n == host {
			return true
		}
	}
	return false
}

// isOwnAddress reports whether ip is an address bound to one of this machine's
// interfaces. Same bounded set the self-signed certificate is issued for, and
// for the same reason.
func isOwnAddress(ip net.IP) bool {
	for _, own := range LocalAddresses() {
		if own.Equal(ip) {
			return true
		}
	}
	return false
}

// isWildcardHost reports whether host means "every interface".
func isWildcardHost(host string) bool {
	return host == "" || host == "0.0.0.0" || host == "::" || host == "[::]"
}

// Upgrade performs the HTTP -> WebSocket handshake and hijacks the
// underlying connection. allowedHost and extraHosts are the same two
// allowlist inputs checkOrigin documents.
func Upgrade(w http.ResponseWriter, r *http.Request, allowedHost string, extraHosts []string) (*Conn, error) {
	if !checkOrigin(r, allowedHost, extraHosts) {
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

	if length > maxFrameSize {
		return false, 0, nil, fmt.Errorf("frame too large: %d bytes (max %d)", length, maxFrameSize)
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
