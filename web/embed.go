// Package web embeds the built single-page application.
package web

import "embed"

// Dist holds the built UI assets. The dist/ directory is produced by
// `make ui` (esbuild + tsc). A committed placeholder keeps the embed valid
// before the first build.
//
//go:embed all:dist
var Dist embed.FS
