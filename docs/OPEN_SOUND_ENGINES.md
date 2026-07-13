# Open Sound Engines — Research Findings

> **Status (2026-07-12):** research complete for threads 1–3 (circuit
> emulation, organ modeling, Surge XT/Helm hosting); thread 4 (MIDI
> controller generalization) returned **zero verified findings** this
> pass — treat as an open gap, not a "nothing exists" conclusion. Sourced
> from a 96-agent deep-research pass: 3 search angles, 16 sources
> fetched, 57 claims extracted, 25 adversarially verified (19 confirmed,
> 6 refuted, 0 left unverified). See `docs/VISION.md` for how these
> threads fit the overall direction, and `docs/ROADMAP.md` §5 for the
> `engine = "<name>"` dispatch seam thread 2 plugs into.

## TL;DR

| Thread | Evidence | Build/do first |
|---|---|---|
| 1. Circuit emulation (pedals/amps) | Strong | **Shipped 2026-07-13** — diode-clipper module (Tube-Screamer-style) in `audio-core/src/dsp/drive_pedal.rs`; see `docs/VISION.md` §1 for what changed from the plan below |
| 2. Organ physical modeling | Moderate, plus a fast win | Load setBfree's existing LV2 plugin today; build a native `organ` engine mirroring its stage order as the real follow-up |
| 3. Hosting Surge XT / Helm / Vital | Strong | Surge XT's CLAP build via the existing `clack-host` path — packaging/testing, no new DSP code |
| 4. MIDI controller generalization | **None verified** | Needs its own dedicated research pass; `docs/CONFIGURABILITY.md`'s Tier 0–4 plan stands independently |

---

## 1. Circuit emulation — drive pedals & amps

### Build first: Wave Digital Filter diode-clipper modeling

Werner, Nangia, Bernardini, Smith III & Sarti (139th AES Convention,
2015 — CCRMA/Stanford + Politecnico di Milano) derive an explicit
wave-domain diode-clipper model supporting an arbitrary number of diodes
per orientation, correcting the prior Paiva et al. (2012) model via a
Lambert-W term, and validate it directly against SPICE and a real modded
Tube Screamer clipping stage.

**Why this is the right first target:** it's a self-contained, precisely
specified nonlinear one-port element that composes with a WDF tree for
the surrounding RC network (input buffer / tone stack). O(1) per-sample
cost once the adaptor tree is built; needs 2×–4× oversampling around the
hard nonlinearity for antialiasing, which is standard and cheap. Fully
buildable pure-Rust from scratch — no crate needed, the Lambert-W
evaluation is a short iterative/rational approximation.

**Landing spot:** a new `audio-core/src/dsp/drive.rs` (or
`dsp/pedal/tube_screamer.rs`) implementing the clipping stage as a WDF
diode-pair + tone-stack model, inserted as a new stage early in the
existing chain — `synth → drive → patch_gain → input_comp → reverb →
mastering_comp → limiter → master_volume`. Drive/distortion belongs
pre-gain-staging and shouldn't double up with `input_comp`'s or
`mastering_comp`'s dynamics processing.

> **Implemented as `audio-core/src/dsp/drive_pedal.rs`** (2026-07-13),
> at the landing spot recommended above. Two deviations from this
> section's plan, both load-bearing enough to flag: (1) no tone-stack
> RC network yet — v1 is the diode-pair nonlinearity alone, memoryless;
> (2) the diode-pair equation is solved via the closed-form
> deep-conduction approximation (`v = Vt·asinh(driven/(2·Is·R))`)
> rather than the paper's Lambert-W solution — a first attempt at
> fixed-iteration Newton-Raphson (an alternative numerical solve, not
> what the paper does either) turned out to diverge at this pedal's
> gain range, which is what motivated the closed-form swap. See
> `docs/VISION.md` §1 for the up-to-date status.

### Alternative / later complement: black-box RNN modeling

Chowdhury (CCRMA/Stanford, ADC20, arXiv:2009.02833) benchmarked a full
nodal+WDF Klon Centaur emulation against an RNN-based one: the RNN had
lower compute-time-per-second-of-audio at every tested block size on a
2017 laptop-class i7, and both ran live in real time on a $35 Teensy 4.0
at 44.1 kHz. The paper's own recommendation: nodal analysis for simple
linear stages, WDF where per-component/topology modularity matters, and
small RNNs (an 8-unit GRU sufficed here) for complex, stateful nonlinear
circuits, where they can match or beat circuit-model accuracy while
running cheaper.

**Verdict:** WDF/nodal stays the first-class implementation — better
documented, deterministic, easier to debug sample-by-sample. Treat RNN
gain-stage replacement as a stretch goal *per circuit*, trained on your
own WDF model's output once one exists. A single 8-unit GRU is cheap
enough to hand-roll in Rust with no ML framework dependency.

**Caveat:** this evidence is one well-vetted case study (Klon Centaur
only). A competing comparative-architecture paper (arXiv 2405.04124,
benchmarking LSTM/LSTM-encoder-decoder/LRU/S4D across an OD300 overdrive
and several compressors/EQ) was investigated but **its claims were
refuted on adversarial verification and excluded** — don't treat "RNN
modeling generalizes across circuit types" as established from this
pass.

### Second-phase target: tube amp emulation

Buffa & Lebrun (WWW'18 Companion, ACM) built a browser-based tool
combining a chainable virtual pedalboard with a stage-by-stage Marshall
JCM 800 emulation (preamp, tone stack, reverb, power amp with negative
feedback, speaker), using FAUST for at least the power-amp stage — the
conventional guitar → pedals → amp signal flow, with the amp as the
pedalboard's terminal block.

**Verdict:** a heavier build than the single diode-clipper pedal (more
state, more nonlinear stages to tune and validate). Recommend as thread
1's *second* module, reusing the WDF/nodal infrastructure from the drive
pedal (triode/op-amp gain stages instead of diode clippers). Use a
convolution IR for speaker cabinet + mic initially — far cheaper and
just as credible for most listeners — and defer a fully modeled cab+mic
physical model to a later pass.

---

## 2. Physically-modeled organ engine

### Reference architecture: setBfree

setBfree ([github.com/pantherb/setBfree](https://github.com/pantherb/setBfree))
is confirmed as the canonical open-source Hammond+Leslie emulator, with
an explicit documented signal flow: **Synth Engine (tonewheels / vibrato
/ click) → Preamp/Overdrive → Reverb → Leslie → Cabinet Emulation →
Audio-Out**. Its core "Beatrix" C engine has been reused near-verbatim
(>97% line-identical in the tonewheel generator) by the independent
[OpenB3](https://github.com/michele-perrone/OpenB3)/JUCE project —
confirming the algorithms are cleanly portable, not glued to a specific
plugin framework.

**Verdict:** mine this at the algorithm/stage-decomposition level, not
via code reuse (it's GPL C; polyclav's native-synth ethos favors
from-scratch Rust). Recommended new backend —
`audio-core/src/synth/organ/` — mirroring setBfree's stage order 1:1:

1. **Tonewheel bank** — additive sine generation across the 91 physical
   tonewheels, with the imperfections that make it read as
   electromechanical rather than digital: crosstalk/leakage via a shared
   coupling matrix between adjacent wheels, foldback distortion when
   summed drawbar levels clip, and a key-click transient injected on
   note-on/off.
2. **Drawbar mixer** — 9 harmonic drawbars per manual.
3. **Vibrato/chorus scanner** — modeled as a modulated short delay line
   / rotating-capacitor emulation; reuse the phase/LFO machinery already
   in `audio-core/src/synth/lfo.rs`.
4. **Preamp/overdrive** — a triode-style soft clip; a second consumer of
   the WDF/nodal infrastructure from thread 1.
5. **Leslie rotary speaker** — Doppler pitch/amplitude modulation for
   horn and drum at independent rotor speeds, AM, horn/drum crossover
   filter, power-amp overdrive, and mic'd cabinet coloration. Its own
   module — it's independently reusable as a general-purpose effect on
   other voices too.

This is a substantially larger build than the drive-pedal module (more
voices, more coupled state) and should be scoped as its own project
phase — but setBfree's documented architecture removes nearly all
research risk on "what are the stages and in what order."

### Fast near-term win: setBfree's own LV2 plugin

setBfree ships real, working LV2 bundles (`b_synth.lv2`, `b_whirl.lv2`,
`b_conv.lv2`, `b_overdrive.lv2`), distro-packaged on Debian/Arch, proven
loadable via lilv-based hosts (`jalv`, MOD Audio's pedalboard) — the
same `lilv` library polyclav's `livi` crate wraps. GPLv2-or-later.

**Verdict:** a genuine near-zero-code path to a working Hammond/Leslie
sound *today*, in parallel with — not instead of — the native engine
effort. Concretely: verify `b_synth.lv2` loads and plays correctly
through polyclav's existing LV2 host path, and document
install-from-distro-package as the supported route. Doesn't diminish the
case for the native engine (matches the pure-Rust preference, avoids a
permanent GPL runtime dependency for an Apache-2.0-only build) but
validates "what should this even sound like" and ships organ support far
sooner. Licensing mechanics are identical to the Surge XT/Helm case
below.

---

## 3. Hosting Surge XT / Helm / Vital

### Surge XT ships real, current Linux builds

Confirmed via the official nightly/stable download pages and the live
GitHub README: Linux builds ship as **Standalone, CLAP, LV2, VST3**. LV2
support is official but **opt-in** (`-DSURGE_BUILD_LV2=TRUE`, arrived
with the JUCE 7 migration) — "we don't build LV2 either by default or in
our CI pipeline," per the README.

> An earlier pass surfaced a claim from Surge XT's FAQ page that it does
> *not* ship LV2 and *does* ship CLAP as the only path — that claim was
> checked against two other primary sources (the nightly download page
> and the live README) and refuted: the FAQ page's wording was stale
> relative to the current build. Standalone/CLAP/LV2/VST3 (LV2 opt-in)
> is the current, authoritative state.

**Verdict:**

- **CLAP — the concrete first step.** polyclav already hosts CLAP via
  `clack-host`, and Surge XT ships an official CLAP build. This should
  be packaging/config/testing only: confirm the plugin loads, exposes
  params, and processes audio correctly through the existing path. No
  new DSP code required.
- **LV2 — usable, but not turnkey.** Works via `livi`/`lilv`, but only
  from a from-source build with the opt-in flag — not in official CI
  binaries. Document it as "works if you build it yourself," not
  assumed-available from a standard package.

### Licensing: hosting a GPLv3 plugin under Apache-2.0

The FSF's own GPL FAQ (gnu.org, verified live) states: *"A main program
that is separate from its plug-ins makes no requirements for the
plug-ins,"* but *"If the main program dynamically links plug-ins, and
they make function calls to each other and share data structures, we
believe they form a single combined program."* Separately, the FAQ's
"aggregate" doctrine (rooted in GPLv3 §5's mere-aggregation clause)
permits distributing unrelated programs together (e.g. bundled in the
same installer/media) under any license mix without GPL obligations
spreading, as long as they remain separate programs rather than a
combined work.

**Verdict:** for the common case — polyclav loading a GPLv3 plugin
(Surge XT/Helm/Vital/setBfree) at runtime via CLAP/LV2 `dlopen`, with
the user separately installing the plugin — this is squarely the FSF's
own "separate programs"/"aggregate" scenario. It does not require
polyclav to be GPL-licensed, and doesn't trigger any GPL obligation on
polyclav's own Apache-2.0 code. This is exactly how every commercial or
proprietary DAW hosts GPL plugins today.

The one nuance that genuinely needs care: polyclav's actual hosting
mechanism involves in-process function calls and shared audio-buffer
data structures across the plugin ABI boundary — closer to the FAQ's
"combined program" language than a fork/exec IPC boundary would be.
That said, this is precisely the situation every VST3/CLAP/LV2 host
(proprietary or open) is already in, and the field's uniform practice —
plus the FSF's own plugin-hosting guidance, written with exactly this
ABI-boundary case in mind — treats standardized plugin-API hosting as
not creating a combined derivative work of the host.

**What does need care:** if polyclav's own installer/release process
ever bundles or redistributes the GPL plugin's *compiled binary* itself,
rather than documenting "install Surge XT/setBfree separately," that
shifts from "aggregate" toward tighter coupling. **Recommendation:
always document plugin installation as a separate, user-driven step
(distro package or upstream installer/build) — never vendor a GPL
plugin binary into polyclav's own release artifacts.** This sidesteps
the nuance entirely.

This is well-grounded guidance, not a court-tested legal conclusion —
standard industry risk every plugin host already accepts, not a hard
guarantee.

### Reimplementing DSP internals natively — not covered this pass

No verified findings in this batch address whether Surge's, Helm's, or
Vital's specific DSP internals (wavetable engine, filter models,
modulation matrix design) are worth studying for native reimplementation
distinct from just hosting the plugin. Open question, not a "no" — see
below.

---

## 4. MIDI controller generalization — no findings this pass

Zero claims survived adversarial verification for this thread. The 19
confirmed claims from this research batch cover threads 1–3 exclusively;
nothing about MCU (Mackie Control Universal), Arturia KeyLab/Analog Lab
mode, Novation SL mkIII, Akai, Korg, Native Instruments' NIHIA protocol,
or Nektar was confirmed. **This is a research gap, not a finding that
nothing exists** — it's unclear from this pass whether the topic wasn't
searched, or whether every candidate claim failed verification.

**This does not block anything above.** `docs/CONFIGURABILITY.md`'s
Tier 0–4 plan for control-surface generalization was scoped
independently of this research pass and remains the reference for this
thread.

**A dedicated follow-up research pass should investigate:**

- Whether MCU's well-documented Mackie Control protocol is a viable
  common denominator for basic knob/fader mapping across non-Launchkey
  keyboards, separate from full native-mode screen/pad control.
- Which manufacturer protocols (NIHIA, KeyLab MCU mode, SL mkIII) are
  actually documented or reverse-engineered in open-source projects —
  comparable to setBfree's role for organ modeling — versus requiring
  fresh reverse engineering from scratch.

---

## Caveats

- Several sub-claims split 2-1 on adversarial vote (the JCM800
  stage-by-stage claim, the embedded-Teensy real-time claim, the OpenB3
  engine-reuse claim, setBfree's LV2-loadability and GPL-licensing
  claims, Surge XT's opt-in-LV2 claim, and one GPL-FAQ
  combined-program claim) — still corroborated by primary/official
  sources plus independent secondary verification, but carrying more
  residual uncertainty than the unanimous 3-0 claims.
- The GPL/Apache-2.0 analysis is well-grounded in the FSF's stated
  position but isn't case law — real legal debate exists about whether
  standardized plugin-API dynamic loading always avoids "combined work"
  status. Standard industry risk every plugin host already accepts, not
  a hard guarantee.
- RNN/neural modeling evidence is one well-vetted case study (Klon
  Centaur); broader claims across other circuit types (Tube Screamer,
  Big Muff, Fuzz Face, Marshall/Fender preamps specifically) were not
  independently verified this pass.
- No independent verification of real-time CPU cost on polyclav's
  actual target hardware — only the Klon Centaur paper's laptop/Teensy
  benchmarks were confirmed. A prototype-and-profile step is needed
  before committing to specific oversampling factors or engine
  complexity for the drive-pedal or organ modules.
- Findings describe upstream project capabilities as of source-fetch
  time (this session, 2026-07-12) — build-flag defaults (e.g. Surge
  XT's opt-in LV2) and CI/packaging status can change; re-check before
  implementation.
- The research grounded its "how this lands in polyclav" verdicts
  against a light read of `audio-core/src/dsp/`, `audio-core/src/synth/`,
  `audio-core/Cargo.toml`, and `internal/launchkey/` (confirming the
  module list and declared deps — `livi 0.7`, `clack-host 0.1`), but did
  not independently re-derive the DSP chain's exact stage order from
  source; the chain order was taken as given from the research prompt.

## Sources

- Werner, Nangia, Bernardini, Smith III & Sarti, *"An Improved and
  Generalized Diode Clipper Model for Wave Digital Filters,"* 139th AES
  Convention (2015) — [researchgate.net](https://www.researchgate.net/publication/299514713_An_Improved_and_Generalized_Diode_Clipper_Model_for_Wave_Digital_Filters)
- Chowdhury, *"A Comparison of Virtual Analog Modelling Techniques for
  Desktop and Embedded Implementations"* (arXiv:2009.02833) —
  [ccrma.stanford.edu](https://ccrma.stanford.edu/~jatin/papers/Klon_Model.pdf)
- Buffa & Lebrun, WWW'18 Companion (ACM) —
  [dl.acm.org](https://dl.acm.org/doi/fullHtml/10.1145/3184558.3186973)
- setBfree — [github.com/pantherb/setBfree](https://github.com/pantherb/setBfree)
- OpenB3 — [github.com/michele-perrone/OpenB3](https://github.com/michele-perrone/OpenB3)
- setBfree LV2 packaging — [x42-plugins.com/x42/setBfree](https://x42-plugins.com/x42/setBfree)
- Surge XT — [github.com/surge-synthesizer/surge](https://github.com/surge-synthesizer/surge)
- Surge XT nightly builds — [surge-synthesizer.github.io/nightly_XT](https://surge-synthesizer.github.io/nightly_XT/)
- GNU GPL FAQ — [gnu.org/licenses/gpl-faq.html](https://www.gnu.org/licenses/gpl-faq.html)
- Refuted (excluded): Comini et al., comparative neural VA architecture
  benchmark (arXiv:2405.04124) — claims about cross-effect neural
  architecture performance did not survive verification for this report.

## Open questions

### Still deferred

1. **MIDI controller generalization (thread 4) needs a dedicated
   follow-up research pass** — see §4 above. **Lean: run it as its own
   focused research pass rather than folding it into a future round on
   these three threads; it's a genuinely separate research question.**

2. **Does RNN/black-box modeling generalize past the Klon Centaur** to
   other reference circuits (Tube Screamer, Big Muff, Fuzz Face,
   Marshall/Fender preamps)? Not established by this pass. **Lean: don't
   commit to RNN modeling as a general strategy until at least one more
   circuit type is benchmarked — build the WDF Tube Screamer module
   first regardless, since it doesn't depend on this answer.**

3. **Real-time CPU cost on polyclav's actual target hardware** is
   unverified beyond the Klon Centaur paper's laptop/Teensy numbers.
   **Lean: prototype the WDF drive module first and profile it in the
   real audio-core RT thread before deciding oversampling factors or
   committing to the organ engine's per-voice cost budget.**

4. **Whether polyclav's specific in-process plugin-hosting mechanism
   needs a closer legal look before any bundling/redistribution
   decision.** Dynamic-load-only hosting (the near-term plan) is fine
   per the FSF's own guidance; this only matters if/when polyclav's
   release process considers bundling a GPL plugin binary directly.
   **Lean: no action needed now — revisit only if bundling is ever
   proposed.**

5. **Whether Surge's/Helm's/Vital's DSP internals are worth studying
   for native reimplementation** (wavetable engine, filter models,
   modulation matrix), distinct from hosting them as plugins. Not
   covered this pass. **Lean: low priority until the CLAP-hosting path
   is actually shipped and evaluated — hosting may satisfy the need
   without any reimplementation.**
