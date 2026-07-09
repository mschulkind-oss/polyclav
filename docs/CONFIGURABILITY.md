# Configurability & Hardware Abstraction

> **Status (2026-07-06):** design notes; Tiers 0–1 shipped 2026-07-05
> (`[osc.mixer]` as the preferred name for `[osc.xr18]`, plus the
> configurable `heartbeat` — unset → `/xinfo`, `""` → fire-and-forget
> for generic OSC targets). Tiers 2–4 remain design. Since then the
> control-surface seam has grown *deeper* Launchkey coupling by design:
> the knob-page state machine (`internal/controls/pages`) mutates only
> through the controls layer — the Tier-3 groundwork — but its
> screen/pad adapters are MK4-shaped. This document describes
> where polyclav is currently hardwired to a **Novation Launchkey 61 MK4**
> control surface and a **Behringer XR18** OSC mixer, and lays out options
> for making both pluggable. The native-synth/UX work in `docs/ROADMAP.md`
> is orthogonal and (deliberately) leans *into* Launchkey specifics; this
> doc is the counterweight — how to keep that UX while letting other
> hardware work too.

---

## TL;DR

polyclav already has a real config file (`polyclav.toml`) and three of its
four hardware seams are at least partly configurable. The honest picture:

| Seam | How coupled today | Configurable now? |
|------|-------------------|-------------------|
| **Audio out** (PipeWire sink) | Not coupled — uses the default sink | ✅ Fully generic (any PipeWire device) |
| **MIDI keyboard in** | `internal/midi.Multiplexer` reads every connected keyboard by default; `port_match` is an optional restriction, no longer coupled to Launchkey detection | ✅ Fully generic — any class-compliant keyboard(s), simultaneously |
| **OSC mixer out** | Arbitrary `[[osc.xr18.bindings]]` (already generic mappings); **but** XR18 naming, default port, and `/xinfo` heartbeat are X-Air-specific | 🟡 Bindings generic; discovery/naming hardwired |
| **Control surface** (knobs/pads/screen/transport) | A dedicated `internal/launchkey` driver speaking MK4 SysEx + fixed CC/note maps, plus hardcoded knob→function and pad→patch logic in `main.go` | 🔴 Effectively Launchkey-only |

So "generalize it" is really two different sizes of job:

- **OSC mixer & MIDI input** — small/medium. Mostly renaming, a config
  schema tweak, and relaxing one or two baked-in assumptions.
- **Control surface** — the big one. The Launchkey is woven into the driver
  layer *and* the daemon's event-handling logic. Making it pluggable means
  introducing a control-surface abstraction.

---

## 1. Current state, seam by seam

### 1.1 Audio out — already generic ✅

The Rust `audio-core` opens whatever PipeWire hands it as the default sink;
there is no XR18 reference in the audio path. The only "XR18" mention is a
WirePlumber latency tuning *rule* documented in `docs/INSTALL.md`, which is
advisory, not code. One latent assumption worth noting: the engine runs at a
hardcoded **48 kHz** (`audio-core/src/lib.rs:39`
`const SAMPLE_RATE: f32 = 48000.0;`, echoed in
`audio-core/src/dsp/compressor.rs:1`). That's a generic-audio concern, not a
device-coupling one, but it would bite anyone running a 44.1 kHz interface.

**Verdict:** no work needed for device-genericness; flag the sample-rate
constant separately.

### 1.2 MIDI keyboard in — generic multi-device note input ✅

Configurable surface:

- `internal/config/config.go` — `MIDIConfig.PortMatch` (`[midi].port_match`),
  default `""`. Empty = every connected keyboard sends notes
  (`internal/midi.Multiplexer` opens every present input port except ones
  that look DAW-role); a non-empty substring restricts to matching port(s)
  instead, bypassing the DAW-role exclusion (an explicit ask is trusted).

Fixed as of 2026-07-09: note input and Launchkey detection used to be one
coupled `port_match` string — a non-Launchkey keyboard produced zero notes
until you retargeted `port_match`, and doing so lost Launchkey detection
even if one was also plugged in. They're now fully independent:

- `internal/midi.Multiplexer` (new) is a hotplug reconciler for *every*
  currently-present port at once, each with its own listener goroutine —
  unplugging one keyboard doesn't affect others. `looksLikeDAWPort`
  (`internal/midi/midi.go`) is the only Launchkey-shaped assumption left in
  this half, and it's just an exclusion heuristic for the default case, not
  a requirement.
- `internal/launchkey.Reconciler` no longer opens `midi.Listen` at all — it
  owns only the DAW control-surface half (`driver.Open`), auto-detected on
  its own fixed, non-configurable `"launchkey"` match
  (`internal/launchkey/reconciler.go`'s `launchkeyMatch` constant).

Added since: per-device exclusion, so `port_match`'s all-or-nothing
substring restriction isn't the only knob. `MIDIConfig.IgnoreDevices`
(`[midi].ignore_devices`) is a denylist of exact port names, layered on top
of Match/the DAW exclusion — deliberately opt-out, not opt-in, so a newly
plugged-in keyboard never needs to be added anywhere first. Three surfaces
edit the same list: the config field itself, `--midi-ignore` (CLI, one-off,
override-not-merge like `--web`), and the web UI's MIDI devices panel
(`GET`/`PUT /api/midi/devices`, live via `Multiplexer.SetIgnore` +
optional config-file save — same contract as the velocity editor).
`polyclav midi list` prints every connected port with its live
classification (`internal/midi.ClassifyPorts`, shared with the web GET
handler so the two surfaces can't disagree) so there's never any guessing
at exact names for either mechanism.

One transport-level assumption, separate from any device: all MIDI I/O goes
through **rtmidi/ALSA-seq** (`gitlab.com/gomidi/midi/v2/drivers/rtmididrv`).
There's no PipeWire-native MIDI path; hotplug detection works by polling
ALSA port names once a second. Fine on any modern Linux, but it's a seam to
keep in mind if PipeWire MIDI ever becomes the target.

**Verdict:** done. The remaining Launchkey coupling is entirely in the
control-surface story (§1.4), not note input.

### 1.3 OSC mixer out — generic bindings, X-Air-specific everything-else 🟡

This seam is in better shape than its naming suggests. The mapping engine is
already device-agnostic:

- `internal/osc/binding.go` — a `Binding` is just
  `{source_kind, channel, controller, osc, transform}`. The `osc` field is an
  **arbitrary OSC address string**; nothing about it is XR18-specific.
- `internal/osc/mapper.go` — `Mapper.Dispatch` does an O(1) lookup keyed by
  `(kind, channel, controller)` and applies `scalar` (→ float 0..1) or
  `press` (→ int 0/1) transforms. Fully generic.

What *is* X-Air/XR18-specific:

- **Config namespace.** `[osc.xr18]` and the Go types `XR18Config`,
  `OSCConfig{XR18 ...}` (`internal/config/config.go:48-54`). A second mixer
  or a non-Behringer OSC target has no place to live.
- **Default port 10024** (`config.go` `Defaults()`), the X-Air family default.
- **Reachability heartbeat.** `internal/osc/reconciler.go` pings a hardcoded
  **`/xinfo`** packet (`xinfoPacket`, line ~70) and treats a reply as "mixer
  present." `/xinfo` is a Behringer X-Air/X32 command; a generic OSC device
  won't answer it, so the reconciler would peg `absent` and silently swallow
  every send (`Send` is a no-op while absent — `reconciler.go` `Send`).
- **Naming/logging** throughout (`xr18 reachable`, `xr18 absent`, etc.).

**Verdict:** the hard part (mapping) is done. Generalizing is mostly a
schema rename plus making discovery/heartbeat optional or configurable.

### 1.4 Control surface — Launchkey through and through 🔴

This is the real coupling. Two layers:

**(a) The driver** — `internal/launchkey/`:

- `driver/driver.go` hardcodes the MK4 control map: knob CC base `0x55`
  (relative mode), fader CC base `5`, fader-button base `37`, and a full
  table of transport note numbers (`0x66`–`0x76`), plus DAW-mode handshake
  notes (`noteDAWModeOn = 0x0C`, `noteFeatureCtrl = 0x0B`).
- `driver/screen.go`, `driver/pads.go`, `components/` — MK4 SysEx for the
  LCD, RGB pads, and the Components palette (named colors referenced by
  `pad_color` indices in config). Every payload is framed with the
  Novation/MK4 SysEx header `F0 00 20 29 02 14`
  (`driver/sysex.go` `mk4SysExHeader`).
- `reconciler.go` opens the joint MIDI+DAW connection and exposes
  `SetPadColor`, `SetDisplayText`, `SetTitle` — a Launchkey-shaped API.
- `internal/supervisor/supervisor.go:17-19` — `supervisor.Config` names its
  two children `Launchkey` and `XR18` outright; any surface abstraction
  renames/generalizes these fields too.
- The standalone `polyclav-components` CLI is MK4-only by design (Custom
  mode SysEx encoding): product variants `launchkey25/37/49/61_mk4`
  (`cmd/polyclav-components/main.go:239`) and a default port match of
  `"Launchkey"` (`main.go:363`). Fine to leave device-specific — just keep
  it out of the daemon's abstraction.

**(b) The daemon's event logic** — `cmd/polyclav/main.go`:

- `knobLabels := map[int]string{1:"Volume", 2:"Reverb", 3:"Comp", 4:"Cutoff"}`
  (`main.go:214`) — knob→function map is a literal.
- `onDAWEvent` (`main.go:235-318`) hardwires: knob 1→volume, 2→reverb,
  3→compressor, 4→native cutoff; **pad row 0 → patch select**
  (`main.go:296-316`); the 800 ms screen-restore-to-patch-name behavior; the
  8-pad cap (`pushPadColors`, `main.go:181-196`).
- `[[patches]].pad_color` and `display` in config are Launchkey concepts
  (RGB pad index, LCD line).

There is **no abstraction** between "a knob turned" and "set master volume."
The daemon consumes `driver.Event` (`KnobEvent`, `PadEvent`, …) types
directly. Swap the hardware and all of this logic has to be re-pointed.

**Verdict:** needs a real control-surface interface to become pluggable.

---

## 2. Ideas for generalization

Ordered roughly by effort / payoff. Each tier is independently shippable.

### Tier 0 — Honest naming & docs (tiny)

Rename `[osc.xr18]` → `[osc.mixer]` (keep `xr18` as a deprecated alias in the
TOML decoder for one release), and the Go types to match. Document the
already-generic binding model as "drive *any* OSC target," with the XR18
fader/mute addresses as one worked example. Zero behavior change; clarifies
that the OSC layer was never really XR18-locked.

### Tier 1 — Make the OSC mixer fully device-agnostic (small)

1. **Generalize the config block** to a named, optionally-repeated mixer:
   ```toml
   [[osc.mixer]]
   name = "xr18"
   host = "192.0.2.6"
   port = 10024
   heartbeat = "/xinfo"     # omit/"" to disable presence polling
   [[osc.mixer.bindings]]
   ...
   ```
2. **Make the heartbeat configurable or optional.** Today `/xinfo` is the
   only liveness probe. Allow `heartbeat = ""` → skip polling and treat the
   target as always-present (fire-and-forget UDP). This alone unlocks any
   OSC device (lighting desks, Reaper, TouchOSC, other DAWs).
3. **Multiple targets** (optional): let bindings fan out to more than one
   mixer. Useful but not required for v1.

Risk: low. The mapper and bindings need no changes.

### Tier 2 — Decouple MIDI input from the Launchkey topology (small-medium)

1. Add `[midi].dual_port = true|false` (or auto-detect: if only one matching
   port exists, treat it as both note + control source). Default keeps the
   Launchkey behavior.
2. For single-port keyboards, route control CCs through the **same** binding
   machinery the OSC mixer uses (see Tier 3), rather than the Launchkey DAW
   driver. This is the bridge into the control-surface abstraction.

### Tier 3 — A control-surface abstraction (the big one)

The goal: keep the rich Launchkey UX as *one implementation* of a generic
interface, and let a plain MIDI keyboard (or any controller) provide a
degraded-but-functional surface via config-driven CC/note bindings.

**Proposed shape:**

- Define a `controlsurface.Surface` interface with two halves:
  - **Inputs** (controller → daemon): a normalized event stream —
    `Knob{id, delta|abs}`, `Pad{id, pressed}`, `Transport{button}` — that the
    daemon maps to *actions* (`SetVolume`, `SelectPatch(i)`, …) via a
    declarative table, not a literal `switch`.
  - **Feedback** (daemon → controller): `SetPadColor`, `SetDisplay`,
    `SetTitle` — all **optional / no-op** so a feedback-less controller just
    silently skips them (the reconciler already no-ops when absent; mirror
    that pattern).
- Two implementations to start:
  - `launchkey` — wraps the existing driver; the SysEx/color/screen richness
    lives here unchanged.
  - `generic-midi` — driven entirely by config bindings (reuse the
    `osc.Binding` idea: `{source_kind, channel, controller} → action`). No
    SysEx, no screen, no RGB; pads-as-patch-select still works if the user
    maps note numbers to `select_patch` actions.
- **Action table in config** instead of hardcoded `knobLabels` / pad logic:
  ```toml
  [controls]
  surface = "launchkey"     # or "generic"

  [[controls.bindings]]
  source = "knob:1"
  action = "master_volume"

  [[controls.bindings]]
  source = "knob:2"
  action = "reverb"

  [[controls.bindings]]
  source = "pad:0:*"        # any pad in row 0
  action = "select_patch"
  arg    = "by_position"
  ```
  The Launchkey surface ships with these as built-in defaults so existing
  users see no change.

**What moves where:**

- `cmd/polyclav/main.go` `onDAWEvent`/`knobLabels`/pad logic → a small
  `controls` package that resolves `event → action` from the table.
- `internal/launchkey` stays, now satisfying `controlsurface.Surface`.
- `[[patches]].pad_color` / `display` become surface-specific hints the
  generic surface ignores.

Risk: medium-high; this is a genuine refactor with test surface
(`reconciler_test.go`, `mapper_test.go`, driver tests). Do it behind the
interface so the Launchkey path is provably unchanged.

### Tier 4 — Profiles (polish)

Once Tiers 1–3 land, ship **device profiles**: a single
`controller = "launchkey-61-mk4"` (or `"akai-mpk-mini"`, `"generic"`) that
selects a bundled defaults file (CC/note maps + action bindings), so users
of supported hardware don't hand-write bindings. Community profiles become a
contribution path.

---

## 3. Recommended sequencing

1. **Tier 0 + Tier 1** together — high clarity, low risk, immediately makes
   polyclav usable with non-Behringer OSC gear. (~1 small PR.)
2. **Tier 2** — single-port keyboard support; modest and user-visible.
3. **Tier 3** — the control-surface interface. Schedule alongside or after
   the Phase 2 Launchkey-UX work in `docs/ROADMAP.md` so the two don't fight
   over `main.go`'s event-handling code. Build the interface, port the
   Launchkey driver onto it with **zero behavior change**, *then* add the
   generic surface.
4. **Tier 4** — profiles, once the abstraction has proven itself with two
   real implementations.

## 4. Open questions

- **Feedback-less degradation:** when the surface has no screen/LEDs, where
  does patch/knob state surface instead? (stdout log line? a `--tui`? an OSC
  feedback channel back to a TouchOSC layout?)
- **Relative vs. absolute knobs:** the Launchkey runs encoders in *relative*
  mode (`driver.go` `ccKnobBase = 0x55`). Generic controllers usually send
  *absolute* 0–127. The action layer needs to handle both (a `mode` per
  binding, or auto-detect from value behavior).
- **Patch selection without 8 lit pads:** the 8-pad cap and `pad_color` are
  Launchkey-isms. For generic surfaces, do we expose patch-select via any
  mappable note range, program-change messages, or a separate mechanism?
- **Should `osc.mixer` and `controls` share one binding engine?** They're
  structurally identical (MIDI event → mapped output). Unifying them reduces
  code but couples two configs that may want to diverge.
