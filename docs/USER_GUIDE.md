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
cat > ~/.config/polyclav/polyclav.toml <<'EOF'
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
   it into `polyclav.toml` (a tool-managed block), making it permanent
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

The config lives at `~/.config/polyclav/polyclav.toml`. On the first run with
no config present, polyclav writes the embedded default there and exits with a
list of the soundfont files it can't find; run `polyclav bootstrap` to fetch
them, then run `polyclav` again. `docs/INSTALL.md` walks through this. The
annotated reference for every field is `polyclav.example.toml` itself — this
section covers only the operational details that aren't obvious from those
inline comments.

The validation is strict: every patch's external dependency (soundfont file,
plugin bundle) must resolve at startup, or polyclav exits 1.

### `[midi]` — which keyboards send notes

`port_match` is OPTIONAL and defaults to `""`: every connected MIDI keyboard
sends notes, with no config needed. `internal/midi.Multiplexer` opens every
currently-present input port and closes/reopens them independently as
devices hotplug — unplugging one keyboard never affects another already
playing.

A Launchkey's DAW control-surface port (see below) is excluded automatically
in this default, name-agnostic mode — it isn't a source of notes. Everything
else, including a Launchkey's own MIDI port, is treated the same as any
other keyboard.

Set a case-insensitive substring here to restrict note input to matching
port(s) instead — e.g. only listen to one keyboard even with others plugged
in. To find exact port names, no guessing required:

```sh
polyclav midi list
```

prints every currently-connected port with its live classification (`ok`
sends notes, `daw` is a Launchkey control surface, `ignored`/`restricted`
per your config) — `aconnect -l` also works (ALSA-specific, no
classification) if you just want a raw port list.

#### `ignore_devices` — excluding specific keyboards

`ignore_devices` is a **denylist**, not an allowlist: list exact port names
(case-insensitive, copy them from `polyclav midi list`) to exclude from note
input on top of `port_match`/the DAW exclusion. A device plugged in later
that ISN'T in this list just works — you never need to add anything to make
a new keyboard send notes, only to silence one you already have.

```toml
[midi]
ignore_devices = ["Some Other Keyboard"]
```

Three equivalent ways to change it:

- Edit `polyclav.toml` directly (above) — takes effect on restart.
- `polyclav --midi-ignore "name one,name two"` — a one-off CLI override for
  this run only, replacing (not merging with) the config file's list.
- The web UI's **MIDI devices** panel (`[web]` must be enabled): a live
  checkbox per connected port, backed by `GET`/`PUT /api/midi/devices`.
  Unchecking a box calls `Multiplexer.SetIgnore` immediately (no restart);
  **Save** additionally writes `ignore_devices` back into `polyclav.toml`,
  in a clearly marked `# BEGIN/END polyclav-managed ignore_devices` block —
  same explicit-save contract as the velocity curve editor. DAW-role and
  `port_match`-restricted ports show in the list but aren't checkable
  (toggling either wouldn't change anything).

The Launchkey MK4 enumerates as **two** ports — `"Launchkey MK4 61 MIDI"`
(keys, wheels, pads) and `"Launchkey MK4 61 DAW"` (transport, knobs, faders).
An explicit `port_match` bypasses the DAW-port exclusion (it's an intentional
restriction, not the generic default), so `port_match = "DAW"` binds note
input to the DAW port's raw CC stream instead — useful for OSC bindings that
want the knobs/faders as CC sources (the bindings below assume that layout).

**Launchkey knobs/pads/screen/transport are unaffected by `port_match`
either way** — `internal/launchkey.Reconciler` auto-detects a Launchkey on
its own fixed `"launchkey"` match, entirely independent of this config.

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

## Playing

1. Connect your MIDI keyboard and audio interface.
2. `overmind start -D` from the repo root.
3. Play. The last-used patch (from `state.toml`, falling back to the
   first `[[patches]]` entry) is loaded and routed through the
   configured PipeWire sink.
4. Tap a top-row pad to switch patches live; the screen and the lit pad
   follow the selection. To make a permanent change, edit
   `~/.config/polyclav/polyclav.toml` (by hand, or in the dashboard's
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
| 1 | **MAIN** | Volume | Reverb | Comp | Cutoff | Resonance | Glide | Drive | — |
| 2 | **OSC** | Osc1 lvl | Osc1 detune | Osc2 lvl | Osc2 detune | Osc3 lvl | Osc3 detune | Noise | Pulse width |
| 3 | **FILTER** | Cutoff | Resonance | Env amount | F.Attack | F.Decay | F.Sustain | F.Release | Kbd track |
| 4 | **AMP** | A.Attack | A.Decay | A.Sustain | A.Release | Vel→Amp | Vel→Cutoff | Drive | — |
| 5 | **LFO/MOD** | LFO rate | LFO→Pitch | LFO→Cutoff | LFO→Amp | Bend range | Glide | Voice mode | — |

Notes:

- **MAIN preserves muscle memory** — knobs 1–4 are exactly the historic
  volume / reverb / comp / cutoff layout.
- With a **non-native patch** selected, only MAIN's knobs 1–3 (the
  global volume / reverb / comp) respond; the synth pages apply to
  native patches only.
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
  the session, or Save to write the curve into `polyclav.toml` (see
  "Velocity curves" below).
- **Mastering** — comp amount and limiter ceiling, live.
- **Audition** — clip picker, tempo slider, loop toggle, play/stop
  (see "Audition mode" below).
- **Config** — view and edit `polyclav.toml` in the browser. Saving
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
| `PATCH /api/params` | Set `volume` / `reverb` / `compressor` / `cutoff_pos` (each 0..1, all fields optional). |
| `PATCH /api/synth` | Set native-synth params — see `docs/NATIVE_SYNTH.md` for fields and ranges. |
| `PATCH /api/mastering` | Set `comp_amount` / `limiter_ceiling_db`. |
| `GET /api/config` | Your `polyclav.toml`, verbatim. |
| `PUT /api/config` | Replace `polyclav.toml` (full TOML text). Validated before write; 422 on a config the daemon would refuse. Restart to apply. |
| `GET /api/velocity` | The active velocity curve and whether it came from config or a session edit. |
| `PUT /api/velocity` | Apply a velocity curve live (`curve`/`gamma` or `points`); `"save": true` also persists it to a managed `[midi.velocity]` block in `polyclav.toml`. |
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
`[midi.velocity]` block in `polyclav.toml` (a hand-written
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
| **No audio.** | Confirm your sink is visible (`pw-cli ls Node \| grep -i sink`) and that PipeWire is the running audio server. Note polyclav *refuses to boot* when a patch's soundfont is missing (it lists the files and exits 1) — so if the daemon is running, the problem is routing, not files. |
| **No MIDI.** | Run `polyclav midi list` (or `aconnect -l`) and confirm your keyboard is listed and classified `ok`. `restricted` means `[midi].port_match` doesn't match its name (clear it or fix the substring); `ignored` means it's in `[midi].ignore_devices` or was unchecked in the web UI's MIDI devices panel. |
| **Knobs/faders do nothing.** | These are the Launchkey's DAW-port CCs, auto-detected independently of `[midi].port_match` — unaffected by that setting either way. If they're still silent, confirm a Launchkey is actually connected: the startup log's "launchkey connected" line, or the web UI's device status chip if `[web]` is enabled. |
| **Latency feels high.** | See `AGENTS.md` → "Latency tuning". For the XR18, the host-side WirePlumber rule pinning `period-size=128, period-num=3, headroom=0` is what gets you to ~8 ms round-trip. |
| **Build fails on the Rust side.** | Check the env-var pins in `mise.toml` (`LIBCLANG_PATH`, `CPLUS_INCLUDE_PATH`, `CGO_LDFLAGS`, `PKG_CONFIG_PATH`, `C_INCLUDE_PATH`). See `AGENTS.md` → "Toolchain quirks pinned in mise.toml". |
| **Daemon ignored my config change.** | Did you `overmind restart polyclav`? The daemon reads config only at startup. |
| **Chords play only one note on the native synth.** | The voice boots `mono_legato` (faithful Minimoog). Set **Voice mode** to `poly` — dashboard Native-synth card, `PATCH /api/synth {"voice_mode":"poly"}`, or LFO/MOD page knob 7. The setting persists per patch. |
| **A `.mid` file I dropped in doesn't show up.** | Clips are scanned only at startup — restart the daemon. Check the log: SMPTE-timed and unparseable files are skipped with a warning. IDs are `file:<name>` (extension stripped). |
| **XR18 not responding.** | Confirm the mixer is reachable (`nc -uvz <host> 10024`), and that `[osc.mixer].host`/`port` (or the legacy `[osc.xr18]` block) match. |
| **Web dashboard unreachable.** | Is `[web].enabled = true`? The default bind is loopback-only (`127.0.0.1:8666`) — from another machine you must opt in with `listen = "0.0.0.0:8666"` (no auth; trusted networks only). Check the log for a `web server` error if the port was taken. |
