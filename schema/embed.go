// Package schema exposes the authoritative, embedded v0.1 JSON Schema bundle.
package schema

import "embed"

// V01 contains the complete Draft 2020-12 schema bundle.
//
//go:embed v0.1/*.json
var V01 embed.FS
