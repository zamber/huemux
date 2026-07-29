// Package lightsync holds the module-root embed of the web/ directory.
//
// It exists only because Go's //go:embed cannot reach outside the
// directory tree of the file that declares it: web/ sits at the repo root,
// so the directive has to live here rather than in internal/server, even
// though asset embedding is conceptually part of that package's job.
package lightsync

import "embed"

// WebFS is the entire web/ directory — HTML, CSS and plain JS, no build
// step — embedded into the binary.
//
//go:embed web
var WebFS embed.FS
