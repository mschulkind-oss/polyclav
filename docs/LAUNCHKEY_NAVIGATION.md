# Launchkey Navigation: Categories × Pages

> **Status (2026-07-13):** proposal — design only, nothing here is
> built. Written in response to a concrete, immediate need: the analog
> delay pedal (`docs/VISION.md` §1b) shipped with no hardware control
> at all, because MAIN's 8 knobs are now fully assigned and it needs 3
> params with nowhere to go. This doc proposes a structural fix rather
> than another one-off knob reassignment, since the same problem will
> recur for every future effect (amp emulation, the organ engine, ...).

## The problem

`internal/controls/pages` is a **flat list** of five `PageDef`s (MAIN /
OSC / FILTER / AMP / LFO-MOD), cycled by Scene ↑/↓, with pad row 1
(columns 0–4) showing one indicator LED per page
(`pages.go`'s `PageIndicatorRow`). This has a real, near-term ceiling,
not a hypothetical one:

- **MAIN is full.** Volume / Reverb / Comp / Pedal / Resonance / Glide
  / Drive / unbound — 8 of 8 knobs assigned. The drive pedal already
  used the one flexibility MAIN had (reassigning Cutoff, still
  reachable on FILTER).
- **Pad indicators cap at 5 without a redesign.** `pages.go`'s own doc
  comment: *"Columns 0..len(pages)-1 light up; columns 5..7 are left
  unpainted for future per-page state pads."* Columns 5–7 are already
  spoken for (`docs/CONFIGURABILITY.md`'s OSC/mixer bindings). Page
  count is effectively capped at 5 today.
- **More is coming, not less.** The delay pedal needs a page for 3
  params right now. `docs/VISION.md`'s roadmap already names an amp
  emulation (thread 1) and a tonewheel organ engine (thread 2) —
  the organ alone, modeled on setBfree's stage count (tonewheels,
  drawbars ×9, vibrato/chorus, preamp/overdrive, Leslie), plausibly
  needs multiple pages by itself.

Patching this per-effect (find one more unused knob, reassign
something) is what got the drive pedal onto MAIN — it doesn't scale
past one more effect, and the delay pedal is already past that point.

## Proposed model: Categories × Pages

Add one navigation level above today's pages: **Category**, a small
number of logical groups, each holding its own list of today's
`PageDef`s.

- **Track ← / Track →** switches **category**.
- **Scene ↑ / ↓** stays exactly what it is today: switches **page
  within the current category**.

This isn't a new gesture. `Track ←`/`Track →` are **already fully
decoded** at the driver level —
`internal/launchkey/driver/driver.go`'s `TransportTrackLeft` /
`TransportTrackRight` constants exist, mapped from real SysEx note
numbers (`noteTransportTrackLeft = 0x66`, `noteTransportTrackRight =
0x67`) — but **unmapped** in `cmd/polyclav/main.go`'s
`dispatchTransport`. The existing transport-table comment in
`main.go` says exactly why: *"Track ←/→ → unmapped for now. §2.2
proposed page switching here; pages moved to Scene ↑/↓, keeping Track
free for a future octave shift or patch bank."* This proposal is that
future use. Wiring it is two new `case` arms in an existing switch
statement — see the implementation sketch below.

Pad row 1 indicators become **per-category**: the same columns
0..N-1 mechanism as today, just re-scoped to show pages *within the
active category* rather than a flat global list. Category identity
itself surfaces through the screen (a transient banner on Track ←/→,
the same 800 ms popup mechanism `NextPage`/`PrevPage` already use for
"Page N/M") — no new LED scheme, no hardware surface change.

## Proposed categories (v1)

### 1. VOICE — unchanged

Today's MAIN / OSC / FILTER / AMP / LFO-MOD, verbatim. Native-synth
only, same gating as today (a non-native patch can't reach it). This
is `categoryDefs()`'s first entry wrapping the existing `pageDefs()`
output with zero changes to any of its five pages — the whole point is
that VOICE users notice nothing different except that Track ←/→ now
does something (it was inert before).

### 2. FX — new

Backend-agnostic — available for **every** patch type, not just
native. This generalizes the precedent MAIN knobs 1–4 already set
(Volume/Reverb/Comp/Pedal work on every patch type because they're
chain-level, not synth-voice-level); FX makes that the category's
defining property instead of a one-off exception on MAIN.

- **Page 1 — DRIVE**: the Pedal knob (currently MAIN knob 4), room to
  grow (e.g. a future tone-stack control from the amp-emulation work
  in `docs/VISION.md` §1).
- **Page 2 — DELAY**: Time / Feedback / Mix (`docs/VISION.md` §1b),
  room for a 4th (e.g. exposing the feedback-loop lowpass cutoff as a
  "tone" knob later, currently a fixed constant).

> **Update (2026-07-14):** `docs/PEDALBOARD_UI.md` dictates the full
> FX page set for the pedals that now exist — DRIVE / CHORUS / TREM /
> DELAY, one page per pedal, generated from a chain registry — and
> adds a fourth always-available category, **MACROS** (8 knob slots
> assigned from the web UI). That doc resolves Open Question 5 below
> for today's effects; the category mechanics proposed here are
> unchanged and become its Phase 2 prerequisite.

### 3. MIX — new

Also backend-agnostic. This is the page `docs/ROADMAP.md` §0/§5
already planned and never shipped: *"A dedicated MIX page exposing the
four globals (volume/reverb/comp/master) is added as a sixth page so
they're never out of reach from inside the synth UI."* That page never
landed; this proposal is the concrete place for it to land, expanded
to include the two globals that currently have **zero hardware
access at all** — Mastering Comp and Limiter Ceiling exist only in the
web UI today (`internal/controls.SetMastering`, no `pages.go` slot
anywhere).

- **Page 1**: Volume / Reverb / Comp / Pedal (aliased — literally the
  same `adjVolume`/`adjReverb`/`adjCompressor`/`adjDrivePedal`
  functions MAIN already uses, just placed in a second `PageDef`; no
  new adjuster code needed) / Mastering Comp / Limiter Ceiling (new —
  see the implementation note below) / two open slots.

**Implementation note, not resolved here:** `Controls` currently only
exposes `SetMastering(compAmount, ceilingDB *float32)` — an absolute,
partial-update setter for the web PATCH endpoint. `pages.Slot`'s
`AdjustFunc` shape needs a *relative* delta setter (mirroring
`AdjustVolume`). This needs a small new pair of methods —
`AdjustMasteringComp`/`AdjustLimiterCeiling` — before MIX page 1 can
actually wire those two knobs; flagged here as a real but small gap,
not glossed over.

### Category availability

```go
VOICE.Available = func(patchType string) bool { return patchType == "native" }
FX.Available    = func(patchType string) bool { return true }
MIX.Available   = func(patchType string) bool { return true }
```

Track ←/→ **skips** unavailable categories and wraps, rather than
refusing the switch the way `NextPage`/`PrevPage` do today (`pages.go`
`cycle()`'s `if !p.native { refuse }`). Refusing doesn't generalize
well once there are multiple always-available categories to land on
instead — skipping does, and degrades correctly to today's exact
behavior when only one category (VOICE) exists for a given patch.

## Implementation sketch (`internal/controls/pages`)

- New `CategoryDef { Name string; Available func(patchType string) bool; Pages []PageDef }`.
- New `categoryDefs() []CategoryDef` — VOICE wraps the existing
  `pageDefs()` output verbatim; FX and MIX are new `[]PageDef` literals
  following the exact same `Slot{Label, Step, Adjust}` shape already
  used throughout `defs.go`.
- `Pages` struct: add `category int` alongside the existing `page int`
  (now "page within category"); replace the `native bool` field with
  `patchType string` (needed once availability isn't a single binary
  flag).
- New `NextCategory`/`PrevCategory` methods — same shape as
  `NextPage`/`PrevPage`, skip-unavailable-and-wrap instead of refuse.
- `HandleKnob`: index `defs[category].Pages[page].Slots` instead of
  the current flat `defs[page].Slots`.
- `paintPadsLocked`: iterate `defs[category].Pages` instead of the
  flat `defs` — same painting logic, re-scoped.
- `cmd/polyclav/main.go`'s `dispatchTransport`: add
  `case driver.TransportTrackLeft: pg.PrevCategory()` and
  `case driver.TransportTrackRight: pg.NextCategory()`.

That's the whole surface. No driver changes (the buttons already
decode), no new SysEx, no new pad rows.

## Screen feedback

- **Track ←/→ (category switch):** flash `"CATEGORY_NAME"` /
  `"Cat N/M"` for 800 ms — the exact same popup mechanism and duration
  as today's page switch, one level up.
- **Scene ↑/↓ (page switch):** unchanged — today's `"PageName"` /
  `"Page N/M"` flash, now implicitly scoped to the active category.
- **Knob turn:** unchanged.

## Migration / compatibility

Default boot state is VOICE category, page 0 (MAIN) — **identical** to
today's boot behavior. This is purely additive: Track ←/→ was inert
before and now does something; nothing about Scene ↑/↓, knob behavior,
or MAIN's layout changes. Existing muscle memory survives completely
intact, matching the same design principle the drive pedal's MAIN
placement followed.

`docs/USER_GUIDE.md`'s knob table gets a new top-level category
grouping once this actually ships — not before, to avoid documenting
something unbuilt as current.

## Relationship to `docs/CONFIGURABILITY.md`'s Tier 3

Tier 3 there proposes a `controlsurface.Surface` interface so a
`generic-midi` surface (config-driven CC bindings, no screen, no
pad-color awareness) can stand alongside the Launchkey implementation.
This Category × Page model is Launchkey-specific UX polish riding on
top of that eventual interface, not a replacement for it or a
prerequisite to it: a generic-midi surface has no screen/pad hierarchy
to speak of regardless of how many categories the Launchkey exposes,
so it keeps using flat CC-to-action bindings either way. Worth
building this now — the Launchkey ships today; Tier 3 doesn't yet —
without trying to prematurely generalize the category concept into
`docs/CONFIGURABILITY.md`'s schema before that abstraction exists.

## Open questions

### Still deferred

1. **What happens to `docs/ROADMAP.md` §3.1's per-patch page
   persistence** (deferred, never shipped) once there are two
   dimensions to remember instead of one? **Lean: defer identically —
   same reasoning as before (it was never built), now scoped to
   `(category, page)` instead of just `page` when it eventually is.**

2. **Should MIX's Pedal knob be removed from MAIN** now that it has a
   proper home, or kept as an alias? **Lean: keep the alias — matches
   the existing Drive-on-MAIN-and-AMP precedent, costs nothing, and
   removing it would be a second disruptive muscle-memory change in
   close succession to the first one.**

3. **Pad-row real estate if a category ever needs more than 5 pages**
   (the organ engine, once built, plausibly could). Does it (a) accept
   indicators stop being 1:1 with pages beyond 5, relying on the
   screen's "Page N/M" text for orientation past that point, or (b)
   reclaim columns 5–7 (currently reserved for user OSC/mixer
   bindings)? **Lean: (a) — don't touch the columns 5-7 reservation
   without a much stronger reason; the screen already gives exact
   orientation regardless of how many LEDs are lit.**

4. **Should Shift + a page-indicator pad jump directly to that page**
   (bypassing sequential Scene cycling)? A real quality-of-life idea
   once a category's page count grows. **Lean: defer to a v2 — Scene
   cycling within a small category (2-5 pages) is already fast enough
   that direct-jump isn't worth the added complexity yet.**

5. **Exact page-2/3+ contents for FX and MIX beyond what's listed
   above** — this doc names DRIVE and DELAY for FX (the two effects
   that exist) and one MIX page (the globals that exist); it
   deliberately doesn't pre-allocate pages for effects that don't
   exist yet (amp emulation, organ engine). **Lean: add pages when the
   effects they'd hold actually ship, not speculatively now.**
