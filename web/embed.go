// Package web embeds the compiled single-page application.
package web

import "embed"

// Dist holds the Vite build output. It is empty until `make web` runs.
//
//go:embed all:dist
var Dist embed.FS
