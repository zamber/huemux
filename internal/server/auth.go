package server

import (
	"net/http"
	"sync"
	"time"

	"github.com/zamber/huemux/internal/appconfig"
)

// Authentication for callers that are not on this machine.
//
// The threat this addresses is narrow and worth stating: binding off loopback
// makes the light-control API reachable by anything on the network. A token
// turns "anyone who can route to this host" into "anyone who has the token".
// It is not a defence against someone who can read the config file, and it is
// not a substitute for TLS — a token over plain HTTP crosses the wire in
// cleartext, which is why appconfig.Warnings says so at startup.

// authFailureWindow and authFailureLimit rate-limit wrong tokens.
//
// The default token is three words from a ~380-word list, around 26 bits.
// That is fine against an attacker who gets a handful of guesses a minute and
// hopeless against one who gets thousands, so the limiter is what makes the
// short, memorable token format defensible at all rather than a nicety.
const (
	authFailureWindow = time.Minute
	authFailureLimit  = 10
)

type authLimiter struct {
	mu     sync.Mutex
	counts map[string]*authWindow
}

type authWindow struct {
	n     int
	start time.Time
}

func newAuthLimiter() *authLimiter {
	return &authLimiter{counts: map[string]*authWindow{}}
}

// blocked reports whether key has already used up its failures, and is called
// before the token is checked so a blocked caller learns nothing from timing.
func (l *authLimiter) blocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.counts[key]
	if w == nil {
		return false
	}
	if time.Since(w.start) > authFailureWindow {
		delete(l.counts, key)
		return false
	}
	return w.n >= authFailureLimit
}

func (l *authLimiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.counts[key]
	if w == nil || time.Since(w.start) > authFailureWindow {
		l.counts[key] = &authWindow{n: 1, start: time.Now()}
		return
	}
	w.n++
}

// succeed clears the counter so a legitimate client that fat-fingered a token
// a few times is not left locked out for the rest of the window.
func (l *authLimiter) succeed(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.counts, key)
}

// authorized reports whether r may proceed, and writes the rejection itself
// if not.
//
// The loopback exemption is opt-in (AllowLoopbackUnauthenticated) and off by
// default: with a passphrase set, the login form must actually validate it —
// a loopback-exempt server accepts any input and the passphrase protects
// nothing. A user who forgets their own token recovers by editing app.json,
// which has always been the real escape hatch anyway.
func (s *Server) authorized(w http.ResponseWriter, r *http.Request) bool {
	cfg := s.Config()
	if cfg.Auth.Mode != appconfig.AuthToken {
		return true
	}
	if cfg.Auth.AllowLoopbackUnauthenticated && isLoopbackRequest(r) {
		return true
	}

	key := clientKey(r)
	if s.authLimit.blocked(key) {
		// 429 rather than 401: a client that retries on 401 would spin, and
		// the distinction tells an honest caller what is actually happening.
		w.Header().Set("Retry-After", "60")
		http.Error(w, "too many failed authentication attempts", http.StatusTooManyRequests)
		return false
	}

	if appconfig.TokenMatches(requestToken(r), cfg.Auth.Token) {
		s.authLimit.succeed(key)
		return true
	}

	s.authLimit.fail(key)
	http.Error(w, "authentication required", http.StatusUnauthorized)
	return false
}

// requestToken pulls the token from either an Authorization header or a query
// parameter.
//
// The query parameter is not laziness: a browser's WebSocket constructor
// cannot set request headers, so ?token= is the only way a web client can
// authenticate its /ws upgrade. Header is preferred where possible because a
// URL is far more likely to end up in a log or a referrer.
func requestToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); len(h) > 7 && (h[:7] == "Bearer " || h[:7] == "bearer ") {
		return h[7:]
	}
	return r.URL.Query().Get("token")
}

// clientKey buckets rate-limit state by source IP, deliberately ignoring the
// port so retries from the same host share a counter rather than each getting
// a fresh allowance.
func clientKey(r *http.Request) string {
	host, _, err := splitHostPortLenient(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
