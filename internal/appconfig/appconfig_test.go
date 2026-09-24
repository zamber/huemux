package appconfig

import (
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestDefaultIsValidAndLoopback(t *testing.T) {
	cfg := Default()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Default() must be valid, got %v", err)
	}
	if !cfg.LoopbackOnly() {
		t.Error("Default() must bind loopback only — anything else is a security regression for existing installs")
	}
	if cfg.Auth.Mode != AuthNone {
		t.Errorf("Default() auth = %q, want %q: adding auth by default would break every existing desktop install", cfg.Auth.Mode, AuthNone)
	}
	if cfg.Auth.AllowLoopbackUnauthenticated {
		t.Error("Default() must not exempt loopback from a configured passphrase — a loopback-exempt login accepts any input")
	}
	if !cfg.NeedsEngine() || !cfg.NeedsLightctl() {
		t.Error("Default() must run both halves, matching pre-appconfig behavior")
	}
	if len(cfg.Warnings()) != 0 {
		t.Errorf("Default() should warn about nothing, got %v", cfg.Warnings())
	}
}

func TestProfileCapabilities(t *testing.T) {
	tests := []struct {
		profile                              Profile
		engine, lightctl, lightsTab, syncTab bool
	}{
		{ProfileFull, true, true, true, true},
		{ProfileLights, false, true, true, false},
		// Sync keeps lightctl on purpose: the sync page's scene strip calls
		// /api/scenes and sends scene_recall. See NeedsLightctl.
		{ProfileSync, true, true, false, true},
	}
	for _, tt := range tests {
		t.Run(string(tt.profile), func(t *testing.T) {
			c := Config{Profile: tt.profile}
			if got := c.NeedsEngine(); got != tt.engine {
				t.Errorf("NeedsEngine() = %v, want %v", got, tt.engine)
			}
			if got := c.NeedsLightctl(); got != tt.lightctl {
				t.Errorf("NeedsLightctl() = %v, want %v", got, tt.lightctl)
			}
			if got := c.ShowsLightsTab(); got != tt.lightsTab {
				t.Errorf("ShowsLightsTab() = %v, want %v", got, tt.lightsTab)
			}
			if got := c.ShowsSyncTab(); got != tt.syncTab {
				t.Errorf("ShowsSyncTab() = %v, want %v", got, tt.syncTab)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	valid := Default()
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{"default", func(*Config) {}, ""},
		{"unknown profile", func(c *Config) { c.Profile = "lites" }, "unknown profile"},
		{"empty host", func(c *Config) { c.Listen.Host = "" }, "listen host is empty"},
		{"host with scheme", func(c *Config) { c.Listen.Host = "http://x" }, "neither an IP address nor a valid hostname"},
		{"host with port", func(c *Config) { c.Listen.Host = "host:1234" }, "neither an IP address nor a valid hostname"},
		{"negative port", func(c *Config) { c.Listen.Port = -1 }, "out of range"},
		{"huge port", func(c *Config) { c.Listen.Port = 70000 }, "out of range"},
		{"port zero is auto", func(c *Config) { c.Listen.Port = 0 }, ""},
		{"ipv6 host", func(c *Config) { c.Listen.Host = "::1" }, ""},
		{"hostname host", func(c *Config) { c.Listen.Host = "lights.example" }, ""},
		{"unknown auth", func(c *Config) { c.Auth.Mode = "password" }, "unknown auth mode"},
		{"token mode without token", func(c *Config) { c.Auth.Mode = AuthToken }, "no token is set"},
		{"token mode with whitespace-only token", func(c *Config) { c.Auth.Mode = AuthToken; c.Auth.Token = "   " }, "no token is set"},
		{"token mode with token", func(c *Config) { c.Auth.Mode = AuthToken; c.Auth.Token = "a.b.c" }, ""},
		{"token mode with padded token", func(c *Config) { c.Auth.Mode = AuthToken; c.Auth.Token = "  a.b.c  " }, ""},
		{"unknown tls", func(c *Config) { c.TLS.Mode = "acme" }, "unknown tls mode"},
		{"tls files without paths", func(c *Config) { c.TLS.Mode = TLSFiles }, "cert_file and/or key_file is empty"},
		{"tls files with paths", func(c *Config) {
			c.TLS.Mode = TLSFiles
			c.TLS.CertFile = "/c"
			c.TLS.KeyFile = "/k"
		}, ""},
		{"allowed host by name", func(c *Config) { c.AllowedHosts = []string{"lights.example"} }, ""},
		{"allowed host by ip", func(c *Config) { c.AllowedHosts = []string{"192.0.2.10"} }, ""},
		{"allowed host as a pasted url", func(c *Config) {
			c.AllowedHosts = []string{"https://lights.example:7654/"}
		}, ""},
		// An entry that reduces to nothing would be invisible: it never
		// matches an Origin, so the page the operator is trying to reach stays
		// empty with no clue why.
		{"allowed host empty entry", func(c *Config) { c.AllowedHosts = []string{""} }, "empty entry"},
		{"allowed host whitespace entry", func(c *Config) { c.AllowedHosts = []string{"   "} }, "empty entry"},
		// A bare host:port is the other thing an operator copies out of a
		// browser; the port is dropped, which matches the Origin check
		// ignoring ports anyway.
		{"allowed host with port", func(c *Config) { c.AllowedHosts = []string{"lights.example:7654"} }, ""},
		{"allowed host typo", func(c *Config) { c.AllowedHosts = []string{"lights example"} }, "neither an IP address nor a valid hostname"},
		{"allowed host with scheme only", func(c *Config) { c.AllowedHosts = []string{"http://"} }, "empty entry"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid
			tt.mutate(&cfg)
			err := cfg.Validate()
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("want valid, got error %v", err)
			case tt.wantErr != "" && err == nil:
				t.Fatalf("want error containing %q, got nil", tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Fatalf("want error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestIsLoopbackHost(t *testing.T) {
	for _, h := range []string{"127.0.0.1", "127.0.0.53", "localhost", "LOCALHOST", "::1"} {
		if !IsLoopbackHost(h) {
			t.Errorf("IsLoopbackHost(%q) = false, want true", h)
		}
	}
	for _, h := range []string{"0.0.0.0", "192.0.2.10", "lights.example", "", "::"} {
		if IsLoopbackHost(h) {
			t.Errorf("IsLoopbackHost(%q) = true, want false", h)
		}
	}
}

// Both sides of the Origin comparison go through NormalizeHost, so this table
// is also the statement of which spellings an allowed_hosts entry may take.
func TestNormalizeHost(t *testing.T) {
	tests := []struct{ in, want string }{
		{"lights.example", "lights.example"},
		{"LIGHTS.example", "lights.example"},
		{"  lights.example  ", "lights.example"},
		{"https://lights.example", "lights.example"},
		{"https://lights.example:7654/", "lights.example"},
		{"http://lights.example/path?q=1#frag", "lights.example"},
		{"lights.example:7654", "lights.example"},
		// Credentials are not part of the host.
		{"https://user:pw@lights.example", "lights.example"},
		// IPv6 literals keep their brackets off and their colons intact.
		{"[::1]", "::1"},
		{"http://[2001:db8::1]:7654", "2001:db8::1"},
		// A bare IPv6 literal has no port to split, so SplitHostPort's error
		// is the right answer rather than a failure.
		{"2001:db8::1", "2001:db8::1"},
		{"192.0.2.10:7654", "192.0.2.10"},
		// Nothing that names a host.
		{"", ""},
		{"   ", ""},
		{"https://", ""},
		{"/just/a/path", ""},
	}
	for _, tt := range tests {
		if got := NormalizeHost(tt.in); got != tt.want {
			t.Errorf("NormalizeHost(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSplitHosts(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"lights.example", []string{"lights.example"}},
		{"a.example,b.example", []string{"a.example", "b.example"}},
		// Blanks are dropped, not carried through as empty entries that would
		// fail Validate and refuse to start.
		{"a.example,,b.example,", []string{"a.example", "b.example"}},
		{" a.example , b.example ", []string{"a.example", "b.example"}},
		{",", nil},
	}
	for _, tt := range tests {
		got := SplitHosts(tt.in)
		if len(got) != len(tt.want) {
			t.Errorf("SplitHosts(%q) = %v, want %v", tt.in, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("SplitHosts(%q) = %v, want %v", tt.in, got, tt.want)
				break
			}
		}
	}
}

func TestWarnsWhenExposedWithoutAuth(t *testing.T) {
	cfg := Default()
	cfg.Listen.Host = "0.0.0.0"
	got := strings.Join(cfg.Warnings(), " ")
	if !strings.Contains(got, "auth disabled") {
		t.Errorf("exposing the server with no auth must warn, got %q", got)
	}

	cfg.Auth.Mode = AuthToken
	cfg.Auth.Token = "a.b.c"
	got = strings.Join(cfg.Warnings(), " ")
	if !strings.Contains(got, "cleartext") {
		t.Errorf("token over plain HTTP must warn, got %q", got)
	}

	cfg.TLS.Mode = TLSSelfSigned
	if w := cfg.Warnings(); len(w) != 0 {
		t.Errorf("token + TLS should warn about nothing, got %v", w)
	}
}

// --- store ---------------------------------------------------------------

func TestLoadMissingFileYieldsDefaults(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load on empty dir: %v", err)
	}
	// reflect.DeepEqual, not ==: Config gained a slice (AllowedHosts), so it
	// is no longer comparable.
	if !reflect.DeepEqual(cfg, Default()) {
		t.Errorf("Load on empty dir = %+v, want Default()", cfg)
	}
	// Loading must not create the file — a fresh install should not sprout
	// config it never asked for.
	if _, err := os.Stat(filepath.Join(dir, FileName)); !os.IsNotExist(err) {
		t.Error("Load must not create the config file as a side effect")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := Config{
		Profile: ProfileLights,
		Listen:  Listen{Host: "0.0.0.0", Port: 9000},
		Auth:    Auth{Mode: AuthToken, Token: "otter.beacon.willow", AllowLoopbackUnauthenticated: true},
		TLS:     TLS{Mode: TLSFiles, CertFile: "/c.pem", KeyFile: "/k.pem"},
		// Included so the one list-valued setting is actually exercised by the
		// round trip rather than assumed to encode.
		AllowedHosts: []string{"lights.example", "192.0.2.10"},
	}
	if err := Save(dir, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// DeepEqual, not ==: Config contains a slice and is no longer comparable.
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip mismatch:\n got %+v\nwant %+v", got, want)
	}
}

func TestSavedFileIsNotWorldReadable(t *testing.T) {
	dir := t.TempDir()
	cfg := Default()
	cfg.Auth.Mode = AuthToken
	cfg.Auth.Token = "otter.beacon.willow"
	if err := Save(dir, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fi, err := os.Stat(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("config mode = %o, want no group/other bits: it holds the auth token", perm)
	}
}

func TestSaveRejectsInvalid(t *testing.T) {
	dir := t.TempDir()
	bad := Default()
	bad.Profile = "nope"
	if err := Save(dir, bad); err == nil {
		t.Fatal("Save must reject an invalid config rather than persist it")
	}
	if _, err := os.Stat(filepath.Join(dir, FileName)); !os.IsNotExist(err) {
		t.Error("a rejected Save must not leave a file behind")
	}
	// And no stray temp files from the aborted write.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("rejected Save left files behind: %v", entries)
	}
}

func TestPartialFileKeepsOtherDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte(`{"profile":"lights"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Profile != ProfileLights {
		t.Errorf("profile = %q, want %q", cfg.Profile, ProfileLights)
	}
	// Port 0 is the default and means "scan upward from DefaultPort" — see
	// Default(). Asserting DefaultPort here would re-encode the behaviour that
	// made a taken port a hard startup failure.
	if cfg.Listen.Host != DefaultHost || cfg.Listen.Port != 0 {
		t.Errorf("listen = %+v, want defaults preserved for keys absent from the file", cfg.Listen)
	}
	if cfg.Auth.AllowLoopbackUnauthenticated {
		t.Error("allow_loopback_unauthenticated should keep its false default when absent from the file")
	}
}

func TestLoadMalformedFileErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("malformed config must be an error, not a silent fallback to defaults")
	}
}

// --- precedence ----------------------------------------------------------

func TestResolvePrecedence(t *testing.T) {
	dir := t.TempDir()
	onDisk := Default()
	onDisk.Profile = ProfileLights
	onDisk.Listen.Port = 8000
	if err := Save(dir, onDisk); err != nil {
		t.Fatal(err)
	}

	t.Run("file wins over defaults", func(t *testing.T) {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		f := RegisterFlags(fs)
		if err := fs.Parse(nil); err != nil {
			t.Fatal(err)
		}
		cfg, err := Resolve(dir, f)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Profile != ProfileLights || cfg.Listen.Port != 8000 {
			t.Errorf("got %+v, want the on-disk values", cfg)
		}
	})

	t.Run("explicit flag wins over file", func(t *testing.T) {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		f := RegisterFlags(fs)
		if err := fs.Parse([]string{"--" + FlagProfile + "=sync"}); err != nil {
			t.Fatal(err)
		}
		cfg, err := Resolve(dir, f)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Profile != ProfileSync {
			t.Errorf("profile = %q, want the flag to win", cfg.Profile)
		}
		// The regression this whole Visit-based design exists to prevent:
		// an unset flag must not clobber the file with its zero value.
		if cfg.Listen.Port != 8000 {
			t.Errorf("port = %d, want 8000 — an unset flag overwrote a file value", cfg.Listen.Port)
		}
	})

	t.Run("bad flag value is rejected", func(t *testing.T) {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		f := RegisterFlags(fs)
		if err := fs.Parse([]string{"--" + FlagProfile + "=bogus"}); err != nil {
			t.Fatal(err)
		}
		if _, err := Resolve(dir, f); err == nil {
			t.Fatal("Resolve must reject an invalid profile from a flag")
		}
	})
}

func TestTokenFlagImpliesTokenAuth(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	f := RegisterFlags(fs)
	if err := fs.Parse([]string{"--" + FlagToken + "=otter.beacon.willow"}); err != nil {
		t.Fatal(err)
	}
	cfg := f.Apply(Default())
	if cfg.Auth.Mode != AuthToken {
		t.Errorf("auth mode = %q, want %q: passing a token should enable token auth", cfg.Auth.Mode, AuthToken)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("config should be valid, got %v", err)
	}
}

func TestAllowedHostsFlagAndFile(t *testing.T) {
	dir := t.TempDir()
	onDisk := Default()
	onDisk.AllowedHosts = []string{"from-file.example"}
	if err := Save(dir, onDisk); err != nil {
		t.Fatal(err)
	}

	// Unset must not clobber the file's list with nil — the same
	// Visit-based precedence the port relies on.
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	f := RegisterFlags(fs)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	cfg, err := Resolve(dir, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.AllowedHosts) != 1 || cfg.AllowedHosts[0] != "from-file.example" {
		t.Errorf("AllowedHosts = %v, want the file's entry", cfg.AllowedHosts)
	}

	// An explicit flag replaces the list, and survives validation in the
	// comma-separated form the operator actually types.
	fs = flag.NewFlagSet("t", flag.ContinueOnError)
	f = RegisterFlags(fs)
	if err := fs.Parse([]string{"--" + FlagAllowedHosts + "=a.example, b.example"}); err != nil {
		t.Fatal(err)
	}
	cfg, err = Resolve(dir, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.AllowedHosts) != 2 || cfg.AllowedHosts[0] != "a.example" || cfg.AllowedHosts[1] != "b.example" {
		t.Errorf("AllowedHosts = %v, want the two flag entries", cfg.AllowedHosts)
	}
}

// --- token ---------------------------------------------------------------

func TestGenerateToken(t *testing.T) {
	tok, err := GenerateToken(DefaultTokenWords)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	parts := strings.Split(tok, TokenSeparator)
	if len(parts) != DefaultTokenWords {
		t.Fatalf("token %q has %d words, want %d", tok, len(parts), DefaultTokenWords)
	}
	known := map[string]bool{}
	for _, w := range tokenWords {
		known[w] = true
	}
	for _, p := range parts {
		if !known[p] {
			t.Errorf("token word %q is not in the wordlist", p)
		}
	}
	// URL-safe: it travels as ?token= because WebSocket can't set headers.
	if tok != strings.TrimSpace(tok) || strings.ContainsAny(tok, " /?&=#%+") {
		t.Errorf("token %q must be safe in a URL query parameter without escaping", tok)
	}
}

func TestGenerateTokenRejectsTooFewWords(t *testing.T) {
	for _, n := range []int{-1, 0, 1, 2} {
		if _, err := GenerateToken(n); err == nil {
			t.Errorf("GenerateToken(%d) must fail: that is not a credential", n)
		}
	}
}

func TestGenerateTokenVaries(t *testing.T) {
	seen := map[string]int{}
	const iterations = 200
	for i := 0; i < iterations; i++ {
		tok, err := GenerateToken(DefaultTokenWords)
		if err != nil {
			t.Fatal(err)
		}
		seen[tok]++
	}
	// With ~26 bits of entropy, 200 draws colliding at all would be
	// astronomically unlikely; anything less than near-total uniqueness means
	// the random source or the indexing is broken.
	if len(seen) < iterations-1 {
		t.Errorf("only %d unique tokens from %d draws — generation is not random", len(seen), iterations)
	}
}

func TestTokenEntropy(t *testing.T) {
	if got := TokenEntropyBits(DefaultTokenWords); got < 24 {
		t.Errorf("default token entropy = %.1f bits, want at least 24", got)
	}
	if TokenEntropyBits(5) <= TokenEntropyBits(3) {
		t.Error("more words must mean more entropy")
	}
	if got := TokenEntropyBits(0); got != 0 {
		t.Errorf("TokenEntropyBits(0) = %v, want 0", got)
	}
}

func TestTokenMatches(t *testing.T) {
	if !TokenMatches("otter.beacon.willow", "otter.beacon.willow") {
		t.Error("identical tokens must match")
	}
	if TokenMatches("otter.beacon.willo", "otter.beacon.willow") {
		t.Error("differing tokens must not match")
	}
	if TokenMatches("", "") {
		t.Error("an empty configured token must never match — that would mean auth-on-with-no-credential accepts everything")
	}
	if TokenMatches("anything", "") {
		t.Error("an empty configured token must never match")
	}
}

// --- wordlist ------------------------------------------------------------

// TestWordlistProperties enforces the constraints the wordlist doc comment
// claims. It exists because an earlier revision of that list quietly violated
// its own stated rules.
func TestWordlistProperties(t *testing.T) {
	if len(tokenWords) < 256 {
		t.Fatalf("wordlist has %d words; too small for a usable token", len(tokenWords))
	}

	shape := regexp.MustCompile(`^[a-z]{3,8}$`)
	seen := map[string]bool{}
	for _, w := range tokenWords {
		if !shape.MatchString(w) {
			t.Errorf("word %q must be 3-8 lowercase ASCII letters", w)
		}
		if seen[w] {
			t.Errorf("word %q appears twice", w)
		}
		seen[w] = true
	}

	// No entry may be another entry with a plural "s" — spoken aloud they are
	// a coin flip.
	for _, w := range tokenWords {
		if strings.HasSuffix(w, "s") && seen[strings.TrimSuffix(w, "s")] {
			t.Errorf("word %q is the plural of another entry", w)
		}
	}
}

// TestDefaultPortIsAutoSelect pins the reason the default is 0 rather than
// DefaultPort: huemux has always fallen through to the next free port when
// 7654 was taken, and an explicit default would have quietly turned that into
// a refusal to start.
func TestDefaultPortIsAutoSelect(t *testing.T) {
	if got := Default().Listen.Port; got != 0 {
		t.Errorf("default port = %d, want 0 (auto-select from %d)", got, DefaultPort)
	}
	if err := Default().Validate(); err != nil {
		t.Errorf("port 0 must be valid: %v", err)
	}
}
