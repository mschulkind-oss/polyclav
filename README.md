# polyclav

A self-contained **live piano host** for Linux. Plug in a MIDI keyboard
and an audio interface, run `polyclav`, and you have a digital piano: keys
make piano sound, effects are in the chain, and (optionally) a Novation
Launchkey's knobs, pads, and screen drive the front panel. No DAW, no
recording — just playing. Devices can come and go; polyclav reconnects
automatically and idles at near-zero CPU when nothing is plugged in.

> **Status:** Linux-only (PipeWire). Developed and tested against a
> Novation Launchkey 61 MK4 + Behringer XR18 over OSC. Should work with
> any class-compliant MIDI keyboard and any PipeWire-supported audio
> interface; Launchkey-specific bits gracefully degrade if the device
> isn't present.

polyclav is implemented in Go with a thin Rust `audio-core` for the
real-time audio thread (PipeWire, oxisynth, sfizz). A `polyclav-components`
CLI is also included for encoding/uploading Launchkey MK4 Custom modes
over SysEx.

---

## What works today

- **Live audio out** via PipeWire. Any default sink works; an XR18 with
  a low-latency WirePlumber rule (see `docs/INSTALL.md`) gets ~8 ms
  round-trip at a 128-frame quantum.
- **Live MIDI in** over ALSA-seq (rtmidi). Notes, CCs, mod wheel, and
  pitch bend forwarded to the synth end-to-end.
- **Two synth backends**, picked by file extension:
  - `.sf2` / `.sf3` → oxisynth (pure Rust)
  - `.sfz`         → sfizz (C++ via thin Rust FFI)
- **Patches** — named soundfont presets defined in `[[patches]]` in the
  config. Top-row Launchkey pads select patches live (8 visible).
- **Per-patch gain matching** via `gain_db` so switching a soft EP for
  a loud grand doesn't blow your ears off.
- **Soundfont hot-swap** — switch patches without dropping audio.
- **DSP chain** in the audio thread, in order:
  `synth → patch_gain → input_comp → reverb → mastering_comp → limiter → master_volume → out`.
  Knobs 1/2/3 drive master volume, reverb, and the input compressor live.
- **XR18 OSC bindings** — faders and pads drive mixer faders and mute
  toggles over UDP. Bindings live in `[osc.xr18.bindings]`.
- **Launchkey MK4 DAW driver** — handshake, knob/pad/screen control,
  per-patch knob-value persistence. The `polyclav-components` CLI also
  encodes and uploads Custom modes over SysEx.

For the developer-facing rundown of every component, see `AGENTS.md`.

---

## Quick start

```sh
mise install                                     # Go + Rust toolchains
just build                                       # Rust audio-core + Go binary
mkdir -p ~/.config/polyclav
cp polyclav.example.toml ~/.config/polyclav/polyclav.toml
$EDITOR ~/.config/polyclav/polyclav.toml             # edit soundfont paths
overmind start -D                                # run as daemon via Procfile
```

`polyclav` ships no soundfonts (license + size). The example config points
at `~/.local/share/polyclav/soundfonts/...` — drop your files there or edit
the paths. `docs/INSTALL.md` lists free starter soundfonts and where to
get them.

If you don't have a Launchkey, the daemon still runs: the audio path and
any MIDI keyboard work; the Launchkey-specific code paths just stay idle.

---

## Documentation

| Path | What it covers |
|---|---|
| [`docs/INSTALL.md`](docs/INSTALL.md) | System-level install: build deps, soundfonts, hardware notes. Start here on a fresh machine. |
| [`docs/USER_GUIDE.md`](docs/USER_GUIDE.md) | End-user install / configure / play, including the full config schema. |
| [`docs/HARDWARE_TESTS.md`](docs/HARDWARE_TESTS.md) | Hardware verification checklist (Launchkey MK4 + XR18). |
| [`docs/ROADMAP.md`](docs/ROADMAP.md) | Forward-looking native-synth roadmap (Phases 2-4). |
| [`AGENTS.md`](AGENTS.md) | For AI agents and developers working on the code. Build/test workflow, current state, DSP API. |

---

## License

polyclav itself is licensed under the **Apache License, Version 2.0** —
see [`LICENSE`](LICENSE).

Transitively used libraries carry their own licenses:

- [`oxisynth`](https://github.com/PolyMeilex/OxiSynth) — LGPL-2.1
- [`sfizz`](https://github.com/sfztools/sfizz) — BSD-2-Clause
- [`pipewire-rs`](https://gitlab.freedesktop.org/pipewire/pipewire-rs) — MIT or Apache-2.0
- [`rtmidi`](https://github.com/thestk/rtmidi) (via cgo) — MIT-like (see upstream)

If you distribute polyclav (or a derivative), bundle the appropriate
notices. Apache 2.0 + LGPL-2.1 (oxisynth) means: dynamic linking is
fine, you must permit relinking against modified oxisynth, and you must
preserve oxisynth's copyright notices.

---

## Contributing

Patches welcome. The workflow, code layout, and current milestones are
documented in [`AGENTS.md`](AGENTS.md). Run `just check` before sending
anything — it gates on `cargo build --release`, `cargo clippy -D warnings`,
`go vet`, `cargo test`, `go test`, and `go build ./...`.

Bug reports: please include `polyclav --version`, the contents of your
`polyclav.toml` (redact paths if you like), and the relevant slice of
`/tmp/polyclav.log` (overmind tees there by default).
