# Vision: A Native, Open Sound Engine

> **Status (2026-07-12):** draft, now backed by research findings for
> threads 1–3 (circuit emulation, tonewheel/organ physical modeling, and
> Surge XT/Helm hosting feasibility) — see `docs/OPEN_SOUND_ENGINES.md`
> for the full report, sources, and caveats. Thread 4 (native MIDI
> controller generalization) returned **no verified findings** in that
> research pass and needs its own dedicated follow-up; the enabling
> control-surface work it depends on is already fully scoped in
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

**Status: v2 shipped (2026-07-13).** `audio-core/src/dsp/drive_pedal.rs`
implements a Tube-Screamer-style antiparallel diode-pair clipper (the
research-recommended diode-clipper model, per `docs/OPEN_SOUND_ENGINES.md`
§1 — using a closed-form deep-conduction approximation rather than the
paper's Lambert-W solve), 2× oversampled, wired into the shared
post-synth chain (`synth → drive_pedal → patch_gain → ...`) and exposed
as MAIN knob 4 (`Pedal`), backend-agnostic and per-patch persisted the
same way as Volume/Reverb/Comp. **v1 shipped with a real bug**: the
diode-pair equation is only ever in deep saturation for a normalized
float signal (the `2·Is·R` denominator is minuscule next to any audible
amplitude), so scaling the pre-gain directly with `amount` made the
knob feel on/off — 1% already sounded maximally distorted. **v2**
fixes this at the architecture level: the nonlinearity always runs at a
fixed, fully-driven gain, and `amount` instead crossfades linearly
between dry and that fixed-character wet signal — smooth by
construction. The fix was verified (and the makeup-gain constant
calibrated) against a new general-purpose LUFS meter
(`audio-core/src/dsp/loudness.rs`, ITU-R BS.1770-4 K-weighting, exposed
via FFI as `polyclav_measure_lufs`/`polyclav_measure_peak_dbfs` and Go
as `audio.MeasureLUFS`/`MeasurePeakDBFS`) and a loudness-sweep
regression test in `lib.rs` that renders a held note across the whole
knob range and asserts no discontinuous loudness jump — exactly the
invariant-testing pattern the wider initiative below is about. A tube
amp emulation (preamp/tone-stack/power-amp/speaker, reusing the same
nonlinear-modeling groundwork) remains the natural second module — not
yet built. Black-box RNN modeling is a credible, likely-cheaper
complement for individual hard circuits later, but shouldn't replace
this approach as the first-class one. Full detail, sources, and
caveats (including a refuted competing paper) in
`docs/OPEN_SOUND_ENGINES.md` §1.

**New general-purpose infrastructure this unlocked:** the LUFS meter
and the render-a-clip-then-assert-an-invariant pattern aren't specific
to the drive pedal — they're the foundation for two things explicitly
on the table: (1) a reusable invariant-test harness other DSP effects
and synth params can adopt (render a sweep, assert bounded/smooth
loudness or peak), and (2) patch-loudness normalization (measuring, and
eventually matching, how loud different patches render relative to each
other) — scope and design for that second piece is still open, see
below.

### 2. Physically-modeled organ engine — "build our own Hammond"

**Target:** a new native synth backend. `docs/ROADMAP.md` §5 already
locked the extensibility seam for exactly this — `type = "native"` +
an `engine` sub-field, with `"minimoog"` as the first value and
`"fm"` / `"plaits"` / `"wavetable"` anticipated as future ones. A
tonewheel/organ engine (`engine = "tonewheel"`) is a natural next
entry in that same dispatch, not a new patch type. **Research verdict:**
setBfree is confirmed as the canonical reference architecture — its
documented signal flow (tonewheels/vibrato/click → preamp/overdrive →
reverb → Leslie → cabinet) maps directly onto a new
`audio-core/src/synth/organ/` module: its own engine (not a bent
`voice.rs`), reusing the existing LFO machinery for the scanner and the
thread-1 WDF infrastructure for the preamp/overdrive stage. A genuine
fast win: setBfree already ships a working LV2 plugin loadable through
polyclav's existing `livi`/`lilv` path *today*, in parallel with the
native build. Full detail, sources, and caveats in
`docs/OPEN_SOUND_ENGINES.md` §2.

### 3. Hosting proven open engines — Surge XT, Helm/Vital family

polyclav's LV2 (`livi`/`lilv`) and CLAP (`clack-host`) hosting is real
and shipping today, not aspirational. **Research verdict:** Surge XT
ships real, current Linux builds in Standalone, CLAP, LV2, and VST3 —
CLAP is a zero-porting-effort path through the existing `clack-host`
integration (packaging/testing only, no new DSP code); LV2 is real but
opt-in at build time, not in official CI binaries, so it's a
"build-it-yourself" path rather than a turnkey one. This is likely the
fastest way to deliver "a wide range of ready-to-play sounds." Licensing
is confirmed low-risk for the dynamic-load/hosting case under the FSF's
own GPL FAQ — the same standard relationship every DAW has with GPL
plugins — with one concrete rule to hold onto: **never bundle or
redistribute a GPL plugin's compiled binary in polyclav's own release
artifacts; always document installing it as a separate, user-driven
step.** Full detail, the licensing reasoning, and caveats in
`docs/OPEN_SOUND_ENGINES.md` §3.

Whether any Surge/Helm/Vital DSP internals are worth studying for native
reimplementation (as opposed to just hosting) wasn't covered by this
research pass — open question, see below.

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

**Research on this thread specifically came back empty** (see
`docs/OPEN_SOUND_ENGINES.md` §4) — none of the candidate claims about
MCU, Arturia KeyLab/Analog Lab mode, Novation SL mkIII, Akai, Korg,
NIHIA, or Nektar survived verification. That's a research gap to close
with a dedicated follow-up pass, not a signal that generalization is
infeasible; `CONFIGURABILITY.md`'s Tier 3 design doesn't depend on it.

A related, sharper question: what happens when a *hosted* engine
(Surge XT, Helm/Vital, setBfree) needs control-surface tie-in, not just
a native one? Generic parameter automation (a knob bound to "some CLAP
param") is already covered by Tier 3's binding model. **Deeper**
tie-in — a dedicated knob page addressing a specific named parameter on
a hosted plugin, the way the native synth's pages work today — is a
different, harder problem, and should be built, if it's ever built, only
through the plugin's own standard exposed protocol surface (CLAP's
parameter/extension API, LV2's control-port/patch mechanism) — never by
linking against or reading the plugin's GPL source directly. That
preserves the same "separate programs across a standard API" boundary
that keeps hosting license-clean in the first place (§3, above).
**Default: don't build this.** A hosted plugin stays opaque behind the
standard CLAP/LV2 API — patch-level selection plus whatever the generic
binding table can already address — until a concrete use case demands
more.

## How it all stacks

| Doc | Scope | Status |
|---|---|---|
| `docs/VISION.md` (this doc) | North star; how the threads below fit together | draft |
| `docs/OPEN_SOUND_ENGINES.md` | Circuit emulation + organ modeling + Surge/Helm hosting research findings and concrete recommendations | threads 1–3 done; thread 4 (MIDI controller generalization) returned no verified findings — open |
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
- **Hosted plugins stay opaque behind their standard protocol.** No
  linking against or reaching into a hosted GPL plugin's source for
  deeper control-surface tie-in. If that's ever needed, build it through
  the plugin's own exposed extension surface (CLAP params, LV2 control
  ports) — otherwise leave hosted engines walled off behind the plain
  CLAP/LV2 API, since that boundary is also what keeps the GPL-hosting
  story license-clean (thread 3, above). If a richer surface is ever
  worth building, it lives in a separate, GPL-licensed fork/addon of the
  hosted project (not in polyclav), which polyclav's docs point users to
  as an optional companion install — never merged into polyclav's own
  Apache-2.0 tree.

## Open questions

### Answered by research (2026-07-12)

- **Does the organ engine share the existing `Voice` architecture or
  need its own?** Confirmed by setBfree's architecture review: its own
  engine module (`audio-core/src/synth/organ/`) under the existing
  `engine = "<name>"` dispatch, not a bent version of `voice.rs` — see
  `docs/OPEN_SOUND_ENGINES.md` §2.
- **How much of Surge/Helm hosting is really zero-code?** CLAP: yes,
  packaging/testing only, no code. LV2: real but opt-in at Surge XT's
  build time, not in official binaries — a "build it yourself" path,
  not turnkey. See `docs/OPEN_SOUND_ENGINES.md` §3.

### Still deferred

1. **Where do circuit-emulated effects live in the config/patch model?**
   A fixed post-synth insert (like today's `input_comp`/`reverb`), or a
   per-patch pluggable effect chain (`[[patches.X.effects]]`)? The
   latter is more flexible but is new schema surface. **Lean: a single
   fixed drive slot is enough for a v1 with one effect type — the
   research pass didn't need to weigh in on config shape, so this stays
   open until a second effect type is actually on the table.**

2. **Sequencing: control-surface Tier 3 first, or a sound-engine thread
   first?** They're each other's force multiplier, but only one goes
   first. **Lean: pair Tier 3 with thread 3 (plugin hosting), since
   it's confirmed the lowest-effort sound-engine win (CLAP hosting is
   packaging-only) — it proves the control-surface abstraction against a
   real, rich hosted instrument instead of a synthetic test case, and
   unblocks thread 4's "wide range of ready-to-play sounds" goal
   fastest.**

3. **MIDI controller generalization (thread 4) needs a dedicated
   follow-up research pass** — the first pass returned zero verified
   claims on MCU, KeyLab/Analog Lab, SL mkIII, Akai, Korg, NIHIA, or
   Nektar. See `docs/OPEN_SOUND_ENGINES.md` §4. **Lean: run it as its
   own focused pass rather than folding it into a future round on the
   sound-engine threads.**

4. **Should hosted plugins ever get deeper control-surface tie-in than
   generic parameter automation** (a dedicated knob page addressing one
   specific named parameter on Surge/Helm/setBfree, the way native synth
   pages work)? **Lean: no, by default — wall it off behind the
   standard CLAP/LV2 parameter API.** If a real use case demands it
   later, the mechanism is a **custom CLAP extension**, not a polyclav
   change to the plugin's source: CLAP plugins already advertise
   optional extensions beyond the standard `params` one
   (`clap_plugin_get_extension`), and a host that recognizes a
   given extension ID can use its richer interface while any other host
   just falls back to the standard one. Concretely, that extension
   (e.g. structured parameter groupings + display names sized for an
   8-knob page, unlike CLAP's flat generic param list) would ship in a
   **separate, GPL-licensed fork/addon of Surge XT (or Helm/Vital)** —
   its own repo, maintained apart from polyclav, distributed
   independently — with polyclav's CLAP host (`plugin_clap.rs`) probing
   for the extension and using it when present, generic bindings
   otherwise. polyclav's own docs would point users at that fork as an
   optional companion install; nothing from it gets linked into or
   merged with polyclav's Apache-2.0 tree, which keeps thread 3's
   licensing verdict intact. Worth doing only once a concrete "which
   parameters, on which page" need shows up — not speculatively.

5. **Does RNN/black-box circuit modeling generalize past the Klon
   Centaur** to other reference circuits (Tube Screamer, Big Muff, Fuzz
   Face, Marshall/Fender preamps)? Not established by the research pass.
   **Lean: doesn't block anything — build the WDF Tube Screamer module
   first regardless, since it doesn't depend on this answer.**

6. **What should patch-loudness normalization actually be** — an
   offline calibration tool that measures each configured patch's LUFS
   and reports/suggests `gain_db` values (a human stays in the loop), or
   a live auto-normalizing DSP stage that continuously matches loudness
   in the chain? **Answered in part (2026-07-13): started with offline
   measurement**, since it's the thin layer over what already existed —
   a live auto-normalizing stage remains undecided and is new real-time
   DSP design work, not started. The gap flagged here originally
   (`polyclav_render_offline` was native-patch-only) is now closed:
   `polyclav_render_offline_events` renders an arbitrary timed MIDI
   event sequence through *any* patch type (soundfont, native, lv2,
   clap), device-free. On top of it, `internal/measure` is a small,
   reusable Go framework:
   - `LoadMIDIFile` parses a Standard MIDI File into frame-timed events
     (`internal/measure/testdata/short_phrase.mid` is the first fixture
     — a 4-beat arpeggio plus an overlapping chord tail, with a
     regenerating tool at `testdata/gen/`).
   - `MeasurePatch`/`MeasurePatches` render a patch against those
     events and report LUFS + peak, folding `GainDB` in additively
     (exact, not an approximation — both are log-power measures).
   - `CheckConsistent`/`CheckGradual`/`CheckBounded` are generic,
     rendering-agnostic checks over a `[]Measurement` — reusable for
     anything that produces labeled loudness/peak numbers, not just
     patch comparison. `CheckGradual` is deliberately shaped to catch
     exactly the drive-pedal "1% is already maximally distorted"
     regression, generalized to any labeled sweep; the Rust-side
     drive-pedal loudness tests haven't been ported to this framework
     yet (they predate it and still work) but are a natural candidate.
   - Tested end-to-end (real MIDI file → real native-synth render →
     real LUFS measurement → consistency check catching a deliberately
     mismatched `gain_db`), but only against the one native engine this
     environment has — no soundfont/sfz assets are bundled in the repo
     (bootstrap-downloaded user data), so cross-*type* consistency
     (piano soundfont vs. native synth, say) is unverified until
     someone runs it with real assets present.
   - Not yet built: any CLI surface (`polyclav measure` or similar) —
     today this is a library other Go code/tests call directly, not a
     user-facing tool.
