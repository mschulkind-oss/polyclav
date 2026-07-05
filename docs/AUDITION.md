# Design: Audition Mode — hear everything without a keyboard

> **Status:** P1+P2 shipped 2026-07-05 — `polyclav --play <clip> [--loop]
> [--tempo N]`, the seven built-in patterns, and the web transport
> (`/api/clips` + `/api/player` and the dashboard's Audition section; see
> `docs/USER_GUIDE.md` "Audition mode"). Still to come: SMF user clips
> from `~/.local/share/polyclav/clips/` and bundled clips. Companions:
> `docs/WEB_UI.md` (the transport controls and per-setting demo buttons
> live there) and `docs/VELOCITY_CURVES.md` (the velocity ramp clip is
> the curve editor's soundtrack).

## Goal

Walk through every setting polyclav has and **hear what it does, live,
with no keyboard connected**: pick a representative clip, loop it, and
tweak the setting while it plays. Concretely:

- Browse patches and hear each one on material that suits it.
- Drag the velocity curve while a velocity ramp loops, and hear the
  layers move.
- Sweep the native synth cutoff over a looping bass line.
- Adjust reverb / mastering comp / limiter ceiling against a sustained
  chord and hear the tail change.

## Why it's cheap: the audio path is already keyboard-agnostic

MIDI input is just `audio.PushMIDI(ev)` called from the rtmidi listener
goroutine (`internal/audio/audio.go:201`). On the Rust side events land
in a lock-free `crossbeam ArrayQueue` drained by the audio callback
(`audio-core/src/lib.rs:133`) — **any** goroutine can push. A clip
player is therefore a pure-Go feature: a goroutine that schedules
`midi.Event`s from a clip and pushes them down the exact same funnel a
keyboard uses. No Rust changes, no FFI changes, identical latency path.

Parsing `.mid` files is also free: `gitlab.com/gomidi/midi/v2` (already
in `go.mod`) ships an SMF reader (`.../smf`). Zero new dependencies.

## Clip sources — three kinds, in priority order

### 1. Generative diagnostic patterns (v1, the core)

Coded patterns, zero assets, **purpose-built to expose one setting
each**. Deterministic (fixed seeds) so A/B tweaks are a fair comparison.
The starting library:

| pattern id | what it plays | built to demo |
|---|---|---|
| `vel-ramp` | same note repeated, velocity stepping 1 → 127 and back | **velocity curves** — you hear each layer boundary move as you drag γ |
| `sustain-chord` | rich chord held ~4 s, then 4 s of silence | reverb tail, mastering comp, limiter ceiling |
| `arp` | 1-bar arpeggio loop, adjustable tempo | general patch character, envelope feel, patch A/B |
| `bass-riff` | low-register 2-bar riff, mono-friendly | **native synth** — cutoff sweeps on knob 4 / web slider |
| `chromatic` | full-range chromatic walk, fixed velocity | sample-layer seams, per-register balance, high-note aliasing check |
| `staccato` | repeated short notes at increasing rate | attack/release transients, compressor pumping |
| `burst` | dense chord showers | polyphony stress, CPU headroom, limiter behavior |

Each pattern is a pure function `(tick) → []midi.Event` — trivially
unit-testable, and adding one is a ~20-line PR.

### 2. User clips (v1.5)

Any `.mid` dropped in `~/.local/share/polyclav/clips/` shows up in the
clip list. Multi-track files are flattened; events play on channel 1.
This is the "loop the intro of the song I actually play" feature.

### 3. Bundled musical clips (later, optional)

Short public-domain musical excerpts for realism the patterns lack
(a Bach prelude fragment for pianos, a jazz voicing loop for EPs).
Distribution follows the existing soundfont pattern: **not** committed
to the repo — fetched by `polyclav bootstrap` with per-item license
provenance (`internal/bootstrap`), or embedded only if we author them
ourselves (a generative pattern saved to SMF is self-authored — likely
the path of least license friction).

## Architecture

```
clip registry (generative patterns + scanned .mid files)
        │
        ▼
internal/player ── goroutine: schedules events on wall clock,
        │          tempo-scaled, loop-aware, tracks held notes
        ▼
velocity curve (docs/VELOCITY_CURVES.md — applied, deliberately)
        ▼
audio.PushMIDI  ──▶  crossbeam queue ──▶ audio thread ──▶ ears
```

### The player (`internal/player`)

- **Transport state:** current clip, playing/stopped, loop on/off, tempo
  multiplier (0.25×–2×), all settable while running.
- **Scheduling:** wall-clock (`time.Timer` to the next event's delta).
  Jitter of a millisecond or two is identical in kind to a human playing
  over USB — this is an audition tool, not a sequencer, and the doc
  says so.
- **Stop is tidy:** the player tracks its held notes and emits NoteOffs
  for all of them on stop/loop-seam/clip-switch. No stuck notes, ever.
- **Coexistence:** the keyboard (if present) and the player both just
  push events; no interlock. Playing along with a looping clip is a
  feature, not a bug.

### What the clip stream does and doesn't touch

- **Velocity curve: applied.** The whole point of `vel-ramp` is hearing
  the curve; player events go through the same remap as keyboard notes.
- **OSC mapper: bypassed.** Clip note events must never fire
  `[osc.xr18.bindings]` (a `note`-sourced binding would mute mixer
  channels in time with the music). The player feeds the synth fork
  only — same split the velocity doc already requires.
- **Launchkey DAW layer: untouched.** Pads/knobs keep working during
  playback.

### Control surfaces for the player

- **Web UI (primary):** a transport bar (clip picker, play/stop, loop,
  tempo) in the phase-B UI, plus the glue that makes this design sing:
  **every settings group gets a "▶ demo" button** that starts its mapped
  pattern looping (velocity editor → `vel-ramp`, mastering → 
  `sustain-chord`, native synth → `bass-riff`, patch grid → `arp`).
  API: `GET /api/clips`, `POST /api/player {clip, loop, tempo}`,
  `POST /api/player/stop`, `player-state` SSE events.
- **Daemon flag (v1, ships first):** `polyclav --play vel-ramp` (or
  `--play arp --loop`) starts playback at boot. This makes the player
  useful — and testable end-to-end — before any web UI exists, and
  doubles as a hardware-free smoke test (`docs/HARDWARE_TESTS.md`
  gains a no-hardware section).

## Interactions worth stating

- **Mono native synth vs. chord clips:** `sustain-chord` and `burst` on
  the mono-legato native engine collapse to one note
  (`docs/NATIVE_SYNTH.md`) — the clip picker should mark chordal
  patterns as "(poly patches)" rather than pretend otherwise.
- **Velocity monitor synergy:** player events flow through the same
  SSE velocity monitor as keyboard notes (`docs/VELOCITY_CURVES.md`),
  so looping `vel-ramp` paints the full in→out curve as moving dots
  while you drag it. This is the flagship demo of the whole web UI.
- **State persistence:** the player is session-only — nothing about
  transport state goes in `state.toml`. A restart comes up silent.

## Phasing

| Phase | Ships | Needs |
|---|---|---|
| **P1** | `internal/player`, the seven generative patterns, `--play`/`--loop` daemon flags, NoteOff hygiene, unit tests on pattern output + scheduling | nothing else — usable from the CLI immediately |
| **P2** | web transport bar + per-setting demo buttons + SSE state | `docs/WEB_UI.md` phase B |
| **P3** | SMF user-clips dir, multi-track flatten, clip list API surfacing files | P1 (SMF lib already vendored) |
| **P4** | bundled musical clips via bootstrap (license-vetted or self-authored) | appetite |

P1 is deliberately tiny: one package, no new deps, no UI — and it
already delivers "hear a patch without a keyboard."

## Open Questions

1. **Pattern gain safety:** `burst` at high velocity through a hot patch
   can slam the limiter. Should patterns carry a per-pattern velocity
   ceiling, or is the mastering limiter (always in-chain) enough?
2. **Reference register per patch type:** should `arp`/`chromatic`
   transpose based on the selected patch (bass patch → down an octave)?
   Nice-to-have; v1 says no, patterns are fixed.
3. **Clip channel semantics:** flatten all SMF tracks/channels to
   channel 1, or preserve channels? (Nothing downstream is
   multi-timbral today — flatten, revisit if that changes.)
4. **Should `--play` without a config'd soundfont fall back to the
   native patch** so a fresh install can hear *something* in one
   command? (`polyclav --play arp` as the ultimate quickstart.)
