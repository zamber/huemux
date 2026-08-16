package server

import (
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"

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
	Lights  bool `json:"lights"`
	Sync    bool `json:"sync"`
	Presets bool `json:"presets"`

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

	// ShareControl is true when the server binds all interfaces (0.0.0.0 or ::).
	ShareControl bool `json:"share_control"`

	// LanAddresses holds this machine's non-loopback unicast IPs, for the
	// settings UI to preview LAN-reachable URLs when sharing is on.
	LanAddresses []string `json:"lan_addresses"`
}

// handleConfig serves the effective configuration, and accepts changes to it
// from loopback callers.
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.writeConfig(w, r)
	case http.MethodPatch, http.MethodPost:
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
	out.Presets = cfg.ShowsPresetsTab()
	out.Listen.Host = displayHost(cfg.Listen.Host)
	out.Listen.Port = listenPort(s.Addr)
	out.Auth.Mode = string(cfg.Auth.Mode)
	out.Auth.HasToken = strings.TrimSpace(cfg.Auth.Token) != ""
	out.TLS.Mode = string(cfg.TLS.Mode)
	out.Editable = isLoopbackRequest(r)
	out.ShareControl = isWildcardHost(cfg.Listen.Host)

	lanIPs := LocalAddresses()
	out.LanAddresses = make([]string, len(lanIPs))
	for i, ip := range lanIPs {
		out.LanAddresses[i] = ip.String()
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// patchConfig applies a partial update and persists it.
//
// Loopback-only, and not because of the auth layer — this predates it and
// stands on its own. Rewriting the listen address or disabling authentication
// is exactly the operation you would not want reachable from the network it
// governs, so the check is on where the request physically came from rather
// than on a credential that could be replayed.
func (s *Server) patchConfig(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r) {
		http.Error(w, "configuration can only be changed from the local machine", http.StatusForbidden)
		return
	}

	// Decode over the current config so omitted fields keep their value —
	// the same partial-update behavior appconfig.Load relies on.
	current := s.Config()
	updated := current
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&updated); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	// A token with surrounding whitespace is almost never the intended
	// credential — it is a paste artifact that silently locks everyone out.
	// Normalize on write so a saved token is exactly what will be asked for.
	updated.Auth.Token = strings.TrimSpace(updated.Auth.Token)
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

	listenChanged := updated.Listen != current.Listen ||
		updated.TLS != current.TLS

	s.setConfig(updated)

	resp := map[string]any{"ok": true}

	if listenChanged {
		newURL, err := s.RestartListener(updated)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		resp["new_url"] = newURL
	}

	// Broadcast to all connected WS clients so every frame can update its
	// features/auth state without a reload.
	go s.broadcastConfigChanged()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
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

// listenPort extracts the port number from a "host:port" address string.
// Returns 0 on parse failure.
func listenPort(addr string) int {
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return 0
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0
	}
	return port
}
