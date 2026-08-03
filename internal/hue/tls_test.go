package hue

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// selfSignedNoSAN builds a self-signed certificate with no subjectAltName
// extension at all — the shape of a real bridge certificate, which is what
// makes normal hostname verification impossible (no IP SANs to match).
func selfSignedNoSAN(t *testing.T) (tls.Certificate, []byte, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "hue-bridge"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, der, fmt.Sprintf("%x", sha256.Sum256(der))
}

// TestPinnedClientAcceptsSANlessCert is the regression guard for the Android
// "bulbs don't show" report: the pin used to be configured with
// InsecureSkipVerify=false, so Go's hostname check rejected the bridge's
// SAN-less certificate before the fingerprint callback could run. A freshly
// paired install could never connect; only installs whose config predated
// pinning (empty pin -> skip-verify fallback) worked.
func TestPinnedClientAcceptsSANlessCert(t *testing.T) {
	cert, _, pin := selfSignedNoSAN(t)

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	server.StartTLS()
	defer server.Close()

	client := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConfigForCert(pin)}}
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("pinned client rejected its own SAN-less cert: %v", err)
	}
	resp.Body.Close() //nolint:errcheck // test teardown
}

// The pin must still be the gate: a different fingerprint is rejected even
// though the connection is to the same host.
func TestPinnedClientRejectsWrongFingerprint(t *testing.T) {
	cert, _, pin := selfSignedNoSAN(t)
	badPin := "00" + pin[2:]

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	server.StartTLS()
	defer server.Close()

	client := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConfigForCert(badPin)}}
	if _, err := client.Get(server.URL); err == nil {
		t.Fatal("connection with a wrong fingerprint was accepted")
	}
}

// The unpinned fallback (configs saved before pinning existed) still
// connects to anything, SAN-less or not.
func TestUnpinnedClientConnects(t *testing.T) {
	cert, _, _ := selfSignedNoSAN(t)

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	server.StartTLS()
	defer server.Close()

	client := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConfigForCert("")}}
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("unpinned fallback rejected the cert: %v", err)
	}
	resp.Body.Close() //nolint:errcheck // test teardown
}
