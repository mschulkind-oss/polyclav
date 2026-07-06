# The Native Synth

polyclav ships a small **pure-Rust analog-style synthesizer** built into
`audio-core` — no soundfont file, no plugin, no external dependency. It's
one of the patch backends (alongside SF2/SF3, SFZ, LV2, and CLAP), selected
with `type = "native"` in your config. This doc covers **exactly what it
does today and how to play it**.

> **One-line summary:** a full Minimoog-flavored voice — three
> oscillators (saw / square / pulse) + noise → pre-filter tanh drive →
> Moog-style 4-pole resonant lowpass with its own ADSR (filter env) →
> ADSR amp envelope — with a global LFO (pitch / cutoff / amp),
> mod-wheel vibrato, pitch bend, velocity → amp/cutoff routing, keyboard
> tracking, glide, and **8-voice polyphony** with runtime-switchable
> voice modes. One factory engine (`minimoog`) whose defaults still
> sound exactly like the original single-saw Phase 1 voice until you
> reach for the controls — and every tweak now **persists per patch**.

*(Filter attribution note: earlier revisions called the ladder
"Huovilainen." fundsp's `Moog` is actually the Stilson/Smith musicdsp
variant with a single tanh stage — see `docs/ROADMAP.md` Appendix A.)*

---

## Why you'd use it

- **It needs zero files.** Every other patch type points at a soundfont or
  plugin on disk. A native patch just works — useful as a guaranteed
  fallback (`polyclav` even suggests it in its "no soundfonts installed"
  startup error) and as an instant bass/lead/pad voice.
- **It's a real analog-style signal path**, not a sample player: three
  anti-aliased oscillators plus noise through a driven Moog-style ladder,
  with the whole voice adjustable live from the web dashboard, the API,
  or the Launchkey knob pages.

## What it is, precisely (implemented today)

The per-voice signal chain (`audio-core/src/synth/voice.rs`), times up
to 8 voices in poly mode:

```
3× PolyBLEP osc (saw/square/pulse, ─▶ mixer ─▶ tanh drive ─▶ Moog 4-pole ladder LPF ─▶ ADSR amp env ─▶ × velocity
   octave, detune, level)  + noise  (renorm)   (pre-filter)  (24 dB/oct, resonance,     (runtime      (vel→amp)
                                                             cutoff ← filter ADSR,       A/D/S/R,
        ▲ pitch ← glide, pitch bend,                         kbd track, LFO, vel)        × LFO amp)
          LFO vibrato × mod wheel
```

A single **global block** (shared across voices) computes the LFO, the
mod-wheel scaling, and the pitch-bend factor each sample; each voice
applies them to its own pitch/cutoff/amp.

- **Oscillators** — three anti-aliased oscillators (fundsp `PolySaw` /
  `PolySquare` / `PolyPulse`), each with its own waveform (`saw`,
  `square`, or `pulse`), octave shift (−2..+2), detune (±100 cents), and
  mixer level. The pulse duty cycle is a shared, runtime-adjustable
  **pulse width** (0.05..0.95). (`audio-core/src/synth/oscillator.rs`)
- **Noise** — a white-noise source with its own mixer level
  (deterministic xorshift32, so renders are reproducible). The mix is
  renormalized by `1/max(1, Σlevels)` so cranking every source up
  doesn't overdrive the filter.
- **Drive** — a pre-filter tanh saturator (0..1, 0 = bit-transparent
  bypass) for Moog grit into the ladder.
- **Filter** — 24 dB/oct resonant lowpass Moog-style ladder
  (`fundsp::Moog` — Stilson/Smith-variant, single tanh stage). This is
  the character of the voice. Cutoff and resonance are both live.
  (`audio-core/src/synth/filter.rs`)
- **Filter envelope (env 2)** — a second ADSR modulating the cutoff:
  `effective_cutoff = knob_cutoff × 2^(amount × env × 4)`, i.e. `amount`
  sweeps up to +4 octaves above the knob cutoff at the envelope peak,
  clamped to 20 Hz–20 kHz. At `amount = 0` (the default) the path is
  bit-transparent.
- **Keyboard tracking** — cutoff follows the keyboard by
  `2^(kbd_track × (note − 60)/12)`: at 1.0 the filter opens 2× per
  octave above middle C and closes 2× per octave below it. Default 0.
- **Velocity routing** — `vel→amp` (default **1.0**: the classic
  `vel/127` loudness scaling; 0 = velocity ignored) and `vel→cutoff`
  (default 0: up to ±1 octave around the knob cutoff, centered at
  velocity 64). The global/per-patch velocity curve of
  `docs/VELOCITY_CURVES.md` applies before both, like any backend.
- **LFO** — one global LFO shared by all voices: triangle / saw /
  square / sample-and-hold, rate 0.05–20 Hz, with three depth knobs —
  → pitch (0..100 cents of vibrato, **scaled live by the mod wheel**),
  → cutoff (0..2 octaves), → amp (0..1 tremolo). All depths default 0.
  (`audio-core/src/synth/lfo.rs`)
- **Mod wheel** — scales the LFO→pitch vibrato depth. It boots at 1.0
  so a configured vibrato is audible before the wheel is ever touched;
  the first wheel move takes over (wheel to 0 silences vibrato).
- **Pitch bend** — full 14-bit bend with a configurable range, 0..12
  semitones at full deflection (default ±2).
- **Glide** — a one-pole slew of the base frequency toward the current
  note (0–5 s time constant; 0 = off, pitch jumps instantly). All three
  oscillators glide together.
- **Amp envelope** — linear ADSR, gate-driven from note on/off, fully
  runtime-adjustable. (`audio-core/src/synth/envelope.rs`)
- **Oversampling (optional)** — a 2× oversampled drive + ladder path
  (`oversample = true`), the Appendix-A mitigation for aliasing when
  the ladder is driven hard. Off by default and bit-transparent when
  off.
- **Output** — each voice renders mono; the voice sum is duplicated to
  both stereo channels, with a fixed ×0.5 trim so a raw saw lands at the
  same level ballpark as the soundfont/plugin backends (`synth/mod.rs`,
  `render`). Stereo width, if you want it, comes from the reverb in the
  DSP chain.

### Polyphony and voice modes

The synth runs one of three allocation modes, switchable live (no
restart, no re-selecting the patch):

- **`mono_legato`** (the default — and bit-identical to the historic
  mono behavior): 1 voice, last-note priority; a new note while another
  is held moves the pitch *without re-attacking* the envelopes; release
  falls back to the still-held note.
- **`mono_retrig`**: 1 voice, last-note priority, envelopes always
  retrigger on every note.
- **`poly`**: up to **8 voices** — chords work. A note-on takes a free
  voice or, when all 8 sound, steals the oldest-fired one.

Switch it from the web dashboard's voice-mode selector, the API
(`PATCH /api/synth {"voice_mode": "poly"}`), or the Launchkey LFO/MOD
page's knob 7 (hardware verification pending). The mode persists per
patch like every other synth parameter.

### The factory voice: `minimoog`

There is exactly one engine today. Its defaults are deliberately the
Phase 1 sound — one saw into the ladder, mono — with everything else
zeroed or neutral until you tweak it. Every parameter below is
runtime-adjustable from the web dashboard / `PATCH /api/synth`; the
"Knob page" column is the Launchkey location (`docs/ROADMAP.md` §2 —
code-complete, hardware verification pending, see
`docs/HARDWARE_TESTS.md`):

| Parameter | Default | Range | Knob page (slot) |
|-----------|---------|-------|------------------|
| Osc 1 (wave / octave / detune / level) | saw / 0 / 0 ¢ / 1.0 | wave `saw\|square\|pulse`, oct −2..2, ±100 ¢, 0..1 | OSC (level 1, detune 2); wave/octave web-only |
| Osc 2 (wave / octave / detune / level) | saw / 0 / −7 ¢ / **0.0 (silent)** | same | OSC (level 3, detune 4) |
| Osc 3 (wave / octave / detune / level) | saw / −1 / +5 ¢ / **0.0 (silent)** | same | OSC (level 5, detune 6) |
| Noise level | 0.0 (silent) | 0..1 | OSC (7) |
| Pulse width (shared by pulse oscs) | 0.25 | 0.05..0.95 | OSC (8) |
| Filter cutoff | ~632 Hz at patch select* | 20 Hz..20 kHz (log) | MAIN (4), FILTER (1) |
| Filter resonance (Q) | 0.3 | 0..0.95 | MAIN (5), FILTER (2) |
| Filter env amount | 0.0 (off) | 0..1 ≈ 0..+4 octaves | FILTER (3) |
| Filter env A/D/S/R | 5 ms / 600 ms / 0.4 / 600 ms | A/D/R 0..10 s, S 0..1 | FILTER (4–7) |
| Keyboard tracking | 0.0 (off) | 0..1 | FILTER (8) |
| Amp env A/D/S/R | 5 ms / 200 ms / 0.7 / 400 ms | A/D/R 0..10 s, S 0..1 | AMP (1–4) |
| Velocity → amp | 1.0 (classic vel/127) | 0..1 | AMP (5) |
| Velocity → cutoff | 0.0 (off) | 0..1 ≈ ±1 octave | AMP (6) |
| Drive (pre-filter tanh) | 0.0 (off) | 0..1 | MAIN (7), AMP (7) |
| LFO wave | triangle | `triangle\|saw\|square\|sh` | web/API only |
| LFO rate | 5 Hz | 0.05..20 Hz | LFO/MOD (1) |
| LFO → pitch | 0 ¢ (off) | 0..100 cents (× mod wheel) | LFO/MOD (2) |
| LFO → cutoff | 0 oct (off) | 0..2 octaves | LFO/MOD (3) |
| LFO → amp | 0.0 (off) | 0..1 | LFO/MOD (4) |
| Pitch bend range | 2 st | 0..12 semitones | LFO/MOD (5) |
| Glide | 0 s (off) | 0..5 s | MAIN (6), LFO/MOD (6) |
| Voice mode | `mono_legato` | `mono_legato\|mono_retrig\|poly` | LFO/MOD (7) |
| 2× oversampling | off | bool | web/API only |

"Web / API" means the dashboard's synth section, or `PATCH /api/synth`
on the embedded web server (`docs/WEB_UI.md`) — both gated on a native
patch being selected (409 otherwise).

\* The cutoff knob resets to position 0.5 on its 20 Hz–20 kHz log taper
(≈632 Hz) every time a native patch is selected
(`internal/controls/controls.go`, `defaultCutoffPos`) — cutoff position
is deliberately the one **session-only** parameter; everything else in
the table persists per patch (see below).

### Per-patch persistence

Every synth tweak — from any surface: web, API, or knob page — is
written (debounced, ~2 s) to `state.toml` as a
`[patches.<name>.synth]` sub-table and restored when you select that
patch again. A native patch you've never tweaked gets the factory
defaults above; `state.toml` files written by older builds are
backfilled with the engine defaults for fields they predate, so a
legacy file can't silently zero your velocity response
(`internal/state/state.go`, `fillSynthDefaults`). The one exception is
the cutoff knob *position*, which resets to its default on every patch
select (footnote above).

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

Then just play the keys — notes route through the synth to your audio
out. Want chords? Set the voice mode to `poly` first (dashboard, API, or
LFO/MOD knob 7) — the default is honest-to-Minimoog mono.

### 3. Shape the sound live

Three control surfaces reach the synth today:

- **The Launchkey knob pages** — five pages (MAIN / OSC / FILTER / AMP /
  LFO/MOD) switched with Scene ▲/▼, covering nearly the whole table
  above; the bottom pad row shows which page is active and the screen
  pops each value as you turn. Code-complete but **pending hardware
  verification** (`docs/HARDWARE_TESTS.md` "Knob pages"); the page table
  lives in `docs/USER_GUIDE.md`. MAIN keeps the historic knob 1–4 layout
  (volume / reverb / comp / cutoff), so muscle memory survives.
- **The web dashboard** (`docs/WEB_UI.md`) exposes the whole voice while
  a native patch is current — both the Next.js app at `/app/` and the
  interim page at `/legacy`.
- **The API** — the same controls are scriptable via `PATCH /api/synth`,
  e.g.:

  ```sh
  curl -X PATCH localhost:8666/api/synth \
       -d '{"resonance": 0.8,
            "voice_mode": "poly",
            "lfo": {"rate_hz": 6, "to_pitch_cents": 12},
            "filter_env": {"amount": 0.3},
            "osc": [{"index": 1, "level": 0.7}]}'
  ```

  Fields are all optional and merge over the current values. Ranges:
  `resonance` 0..0.95, `glide` 0..5 (seconds), `noise` 0..1,
  `pulse_width` 0.05..0.95, `drive` 0..1, `kbd_track` 0..1,
  `bend_range` 0..12, `voice_mode` `"mono_legato" | "mono_retrig" |
  "poly"`, `oversample` bool, `filter_env` / `amp_env`
  `attack/decay/release` 0..10 (seconds) and `sustain` (+ filter
  `amount`) 0..1, `vel_routing.to_cutoff/to_amp` 0..1, `lfo.wave`
  `"triangle" | "saw" | "square" | "sh"`, `lfo.rate_hz` 0.05..20,
  `lfo.to_pitch_cents` 0..100, `lfo.to_cutoff_oct` 0..2, `lfo.to_amp`
  0..1, per-osc `wave` `"saw" | "square" | "pulse"`, `octave` −2..2,
  `detune_cents` −100..100, `level` 0..1.

The best way to explore: loop the bass clip while you tweak —
`polyclav --play bass-riff --loop`, then drag sliders
(`docs/AUDITION.md`). Everything you change is saved to the patch as
you go.

---

## Limits & caveats (today)

- **Mono by default** — the factory `minimoog` boots in `mono_legato`;
  chords need `voice_mode = "poly"` (one click/knob away, and it
  persists per patch — but a fresh patch will surprise you once).
- **Knob pages are unverified on hardware** — the page code shipped
  without a Launchkey on the bench; until `docs/HARDWARE_TESTS.md`
  "Knob pages" passes, treat the web dashboard as the reference surface.
- **One LFO, fixed destinations** — no per-voice LFO, no LFO sync to
  tempo, no mod-wheel routing beyond vibrato depth, no FM/wavetable
  engines (`docs/ROADMAP.md` §5 keeps those as open questions).
- **Cutoff position is session-only** — every other parameter persists
  per patch; the cutoff knob resets to ~632 Hz on patch select.
- **Fixed 48 kHz** — `audio-core` runs at a hardcoded 48 kHz sample rate
  (`audio-core/src/lib.rs`). The optional 2× oversampling applies only
  to the drive + ladder stage, not the whole engine.
- **One engine** — `engine` must be `"minimoog"`; any other value fails
  config validation at startup with a clear error
  (`internal/config/config.go`, `knownNativeEngines`).

## What's coming (not yet built)

- **Hardware verification of the knob pages** — the checklist is
  `docs/HARDWARE_TESTS.md` "Knob pages".
- **Stereo voice spread** — voices currently sum to mono before the
  stereo duplicate (a Phase-4 polish lever in `docs/ROADMAP.md` §1.1).
- **LFO tempo sync / more engines (FM, wavetable)** — see
  `docs/ROADMAP.md` §5 and Appendix A.

---

## Where it lives (for the curious)

| Piece | File |
|-------|------|
| Voice (osc + drive + filter + envs) | `audio-core/src/synth/voice.rs` |
| Oscillator | `audio-core/src/synth/oscillator.rs` |
| Moog ladder filter (+ 2× oversampled path) | `audio-core/src/synth/filter.rs` |
| ADSR envelope | `audio-core/src/synth/envelope.rs` |
| Global LFO | `audio-core/src/synth/lfo.rs` |
| Engine, voice allocator, mod wheel / bend | `audio-core/src/synth/mod.rs` |
| Backend selection / param atomics | `audio-core/src/lib.rs` |
| Go FFI (`SetNativePatch`, `SetNativeCutoffHz`, ... `SetNativeLFO`, `SetNativeVoiceMode`, `SetNativeOversample`) | `internal/audio/audio.go` |
| Patch select wiring | `internal/patches/patches.go` |
| Config schema (`type`/`engine` validation) | `internal/config/config.go` |
| Runtime param gating, caching, per-patch persistence | `internal/controls/controls.go` |
| Per-patch synth state schema | `internal/state/state.go` |
| Launchkey knob pages | `internal/controls/pages/` |
| `PATCH /api/synth` + dashboards | `internal/web/server.go`, `web/` (Next.js), `internal/web/static/index.html` |

**FFI note:** selecting a native patch calls
`polyclav_audio_set_native_patch(engine)`, which schedules the swap on a
background thread and returns a status code: `0` = scheduled, `1` = audio
engine not running, `2` = null / invalid UTF-8 engine string, `3` = unknown
engine. The Go side (`audio.SetNativePatch`) turns any non-zero code into an
error. The swap itself lands on the next audio callback
(`audio-core/src/lib.rs`).
