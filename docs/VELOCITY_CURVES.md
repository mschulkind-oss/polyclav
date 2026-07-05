# Design: Velocity Curves

> **Status:** v1 shipped 2026-07-05 — gamma presets (soft / linear /
> hard / custom) + output clamp in `[midi.velocity]`, with per-patch
> `velocity_curve` / `velocity_gamma` overrides (see `docs/USER_GUIDE.md`
> "Velocity curves"). Still to come: v2 control points and the web UI's
> curve editor + velocity monitor. Companion doc: `docs/WEB_UI.md`.

## Problem

polyclav passes note velocity through **completely raw**. The path:

```
keyboard ─▶ internal/midi/midi.go:224 (parse NoteOn, Vel byte)
         ─▶ cmd/polyclav/main.go onMIDIEvent (forwards as-is)
         ─▶ internal/audio/audio.go:204 PushMIDI → C FFI note_on(vel)
         ─▶ backend (oxisynth / sfizz / plugin / native synth)
```

No remapping happens anywhere. That's a problem because the *feel* of a
patch is the product of two curves you don't control:

- **The keybed's.** Every keyboard has its own velocity response; a
  semi-weighted Launchkey and a weighted digital piano send very different
  values for the same finger effort. (The Launchkey has onboard velocity
  settings, but they're device-global, device-specific, and don't help any
  other keyboard.)
- **The patch's.** A multi-layer SFZ like Salamander crossfades sample
  layers by velocity; a DX7 SF2 might barely respond. The same keybed
  feels "shouty" on one patch and "dead" on another — which is exactly why
  this needs to be **per-patch**, not just global.

`gain_db` can't fix this: it scales loudness uniformly, it doesn't change
how hard you have to play to get there.

## Design

### Where the remap lives: Go, at the funnel point

Apply the curve in Go, in the note-event path just before
`audio.PushMIDI` — **not** in Rust:

- One remap covers **every** backend uniformly (oxisynth, sfizz, LV2,
  CLAP, native) with zero per-backend work.
- The real-time audio thread is untouched. `onMIDIEvent` runs on the
  rtmidi listener goroutine, where a float `pow` per keypress is nothing.
- It composes with backends' own velocity handling rather than fighting
  it: for a layered SFZ, remapping the *input* velocity is precisely how
  you shift which layers you reach — that's the point.

**One subtlety — don't remap the OSC fork.** `onMIDIEvent` forwards the
same event to two places: the synth (`audio.PushMIDI`) and the OSC mapper
(`mapper.Dispatch`). A `source_kind = "note"` binding with
`transform = "scalar"` uses raw velocity as the fader value
(`internal/osc/mapper.go`, `raw = ev.Vel`), and `"press"` bindings key on
`vel > 0`. The curve must apply **only on the synth fork**; the mapper
keeps the untouched event.

### Curve model: gamma first, control points later

**v1 — gamma (power curve) + output clamp:**

```
out(0)      = 0                                   # NoteOn vel 0 stays NoteOff
out(v≥1)    = clamp(round(127 · (v/127)^γ), out_min, out_max)
```

- `γ < 1` lifts the middle — less effort to reach loud layers ("**soft**"
  curve, for heavy keybeds / quiet patches).
- `γ > 1` suppresses the middle — more headroom before the loud layers
  ("**hard**" curve, for light keybeds / patches that jump to fortissimo).
- `out_min ≥ 1` guarantees no played note ever remaps to a NoteOff;
  raising it (e.g. 20) also acts as a floor so ppp notes still speak.
  `out_max` caps the top (never trigger the hammer-noise layer, etc.).

One intuitive parameter, monotonic by construction, covers the large
majority of "this patch feels wrong" cases. Named presets so nobody has
to think in exponents — starting values, tune by ear:

| preset | γ |
|---|---|
| `soft` | 0.6 |
| `linear` | 1.0 |
| `hard` | 1.6 |

**v2 — piecewise-linear control points** (the DAW-style editor), e.g.
`points = [[0,0], [64,90], [127,127]]`, linearly interpolated, validated
monotonic non-decreasing. This is what the web UI's drag-the-curve editor
edits; gamma remains the config-friendly shorthand. A raw 128-entry table
is rejected: maximal flexibility, hand-uneditable, nothing the control
points can't express to within ±1.

### Config schema

Global default plus per-patch override — patch wins:

```toml
[midi.velocity]                # global default for all patches
curve = "linear"               # "soft" | "linear" | "hard" | "custom"
# gamma = 0.8                  # required iff curve = "custom"
# out_min = 1                  # optional clamps, defaults 1 / 127
# out_max = 127

[[patches]]
name           = "salamander"
# ...existing fields...
velocity_curve = "soft"        # per-patch override (or velocity_gamma = 0.7)
```

Validation at `config.Load` time, same pattern as patch types: unknown
curve name or non-monotonic/out-of-range values → startup error listing
every offender at once (the existing "errors not warnings" rule).

### Implementation sketch

1. **`internal/velocity` package** — pure and tiny:
   ```go
   type Curve struct { /* gamma, outMin, outMax; later: points */ }
   func FromConfig(c config.VelocityConfig) (Curve, error)
   func (c Curve) Apply(v uint8) uint8   // total, monotonic, 0→0
   ```
   Table-driven tests: endpoints (0→0, 127→out_max), monotonicity over
   all 128 inputs for representative γ values, clamp behavior, preset
   name resolution.
2. **Active-curve holder** — an `atomic.Pointer[velocity.Curve]` owned by
   the daemon; recomputed on patch select (registry already has the
   select path; the same hook that applies `gain_db` swaps the curve).
3. **Wiring** — in `onMIDIEvent` (`cmd/polyclav/main.go`), NoteOn only:
   `ev.Vel = curve.Load().Apply(ev.Vel)` on a *copy* forwarded to
   `audio.PushMIDI`; the original event goes to `mapper.Dispatch`.
4. **Config** — `VelocityConfig` in `internal/config`, global +
   per-patch field, validation as above.

No Rust changes, no FFI changes, no audio-thread changes. Small enough to
TDD end-to-end in one sitting.

### Live tweaking (web UI tie-in)

Phase C of `docs/WEB_UI.md` adds the editor page:

- **Curve graph** — the in→out mapping drawn 0–127; gamma slider in v1,
  draggable control points in v2.
- **Velocity monitor** — the killer feature for tuning: every NoteOn
  streams `(in, out)` over the SSE channel and renders as dots on the
  curve. Play the key, *see* where your touch lands, drag the curve until
  the layers sit right. This turns minutes of TOML-edit-and-restart into
  seconds.
- **Persistence:** edits apply in-memory immediately (atomic pointer
  swap); an explicit **Save** writes the value back to `polyclav.toml`
  via the phase-C config write path. No silent config mutation.

Until the web UI exists, the config file is the interface — which is why
gamma-with-presets (one line to try) matters more in v1 than the control
points.

### Interaction with layered soundfonts — document, don't hide

Remapping velocity changes **which sample layers trigger** in multi-layer
SFZ instruments, not just loudness. That's the feature, but it should be
stated in the user guide: a `soft` curve on Salamander means reaching the
forte layers with less force — timbre change included. Users coming from
"velocity = volume" mental models will otherwise report it as a bug.

## Out of scope (v1)

- Curves for aftertouch, CC, or pitch bend — notes only.
- Per-MIDI-channel curves — polyclav is a single-instrument host.
- Release-velocity shaping — nothing downstream consumes it today.
- NoteOff velocity passthrough is unchanged.

## Open Questions

1. **Preset values:** are γ = 0.6 / 1.6 the right endpoints for `soft` /
   `hard`? Needs an evening with the Launchkey + Salamander + the DX7
   SF2. Cheap to retune — they're named presets precisely so the numbers
   can move without breaking configs.
2. **Should the native synth get the curve too?** Its velocity response
   is a bare linear `vel/127` (`audio-core/src/synth/voice.rs`). Applying
   the curve there (yes, by default — it's just another backend at the
   funnel point) is consistent; noting it here because its Phase 2 patch
   format may grow its own velocity-sensitivity parameter
   (`docs/ROADMAP.md` AMP page), and the two shouldn't double-apply
   confusion.
3. **Curve state in `state.toml`?** If the web UI lets you tweak gamma
   live, should the tweak survive a restart *without* an explicit save
   (like knob values do), or is the curve config-only with explicit save
   (current proposal)? Leaning explicit-save: feel is a deliberate
   setting, not a performance gesture.
