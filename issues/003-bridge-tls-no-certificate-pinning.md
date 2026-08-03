# Bridge HTTPS uses InsecureSkipVerify without certificate pinning

**Severity:** `high`

**Category:** `security`

**Found by:** exploratory proofing run, 2026-08-02

## Summary

The Hue bridge HTTP client uses `tls.Config{InsecureSkipVerify: true}` in two places without any form of certificate or public-key pinning. On a compromised LAN, an attacker can MITM bridge traffic undetected.

## Affected Files

- `internal/hue/clip.go:36` — `TLSClientConfig: &tls.Config{InsecureSkipVerify: true}`
- `internal/hue/eventstream.go:76` — same pattern for the SSE eventstream

## Evidence

```go
// clip.go:33-38
hc: &http.Client{
    Timeout: 10 * time.Second,
    Transport: &http.Transport{
        TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // bridge cert is self-signed; see doc comment above
    },
},
```

```go
// eventstream.go:74-78
client := &http.Client{
    Transport: &http.Transport{
        TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // bridge cert is self-signed, see clip.go
    },
}
```

## Why It Matters

The `//nolint:gosec` comments acknowledge the risk but dismiss it with "bridge cert is self-signed." True, but:

1. A self-signed certificate can still be pinned by its public key or fingerprint
2. The bridge certificate is stable — it changes only on factory reset
3. Without pinning, any device on the LAN that can spoof the bridge IP can intercept:
   - The application key (username/clientkey) during pairing
   - All REST API calls during normal operation
   - The eventstream (SSE), which carries real-time light state

The DTLS stream has stronger protection (PSK derived from the clientkey), but the clientkey itself is obtained over the unpinned HTTPS channel during pairing.

The pairing flow in `cmd/huemux/main.go:177` already hex-decodes and validates the clientkey length, limiting the worst case: an attacker can't inject a short/weak key. But they can still record everything the client and bridge exchange.

## Reproduction

1. Set up ARP spoofing on the LAN to intercept traffic between HueMux host and bridge
2. Run `huemux pair <bridge-ip>` 
3. The attacker sees the application key (username + clientkey) in cleartext
4. The attacker can now impersonate the client to the bridge indefinitely

## Suggested Fix

Two options, in increasing order of implementation effort:

**Option A — Certificate fingerprint pinning (simpler):**

After the first successful connection to a bridge IP, store the certificate's SHA-256 fingerprint in the bridge config. On subsequent connections, verify the presented certificate matches.

```go
// Store alongside BridgeIP/BridgeID in config.Bridge
CertSHA256 string `json:"cert_sha256,omitempty"`
```

**Option B — Public key pinning:**

Extract the bridge's public key on first connection and pin it. Survives certificate reissuance.

Both options should apply to the existing `config.Bridge` struct, persisted by `SaveBridge`, and checked in `NewClient`.

At minimum, document this as a known limitation in the security section of README.md.

## Related Issues

None directly, but this is part of the broader "loopback-only is the security model" stance. If the server is exposed to LAN (`--listen-host=0.0.0.0`), the bridge communication path is also on that LAN and equally vulnerable.
