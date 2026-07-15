# Plugin-UI Research: Toward a VST-Style Pedalboard Interface

> **Status (2026-07-14):** research round, complete. Feeds the visual
> redesign of the pedalboard web UI — `docs/PEDALBOARD_UI.md`'s v1
> mockups are deliberately plain dashboard-style; the decision after
> reviewing them was to go more graphical and adventurous ("nice VST
> plugin with knobs"). This doc is the pattern library and framework
> shortlist for that redesign. **No mockups yet by explicit
> instruction** — direction gets picked from this research first.
>
> **Artifacts (local-only, gitignored):** 42 exemplar UIs across 7
> categories, 84 verified screenshots, browsable gallery at
> `scratch/plugin-ui-research/index.html`, detailed per-category notes
> in `scratch/plugin-ui-research/notes/*.md`, machine-readable
> `manifest.json`. Screenshots are vendor property — kept out of the
> repo on purpose; only this synthesis is committed.

## Method

Seven parallel researchers, one per category: amp-sim suites
(AmpliTube 5, Guitar Rig 7, BIAS FX 2, Neural DSP, TH-U), stompbox
plugins (Soundtoys, UAD, Arturia FX, Strymon, Baby Audio, Valhalla),
synth VSTs (Pigments, Diva, Vital, Serum 2, Cherry Audio, Korg),
hardware editors & web pedalboards (MOD Audio mod-ui, Axe-Edit III,
Kemper Rig Manager, Boss Tone Studio), open-source/web-audio toolkits
(VCV/Cardinal, webaudio-controls, NexusUI, WAM, Faust), plus two
code-focused surveys of JS rendering libraries and interaction/graph
libraries. Every screenshot was size- and format-verified; pages that
resisted download were captured with a real browser.

## What to steal — the pattern synthesis

### 1. Art direction: refined-hybrid pedals, flat-modern chrome

The field splits into three camps: aggressive skeuomorphism
(AmpliTube's photographic gear, Cherry Audio's replica panels),
**refined-hybrid** (Soundtoys' leather-and-bevel pedal faces, UAD's
hardware-faithful-but-cleaned-up units, Neural DSP's realistic knobs
on flat bright panels), and **flat-modern** (Vital, Valhalla, the
Strymon *plugins*, Arturia's FX line, Baby Audio).

The trend line for new designs is flat-modern with selective depth —
but polyclav's pedals are literal circuit emulations of specific
hardware archetypes (CE-2-style BBD chorus, DM-2-territory BBD delay,
blackface-style tremolo, Tube-Screamer-style drive). A **refined-hybrid
pedal face per effect** (distinct color/character per pedal, real knob
rendering, LED stomp state — no fake screws or wood) plays to that
identity, while everything around the pedals (chain rail, bus strip,
macros, hardware map) stays **flat-modern using the existing app
tokens**. Precedents: Soundtoys for restraint, Neural DSP for
hybrid-on-clean-background, VCV/Cardinal for dark-native module color.

### 2. Signal chain: left-to-right board of real pedal faces

Universal convention across TH-U, BIAS FX 2, MOD's mod-ui, and
pedalboard.js: audio flows left→right as distinct units. Specifics
worth copying:

- **Mini-knobs inline on the chain view** (MOD mod-ui): the overview
  is not just status — small functional knobs on each pedal face make
  most edits possible without opening anything.
- **Board view vs. edit view as an explicit toggle** (BIAS FX 2's
  performance view): overview for playing, focused view for tweaking.
  Matches our planned strip → pedal-editor navigation.
- **Dual representation** (Guitar Rig 7's sidebar minimap, Axe-Edit's
  librarian/flow/inspector trio): a compact map plus a detail panel
  beats one giant view. Our Hardware Map page already follows this.

### 3. The knob interaction canon

Every serious plugin converges on the same contract — adopt it
wholesale: **vertical drag** to change, **Shift = fine adjust**,
**double-click = reset to default**, **scroll wheel** steps,
value+unit readout appears on touch/hover (map to our 800 ms
Launchkey popup for cross-surface consistency), arc/ring indicator
showing position and (see §5) modulation range. Two refinements from
the stompbox set: **proportional knob sizing** (Arturia JUN-6 — the
important knob is physically bigger; for us: Mix/Depth large,
Rate/Time medium) and **dynamic parameter reveal** (Strymon BigSky —
only show controls relevant to the current mode; relevant when pedals
grow tone/character params later).

### 4. Macro assignment: Vital/Pigments drag-to-assign is the bar

The single most relevant interaction finding. Vital, Serum, and
Pigments all do macro/modulation assignment as **direct
manipulation**: drag from the macro source onto any target knob;
the target then wears a **colored ring segment showing the macro's
sweep range**, draggable to resize. This is dramatically better than
our v1-proposed dropdown-per-slot, and it maps cleanly onto the
registry + `PUT /api/macros` design in `docs/PEDALBOARD_UI.md` — same
wire format, better gesture. Large, always-visible macro knobs
(Pigments) reinforce that macros are the performance surface — they
mirror the 8 physical MACROS knobs 1:1.

### 5. Animate the actual DSP state

Baby Audio Crystalline's animated visualization and Vital's 60 fps
everything both land the same lesson: the UI should *move with the
sound*. For us this is cheap because the client already knows the
params via SSE: tremolo LED **blinking at `rate_hz`**, chorus LFO
sweep animation at its rate, delay repeat pulses at `time_ms`
intervals, drive saturation glow by amount. All client-side
`requestAnimationFrame` driven by parameter values — no audio-thread
or wire cost, and it doubles as at-a-glance state display
(a blinking-fast trem LED *is* the rate readout).

### 6. Hardware ↔ screen (validation from the editor category)

The hardware-editor survey mostly **validates** `docs/PEDALBOARD_UI.md`'s
nav-sync design: editors live or die on the screen following the
hardware and vice versa. Axe-Edit's inspector pattern and Boss Tone
Studio's explicit write-to-hardware flow are the references; MOD's
mod-ui (open source, GPL — reference for *patterns*, not code
vendoring) is the closest thing to what we're building and worth a
deep look at its pedalboard interaction details.

## Framework shortlist

The two code surveys plus my own weighing (agent-reported bundle
numbers varied too much to trust — treat sizes below as ballpark,
re-measure at adoption). All candidates are MIT, React-19-compatible,
and client-only (fine under Next static export; render behind a
`"use client"` + dynamic import boundary).

| Layer | Pick | Why | Ballpark cost |
|---|---|---|---|
| Knob/control primitives | **react-knob-headless + @use-gesture** | Headless = our look, our tokens; gesture lib gives drag/fine/scroll for free; accessible | ~15–25 KB gz |
| Pedal faces / board | **Hand-built SVG + CSS (no framework)** | ≤ ~40 mostly-static controls is far below SVG's practical ceiling; keeps theming, inspectability, zero lock-in | 0 |
| Reorder / drag-drop | **dnd-kit** (when chain edit lands) | Proven, small, works with DOM/SVG cards | ~15 KB gz |
| Free-form routing (future) | **React Flow (xyflow)** | Custom React nodes mean our pedal components embed directly in the graph; adopt only when chain stops being a fixed rail | ~45–50 KB gz |
| High-rate visualizations | **Canvas/WebGL as isolated components** (plain canvas first; Pixi v8 only if scopes/spectra multiply) | 60 fps scope-style animation is canvas territory; but don't build the whole UI in canvas — it costs accessibility, theming, and text quality | 0 → ~100 KB gz |

**Explicitly rejected for v1:** building the entire pedalboard in
Pixi/Konva (one survey's lead recommendation). The "SVG falls over
past ~20 controls" claim assumes continuous whole-scene animation; our
knobs are static except during interaction and the few live-state
animations in §5 are per-element and cheap. A canvas-first UI would
sacrifice the CSS token theming, DOM accessibility, and the SliderRow
debounce/SSE-echo machinery the app already has. Canvas earns its
place per-visualization, not as the foundation.

**Also noted:** webaudio-controls (g200kg) ships pre-styled
skeuomorphic knobs as WebComponents with filmstrip rendering — useful
as a *visual reference* (and WebKnobMan for generating knob art), but
headless + our own SVG is the better fit for the token system.

## Open questions

### 1. Art direction for the pedal faces

(a) refined-hybrid skeuomorphic pedals on flat chrome (per §1),
(b) all flat-modern (Vital-like, closest to current app), or
(c) heavy skeuomorphism (Cherry Audio / AmpliTube).

_Leaning:_ (a) — it matches the circuit-emulation identity, ages
better than (c), and is more fun than (b). Each pedal gets its own
enclosure color + knob style riffing on its hardware archetype
(CE-2 blue for chorus, DM-2/Carbon-Copy dark green for delay, blackface
cream for tremolo, TS green for drive).

**Answer:**
> _(empty — fill in when decided)_

### 2. Do the pedals stay theme-aware?

Real VST plugins are single-theme; our app has light/dark tokens.

_Leaning:_ pedal faces sit on a dark "board" surface in both themes
(dark-native like VCV, board color fixed), while app chrome around the
board keeps following the light/dark tokens. One design, no washed-out
light-mode pedals.

**Answer:**
> _(empty — fill in when decided)_

### 3. How far does the redesign reach beyond the pedalboard?

Pedal faces + macros + hardware map are the obvious scope. Does the
synth editor (SynthCard) and mix bus get the same treatment?

_Leaning:_ pedalboard + macros first; the Minimoog-style VOICE panel is
its own (bigger, very tempting) project once the pedal language exists.

**Answer:**
> _(empty — fill in when decided)_
