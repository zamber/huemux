package server

import (
	"encoding/json"
	"net"
	"net/http"

	"github.com/zamber/huemux/internal/appconfig"
	"github.com/zamber/huemux/internal/config"
)

// configWire is what /api/config exposes. Deliberately not appconfig.Config
// itself: the UI needs derived answers ("should there be a Lights tab?") that
// it should not be deducing from a profile string on its own, and the auth
// token must never be handed to a caller that could be a page in someone
// else's browser.
type configWire struct {
	Profile string `json:"profile"`

	// Features the UI should offer. Derived server-side so the rule for what
	// a profile means lives in exactly one place — appconfig — rather than
	// being re-implemented in JavaScript and drifting from it.
	Lights bool `json:"lights"`
	Sync   bool `json:"sync"`

	Listen struct {
		Host string `json:"host"`
		Port int    `json:"port"`
	} `json:"listen"`

	Auth struct {
		Mode string `json:"mode"`
		// HasToken rather than the token itself. Whether one is configured is
		// useful to the settings screen; the value is a credential.
		HasToken bool `json:"has_token"`
	} `json:"auth"`

	TLS struct {
		Mode string `json:"mode"`
	} `json:"tls"`

	// Editable reports whether this caller may PATCH. False for non-loopback
	// callers, so the settings screen can render read-only instead of
	// offering controls that would 403.
	Editable bool `json:"editable"`
}

// handleConfig serves the effective configuration, and accepts changes to it
// from loopback callers.
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.writeConfig(w, r)
	case http.MethodPatch:
		s.patchConfig(w, r)
	default:
		w.Header().Set("Allow", "GET, PATCH")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) writeConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.Config()
	var out configWire
	out.Profile = string(cfg.Profile)
	out.Lights = cfg.ShowsLightsTab()
	out.Sync = cfg.ShowsSyncTab()
	out.Listen.Host = cfg.Listen.Host
	out.Listen.Port = cfg.Listen.Port
	out.Auth.Mode = string(cfg.Auth.Mode)
	out.Auth.HasToken = cfg.Auth.Token != ""
	out.TLS.Mode = string(cfg.TLS.Mode)
	out.Editable = isLoopbackRequest(r)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// patchConfig applies a partial update and persists it.
//
// Loopback callers are always trusted — they have physical access to the
// machine. Non-loopback callers are allowed only when auth.mode is "token" and
// the request carries a valid token.
//
// POST was dropped from the allowed methods: PATCH triggers a CORS preflight
// (unlike POST, which is a "simple method"), so a malicious cross-origin page
// cannot issue a PATCH without the browser first sending an OPTIONS request
// that this server rejects by not setting any CORS response headers. The
// token requirement is defense-in-depth for non-browser clients and for cases
// where the security model's primary control (loopback binding) is relaxed.
func (s *Server) patchConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.Config()
	if !isLoopbackRequest(r) {
		if cfg.Auth.Mode != appconfig.AuthToken || !appconfig.TokenMatches(requestToken(r), cfg.Auth.Token) {
			http.Error(w, "configuration can only be changed from the local machine", http.StatusForbidden)
			return
		}
	}

	// Decode over the current config so omitted fields keep their value —
	// the same partial-update behavior appconfig.Load relies on.
	current := s.Config()
	updated := current
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&updated); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := updated.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	dir, err := config.Dir()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := appconfig.Save(dir, updated); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Listen address and TLS are fixed at bind time, and the profile decides
	// which services were constructed at startup. Rather than pretend a live
	// swap happened, persist and report what still needs a restart — a
	// settings screen that silently lies about having applied something is
	// worse than one that asks you to restart.
	restart := updated.Listen != current.Listen ||
		updated.TLS != current.TLS ||
		updated.Profile != current.Profile

	s.setConfig(updated)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":               true,
		"restart_required": restart,
	})
}

// isLoopbackRequest reports whether r arrived over the loopback interface.
//
// Uses RemoteAddr, never a forwarded-for header: those are caller-supplied
// strings and trusting one here would let anything behind a proxy — or
// anything willing to set the header — claim to be local.
func isLoopbackRequest(r *http.Request) bool {
	host, _, err := splitHostPortLenient(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// splitHostPortLenient splits an address that may or may not carry a port,
// which RemoteAddr does not actually guarantee.
func splitHostPortLenient(addr string) (string, string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr, "", err
	}
	return host, port, nil
}
