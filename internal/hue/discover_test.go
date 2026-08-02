package hue

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func dnsHeader(qd, an, ns, ar int) []byte {
	h := make([]byte, 12)
	binary.BigEndian.PutUint16(h[4:6], uint16(qd))   // QDCOUNT
	binary.BigEndian.PutUint16(h[6:8], uint16(an))   // ANCOUNT
	binary.BigEndian.PutUint16(h[8:10], uint16(ns))  // NSCOUNT
	binary.BigEndian.PutUint16(h[10:12], uint16(ar)) // ARCOUNT
	return h
}

// aRecord builds one resource record: a compressed name pointer followed by
// the standard 10-byte fixed part and the IPv4 rdata.
func aRecord(ip [4]byte) []byte {
	rec := []byte{0xC0, 0x0C}                 // compression pointer to offset 12
	rec = append(rec, 0x00, 0x01)             // TYPE A
	rec = append(rec, 0x00, 0x01)             // CLASS IN
	rec = append(rec, 0x00, 0x00, 0x00, 0x78) // TTL
	rec = append(rec, 0x00, 0x04)             // RDLEN
	rec = append(rec, ip[:]...)
	return rec
}

func ptrRecord(rdata []byte) []byte {
	rec := []byte{0xC0, 0x0C}
	rec = append(rec, 0x00, 0x0C)                            // TYPE PTR
	rec = append(rec, 0x00, 0x01)                            // CLASS IN
	rec = append(rec, 0x00, 0x00, 0x00, 0x78)                // TTL
	rec = append(rec, byte(len(rdata)>>8), byte(len(rdata))) // RDLEN
	rec = append(rec, rdata...)
	return rec
}

func questionSection() []byte {
	q := encodeDNSName(hueServiceName)
	q = append(q, 0x00, 0x0C) // QTYPE PTR
	q = append(q, 0x00, 0x01) // QCLASS IN
	return q
}

func TestExtractARecordsKnownGoodDump(t *testing.T) {
	// Hand-verified DNS response: id=0x0000, flags=0x8400 (response, authorit-
	// ative), QDCOUNT=0, ANCOUNT=1, one A record for 192.168.1.240 whose name
	// is a compression pointer back to offset 12.
	msg := []byte{
		0x00, 0x00, 0x84, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00,
		0xC0, 0x0C, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0x00, 0x78, 0x00, 0x04,
		0xC0, 0xA8, 0x01, 0xF0,
	}
	ips := extractARecords(msg)
	if len(ips) != 1 || ips[0] != "192.168.1.240" {
		t.Fatalf("extractARecords = %v, want [192.168.1.240]", ips)
	}
}

func TestExtractARecordsShortBuffer(t *testing.T) {
	for _, n := range []int{0, 1, 11} {
		if ips := extractARecords(make([]byte, n)); ips != nil {
			t.Errorf("len %d: got %v, want nil", n, ips)
		}
	}
}

func TestExtractARecordsSkipsQuestions(t *testing.T) {
	msg := append(dnsHeader(1, 1, 0, 0), questionSection()...)
	msg = append(msg, aRecord([4]byte{10, 0, 0, 5})...)
	ips := extractARecords(msg)
	if len(ips) != 1 || ips[0] != "10.0.0.5" {
		t.Fatalf("extractARecords = %v, want [10.0.0.5]", ips)
	}
}

func TestExtractARecordsIgnoresNonARecords(t *testing.T) {
	msg := append(dnsHeader(0, 2, 0, 0), ptrRecord([]byte{0x03, 'h', 'u'})...)
	msg = append(msg, aRecord([4]byte{192, 168, 1, 42})...)
	ips := extractARecords(msg)
	if len(ips) != 1 || ips[0] != "192.168.1.42" {
		t.Fatalf("extractARecords = %v, want [192.168.1.42] (PTR must be skipped)", ips)
	}
}

func TestExtractARecordsSkipsWrongRDLen(t *testing.T) {
	// An A-type record whose rdlen is not 4 is not an address; it must be
	// skipped without corrupting the walk over the following record.
	long := func() []byte {
		rec := []byte{0xC0, 0x0C}
		rec = append(rec, 0x00, 0x01)             // TYPE A
		rec = append(rec, 0x00, 0x01)             // CLASS IN
		rec = append(rec, 0x00, 0x00, 0x00, 0x78) // TTL
		rec = append(rec, 0x00, 0x08)             // RDLEN 8
		rec = append(rec, 1, 2, 3, 4, 0xDE, 0xAD, 0xBE, 0xEF)
		return rec
	}
	msg := append(dnsHeader(0, 2, 0, 0), long()...)
	msg = append(msg, aRecord([4]byte{5, 6, 7, 8})...)
	ips := extractARecords(msg)
	if len(ips) != 1 || ips[0] != "5.6.7.8" {
		t.Fatalf("extractARecords = %v, want [5.6.7.8]", ips)
	}
}

func TestExtractARecordsMultiple(t *testing.T) {
	msg := append(dnsHeader(0, 2, 0, 0), aRecord([4]byte{10, 1, 1, 1})...)
	msg = append(msg, aRecord([4]byte{10, 1, 1, 2})...)
	ips := extractARecords(msg)
	if len(ips) != 2 || ips[0] != "10.1.1.1" || ips[1] != "10.1.1.2" {
		t.Fatalf("extractARecords = %v, want [10.1.1.1 10.1.1.2]", ips)
	}
}

func TestExtractARecordsTruncatedRData(t *testing.T) {
	msg := append(dnsHeader(0, 1, 0, 0), aRecord([4]byte{1, 2, 3, 4})...)
	msg = msg[:len(msg)-2] // rdata truncated: only 2 of 4 bytes present
	if ips := extractARecords(msg); ips != nil {
		t.Errorf("truncated message: got %v, want nil", ips)
	}
}

func TestExtractARecordsAdditionalSection(t *testing.T) {
	// A records in the additional section count too — mDNS responders
	// commonly put addresses there rather than in the answer section.
	msg := append(dnsHeader(0, 0, 0, 1), aRecord([4]byte{192, 168, 0, 99})...)
	ips := extractARecords(msg)
	if len(ips) != 1 || ips[0] != "192.168.0.99" {
		t.Fatalf("extractARecords = %v, want [192.168.0.99]", ips)
	}
}

func TestExtractARecordsNoRecords(t *testing.T) {
	if ips := extractARecords(dnsHeader(0, 0, 0, 0)); len(ips) != 0 {
		t.Errorf("empty message: got %v, want none", ips)
	}
}

func TestBuildPTRQuery(t *testing.T) {
	q := buildPTRQuery(hueServiceName)
	if len(q) != 12+len(encodeDNSName(hueServiceName))+4 {
		t.Fatalf("query length = %d", len(q))
	}
	if binary.BigEndian.Uint16(q[4:6]) != 1 {
		t.Errorf("QDCOUNT = %d, want 1", binary.BigEndian.Uint16(q[4:6]))
	}
	if binary.BigEndian.Uint16(q[6:8]) != 0 || binary.BigEndian.Uint16(q[8:10]) != 0 || binary.BigEndian.Uint16(q[10:12]) != 0 {
		t.Error("ANCOUNT/NSCOUNT/ARCOUNT must all be zero in a query")
	}
	if !bytes.HasPrefix(q[12:], encodeDNSName(hueServiceName)) {
		t.Errorf("question name = %x, want %x", q[12:], encodeDNSName(hueServiceName))
	}
	if !bytes.HasSuffix(q, []byte{0x00, 0x0C, 0x00, 0x01}) {
		t.Errorf("question tail = %x, want [00 0c 00 01] (PTR IN)", q[len(q)-4:])
	}
}

func TestEncodeDNSName(t *testing.T) {
	tests := []struct {
		name string
		want []byte
	}{
		{"", []byte{0x00}},
		{"a", []byte{0x01, 'a', 0x00}},
		{"a.b.", []byte{0x01, 'a', 0x01, 'b', 0x00}},
		{"a.b", []byte{0x01, 'a', 0x01, 'b', 0x00}}, // trailing dot optional
		{hueServiceName, []byte{0x04, '_', 'h', 'u', 'e', 0x04, '_', 't', 'c', 'p', 0x05, 'l', 'o', 'c', 'a', 'l', 0x00}},
	}
	for _, tt := range tests {
		if got := encodeDNSName(tt.name); !bytes.Equal(got, tt.want) {
			t.Errorf("encodeDNSName(%q) = %x, want %x", tt.name, got, tt.want)
		}
	}
}

func TestSkipName(t *testing.T) {
	tests := []struct {
		buf  []byte
		off  int
		next int
		ok   bool
	}{
		{[]byte{0x00}, 0, 1, true},
		{[]byte{0x01, 'a', 0x00}, 0, 3, true},
		{[]byte{0x03, 'a', 'b', 'c', 0x00}, 0, 5, true},
		{[]byte{0xC0, 0x0C}, 0, 2, true}, // compression pointer
		{[]byte{0x01, 'a', 0xC0, 0x0C}, 0, 4, true},
		{[]byte{0x01, 'a'}, 0, 0, false},       // name runs past the buffer
		{[]byte{0x01, 'a', 0x00}, 3, 0, false}, // offset at the end
	}
	for _, tt := range tests {
		next, ok := skipName(tt.buf, tt.off)
		if next != tt.next || ok != tt.ok {
			t.Errorf("skipName(%x, %d) = (%d, %v), want (%d, %v)", tt.buf, tt.off, next, ok, tt.next, tt.ok)
		}
	}
}
