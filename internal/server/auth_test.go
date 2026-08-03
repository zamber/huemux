package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zamber/huemux/internal/appconfig"
)

func tokenCfg(host string) appconfig.Config {
	c := appconfig.Default()
	c.Listen.Host = host
	c.Auth.Mode = appconfig.AuthToken
	c.Auth.Token = "otter.beacon.willow"
	return c
}

func get(s *Server, path, remote string, mut func(*http.Request)) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = remote
	if mut != nil {
		mut(req)
	}
	s.mux.ServeHTTP(rec, req)
	return rec
}

func TestAuthDisabledByDefault(t *testing.T) {
	s := New(appconfig.Default(), nil, nil, nil, nil)
	if rec := get(s, "/api/lights", "192.0.2.10:1", nil); rec.Code == http.StatusUnauthorized {
		t.Error("default config must not require authentication")
	}
}

// Turning auth on must not put a login in front of ordinary desktop use.
func TestLoopbackExemptFromToken(t *testing.T) {
	s := New(tokenCfg("0.0.0.0"), nil, nil, nil, nil)
	for _, addr := range []string{"127.0.0.1:1", "[::1]:1", "127.0.0.53:1"} {
		if rec := get(s, "/api/lights", addr, nil); rec.Code == http.StatusUnauthorized {
			t.Errorf("loopback %s should be exempt, got 401", addr)
		}
	}
}

func TestTokenRequiredOffLoopback(t *testing.T) {
	s := New(tokenCfg("0.0.0.0"), nil, nil, nil, nil)

	if rec := get(s, "/api/lights", "192.0.2.10:1", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: status %d, want 401", rec.Code)
	}
	// Header form.
	rec := get(s, "/api/lights", "192.0.2.11:1", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer otter.beacon.willow")
	})
	if rec.Code == http.StatusUnauthorized {
		t.Error("valid bearer token was rejected")
	}
	// Query form — the only option a browser WebSocket has, since it cannot
	// set headers.
	if rec := get(s, "/api/lights?token=otter.beacon.willow", "192.0.2.12:1", nil); rec.Code == http.StatusUnauthorized {
		t.Error("valid ?token= was rejected")
	}
	if rec := get(s, "/api/lights?token=wrong.words.here", "192.0.2.13:1", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong token: status %d, want 401", rec.Code)
	}
}

// A short memorable token is only defensible with a limiter in front of it.
func TestFailedAuthIsRateLimited(t *testing.T) {
	s := New(tokenCfg("0.0.0.0"), nil, nil, nil, nil)
	const addr = "192.0.2.50:1"

	for i := 0; i < authFailureLimit; i++ {
		if rec := get(s, "/api/lights?token=nope", addr, nil); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status %d, want 401", i, rec.Code)
		}
	}
	rec := get(s, "/api/lights?token=nope", addr, nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("after %d failures: status %d, want 429", authFailureLimit, rec.Code)
	}
	// A different source must not inherit that block.
	if rec := get(s, "/api/lights?token=otter.beacon.willow", "192.0.2.51:1", nil); rec.Code == http.StatusTooManyRequests {
		t.Error("rate limit must be per-client, not global")
	}
}

// Static assets stay reachable: they are an HTML shell that cannot do anything
// until a guarded endpoint answers it.
func TestStaticAssetsNotGuarded(t *testing.T) {
	s := New(tokenCfg("0.0.0.0"), nil, nil, nil, nil)
	if rec := get(s, "/shared/theme.css", "192.0.2.10:1", nil); rec.Code == http.StatusUnauthorized {
		t.Error("static assets should not require a token")
	}
}

// --- Origin -------------------------------------------------------------

func TestCheckOrigin(t *testing.T) {
	tests := []struct {
		origin, allowedHost string
		want                bool
	}{
		{"http://127.0.0.1:7654", "", true},
		{"http://localhost:7654", "", true},
		{"http://[::1]:7654", "", true}, // was dead code before: Hostname() strips brackets
		{"http://127.0.0.53:7654", "", true},
		{"", "", false},
		{"https://evil.example", "", false},
		// The configured host, and only it.
		{"http://lights.example:7654", "lights.example", true},
		{"http://evil.example:7654", "lights.example", false},
		// Port is ignored on purpose.
		{"http://lights.example:9999", "lights.example", true},
		{"http://sub.lights.example", "lights.example", false},
	}
	for _, tt := range tests {
		r := httptest.NewRequest(http.MethodGet, "/ws", nil)
		if tt.origin != "" {
			r.Header.Set("Origin", tt.origin)
		}
		if got := checkOrigin(r, tt.allowedHost); got != tt.want {
			t.Errorf("checkOrigin(%q, allowed=%q) = %v, want %v", tt.origin, tt.allowedHost, got, tt.want)
		}
	}
}

// Widening the allowlist must not have turned it into a wildcard.
func TestOriginStillRejectsForeignSitesWhenAuthEnabled(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	r.Header.Set("Origin", "https://attacker.example")
	if checkOrigin(r, "lights.example") {
		t.Fatal("a foreign Origin must be rejected regardless of auth being configured")
	}
}

// TestOriginAcceptsOwnAddressOnWildcardBind covers the bug that made LAN
// access silently useless: with listen.host = "0.0.0.0" a browser's Origin is
// a concrete address and can never equal the wildcard, so every WebSocket
// upgrade was rejected while static assets still loaded — the page rendered
// its header and then stayed empty.
func TestOriginAcceptsOwnAddressOnWildcardBind(t *testing.T) {
	own := LocalAddresses()
	if len(own) == 0 {
		t.Skip("no non-loopback address on this host")
	}
	mine := own[0].String()

	for _, wildcard := range []string{"0.0.0.0", "::", ""} {
		r := httptest.NewRequest(http.MethodGet, "/ws", nil)
		r.Header.Set("Origin", "http://"+mine+":7654")
		if !checkOrigin(r, wildcard) {
			t.Errorf("wildcard %q must accept this machine's own address %s", wildcard, mine)
		}
	}

	// Still bounded: an address this machine does not hold is rejected even
	// under a wildcard bind.
	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	r.Header.Set("Origin", "http://203.0.113.9:7654")
	if checkOrigin(r, "0.0.0.0") {
		t.Error("a wildcard bind must not accept an arbitrary foreign address")
	}
	r.Header.Set("Origin", "https://attacker.example")
	if checkOrigin(r, "0.0.0.0") {
		t.Error("a wildcard bind must not accept an arbitrary foreign hostname")
	}
}
