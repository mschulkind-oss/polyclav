# The Native Synth

polyclav ships a small **pure-Rust analog-style synthesizer** built into
`audio-core` — no soundfont file, no plugin, no external dependency. It's
one of the patch backends (alongside SF2/SF3, SFZ, LV2, and CLAP), selected
with `type = "native"` in your config. This doc covers **exactly what it
does today and how to play it**.

> **One-line summary:** a single sawtooth oscillator → Moog-style 4-pole
> resonant lowpass → ADSR amp envelope. Monophonic. One factory voice
> (`minimoog`). Great for basses and leads; not yet a full polysynth.

---

## Why you'd use it

- **It needs zero files.** Every other patch type points at a soundfont or
  plugin on disk. A native patch just works — useful as a guaranteed
  fallback (`polyclav` even suggests it in its "no soundfonts installed"
  startup error) and as an instant bass/lead voice.
- **It's a real analog-style signal path**, not a sample player: anti-aliased
  saw through a Huovilainen Moog ladder filter with live cutoff control.

## What it is, precisely (implemented today)

The per-voice signal chain (`audio-core/src/synth/voice.rs`):

```
PolyBLEP saw osc ──▶ Moog 4-pole ladder LPF (24 dB/oct) ──▶ ADSR amp env ──▶ ×velocity
   (fundsp)              (fundsp, Huovilainen)              (linear A/D/S/R)
```

- **Oscillator** — one anti-aliased sawtooth (`fundsp::PolySaw`). One
  waveform, one oscillator, no detune/octave/sub.
  (`audio-core/src/synth/oscillator.rs`)
- **Filter** — 24 dB/oct resonant lowpass, Huovilainen Moog ladder
  (`fundsp::Moog`). This is the character of the voice.
  (`audio-core/src/synth/filter.rs`)
- **Amp envelope** — linear ADSR, gate-driven from note on/off.
  (`audio-core/src/synth/envelope.rs`)
- **Velocity** — note velocity scales voice output linearly (`vel / 127`),
  so the patch responds to how hard you play.
- **Output** — the voice renders mono and is duplicated to both stereo
  channels, with a fixed ×0.5 trim so a raw saw lands at the same level
  ballpark as the soundfont/plugin backends (`synth/mod.rs`, `render`).
  Stereo width, if you want it, comes from the reverb in the DSP chain.

### The factory voice: `minimoog`

There is exactly one engine today. Its hardcoded defaults
(`audio-core/src/synth/voice.rs`, `synth/mod.rs`):

| Parameter | Default | Adjustable at runtime? |
|-----------|---------|------------------------|
| Oscillator | Sawtooth | ❌ fixed |
| Filter cutoff | ~632 Hz at startup* | ✅ **Launchkey knob 4** |
| Filter resonance (Q) | 0.3 | ❌ fixed |
| Amp attack | 5 ms | ❌ fixed |
| Amp decay | 200 ms | ❌ fixed |
| Amp sustain | 0.7 | ❌ fixed |
| Amp release | 400 ms | ❌ fixed |

\* The synth's internal default cutoff is 2 kHz, but the daemon pushes the
knob-4 start position (≈632 Hz on its log curve) at startup
(`cmd/polyclav/main.go:222`), so that's what you actually hear first.

### Polyphony — read this

Despite the pool being sized for 4 voices, **the native synth is
monophonic today.** It runs **mono-legato, last-note priority**: play a
chord and you hear only the most recently pressed note. Hold a key and
press another and the pitch jumps to the new note *without re-attacking*
the envelope (that's the legato part — there is **no glide/portamento**,
the pitch change is instant); release it and the pitch falls back to the
still-held note (`audio-core/src/synth/mod.rs`, `note_on`/`note_off`).
True polyphony is scaffolded but stubbed (Phase 3 in `docs/ROADMAP.md`).

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
- **Without a Launchkey:** patch selection today is pad-driven (or restored
  from `state.toml`). With no control surface and no saved state, polyclav
  selects the **first** patch in your config — so put the native patch first
  to play it on a plain keyboard.

Then just play the keys — notes route through the synth to your audio out.

### 3. Shape the sound live: filter cutoff (knob 4)

The one real-time control is **Launchkey knob 4 → filter cutoff**, active
only while a native patch is selected (`cmd/polyclav/main.go:266`). It sweeps
a log-tapered **20 Hz – 20 kHz** range and the screen shows the current Hz
while you turn it. Knobs 1/2/3 keep their global roles (master volume,
reverb, input compressor) and apply *after* the synth.

Everything else (waveform, resonance, the ADSR) is fixed in code for now.

---

## Limits & caveats (today)

- **Monophonic** — no chords (see above). This is the biggest surprise.
- **One voice, one waveform** — a single saw; no detune, sub-osc, or noise.
- **Only cutoff is live** — resonance and the envelope aren't adjustable at
  runtime or in config; there's no config field for them yet.
- **Cutoff needs a Launchkey** — without knob 4 there's no way to change the
  cutoff; it stays at the ~632 Hz startup value.
- **No pitch bend / mod wheel** — these MIDI messages are accepted but
  silently dropped by the native backend (`synth/mod.rs`, `handle_event`).
- **Cutoff isn't saved** — the knob-4 sweep position isn't persisted; on
  the next launch the cutoff resets to its ~632 Hz startup value
  (state.toml persistence for synth params is Phase 2).
- **Fixed 48 kHz** — `audio-core` runs at a hardcoded 48 kHz sample rate
  (`audio-core/src/lib.rs:39`).
- **One engine** — `engine` must be `"minimoog"`; any other value fails
  config validation at startup with a clear error
  (`internal/config/config.go`, `knownNativeEngines`).

## What's coming (not yet built)

`docs/ROADMAP.md` scopes the full vision and is explicitly forward-looking —
none of the below exists in code yet:

- **Phase 2** — full Minimoog voice (3 oscillators + mixer + noise), a
  dedicated filter envelope, glide, and a Launchkey "knob pages" UX so more
  than just cutoff is tweakable.
- **Phase 3** — real polyphony (voice stealing), LFO with multiple
  destinations, modulation routing.
- **Phase 4** — polish, a MOD page, and possible FM/wavetable engines.
- **Per-patch synth parameters in `state.toml`** so your tweaks persist and
  each native patch can have its own cutoff/resonance/ADSR.

Until then: treat the native synth as a clean, no-dependency **mono
saw-bass / lead** with a live filter sweep. For polyphonic or richer voices,
use a soundfont, SFZ, or a plugin patch.

---

## Where it lives (for the curious)

| Piece | File |
|-------|------|
| Voice (osc + filter + env) | `audio-core/src/synth/voice.rs` |
| Oscillator | `audio-core/src/synth/oscillator.rs` |
| Moog ladder filter | `audio-core/src/synth/filter.rs` |
| ADSR envelope | `audio-core/src/synth/envelope.rs` |
| Engine + voice allocator | `audio-core/src/synth/mod.rs` |
| Backend selection / cutoff atomic | `audio-core/src/lib.rs` |
| Go FFI (`SetNativePatch`, `SetNativeCutoffHz`) | `internal/audio/audio.go` |
| Patch select wiring | `internal/patches/patches.go` |
| Config schema (`type`/`engine` validation) | `internal/config/config.go` |
| Knob-4 → cutoff mapping | `cmd/polyclav/main.go` |

**FFI note:** selecting a native patch calls
`polyclav_audio_set_native_patch(engine)`, which schedules the swap on a
background thread and returns a status code: `0` = scheduled, `1` = audio
engine not running, `2` = null / invalid UTF-8 engine string, `3` = unknown
engine. The Go side (`audio.SetNativePatch`) turns any non-zero code into an
error. The swap itself lands on the next audio callback
(`audio-core/src/lib.rs`).
