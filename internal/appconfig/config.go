// Package appconfig is huemux's application configuration: which half of the
// app to run, where to listen, and how (or whether) to authenticate.
//
// Deliberately separate from internal/config, which owns *feature data* —
// bridge credentials, per-area sync tuning, favorites. That split matters for
// more than tidiness: internal/config.SaveBridge fsyncs a file holding a
// clientkey the bridge issues exactly once and will never re-issue, so
// runtime-mutable settings must never share its write path.
//
// This package is the single schema behind every way the app can be
// configured — CLI flags, the on-disk file, the runtime API, and the settings
// UI all read and write these same types, so none of them can drift from the
// others.
package appconfig

import (
	"fmt"
	"net"
	"strings"
)

// Profile selects which half of the app runs. See NeedsEngine/NeedsLightctl
// below — the mapping to actual subsystems is not the obvious one.
type Profile string

const (
	// ProfileFull runs screen sync and light control. The default, and what
	// every build before this option existed did.
	ProfileFull Profile = "full"
	// ProfileLights runs light control only: no DTLS socket, no capture
	// plumbing, no output loop. The headless-server case.
	ProfileLights Profile = "lights"
	// ProfileSync runs screen sync only, hiding the Lights tab.
	ProfileSync Profile = "sync"
)

// AuthMode selects how (or whether) non-loopback callers authenticate.
type AuthMode string

const (
	AuthNone  AuthMode = "none"
	AuthToken AuthMode = "token"
)

// TLSMode selects how the server obtains a certificate. huemux deliberately
// issues nothing itself: "files" covers Tailscale, Let's Encrypt via DNS-01,
// and any other real certificate, and "selfsigned" covers zero-config LAN use.
type TLSMode string

const (
	TLSOff        TLSMode = "off"
	TLSSelfSigned TLSMode = "selfsigned"
	TLSFiles      TLSMode = "files"
)

// Config is the whole application configuration.
type Config struct {
	Profile Profile `json:"profile"`
	Listen  Listen  `json:"listen"`
	Auth    Auth    `json:"auth"`
	TLS     TLS     `json:"tls"`

	// AllowedHosts names extra hosts that may serve the UI, over and above
	// the ones huemux works out for itself: the listen host, this machine's
	// own addresses, and its hostname.
	//
	// The WebSocket origin check compares hostnames, not addresses, so a name
	// a browser reaches the server by — a reverse proxy's vhost, a Tailscale
	// MagicDNS name, a second alias — cannot be derived from the socket the
	// request arrived on. Without this list, serving the UI under a name has
	// meant giving up access by IP, and the other way round.
	//
	// Entries are bare hostnames or IP addresses. A pasted URL is accepted
	// and reduced to its host, so "https://lights.example:7654/" works.
	//
	// This list is not a wildcard and must not become one: an entry only ever
	// admits one name, and a foreign Origin is still rejected whether or not
	// entries are present.
	AllowedHosts []string `json:"allowed_hosts,omitempty"`
}

// Listen is the bind address. The default is loopback, and moving off it is
// the single change that turns huemux from "unreachable by anything else" into
// something that needs an auth story — see Auth.
type Listen struct {
	Host string `json:"host"`
	// Port 0 means "scan upward from DefaultPort for the first free port",
	// which is the long-standing behavior of ListenAndServe.
	Port int `json:"port"`
}

// Auth covers non-loopback callers. Loopback is exempt by default so that
// adding authentication for a LAN deployment never puts a login in front of
// ordinary desktop use.
type Auth struct {
	Mode  AuthMode `json:"mode"`
	Token string   `json:"token"`
	// AllowLoopbackUnauthenticated exempts connections originating from the
	// loopback interface. Defaults false: the passphrase is the gate, even
	// for a local browser — otherwise the login form accepts any input on
	// the default loopback bind and the passphrase protects nothing. Set
	// true if you trust every local user and want the convenience back.
	AllowLoopbackUnauthenticated bool `json:"allow_loopback_unauthenticated"`
}

// TLS is certificate configuration. CertFile/KeyFile are only read in
// TLSFiles mode.
type TLS struct {
	Mode     TLSMode `json:"mode"`
	CertFile string  `json:"cert_file"`
	KeyFile  string  `json:"key_file"`
}

// DefaultPort is the port ListenAndServe starts scanning from.
const DefaultPort = 7654

// DefaultHost is loopback: reachable from this machine and nothing else.
const DefaultHost = "127.0.0.1"

// Default returns the configuration huemux runs with when nothing is
// specified. It reproduces the behavior of every build before this package
// existed, which is what makes it safe to load unconditionally.
func Default() Config {
	return Config{
		Profile: ProfileFull,
		// Port 0, not DefaultPort: 0 means "scan upward from DefaultPort",
		// which is how huemux has always behaved — a port already in use is
		// not a reason to refuse to start. Setting an explicit port here
		// would silently turn that into a hard failure.
		Listen: Listen{Host: DefaultHost, Port: 0},
		Auth: Auth{
			Mode:                         AuthNone,
			AllowLoopbackUnauthenticated: false,
		},
		TLS: TLS{Mode: TLSOff},
	}
}

// NeedsEngine reports whether internal/engine — the DTLS stream, capture
// intake, and output loop — has to be constructed.
func (c Config) NeedsEngine() bool {
	return c.Profile == ProfileFull || c.Profile == ProfileSync
}

// NeedsLightctl reports whether internal/lightctl has to be constructed.
//
// Note this is true for ProfileSync as well, which is not obvious: the sync
// page renders its own scene strip, fetching /api/scenes and sending
// scene_recall (web/app.js). Dropping lightctl under a sync-only profile
// would silently break a feature on the very page that profile exists to
// serve. So the profiles are asymmetric on purpose — ProfileLights is a real
// resource saving, ProfileSync is a UI simplification.
func (c Config) NeedsLightctl() bool {
	return true
}

// ShowsLightsTab reports whether the light-control UI should be offered.
func (c Config) ShowsLightsTab() bool {
	return c.Profile == ProfileFull || c.Profile == ProfileLights
}

// ShowsSyncTab reports whether the screen-sync UI should be offered.
func (c Config) ShowsSyncTab() bool {
	return c.Profile == ProfileFull || c.Profile == ProfileSync
}

// ShowsPresetsTab reports whether the preset browser / node editor tab
// should be offered. It needs the music engine, same gate as sync.
func (c Config) ShowsPresetsTab() bool {
	return c.Profile == ProfileFull || c.Profile == ProfileSync
}

// LoopbackOnly reports whether the configured listen host is unreachable from
// other machines. Callers use this to decide whether the deployment needs an
// auth story at all.
func (c Config) LoopbackOnly() bool {
	return IsLoopbackHost(c.Listen.Host)
}

// IsLoopbackHost reports whether host names the loopback interface. Accepts
// the literal "localhost" as well as any loopback IP, so both ::1 and
// 127.0.0.0/8 are covered rather than just 127.0.0.1.
func IsLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// NormalizeHost reduces a host written in any of the forms a person might
// paste or type — "http://lights.example:7654/", "LIGHTS.example", "[::1]" —
// to the bare, lowercased hostname the origin comparison uses. It returns ""
// when the input names no host at all.
//
// Both sides of that comparison go through here, which is what lets a
// configured "https://lights.example/" match the "lights.example" a browser
// puts in its Origin header. Normalizing only the request would make the
// friendly forms in app.json silently ineffective.
func NormalizeHost(host string) string {
	s := strings.TrimSpace(host)
	if s == "" {
		return ""
	}
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+len("://"):]
	}
	// A trailing path, query or fragment is never part of the host. Credentials
	// ("user@host") are stripped with everything before the last '@' below.
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, "@"); i >= 0 {
		s = s[i+1:]
	}
	// SplitHostPort only recognizes a port when there is one, and rejects a
	// bare IPv6 literal without brackets — which is exactly the distinction
	// wanted here, so its error is a valid answer rather than a failure.
	if h, _, err := net.SplitHostPort(s); err == nil {
		s = h
	}
	return strings.ToLower(strings.Trim(s, "[]"))
}

// SplitHosts parses a comma-separated host list — the form the
// --allowed-hosts flag takes — dropping blanks so that "a,,b," is two hosts
// rather than four. Entries are not validated here; Validate does that, so a
// typo is an error at startup rather than a silently ignored entry.
func SplitHosts(list string) []string {
	var out []string
	for _, part := range strings.Split(list, ",") {
		if h := strings.TrimSpace(part); h != "" {
			out = append(out, h)
		}
	}
	return out
}

// Validate reports the first problem with c, or nil. Unknown enum values are
// errors rather than silently falling back to a default: a typo'd profile
// should stop the process, not quietly run the wrong half of the app.
func (c Config) Validate() error {
	switch c.Profile {
	case ProfileFull, ProfileLights, ProfileSync:
	default:
		return fmt.Errorf("unknown profile %q (want %q, %q or %q)",
			c.Profile, ProfileFull, ProfileLights, ProfileSync)
	}

	if c.Listen.Host == "" {
		return fmt.Errorf("listen host is empty (use %q for loopback-only)", DefaultHost)
	}
	// A hostname is allowed — it may resolve to an interface address — but an
	// IP that isn't parseable and isn't a plausible hostname is a typo.
	if net.ParseIP(c.Listen.Host) == nil && !plausibleHostname(c.Listen.Host) {
		return fmt.Errorf("listen host %q is neither an IP address nor a valid hostname", c.Listen.Host)
	}
	if c.Listen.Port < 0 || c.Listen.Port > 65535 {
		return fmt.Errorf("listen port %d out of range 0-65535 (0 means auto-select)", c.Listen.Port)
	}

	// An entry that reduces to nothing, or to something that is neither an IP
	// nor a hostname, is a typo. It would also be invisible: the entry simply
	// never matches an Origin, so the page the operator is trying to reach
	// stays empty with no clue why.
	for _, h := range c.AllowedHosts {
		n := NormalizeHost(h)
		if n == "" {
			return fmt.Errorf("allowed_hosts contains an empty entry")
		}
		if net.ParseIP(n) == nil && !plausibleHostname(n) {
			return fmt.Errorf("allowed_hosts entry %q is neither an IP address nor a valid hostname", h)
		}
	}

	switch c.Auth.Mode {
	case AuthNone, AuthToken:
	default:
		return fmt.Errorf("unknown auth mode %q (want %q or %q)", c.Auth.Mode, AuthNone, AuthToken)
	}
	// TrimSpace, not just == "": a whitespace-only token passes an empty check
	// and yet locks everyone out with a "password" that is invisible to type —
	// the classic self-inflicted lockout the UI's lock button exists to avoid.
	if c.Auth.Mode == AuthToken && strings.TrimSpace(c.Auth.Token) == "" {
		return fmt.Errorf("auth mode is %q but no token is set", AuthToken)
	}

	switch c.TLS.Mode {
	case TLSOff, TLSSelfSigned:
	case TLSFiles:
		if c.TLS.CertFile == "" || c.TLS.KeyFile == "" {
			return fmt.Errorf("tls mode is %q but cert_file and/or key_file is empty", TLSFiles)
		}
	default:
		return fmt.Errorf("unknown tls mode %q (want %q, %q or %q)",
			c.TLS.Mode, TLSOff, TLSSelfSigned, TLSFiles)
	}

	return nil
}

// Warnings returns non-fatal configuration concerns worth printing at
// startup. Separate from Validate because these are deployments that work
// but that the operator probably wants to know about — most importantly,
// exposing the server beyond loopback with nothing in front of it.
func (c Config) Warnings() []string {
	var w []string
	if !c.LoopbackOnly() {
		if c.Auth.Mode == AuthNone {
			w = append(w, fmt.Sprintf(
				"listening on %s with auth disabled — anything that can reach this host can control your lights",
				c.Listen.Host))
		}
		if c.TLS.Mode == TLSOff && c.Auth.Mode == AuthToken {
			w = append(w, "auth token is enabled but TLS is off — the token crosses the network in cleartext")
		}
	}
	return w
}

// plausibleHostname is a deliberately loose check: it rejects obvious typos
// (embedded spaces, slashes, a stray scheme or port) without trying to be a
// full RFC 1123 validator, since the real test is whether the bind succeeds.
func plausibleHostname(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	if strings.ContainsAny(h, " \t/\\:@") {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" {
			return false
		}
		for _, r := range label {
			isAlnum := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
			if !isAlnum && r != '-' && r != '_' {
				return false
			}
		}
	}
	return true
}
