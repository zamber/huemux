package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zamber/huemux/internal/appconfig"
	"github.com/zamber/huemux/internal/debuglog"
)

func getDiag(s *Server, remote string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/diagnostics", nil)
	req.RemoteAddr = remote
	s.mux.ServeHTTP(rec, req)
	return rec
}

// The report exists to be pasted into a public issue, so the token must never
// appear in it — only whether one is configured.
func TestDiagnosticsNeverIncludesToken(t *testing.T) {
	cfg := appconfig.Default()
	cfg.Auth.Mode = appconfig.AuthToken
	cfg.Auth.Token = "otter.beacon.willow"
	s := New(cfg, nil, nil, nil, nil)

	body := getDiag(s, "127.0.0.1:1").Body.String()
	if strings.Contains(body, "otter.beacon.willow") {
		t.Fatal("diagnostics leaked the auth token")
	}
	if !strings.Contains(body, "auth token set") || !strings.Contains(body, "true") {
		t.Errorf("should report that a token is set; got:\n%s", body)
	}
}

// It carries the bridge address and recent log lines, so it is not something
// any device on the network should be able to pull.
func TestDiagnosticsIsLoopbackOnly(t *testing.T) {
	s := New(appconfig.Default(), nil, nil, nil, nil)
	if rec := getDiag(s, "127.0.0.1:1"); rec.Code != http.StatusOK {
		t.Errorf("loopback: status %d, want 200", rec.Code)
	}
	if rec := getDiag(s, "192.0.2.10:1"); rec.Code != http.StatusForbidden {
		t.Errorf("LAN: status %d, want 403", rec.Code)
	}
}

// Served as a download so Android's WebView download listener can hand it to
// the system share sheet — on a phone that is the only route off the device.
func TestDiagnosticsIsOfferedAsAFile(t *testing.T) {
	s := New(appconfig.Default(), nil, nil, nil, nil)
	rec := getDiag(s, "127.0.0.1:1")
	cd := rec.Header().Get("Content-Disposition")
	if !strings.HasPrefix(cd, "attachment;") || !strings.Contains(cd, ".txt") {
		t.Errorf("Content-Disposition = %q, want an attachment with a .txt name", cd)
	}
}

func TestDiagnosticsReportsCoreState(t *testing.T) {
	s := New(cfgWithProfile(appconfig.ProfileLights), nil, nil, nil, nil)
	body := getDiag(s, "127.0.0.1:1").Body.String()
	for _, want := range []string{
		"HueMux diagnostics", "profile:", "paired:", "recent log", "-- end of report --",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("report missing %q", want)
		}
	}
}

// The host block is how the Android half of the app gets into a report at all.
// Everything about MediaProjection, the capture display and the video encoder
// lives in Kotlin, so without this a report from a phone where recording had
// failed described a Go process that believed all was well and said nothing
// whatsoever about recording.
func TestDiagnosticsIncludesHostBlock(t *testing.T) {
	t.Cleanup(func() { SetHostInfo("") })

	s := New(appconfig.Default(), nil, nil, nil, nil)
	if body := getDiag(s, "127.0.0.1:1").Body.String(); strings.Contains(body, "\nhost\n") {
		t.Errorf("host section should be absent when nothing set it; got:\n%s", body)
	}

	SetHostInfo("capture               running 234x480 scale=0.2\nrecording             stopped\n")
	body := getDiag(s, "127.0.0.1:1").Body.String()
	if !strings.Contains(body, "\nhost\n----\n") {
		t.Errorf("host section missing; got:\n%s", body)
	}
	if !strings.Contains(body, "running 234x480 scale=0.2") {
		t.Errorf("host text missing; got:\n%s", body)
	}
	// It must land before the log, so a reader sees the state that produced
	// the lines before reading them.
	if strings.Index(body, "\nhost\n") > strings.Index(body, "\nrecent log\n") {
		t.Error("host section should precede the log")
	}
}

// Host lines share the ring with the Go ones so the two interleave in order —
// that ordering is the whole diagnostic value of having them in one report.
func TestHostLinesAppearInTheLog(t *testing.T) {
	t.Cleanup(func() { SetHostInfo("") })
	debuglog.Note("huemux/host: record: encoder configure failed 234x480")

	s := New(appconfig.Default(), nil, nil, nil, nil)
	body := getDiag(s, "127.0.0.1:1").Body.String()
	if !strings.Contains(body, "record: encoder configure failed 234x480") {
		t.Errorf("host log line missing from report; got:\n%s", body)
	}
}
