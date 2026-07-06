# polyclav — Native Synth Roadmap (Phases 2-4)

> **Phase 1 shipped.** polyclav already has a `SynthBackend::Native`
> variant in `audio-core`: a Moog-flavored, pure-Rust subtractive synth.
> Today it ships a **single-oscillator, monophonic (mono-legato) subset**
> with a minimal hardcoded knob mapping — the 4-voice pool and poly
> allocator are scaffolded but stubbed (Phase 3). This document scopes
> **Phases 2-4** —
> the full Minimoog voice, the Launchkey-native front-panel UX, and the
> per-patch persistence model — all of which is forward-looking design for
> code that does not yet exist.

The DSP foundation is **fundsp** (MIT OR Apache-2.0), chosen for its
PolyBLEP oscillators, 4-pole Moog-style ladder (`moog_hz` / `moog_q` — a
Stilson/Smith-variant with a single tanh stage, **not** Huovilainen; see
Appendix A),
ADSR (`adsr_live`), saturation, and `oversample()` wrapper. `mi-plaits-dsp-rs`
(MIT) is reserved for a possible Phase 4+ FM/wavetable expansion. Everything
license-clean for static linking under polyclav's Apache 2.0.

The first full patch targets a **classic Minimoog Model D** flavor:
3 oscillators, mixer with noise, 24 dB/oct Moog ladder filter, two ADSR
envelopes (amp and filter), LFO with three destinations, glide, and
mono-legato + poly modes. Subsequent patches reuse the same voice with
different defaults (Mother-32-style mono lead, Matriarch-style poly pad,
etc.).

---

## 0. Design docs & cross-cutting workstreams (2026-07-04)

The native synth no longer roadmaps alone. Five design docs landed
2026-07-04; together with this document they form the project plan:

| Doc | Scope | Status |
|---|---|---|
| `docs/CONFIGURABILITY.md` | The four hardware seams (audio, MIDI in, OSC mixer, control surface) + tiered generalization plan (Tier 0–4) | Tier 0-1 shipped |
| `docs/NATIVE_SYNTH.md` | User-facing state-of-the-synth: what Phase 1 actually ships and how to play it | current |
| `docs/WEB_UI.md` | Daemon-hosted web settings UI: REST + SSE + change hub + Next.js static export; decisions locked (no auth, :8666, laptop-first) | phases A-B shipped (interim dashboard) |
| `docs/VELOCITY_CURVES.md` | Per-patch velocity remapping: gamma+presets v1, control-point editor v2 | v1 shipped |
| `docs/AUDITION.md` | Keyboard-free clip player: generative diagnostic patterns, loop/tempo transport, per-setting demo buttons | P1-P2 shipped |

**How they stack** (each unlocks the next):

1. **Velocity curves v1 + Audition P1** — pure Go, no UI, no hardware;
   `polyclav --play vel-ramp` makes both audible immediately.
2. **Controls layer + change hub** — the shared refactor of `main.go`'s
   closures that both `docs/WEB_UI.md` and `docs/CONFIGURABILITY.md`
   Tier 3 require. Build once.
3. **Web UI phases A–B** — dashboard + live control; becomes the
   keyboard-free front panel for everything below.
4. **OSC Tier 0–1** (`docs/CONFIGURABILITY.md`) — `[osc.mixer]` naming +
   configurable/optional heartbeat.
5. **Native synth Phases 2–4** (§1–§5 below) — with audition patterns +
   web sliders as the hardware-free test rig for every DSP increment.

### 0.1 Getting the full Moog voice working — consolidated checklist

Everything required to go from today's Phase-1 subset to the §1 voice,
ordered so each step is audible via `--play bass-riff` + web tweaking
(no Launchkey needed until the last item):

- [x] **Runtime resonance** — expose Q alongside cutoff (atomic + FFI +
      web slider). Smallest possible first step; proves the param plumbing.
      (shipped 2026-07-05)
- [x] **Filter ADSR (env 2)** — second envelope with env-amount into
      cutoff; the single biggest "sounds like a Moog" win.
      (shipped 2026-07-05)
- [x] **3-osc + mixer + noise** — per-osc waveform (saw/square/pulse),
      octave, detune; mixer levels; noise source. (shipped 2026-07-05)
- [x] **Glide** — one-pole frequency slew, mono modes only.
      (shipped 2026-07-05)
- [ ] **Velocity → filter/amp routing** — composes with
      `docs/VELOCITY_CURVES.md` (curve shapes input; routing decides
      what velocity modulates).
- [ ] **Poly + voice modes** — LRU steal, `mono_legato | mono_retrig |
      poly` per §3.1 schema.
- [ ] **LFO** — rate/depth, destinations pitch/cutoff/amp.
- [ ] **Patch param persistence** — §3 `state.toml` schema (synth
      sub-table per patch).
- [ ] **2× oversampling around the ladder** — mitigation for the
      Stilson/Smith tanh stage (Appendix A).
- [x] **Launchkey knob pages (§2)** — the hardware UX; last because the
      web UI covers control until the device is back on the bench.
      (code-complete 2026-07-06 — `internal/controls/pages`, §2 adapted
      to the shipped controls layer with deviations documented on the
      page table; hardware verification pending, see
      docs/HARDWARE_TESTS.md "Knob pages".)

---

## 1. Moog-flavored voice architecture (Patch 1: "Classic Minimoog")

Phase 1 ships only a single oscillator into the ladder filter. The full
voice below is the **Phase 2** target.

### 1.1 Signal flow (per voice)

```
                    ┌──────────────────────────────────────────────┐
                    │  GLOBAL (shared across voices)               │
                    │                                              │
                    │   LFO ── tri / saw / sq / S&H                │
                    │     │      sync to BPM (transport buttons)   │
                    │     ▼                                        │
                    │   mod matrix (LFO → pitch / cutoff / amp)    │
                    │     │                                        │
                    └─────│────────────────────────────────────────┘
                          │
              ┌───────────│────────────────────────────────────────┐
              │  PER VOICE (one of N)                              │
              │           │                                        │
              │   ┌───────┼──── note + velocity + glide ────┐      │
              │   │       │       (slew to target Hz)       │      │
              │   │       ▼                                 │      │
              │   │   ┌────────┐  ┌────────┐  ┌────────┐   │      │
              │   │   │ OSC 1  │  │ OSC 2  │  │ OSC 3  │   │      │
              │   │   │ saw|sq │  │ saw|sq │  │ saw|sq │   │      │
              │   │   │ tri|pls│  │ tri|pls│  │ tri|pls│   │      │
              │   │   │ ±1 oct │  │ ±1 oct │  │ ±1 oct │   │      │
              │   │   │ detune │  │ detune │  │ detune │   │      │
              │   │   └───┬────┘  └───┬────┘  └───┬────┘   │      │
              │   │       │           │           │        │      │
              │   │       │     ┌─────────┐       │        │      │
              │   │       │     │  noise  │       │        │      │
              │   │       │     │ (white) │       │        │      │
              │   │       │     └────┬────┘       │        │      │
              │   │       │          │            │        │      │
              │   │   ┌───▼──────────▼────────────▼────┐   │      │
              │   │   │  MIXER (per-source level 0..1) │   │      │
              │   │   └───────────────┬────────────────┘   │      │
              │   │                   │                    │      │
              │   │       ┌───────────▼────────────┐       │      │
              │   │       │  TANH / SOFTCLIP DRIVE │       │      │
              │   │       │   (pre-filter input)   │       │      │
              │   │       └───────────┬────────────┘       │      │
              │   │                   │                    │      │
              │   │       ┌───────────▼────────────┐       │      │
              │   │       │  MOOG LADDER FILTER    │       │      │
              │   │       │  24 dB/oct, lowpass    │       │      │
              │   │       │  ◄── cutoff(env, kbd,  │       │      │
              │   │       │       LFO, vel, knob)  │       │      │
              │   │       │  ◄── resonance         │       │      │
              │   │       └───────────┬────────────┘       │      │
              │   │                   │                    │      │
              │   │       ┌───────────▼────────────┐       │      │
              │   │       │  AMP (VCA)             │       │      │
              │   │       │  ◄── amp env (ADSR)    │       │      │
              │   │       │  ◄── LFO amp mod       │       │      │
              │   │       │  ◄── velocity          │       │      │
              │   │       └───────────┬────────────┘       │      │
              │   │                   │                    │      │
              │   └───────────────────┼────────────────────┘      │
              │                       │                           │
              └───────────────────────┼───────────────────────────┘
                                      │
                          ┌───────────▼────────────┐
                          │  voice sum (interleaved│
                          │  stereo: voices spread │
                          │  L/R as a polish later)│
                          └───────────┬────────────┘
                                      │
                                      ▼
                       (existing DSP chain in lib.rs)
                       patch_gain → input_comp → reverb
                       → mastering_comp → limiter → master
                       → PipeWire out
```

The voice output is **mono per voice** for v1; the audio thread fans it
out to both channels. Stereo spread (one of: detune-pan, position-pan,
chorus) is a Phase 4 polish lever.

### 1.2 What we build vs. reuse

`fundsp` supplies the oscillators, filter, envelopes, LFO primitives, and
saturation. The bespoke glue is small:

| Component | Size | Why ours, not theirs |
|---|---|---|
| `Voice` struct + allocator | ~150 lines | Specific to our envelope / glide / mono-vs-poly semantics. |
| `ModMatrix` | ~80 lines | Domain-specific shape (6 sources, 6 dests). |
| Patch (de)serializer | ~100 lines | Glue between TOML schema and DSP state. |
| Per-knob curve / taper functions | ~30 lines | Audio-specific (exponential cutoff, log volume). |
| Note priority allocator | ~50 lines | Mono-legato, poly-steal logic. |

**Voice / polyphony**: a fixed-size `Vec<Voice>` (e.g. up to 8 for poly,
1 for mono), a **last-note-priority** allocator for mono modes
(Minimoog tradition), and **oldest-quietest stealing** for poly. Glide /
portamento is a per-voice one-pole frequency slew driven by a glide-rate
parameter.

**Modulation matrix**: the Minimoog/Mother-32 lineage has a small, fixed
set of mod destinations (pitch, filter cutoff, amp) and sources (LFO,
env2, noise, mod wheel, velocity, key tracking). A hand-rolled
`ModMatrix` with ~6 sources × ~6 destinations is zero-deps and
RT-allocation-free. If it later grows (a "patch creator" mode), fundsp's
graph DSL is the path of least resistance.

### 1.3 Module / file breakdown (proposed)

All in `audio-core/src/synth/` — a module sibling to `dsp/` and `sfizz.rs`.
Phase 1 shipped a minimal subset (`mod.rs`, a single-oscillator `voice.rs`,
`filter.rs`, `envelope.rs`); the files below are the Phase 2-4 build-out.

```
audio-core/src/synth/
├── mod.rs              Public surface: NativeSynth struct + new() / render() / handle_event().
├── voice.rs            Voice struct: 3 osc phases, noise rng, glide slew, two envelopes,
│                       filter state. Voice::render(&mut self, params, lfo_val, out_mono).
├── allocator.rs        VoiceAllocator: NoteOn → pick a free/stealable voice; NoteOff → release.
│                       Mono-legato vs poly-steal-LRU strategies.
├── oscillator.rs       Thin wrapper around fundsp::poly_saw / poly_square / poly_pulse / sine
│                       to give us a stable API + waveform enum. Per-osc octave/detune state.
├── filter.rs           Wraps fundsp::moog_hz/moog_q with cutoff-modulation summing
│                       (knob + env_amount * env + key_track + lfo + velocity).
├── envelope.rs         ADSR. We can use fundsp::adsr_live for simple cases; envelope.rs may
│                       just be a thin re-export + the curve/taper helpers.
├── lfo.rs              Single shared LFO: tri / saw / square / S&H. Tempo-sync (uses
│                       transport position pushed in from Go).
├── modmatrix.rs        Fixed-shape mod matrix (6 src × 6 dst f32 amounts).
├── patch.rs            Patch struct (all knob values + osc waveforms + routing). Serde/TOML.
└── preset_minimoog.rs  The "factory preset 0" patch values for the classic Minimoog feel.
```

### 1.4 Patch defaults — classic Minimoog flavor

| Parameter | Default | Notes |
|---|---|---|
| OSC 1 wave / oct / detune | Saw / 0 oct / 0 cents | The lead voice. |
| OSC 2 wave / oct / detune | Saw / 0 oct / +7 cents | Slight detune for chorus thickness. |
| OSC 3 wave / oct / detune | Triangle / -1 oct | Sub. |
| OSC 1/2/3 mix levels | 1.0 / 0.7 / 0.5 | OSC1 dominant. |
| Noise level | 0.0 | Off by default; tasteful when added. |
| Filter cutoff | 60% | Already in the "bright but not piercing" range. |
| Filter resonance | 0.3 | Just shy of self-oscillation onset. |
| Filter env amount | +30% | Classic Moog "wow" amount. |
| Filter keytrack | 0.5 | Half-keyboard tracking (Minimoog-typical). |
| Amp ADSR | 5 ms / 200 ms / 0.7 / 400 ms | Fast attack, decent sustain, medium release. |
| Filter ADSR | 5 ms / 600 ms / 0.4 / 600 ms | Slower decay than amp — the "filter sweep" sound. |
| LFO rate | 4 Hz | Slow vibrato range. |
| LFO wave | Triangle | |
| LFO destinations | All 0 | User adds intent via knobs. |
| Glide | 0 ms (off) | |
| Voice mode | Mono-legato | Minimoog is monophonic — start there. |
| Drive (pre-filter sat) | 0.3 | A little Moog grit, not aggressive. |

### 1.5 Polyphony

The full voice supports:
- **Mono-legato** (Minimoog mode): 1 voice, last-note priority, retrigger
  envelopes only when no other note held.
- **Poly** (Matriarch/Trigon flavor): 4-voice initially, knob/page-bumpable
  to 8 in later phases. Stealing strategy: oldest *released* voice first,
  fall back to oldest *playing*.

CPU budget: 8 voices × (3 polyBLEP osc + noise + ladder filter + 2 ADSR +
saturator) at 48 kHz should land ~3–8% of one core on a modern CPU. The
existing rustysynth + DSP chain runs <1%. Plenty of headroom.

### 1.6 Sound-design realism — what we're *not* faking

The audio chain is mature (real mastering compressor + limiter) — the
synth has to match that bar:

- **Anti-aliased oscillators** at all key ranges: `fundsp::poly_saw` etc.
  give us PolyBLEP at no measurable runtime cost. No naive
  `(phase * 2.0 - 1.0)` saw — aliasing above C6 is brutal.
- **Filter nonlinearity**: `fundsp::moog_hz` is the classic Stilson/Smith
  musicdsp ladder with a tanh saturator on the fourth stage only (verified
  against `fundsp-0.23.0/src/moog.rs`; earlier drafts of this doc called
  it Huovilainen, which has per-stage tanh + thermal-voltage scaling — it
  isn't). Resonance still behaves musically, but expect self-oscillation
  onset/character at high Q to deviate from a real Model D; the 2×
  oversampling below and the Appendix A pivot ladder are the mitigations.
- **Selective 2× oversampling** around the filter+saturation block —
  inside `fundsp::oversample(...)`.
- **Slew on every modulation source** so knob twists don't zipper: each
  parameter goes through a short one-pole LP (`fundsp::afollow(...)`)
  before reaching the audio-rate path.

---

## 2. Launchkey-native UX

The differentiator: the Launchkey **is** the synth's front panel, not a
generic MIDI controller. Every knob has a name, every pad has a state,
the screen always knows what to show. **No knob-page Go code exists
today** — this entire section is forward-looking.

The MK4 hardware available to us (validated from the existing driver code
in `internal/launchkey/driver/`):

- **8 encoders** (relative mode, ch16 CC 0x55–0x5C) with `KnobEvent{Delta}`.
- **9 faders + 8 fader buttons** on 49/61 SKU. Currently assigned to XR18.
- **16 RGB pads** (top row notes 96–103, bottom row 112–119, ch1).
- **Transport row**: Play, Stop, Record, Loop, Rewind, FF, Track L/R,
  Scene Up/Down, Shift.
- **128×64 LCD screen** with multiple display targets:
  - `0x20` Stationary (the persistent overlay — currently our patch name).
  - `0x21` Global temp (auto-popups, we currently suppress).
  - `0x22` DAW pad mode name (e.g. "PIANO" or "SYNTH" label).
  - `0x25` Plugin encoder mode name (e.g. "OSC MIX" — page label).
  - `0x15..0x1C` Per-encoder temporary display (knob popup, native to the
    device — auto-temp bits are currently suppressed in `driver.go` to
    avoid the device's own "value popup" overriding our patch display).
- **Arrangement 1** display: 2-line text. Currently we use only this.
- **Arrangement 3** is available: "1 line title + 2×4 grid of encoder
  names" — a candidate for a page label + 8 knob labels.

### 2.1 Knob pages — the central concept

8 encoders, ~30 synth parameters → we need pages. Five pages cover the
classic Minimoog control set comfortably:

| # | Page | Knob 1 | Knob 2 | Knob 3 | Knob 4 | Knob 5 | Knob 6 | Knob 7 | Knob 8 |
|---|---|---|---|---|---|---|---|---|---|
| 1 | **MIX** | OSC1 lvl | OSC2 lvl | OSC3 lvl | Noise lvl | OSC1 detune | OSC2 detune | OSC3 detune | Drive |
| 2 | **FILTER** | Cutoff | Resonance | Env amt | Keytrack | Attack(F) | Decay(F) | Sustain(F) | Release(F) |
| 3 | **AMP** | Volume | — | — | Velocity sens | Attack(A) | Decay(A) | Sustain(A) | Release(A) |
| 4 | **LFO** | Rate | Depth | → Pitch | → Cutoff | → Amp | Sync (off/¼/⅛/¹⁄₁₆) | Shape (tri/saw/sq/SH) | Smoothing |
| 5 | **MOD** | Mod wheel → Cutoff | Mod wheel → LFO | Mod wheel → Pitch | Vel → Cutoff | Vel → Amp | Glide rate | Glide on/off | Pitch bend ± semis |

Knob 1 of Page 3 (AMP) is "Volume" — keeping muscle-memory continuity
with the current Page 1 default. Knob 2/3 on Page 3 are blank
deliberately (Volume / Reverb / Compressor currently live there in the
soundfont-patch world; on the analog backend, Reverb and Compressor
still apply post-synth from the existing DSP chain — we could surface
them on a Page 6 "MIX" or just leave them on the existing global knob 2/3
when *no* synth page is active. **Open question.**)

Knob ranges and tapers (`taper` = the user-felt curve):

| Param | Range | Taper |
|---|---|---|
| OSC level | 0.0 – 1.0 | linear |
| Detune | -50 to +50 cents (knob 5/6/7 page 1); -2 to +2 octaves switchable via pads (page 1 bottom row) | linear |
| Drive | 0.0 – 1.0 → 0 dB to +24 dB pre-filter | exponential |
| Cutoff | 20 Hz – 20 kHz | exponential (log freq) |
| Resonance | 0.0 – 1.0 (1.0 = self-oscillation onset) | exponential near top |
| Env amount | -100% to +100% | linear, bipolar |
| Keytrack | 0.0 – 1.0 (1.0 = 100%, equal cents per semitone) | linear |
| ADSR A/D/R | 1 ms – 10 s | exponential (log time) |
| ADSR S | 0.0 – 1.0 | linear |
| Volume | 0.0 – 1.0 | exponential (audio taper, dB) |
| Velocity sens | 0.0 – 1.0 | linear |
| LFO rate | 0.05 Hz – 30 Hz | exponential |
| LFO depth | 0.0 – 1.0 | linear |
| LFO → X | -1.0 to +1.0 (bipolar amount) | linear |
| Glide rate | 0 – 2 s | linear |
| Pitch bend range | ±1 to ±12 semitones | integer step |

Page-state and parameter values **persist per patch** (see §3).

### 2.2 Page switching

The MK4 has dedicated `Scene Up / Scene Down` and `Track Left / Track
Right` transport buttons. None are currently bound to anything in
polyclav (the transport row is reserved). Proposal:

- **Track Left / Track Right**: previous / next knob page. (Five pages →
  cycles MIX → FILTER → AMP → LFO → MOD → MIX → ...)
- **Scene Up / Scene Down**: octave shift for the keyboard (a Minimoog
  comfort — currently no octave control besides the keyboard's own +/–).
- **Stop**: panic / all-notes-off (matches Minimoog tradition).
- **Play**: LFO tap-tempo (each press registers a tap; after 2 taps the
  LFO rate locks to the inter-tap interval, displayed on screen).
- **Record**: arms "save patch" — next-knob-twist captures into the
  current patch; another Record press disarms. Saves to state.toml.

The "shift to second page" pattern via a held button is *also* an option
(MK4 has `Shift` as note 0x6A on ch16) — we already see it as
`TransportShift`. Could be combined: Shift + Knob 1 sets cutoff with
fine-grained taper, or Shift + Track L jumps to MIX page directly. **Open
question; lean toward not over-loading at v1.**

### 2.3 Screen — what shows when

The current single-line "patch name on top, knob value on bottom for 800
ms" pattern is preserved as the **base layer**. For the analog backend,
we extend:

- **Idle (no knob touched, no pad held)**: line 1 = patch display name
  (e.g. "MINIMOOG"); line 2 = current page name + "▾" (e.g. "FILTER ▾").
  This tells the user where they are; pre-empts the "which knob does
  what?" question.
- **Knob touched/turned**: line 1 = parameter name + glyph (e.g.
  "Cutoff ◀█████░░░"); line 2 = numeric value (e.g. "8.4 kHz" or "47%").
  Reverts to idle after 800 ms (existing pattern).
- **Page changed (Track L/R press)**: line 1 = new page name in caps
  ("FILTER"); line 2 = brief hint ("8 knobs, K1=cutoff"). Reverts to
  idle after ~1.5 s.
- **Patch loaded (top-row pad press)**: line 1 = patch display name;
  line 2 = "synth" / "soundfont" tag for half a second, then idle.
- **Panic / tap-tempo / save events**: distinctive 1-line + status
  flash (e.g. "PANIC", "TAP… (1)", "SAVED").

**Display arrangement choice**: Stick with arrangement 1 (2-line
name+value) for v1 — it's what the rest of polyclav uses and the driver
already prefills it. Arrangement 3 (1-line title + 2×4 grid of encoder
names) is *tempting* for a "Page label up top, 8 knob names below" layout
but: it requires writing 9 fields per refresh; the 14-char-wide screen
can only fit 4-character knob labels in a 2×4 grid (cramped); and the
text-field paint bit pattern is the same — there's no efficiency win.
**Lean: stay on arrangement 1 for v1.** Revisit arrangement 3 once we've
shipped and felt the screen real estate firsthand.

### 2.4 Pad colors as state indicators

The Launchkey has 16 RGB pads. Currently the top row is patch select
(palette color per patch); the bottom row is unbound for analog
synth-mode use. Two layouts proposed:

#### Layout A — "patch selectors always live; bottom row is synth state"

Top row 0..7: patch selectors (current behavior preserved).

Bottom row: synth-state grid; meaning depends on the current page.

| Page | Bottom-row layout (8 pads) |
|---|---|
| MIX | OSC1 wave, OSC2 wave, OSC3 wave, Noise (toggle white/pink/off), OSC1 oct, OSC2 oct, OSC3 oct, Drive on/off |
| FILTER | LP/HP toggle (future), Reso lock-to-self-osc, Env amt sign (+/−), Keytrack on/off, Vel→cutoff on/off, (3 unbound) |
| AMP | Voice mode (mono/poly/legato — 3 of 8), Velocity sens preset (lo/med/hi — 3 of 8), (2 unbound) |
| LFO | LFO target on/off: pitch / cutoff / amp (3 pads); sync rate steps (free/¼/⅛/¹⁄₁₆ — 4 pads); shape (1 pad) |
| MOD | Mod wheel routing (3 pads); Vel routing (2 pads); Glide on/off (1 pad); Pitch bend range steps (2 pads) |

Each pad's color reflects its current state:

- **Toggle off** → dim white.
- **Toggle on** → bright Components-palette color (per-control thematic:
  filter = orange, LFO = blue, mod = purple, etc.).
- **Multi-state selector** (waveform, octave, voice mode): one pad lit
  in the group, others dim — like a radio button.
- **Sub-state via pulse/flash**: e.g. LFO pad pulses at the LFO rate
  (using the device's ch3 pulse channel — sync to MIDI beat clock if
  we send one).
- **Octave display** (e.g. for OSC1 in MIX page): the pad shows a color
  ramp — red (-2 oct) → yellow (-1) → green (0) → cyan (+1) → blue (+2).
  Color is a discrete index in the 128-entry palette; pre-pick five
  indices that read clearly.

#### Layout B — "envelope LED ring"

Aspirational, Phase 4+. The 4 pads on the right of the bottom row become
a visual ADSR display: pad 4 lit during attack, pad 5 during decay, pad 6
during sustain (constant lit while held), pad 7 during release. Pulses
during the corresponding envelope stage. Pure eye candy — but exactly
the kind of "the controller knows the synth's internal state" detail that
makes polyclav feel like a real instrument and not a generic MIDI box.

**Lean for v1: Layout A on the MIX page** (already meaningful); the
other pages can ship pad-blank initially and gain pad meanings phase by
phase.

### 2.5 Transport buttons — assignment table (concrete)

| Button | Action | State display |
|---|---|---|
| **Play** | LFO tap-tempo (debounced; need ≥2 taps to lock) | screen flash "TAP" + Hz |
| **Stop** | Panic — all notes off, kill voices, reset envelopes | screen flash "PANIC" |
| **Record** | Toggle "save mode" — next knob twist commits the new value to disk immediately rather than after the usual 2 s debounce | bottom row dims, record-button pulses red until released |
| **Loop** | (reserved) | — |
| **Rewind / FF** | Reserved for XR18 control (keep existing) | — |
| **Track ←** | Previous page (MOD → LFO → AMP → FILTER → MIX → MOD ...) | top row briefly flashes; page name shows |
| **Track →** | Next page (MIX → FILTER → ... → MOD → MIX ...) | same as above |
| **Scene ↑** | Octave +1 (keyboard) | screen flash "OCT +1" |
| **Scene ↓** | Octave -1 | screen flash "OCT -1" |
| **Shift** | (modifier for fine-knob, see §2.2) | held → screen line 2 shows "FINE" |

### 2.6 Verification against the driver code

Cross-checked with `internal/launchkey/driver/driver.go`:

- ✅ All transport notes (0x66–0x76 on ch16) are already decoded — no
  parser changes needed.
- ✅ `KnobEvent{Index, Delta}` — already in relative mode (ch16 CC
  0x55–0x5C), already delta-decoded.
- ✅ `PadEvent{Row, Col, Pressed, Velocity}` — both rows already decoded.
- ✅ Screen targets 0x20 (Stationary), 0x22 (DAW pad mode) — already
  used; 0x25 (Plugin encoder mode) — available, not yet used, suitable
  for the page label.
- ✅ Pad LED set via `SetPadColor(row, col, components.Color)` (palette
  by velocity) and `SetPadRGB(row, col, r, g, b)` (24-bit SysEx).
- ⚠️ The pre-startup loop in `Open()` suppresses auto-temp on
  targets 0x15–0x1C and 0x21 to prevent the device from overlaying its
  own knob-value popup on our patch screen. **This must stay** — we
  drive the screen entirely from Go for analog-synth knobs too.

No changes to `driver.go` are required for the UX described above. The
new page-switching, screen-rendering, and pad-coloring logic lives in
new Go code (e.g. `internal/synth/launchkey_ux.go` or extending
`cmd/polyclav/main.go`'s `onDAWEvent` handler).

---

## 3. Patch persistence schema

The existing `state.toml` schema is too narrow for a multi-parameter
synth. `cmd/polyclav/main.go` notes that synth-parameter persistence is
**Phase 2 work** — the schema and store API below do not exist yet.

```toml
# current
current_patch = "ydp-grand"
[patches.ydp-grand]
volume = 1.0
reverb = 0.0
compressor = 0.0
```

Each patch stores 3 floats. The analog backend has ~35 parameters. We
extend the schema with a nested table per patch:

### 3.1 Proposed state.toml extension

```toml
current_patch = "minimoog"

# Existing soundfont-patch entries continue to work — unchanged.
[patches.ydp-grand]
volume = 1.0
reverb = 0.2
compressor = 0.1

# New: analog-patch entries get a synth sub-table.
[patches.minimoog]
volume = 0.8
reverb = 0.15
compressor = 0.0
# ^ same three knobs as before, applied post-synth via the existing DSP chain.

[patches.minimoog.synth]
page = "FILTER"      # last-active knob page (so it restores on patch reload)
octave_shift = 0

[patches.minimoog.synth.osc1]
wave = "saw"         # saw | square | triangle | pulse | sine
level = 1.0
octave = 0           # -2..+2
detune_cents = 0.0

[patches.minimoog.synth.osc2]
wave = "saw"
level = 0.7
octave = 0
detune_cents = 7.0

[patches.minimoog.synth.osc3]
wave = "triangle"
level = 0.5
octave = -1
detune_cents = 0.0

[patches.minimoog.synth.noise]
level = 0.0
color = "white"      # white | pink — future

[patches.minimoog.synth.filter]
cutoff_hz = 2000.0   # stored as Hz; UI displays log-tapered
resonance = 0.3
env_amount = 0.3     # bipolar -1..+1
keytrack = 0.5
drive = 0.3
velocity_to_cutoff = 0.0

[patches.minimoog.synth.amp_env]
attack_ms = 5.0
decay_ms = 200.0
sustain = 0.7
release_ms = 400.0

[patches.minimoog.synth.filter_env]
attack_ms = 5.0
decay_ms = 600.0
sustain = 0.4
release_ms = 600.0

[patches.minimoog.synth.lfo]
rate_hz = 4.0
depth = 0.0
wave = "triangle"
sync = "off"         # off | 1/4 | 1/8 | 1/16
target_pitch = 0.0   # bipolar amount
target_cutoff = 0.0
target_amp = 0.0
smoothing = 0.2

[patches.minimoog.synth.mod]
glide_rate_ms = 0.0
glide_enabled = false
mod_wheel_to_cutoff = 0.0
mod_wheel_to_lfo = 0.0
mod_wheel_to_pitch = 0.0
velocity_to_amp = 0.0
pitch_bend_semitones = 2
voice_mode = "mono_legato"  # mono_legato | mono_retrig | poly
```

### 3.2 Compatibility with existing state mechanism

- The existing `Knob{Volume, Reverb, Compressor}` block stays at the
  patch root for **all** patches, soundfont or analog. The DSP chain
  applies these post-synth regardless — no behavior change for
  soundfont patches.
- The `[patches.<name>.synth]` sub-table is **optional**. Soundfont
  patches simply don't have one. The TOML decoder ignores unknown fields
  by default; we don't need a migration step.
- Per-knob debounced flush still works — the `Store.dirty` flag is set
  the same way; the only difference is that *more* fields end up in the
  serialized snapshot for analog patches.
- For `Store.UpdatePatchKnob(patchName, field, value)` to remain
  ergonomic with the deep synth tree, we add a sibling method
  `UpdateSynthParam(patchName, path, value)` where `path` is e.g.
  `"filter.cutoff_hz"` or `"osc2.detune_cents"`. The Go side maps a
  knob page+slot to a path; the store handles the recursive set.
- **Atomic writes** (the existing `tmp + rename` pattern) work without
  change.

### 3.3 Factory presets vs. user state

There's a tension here: `polyclav.example.toml` declares patches; user
state lives in `state.toml`. For analog patches the factory defaults
*are* the synth setting — we need a way to ship "the Minimoog patch"
even on first boot. Two options:

| Option | Description | Verdict |
|---|---|---|
| A | Bake factory defaults into polyclav source. `[[patches]]` in `polyclav.toml` just declares `type = "native"`, `engine = "minimoog"` (or `"mother32"`, etc.), and `display = "Minimoog"`; the synth itself seeds the voice from a hardcoded patch struct keyed by `engine`. User edits go to `state.toml`. | **Lean A.** Simpler. Adding new factory patches = adding new Rust constants. |
| B | Ship factory defaults as `polyclav.example.toml` `[[patches]]` entries with full inline `[patches.X.synth]` tables. User-state still overrides via `state.toml`. | More flexible (user can swap factory defaults) but bloats `polyclav.example.toml` to thousands of lines for 5+ patches. Reject for v1. |

`type = "native"` joins the existing `"soundfont"`, `"lv2"`, `"clap"` —
covers the existing `PatchConfig.Type` enum at minimal cost. The
`engine` sub-field (`"minimoog"` for v1; later `"fm"`, `"plaits"`, ...)
selects which in-house voice architecture to instantiate.

### 3.4 Patch save UX (the Record button)

Per §2.5, pressing Record arms "immediate save mode". This is mostly a
UX hint — under the hood, every knob change *already* triggers a
debounced save (2 s today). The Record button just (a) bypasses the
debounce on the next knob change for instant gratification, and (b)
provides a visible cue that changes ARE being persisted (a common
question after the first 10 minutes with an unfamiliar synth).

A future "patch slot" mechanism (save current edits as a new named
patch) is out of scope for v1.

---

## 4. Phased implementation plan

Phase 1 (single oscillator + ladder filter + amp envelope, proof of life)
is **shipped**. The remaining phases each are independently shippable +
demoable.

### Phase 2 — Full Minimoog voice + page-1 UX

**Goal**: 3 osc + noise + mixer + ladder filter + 2 ADSR + drive. Mono.
Knob page 1 (MIX) fully wired. Saved/restored from `state.toml`.

**Deliverables**:
- `voice.rs` extended to 3 oscillators + noise + per-osc octave/detune/level.
- `modmatrix.rs` (skeleton — empty matrix for now; just amp + filter env routing).
- `patch.rs` + `preset_minimoog.rs` — full Minimoog patch struct, serializable.
- `state.toml` extension: `[patches.X.synth]` sub-tables. New `UpdateSynthParam` method on `Store`.
- Knob page state in Go: page index + page→param mapping table.
- Track L/R buttons switch pages (only one page populated this phase).
- Screen idle line 2 shows current page name.
- Bottom row pads on MIX page: 4 waveform-toggle pads for OSC1 wave + level toggles.
- **Tests**: round-trip the synth patch through state.toml. Hardware test: 8 knobs on page 1 modify the right things audibly. Pad row toggles waveforms with correct LED color feedback.

### Phase 3 — Filter / Amp / LFO pages, polyphony, modulation

**Goal**: All 5 knob pages live. 4-voice poly mode (toggleable per
patch). Working LFO with 3 destinations. Glide.

**Deliverables**:
- Pages 2 (FILTER), 3 (AMP), 4 (LFO) — knob + state + pad layouts.
- `allocator.rs` — last-note-priority mono + LRU-steal poly.
- `lfo.rs` — global LFO with tri/saw/sq/SH, fed via mod matrix to pitch, cutoff, amp.
- Tempo-sync LFO using Play-as-tap-tempo (§2.5).
- Glide / portamento on per-voice frequency slew.
- 3 additional factory patches: "Mother-32 lead", "Matriarch pad", "Taurus bass-native".
- **Tests**: voice-stealing under > 4 simultaneous keys. LFO sync stability. Hardware test: all 5 pages reachable, all knobs do something audible.

### Phase 4 — Polish + page 5 (MOD)

**Goal**: production-grade. Page 5 (MOD) live. Pad-color animations
(LFO pulse, envelope ring). Stereo voice spread. Oversampling around
filter. Full UX feedback (record-save button, panic, octave shift).

**Deliverables**:
- Page 5 (MOD) full.
- Bottom-row pad ADSR ring (Layout B from §2.4).
- Pad pulse synced to LFO rate (Components ch3 pulse).
- Stereo voice spread (alternating L/R pan per voice, or detune-pan).
- 2× oversampling wrapper around filter+saturation.
- Record-button save UX.
- Transport row fully bound (octave, panic, tap-tempo).
- Documentation: a short `docs/USER_GUIDE.md` section on the analog synth pages.
- **Tests**: 8-voice poly stress; CPU profile (<10% one core target).
  Sound-design pass: A/B against hosted reference synths for a half-dozen
  classic Minimoog patches; pick out audible weaknesses, fix or document.

---

## 5. Open questions / decisions deferred

### Locked decisions (2026-05-25)

- **Reverb & compressor placement: both.** The global volume / reverb /
  compressor / master knobs remain always-applied (current behavior
  preserved for every patch). In addition, where it makes sense, a synth
  knob page exposes a per-page send that's **aliased to the same global
  value** — so e.g. Knob 8 on the AMP page reads/writes the global reverb
  amount. No duplicate state, no conflict; the synth page just gives you
  a quick reach. A dedicated MIX page exposing the four globals
  (volume/reverb/comp/master) is added as a sixth page so they're never
  out of reach from inside the synth UI.

- **Polyphony: ship both, runtime-toggled.** The first patch ("Minimoog")
  boots in `mono_legato` mode (honest to the source). The MOD page
  (page 5) carries a `voice_mode` selector cycling `mono_legato →
  mono_retrig → poly` on a dedicated pad. Schema already supports this
  via `[patches.X.synth.mod].voice_mode` (see §3.1). The voice allocator
  supports both topologies — a single voice for mono modes, a small voice
  pool (4 voices) for poly. Voice stealing is "oldest" for v1.

- **Patch type: `type = "native"` + `engine = "<name>"` sub-field.**
  Joins existing `soundfont` / `lv2` / `clap`. The `engine` field
  selects which native voice architecture to spin up: `"minimoog"` for
  v1; future values like `"fm"`, `"plaits"`, `"wavetable"` share the same
  `native` type. Keeps the top-level `PatchConfig.Type` enum small.

### Still deferred

1. **Knob page persistence: per-patch or global?** When the user switches
   from "Minimoog" to "Salamander Piano" and back, should the analog
   patch remember it was on the FILTER page? Per-patch is more
   user-friendly; global is simpler. **Lean: per-patch — already covered
   by the `[patches.X.synth].page` field in §3.1.**

2. **Should we vendor the fundsp Moog ladder or stay on the crate?**
   `fundsp` is large and active; pinning to a major version is enough
   risk management. **Lean: just depend on `fundsp = "0.x"` like any
   other crate. Vendor only if upstream breaks ABI on us.**

3. **Tempo source for LFO sync.** No MIDI clock currently flows into
   polyclav from the Launchkey — the device sends real-time start/stop
   but no per-beat clock unless we configure one. Tap-tempo via Play
   button works without external clock; full MIDI clock sync needs
   investigation. **Defer to Phase 4; tap-tempo is sufficient for
   Phase 3.**

4. **Where do non-analog FX (chorus, delay) live?** Out of scope for the
   analog synth design — they belong to a separate FX-stage discussion
   (the existing reverb + compressors are already in place). Worth noting
   that `fundsp` does ship a chorus, but adding it changes the chain
   topology. **Defer.**

5. **`mi-plaits-dsp-rs` integration for non-analog patches** (FM, chord,
   speech, granular). Not in the analog-synth design but a natural
   next-next step: once we have the `NativeSynth` backend, a parallel
   `PlaitsSynth` backend that selects between Plaits engines could ship
   exotic timbres without the LV2/CLAP host overhead. **Defer to a future
   doc; mention only.**

6. **Pad ergonomics for octave/waveform display.** The proposal in §2.4
   uses palette indices for octave color-ramps; the actual palette
   indices need to be picked from the 128-entry Components palette to
   ensure they read clearly on the device. Defer to Phase 2
   implementation — prototype the colors on real hardware and tune.

---

## Appendix A — Rust DSP crate survey (2026-07-04)

> **Decision: stay on fundsp for Phases 2–4.** Nothing in the mid-2026
> landscape justifies replacing it; the pivot triggers at the end of
> this appendix are the conditions that would reopen the question.
> Versions/dates verified against crates.io and GitHub on 2026-07-04.
> License key: GPL/AGPL = hard blocker (Apache-2.0 statically-linked
> binary); MIT / Apache-2.0 / BSD = compatible.

### A.1 fundsp — the backbone (keep)

- **0.23.0** (crates.io 2026-01-07) is the latest release — polyclav is
  current. MIT OR Apache-2.0. Very active (commits through 2026-03).
- **Pinning:** every 0.x bump is semver-breaking and upstream ships **no
  changelog and no GitHub releases** — upgrades mean reading commits.
  Master already has sequencer looping, an RT-queue swap
  (`thingbuf`→`lfqueue`), and node-tree introspection brewing toward an
  0.24; treat that as a planned migration, not a routine bump.
- **Ladder reality check (verified in `fundsp-0.23.0/src/moog.rs`):**
  `moog`/`moog_hz`/`moog_q` is the **Stilson/Smith musicdsp variant**
  (`p = c(1.8−0.8c)`, `k = 2sin(πc/2)−1`, the `1.386249` resonance
  polynomial) with a **single tanh on stage 4** — *not* Huovilainen
  (per-stage tanh, thermal-voltage scaling, assumes ~2× oversampling).
  Consequences: drive behavior and self-oscillation character at high Q
  won't match a real Model D. Mitigations: wrap in `oversample()` when
  driven hard (§1.6 already plans this); pivot ladder below if listening
  tests fail. (Background: D'Angelo & Välimäki, "An improved virtual
  analog model of the Moog ladder filter.")
- **PolyBLEP tier:** fundsp's own README rates its PolyBLEP oscillators
  "fast approximation, fair quality" vs. wavetable = "pristine." Fine at
  48 kHz for v1; **the in-crate upgrade path is fundsp's wavetable
  oscillators**, before any external crate.
- All Phase 2–4 primitives confirmed present in 0.23: `adsr_live`,
  `afollow`/`follow`, `oversample`, `poly_saw`/`poly_square`/
  `poly_pulse`. RT-safe: graph build allocates, `tick`/`process` don't.

### A.2 mi-plaits-dsp-rs — FM/wavetable phase (keep penciled in)

MIT, very active (commits 2026-06), full port of Plaits firmware 1.2
(all 24 engines), `no_std`, caller-provided buffers, explicitly designed
for 48 kHz (our rate). **Caveat: never published to crates.io** — git
dependency only; pin a commit SHA, and note crates.io forbids git deps
if polyclav ever publishes library crates (then: extract needed engines
under MIT, or evaluate `dx7` below).

### A.3 Rejected / watch list

| Crate | Status (2026-07) | License | Verdict |
|---|---|---|---|
| `dasp` 0.11 | release-frozen since 2020; plumbing (samples/frames), no synthesis | MIT/Apache ✅ | ❌ nothing over fundsp |
| `synfx-dsp` 0.5.6 / `hexodsp` | **GPL-3.0**; author deleted the repos, publishing stopped | 🚫 | 🚫 license + dead upstream (shipped exactly our shopping list — was never usable) |
| `va-filter` (Fredemus) | plugin not crate; inactive since 2023; **the** DK-method ladder/SVF reference (Newton-solved nonlinearities) | **GPL-3.0** 🚫 | 🚫 as dependency; ✅ as *algorithm reference* — reimplementing from the cited papers is clean, copying code is not |
| `surge-rs` (~100 micro-crates incl. `surgefilter-huovilainen`) | perpetual 0.2-alpha since 2023 | **GPL-3.0** 🚫 | 🚫 license + alpha quality |
| `valib` (SolarLiner, git-only) | dormant since 2024-12; SVF, **ladder**, saturators, oversampling; `src/` is **MIT** (only example plugins GPL) | core ✅ | 👀 **best MIT porting reference** if fundsp's ladder proves insufficient. (NB: the crates.io `valib` is an unrelated name-collision crate.) |
| `oscen` (rewrite) | unpublished rewrite, commits 2026-07: graph compiler, TPT filter, PolyBLEP, ADSR | MIT/Apache ✅ | 👀 only credible permissive challenger; re-evaluate if it ships ≥0.2 with RT guarantees |
| `simper-filter` 0.1.1, `dirtydata-dsp-svf` 0.1.0 | small permissive ZDF/TPT SVFs (2025/2026) | ✅ | 👀 relevant only if a non-ladder filter mode appears |
| `dx7` (spacejam) 0.0.4 | Plaits DX7 engine port driven by real SYSEX patches; early but credible author | MIT ✅ | 👀 alternative for the FM slot |
| `twang`, `glicol_synth`, `sirena`, misc 2026 shovelware (`naad`, `audio_core_dsp`, …) | dormant / wrong scope / unvetted | mixed | ❌ |

Notable gap: **no permissively-licensed, solved DK-method/ZDF ladder
exists in Rust** as of mid-2026. The options remain fundsp's
Stilson-variant, valib's dormant MIT ladder, or DIY from the literature.

### A.4 Pivot triggers (re-run this survey if any fires)

1. **fundsp goes quiet ≥ 9–12 months** (healthy as of 2026-03). Fallback:
   vendor the ~6 fundsp modules we use (MIT permits), or thin in-house
   DSP layer.
2. **Ladder fails listening tests** — self-oscillation onset/stability at
   max Q, filter-FM growl, aliasing under drive. Escalation ladder:
   (a) 2× `oversample()`; (b) port valib's MIT ladder; (c) implement
   Huovilainen/DK-method from the papers (va-filter as conceptual
   reference only — no GPL code).
3. **PolyBLEP saw too dull/aliased vs. Model D references** — switch to
   fundsp wavetable oscillators (in-crate) first.
4. **fundsp 0.24 breaks the graph API badly** — migrate vs. vendor 0.23
   decision point.
5. **oscen publishes a stable compiled-graph release with RT
   guarantees** — re-evaluate as the voice-graph engine.
6. **mi-plaits-dsp still unpublished when we want to ship library
   crates** — extract engines or adopt `dx7` for FM.

### A.5 Sources

crates.io + GitHub for each crate named above; fundsp source readings:
`src/moog.rs`, `src/prelude.rs` @ 0.23.0/master;
github.com/sourcebox/mi-plaits-dsp-rs; github.com/SolarLiner/valib;
github.com/Fredemus/va-filter; github.com/klebs6/surge-rs;
github.com/reedrosenbluth/oscen; D'Angelo & Välimäki (2013), "An
improved virtual analog model of the Moog ladder filter."
