---
title: "Developer brief: make an instrument and a control surface work together"
date: 2026-09-29
status: in-review
tags: [handoff, instruments, control-surfaces]
summary: "Reusable intake for instrument developers, controller developers and their host integrator."
vantage:
  status-chip: true
---

# Developer brief: make an instrument and a control surface work together

**Status:** CURRENT. Use this to start a design exchange with any instrument
or controller developer. It describes a **proposed contract, not a shipped
Polyclav SDK**. The [companion design](./design/instrument-control-surface.md)
owns the architectural decisions and two open owner rulings.

## The request

We want an instrument to feel playable from a hardware control surface
without tying musical behavior to one model's MIDI numbers. Please tell us
what your instrument or controller can *actually* do, what users most need
on stage, and what an integration can verify. Propose a layout that survives
a smaller controller as well as a fully featured one. **Return a design and
test plan, not a code change yet.**

An instrument's named value or action has a stable identity, such as filter
cutoff or rotary speed; it is not the encoder, MIDI CC or screen position
assigned to it. A controller reports only inputs and outputs it can actually
use, such as an absolute slider or short text display; not every surface has
both. The host assigns these actions to the available hardware while owning
patch selection and conflict rules. The [portable boundary](./design/instrument-control-surface.md#2-the-portable-boundary)
defines the terms used in the design.

## Current host realities, not requirements on your product

- Polyclav accepts selected class-compliant MIDI note inputs and plays
  soundfont, native synth, LV2 and CLAP patches. It already saves native
  synth settings and CLAP plugin state per patch; this does **not** imply
  state recall exists for every backend. Linux CLAP hosting exists; macOS
  CLAP hosting does not yet. Your instrument need not be CLAP or persist
  state.
- The *current* rich surface driver is Launchkey MK4-specific. It has a
  read-only MIDI debugger, separate DAW control and performance ports,
  button/pad feedback, and host-created encoder pages. There is **no
  generic control-surface driver or profile API yet**. Supporting another
  surface beyond note input will require host adapter work.
- An Organ-only path presently maps nine faders to nine validated drawbars.
  Native-synth knob pages do not automatically become plugin pages. These
  are examples of today's code, not expectations for another instrument.
- The proposed split is: your backend supplies actual identity, range,
  state and feedback; a surface supplies input/output capabilities; the
  host supplies musical grouping, assignments, conflict resolution and
  patch-aware page state. An optional semantic profile can augment native
  metadata, but bare instruments still play and can use explicit bindings.

The [design](./design/instrument-control-surface.md) specifies the proposed
boundary and failure behavior. [User documentation](./USER_GUIDE.md) and
[hardware observations](./HARDWARE_TESTS.md) describe what is already live
on the Launchkey. Treat the code, not older roadmap sketches, as the source
of truth for existing host behavior.

## If you develop an instrument or plugin

Please report one row per *musically useful* control; a long undifferentiated
parameter dump is not a front panel. Use `unknown` for facts you have not
verified rather than guessing.

| Tell us | Questions to answer |
|---|---|
| Identity | What backend ID is stable across versions? Can two controls share a display name? Is module/scope part of the identity or only presentation? |
| Shape | Range, default, continuous/stepped/enum/momentary/read-only, musical adjustment step, units, value-to-text and short label. Which parameter values are *requests* versus actual results? |
| Applicability | Which engine, voice, preset or operating mode enables the control? Does a change to that variant reset or hide other values? Which controls are safe during a held note? |
| Priority | Which eight adjustments matter most during playing? What belongs on a second page, a switch or a web editor? Provide two or three concrete playing workflows. |
| State | Can you save and restore a complete sound? How do new named variants acquire initial state, and what happens across format/version changes? If not supported, say so. |
| Feedback | How does an edit in your own UI reach the host? Is there a confirmed readback after restore or a write? What can fail or be deferred? |
| Compatibility | Which parameter/notification/state operations already exist in your standard interface, and what musical information would require an optional profile? |

For a CLAP plugin, the standard
[parameter and state extensions](https://github.com/free-audio/clap/tree/main/include/clap/ext)
are the first place to look; stable IDs, name/module, flags, current values,
value-to-text, change events and state each answer part of the request.
That is a transport example, not a mandate for every plugin. For a MIDI-only
instrument, an explicit CC map can start the conversation; no readback
means we cannot promise that a displayed value equals the instrument's
current state.

## If you develop a controller or other surface

Please supply its physical map **per mode and port**: input status/channel,
number, press/release, relative encoding or absolute resolution, touch and
pressure behavior; output commands, palettes, display dimensions and text
encoding; reconnect behavior; which local modes are user-owned. Separate
what the manual specifies from raw captures and from output **visibly
confirmed** on a real device.

Also describe reserved navigation and safety gestures, whether the surface
can display a parameter's resulting value, whether fixed faders support
pickup/motorization, and what happens without a display or a matching
control. A usable eight-encoder surface need not pretend to have nine
faders; it can use named pages or a smaller explicit selection. Do not
reprogram persistent Custom modes as a substitute for host assignments.

## Questions for the joint layout

1. How does a player choose a sound, locate the important parameters, and
   return to a known sound after switching? Distinguish a device's hardware
   mode, a host-created page, and an instrument's own sound/variant.
2. Which physical controls are reserved for patch/page navigation or safety?
   Never silently reuse one for an instrument action. An instrument gesture
   must have **one destination**; failure to bind it must not redirect it to
   a mixer or another instrument.
3. What does a fixed fader do when recalled state and physical position
   disagree: immediate change, pickup, or an explicit role-specific rule?
   How will a player know it is waiting for pickup?
4. On load, disconnect, external UI edit or failed parameter resolution,
   which values can be confirmed and what can still play? A successful MIDI
   send or plugin write request is not confirmation of an audible/visible
   result.
5. Recreate the layout on a second device with fewer controls and no
   colored lights. Which musical meanings remain identical? Where does a
   player find the omitted controls?

## What to send back

- **Instrument developer:** a ranked control table, two or three playing
  workflows, supported state/feedback behavior, variant initialization,
  compatibility guarantees, unknowns and a proposed minimal integration.
- **Surface developer:** a mode/port map, capability and feedback table,
  verified capture/output evidence, reserved gestures and limitations.
- **Joint proposal:** one layout on the capable device, one reduced layout,
  an explicit failure path, and a test sequence (synthetic mapping first,
  real instrument and hardware afterward). Identify any host changes it
  needs rather than describing them as already implemented.

The owner has not yet ruled on default absolute-fader takeover or who may
reassign existing system gestures; see the
[design's open questions](./design/instrument-control-surface.md#7-open-questions).
Developers may recommend answers, but no integration profile silently
settles those decisions.

## Worked example only: Potato Keys and Launchkey MK4 61

A Linux Potato Keys build inspected on 2026-09-29 reported 40 CLAP
parameters for `com.littlepotato.keys`: nine 0–8 drawbars, Expression
0–100, Rotary 0–3 and Engine 0–2. `Drive`, `Tone` and `Output Level` occur
in more than one module, so names alone cannot select their targets. Earlier
exploration suggests Engine 0/1/2 correspond to Organ/Tine/Reed; **the plugin
developer needs to confirm that** and report which controls apply to each.
Distinct named patches can hold distinct CLAP state, but a new unseeded
patch loads the plugin's default Organ regardless of its name. Initial
Tine/Reed state is therefore a real design problem, not a filename trick.

The Launchkey example has eight relative encoders, nine absolute faders and
buttons, 16 RGB pads, and a two-line ASCII display capped at 16 bytes per
line. Polyclav keeps DAW pads, Plugin encoders and Volume faders as the
supported hardware layout. Top-row pads select patches; bottom-row pads
currently indicate five native-synth pages. CLAP patches have only the MAIN
encoder page today. Organ's current faders and Leslie behavior are explicit
patch opt-ins; a piano or Reed patch must not inherit them. The current
screen uses short parameter/status popups and restores the patch name after
800 ms. [Organ behavior](./USER_GUIDE.md#launchkey-organ-drawbars) and the
[device test plan](./HARDWARE_TESTS.md) distinguish code from physical
verification.

This example can pressure-test the proposed contract. **It is not the
contract's field count, required plugin API, controller layout, or default
mapping for another instrument.**
