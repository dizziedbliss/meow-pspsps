// Package web holds the judge's HTML templates and static files, embedded in the binary.
package web

import "embed"

// FS holds templates/*.html and static/*.
//
//go:embed templates static
var FS embed.FS
