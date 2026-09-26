### Agent Instructions — polyclav

`polyclav` is a self-contained Go + Rust project: a Linux live-piano host
that maps a MIDI keyboard through soundfont synthesis to a PipeWire sink.

### Toolchain

- **mise** manages the Go and Rust toolchains.

### Polyclav-specific

- Forward-looking + user-facing docs only: `README.md`, `docs/INSTALL.md`,
  `docs/USER_GUIDE.md`, `docs/HARDWARE_TESTS.md`, `docs/ROADMAP.md`,
  `scripts/README.md`. The code is the source of truth.
- **`just check` is the universal gate** (Rust build, lint, then tests on
  both sides).
- The Rust `audio-core` is built first (cgo links its staticlib); never edit
  Go cgo bindings without rebuilding the Rust side.

### Where things live

- **API surface** (audio DSP knobs, patch registry): read `internal/audio`
  and `internal/patches` — the Go signatures are the spec.
- **Build, run, PipeWire/overmind, latency, mise pins**: see
  `docs/INSTALL.md`.
- **Configuring and playing**: see `docs/USER_GUIDE.md`.
