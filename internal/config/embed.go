package config

import _ "embed"

// exampleConfig is the canonical default polyclav.toml — polyclav.example.toml
// baked into the binary at build time via go:embed. ExampleConfig()
// returns these bytes; the daemon writes them to
// ~/.config/polyclav/polyclav.toml on first run when no config exists yet
// (see cmd/polyclav/main.go).
//
// The canonical copy lives next to this file in internal/config/. The
// repo-root polyclav.example.toml is a symlink pointing here so that docs
// and the "copy from repo" workflow continue to work — there is exactly
// one source of truth.
//
//go:embed polyclav.example.toml
var exampleConfig []byte

// ExampleConfig returns the embedded polyclav.example.toml bytes. The
// returned slice MUST NOT be modified by callers — it aliases the
// program's read-only data segment.
func ExampleConfig() []byte {
	return exampleConfig
}
