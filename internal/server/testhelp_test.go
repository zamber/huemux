package server

import (
	"testing"

	"github.com/zamber/huemux/internal/config"
)

// withConfigDir points internal/config at a temp dir for the duration of a
// test, so persisting a config never touches the developer's real
// ~/.config/huemux.
func withConfigDir(t *testing.T, dir string) {
	t.Helper()
	config.SetDir(dir)
	t.Cleanup(func() { config.SetDir("") })
}
