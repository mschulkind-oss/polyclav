# polyclav — User Guide

A first-run guide for musicians who want to install, configure, and play.
For internals, hacking notes, and the host-side environment, see `AGENTS.md`.
For the forward-looking feature plan, see `docs/ROADMAP.md`.

## What polyclav is

`polyclav` is a Linux daemon that turns a MIDI keyboard plus an audio interface
into a playable digital piano. MIDI in → synthesis (soundfont, native synth, or
LV2/CLAP plugin) → per-patch gain → user compressor + reverb → mastering
compressor + brick-wall limiter → master gain → audio out via PipeWire. It is
designed around a Novation Launchkey 61 MK4 and a Behringer XR18 mixer, but
works with any MIDI keyboard and any PipeWire-supported audio sink. There is no
DAW and no recording — keys in, sound out. An optional browser dashboard
(off by default, localhost-only) stands in for the Launchkey as a front
panel; see "Web dashboard" below.

## What works today

- Live audio out via PipeWire. Any default sink works; an XR18 with the
  host-side WirePlumber rule documented in `AGENTS.md` gets ~8 ms round-trip
  at a 128-frame quantum.
- Live MIDI in over ALSA-seq. Notes, CCs, pitch bend, and mod wheel from the
  Launchkey (or any MIDI keyboard) are forwarded into the synth.
- Synthesis backends, chosen per patch:
  - `.sf2` / `.sf3` soundfonts → oxisynth (pure Rust)
  - `.sfz` soundfonts → sfizz (C library via Rust FFI)
  - native pure-Rust synth (`type = "native"`)
  - LV2 and CLAP plugins (`type = "lv2"` / `type = "clap"`)
- DSP chain in the audio thread, in order:
  `synth → patch_gain → input_comp → reverb → mastering_comp → limiter → master_volume → out`.
- Launchkey live control surface:
  - Top-row pads select patches; the lit pad tracks the current patch.
  - Knobs: 1 = master volume, 2 = reverb wet/dry, 3 = input compressor amount.
    Knob 4 sweeps the filter cutoff while a native patch is selected.
  - The screen shows the current patch's `display` name.
  - The mastering compressor, brick-wall limiter, and per-patch gain are set
    from config and apply automatically (and can be adjusted live from the
    web dashboard).
- Web dashboard (`[web]`, off by default): patch switching, volume / reverb
  / compressor / cutoff sliders, mastering, the native synth's full
  parameter set, and the audition transport, from any browser on
  `127.0.0.1:8666` — plus a REST + SSE API. See "Web dashboard" below.
- Velocity curves: a global `[midi.velocity]` remap (soft / linear / hard /
  custom gamma, with an output clamp) and per-patch overrides. See
  "Velocity curves" below.
- Audition mode: `polyclav --play <clip>` plays built-in diagnostic clips
  through the full audio path with no keyboard connected. See "Audition
  mode" below.
- Native synth (Phase 2 voice): 3 oscillators + noise, runtime resonance,
  filter ADSR, and glide, all adjustable live from the web dashboard. See
  `docs/NATIVE_SYNTH.md`.
- Per-patch gain matching via `gain_db` on each `[[patches]]` entry — line
  up the perceived loudness of wildly different soundfonts (Salamander vs
  DX7 vs analog bass) so switching patches doesn't blow your ears off.
- XR18 OSC bindings: faders and pads on the keyboard drive mixer faders and
  mute toggles over UDP. Bindings live in `[osc.mixer]` (preferred name;
  the legacy `[osc.xr18]` still works).
- `polyclav-components` standalone CLI: encode and upload Launchkey MK4 Custom
  modes (Pots / Pads / Faders) over SysEx. Independent of the daemon.

## Prerequisites

- Linux with a running **PipeWire** server and **ALSA-seq** (for MIDI).
- A MIDI keyboard that ALSA enumerates (anything class-compliant).
- An audio interface (or onboard audio) recognized by PipeWire.
- At least one soundfont (`.sf2`, `.sf3`, or `.sfz`), or a configured plugin /
  native patch.
- **mise** for managing the Go and Rust toolchains in the dev environment.
- **overmind** for process supervision (optional — you can run `bin/polyclav`
  directly, but the rest of this guide uses overmind).

Low-latency tuning (the XR18 WirePlumber rule, host-side audio bridges, and
similar) is out of scope here — see the "Latency tuning" and
"Audio + MIDI bridging" sections of `AGENTS.md`.

## Install / build

From the repo root:

```sh
mise install                      # install pinned Go + Rust toolchains
eval "$(mise env)" && just build  # build audio-core (Rust) + polyclav (Go)
```

`just build` compiles the Rust `audio-core` staticlib (cgo links against it),
then builds two Go binaries into `./bin/`: `polyclav` (the daemon) and
`polyclav-components` (the Launchkey Custom-mode uploader). Other handy targets:

```sh
just check            # full gate: rust build + clippy + go vet + tests
just fetch-soundfont  # download a small free SF2 into ./soundfonts/
```

For the full first-run sequence (writing the default config, downloading the
soundfonts it expects), see `docs/INSTALL.md`.

## Configuration

The config lives at `~/.config/polyclav/polyclav.toml`. On the first run with
no config present, polyclav writes the embedded default there and exits with a
list of the soundfont files it can't find; run `polyclav bootstrap` to fetch
them, then run `polyclav` again. `docs/INSTALL.md` walks through this. The
annotated reference for every field is `polyclav.example.toml` itself — this
section covers only the operational details that aren't obvious from those
inline comments.

The validation is strict: every patch's external dependency (soundfont file,
plugin bundle) must resolve at startup, or polyclav exits 1.

### `[midi]` — picking the right port

`port_match` is a case-insensitive substring matched against ALSA-seq input
port names; `""` opens the first available input. To find your keyboard's
port name:

```sh
aconnect -l                # list ALSA-seq clients/ports
```

The Launchkey MK4 enumerates as **two** ports — `"Launchkey MK4 61 MIDI"`
(keys, wheels, pads) and `"Launchkey MK4 61 DAW"` (transport, knobs, faders).
The default `"launchkey"` match picks the first, which is the MIDI port. If you
want the knobs/faders/transport stream instead, match `"DAW"` explicitly — but
note the bindings below assume the DAW port's CC layout.

### `[web]` — the browser dashboard

Off by default. Enable it and (optionally) pick a listen address:

```toml
[web]
enabled = true
# listen = "127.0.0.1:8666"   # the default; loopback is the security boundary
```

There is **no auth** — the loopback default is the boundary. Binding
wider (e.g. `listen = "0.0.0.0:8666"`) exposes full control of the daemon
to your LAN; do that only on a network you trust. If the port is busy the
daemon logs an error and keeps running — the web UI is never
load-bearing. See "Web dashboard" below for what it does.

### `[osc.mixer]` / `[[osc.mixer.bindings]]` — semantics

`[osc.mixer]` is the preferred name for the OSC mixer block; the legacy
`[osc.xr18]` spelling still works (if both are present, `[osc.mixer]`
wins). Fields are identical either way.

`heartbeat` selects the OSC address polled to decide whether the mixer is
reachable. Leave it unset for the X-Air default (`"/xinfo"`); set it to
`""` to disable presence polling entirely, which turns sends into
fire-and-forget UDP — use that for generic OSC targets that don't answer
X-Air pings.

Each binding maps one MIDI control event to one OSC dispatch on the mixer.
Lookup is keyed by `(source_kind, channel, controller)`; **NoteOff is ignored**
(so pad releases don't double-fire).

| Field         | Values                                                            |
|---------------|-------------------------------------------------------------------|
| `source_kind` | `"cc"` or `"note"`                                                |
| `channel`     | 1..16 (MIDI channel)                                              |
| `controller`  | CC number, or note number for `source_kind="note"`               |
| `osc`         | OSC address, e.g. `/lr/mix/fader`                                |
| `transform`   | `"scalar"` (float32 0..1, for faders/knobs) or `"press"` (int32 `1` on NoteOn, for pad-press toggles) |

`"press"` sends `1` on note-on and nothing on note-off. The XR18 does not
toggle itself — to unmute, re-press and it receives `1` again.

The Launchkey 61 MK4 in DAW mode (from its Programmer's Reference) lays out:
8 knobs CC 21..28 ch16, 9 faders CC 5..13 ch16, fader buttons CC 37..45 ch16,
top pads notes 96..103 ch1, bottom pads notes 112..119 ch1.

See `polyclav.example.toml` for a full set of fader and pad bindings.

### `[[patches]]` — schema

Named presets surfaced on the Launchkey's top-row pads (8 max; extra entries
stay in the registry without a pad slot). The first entry is loaded on startup.

| Field         | Meaning                                                              |
|---------------|----------------------------------------------------------------------|
| `name`        | Internal id (logs, state, CLI/OSC hooks).                            |
| `display`     | Label shown on the Launchkey screen when selected.                  |
| `type`        | `"soundfont"` (default), `"native"`, `"lv2"`, or `"clap"`.          |
| `soundfont`   | For `soundfont` type: path; extension picks oxisynth vs sfizz.      |
| `engine`      | For `native` type: factory voice, e.g. `"minimoog"`.               |
| `plugin_uri`  | For `lv2` type: the plugin's LV2 URI.                               |
| `plugin_path` | For `clap` type: filesystem path to the `.clap` bundle.            |
| `plugin_id`   | For `clap` type: the plugin's CLAP ID string.                      |
| `pad_color`   | Components palette index 0..127 — the pad's lit color.             |
| `gain_db`     | Per-patch loudness trim in dB; default `0.0`, useful range roughly -24..+24. Applied as the first stage of the DSP chain on every patch select. See "Mastering & level matching" below. |
| `velocity_curve` | Optional per-patch velocity curve override — wins over the global `[midi.velocity]` block. `"linear"`, `"soft"`, `"hard"`, or `"custom"` (the latter needs `velocity_gamma`). See "Velocity curves" below. |
| `velocity_gamma` | Custom gamma (> 0); setting it alone implies `velocity_curve = "custom"`. |

```toml
# soundfont patch
[[patches]]
name      = "ydp-grand"
display   = "YDP Grand"
soundfont = "/path/to/YDP-GrandPiano.sf2"
pad_color = 3            # bright white
gain_db   = 0.0          # reference level; trim others to match

# native synth patch (knob 4 sweeps cutoff while this patch is current)
[[patches]]
name    = "moog-native"
display = "Moog (native)"
type    = "native"
engine  = "minimoog"

# CLAP plugin patch
[[patches]]
name        = "dexed"
display     = "Dexed (DX7)"
type        = "clap"
plugin_path = "/path/to/Dexed.clap"
plugin_id   = "com.digital-suburban.dexed"
```

The example config ships piano, Rhodes/Wurlitzer EPs, DX7 FM voices, analog
basses, and a native Moog voice out of the box — see `polyclav.example.toml`
for the LV2 fields and the full annotated set.

## Running

`polyclav` is supervised by overmind via the `Procfile` at the project root.
From the repo root:

```sh
overmind start -D            # daemonize (tmux session "polyclav")
overmind ps                  # status
overmind echo                # attach to log stream (tmux)
overmind restart polyclav    # after editing config
overmind quit                # graceful stop
```

The Procfile tees stdout/stderr to `/tmp/polyclav.log`, so you can grep
without attaching:

```sh
tail -f /tmp/polyclav.log
```

You can also run the binary directly: `./bin/polyclav`.

## Playing

1. Connect your MIDI keyboard and audio interface.
2. `overmind start -D` from the repo root.
3. Play. The first `[[patches]]` entry (or `[soundfont].path` if you have
   no patches block) is loaded and routed through the configured PipeWire
   sink.
4. Tap a top-row pad to switch patches live; the screen and the lit pad
   follow the selection. To make a permanent change, edit
   `~/.config/polyclav/polyclav.toml` and `overmind restart polyclav` — the
   daemon reads config only at startup.

## Web dashboard

With `[web]` enabled (see Configuration above), the daemon serves a
single-page dashboard at `http://127.0.0.1:8666/`. It is a laptop-first
interim front panel — the same live controls the Launchkey gives you,
plus the ones no knob reaches:

- **Patches** — a pad-style grid; click to switch. The current patch is
  highlighted and colors follow each patch's `pad_color`.
- **Patch params** — volume, reverb, and compressor sliders, plus a
  cutoff slider that activates while a native patch is selected.
- **Native synth** — shown only while a native patch is current:
  resonance, glide, noise, the filter ADSR + env amount, and per-osc
  wave / octave / detune / level. See `docs/NATIVE_SYNTH.md`.
- **Mastering** — comp amount and limiter ceiling, live.
- **Audition** — clip picker, tempo slider, loop toggle, play/stop
  (see "Audition mode" below).
- A header strip shows connection health, Launchkey/XR18 device states,
  the current patch, and the daemon version.

Everything updates live in both directions: turn a Launchkey knob and the
slider moves; drag the slider and the sound changes. Web tweaks flow
through the same controls layer as the hardware, so per-patch knob values
persist to `state.toml` identically.

The page is a thin client over a JSON API you can also drive with curl:

| Endpoint | What it does |
|---|---|
| `GET /api/status` | Full snapshot: version, device states, params, patches, player. |
| `GET /api/events` | SSE stream — a `snapshot` event on connect, then `params` / `synth` / `patch` / `mastering` / `velocity` / `player` change events. |
| `GET /api/patches` | The patch list. |
| `POST /api/patches/{name}/select` | Switch patch by name. |
| `PATCH /api/params` | Set `volume` / `reverb` / `compressor` / `cutoff_pos` (each 0..1, all fields optional). |
| `PATCH /api/synth` | Set native-synth params — see `docs/NATIVE_SYNTH.md` for fields and ranges. |
| `PATCH /api/mastering` | Set `comp_amount` / `limiter_ceiling_db`. |
| `GET /api/config` | Your `polyclav.toml`, verbatim (read-only). |
| `GET /api/clips` | The audition clip library. |
| `POST /api/player` | Start a clip: `{"clip": "arp", "loop": true, "tempo": 1.0}`. |
| `POST /api/player/stop` | Stop playback. |
| `POST /api/player/tempo` | Change playback tempo live: `{"tempo": 1.5}`. |

The dashboard cannot edit the config file — `GET /api/config` is
read-only, and browser-side config editing is a later phase
(`docs/WEB_UI.md`).

## Audition mode — hear settings without a keyboard

`polyclav --play <clip>` starts the daemon and immediately plays a
built-in diagnostic clip through the full audio path — the exact route
keyboard notes take, minus the keyboard:

```sh
polyclav --play vel-ramp --loop              # loop until shutdown
polyclav --play bass-riff --loop --tempo 0.5 # ... at half speed
```

| Flag | Meaning |
|---|---|
| `--play <id>` | Clip to play at startup. An unknown id exits 1 and prints the clip library. |
| `--loop` | Repeat the clip until shutdown (otherwise it plays once and the daemon keeps running). |
| `--tempo N` | Tempo **multiplier** (not BPM), clamped to 0.25..2.0; `0` means 1.0. |

Seven clips ship built in, each purpose-built to expose one setting:

| id | What it plays | Built to demo |
|---|---|---|
| `vel-ramp` | Middle C, velocity stepping 1→127→1 | Velocity curves — hear each layer boundary move |
| `sustain-chord` | Cmaj9 held 8 beats, then 8 beats of silence | Reverb tail, mastering comp, limiter ceiling |
| `arp` | One-bar Am7 arpeggio in 16ths | Patch character, envelope feel, patch A/B |
| `bass-riff` | Two-bar low-register riff, mono-friendly | Native synth — sweep the cutoff over it |
| `chromatic` | Every note 21–108 at fixed velocity | Sample-layer seams, register balance, aliasing |
| `staccato` | Short notes with shrinking gaps | Attack/release transients, compressor pumping |
| `burst` | Dense five-note chords every beat | Polyphony stress, CPU headroom, limiter |

`sustain-chord` and `burst` are chordal: on the monophonic native synth
they collapse to a single line, so clip pickers label them "(poly
patches)".

The workflow this exists for is **tweak while listening**: enable
`[web]`, run e.g. `polyclav --play bass-riff --loop`, open the dashboard,
and drag cutoff / resonance / filter-env sliders while the riff loops.
Every change is audible in place — no keyboard, no restart. The
dashboard's Audition section (or `POST /api/player`) switches clips,
loops, and changes tempo at runtime. Clip notes drive the synth only;
they never fire the OSC mixer bindings.

## Velocity curves

polyclav can reshape incoming note velocity before it reaches the synth,
so the *feel* of a patch matches your keybed. The global default lives in
`[midi.velocity]`; any patch can override it:

```toml
[midi.velocity]                # global default for all patches
curve = "linear"               # "soft" | "linear" | "hard" | "custom"
# gamma = 0.8                  # required iff curve = "custom"
# out_min = 1                  # optional output clamps, defaults 1 / 127
# out_max = 127

[[patches]]
name           = "salamander"
# ...existing fields...
velocity_curve = "soft"        # per-patch override (or velocity_gamma = 0.7)
```

The curve is a gamma (power) remap with an output clamp:
`out(v) = clamp(round(127·(v/127)^γ), out_min, out_max)`, with velocity 0
passed through untouched (NoteOn vel 0 is NoteOff on the wire).

| Preset | γ | Feel |
|---|---|---|
| `soft` | 0.6 | Lifts the middle — heavy keybeds / quiet patches reach loud layers with less force. |
| `linear` | 1.0 | Identity (the default). |
| `hard` | 1.6 | Suppresses the middle — light keybeds / shouty patches get more headroom. |
| `custom` | your `gamma` | Anything in between (or beyond). |

Details worth knowing:

- **Per-patch wins.** `velocity_curve` / `velocity_gamma` on a patch
  replaces the global curve entirely while that patch is selected.
  Setting `velocity_gamma` alone implies `velocity_curve = "custom"`.
- **Synth path only.** The curve applies to NoteOn events headed for the
  synth. OSC mixer bindings always see the **raw** velocity, so
  fader/pad bindings behave identically whatever curve is active.
- `out_min` (≥ 1) is a floor — a played note can never remap to a
  NoteOff; `out_max` caps the top (e.g. never trigger a hammer-noise
  layer).
- Bad settings (unknown curve name, `custom` without a positive gamma,
  `out_min > out_max`) are startup errors listing every offender at once.
- Tuning by ear: loop the ramp clip while you edit —
  `polyclav --play vel-ramp --loop`.

**Timbre note for layered soundfonts:** remapping velocity changes
*which sample layers trigger* in multi-layer instruments, not just
loudness. A `soft` curve on Salamander means reaching the forte layers
with less force — timbre change included. That's the feature, not a bug.

## Native synth: live parameters

With a `type = "native"` patch selected, the full Phase 2 voice is
adjustable live from the web dashboard (or `PATCH /api/synth`) — no
restart, no config edit:

- **Filter** — cutoff (also on Launchkey knob 4), resonance, and a
  dedicated filter ADSR with an env→cutoff amount.
- **Oscillators** — three of them: waveform (`saw` / `square` / `pulse`),
  octave (−2..+2), detune (±100 cents), and level each; plus a white
  noise source.
- **Glide** — portamento time, 0–5 s (0 = off, the default).

The defaults preserve the original single-saw Phase 1 sound (osc 2/3 and
noise at level 0, env amount 0), so a native patch sounds the same until
you reach for the sliders. The amp envelope is still fixed, the voice is
still monophonic, and tweaks aren't yet persisted per patch — see
`docs/NATIVE_SYNTH.md` for the full parameter table and limits.

## Mastering & level matching

Different soundfonts are mastered at wildly different levels. A
well-sampled grand like Salamander can be 10–15 dB louder than a vintage
Wurlitzer SFZ, which in turn is louder than a typical DX7 SF2. Flipping
between patches without compensation is unpleasant at best and
speaker-shredding at worst. `polyclav` gives you two knobs to fix this: a
per-patch `gain_db` trim, and a fixed mastering chain at the tail of the
DSP path.

### Per-patch gain (`gain_db`)

Each `[[patches]]` entry takes an optional `gain_db` (default `0.0`,
useful range roughly `-24` to `+24`). On every patch select, `polyclav`
converts it to a linear factor (`10^(gain_db/20)`) and pushes it to the
audio thread, where it is applied as the **very first stage** of the
signal chain (before the user compressor on knob 3 ever sees the signal,
so the compressor behaves the same regardless of which patch is loaded).

Workflow for tuning a new patch — ears are the meter:

1. Pick a "reference" patch (e.g. your main grand) and leave it at
   `gain_db = 0.0`.
2. Play a familiar phrase — a medium-volume chord progression works well.
3. Switch to the new patch and play the same phrase.
4. Adjust the new patch's `gain_db` until perceived loudness matches.
   Halve the value if you're not sure — small changes (1–3 dB) are
   surprisingly audible.
5. Re-select the patch (or `overmind restart polyclav` to reload the config).

A typical real-world set ends up with the grand at `0`, a bright EP at
`-6`, a DX7 at `+3`, and an analog bass at `-9`.

### Mastering chain (`[mastering]`)

After the user-controllable compressor and reverb, every patch goes
through a transparent mastering compressor and a brick-wall limiter
before the master volume knob. The defaults are sensible — most users
will never touch this block — but it's there if you want to ride the
overall dynamic feel.

```toml
[mastering]
comp_amount        = 0.5     # 0..1, transparent leveling compressor (4:1
                              # soft-knee, 10 ms attack, 100 ms release,
                              # auto-makeup). 0 = bypass, 1 = max leveling.
limiter_ceiling_db = -0.3    # -12..0 dBFS brick-wall limiter ceiling.
```

The limiter is **always on** as a peak-safety net, even when
`comp_amount = 0`. It is lookahead-free (zero added latency), with a
tanh soft-knee, instant attack and a ~5 ms release. Set
`limiter_ceiling_db` to roughly `-0.3` for a normal listening ceiling, or
push it down (e.g. `-3.0`) if you want extra headroom into a downstream
mixer.

## Programming your Launchkey (Custom modes)

`polyclav-components` is an independent CLI that uploads a Custom mode — Pots,
Pads, Faders, Pedal, or Modwheel — to a Launchkey MK4 over SysEx. Custom
modes live on the keyboard's firmware: once uploaded, they persist across
power cycles and are available even when `polyclav` is not running.

Subcommands:

```sh
polyclav-components encode <toml-path> [--out FILE] [--product VARIANT]
polyclav-components decode <hex-bytes> [--file PATH]
polyclav-components help
```

`--product` defaults to `launchkey61_mk4`. Other supported variants:
`launchkey25_mk4`, `launchkey37_mk4`, `launchkey49_mk4`.

Minimal example — encode a Pots mode from a TOML definition and print the
SysEx bytes as hex:

```sh
go run ./cmd/polyclav-components encode \
    cmd/polyclav-components/testdata/example.toml
```

Write the bytes to a file instead:

```sh
polyclav-components encode my-mode.toml --out my-mode.syx
```

See `cmd/polyclav-components/testdata/example.toml` for the full TOML schema
(surface, slot, name, palette colors, control kinds, behaviours).

Once you have a `.syx` file, send it to the keyboard with any SysEx tool
(e.g. `amidi -p <port> -s my-mode.syx`).

## XR18 mixer integration

`[[osc.mixer.bindings]]` entries (legacy spelling: `[[osc.xr18.bindings]]`)
tell `polyclav` to forward MIDI events from the keyboard out to the XR18
as OSC messages. Examples:

- Move fader 9 on the Launchkey (CC 13 on channel 16) → `/lr/mix/fader`
  on the XR18 → main L/R fader moves.
- Tap a bottom-row pad → `/ch/01/mix/on` with value `1` → channel 1
  toggles (XR18 does not toggle itself — re-tap to send `1` again).

To verify the link is alive, move a bound fader on the keyboard and watch
the corresponding control on the XR18 (front panel, X-Air-Edit, or the
mixer's web UI). The XR18 must be reachable on the LAN at the configured
`host:port`.

## Troubleshooting

| Symptom | Fix |
|---------|-----|
| **No audio.** | Confirm your sink is visible (`pw-cli ls Node | grep -i sink`) and that PipeWire is the running audio server. A missing `[soundfont]` file falls back to a 440 Hz sine, not silence. |
| **No MIDI.** | Run `aconnect -l` and confirm your keyboard is listed. Make sure `[midi].port_match` is a substring of the port name (case-insensitive); set it to `""` to grab the first available input. |
| **Knobs/faders do nothing.** | The Launchkey exposes keys/pads on its MIDI port and knobs/faders on its DAW port. If you matched the MIDI port, the DAW-port CCs never arrive — see `[midi]` above. |
| **Latency feels high.** | See `AGENTS.md` → "Latency tuning". For the XR18, the host-side WirePlumber rule pinning `period-size=128, period-num=3, headroom=0` is what gets you to ~8 ms round-trip. |
| **Build fails on the Rust side.** | Check the env-var pins in `mise.toml` (`LIBCLANG_PATH`, `CPLUS_INCLUDE_PATH`, `CGO_LDFLAGS`, `PKG_CONFIG_PATH`, `C_INCLUDE_PATH`). See `AGENTS.md` → "Toolchain quirks pinned in mise.toml". |
| **Daemon ignored my config change.** | Did you `overmind restart polyclav`? The daemon reads config only at startup. |
| **XR18 not responding.** | Confirm the mixer is reachable (`nc -uvz <host> 10024`), and that `[osc.mixer].host`/`port` (or the legacy `[osc.xr18]` block) match. |
| **Web dashboard unreachable.** | Is `[web].enabled = true`? The default bind is loopback-only (`127.0.0.1:8666`) — from another machine you must opt in with `listen = "0.0.0.0:8666"` (no auth; trusted networks only). Check the log for a `web server` error if the port was taken. |
