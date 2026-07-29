package hue

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"time"
)

// mdnsAddr is the standard mDNS multicast group and port.
var mdnsAddr = &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}

// hueServiceName is the mDNS service type Hue bridges advertise under.
const hueServiceName = "_hue._tcp.local."

// DiscoverMDNS sends one mDNS PTR query for _hue._tcp.local and collects any
// A records seen in the responses within the listen window.
//
// This is deliberately best-effort: mDNS does not cross VLANs, and a
// self-hoster's IoT gear is exactly the kind of thing that lives on a
// segmented one. Manual IP entry is the path of record, not this.
func DiscoverMDNS(ctx context.Context, listenFor time.Duration) ([]string, error) {
	conn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		return nil, fmt.Errorf("open mdns socket: %w", err)
	}
	defer conn.Close()

	query := buildPTRQuery(hueServiceName)
	if _, err := conn.WriteToUDP(query, mdnsAddr); err != nil {
		return nil, fmt.Errorf("send mdns query: %w", err)
	}

	deadline := time.Now().Add(listenFor)
	_ = conn.SetReadDeadline(deadline)

	seen := map[string]bool{}
	var ips []string
	buf := make([]byte, 8192)
	for {
		if ctx.Err() != nil {
			break
		}
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			break // timeout or closed; either way we're done listening
		}
		for _, ip := range extractARecords(buf[:n]) {
			if !seen[ip] {
				seen[ip] = true
				ips = append(ips, ip)
			}
		}
	}
	return ips, nil
}

// cloudBridge is one entry in the discovery.meethue.com response.
type cloudBridge struct {
	ID                string `json:"id"`
	InternalIPAddress string `json:"internalipaddress"`
}

// DiscoverCloud falls back to Philips' cloud discovery endpoint, which
// answers with any bridge the requester's public IP has previously
// registered with Hue's cloud service. It requires internet access and
// will find nothing for a bridge that has never phoned home.
func DiscoverCloud(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://discovery.meethue.com", nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("cloud discovery: %w", err)
	}
	defer resp.Body.Close()

	var bridges []cloudBridge
	if err := json.NewDecoder(resp.Body).Decode(&bridges); err != nil {
		return nil, fmt.Errorf("decode cloud discovery response: %w", err)
	}
	ips := make([]string, 0, len(bridges))
	for _, b := range bridges {
		ips = append(ips, b.InternalIPAddress)
	}
	return ips, nil
}

// Discover tries mDNS first, then cloud discovery, and returns whatever
// candidate bridge IPs it found from either. Manual entry, handled by the
// caller, is always the fallback of last resort.
func Discover(ctx context.Context) []string {
	seen := map[string]bool{}
	var out []string
	add := func(ips []string) {
		for _, ip := range ips {
			if ip != "" && !seen[ip] {
				seen[ip] = true
				out = append(out, ip)
			}
		}
	}

	mctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if ips, err := DiscoverMDNS(mctx, 1500*time.Millisecond); err == nil {
		add(ips)
	}
	if len(out) == 0 {
		cctx, cancel2 := context.WithTimeout(ctx, 5*time.Second)
		defer cancel2()
		if ips, err := DiscoverCloud(cctx); err == nil {
			add(ips)
		}
	}
	return out
}

// --- minimal DNS wire format helpers, just enough for a PTR query and to
// pull A records out of whatever comes back. ---

func encodeDNSName(name string) []byte {
	var out []byte
	start := 0
	for i := 0; i <= len(name); i++ {
		if i == len(name) || name[i] == '.' {
			if i > start {
				label := name[start:i]
				out = append(out, byte(len(label)))
				out = append(out, label...)
			}
			start = i + 1
		}
	}
	out = append(out, 0x00)
	return out
}

func buildPTRQuery(name string) []byte {
	var hdr [12]byte
	binary.BigEndian.PutUint16(hdr[0:2], uint16(rand.Intn(65536))) //nolint:gosec // query id, not security sensitive
	binary.BigEndian.PutUint16(hdr[4:6], 1)                        // QDCOUNT

	q := encodeDNSName(name)
	q = append(q, 0x00, 0x0c) // QTYPE PTR
	q = append(q, 0x00, 0x01) // QCLASS IN

	return append(hdr[:], q...)
}

// skipName advances past a (possibly compressed) DNS name starting at off
// and returns the offset immediately after it.
func skipName(buf []byte, off int) (int, bool) {
	for {
		if off >= len(buf) {
			return 0, false
		}
		b := buf[off]
		switch {
		case b == 0x00:
			return off + 1, true
		case b&0xC0 == 0xC0:
			// Compression pointer: two bytes, and the name ends here as far
			// as the containing record is concerned.
			return off + 2, true
		default:
			off += int(b) + 1
		}
	}
}

// extractARecords scans every resource record in an mDNS/DNS message
// (answer, authority and additional sections alike) and returns the dotted
// IPv4 addresses of any A records found. It does not attempt to correlate
// them with PTR/SRV names — a Hue bridge's own response only ever contains
// its own address, so anything with an A record here is a candidate.
func extractARecords(buf []byte) []string {
	if len(buf) < 12 {
		return nil
	}
	qd := int(binary.BigEndian.Uint16(buf[4:6]))
	an := int(binary.BigEndian.Uint16(buf[6:8]))
	ns := int(binary.BigEndian.Uint16(buf[8:10]))
	ar := int(binary.BigEndian.Uint16(buf[10:12]))

	off := 12
	for i := 0; i < qd; i++ {
		next, ok := skipName(buf, off)
		if !ok || next+4 > len(buf) {
			return nil
		}
		off = next + 4 // QTYPE + QCLASS
	}

	var ips []string
	total := an + ns + ar
	for i := 0; i < total; i++ {
		next, ok := skipName(buf, off)
		if !ok || next+10 > len(buf) {
			return ips
		}
		rtype := binary.BigEndian.Uint16(buf[next : next+2])
		rdlen := int(binary.BigEndian.Uint16(buf[next+8 : next+10]))
		rdataStart := next + 10
		if rdataStart+rdlen > len(buf) {
			return ips
		}
		if rtype == 0x0001 && rdlen == 4 { // A record
			ip := net.IP(buf[rdataStart : rdataStart+4])
			ips = append(ips, ip.String())
		}
		off = rdataStart + rdlen
	}
	return ips
}
