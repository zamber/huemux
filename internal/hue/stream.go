package hue

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/pion/dtls/v3"
)

// StreamPort is fixed by the Hue Entertainment API.
const StreamPort = 2100

// ColorSpace selects how channel values are interpreted by the bridge.
type ColorSpace byte

const (
	ColorSpaceRGB ColorSpace = 0x00
	ColorSpaceXY  ColorSpace = 0x01
)

// maxChannelsPerPacket is the documented per-packet channel ceiling.
const maxChannelsPerPacket = 10

// Channel is one addressable light channel in an entertainment configuration.
// R, G and B are 8-bit; the encoder widens them (see writeComponent).
type Channel struct {
	ID      uint8
	R, G, B uint8
}

// Stream owns the DTLS session to the bridge and the fixed-rate output loop.
//
// The output loop is deliberately independent of any frame source: the
// bridge tears the stream down after roughly ten seconds of silence, so
// packets must keep flowing whether or not new colors are arriving.
type Stream struct {
	cfg Config

	mu      sync.Mutex
	latest  []Channel
	seq     uint8
	conn    *dtls.Conn
	udpConn *net.UDPConn
	buf     []byte
	stopped bool

	// Stats, read by the CLI status view.
	Sent      uint64
	LastError error
}

// Config carries everything needed to open a stream.
type Config struct {
	BridgeIP    string
	Username    string // doubles as the DTLS PSK identity
	ClientKey   string // hex-encoded; MUST be decoded before use as the PSK
	AreaID      string // entertainment configuration UUID, 36 ASCII chars
	ColorSpace  ColorSpace
	OutputHz    int
	DisableEMS  bool // some bridge firmware will not negotiate RFC 7627
	HandshakeTO time.Duration
}

// Dial performs the DTLS-PSK handshake.
//
// The caller must have already PUT {"action":"start"} on the entertainment
// configuration: the bridge only accepts a handshake for an area that is
// already in streaming state.
func Dial(ctx context.Context, cfg Config) (*Stream, error) {
	if len(cfg.AreaID) != 36 {
		return nil, fmt.Errorf("area id must be a 36-char UUID, got %d chars", len(cfg.AreaID))
	}

	// The clientkey from registration is a hex string. Passing the ASCII
	// bytes straight through is the most common cause of an unexplained
	// handshake failure, so fail here with a message that says so.
	psk, err := hex.DecodeString(cfg.ClientKey)
	if err != nil {
		return nil, fmt.Errorf("clientkey is not valid hex (it must be hex-decoded before use as the PSK): %w", err)
	}

	ems := dtls.RequireExtendedMasterSecret
	if cfg.DisableEMS {
		ems = dtls.DisableExtendedMasterSecret
	}

	dc := &dtls.Config{
		PSK:                  func(_ []byte) ([]byte, error) { return psk, nil },
		PSKIdentityHint:      []byte(cfg.Username),
		CipherSuites:         []dtls.CipherSuiteID{dtls.TLS_PSK_WITH_AES_128_GCM_SHA256},
		ExtendedMasterSecret: ems,
	}

	to := cfg.HandshakeTO
	if to <= 0 {
		to = 10 * time.Second
	}
	hctx, cancel := context.WithTimeout(ctx, to)
	defer cancel()

	addr := &net.UDPAddr{IP: net.ParseIP(cfg.BridgeIP), Port: StreamPort}
	if addr.IP == nil {
		return nil, fmt.Errorf("invalid bridge ip %q", cfg.BridgeIP)
	}

	// Deliberately net.ListenUDP, not net.DialUDP: a "connected" UDP socket
	// only allows plain Write (to the address it was dialed to), and pion's
	// Conn addresses every write explicitly via WriteTo, which Go's net
	// package refuses on a connected socket ("use of WriteTo with
	// pre-connected connection"). That failure doesn't surface until the
	// first post-handshake application write, which makes it look like the
	// handshake succeeded and then the session mysteriously died — pion's
	// own Dial() carries the same comment for the same reason.
	udpConn, err := net.ListenUDP("udp", nil)
	if err != nil {
		return nil, fmt.Errorf("open udp socket: %w", err)
	}

	// dtls.Client blocks for the full handshake with no context support in
	// this API version. Run it in a goroutine and force it to unblock by
	// closing the underlying socket if our deadline arrives first.
	type result struct {
		conn *dtls.Conn
		err  error
	}
	resCh := make(chan result, 1)
	go func() {
		conn, err := dtls.Client(udpConn, addr, dc)
		resCh <- result{conn, err}
	}()

	select {
	case <-hctx.Done():
		udpConn.Close() //nolint:errcheck
		<-resCh         // let the goroutine unblock before we return
		return nil, fmt.Errorf("dtls handshake with %s: %w", addr, hctx.Err())
	case r := <-resCh:
		if r.err != nil {
			udpConn.Close() //nolint:errcheck
			return nil, fmt.Errorf("dtls handshake with %s: %w", addr, r.err)
		}
		if cfg.OutputHz <= 0 {
			cfg.OutputHz = 20
		}
		if cfg.OutputHz > 25 {
			// The bridge is rate limited at the source; above roughly 25 Hz
			// it drops packets and the feed stalls rather than going faster.
			cfg.OutputHz = 25
		}
		return &Stream{cfg: cfg, conn: r.conn, udpConn: udpConn, buf: make([]byte, 0, 512)}, nil
	}
}

// Set replaces the colors that the output loop will send from now on.
// Safe to call from any goroutine, at any rate, including not at all.
func (s *Stream) Set(ch []Channel) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.latest = append(s.latest[:0], ch...)
}

// Run drives the fixed-rate output loop until ctx is cancelled.
//
// It re-sends the last known colors when nothing new has arrived. That is
// not a wasted packet: it is the keepalive that stops the bridge handing the
// lights back to their previous state.
func (s *Stream) Run(ctx context.Context) error {
	interval := time.Second / time.Duration(s.cfg.OutputHz)
	t := time.NewTicker(interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			s.fadeOut()
			return ctx.Err()
		case <-t.C:
			if err := s.flush(); err != nil {
				s.mu.Lock()
				s.LastError = err
				s.mu.Unlock()
				// UDP gives no close signal, so a write error is the main
				// way we learn the session died. Surface it and let the
				// supervisor rebuild the stream with backoff.
				return err
			}
		}
	}
}

func (s *Stream) flush() error {
	s.mu.Lock()
	if s.stopped || len(s.latest) == 0 {
		s.mu.Unlock()
		return nil
	}
	chans := append([]Channel(nil), s.latest...)
	s.seq++
	seq := s.seq
	s.mu.Unlock()

	// Areas may exceed the ten-channel packet ceiling; split across packets
	// within the same tick.
	for i := 0; i < len(chans); i += maxChannelsPerPacket {
		end := i + maxChannelsPerPacket
		if end > len(chans) {
			end = len(chans)
		}
		pkt := s.encode(seq, chans[i:end])
		if _, err := s.conn.Write(pkt); err != nil {
			return fmt.Errorf("stream write: %w", err)
		}
	}

	s.mu.Lock()
	s.Sent++
	s.mu.Unlock()
	return nil
}

// Stats returns a consistent snapshot of the stream statistics, safe for
// concurrent access from callers that do not hold s.mu.
func (s *Stream) Stats() (sent uint64, lastErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Sent, s.LastError
}

// encode builds one HueStream v2 packet.
//
//	 0  9  "HueStream"
//	 9  2  0x02 0x00                protocol version 2.0
//	11  1  sequence (bridge ignores it, but increment anyway)
//	12  2  reserved
//	14  1  color space
//	15  1  reserved
//	16 36  entertainment configuration id, ASCII
//	52  7N [id][R hi][R lo][G hi][G lo][B hi][B lo]
func (s *Stream) encode(seq uint8, chans []Channel) []byte {
	b := s.buf[:0]
	b = append(b, "HueStream"...)
	b = append(b, 0x02, 0x00)
	b = append(b, seq)
	b = append(b, 0x00, 0x00)
	b = append(b, byte(s.cfg.ColorSpace))
	b = append(b, 0x00)
	b = append(b, s.cfg.AreaID...)

	for _, c := range chans {
		b = append(b, c.ID)
		b = writeComponent(b, c.R)
		b = writeComponent(b, c.G)
		b = writeComponent(b, c.B)
	}
	s.buf = b
	return b
}

// writeComponent widens an 8-bit value into the 16-bit wire field.
//
// The API documents 16-bit resolution, but in practice the low byte is
// ignored and sending genuinely different high and low bytes makes lights
// misbehave. Duplicating the byte gives a clean full-range ramp.
func writeComponent(b []byte, v uint8) []byte {
	return append(b, v, v)
}

// fadeOut sends a short ramp to black so that releasing the area does not
// end in an abrupt snap back to whatever the lights were doing before.
func (s *Stream) fadeOut() {
	s.mu.Lock()
	chans := append([]Channel(nil), s.latest...)
	s.mu.Unlock()
	if len(chans) == 0 {
		return
	}

	const steps = 10
	for i := steps; i >= 0; i-- {
		scaled := make([]Channel, len(chans))
		for j, c := range chans {
			scaled[j] = Channel{
				ID: c.ID,
				R:  uint8(int(c.R) * i / steps),
				G:  uint8(int(c.G) * i / steps),
				B:  uint8(int(c.B) * i / steps),
			}
		}
		s.Set(scaled)
		_ = s.flush()
		time.Sleep(time.Second / time.Duration(s.cfg.OutputHz))
	}
}

// Close tears down the DTLS session. The caller is responsible for the
// subsequent PUT {"action":"stop"} on the entertainment configuration.
func (s *Stream) Close() error {
	s.mu.Lock()
	s.stopped = true
	conn := s.conn
	udpConn := s.udpConn
	s.mu.Unlock()
	var err error
	if conn != nil {
		err = conn.Close()
	}
	if udpConn != nil {
		_ = udpConn.Close()
	}
	return err
}

// ErrAreaBusy is returned when another application already holds the stream.
var ErrAreaBusy = errors.New("entertainment area is already being streamed to by another application")
