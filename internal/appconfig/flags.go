package appconfig

import (
	"flag"
	"fmt"
)

// Flags binds the config schema to a flag.FlagSet. It exists so the CLI
// surface is generated from the same struct the file and the runtime API use,
// rather than being a parallel list that drifts from them.
//
// The reason this is not just "parse flags into a Config" is precedence: a
// flag left unset must not clobber a value from the file with its zero value.
// Go's flag package has no notion of "was this provided", so Apply consults
// FlagSet.Visit, which reports only the flags actually seen on the command
// line.
type Flags struct {
	fs *flag.FlagSet

	profile  string
	host     string
	port     int
	authMode string
	token    string
	tlsMode  string
	certFile string
	keyFile  string
}

// Flag names, exported so callers can reference them in help text and tests
// without restating string literals.
const (
	FlagProfile  = "profile"
	FlagHost     = "listen-host"
	FlagPort     = "listen-port"
	FlagAuthMode = "auth"
	FlagToken    = "token"
	FlagTLSMode  = "tls"
	FlagCertFile = "tls-cert"
	FlagKeyFile  = "tls-key"
)

// RegisterFlags adds huemux's configuration flags to fs and returns the
// binding to pass to Apply after fs.Parse.
//
// Defaults are deliberately the zero value rather than Default()'s values:
// the printed default would otherwise claim a value that the file may
// legitimately override, and Apply only reads flags that were explicitly set
// anyway.
func RegisterFlags(fs *flag.FlagSet) *Flags {
	f := &Flags{fs: fs}
	fs.StringVar(&f.profile, FlagProfile, "", "which half of the app to run: full, lights, or sync")
	fs.StringVar(&f.host, FlagHost, "", "address to bind (default 127.0.0.1; anything else needs an auth story)")
	fs.IntVar(&f.port, FlagPort, 0, "port to bind (default 7654, scanning upward if taken)")
	fs.StringVar(&f.authMode, FlagAuthMode, "", "authentication for non-loopback callers: none or token")
	fs.StringVar(&f.token, FlagToken, "", "auth token; generated and printed on first non-loopback start if unset")
	fs.StringVar(&f.tlsMode, FlagTLSMode, "", "TLS: off, selfsigned, or files")
	fs.StringVar(&f.certFile, FlagCertFile, "", "certificate path (tls=files)")
	fs.StringVar(&f.keyFile, FlagKeyFile, "", "private key path (tls=files)")
	return f
}

// Apply overlays the flags that were explicitly passed onto cfg and returns
// the result. Flags absent from the command line leave cfg untouched, which
// is what makes the defaults → file → flags precedence work.
func (f *Flags) Apply(cfg Config) Config {
	if f == nil || f.fs == nil {
		return cfg
	}
	set := map[string]bool{}
	f.fs.Visit(func(fl *flag.Flag) { set[fl.Name] = true })

	if set[FlagProfile] {
		cfg.Profile = Profile(f.profile)
	}
	if set[FlagHost] {
		cfg.Listen.Host = f.host
	}
	if set[FlagPort] {
		cfg.Listen.Port = f.port
	}
	if set[FlagAuthMode] {
		cfg.Auth.Mode = AuthMode(f.authMode)
	}
	if set[FlagToken] {
		cfg.Auth.Token = f.token
		// Supplying a token is an unambiguous request to use one; requiring
		// --auth=token alongside it would be a papercut with no upside.
		if !set[FlagAuthMode] {
			cfg.Auth.Mode = AuthToken
		}
	}
	if set[FlagTLSMode] {
		cfg.TLS.Mode = TLSMode(f.tlsMode)
	}
	if set[FlagCertFile] {
		cfg.TLS.CertFile = f.certFile
	}
	if set[FlagKeyFile] {
		cfg.TLS.KeyFile = f.keyFile
	}
	return cfg
}

// Resolve is the whole precedence chain in one call: defaults, then the file
// in dir, then any explicitly-passed flags, then validation.
//
// It does not write anything. Persisting belongs to the caller that knows
// whether a change was a one-off invocation or a durable setting.
func Resolve(dir string, f *Flags) (Config, error) {
	cfg, err := Load(dir)
	if err != nil {
		return Default(), err
	}
	cfg = f.Apply(cfg)
	if err := cfg.Validate(); err != nil {
		return Default(), fmt.Errorf("invalid configuration: %w", err)
	}
	return cfg, nil
}
