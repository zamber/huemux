package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zamber/huemux/internal/appconfig"
)

func TestConfigAPIDerivesFeaturesServerSide(t *testing.T) {
	for _, tt := range []struct {
		profile              appconfig.Profile
		wantLights, wantSync bool
	}{
		{appconfig.ProfileFull, true, true},
		{appconfig.ProfileLights, true, false},
		{appconfig.ProfileSync, false, true},
	} {
		t.Run(string(tt.profile), func(t *testing.T) {
			s := New(cfgWithProfile(tt.profile), nil, nil, nil, nil)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
			req.RemoteAddr = "127.0.0.1:5555"
			s.mux.ServeHTTP(rec, req)

			var got configWire
			if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
				t.Fatal(err)
			}
			if got.Lights != tt.wantLights || got.Sync != tt.wantSync {
				t.Errorf("lights=%v sync=%v, want %v/%v", got.Lights, got.Sync, tt.wantLights, tt.wantSync)
			}
		})
	}
}

// The token is a credential. Only its existence may be reported.
func TestConfigAPINeverExposesToken(t *testing.T) {
	cfg := appconfig.Default()
	cfg.Auth.Mode = appconfig.AuthToken
	cfg.Auth.Token = "otter.beacon.willow"
	s := New(cfg, nil, nil, nil, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	s.mux.ServeHTTP(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, "otter.beacon.willow") {
		t.Fatalf("token leaked in /api/config response: %s", body)
	}
	if !strings.Contains(body, `"has_token":true`) {
		t.Errorf("response should report has_token=true, got %s", body)
	}
}

// Rewriting the listen address or disabling auth must not be reachable from
// the network the setting governs.
func TestConfigPatchIsLoopbackOnly(t *testing.T) {
	for _, tt := range []struct {
		name, remote string
		wantStatus   int
	}{
		{"loopback v4", "127.0.0.1:5555", http.StatusOK},
		{"loopback v6", "[::1]:5555", http.StatusOK},
		{"lan", "192.0.2.10:5555", http.StatusForbidden},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			withConfigDir(t, dir)

			s := New(appconfig.Default(), nil, nil, nil, nil)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPatch, "/api/config",
				strings.NewReader(`{"profile":"lights"}`))
			req.RemoteAddr = tt.remote
			s.mux.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status %d, want %d (%s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
		})
	}
}

// A forwarded-for header is caller-supplied; trusting it would let anything
// willing to set it claim to be local.
func TestConfigPatchIgnoresForwardedHeaders(t *testing.T) {
	dir := t.TempDir()
	withConfigDir(t, dir)

	s := New(appconfig.Default(), nil, nil, nil, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/config",
		strings.NewReader(`{"profile":"lights"}`))
	req.RemoteAddr = "192.0.2.10:5555"
	req.Header.Set("X-Forwarded-For", "127.0.0.1")
	s.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status %d, want 403 — X-Forwarded-For must not grant local access", rec.Code)
	}
}

// POST used to be accepted as a simple CORS method — no preflight, so any
// webpage could fire it at loopback. Only PATCH may change configuration now.
func TestConfigRejectsPostMethod(t *testing.T) {
	s := New(appconfig.Default(), nil, nil, nil, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/config",
		strings.NewReader(`{"profile":"lights"}`))
	req.RemoteAddr = "127.0.0.1:5555"
	s.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST rejected with %d, want %d MethodNotAllowed", rec.Code, http.StatusMethodNotAllowed)
	}
}

// A loopback PATCH from a browser carries an Origin header naming some other
// site. RemoteAddr alone cannot tell a curl invocation from an attacker's
// page, so the Origin must be ours.
func TestConfigPatchRejectsCrossOrigin(t *testing.T) {
	dir := t.TempDir()
	withConfigDir(t, dir)

	s := New(appconfig.Default(), nil, nil, nil, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/config",
		strings.NewReader(`{"profile":"lights"}`))
	req.RemoteAddr = "127.0.0.1:5555"
	req.Header.Set("Origin", "http://evil.example.com")
	s.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-origin PATCH got %d, want %d Forbidden", rec.Code, http.StatusForbidden)
	}
}

func TestConfigPatchValidatesAndPartiallyUpdates(t *testing.T) {
	dir := t.TempDir()
	withConfigDir(t, dir)

	start := appconfig.Default()
	start.Listen.Port = 9999
	s := New(start, nil, nil, nil, nil)

	patch := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPatch, "/api/config", strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:5555"
		s.mux.ServeHTTP(rec, req)
		return rec
	}

	if rec := patch(`{"profile":"lites"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid profile: status %d, want 400", rec.Code)
	}
	// A field absent from the patch must keep its value.
	if rec := patch(`{"profile":"lights"}`); rec.Code != http.StatusOK {
		t.Fatalf("valid patch failed: %d %s", rec.Code, rec.Body.String())
	}
	if got := s.Config(); got.Profile != appconfig.ProfileLights || got.Listen.Port != 9999 {
		t.Errorf("got %+v — profile should change and port should be preserved", got)
	}
}

// Changing something that only takes effect at bind time must say so rather
// than reporting success and quietly doing nothing.
func TestConfigPatchReportsRestartRequired(t *testing.T) {
	dir := t.TempDir()
	withConfigDir(t, dir)
	s := New(appconfig.Default(), nil, nil, nil, nil)

	do := func(body string) bool {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPatch, "/api/config", strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:5555"
		s.mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("patch failed: %d %s", rec.Code, rec.Body.String())
		}
		var out struct {
			RestartRequired bool `json:"restart_required"`
		}
		_ = json.NewDecoder(rec.Body).Decode(&out)
		return out.RestartRequired
	}

	if !do(`{"listen":{"host":"127.0.0.1","port":7777}}`) {
		t.Error("a listen-address change requires a restart and must say so")
	}
	if do(`{"auth":{"mode":"token","token":"a.b.c"}}`) {
		t.Error("an auth change applies live and must not demand a restart")
	}
}
