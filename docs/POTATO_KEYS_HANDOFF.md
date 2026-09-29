---
title: "Brief for the Potato Keys agent: make an instrument-native control surface"
date: 2026-09-29
status: in-review
tags: [handoff, instruments, control-surfaces]
summary: "Self-contained facts, design constraints, and questions for joint Potato Keys–Polyclav planning."
vantage:
  status-chip: true
---

# Brief for the Potato Keys agent: make an instrument-native control surface

**Status:** CURRENT. This is a planning brief, not authorization to implement.
The proposed architecture and owner decisions live in the
[companion design](./design/potato-keys-control-surface.md); it takes
precedence if these documents diverge.

## What we are trying to achieve

Potato Keys should feel like an instrument whose front panel happens to be a
Novation Launchkey MK4 61, not like a list of 40 anonymous CLAP parameters.
Organ, Tine and Reed should become distinct playable, recallable patches.
The same musical control meanings should transfer to a second surface with
fewer controls or no screen. Please challenge the proposed assignments and
return a better musical layout where you see one; **do not implement changes
before we agree on the contract**.

## What Polyclav can actually do today

- **Play and recall.** On Linux, Polyclav loads `com.littlepotato.keys` from
  a configured `.clap` path, sends keyboard MIDI, discovers parameters,
  schedules writes, receives parameter feedback, and saves/restores an opaque
  CLAP state blob **per named patch** on switch/shutdown. Loading is
  asynchronous. macOS has audio output but **not CLAP hosting** yet; it also
  needs an arm64 Potato Keys build.
- **Keep one Launchkey hardware layout.** Polyclav enters DAW mode and
  supports DAW pads, Plugin encoders with relative output, and Volume faders.
  It restores those layouts when the player selects another mode by default;
  we do not want to program or overwrite the user's Custom modes. Device
  modes and *host-created pages* are different: a host page only changes
  what the existing encoders/buttons mean.
- **Use real physical controls.** The DAW port supplies 16 velocity/pressure
  pads (top notes 96–103, bottom 112–119), eight relative encoders (channel
  16, CC85–92, pivot 64), nine absolute faders (channel 16, CC5–13), nine
  buttons (observed channel 1, CC37–45), and Play/Stop/Record/Loop (observed
  channel 1, CC115–118). Track Left/Right select banks of eight patches;
  pad-bank Up/Down enter the current page navigation. The encoder-bank
  arrows and shifted combinations are observed in raw MIDI but not assigned
  as host actions. Device-local settings/Octave/Fixed Chord need not send
  usable DAW events. MIDI is not proof of a working visible action: the
  recently corrected transport decoder has tests but awaits a live check.
- **Paint a small screen and lights.** The current host interface writes two
  ASCII lines of up to 16 bytes, with an 800 ms parameter/status popup before
  restoring the patch name. It colors individual pads and fader buttons.
  Top-row pads currently select patches, bottom-row pads indicate five
  native-synth knob pages. For a CLAP patch, only the global MAIN encoder page
  works today; plugin-specific pages are a proposed host change.
- **Organ special case exists.** An explicitly Organ-owned patch binds all
  nine faders to footage-named CLAP parameters (16′, 5⅓′, 8′, 4′, 2⅔′,
  2′, 1⅗′, 1⅓′, 1′) with range 0–8. It never sends the same fader to the
  OSC mixer. Button 1 cycles drawbar coloring, 2 cycles color schemes,
  9 toggles Leslie Stop/Fast; a connected expression pedal can move an
  `Expression` parameter 0–100; Launchkey sustain CC64 can toggle/momentarily
  control a `Rotary` parameter 0–3. These behaviors are patch opt-ins, **not
  Tine/Reed defaults**. A parameter mismatch captures the Organ fader but
  changes neither plugin nor mixer.

This inventory is grounded in [hardware captures](./HARDWARE_TESTS.md), the
[current user guide](./USER_GUIDE.md#launchkey-organ-drawbars) and Polyclav's
[driver](../internal/launchkey/driver/driver.go),
[host pages](../internal/controls/pages/pages.go),
[organ fader router](../cmd/polyclav/organ_fader_router.go), and
[CLAP bridge](../internal/audio/audio.go). The proposed direction is in the
[companion design](./design/potato-keys-control-surface.md).

## What the Linux plugin reported here

On 2026-09-29, `DiscoverClapParams` against the mounted Linux Potato Keys
binary returned **40** controls for `com.littlepotato.keys`:

| Group or parameter | Reported shape | Integration concern |
|---|---|---|
| Drawbars | Nine footage labels, each 0–8 | Organ-only meaning; current host requires exactly these labels/ranges. |
| Expression; Rotary | 0–100; 0–3 respectively | Confirm value names, continuous/stepped behavior and who changes them. |
| Engine | 0–2, default 0 | Prior exploration says 0=Organ, 1=Tine, 2=Reed; please confirm from plugin source and saved state. |
| Engine module | Touch, Bark, Bell, Decay, Release, Pickup, Detune, Tremolo, Trem Rate, Drive, Tone, Width, Output Level | Rank *by sound and performance use* for each engine; names alone do not establish which is relevant. |
| Other controls | Key Click, Percussion, Percussion Level/Decay, Rotary Width, Drive, Tone, Output Level, Factory Preset, Whisk Preset, Leslie Mode, Rotor Running/Speed and two Leslie-specific switches | Distinguish current, deprecated, informational, hidden, and engine-specific parameters. |

`Drive`, `Tone` and `Output Level` are **duplicate names** in different
modules. Numeric parameter IDs were returned, but this brief does not treat
one build's numeric list as a cross-version mapping. The plugin agent can
confirm which IDs are intentionally stable. Our host currently exposes
name/module/range/default/current/flags and raw numeric writes, not rich
step labels or CLAP `value_to_text` strings on the hardware display.

## Proposed happy path to evaluate together

1. **Separate patches, one plugin.** Organ, Tine and Reed use the same CLAP
   plugin ID but independent per-patch saved state. A new patch with no saved
   state currently loads the plugin's *default Organ*, regardless of the
   patch name. Please propose a reliable way to seed Tine and Reed: versioned
   factory state blobs, a documented parameter initialization transaction,
   or another safe mechanism. Include state migration when the plugin updates.
2. **Describe musical controls before physical mapping.** For each engine,
   provide stable control identity, units, step/enum labels, current value,
   availability, value-to-display text, and a sensible performance grouping.
   Say what standard CLAP metadata and state already cover, what the plugin
   could improve, and what would need an optional integration profile.
   Never assume the first eight parameters are the best eight encoders.
3. **Use the Launchkey as one realization.** Organ keeps nine drawbars as the
   fader bank. Candidate encoder pages address percussion/click, Leslie,
   timbre and output. Tine/Reed would have their own tone/envelope/motion
   pages; faders are *not* Organ drawbars in those engines. Patch pads and
   page indicators retain coherent meanings. Give priority to playable
   changes, with short screen labels and feedback from the plugin's actual
   resulting value. Evaluate whether the existing color-cycling buttons are
   worth their real estate; do not change them silently.
4. **Handle transitions and another controller.** Decide how absolute
   faders behave after a preset changes without moving the physical fader;
   how an external plugin UI or preset change updates the lights/screen;
   how a loading/failed patch silences old assignments; and what falls back
   to web controls when a second controller lacks a screen, LEDs or faders.
   Same musical meaning, different device-specific presentation.

## What we need back from you

Please send a **design response, not a code change**:

1. A table of Organ/Tine/Reed controls with semantic identity, module and
   stable CLAP ID policy, range/units/step/labels, engine applicability,
   suggested rank for performance, and which engine(s) may alter each value.
   Mark any field requiring source inspection or audible testing unknown.
2. Two or three concrete playing workflows (e.g. drawbar change + Leslie,
   Tine tremolo/tone, Reed articulation), including screen feedback and
   patch recall. Identify missing plugin capabilities before proposing a
   hardware gesture.
3. A recommendation for initial engine-specific state, plugin→host feedback
   and compatibility across plugin versions. Say whether standard CLAP
   parameter and state APIs are enough or what extra descriptor would add.
4. One recommended MK4 layout per engine, plus how its **musical controls**
   degrade onto an eight-encoder-only or plain MIDI CC surface. Compare a
   performance-first option with a minimal-change option; name trade-offs.
5. Questions for the Polyclav owner: fader pickup versus immediate drawbar
   moves and whether to repurpose buttons 1/2. Those are live decisions in
   the [companion design](./design/potato-keys-control-surface.md#open-questions),
   not permissions to change established behavior now.

We will reconcile the answer against current host behavior, then settle the
mapping together and test the result on the **real** Launchkey and a real
Potato Keys build. Simulated MIDI and CLAP tests are necessary but cannot
verify the keyboard's applied layouts, display or audible workflow.
