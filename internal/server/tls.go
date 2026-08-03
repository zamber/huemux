package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/zamber/huemux/internal/appconfig"
	"github.com/zamber/huemux/internal/config"
)

// TLS support, such as it is.
//
// HueMux deliberately does not obtain certificates. It has no ACME client, no
// DNS integration, and no opinion about your domain — "files" mode consumes
// whatever you already have, which covers the three approaches that actually
// work for a LAN service: a Tailscale cert (`tailscale cert`, auto-renewing
// and the easiest by a distance), a real certificate for a domain you own via
// a DNS-01 challenge, or a local CA like mkcert.
//
// "selfsigned" exists for the case where none of that is set up and you just
// want the connection encrypted. Browsers will warn, because a self-signed
// certificate is exactly as trustworthy as the network you got it over.

func tlsConfigFor(cfg appconfig.Config) (*tls.Config, error) {
	switch cfg.TLS.Mode {
	case appconfig.TLSFiles:
		cert, err := tls.LoadX509KeyPair(cfg.TLS.CertFile, cfg.TLS.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("load TLS keypair: %w", err)
		}
		return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}, nil

	case appconfig.TLSSelfSigned:
		cert, err := selfSignedCert(cfg.Listen.Host)
		if err != nil {
			return nil, err
		}
		return &tls.Config{Certificates: []tls.Certificate{*cert}, MinVersion: tls.VersionTLS12}, nil

	default:
		return nil, fmt.Errorf("tls mode %q needs no TLS config", cfg.TLS.Mode)
	}
}

// selfSignedCert loads a previously generated certificate, or makes one.
//
// Persisted rather than regenerated per launch on purpose: a browser that has
// been told once to accept this certificate should not be asked again on every
// restart, and a fresh identity each boot is indistinguishable from an attack.
func selfSignedCert(host string) (*tls.Certificate, error) {
	dir, err := config.Dir()
	if err != nil {
		return nil, err
	}
	certPath := filepath.Join(dir, "selfsigned-cert.pem")
	keyPath := filepath.Join(dir, "selfsigned-key.pem")

	if cert, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		if leaf, perr := x509.ParseCertificate(cert.Certificate[0]); perr == nil {
			// Regenerate rather than serve something already expired, or about
			// to expire mid-session.
			if time.Now().Before(leaf.NotAfter.Add(-24 * time.Hour)) {
				return &cert, nil
			}
		}
	}
	return generateSelfSigned(host, certPath, keyPath)
}

func generateSelfSigned(host, certPath, keyPath string) (*tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("generate serial: %w", err)
	}

	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{Organization: []string{"HueMux"}, CommonName: "HueMux"},
		NotBefore:             time.Now().Add(-time.Hour), // tolerate mild clock skew
		NotAfter:              time.Now().AddDate(2, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	// Loopback names always go in, whatever the configured host: even a
	// LAN-bound server is still reached over 127.0.0.1 from the machine
	// itself, and a certificate that fails there would break the desktop app
	// pointing at its own server.
	tmpl.DNSNames = []string{"localhost"}
	tmpl.IPAddresses = []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
	switch {
	case host == "" || host == "0.0.0.0" || host == "::":
		// A wildcard bind is the usual way to expose this on a LAN, and the
		// address people actually type is the machine's LAN IP — which is not
		// derivable from "0.0.0.0". Without enumerating the interfaces here,
		// the certificate covers only loopback and every LAN browser gets a
		// name-mismatch error stacked on top of the self-signed warning,
		// which makes the whole mode useless for the case it exists to serve.
		for _, ip := range LocalAddresses() {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		}
	default:
		if ip := net.ParseIP(host); ip != nil {
			if !ip.IsLoopback() {
				tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
			}
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, host)
		}
	}

	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("create certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	// 0600 on the key. It is a private key, even a throwaway one.
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", certPath, err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return nil, fmt.Errorf("write %s: %w", keyPath, err)
	}

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("load generated keypair: %w", err)
	}
	return &cert, nil
}

// LocalAddresses returns this machine's non-loopback unicast IPs, for naming
// in a self-signed certificate when the server binds a wildcard address, and
// for the settings UI to preview LAN reachable URLs.
// Best-effort: a failure here costs a SAN entry, not the server starting.
func LocalAddresses() []net.IP {
	var out []net.IP
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return out
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() || !ipnet.IP.IsGlobalUnicast() {
			continue
		}
		out = append(out, ipnet.IP)
	}
	return out
}
