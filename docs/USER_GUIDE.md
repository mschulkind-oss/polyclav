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
  `synth → drive_pedal → chorus → tremolo → analog_delay → patch_gain → input_comp → reverb → mastering_comp → limiter → master_volume → out`.
  The four pedals (drive, chorus, tremolo, analog delay) default to
  bit-exact bypass; only the drive pedal currently has a Launchkey knob
  (MAIN knob 4) — the other three are implemented and controllable via
  the Rust/Go APIs but not yet exposed on a knob, REST field, or
  per-patch save (see [delay](./VISION.md#1b-analog-style-delay-pedal),
  [chorus](./VISION.md#1c-chorus-pedal), and
  [tremolo](./VISION.md#1d-tremolo-pedal)).
- Launchkey live control surface:
  - Top-row pads select patches; the lit pad tracks the current patch.
  - Five knob pages (MAIN / OSC / FILTER / AMP / LFO/MOD) switched with
    Scene ▲/▼; the bottom pad row shows the active page. MAIN keeps the
    classic layout: knob 1 = master volume, 2 = reverb wet/dry, 3 = input
    compressor, 4 = filter cutoff (native patches). The other pages cover
    the native synth's full voice. Code-complete, **pending hardware
    verification** — see "Launchkey knob pages" below.
  - The screen shows the current patch's `display` name, with value
    popups while you turn a knob.
  - The mastering compressor, brick-wall limiter, and per-patch gain are set
    from config and apply automatically (and can be adjusted live from the
    web dashboard).
- Web dashboard (`[web]`, off by default): a Next.js app at `/app/`
  (the root URL redirects there; the original single-file page remains at
  `/legacy`) with patch switching, volume / reverb / compressor / cutoff
  sliders, mastering, the native synth's full parameter set, a velocity
  curve editor with a live note monitor, a validated config editor, and
  the audition transport, from any browser on `127.0.0.1:8666` — plus a
  REST + SSE API. See "Web dashboard" below.
- Velocity curves: a global `[midi.velocity]` remap (soft / linear / hard /
  custom gamma, or 2–16 draggable control points, with an output clamp)
  and per-patch overrides — editable live from the browser. See
  "Velocity curves" below.
- Audition mode: `polyclav --play <clip>` plays built-in diagnostic clips
  through the full audio path with no keyboard connected. See "Audition
  mode" below.
- Native synth — the full Minimoog-style voice: 3 oscillators + noise +
  drive, two runtime ADSRs, LFO, velocity routing, keyboard tracking,
  glide, pitch bend + mod wheel, and up to 8-voice polyphony with
  switchable voice modes; every tweak persists per patch. See
  `docs/NATIVE_SYNTH.md`.
- Per-patch gain matching via `gain_db` on each `[[patches]]` entry — line
  up the perceived loudness of wildly different soundfonts (Salamander vs
  DX7 vs analog bass) so switching patches doesn't blow your ears off.
- Optional OSC mixer bindings: once explicitly configured, faders and pads
  on the keyboard can drive mixer faders and mute toggles over UDP. Bindings
  live in `[osc.mixer]` (preferred name; the legacy `[osc.xr18]` still works).
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
just install          # copy both binaries to ~/.local/bin (PREFIX=... to override)
just fetch-soundfont  # download a small free SF2 into ./soundfonts/
```

The examples in this guide invoke `polyclav` bare, which assumes
`just install` put it on your PATH (`~/.local/bin` by default). Straight
after `just build`, use `./bin/polyclav` instead — same flags.

For the full first-run sequence (writing the default config, downloading the
soundfonts it expects), see `docs/INSTALL.md`.

## Quick start — sound in sixty seconds, zero downloads

The native synth needs no soundfont files, the audition player needs no
keyboard, and the web flag needs no config edit — so the shortest path to
sound is one config block and one command:

```sh
mkdir -p ~/.config/polyclav
cat > ~/.config/polyclav/config.toml <<'EOF'
[[patches]]
name    = "moog"
display = "Moog"
type    = "native"
engine  = "minimoog"
EOF

polyclav --play arp --loop --web on
```

You should hear a looping arpeggio on a single-saw Moog voice, and
`http://127.0.0.1:8666/` now serves the dashboard. Everything below
builds on this: the clip keeps playing while you tweak, so every change
is audible the moment you make it.

(Prefer real pianos? That's the *other* first-run path: with **no**
config file present, `polyclav` seeds the full example config — pianos,
EPs, DX7s — and exits listing the soundfont files it needs;
`polyclav bootstrap` then downloads them. See `docs/INSTALL.md`. If
you've already written the one-block config above, move it aside first,
or merge the `[[patches]]` entries you want from
`polyclav.example.toml`. The tours below all work on the one-block
config.)

## Guided tours

Each tour is a five-minute path through one part of the system. They
assume the quick start above is running (clip looping, dashboard open).

### Tour 1 — sculpt a Moog bass, no keyboard attached

Switch the clip first: in the dashboard's **Audition** card pick
`bass-riff`, loop on — or restart as `polyclav --play bass-riff --loop
--web on`. Then, in the **Native synth** card:

1. Raise **Osc 2 level** to ~0.5 — it's pre-detuned −7 cents, so the
   riff instantly thickens.
2. Raise **Osc 3 level** to ~0.4 — it sits an octave down (+5 cents):
   there's your sub.
3. **Drive** to ~0.4 for grit into the ladder.
4. **Cutoff** down until the riff darkens, **resonance** up to ~0.6,
   then **Filter env amount** to ~0.5 — the filter now *plucks* each
   note open.
5. Tighten **F.Decay** (~0.3 s) and drop **F.Sustain** (~0.2) to sharpen
   that pluck; add a touch of **Glide** (~0.08 s) for the slides.

There is no save button: every slider move persists to this patch
automatically and comes back on the next boot — with one exception, the
**cutoff knob position**, which is deliberately session-only and resets
to ~632 Hz (see "Native synth: live parameters"). To A/B against where
you started, add a second native patch block to the config — each patch
keeps its own sound.

### Tour 2 — turn it into a polysynth

The native synth boots monophonic (faithful to the source material).
Chords are one setting away:

1. In **Native synth**, set **Voice mode** to `poly` (up to 8 voices).
2. Switch the clip to `sustain-chord` or `burst` and hear it stack.
3. Slow breathing pad: **LFO rate** ~0.5 Hz, **LFO→Cutoff** ~0.3.
4. Vibrato lives on **LFO→Pitch**, but it's scaled by the mod wheel —
   with no keyboard attached, wheel defaults to full, so the depth
   slider is audible as-is.
5. If you push **Drive** hard up here, flip on **Oversample** (2×) to
   keep the top end clean.

### Tour 3 — match the velocity curve to your keybed *(keyboard needed)*

1. Open the **Velocity** card and play normally for ten seconds — every
   note lands as an (in, out) dot on the curve, so you can *see* where
   your touch actually sits.
2. If you have to hammer to reach fortissimo, click **soft** (or drag
   gamma below 1). If it's shouty, **hard**.
3. Want full control? Switch to points mode and drag the curve — e.g.
   pull the middle down but pin `[127,127]` so ff stays available.
4. **Apply** installs it immediately for this session; **Save** writes
   it into `config.toml` (a tool-managed block), making it permanent
   across patch changes and restarts.
5. No keyboard handy? `polyclav --play vel-ramp --loop` sweeps velocity
   1→127→1 through whatever curve is active — you'll hear layer
   boundaries move as you drag.

Per-patch overrides (`velocity_curve` / `velocity_points` on a
`[[patches]]` entry) still win over anything saved globally — those stay
a config-file edit.

### Tour 4 — level-match your patch set

Switch between your patches with a familiar phrase and trim each entry's
`gain_db` until nothing jumps out — full workflow and typical values in
"Mastering & level matching" below. The **Config** card lets you edit
the TOML in the browser (validated on save; restart to apply).

### Tour 5 — the same tour on the Launchkey *(pending hardware verification)*

Everything in tours 1–2 maps to the five knob pages: **Scene ▼** to the
OSC page for the mixer knobs (osc levels/detunes, noise, pulse width),
FILTER for cutoff/resonance/env, AMP for the envelope and velocity
routing, LFO/MOD for the LFO, bend range, and voice mode (knob 7 steps
mono→retrig→poly). The bottom pad row shows which page you're on; the
screen pops each value as you turn. Layouts in "Launchkey knob pages"
below.

### Tour 6 — loop your own music

Drop any `.mid` file into `~/.local/share/polyclav/clips/` and restart —
it appears in the clip picker as `file:<name>` and works everywhere the
built-ins do:

```sh
polyclav --play file:mysong --loop --tempo 0.75
```

Practice-loop the intro of the piece you're learning through the exact
patch you'll perform it on, and tweak the patch while it plays.

## Configuration

The config lives at `~/.config/polyclav/config.toml`. On the first run with
no config present, polyclav writes the embedded default there and exits with a
list of the soundfont files it can't find; run `polyclav bootstrap` to fetch
them, then run `polyclav` again. `docs/INSTALL.md` walks through this. The
annotated reference for every field is `polyclav.example.toml` itself — this
section covers only the operational details that aren't obvious from those
inline comments.

The validation is strict: every patch's external dependency (soundfont file,
plugin bundle) must resolve at startup, or polyclav exits 1.

### `[midi]` — which keyboards send notes

`allow_devices` is an **allowlist**, and it starts **empty**. Until you name
a device, no keyboard sends notes and polyclav makes no sound. That is a
deliberate opt-in, not an oversight: a machine enumerates loopback buses,
control surfaces, and whatever else happens to be plugged in, and silently
binding to all of them is worse than asking you which one you meant.

You will not have to guess that this happened. With nothing selected,
startup prints a banner naming your connected ports and the exact line to
add:

```
════════════════════════════════════════════════════════════════════
  NO MIDI INPUT DEVICES ARE SELECTED — nothing will make sound.
════════════════════════════════════════════════════════════════════
```

To see port names any time:

```sh
polyclav midi list
```

prints every currently-connected port with its live classification: `ok`
sends notes, `off` is connected but not selected, `daw` is a control
surface, `loopback` is ALSA's Midi Through. (`aconnect -l` also works if
you just want a raw port list, no classification.)

Then list case-insensitive **substrings** of the names you want:

```toml
[midi]
allow_devices = ["Launchkey MK4 61 MIDI", "CASIO USB-MIDI"]
```

Substring matching (not exact) is deliberate: the full port name ALSA
reports ends in a volatile ` <client>:<port>` address (e.g. ` 36:0`) that
shifts on replug/reboot/device-order changes. A stable fragment like
`"CASIO USB-MIDI"` keeps matching across that shift; baking in the address
would silently stop working the next time it changes. Copy a short fragment,
not the whole string.

Once a device is selected it is selected for good — `internal/midi.Multiplexer`
opens and closes ports independently as they hotplug, so unplugging one
keyboard never affects another already playing, and a selected keyboard
starts working the moment it's plugged back in.

Three equivalent ways to change the list:

- Edit `config.toml` directly (above) — takes effect on restart.
- `polyclav --midi-allow "name one,name two"` — a one-off CLI override for
  this run only, replacing (not merging with) the config file's list.
- The web UI's **MIDI devices** panel (`[web]` must be enabled): a live
  checkbox per connected port, backed by `GET`/`PUT /api/midi/devices`.
  Ticking a box calls `Multiplexer.SetAllow` immediately (no restart);
  **Save** additionally writes `allow_devices` back into `config.toml`,
  in a clearly marked `# BEGIN/END polyclav-managed allow_devices` block —
  same explicit-save contract as the velocity curve editor. Entries naming
  hardware that isn't plugged in right now are listed too, so a Save can't
  silently drop them.

#### Control-surface and loopback ports

The Launchkey MK4 enumerates as **two** ports — `"Launchkey MK4 61 MIDI"`
(keys, wheels, pads) and `"Launchkey MK4 61 DAW"` (transport, knobs,
faders). ALSA also exposes a `"Midi Through"` loopback port. Neither the DAW
port nor the loopback port is a keyboard, so `polyclav midi list` and the
web panel flag them as `daw` / `loopback` — a hint, not a veto.

Selecting one anyway does exactly what you asked. `allow_devices =
["Launchkey MK4 61 DAW"]` binds note input to the DAW port's raw CC stream,
which is useful for OSC bindings that want the knobs/faders as CC sources
(the bindings below assume that layout).

**Launchkey knobs/pads/screen/transport are unaffected by `allow_devices`
either way** — `internal/launchkey.Reconciler` auto-detects a Launchkey on
its own fixed `"launchkey"` match, entirely independent of this config.

`allow_devices` is the *only* device-selection knob. An earlier
`port_match` filter was removed once the allowlist subsumed it; a
`port_match` line left in an old config is silently ignored.

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
8 knobs CC 21..28 ch16, 9 faders CC 5..13 ch16, fader buttons CC 37..45 ch1,
top pads notes 96..103 ch1, bottom pads notes 112..119 ch1.

See `polyclav.example.toml` for commented fader and pad binding examples; fresh installs leave them inactive until you uncomment and configure a host.

### `[[patches]]` — schema

Named presets surfaced on the Launchkey's top-row pads (8 max; extra entries
stay in the registry without a pad slot). On startup the last-used patch
(recorded in `state.toml`) is restored; with no saved state — or if that
patch no longer exists — the first entry is loaded.

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
| `velocity_points` | Per-patch control-point curve, e.g. `[[0,0], [64,40], [127,127]]` — 2..16 monotonic points; mutually exclusive with `velocity_curve`/`velocity_gamma`. Wins over everything. |

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

Only one daemon instance may run per user. If `just dev`, overmind, or a
manual `./bin/polyclav` is already holding the MIDI/audio devices, a second
daemon waits briefly for a graceful restart and then exits with
`another polyclav is already running`. Stop the existing supervisor
(`overmind quit`, Ctrl-C in `just dev`, or the terminal running the manual
binary) before starting another one.

## Playing

1. Connect your MIDI keyboard and audio interface.
2. `overmind start -D` from the repo root.
3. Play. The last-used patch (from `state.toml`, falling back to the
   first `[[patches]]` entry) is loaded and routed through the
   configured PipeWire sink.
4. Tap a top-row pad to switch patches live; the screen and the lit pad
   follow the selection. To make a permanent change, edit
   `~/.config/polyclav/config.toml` (by hand, or in the dashboard's
   Config card, which validates before writing) and
   `overmind restart polyclav` — the daemon reads config only at startup.

## Launchkey knob pages

> **Hardware-pending caveat:** the knob-page code is complete and
> unit-tested, but it shipped without a Launchkey on the bench — the
> on-device checklist in `docs/HARDWARE_TESTS.md` ("Knob pages") hasn't
> been run yet. Until it passes, treat the web dashboard as the
> reference control surface.

The 8 encoders drive the native synth through five pages. **Scene ▲**
goes to the previous page, **Scene ▼** to the next; the page name
flashes on the screen and the **bottom pad row lights the active page**
(pads 1–5 as indicators). The bottom row is split: columns 0–4 (notes
112–116) are reserved for these page indicators, while columns 5–7
(notes 117–119) stay free for your own `[[osc.mixer.bindings]]` — the
example config's mute pads live there. Turning a knob pops the
parameter name and value on the screen for 800 ms, then the patch name
returns.

| # | Page | Knob 1 | Knob 2 | Knob 3 | Knob 4 | Knob 5 | Knob 6 | Knob 7 | Knob 8 |
|---|------|--------|--------|--------|--------|--------|--------|--------|--------|
| 1 | **MAIN** | Volume | Reverb | Comp | Pedal | Resonance | Glide | Drive | — |
| 2 | **OSC** | Osc1 lvl | Osc1 detune | Osc2 lvl | Osc2 detune | Osc3 lvl | Osc3 detune | Noise | Pulse width |
| 3 | **FILTER** | Cutoff | Resonance | Env amount | F.Attack | F.Decay | F.Sustain | F.Release | Kbd track |
| 4 | **AMP** | A.Attack | A.Decay | A.Sustain | A.Release | Vel→Amp | Vel→Cutoff | Drive | — |
| 5 | **LFO/MOD** | LFO rate | LFO→Pitch | LFO→Cutoff | LFO→Amp | Bend range | Glide | Voice mode | — |

Notes:

- **MAIN knobs 1–4** are volume / reverb / comp / drive pedal — all
  four are backend-agnostic (see `docs/OPEN_SOUND_ENGINES.md`) and
  respond on every patch type, not just native ones. Knob 4 was cutoff
  through earlier releases; cutoff is still one page-flip away on
  **FILTER**'s knob 1.
- With a **non-native patch** selected, only MAIN's knobs 1–4 (the
  global volume / reverb / comp / pedal) respond; the rest of the synth
  pages apply to native patches only.
- **Voice mode** (LFO/MOD knob 7) steps mono_legato → mono_retrig →
  poly, one detent per step.
- **Play** on the transport row toggles the audition player's last-used
  clip. Every knob edit persists to the current patch automatically
  (debounced) — there is nothing to "save".

## Web dashboard

With `[web]` enabled (see Configuration above), the daemon serves a
dashboard at `http://127.0.0.1:8666/` — a laptop-first front panel with
the same live controls the Launchkey gives you, plus the ones no knob
reaches. The root URL redirects to the **Next.js app at `/app/`** (a
static export embedded in the binary — no Node.js needed at runtime);
the original single-file page is still available at `/legacy`. Both
offer the same cards:

- **Patches** — a pad-style grid; click to switch. The current patch is
  highlighted and colors follow each patch's `pad_color`.
- **Patch params** — volume, reverb, and compressor sliders, plus a
  cutoff slider that activates while a native patch is selected.
- **Native synth** — shown only while a native patch is current: the
  full voice — oscillators, pulse width, noise, drive, filter + both
  ADSRs, keyboard tracking, velocity routing, LFO, bend range, glide,
  voice mode (mono/poly), and the 2× oversampling toggle. See
  `docs/NATIVE_SYNTH.md`.
- **Velocity** — a curve editor (presets, custom gamma, or draggable
  control points on a canvas) with a **live note monitor**: play and
  watch each note appear as an (in, out) dot on the curve. Apply for
  the session, or Save to write the curve into `config.toml` (see
  "Velocity curves" below).
- **Mastering** — comp amount and limiter ceiling, live.
- **Audition** — clip picker, tempo slider, loop toggle, play/stop
  (see "Audition mode" below).
- **Config** — view and edit `config.toml` in the browser. Saving
  validates the whole file first (a config the daemon would refuse to
  boot from is never written) and shows a restart banner on success —
  config edits still apply at the next restart, not live.
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
| `GET /api/events` | SSE stream — a `snapshot` event on connect, then `params` / `synth` / `patch` / `mastering` / `velocity` / `player` / `device` / `note` change events (`note` carries each played note's raw + remapped velocity for the monitor, throttled to ~30/s). |
| `GET /api/patches` | The patch list. |
| `POST /api/patches/{name}/select` | Switch patch by name. |
| `PATCH /api/params` | Set `volume` / `reverb` / `compressor` / `drive_pedal` / `cutoff_pos` (each 0..1, all fields optional). |
| `PATCH /api/synth` | Set native-synth params — see `docs/NATIVE_SYNTH.md` for fields and ranges. |
| `PATCH /api/mastering` | Set `comp_amount` / `limiter_ceiling_db`. |
| `GET /api/config` | Your `config.toml`, verbatim. |
| `PUT /api/config` | Replace `config.toml` (full TOML text). Validated before write; 422 on a config the daemon would refuse. Restart to apply. |
| `GET /api/velocity` | The active velocity curve and whether it came from config or a session edit. |
| `PUT /api/velocity` | Apply a velocity curve live (`curve`/`gamma` or `points`); `"save": true` also persists it to a managed `[midi.velocity]` block in `config.toml`. |
| `GET /api/clips` | The audition clip library. |
| `POST /api/player` | Start a clip: `{"clip": "arp", "loop": true, "tempo": 1.0}`. |
| `POST /api/player/stop` | Stop playback. |
| `POST /api/player/tempo` | Change playback tempo live: `{"tempo": 1.5}`. |

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
| `--web <addr>` | Enable the web UI without editing the config: an address (`127.0.0.1:8666`, `:8666`) or `on` for the configured/default address. Overrides `[web]`. |

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

**User clips (`.mid` files).** Drop `.mid`/`.midi` files in
`~/.local/share/polyclav/clips/` and they join the clip list at the next
daemon start (IDs `file:<name>`, e.g. `polyclav --play file:mysong`).
Files are flattened to a single notes-only stream on channel 1; SMPTE-
timed files are rejected with a logged warning; unparseable files are
skipped without breaking the rest of the scan.

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
# points = [[0, 0], [64, 40], [127, 127]]   # OR a control-point curve
                               # (points and curve/gamma are mutually
                               # exclusive within one block)

[[patches]]
name           = "salamander"
# ...existing fields...
velocity_curve = "soft"        # per-patch override (or velocity_gamma = 0.7,
                               # or velocity_points = [[0,0], ..., [127,127]])
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

**Control points (v2):** instead of a gamma curve, either scope can
carry a piecewise-linear curve of 2–16 `[in, out]` control points —
`points` in `[midi.velocity]`, `velocity_points` on a patch. The first
point must be exactly `[0, 0]` (vel 0 stays NoteOff), the last input
must be `127`, inputs strictly increasing, outputs non-decreasing.
Within one scope, points and curve/gamma are mutually exclusive.

Details worth knowing:

- **Precedence, most specific first:** per-patch `velocity_points` >
  per-patch `velocity_curve`/`velocity_gamma` > global `points` >
  global `curve`/`gamma`. A per-patch override replaces the global
  curve entirely while that patch is selected. Setting
  `velocity_gamma` alone implies `velocity_curve = "custom"`.
- **Synth path only.** The curve applies to NoteOn events headed for the
  synth. OSC mixer bindings always see the **raw** velocity, so
  fader/pad bindings behave identically whatever curve is active.
- `out_min` (≥ 1) is a floor — a played note can never remap to a
  NoteOff; `out_max` caps the top (e.g. never trigger a hammer-noise
  layer).
- Bad settings (unknown curve name, `custom` without a positive gamma,
  non-monotonic points, `out_min > out_max`) are startup errors listing
  every offender at once.
- Tuning by ear: loop the ramp clip while you edit —
  `polyclav --play vel-ramp --loop`.

**Live tweaking from the browser:** the dashboard's Velocity card is
the fastest way to dial a curve in. Pick a preset or drag the gamma
slider, or switch to points mode and drag control points directly on
the canvas; **Apply** installs the curve for the session immediately
(no config edit, no restart). Play while you tweak — the **live
monitor** plots every note you strike as an (in, out) dot on the curve,
so you can see exactly where your keybed lands. When it feels right,
**Save** writes the curve into a clearly-marked, tool-managed
`[midi.velocity]` block in `config.toml` (a hand-written
`[midi.velocity]` section is never overwritten — saving refuses
instead). The difference matters on patch changes: an **Apply**-only
(session) curve is replaced the next time a patch change re-resolves
curves from config, while a **Saved** curve becomes the global default
immediately — it survives patch changes and restarts. Per-patch
overrides still win over either and remain a config-file edit.

**Timbre note for layered soundfonts:** remapping velocity changes
*which sample layers trigger* in multi-layer instruments, not just
loudness. A `soft` curve on Salamander means reaching the forte layers
with less force — timbre change included. That's the feature, not a bug.

## Native synth: live parameters

With a `type = "native"` patch selected, the whole voice is adjustable
live from the web dashboard, `PATCH /api/synth`, or the Launchkey knob
pages — no restart, no config edit:

- **Oscillators** — three of them: waveform (`saw` / `square` / `pulse`),
  octave (−2..+2), detune (±100 cents), and level each; a shared pulse
  width; plus a white noise source.
- **Filter** — cutoff, resonance, a dedicated filter ADSR with an
  env→cutoff amount, and keyboard tracking.
- **Amp** — a runtime ADSR, velocity→amp and velocity→cutoff routing
  amounts, and a pre-filter tanh drive.
- **LFO** — triangle / saw / square / sample-and-hold, 0.05–20 Hz, with
  depths into pitch (vibrato, scaled by the mod wheel), cutoff, and amp.
- **Performance** — glide (0–5 s portamento), pitch-bend range (0–12
  semitones), and the **voice mode**: `mono_legato` (the default),
  `mono_retrig`, or `poly` — switch to `poly` and **chords work** (up to
  8 voices; when all are sounding, the oldest is stolen).
- **Oversampling** — an optional 2× oversampled drive + filter path for
  cleaner high-drive sounds.

The defaults preserve the original single-saw Phase 1 sound (osc 2/3 and
noise at level 0, env amount 0, LFO depths 0, mono), so a native patch
sounds the same until you reach for the controls. Every tweak persists
to the patch automatically (only the cutoff knob position is
session-only) — see `docs/NATIVE_SYNTH.md` for the full parameter table
with defaults and ranges.

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
polyclav-components upload --slot <0..7> --type pots|pads|faders \
    [--port <name>] [--activate] <file.syx>
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

Once you have a `.syx` file, `upload` sends it to the keyboard directly
(`--port` matches the MIDI port name, default `"Launchkey"`; add
`--activate` to switch the device onto the freshly-uploaded mode):

```sh
polyclav-components upload --slot 0 --type pots --activate my-mode.syx
```

Any generic SysEx tool works too (e.g. `amidi -p <port> -s my-mode.syx`).

## XR18 mixer integration

`[[osc.mixer.bindings]]` entries (legacy spelling: `[[osc.xr18.bindings]]`)
tell `polyclav` to forward MIDI events from the keyboard out to the XR18
as OSC messages. Fresh installs do not include active bindings or a mixer host;
copy/uncomment examples only for a mixer you explicitly want to control. Examples:

- Move fader 9 on the Launchkey (CC 13 on channel 16) → `/lr/mix/fader`
  on the XR18 → main L/R fader moves.
- Tap a bottom-row pad → `/ch/01/mix/on` with value `1` → channel 1
  toggles (XR18 does not toggle itself — re-tap to send `1` again).

To verify the link is alive, move a bound fader on the keyboard and watch
the corresponding control on the XR18 (front panel, X-Air-Edit, or the
mixer's web UI). The XR18 must be reachable on the LAN at the configured
`host:port`.

### Launchkey organ drawbars

Launchkey DAW faders are not mapped to the mixer unless you configure OSC
bindings. For an organ patch, opt in to drawbars 1–9 without copying numeric
CLAP parameter IDs. A parameter ID is the plugin's numeric handle for a
control; its **name** is the human-readable label. By default, Polyclav
matches the nine exact footage names (16′ through 1′) on the active CLAP
plugin, regardless of its plugin ID. It checks that every name resolves to
one distinct numeric ID with a 0–8 range:

```toml
[[patches]]
name = "potato-keys-organ"
display = "Potato Keys"
type = "clap"
plugin_path = "~/.clap/Potato Keys.clap"
plugin_id = "com.littlepotato.keys"

[patches.launchkey_organ]
enabled = true
ownership = "organ"
```

If discovery is incomplete or ambiguous, the organ faders do nothing and
the Launchkey displays `CHECK LABELS`; they do not unexpectedly move mixer
volume. For a plugin with different labels, set this in that patch's
`[patches.launchkey_organ]` block:

```toml
drawbar_param_names = [
  "bar1", "bar2", "bar3", "bar4", "bar5",
  "bar6", "bar7", "bar8", "bar9",
]
```

Supply nine distinct, exact parameter names in fader order, each with a 0–8
range; a missing or ambiguous name captures the faders but changes neither
the organ nor the mixer. Old
`drawbar_clap_ids` numeric bindings remain supported but are no longer
required for other plugins. The optional Potato Keys integration test runs
without an audio device:
`POLYCLAV_KEYS_CLAP_PATH="$HOME/.clap/Potato Keys.clap" just test`.

When this capture is active, fader 9 changes the ninth drawbar only; it does
not also send the mixer L/R fader OSC binding.

### Launchkey organ drawbar buttons and coloring

The nine buttons directly below the drawbar sliders illuminate to reflect the
drawbars and Leslie state. By default, buttons 1–8 emulate the coloring of the drawbars
on a classic Hammond B3, using red for the black mutation drawbars so they stand out
with deep saturation against white on hardware button LEDs, while button 9 acts as the dedicated
Leslie status indicator:

- **16′ & 5⅓′ (buttons 1 & 2):** Brown (amber)
- **8′ & 4′ (buttons 3 & 4):** White (foundation and 2nd harmonic)
- **2⅔′ (button 5):** Red (3rd harmonic mutation)
- **2′ (button 6):** White (4th harmonic)
- **1⅗′ & 1⅓′ (buttons 7 & 8):** Red (5th and 6th harmonic mutations)
- **Leslie rotary (button 9):** Green when running (Slow or Fast), off when stopped

Pressing the **first button** (button 1, below the 16′ drawbar slider) cycles buttons 1–8 through
three additional coloring modes that illustrate how organists group and use
drawbars together, before returning to the B3 standard coloring on the fourth press:

1. **Registers (`LOW / MID / HIGH`):** Groups the drawbars into frequency zones:
   Low/Bass (red), Mid/Body (green), and High/Brilliance (cyan).
2. **Harmonics (`OCTAVE / MUTATION`):** Highlights the distinction between
   pure consonant octaves (16′, 8′, 4′, 2′, 1′ in white) and harmonic color mutations
   (5⅓′, 2⅔′, 1⅗′, 1⅓′ in orange).
3. **Performance (`FIRST 3 + WHISTLER`):** Highlights classic performance groupings:
   the legendary "first three" jazz/blues foundation (16′, 5⅓′, 8′ in green),
   the mid-body fill (dim white), and "the whistler" solo lead (1′ in yellow).
4. **B3 Standard (`B3 STANDARD`):** Cycles back to the classic console drawbar coloring.

In all four modes, button 9 remains dedicated to the Leslie rotary status.
The Launchkey screen displays the active coloring mode name for 800 ms on each cycle.

Pressing the **second button** (button 2, below the 5⅓′ drawbar slider) cycles buttons 1–8 through
five vibrant color schemes for the default B3 drawbars:

1. **Classic (`1: CLASSIC`):** Traditional console palette — brown subs (16′, 5⅓′), white foundations (8′, 4′, 2′), and red mutation drawbars (2⅔′, 1⅗′, 1⅓′).
2. **Neon (`2: NEON`):** Vibrant synthwave palette — glowing orange subs, electric cyan foundations, and hot pink mutations.
3. **Ocean (`3: OCEAN`):** High-contrast coastal palette — bright yellow subs, clean white foundations, and deep blue mutations.
4. **Triad (`4: TRIAD`):** High-saturation primary triad — sunny yellow subs, vivid cyan foundations, and deep red mutations.
5. **Candy (`5: CANDY`):** Vibrant pop palette — hot pink subs, clean white foundations, and electric cyan mutations.

Pressing button 2 while in another coloring mode (such as Registers or Harmonics) automatically switches back to the B3 drawbars with the next color scheme. The Launchkey screen displays `B3 SCHEME` and the scheme name for 800 ms on each press.
When leaving an organ patch, all nine fader button lights turn off.

### Organ swell and Leslie pedal

A **swell** is the organ's expression control: it changes the organ's own
expression parameter, not Polyclav's master volume. An explicitly named USB
pedal can control it only while an active CLAP patch opts in and exposes an
`Expression` parameter with a 0–100 range. Patches with
`launchkey_organ = { enabled = true, ownership = "organ" }` opt in by default.
Set `swell_pedal = false` in the organ's `[[patches]]` entry (before its
`[patches.launchkey_organ]` block) to disable it, or
`swell_pedal = true` in another CLAP instrument's entry to opt in. A piano
has no automatic swell binding. Notes and other messages from that
dedicated pedal port are not forwarded to instruments or the mixer.
For example:

```toml
[midi]
allow_devices = ["Launchkey MK4 61:Launchkey MK4 61 MIDI In", "Expression Pedal:Expression Pedal MIDI 1"]

[midi.organ_expression]
device = "Expression Pedal:Expression Pedal MIDI 1"
cc = 11
min = 0
max = 127
```

Use the stable part of the input port name, **not** its changing ALSA address
(such as `28:0`). Both the allowlist entry and `device` are required. `cc`
is the MIDI controller number the pedal actually sends; **11 is only the
standard default, not a measurement of your pedal**. Use `aconnect -l` to
find the source port and `aseqdump -p CLIENT:PORT` while sweeping the pedal
to confirm its controller number and endpoints without sending MIDI. `min`
is the observed heel value and `max` the toe value (defaults: 0 and 127).
Values outside the calibrated endpoints clamp to 0 or 100; a reversed
pedal can use `min > max`. An empty `device` disables the binding.

On an organ-owned CLAP patch, Launchkey sustain CC64 switches a compatible
`Rotary` parameter between Slow (2) and Fast (3), if the parameter has the
expected 0–3 range. **Toggle** (the default for organ-owned patches) means one press chooses
Fast and the next chooses Slow; releases do not change speed. **Momentary**
means Fast while held and Slow on release. To choose explicitly, add
`leslie_mode = "toggle"`, `"momentary"`, or `"off"` to the patch's
`[patches.launchkey_organ]` block; `off` leaves sustain CC64 untouched.
An organ without a compatible `Rotary` parameter keeps ordinary sustain;
if another plugin uses different values for its rotary modes, set
`leslie_mode = "off"` rather than assuming that 2 and 3 mean Slow and Fast.
Piano and other non-organ patches retain normal sustain behavior. The
switching happens in software, so a momentary physical footswitch suffices
for either mode. The ninth (rightmost) fader button toggles the rotary
between Stop (1) and Fast (3) on an active organ, independently of
`leslie_mode`, and flashes the rotary state (`LESLIE: FAST` or `LESLIE: STOP`)
on the screen. Its LED acts as a dedicated Leslie status indicator across all
drawbar modes, illuminating in green when the rotary is running (Slow or Fast)
and turning off when stopped. Changes from the pedal or plugin update the LED too.

## Troubleshooting

| Symptom | Fix |
|---------|-----|
| **No audio.** | Confirm your sink is visible (`pw-cli ls Node \| grep -i sink`) and that PipeWire is the running audio server. Note polyclav *refuses to boot* when a patch's soundfont is missing (it lists the files and exits 1) — so if the daemon is running, the problem is routing, not files. |
| **No MIDI.** | Run `polyclav midi list` (or `aconnect -l`) and confirm your keyboard is listed and classified `ok`. `off` means it isn't in `[midi].allow_devices` — that list is an allowlist and starts empty, so this is the usual answer; add a substring of the name, or tick the box in the web UI's MIDI devices panel. |
| **Knobs/faders do nothing.** | These are the Launchkey's DAW-port CCs, auto-detected independently of `[midi].allow_devices` — unaffected by that setting either way. If they're still silent, confirm a Launchkey is actually connected: the startup log's "launchkey connected" line, or the web UI's device status chip if `[web]` is enabled. |
| **Latency feels high.** | See `AGENTS.md` → "Latency tuning". For the XR18, the host-side WirePlumber rule pinning `period-size=128, period-num=3, headroom=0` is what gets you to ~8 ms round-trip. |
| **Build fails on the Rust side.** | Check the env-var pins in `mise.toml` (`LIBCLANG_PATH`, `CPLUS_INCLUDE_PATH`, `CGO_LDFLAGS`, `PKG_CONFIG_PATH`, `C_INCLUDE_PATH`). See `AGENTS.md` → "Toolchain quirks pinned in mise.toml". |
| **Daemon ignored my config change.** | Did you `overmind restart polyclav`? The daemon reads config only at startup. |
| **Chords play only one note on the native synth.** | The voice boots `mono_legato` (faithful Minimoog). Set **Voice mode** to `poly` — dashboard Native-synth card, `PATCH /api/synth {"voice_mode":"poly"}`, or LFO/MOD page knob 7. The setting persists per patch. |
| **A `.mid` file I dropped in doesn't show up.** | Clips are scanned only at startup — restart the daemon. Check the log: SMPTE-timed and unparseable files are skipped with a warning. IDs are `file:<name>` (extension stripped). |
| **XR18 not responding.** | Confirm the mixer is reachable (`nc -uvz <host> 10024`), and that `[osc.mixer].host`/`port` (or the legacy `[osc.xr18]` block) match. |
| **Web dashboard unreachable.** | Is `[web].enabled = true`? The default bind is loopback-only (`127.0.0.1:8666`) — from another machine you must opt in with `listen = "0.0.0.0:8666"` (no auth; trusted networks only). Check the log for a `web server` error if the port was taken. |
