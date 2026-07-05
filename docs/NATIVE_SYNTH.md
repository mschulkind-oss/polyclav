# The Native Synth

polyclav ships a small **pure-Rust analog-style synthesizer** built into
`audio-core` — no soundfont file, no plugin, no external dependency. It's
one of the patch backends (alongside SF2/SF3, SFZ, LV2, and CLAP), selected
with `type = "native"` in your config. This doc covers **exactly what it
does today and how to play it**.

> **One-line summary:** three oscillators (saw / square / pulse) + noise
> → Moog-style 4-pole resonant lowpass with its own ADSR (filter env) →
> ADSR amp envelope, with glide. Monophonic. One factory voice
> (`minimoog`) whose defaults sound exactly like the original single-saw
> Phase 1 voice until you reach for the web dashboard's sliders. Great
> for basses and leads; not yet a full polysynth.

*(Filter attribution note: earlier revisions called the ladder
"Huovilainen." fundsp's `Moog` is actually the Stilson/Smith musicdsp
variant with a single tanh stage — see `docs/ROADMAP.md` Appendix A.)*

---

## Why you'd use it

- **It needs zero files.** Every other patch type points at a soundfont or
  plugin on disk. A native patch just works — useful as a guaranteed
  fallback (`polyclav` even suggests it in its "no soundfonts installed"
  startup error) and as an instant bass/lead voice.
- **It's a real analog-style signal path**, not a sample player: three
  anti-aliased oscillators plus noise through a Moog-style ladder filter,
  with the whole voice adjustable live from the web dashboard.

## What it is, precisely (implemented today)

The per-voice signal chain (`audio-core/src/synth/voice.rs`):

```
3× PolyBLEP osc (saw/square/pulse, ─▶ mixer ─▶ Moog 4-pole ladder LPF ─▶ ADSR amp env ─▶ ×velocity
   octave, detune, level)  + noise  (renorm)   (24 dB/oct, resonance,     (linear A/D/S/R)
                                               cutoff ← filter ADSR × amount)
```

- **Oscillators** — three anti-aliased oscillators (fundsp `PolySaw` /
  `PolySquare` / `PolyPulse`), each with its own waveform (`saw`,
  `square`, or `pulse` — the pulse is a fixed 25% duty cycle), octave
  shift (−2..+2), detune (±100 cents), and mixer level.
  (`audio-core/src/synth/oscillator.rs`)
- **Noise** — a white-noise source with its own mixer level
  (deterministic xorshift32, so renders are reproducible). The mix is
  renormalized by `1/max(1, Σlevels)` so cranking every source up
  doesn't overdrive the filter.
- **Filter** — 24 dB/oct resonant lowpass Moog-style ladder
  (`fundsp::Moog` — Stilson/Smith-variant, single tanh stage). This is
  the character of the voice. Cutoff and resonance are both live.
  (`audio-core/src/synth/filter.rs`)
- **Filter envelope (env 2)** — a second ADSR modulating the cutoff:
  `effective_cutoff = knob_cutoff × 2^(amount × env × 4)`, i.e. `amount`
  sweeps up to +4 octaves above the knob cutoff at the envelope peak,
  clamped to 20 Hz–20 kHz. At `amount = 0` (the default) the path is
  bit-transparent.
- **Glide** — a one-pole slew of the base frequency toward the current
  note (0–5 s time constant; 0 = off, pitch jumps instantly). All three
  oscillators glide together.
- **Amp envelope** — linear ADSR, gate-driven from note on/off.
  (`audio-core/src/synth/envelope.rs`)
- **Velocity** — note velocity scales voice output linearly (`vel / 127`),
  so the patch responds to how hard you play. (The global/per-patch
  velocity curve of `docs/VELOCITY_CURVES.md` applies before this, like
  any backend.)
- **Output** — the voice renders mono and is duplicated to both stereo
  channels, with a fixed ×0.5 trim so a raw saw lands at the same level
  ballpark as the soundfont/plugin backends (`synth/mod.rs`, `render`).
  Stereo width, if you want it, comes from the reverb in the DSP chain.

### The factory voice: `minimoog`

There is exactly one engine today. Its defaults are deliberately the
Phase 1 sound — one saw into the ladder — with the new sections zeroed
out until you tweak them (`audio-core/src/synth/voice.rs`,
`synth/mod.rs`):

| Parameter | Default | Adjustable at runtime? |
|-----------|---------|------------------------|
| Osc 1 (wave / octave / detune / level) | saw / 0 / 0 ¢ / 1.0 | ✅ web / API |
| Osc 2 (wave / octave / detune / level) | saw / 0 / −7 ¢ / **0.0 (silent)** | ✅ web / API |
| Osc 3 (wave / octave / detune / level) | saw / −1 / +5 ¢ / **0.0 (silent)** | ✅ web / API |
| Noise level | 0.0 (silent) | ✅ web / API |
| Filter cutoff | ~632 Hz at startup* | ✅ **Launchkey knob 4** or web / API |
| Filter resonance (Q) | 0.3 | ✅ web / API (0..0.95) |
| Filter env A/D/S/R | 5 ms / 600 ms / 0.4 / 600 ms | ✅ web / API |
| Filter env amount | 0.0 (off) | ✅ web / API (0..1 ≈ 0..+4 octaves) |
| Glide | 0 s (off) | ✅ web / API (0..5 s) |
| Amp attack | 5 ms | ❌ fixed |
| Amp decay | 200 ms | ❌ fixed |
| Amp sustain | 0.7 | ❌ fixed |
| Amp release | 400 ms | ❌ fixed |

"Web / API" means the dashboard's **Native synth** section, or
`PATCH /api/synth` on the embedded web server (`docs/WEB_UI.md`) — both
gated on a native patch being selected (409 otherwise). Only cutoff has
a Launchkey mapping today (knob 4); the rest is browser-only until the
knob-pages UX lands (`docs/ROADMAP.md` §2).

\* The daemon boots the cutoff knob at position 0.5 on its 20 Hz–20 kHz
log taper, ≈632 Hz (`internal/controls/controls.go`,
`defaultCutoffPos`), so that's what you hear first.

### Polyphony — read this

Despite the pool being sized for 4 voices, **the native synth is
monophonic today.** It runs **mono-legato, last-note priority**: play a
chord and you hear only the most recently pressed note. Hold a key and
press another and the pitch moves to the new note *without re-attacking*
the envelope (that's the legato part); release it and the pitch falls
back to the still-held note (`audio-core/src/synth/mod.rs`,
`note_on`/`note_off`). With glide at 0 (the default) the pitch change is
instant; raise glide and it slews. True polyphony is scaffolded but
stubbed (Phase 3 in `docs/ROADMAP.md`).

---

## How to use it

### 1. Add a native patch to your config

In `~/.config/polyclav/polyclav.toml`, a native patch is a `[[patches]]`
entry with `type = "native"` and `engine = "minimoog"` — **no `soundfont`
path required**:

```toml
[[patches]]
name      = "moog-bass"
display   = "Moog Bass"     # shown on the Launchkey screen
type      = "native"
engine    = "minimoog"      # the only valid engine today
pad_color = 33              # vibrant cyan (Launchkey pad lit color)
gain_db   = 0.0             # per-patch loudness trim, like any patch
```

This exact entry already exists (commented context) in
`polyclav.example.toml`. `gain_db` and the global knob state
(volume/reverb/comp) apply to native patches just like soundfont patches.

> **Gotcha — pad placement.** Top-row pads map to the **first 8**
> `[[patches]]` entries in order. In the shipped example config the native
> entry is the 10th, so it has no pad and you can't select it from hardware.
> **Move it into the first 8 entries** if you want to reach it from a pad.

### 2. Select and play it

- **From the Launchkey:** tap the top-row pad for its slot. The screen shows
  your `display` name; the pad lights in `pad_color`.
- **From the web dashboard:** enable `[web]` in your config and click the
  patch in the grid at `http://127.0.0.1:8666/` (or
  `POST /api/patches/{name}/select`).
- **Without either:** selection is restored from `state.toml`; with no
  saved state, polyclav selects the **first** patch in your config — so
  put the native patch first to play it on a plain keyboard.

Then just play the keys — notes route through the synth to your audio out.

### 3. Shape the sound live

Two control surfaces reach the synth today:

- **Launchkey knob 4 → filter cutoff**, active only while a native patch
  is selected. It sweeps a log-tapered **20 Hz – 20 kHz** range and the
  screen shows the current Hz while you turn it. Knobs 1/2/3 keep their
  global roles (master volume, reverb, input compressor) and apply
  *after* the synth. Knob 4 is the **only** synth parameter on the
  hardware for now.
- **The web dashboard** (`docs/WEB_UI.md`) exposes the whole voice while
  a native patch is current: cutoff, resonance, glide, noise, the filter
  ADSR + env amount, and each oscillator's wave / octave / detune /
  level. The same controls are scriptable via `PATCH /api/synth`, e.g.:

  ```sh
  curl -X PATCH localhost:8666/api/synth \
       -d '{"resonance": 0.8,
            "filter_env": {"amount": 0.3},
            "osc": [{"index": 1, "level": 0.7}]}'
  ```

  Fields are all optional and merge over the current values. Ranges:
  `resonance` 0..0.95, `glide` 0..5 (seconds), `noise` 0..1,
  `filter_env.attack/decay/release` 0..10 (seconds),
  `filter_env.sustain/amount` 0..1, per-osc `wave` `"saw" | "square" |
  "pulse"`, `octave` −2..2, `detune_cents` −100..100, `level` 0..1.

The best way to explore: loop the bass clip while you tweak —
`polyclav --play bass-riff --loop`, then drag sliders
(`docs/AUDITION.md`). The amp ADSR is still fixed in code.

---

## Limits & caveats (today)

- **Monophonic** — no chords (see above). This is the biggest surprise.
- **No LFO, no modulation routing** — the filter envelope is the only
  modulator; vibrato/wah/tremolo are Phase 3.
- **Amp envelope is fixed** — 5 ms / 200 ms / 0.7 / 400 ms in code; only
  the *filter* envelope is adjustable.
- **On the Launchkey, knob 4 only** — cutoff is the sole hardware-mapped
  synth parameter. Everything else (resonance, filter env, oscillators,
  noise, glide) needs the web dashboard or `PATCH /api/synth`; the knob
  pages UX is roadmap (`docs/ROADMAP.md` §2).
- **No pitch bend / mod wheel** — these MIDI messages are accepted but
  silently dropped by the native backend (`synth/mod.rs`, `handle_event`).
- **Tweaks aren't saved** — neither the cutoff position nor any of the
  Phase 2 parameters persist across a restart; everything resets to the
  defaults above (per-patch synth-param persistence in `state.toml` is
  roadmap).
- **Fixed 48 kHz** — `audio-core` runs at a hardcoded 48 kHz sample rate
  (`audio-core/src/lib.rs`).
- **One engine** — `engine` must be `"minimoog"`; any other value fails
  config validation at startup with a clear error
  (`internal/config/config.go`, `knownNativeEngines`).

## What's coming (not yet built)

`docs/ROADMAP.md` scopes the full vision and is explicitly forward-looking —
none of the below exists in code yet:

- **Velocity → filter/amp routing** — composes with the velocity curves
  of `docs/VELOCITY_CURVES.md` (the curve shapes the input; routing
  decides what velocity modulates).
- **Phase 3** — real polyphony (voice stealing) with selectable voice
  modes, LFO with multiple destinations, modulation routing.
- **Phase 4** — polish, a MOD page, and possible FM/wavetable engines.
- **Launchkey "knob pages" UX** — the whole voice on hardware knobs, not
  just cutoff on knob 4.
- **Per-patch synth parameters in `state.toml`** so your tweaks persist and
  each native patch can have its own cutoff/resonance/envelopes.

Until then: treat the native synth as a clean, no-dependency **mono
bass / lead** — now with a full source section and filter envelope to
sculpt in the browser. For polyphonic voices, use a soundfont, SFZ, or a
plugin patch.

---

## Where it lives (for the curious)

| Piece | File |
|-------|------|
| Voice (osc + filter + env) | `audio-core/src/synth/voice.rs` |
| Oscillator | `audio-core/src/synth/oscillator.rs` |
| Moog ladder filter | `audio-core/src/synth/filter.rs` |
| ADSR envelope | `audio-core/src/synth/envelope.rs` |
| Engine + voice allocator | `audio-core/src/synth/mod.rs` |
| Backend selection / param atomics | `audio-core/src/lib.rs` |
| Go FFI (`SetNativePatch`, `SetNativeCutoffHz`, `SetNativeResonance`, `SetNativeFilterEnv`, `SetNativeOsc`, `SetNativeNoise`, `SetNativeGlide`) | `internal/audio/audio.go` |
| Patch select wiring | `internal/patches/patches.go` |
| Config schema (`type`/`engine` validation) | `internal/config/config.go` |
| Runtime param gating, knob-4 + web → synth mapping | `internal/controls/controls.go` |
| `PATCH /api/synth` + dashboard | `internal/web/server.go`, `internal/web/static/index.html` |

**FFI note:** selecting a native patch calls
`polyclav_audio_set_native_patch(engine)`, which schedules the swap on a
background thread and returns a status code: `0` = scheduled, `1` = audio
engine not running, `2` = null / invalid UTF-8 engine string, `3` = unknown
engine. The Go side (`audio.SetNativePatch`) turns any non-zero code into an
error. The swap itself lands on the next audio callback
(`audio-core/src/lib.rs`).
