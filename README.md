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
- **Four synth backends**, picked per patch:
  - `.sf2` / `.sf3` → oxisynth (pure Rust)
  - `.sfz`         → sfizz (C++ via thin Rust FFI)
  - `type = "native"` → built-in pure-Rust analog-style synth
  - `type = "lv2"` / `type = "clap"` → plugin hosting
- **Patches** — named presets defined in `[[patches]]` in the
  config. Top-row Launchkey pads select patches live (8 visible).
- **Per-patch gain matching** via `gain_db` so switching a soft EP for
  a loud grand doesn't blow your ears off.
- **Soundfont hot-swap** — switch patches without dropping audio.
- **DSP chain** in the audio thread, in order:
  `synth → patch_gain → input_comp → reverb → mastering_comp → limiter → master_volume → out`.
  Knobs 1/2/3 drive master volume, reverb, and the input compressor live.
- **Launchkey knob pages** — five pages (MAIN / OSC / FILTER / AMP /
  LFO/MOD) on Scene ▲/▼ put the whole native-synth voice on the 8
  encoders, with page indicators on the bottom pad row. Code-complete,
  pending hardware verification (`docs/HARDWARE_TESTS.md`).
- **Web dashboard** — an embedded, localhost-only web UI (`[web]` in the
  config, on by default, `127.0.0.1:8666`): a Next.js app (static
  export embedded in the binary — `go build` needs no Node) with patch
  switching, all live params, mastering, the full native-synth panel, a
  velocity-curve editor with a live note monitor, validated in-browser
  config editing, a generic MIDI device probe/reverse-engineering tool,
  and the audition transport, plus a REST + SSE API.
  The pre-Next single-file page remains at `/legacy`. See
  `docs/WEB_UI.md`.
- **Velocity curves** — global `[midi.velocity]` curve (soft / linear /
  hard / custom gamma, or 2–16 control points, + output clamp) with
  per-patch overrides, so every patch responds to your keybed the way
  you want — editable live from the browser. See
  `docs/VELOCITY_CURVES.md`.
- **Audition mode** — `polyclav --play <clip> [--loop] [--tempo N]` plays
  one of seven built-in diagnostic clips through the full audio path, no
  keyboard needed; the web dashboard has the same transport. See
  `docs/AUDITION.md`.
- **Native synth — a full Minimoog-style voice**: 3 oscillators
  (saw/square/pulse, octave, detune, level) + noise + pre-filter drive,
  resonant ladder filter with its own ADSR and keyboard tracking, a
  runtime amp ADSR, velocity→amp/cutoff routing, a global LFO
  (pitch/cutoff/amp), mod-wheel vibrato, pitch bend, glide, optional 2×
  oversampling, and up to **8-voice polyphony** with live-switchable
  voice modes. Every tweak persists per patch in `state.toml`. See
  `docs/NATIVE_SYNTH.md`.
- **Mixer OSC bindings** — faders and pads drive mixer faders and mute
  toggles over UDP. Bindings live in `[osc.mixer]` (preferred name;
  `[osc.xr18]` still works), with a configurable presence-check
  `heartbeat` for non-X-Air OSC targets.
- **Launchkey MK4 DAW driver** — handshake, knob/pad/screen control,
  per-patch knob-value persistence. The `polyclav-components` CLI also
  encodes and uploads Custom modes over SysEx.

For the developer-facing rundown of every component, see `AGENTS.md`.

---

## Install

**Linux only** (PipeWire). polyclav is a dynamically-linked binary that uses
your system's audio libraries — install those from your distro, then install
polyclav however you like.

### 1. System libraries

PipeWire, ALSA, and the LV2 host library (lilv):

```sh
# Debian / Ubuntu
sudo apt install pipewire libasound2 liblilv-0-0
# Fedora
sudo dnf install pipewire alsa-lib lilv
# Arch
sudo pacman -S pipewire alsa-lib lilv
```

**Optional — sfizz**, for `.sfz` sample libraries (Salamander Grand, etc.). It's
a runtime-loaded *optional* backend: without it, `.sfz` patches are silent but
SF2/SF3 soundfonts, the native synth, and LV2/CLAP plugins all work. sfizz isn't
always in a distro's default repos — check your package manager (e.g. the AUR on
Arch) or build it from source. Run `polyclav doctor` to see what's available.

### 2. polyclav

```sh
uvx polyclav            # run without installing
pipx install polyclav   # or install it persistently
```

Both fetch a prebuilt Linux wheel from PyPI; the `polyclav-components` Launchkey
SysEx CLI ships in the same wheel. For Go developers:

```sh
go install github.com/mschulkind-oss/polyclav/cmd/polyclav@latest
```

(or build from source — see [Build from source](#build-from-source) below.)

### 3. First run

```sh
polyclav doctor         # report backends + analyse your config
polyclav bootstrap      # download free starter soundfonts (~500 MB; prompts for licenses)
polyclav                # start playing
```

---

## Build from source

```sh
mise install                                     # Go + Rust toolchains
just build                                       # Rust audio-core + Go binary
just install                                     # or: install both binaries to ~/.local/bin
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
| [`docs/ROADMAP.md`](docs/ROADMAP.md) | Native-synth design record (Phases 2-4, now implemented) + open questions. |
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
