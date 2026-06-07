### Agent Instructions — polyclav

`polyclav` is a self-contained Go + Rust project: a Linux live-piano host
that maps a MIDI keyboard through soundfont synthesis to a PipeWire sink.

### Conventions

- **mise** for toolchains (Go + Rust here).
- **just** for tasks — everything goes through this directory's `Justfile`.
- **TDD** — red, green, refactor.
- **Never `rm`** during a working session. `mv` to a local `trash/` dir
  (gitignored) so accidental destructions are recoverable.

### Committing

1. Run `just format` before committing
2. Use conventional commits: feat:, fix:, docs:, chore:, refactor:, test:
3. Commit straight to main
4. If the commit is rejected by the pre-commit hook, fix and retry — do
   not bypass the hook

### Polyclav-specific

- Forward-looking + user-facing docs only: `README.md`, `docs/INSTALL.md`,
  `docs/USER_GUIDE.md`, `docs/HARDWARE_TESTS.md`, `docs/ROADMAP.md`,
  `scripts/README.md`. The code is the source of truth.
- **`just check` is the universal gate** (Rust build, lint, then tests on
  both sides).
- The Rust `audio-core` is built first (cgo links its staticlib); never edit
  Go cgo bindings without rebuilding the Rust side.

### Tool routing

For code modifications in this subproject, use `mcp__cerebras-mcp__write` on
real source files (`.go`, `.rs`, `.h`). For trivial config glue
(`.gitignore`, `Justfile`, `mise.toml`, `go.mod`, `Cargo.toml`) use the
normal Write tool — these files are short, exact, and gain nothing from
generation.

### Where things live

- **API surface** (audio DSP knobs, patch registry): read `internal/audio`
  and `internal/patches` — the Go signatures are the spec.
- **Build, run, PipeWire/overmind, latency, mise pins**: see
  `docs/INSTALL.md`.
- **Configuring and playing**: see `docs/USER_GUIDE.md`.
