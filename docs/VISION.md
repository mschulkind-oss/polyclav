# Vision: A Native, Open Sound Engine

> **Status (2026-07-12):** draft. Threads 1–3 below are backed by an
> in-flight research pass (circuit emulation, tonewheel/organ physical
> modeling, and Surge XT/Helm hosting feasibility); their sections are
> placeholders pending that report, which will land as
> `docs/OPEN_SOUND_ENGINES.md` and get cross-linked here. Thread 4
> (control-surface generalization) is already fully scoped in
> `docs/CONFIGURABILITY.md` and isn't re-planned here.

## The north star

polyclav should become a self-contained, native, low-latency instrument
rig where every sound source — synth voice, drive/amp stage, organ — is
either genuinely **modeled** (circuit-emulated or physically derived) or
**hosted** as a first-class open-source engine, playable live from
whatever consumer MIDI keyboard is plugged in, with control mapped as
deeply as that keyboard supports — not just generic CC passthrough.

Unpacked:

- **Live playable** — the existing PipeWire chain already gets ~8 ms
  round-trip at a 128-frame quantum (README). Nothing added in service
  of this vision should regress that.
- **Native as possible** — pure-Rust, built into `audio-core`, no
  external process, matching the existing native synth's ethos. Hosting
  a plugin (thread 3, below) is the deliberate exception, not the
  default.
- **Low latency** — everything that runs in the real-time audio thread
  keeps the existing discipline: no heap allocation, no locks, no
  blocking I/O in the hot path.
- **Open sounds** — "open" in two senses at once: license-clean (no
  sample libraries to clear, no multi-GB downloads — see README's "ships
  no soundfonts (license + size)"), and *sonically* open-ended — a
  modeled drive pedal responds continuously to a knob turned mid-note in
  a way a sample layer never can.
- **Integrated to consumer MIDI keyboards** — reachable from more than
  the one Launchkey model polyclav is validated against today.

## Why move past samples

The soundfont/SFZ path (oxisynth, sfizz) is the right tool for "make a
convincing piano now," and stays. But it caps what polyclav can become:
samples are licensed, large, and fixed-timbre — there's no way to "turn
the drive up" on a sample the way you can on a modeled circuit, and no
path to the kind of instrument the samples don't already cover (nobody
ships a free, license-clean, tweakable Hammond B3 soundfont with a
working Leslie). Modeled and hosted sound sources sidestep all three
constraints: small, tweakable in real time, and — when built
natively — fully license-clean under polyclav's Apache-2.0.

## Three build threads, plus one enabling thread

### 1. Circuit-emulated effects — drive pedals & amps

**Target:** a new `audio-core/src/dsp/` module, inserted into the
existing chain (today: `synth → patch_gain → input_comp → reverb →
mastering_comp → limiter → master_volume`) as a pre- or post-synth drive
stage. Virtual-analog nonlinear circuit modeling (diode-clipper /
triode-stage models, Wave Digital Filters, nodal DK method — whichever
the research pass rates as the best complexity/CPU tradeoff for a
real-time voice-per-note or bus-level insert) applied to well-documented
reference circuits (overdrive, fuzz, tube preamp saturation).

**[PENDING RESEARCH]** — concrete technique choice, reference circuit(s)
to start with, and CPU-cost verdict land in `docs/OPEN_SOUND_ENGINES.md`.

### 2. Physically-modeled organ engine — "build our own Hammond"

**Target:** a new native synth backend. `docs/ROADMAP.md` §5 already
locked the extensibility seam for exactly this — `type = "native"` +
an `engine` sub-field, with `"minimoog"` as the first value and
`"fm"` / `"plaits"` / `"wavetable"` anticipated as future ones. A
tonewheel/organ engine (`engine = "tonewheel"`) is a natural next
entry in that same dispatch, not a new patch type. Shape: additive
tonewheel generation with the imperfections that make it read as
*electromechanical* rather than digital (crosstalk/leakage, key click),
drawbars, the scanner vibrato/chorus, and a Leslie rotary-speaker model
as a bus effect shared across voices rather than per-voice.

**[PENDING RESEARCH]** — architecture verdict (own engine module vs.
adapting `voice.rs`) and prior-art notes (setBfree, studied as reference
architecture) land in `docs/OPEN_SOUND_ENGINES.md`.

### 3. Hosting proven open engines — Surge XT, Helm/Vital family

polyclav's LV2 (`livi`/`lilv`) and CLAP (`clack-host`) hosting is real
and shipping today, not aspirational. If Surge XT ships usable CLAP
and/or LV2 builds — to be confirmed by research — getting its
wavetable/hybrid engine into polyclav could be close to a packaging and
config exercise rather than new DSP code, and would deliver "a wide
range of ready-to-play sounds" faster than any from-scratch modeling
work. The licensing shape is the standard host/plugin relationship:
dynamically loading a GPLv3 plugin at runtime doesn't require the
Apache-2.0 host to relicense, the same way any DAW hosts GPL plugins
today — but this is a load-bearing enough claim that the research pass
should confirm it precisely (including any nuance if polyclav ever
bundles/redistributes plugin binaries itself, vs. documenting a
separate install step).

**[PENDING RESEARCH]** — plugin format confirmation, licensing nuance,
and a verdict on whether any Surge/Helm/Vital DSP internals (wavetable
engine, filter models) are worth studying for native reimplementation
land in `docs/OPEN_SOUND_ENGINES.md`.

### 4. Control-surface generalization (enabling thread — already scoped)

Threads 1–3 are only as reachable as the control surface lets them be.
This is fully planned already in `docs/CONFIGURABILITY.md`: MIDI note
input is already device-generic; the OSC mixer seam is Tier 0–1 shipped;
the real work is Tier 3, a `controlsurface.Surface` abstraction that
keeps the rich Launchkey UX as one implementation while a
config-driven `generic-midi` implementation covers everything else, with
Tier 4 device profiles as the polish layer. Nothing here re-plans that
work — the vision-level point is sequencing: **new native/hosted
engines are worth little if they're only reachable through
Launchkey-specific knob pages**, so Tier 3 should track the pace of
whichever sound-engine thread ships first, not trail it by a release.

## How it all stacks

| Doc | Scope | Status |
|---|---|---|
| `docs/VISION.md` (this doc) | North star; how the threads below fit together | draft |
| `docs/OPEN_SOUND_ENGINES.md` (planned) | Circuit emulation + organ modeling + Surge/Helm hosting research findings and concrete recommendations | pending — research in flight |
| `docs/ROADMAP.md` | Native subtractive synth (Minimoog-style voice) design record; owns the `engine` dispatch seam thread 2 plugs into | shipped (Phases 2–4) |
| `docs/CONFIGURABILITY.md` | Control-surface + hardware-seam generalization (Tier 0–4) — thread 4 | Tier 0–1 shipped, Tier 2–4 design |
| `docs/NATIVE_SYNTH.md` | User-facing state of the native synth | current |

## Guiding principles

- **Real-time safety first.** Anything added to the audio-core chain
  follows the existing native synth's discipline: no heap allocation, no
  locks, no blocking calls in the audio callback.
- **Native by default, hosting by exception.** Threads 1 and 2 default
  to from-scratch Rust — both for license cleanliness and because "build
  our own Hammond" is the explicit ambition, not "wrap someone else's
  clone." Thread 3 (hosting) is the deliberate, acknowledged exception:
  a fast path to breadth, with the dynamic-load licensing boundary kept
  strict. External crates remain fine when license-clean for static
  linking under Apache-2.0, per the existing `fundsp`/`mi-plaits-dsp-rs`
  precedent in `docs/ROADMAP.md`.
- **Don't regress the latency bar.** ~8 ms round-trip at a 128-frame
  quantum is the number to protect.
- **Don't smuggle in new cross-cutting assumptions.** The engine's
  hardcoded 48 kHz sample rate (`docs/CONFIGURABILITY.md` §1.1) is a
  known, separate seam — new instrument/effect code shouldn't bake in
  further sample-rate assumptions, and a general fix (if it happens)
  should be its own change, not a side effect of shipping an organ
  engine.

## Open questions

### Still deferred

1. **Where do circuit-emulated effects live in the config/patch model?**
   A fixed post-synth insert (like today's `input_comp`/`reverb`), or a
   per-patch pluggable effect chain (`[[patches.X.effects]]`)? The
   latter is more flexible but is new schema surface. **Lean: a single
   fixed drive slot is enough for a v1 with one effect type; revisit
   once the research pass says how many effect types are actually in
   scope.**

2. **Does the organ engine share the existing `Voice` architecture or
   need its own?** The Minimoog voice is osc+filter+env shaped per
   note; a tonewheel voice is closer to fixed-frequency additive
   generation plus a shared scanner/Leslie *bus* effect, not a per-voice
   one. **Lean: likely its own engine module under the existing
   `engine = "<name>"` dispatch, not a bent version of `voice.rs`** —
   to be confirmed by the research pass's architecture review of
   setBfree.

3. **How much of Surge/Helm hosting is really zero-code?** Pending
   confirmation of actual CLAP/LV2 build availability and behavior
   under polyclav's existing plugin backend (param automation, patch
   browsing UX) — this could be a doc-and-config update away, or could
   need real work in `plugin_clap.rs`/`plugin_lv2.rs`.

4. **Sequencing: control-surface Tier 3 first, or a sound-engine thread
   first?** They're each other's force multiplier, but only one goes
   first. **Lean: pair Tier 3 with thread 3 (plugin hosting), since
   it's likely the lowest-effort sound-engine win — it proves the
   control-surface abstraction against a real, rich hosted instrument
   instead of a synthetic test case, and unblocks thread 4's "wide
   range of ready-to-play sounds" goal fastest.**
