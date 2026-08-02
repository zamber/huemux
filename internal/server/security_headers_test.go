package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zamber/huemux/internal/appconfig"
)

// TestSecurityHeaders exercises securityHeaders on the same wrapped-mux shape
// ListenAndServe serves (http.Serve(ln, securityHeaders(s.mux))). The rest of
// this package's tests talk to s.mux directly and never see these headers, so
// without this test the middleware could be deleted and every test would still
// pass.
func TestSecurityHeaders(t *testing.T) {
	s := New(appconfig.Default(), nil, nil, nil, nil)
	handler := securityHeaders(s.mux)

	want := map[string]string{
		"Content-Security-Policy": "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; connect-src 'self' ws: wss:; img-src 'self' data:; font-src 'self'",
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "SAMEORIGIN",
		"Referrer-Policy":         "no-referrer",
	}

	for _, path := range []string{"/app.html", "/api/status"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", path, rec.Code)
		}
		for name, value := range want {
			if got := rec.Header().Get(name); got != value {
				t.Errorf("%s: %s = %q, want %q", path, name, got, value)
			}
		}
	}
}
