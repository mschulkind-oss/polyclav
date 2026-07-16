# Pedalboard UI: Web + Launchkey Control for the Post-Synth Chain

> **Status (2026-07-15):** Phase 1 **shipped**, plus extras — the Flat
> Modern pedalboard is now the daemon's main web UI (`web/app/page.tsx`,
> replacing the card dashboard). Built: the chain registry +
> plumbing for chorus/tremolo/delay params & per-pedal enables
> (`internal/controls/chain.go`, Go-only — no Rust change), per-patch
> persistence, `GET`/`PATCH /api/chain`, the SSE `chain` type, and the
> web pedalboard + generic pedal editor consuming them
> (`web/lib/pedalboard/wiring.ts`). Beyond the original Phase-1 scope:
> the post-synth **compressor and reverb** graduated from the old bus
> card into their own pedals, and pedals gained a drag/keyboard
> **reorder** bar (display order only — the DSP path stays fixed in
> Rust; `docs/mockups/pedalboard-style-b-flat-modern.html` is the
> art-direction reference). Still open: Phase 2 (Launchkey Categories ×
> Pages, `nav`/`hwmap` sync) and Phase 3 (macros). Original motivation:
> three of the four pedals (`docs/VISION.md` §1b/1c/1d) had shipped with
> **zero interface** — chorus, tremolo, and the analog delay existed in
> the Rust DSP chain with working FFI and Go wrappers, and nothing
> called them.

## The problem

The post-synth chain (`audio-core/src/lib.rs` `render_block`,
`:2235-2398`) is now a real pedalboard:

```
synth → DRIVE → CHORUS → TREMOLO → DELAY → patch gain → comp → reverb
      → mastering comp → limiter → master volume
```

Of the four pedals, only DRIVE is reachable: MAIN knob 4, the
`drive_pedal` field on `PATCH /api/params`, per-patch persistence in
`state.toml`. The other eight parameters (chorus rate/depth/mix,
tremolo rate/depth, delay time/feedback/mix) are set-at-boot defaults
forever. There are three distinct gaps, and patching any one of them
alone reproduces the mess:

1. **No plumbing.** `controls.Audio` (`internal/controls/controls.go:19-41`)
   declares `SetDrivePedal` and nothing else pedal-shaped. The eight
   orphan params need the standard clamp → atomics → `state.toml` →
   hub-publish path that every other live param already follows.
2. **No metadata.** Ranges, defaults, units, and tapers are
   hand-triplicated today (Rust `DspParams` setters, Go `controls.go`
   constants, `pages/defs.go` steps). Every surface we add — web
   sliders, Launchkey pages, macros — re-transcribes the same table by
   hand. That's the actual reason the delay shipped controls-less: each
   new param costs edits in six files.
3. **No navigation.** MAIN's 8 knobs are full; the Categories × Pages
   model (`docs/LAUNCHKEY_NAVIGATION.md`) that would give the pedals a
   home is designed but unbuilt; and the web dashboard has no concept
   of the chain at all (it doesn't even render the drive pedal it
   already has API access to — `components/ParamsCard.tsx` has no
   pedal slider).

## Design stance: dictate, then flex

Explicit scope decision, stated up front: **v1 dictates every
mapping.** There is no custom-map editor, no drag-a-CC-onto-a-param
screen, no per-user page layouts. The Launchkey layout is fixed data
we ship; the web layout mirrors it. The **one** deliberately flexible
surface is the **macro system**: 8 knob slots the user assigns to any
chain parameter from the web UI. Everything else earns flexibility
later, if ever.

What we still owe the user under that stance — and what this doc
specifies:

- **Discovery** — you can always see, in the browser and on the
  device, what exists, what it's set to, and which knob touches it.
  Machine-readable (`GET /api/chain`, `GET /api/hwmap`) and human
  (the Hardware Map page, screen hints).
- **Configuring signal chains** — per-pedal enable, per-pedal params,
  persisted per patch, editable from browser and hardware. (Reordering
  the chain is explicitly *not* v1 — see Open Questions.)
- **Assigning macros to knobs** — the flex escape valve.
- **Navigating to direct edit** — one click/press from "I can see it"
  to "I'm editing it", in both directions: web ↔ hardware.

This is the same philosophy as `docs/CONFIGURABILITY.md` Tier 3's
sequencing ("build the interface, port the Launchkey with zero
behavior change, *then* add the generic surface") applied one level
up: the dictated tables proposed here become the built-in defaults of
whatever binding engine eventually exists. Nothing here blocks Tier 3;
most of it is Tier 3's future seed data.

## Piece 1 — the chain registry (single source of truth)

A declarative table of the post-synth chain, one place, driving
everything else. Lives in `internal/controls` (new file `chain.go`)
because the setters it references live there; exported read-only to
`internal/web` and `internal/controls/pages`.

```go
type ChainParam struct {
    ID      string  // "chorus.rate_hz" — wire id, macro target, state key
    Label   string  // "Rate" — web + screen (≤14 chars fits the LCD)
    Unit    string  // "Hz", "ms", "" (unitless 0-1 renders as %)
    Min, Max, Default float32
    Taper   Taper   // Linear | Exp — one enum, both UIs honor it
    Step    float32 // per encoder tick, in normalized [0,1] position
    Gate    bool    // true when 0.0 == the stage's bit-exact bypass
}

type ChainStage struct {
    ID     string       // "chorus"
    Label  string       // "Chorus"
    Kind   StageKind    // Pedal | Bus
    Params []ChainParam
}
```

The registry rows, transcribed **once** from the authoritative Rust
clamps (`DspParams`, `lib.rs:392-453, 620-766`):

| Stage | Param ID | Label | Range | Default | Unit | Taper | Gate |
|---|---|---|---|---|---|---|---|
| drive | `drive.amount` | Drive | 0 – 1 | 0.0 | % | linear | ✓ |
| chorus | `chorus.rate_hz` | Rate | 0.02 – 5 | 0.8 | Hz | exp | |
| chorus | `chorus.depth` | Depth | 0 – 1 | 0.0 | % | linear | |
| chorus | `chorus.mix` | Mix | 0 – 1 | 0.0 | % | linear | ✓ |
| tremolo | `tremolo.rate_hz` | Rate | 0.05 – 20 | 4.0 | Hz | exp | |
| tremolo | `tremolo.depth` | Depth | 0 – 1 | 0.0 | % | linear | ✓ |
| delay | `delay.time_ms` | Time | 1 – 1000 | 300 | ms | exp | |
| delay | `delay.feedback` | Feedback | 0 – 0.9 | 0.0 | % | linear | |
| delay | `delay.mix` | Mix | 0 – 1 | 0.0 | % | linear | ✓ |

(Tremolo has no mix by design — `depth` is both character and gate;
see `audio-core/src/dsp/tremolo.rs:1-29`. The bus params —
volume/reverb/comp/mastering comp/limiter ceiling — join the registry
too with `Kind: Bus`, so macros can target them; their existing wire
fields and state keys stay untouched.)

**What the registry drives:**

1. `GET /api/chain` — discovery: schema *and* current values in one
   response. A UI that renders from it never goes stale when a pedal
   grows a knob.
2. `PATCH /api/chain` — one generic handler validating against
   registry ranges, instead of a hand-written field per param.
3. Per-patch persistence keys in `state.toml` (param ID with `.`
   → `_`, e.g. `chorus_rate_hz`) — same `state.Knob` mechanism as
   `drive_pedal` today.
4. The Launchkey FX pages (Piece 3) — `PageDef`s generated from
   registry rows instead of hand-written `Slot` literals.
5. Macro target enumeration (Piece 4) — the assignment dropdown is
   `registry.Params()`, no separate list.
6. Screen value formatting — one formatter keyed on `Unit`/`Taper`
   shared by the 800 ms knob popup and the web readouts.

The existing `drive_pedal` plumbing keeps working: registry ID
`drive.amount` maps onto the existing state key/wire field as an alias
(Open Question 5).

## Piece 2 — plumbing the orphan pedals + enable flags

Standard wiring, one method per param, copying the `drive_pedal`
pattern exactly (`controls.go:514-536` `setKnob`: clamp → apply →
`UpdatePatchKnob` → publish):

- `controls.Audio` interface + `audioBackend` adapter
  (`cmd/polyclav/main.go:666-698`): add the eight `Set*` passthroughs
  to the already-existing `internal/audio` wrappers.
- `Controls`: `SetChainParam(id string, v float32)` /
  `AdjustChainParam(id string, delta float32)` — registry-generic
  instead of eight bespoke setter pairs; internally dispatches to the
  right audio call via a `map[string]func` built from the registry.
- `state.Knob`: eight new optional fields (`chorus_rate_hz`, …,
  `delay_mix`), restored in `afterSelect` alongside
  volume/reverb/comp/drive (`controls.go:1559-1606`).
- SSE: new `chain` change type, `{"field":"chorus.depth","value":0.4,
  "patch":"Rhodes"}` — same shape as today's `params` events.

**Per-pedal enable (the stomp switch).** The engine's bypass contract
is "gate param == 0 → bit-exact bypass" — which means naively zeroing
`mix` to turn a pedal off *destroys the setting*. Fix at the controls
layer, no Rust change: a per-patch, per-pedal `enabled` bool
(default true). Effective value pushed to the atomics is
`enabled ? gate_value : 0.0`; the stored gate value survives. LED/UI
state for "this pedal is audible" is `enabled && gate > 0`.

- state keys: `drive_enabled`, `chorus_enabled`, `tremolo_enabled`,
  `delay_enabled` (per patch, default true — absent key means true, so
  existing state files are untouched).
- wire: same `PATCH /api/chain` body, boolean values on stage-scoped
  ids: `{"chorus.enabled": false}`.
- Publishes on the same `chain` SSE type.

This gives true pedalboard stomp behavior everywhere the API reaches,
without inventing engine state.

## Piece 3 — web UI

Four surfaces, all mocked in `docs/mockups/pedalboard-ui.html` (tab
per screen). All rendering is registry-driven — components consume
`GET /api/chain` + SSE, no hardcoded param lists. Conventions per the
shipped app: snake_case wire JSON, `SliderRow` debounce/drag-guard
reuse, `globals.css` tokens, `section.wide` for full-width panels.

### 3a. Pedalboard strip (dashboard, new full-width section)

The signal chain as a horizontal pedalboard, one card per pedal in
chain order, with the bus stages compressed into a trailing strip:

- Per pedal: name, stomp LED (green = enabled && gate > 0, off =
  bypassed), the 2–3 param values as mini readouts, stomp button.
- Click anywhere on a pedal → opens the **pedal editor** (3b).
- Signal-flow arrows between cards; the fixed order *is* the
  documentation of the chain.
- A "Follow hardware" toggle in the section header (see 3d).
- Live: SSE `chain` events tick the readouts when the Launchkey (or
  another browser) turns a knob; the changed value flashes.

This replaces nothing — `ParamsCard` keeps volume/reverb/comp/cutoff.
(While we're here: `ParamsCard` finally grows the missing
`drive_pedal` slider, or drops it in favor of this strip — Open
Question 6.)

### 3b. Pedal editor (direct edit)

A focused panel for one pedal (modal-or-inline; mockup shows inline
expansion below the strip):

- Full-width sliders per param — registry label, unit-formatted value
  (`480 ms`, `0.8 Hz`, `35%`), taper-aware track (exp params get a
  log-scaled slider so the useful range isn't crushed into the left
  edge).
- Stomp toggle + "reset to defaults" (registry defaults).
- **Hardware hint line**: "Launchkey: FX ▸ DELAY — K1 Time · K2
  Feedback · K3 Mix", rendered from `GET /api/hwmap` so it can never
  disagree with the device.
- **"Edit on hardware"** button → `POST /api/nav
  {"category":"FX","page":"DELAY"}` — the Launchkey jumps to that
  page and flashes its category/page banner. One press of physical
  intent from a browser click.

### 3c. Macros card

Eight slots, mirroring the MACROS knob page 1:1 (slot N ≡ knob N):

- Per slot: assignment dropdown (registry params, grouped by stage),
  optional custom name (what the LCD shows, ≤14 chars), min/max range
  fields (defaulting to the param's full range), live value arc.
- Unassigned slots render as "—" both here and on the device screen.
- Persisted via `PUT /api/macros`; applied live (no restart).

### 3d. Hardware Map page (`/app/hardware`) — the discovery surface

A visual Launchkey (knob row, screen, pads, transport) plus a
category/page browser:

- Renders entirely from `GET /api/hwmap`: every category, page, and
  knob assignment as dictated data — the self-updating manual.
- **Live**: SSE `nav` events highlight the category/page the device
  is actually on; SSE `chain` events flash the knob that just moved.
  Turn a physical knob and watch the map point at it.
- **Click a knob → jump to direct edit**: navigates to the pedal
  editor (or synth/mix card) that owns that param. This is
  "discovery → edit" in one click, the web-side mirror of "Edit on
  hardware".
- Follow mode (3a's toggle) is this page's inverse: hardware nav
  drives web nav.

### API additions (summary)

| Method & path | Purpose |
|---|---|
| `GET /api/chain` | Registry schema + current values + enable flags — the discovery endpoint |
| `PATCH /api/chain` | Body `{"<param-id>": number \| bool, ...}`; validates against registry; 409 if no patch selected (matches `/api/params`) |
| `GET /api/macros` / `PUT /api/macros` | The 8 slots: `[{slot, target, name?, min?, max?}]` |
| `GET /api/hwmap` | Dictated surface map: categories → pages → knob slots (+ pads, transport) with param ids |
| `POST /api/nav` | `{"category": "...", "page": "..."}` — point the Launchkey at a page |
| SSE `chain` | `{field, value, patch}` on any chain param/enable change |
| SSE `macros` | Full slot list on assignment change |
| SSE `nav` | `{category, page, page_label}` on device page/category switch |

## Piece 4 — Launchkey mapping (dictated)

**Prerequisite: build `docs/LAUNCHKEY_NAVIGATION.md`'s Categories ×
Pages as specced there** (Track ←/→ = category, Scene ↑/↓ = page
within category, skip-unavailable-and-wrap, 800 ms banners, pad row 1
cols 0–4 as per-category page indicators). This doc adds the concrete
FX page dictation it deliberately left open (its Open Question 5) and
one new category. Category ring:

```
VOICE  →  FX  →  MIX  →  MACROS  →  (wrap)
(native   (all    (all     (all
 only)    patches) patches) patches)
```

### FX category — one page per pedal, chain order

Page label = pedal name = web card name: discovery by naming
consistency. Generated from the registry, not hand-written slots.

| Page | K1 | K2 | K3 | K4–K8 |
|---|---|---|---|---|
| 1 DRIVE | Drive | — | — | — (future: tone stack, `docs/VISION.md` §1) |
| 2 CHORUS | Rate | Depth | Mix | — |
| 3 TREM | Rate | Depth | — | — |
| 4 DELAY | Time | Feedback | Mix | — (future: feedback-loop tone) |

Four pages fits the 5-LED indicator cap with a slot to spare.
Alternative considered and rejected: packing chorus+tremolo onto one
MOD-FX page (5 knobs) — saves a page we don't need to save, breaks
the page-label ≡ pedal-name discovery property, and leaves no growth
room per pedal.

Knob behavior: existing relative-encoder path (`pages.HandleKnob` →
`AdjustChainParam(id, delta)`), step and taper from the registry
(exp-taper params adjust in normalized position, so a detent moves
delay time by a constant *ratio*, not a constant ms — matches how the
existing Cutoff knob feels). Value popup: existing 800 ms
`SetDisplayText` pattern, formatted by the registry formatter
("Time" / "480 ms").

### MIX category — as proposed in LAUNCHKEY_NAVIGATION.md

Unchanged from that doc: Volume / Reverb / Comp / Pedal /
Mastering Comp / Limiter Ceiling / — / —, including its flagged
`AdjustMasteringComp`/`AdjustLimiterCeiling` gap (which
`AdjustChainParam` now covers generically — the registry's bus rows
close that gap for free).

### MACROS category — the one flexible surface

One page, 8 knobs = the 8 web-assigned slots.

- Knob N adjusts slot N's target param, scaled to the slot's
  min–max range in the target's taper space (a macro over
  `delay.time_ms` sweeps ratios, not milliseconds).
- Screen popup: slot name (or target label if unnamed) + formatted
  value — same 800 ms mechanism.
- Unassigned slot: popup "Macro N —", no action.
- Assignment editing is **web-only** by design (v1 dictation stance:
  the device performs, the browser configures). SSE `macros` keeps a
  device-side cache fresh; no reboot, next knob turn uses the new
  target.

### What stays untouched

Top pad row = patch select; bottom row cols 5–7 reserved for OSC
bindings; faders stay XR18/unused (Open Question 4); transport map
unchanged except Track ←/→ coming alive as category switch; VOICE
category = today's five pages verbatim, native-gated as today.

## Piece 5 — persistence

Per-patch pedal state extends `state.Knob` (flat keys, exactly like
`drive_pedal` — nested tables rejected to keep the
one-struct-one-table state format and zero migration):

```toml
[patch."Rhodes Mk I"]
volume = 0.8
drive_pedal = 0.15
chorus_rate_hz = 0.9
chorus_depth = 0.45
chorus_mix = 0.3
chorus_enabled = true
tremolo_rate_hz = 5.2
tremolo_depth = 0.0
delay_time_ms = 380.0
delay_feedback = 0.35
delay_mix = 0.25
delay_enabled = false   # parked: settings survive the stomp
```

Macro slots are global (not per-patch — Open Question 2), also in
`state.toml` (they're runtime-editable user state, not structural
config; the config-file managed-block pattern stays for things that
need a restart):

```toml
[[macros]]
slot = 1
target = "delay.mix"
name = "Echo"
min = 0.0
max = 0.6
```

## Phasing

| Phase | Ships | User-visible win |
|---|---|---|
| **1 — registry + plumbing + web pedalboard** | `chain.go` registry, 8 params + 4 enables wired end-to-end, `GET/PATCH /api/chain`, SSE `chain`, pedalboard strip + pedal editors, per-patch persistence | Chorus/tremolo/delay controllable **at all**, from any browser; settings stick per patch |
| **2 — Launchkey categories + FX/MIX pages + nav sync** | Categories × Pages build-out, registry-generated FX pages, MIX page, `nav` SSE + `POST /api/nav` + `GET /api/hwmap`, Hardware Map page, follow mode | Pedals playable from the device; web ↔ hardware jump in both directions |
| **3 — macros** | Slot storage, `AdjustMacro` scaling, MACROS category, macros card | User-shaped performance page |
| **later / not scheduled** | chain reorder, multi-target macros, pad stomps, custom mapping UI, Tier 3 generic surface | — |

Phase 1 stands alone and kills the immediate pain (three pedals with
no interface). Phase 2 depends on 1; 3 depends on 1 (not on 2 for the
web half, but the MACROS category needs 2's category machinery).

## Open questions

### 1. Delay time per-patch, or global?

Every other pedal param is obviously per-patch (a patch is a sound).
Delay *time* is arguably a performance/tempo property you'd want to
survive patch switches — and a future tempo-sync feature would make it
global-ish anyway.

_Leaning:_ per-patch like everything else, for consistency and
because tempo-sync doesn't exist; revisit only when it does.

**Answer:**
> _(empty — fill in when decided)_

### 2. Macro assignments global or per-patch?

Ableton-style racks scope macros per patch; a global performance page
is simpler (one table, no restore-on-select interplay) and matches the
"MACROS is its own category" mental model.

_Leaning:_ global for v1 — per-patch macros double the state surface
for a benefit nobody has asked for yet, and migrating global → per-patch
later is mechanical.

**Answer:**
> _(empty — fill in when decided)_

### 3. Pad stomp switches in the FX category?

Pedalboard metaphor says pads should stomp pedals. But bottom-row
cols 0–4 are page indicators, 5–7 are reserved for OSC bindings, and
the top row is patch select — a stomp row would overload existing
semantics (e.g. "pressing the active page's indicator toggles that
pedal's enable").

_Leaning:_ defer. Web stomp + knob-to-zero covers bypass; overloading
indicator pads is the kind of cleverness that costs more confusion
than it saves. Reconsider if/when pads get a mode system.

**Answer:**
> _(empty — fill in when decided)_

### 4. Faders (49/61 SKU) as chain controls?

Nine faders + 8 fader buttons sit completely unused by the daemon
(parsed, never dispatched). They're an obvious continuous-control
surface — e.g. faders 1–4 as pedal gates.

_Leaning:_ no for v1 — faders are spoken for in spirit by the XR18
raw-CC binding path, and adding a third control surface mid-design
muddies the dictation. Note it as future MACROS-like territory.

**Answer:**
> _(empty — fill in when decided)_

### 5. Registry ID vs. legacy `drive_pedal` naming?

The registry calls it `drive.amount`; the wire field
(`PATCH /api/params`), state key, and SSE events say `drive_pedal`.

_Leaning:_ keep `drive_pedal` as the state key and `/api/params`
field (zero migration, zero client breakage); the registry row for
`drive.amount` declares it as its storage/wire alias. New params get
clean registry-derived names from day one.

**Answer:**
> _(empty — fill in when decided)_

### 6. Does `ParamsCard` keep a drive slider once the strip exists?

The dashboard would show drive in two places (ParamsCard row +
pedalboard strip).

_Leaning:_ drop it from `ParamsCard` (it was never actually rendered
there anyway — the strip becomes the pedal's one web home);
volume/reverb/comp/cutoff stay in `ParamsCard` since they're bus/voice
controls, not pedals.

**Answer:**
> _(empty — fill in when decided)_
