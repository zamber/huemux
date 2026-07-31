package appconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// FileName is the config file, alongside internal/config's config.json,
// settings.json and favorites.json.
const FileName = "app.json"

// Load reads FileName from dir and returns it merged over Default().
//
// A missing file is not an error — it is the overwhelmingly common case (no
// build before this package existed wrote one) and yields plain defaults. The
// file is also not created as a side effect of loading: a fresh install should
// not sprout a config file it never asked for, and an absent file is a useful
// signal that nothing has been customized.
//
// Fields absent from the file keep their default, so a file containing only
// {"profile":"lights"} is valid and means exactly what it looks like.
func Load(dir string) (Config, error) {
	cfg := Default()

	raw, err := os.ReadFile(filepath.Join(dir, FileName))
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("read %s: %w", FileName, err)
	}

	// Unmarshalling into the already-defaulted struct is what makes partial
	// files work: JSON only overwrites the keys it actually contains.
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Default(), fmt.Errorf("parse %s: %w", FileName, err)
	}
	return cfg, nil
}

// Save writes cfg to FileName in dir, atomically: write a temp file in the
// same directory, fsync it, then rename over the target. The rename is what
// makes a concurrent reader see either the old file or the new one and never
// a half-written one — worth doing here because this file becomes
// runtime-mutable from the settings UI, so writes can land at any moment.
//
// Mode 0600 rather than 0644 because the auth token lives here.
func Save(dir string, cfg Config) (err error) {
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("refusing to save invalid config: %w", err)
	}

	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	raw = append(raw, '\n')

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config dir %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, FileName+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	// Any failure from here on leaves a stray temp file behind unless we
	// clean up; the rename below makes this a no-op on the success path.
	defer func() {
		if err != nil {
			_ = os.Remove(tmpName)
		}
	}()

	if err = tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if _, err = tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err = os.Rename(tmpName, filepath.Join(dir, FileName)); err != nil {
		return fmt.Errorf("rename temp file into place: %w", err)
	}
	return nil
}
