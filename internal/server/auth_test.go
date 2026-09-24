package server

import (
	"net"
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

// A set passphrase gates loopback too: the login form would otherwise accept
// any input on the default loopback bind, and the passphrase would protect
// nothing.
func TestTokenRequiredOnLoopbackByDefault(t *testing.T) {
	s := New(tokenCfg("0.0.0.0"), nil, nil, nil, nil)
	for _, addr := range []string{"127.0.0.1:1", "[::1]:1", "127.0.0.53:1"} {
		if rec := get(s, "/api/lights", addr, nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("loopback %s without token: status %d, want 401", addr, rec.Code)
		}
	}
	// The valid token works from loopback as well.
	rec := get(s, "/api/lights", "127.0.0.1:1", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer otter.beacon.willow")
	})
	if rec.Code == http.StatusUnauthorized {
		t.Error("valid token rejected on loopback")
	}
}

// The opt-in flag restores the old convenience for trusted local machines.
func TestLoopbackExemptWhenFlagSet(t *testing.T) {
	c := tokenCfg("0.0.0.0")
	c.Auth.AllowLoopbackUnauthenticated = true
	s := New(c, nil, nil, nil, nil)
	if rec := get(s, "/api/lights", "127.0.0.1:1", nil); rec.Code == http.StatusUnauthorized {
		t.Error("loopback should be exempt when the flag is set, got 401")
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
		extraHosts          []string
		want                bool
	}{
		{origin: "http://127.0.0.1:7654", want: true},
		{origin: "http://localhost:7654", want: true},
		{origin: "http://[::1]:7654", want: true}, // was dead code before: Hostname() strips brackets
		{origin: "http://127.0.0.53:7654", want: true},
		{origin: "http://localhost.evil.example:7654", want: false},
		{origin: "", want: false},
		{origin: "https://evil.example", want: false},
		// The configured host, and only it.
		{origin: "http://lights.example:7654", allowedHost: "lights.example", want: true},
		{origin: "http://evil.example:7654", allowedHost: "lights.example", want: false},
		// Port is ignored on purpose.
		{origin: "http://lights.example:9999", allowedHost: "lights.example", want: true},
		{origin: "http://sub.lights.example", allowedHost: "lights.example", want: false},
		// Upper case in either direction is the same host.
		{origin: "http://Lights.Example", allowedHost: "lights.example", want: true},
		// Extra hosts widen the list by exactly the names given.
		{origin: "http://lights.lan", allowedHost: "127.0.0.1", extraHosts: []string{"lights.lan"}, want: true},
		{origin: "http://other.lan", allowedHost: "127.0.0.1", extraHosts: []string{"lights.lan"}, want: false},
		// One entry from a longer list is enough...
		{origin: "http://b.example", extraHosts: []string{"a.example", "b.example"}, want: true},
		// ...an empty entry matches nothing rather than everything...
		{origin: "http://evil.example", extraHosts: []string{"", "  "}, want: false},
		// ...and a pasted URL is reduced to its host, so the friendly spelling
		// an operator copies out of a browser still takes effect.
		{origin: "http://lights.lan", extraHosts: []string{"https://lights.lan:7654/settings"}, want: true},
		// A list of extras is not a wildcard: a name not in it is refused.
		{origin: "https://attacker.example", allowedHost: "0.0.0.0", extraHosts: []string{"lights.lan"}, want: false},
	}
	for _, tt := range tests {
		r := httptest.NewRequest(http.MethodGet, "/ws", nil)
		if tt.origin != "" {
			r.Header.Set("Origin", tt.origin)
		}
		if got := checkOrigin(r, tt.allowedHost, tt.extraHosts); got != tt.want {
			t.Errorf("checkOrigin(%q, allowed=%q, extra=%v) = %v, want %v",
				tt.origin, tt.allowedHost, tt.extraHosts, got, tt.want)
		}
	}
}

// Widening the allowlist must not have turned it into a wildcard.
func TestOriginStillRejectsForeignSitesWhenAuthEnabled(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	r.Header.Set("Origin", "https://attacker.example")
	if checkOrigin(r, "lights.example", nil) {
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
		if !checkOrigin(r, wildcard, nil) {
			t.Errorf("wildcard %q must accept this machine's own address %s", wildcard, mine)
		}
	}

	// Still bounded: an address this machine does not hold is rejected even
	// under a wildcard bind.
	r := httptest.NewRequest(http.MethodGet, "/ws", nil)
	r.Header.Set("Origin", "http://203.0.113.9:7654")
	if checkOrigin(r, "0.0.0.0", nil) {
		t.Error("a wildcard bind must not accept an arbitrary foreign address")
	}
	r.Header.Set("Origin", "https://attacker.example")
	if checkOrigin(r, "0.0.0.0", nil) {
		t.Error("a wildcard bind must not accept an arbitrary foreign hostname")
	}
}

// TestOriginAcceptsAddressUnderNamedBind is the other half of the same bug:
// binding the listen host to a *name* — which a reverse proxy vhost needs —
// used to make this machine's own address stop matching, so huemux became
// reachable by its vhost or by its IP but never both. A client that does not
// use the local DNS (a Tailscale peer, say) only has the address, so both have
// to work at once.
func TestOriginAcceptsAddressUnderNamedBind(t *testing.T) {
	own := LocalAddresses()
	if len(own) == 0 {
		t.Skip("no non-loopback address on this host")
	}
	// JoinHostPort, so an IPv6 interface address is bracketed rather than
	// producing an unparseable "http://fe80::1:7654".
	mine := net.JoinHostPort(own[0].String(), "7654")

	tests := []struct {
		origin string
		extras []string
		want   bool
	}{
		// The vhost name, via allowed_hosts, and the bare address together.
		{"http://lights.lan", []string{"lights.lan"}, true},
		{"http://" + mine, []string{"lights.lan"}, true},
		// An address this machine does not hold is still refused while bound
		// to a name — the widening is "our own addresses", not "addresses".
		{"http://203.0.113.9:7654", []string{"lights.lan"}, false},
	}
	for _, tt := range tests {
		r := httptest.NewRequest(http.MethodGet, "/ws", nil)
		r.Header.Set("Origin", tt.origin)
		if got := checkOrigin(r, "lights.lan", tt.extras); got != tt.want {
			t.Errorf("checkOrigin(%q, allowed=lights.lan, extra=%v) = %v, want %v",
				tt.origin, tt.extras, got, tt.want)
		}
	}
}
