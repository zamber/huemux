// Package config handles huemux's on-disk state: bridge credentials and
// per-entertainment-area tuning.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Bridge holds what Pair() produces: enough to reconnect to the bridge and
// re-open the DTLS stream without pairing again.
type Bridge struct {
	BridgeIP  string `json:"bridge_ip"`
	BridgeID  string `json:"bridge_id"`
	Username  string `json:"username"`
	ClientKey string `json:"clientkey"`
}

// Dir returns huemux's config directory, creating it if necessary.
func Dir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return "", fmt.Errorf("resolve config dir: %w", err)
		}
		base = filepath.Join(home, ".config")
	}
	dir := filepath.Join(base, "huemux")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create config dir %s: %w", dir, err)
	}
	return dir, nil
}

func bridgePath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// LoadBridge reads the persisted bridge credentials. A missing file is
// reported as a plain *os.PathError so callers can check os.IsNotExist.
func LoadBridge() (Bridge, error) {
	path, err := bridgePath()
	if err != nil {
		return Bridge{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Bridge{}, err
	}
	var b Bridge
	if err := json.Unmarshal(raw, &b); err != nil {
		return Bridge{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return b, nil
}

// SaveBridge writes bridge credentials with mode 0600 and fsyncs before
// returning, because the clientkey is issued exactly once at registration —
// there is no endpoint to read it back if this write is lost.
func SaveBridge(b Bridge) error {
	path, err := bridgePath()
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return fmt.Errorf("encode bridge config: %w", err)
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	if _, err := f.Write(raw); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return f.Sync()
}
