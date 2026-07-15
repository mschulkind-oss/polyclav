# Pedalboard design system (Flat Modern)

React port of `docs/mockups/pedalboard-style-b-flat-modern.html` — that file is the
source of truth for every visual, value, and interaction. Composed on the hidden
playground page `/app/mockup` with static mock data only (no API calls).

## File map

| Path | Owns |
| --- | --- |
| `web/lib/pedalboard/model.ts` | Types (`Role`, `ParamSpec`, `PedalSpec`, …) + static data (`CHAIN`, `BUS_PARAMS`, `PATCHES`/`PAD_SLOTS`, `SYNTH`) |
| `web/lib/pedalboard/knobMath.ts` | Pure knob math: `clamp`, `valueToFrac`, `fracToAngle`, `arcDash`, `pointerTransform`, `gateNotchTransform`, `formatValue`, `dragValue`, `wheelStep`, `keyStep`, `A0`, `SWEEP` |
| `web/components/pedalboard/pedalboard.css` | ALL shared styles (Foundation-owned; do not edit) |
| `web/components/pedalboard/<yourpkg>.extra.css` | Styles a builder genuinely needs that are missing here — scoped under `.pb-root`, `pb-` prefixed, declared in your return |
| `web/components/pedalboard/*.tsx` | Components (builders). Components import NO css; the page imports the stylesheets once |

## Scaling: `--pb-scale` and `--u`

The reference used `1rem = 10px x scale`. Here `.pb-root` defines
`--pb-scale: 1` and `--u: calc(1px * var(--pb-scale))`; every dimension in the
stylesheet is `calc(var(--u) * N)` where N = the reference's rem x 10 (px at
scale 1). The A-/A+ control just sets `--pb-scale` inline on `.pb-root`
(steps `0.8 0.9 1 1.1 1.2 1.3 1.4`) and the whole system resizes. Rules:

- Never hard-code px for layout — only 1px/2px hairline borders stay literal px.
- Sizing an element from JS/JSX: `style={{ width: \`calc(var(--u) * ${size})\` }}`
  (the reference's `sizeEl(el, size)`).
- `dragValue(..., scale)` takes the current numeric `--pb-scale` so drag feel is
  identical at every zoom.
- SVG internals (viewBox coordinates, `translateX(-26px)` in `pb-wavescroll`,
  9px SVG text) are user units, NOT `--u` — leave them alone.

## The alignment contract (role -> row)

`.pb-strip`, `.pb-srcnode`, and `.pb-bus` share one grid template —
`grid-template-rows: header(24) time(80) shape(80) blend(80) viz(34) stomp(30)`
(all `--u` units, `row-gap` 12). The same param ROLE always sits in the same row
on every card; a pedal without a role renders `.pb-slot-empty` with
`--pb-row: <2|3|4>` (a faint dashed ring — deliberately empty). Never add fixed
card heights.

| Row | Content | Class | Role glyph |
| --- | --- | --- | --- |
| 1 | header | `.pb-strip-top` / `.pb-src-top` / `.pb-bus-top` | — |
| 2 | time / rate | `.pb-param.pb-r-time` | clock |
| 3 | shape / intensity | `.pb-param.pb-r-shape` | ramp triangle |
| 4 | blend (wet/dry) | `.pb-param.pb-r-blend` | half-filled circle |
| 5 | signature module | `.pb-viz` | — |
| 6 | stomp | `.pb-stomp` | — |

The bus packs its four params as pairs (`.pb-bus-pair.pb-r1/.pb-r2`) into rows
2–3; `level` role uses the fader-bars glyph. `gate` params (`ParamSpec.gate`)
show the square `.pb-gate-dot` after the label and the `.pb-k-gate` notch at
sweep start: knob at 0 is a true bypass. `.pb-legend` explains all of this.

## Knob canon (interaction + rendering)

SVG circle `pathLength="360"` rotated 135deg; track dasharray `270 360`; value
arc from `arcDash(frac, bipolar)`; pointer line rotated `-135..+135deg`
(`pointerTransform`). Hide the arc near zero (mini: dash len < 0.75; big:
frac < 0.004). Interactions: vertical pointer-drag with `setPointerCapture`
(~200 scaled px = full sweep -> `dragValue`), Shift = 5x fine, wheel 1%
(Shift 0.2%) -> `wheelStep`, double-click = default, ArrowKeys -> `keyStep`.
A11y: `role="slider"`, `aria-valuemin/max/now/text`, `tabindex 0`,
`cursor: ns-resize`, never selects text. Add `.pb-dragging` while captured.

## Class inventory (all `pb-` prefixed, all scoped under `.pb-root`)

- **Root/util**: `pb-root` (tokens + `--u`, fills viewport), `pb-num`, `pb-kicker`
- **Accents**: set `--pb-accent` inline per card from `PedalSpec.accentVar`
  (`--pb-amber` drive, `--pb-cyan` chorus, `--pb-violet` trem, `--pb-mint`
  delay, `--pb-neutral` bus/src); `.pb-bypassed` overrides it to `--pb-off`
- **Header**: `pb-header`, `pb-brand`, `pb-chain-glyph`, `pb-wordmark`,
  `pb-badge`, `pb-head-right`, `pb-tabs`, `pb-tab` (+`pb-active`),
  `pb-scalectl`, `pb-scale-val`
- **Screens**: `pb-main`, `pb-screen` (+`pb-active`), `pb-screen-head`,
  `pb-sub`, `pb-meta`, `pb-footer`
- **Rail**: `pb-railwrap`, `pb-rail`, `pb-wire`, `pb-strip` (+`pb-bypassed`,
  `pb-depth-zero`), `pb-srcnode`, `pb-src-glyph`, `pb-src-sub`, `pb-strip-top`,
  `pb-src-top`, `pb-bus-top`, `pb-slot-ix`, `pb-led`, `pb-rail-hint`
- **Params**: `pb-param` + `pb-r-time`/`pb-r-shape`/`pb-r-blend`,
  `pb-slot-empty` (needs `--pb-row`), `pb-p-name`, `pb-p-val`, `pb-glyph`,
  `pb-gate-dot`, `pb-legend`, `pb-lg-item`
- **Knobs**: `pb-mini` (display-only), `pb-knob` (+size `pb-md`/`pb-xl`, state
  `pb-dragging`), `pb-knob-ghost`; SVG parts `pb-k-track`, `pb-k-arc`,
  `pb-k-ptr`, `pb-k-gate`, `pb-k-otrack`, `pb-k-orange`, `pb-k-otick`; readout
  `pb-k-read`, `pb-k-num`, `pb-k-unit`; labels `pb-kgroup`, `pb-k-label`,
  `pb-k-minmax`, `pb-knob-hint`
- **Signature viz**: `pb-viz`, `pb-fillpath` (drive heat), `pb-wave-anim` +
  `pb-wave-b` (chorus; set `--pb-wave-cycle` = 1/rate s), `pb-opto` +
  `pb-trem-ghost` (trem; set `--pb-trem-cycle` = 1/rate s), `pb-strip-tail`
  (delay dots), `pb-axis`
- **Stomp**: `pb-stomp` (+`pb-on`, `pb-big`), `pb-stomp-dot`
- **Bus**: `pb-bus`, `pb-meters` (`pb-mL`/`pb-mR`), `pb-bus-pair` +
  `pb-r1`/`pb-r2`/`pb-r3`, `pb-bus-sub`
- **Editor**: `pb-crumb`, `pb-crumb-link`, `pb-sep`, `pb-crumb-cur`, `pb-chip`
  (+`pb-live`), `pb-hero` (+`pb-bypassed`), `pb-hero-main`, `pb-hero-id`,
  `pb-hero-title`, `pb-hero-rule`, `pb-hero-sub`, `pb-ghostbtn`,
  `pb-hero-knobs`, `pb-hero-tail`, `pb-tail-head`, `pb-tail-caption`,
  `pb-tail-wrap`, tail SVG parts `pb-t-base`, `pb-t-tick`, `pb-t-lab`,
  `pb-t-dry`, `pb-e-dot`, `pb-t-ms`
- **Hardware bar**: `pb-hwbar`, `pb-hw-left`, `pb-hw-path`, `pb-hw-map`,
  `pb-kchip`, `pb-hw-btn` (+`pb-sent`)
- **Macros**: `pb-macro-grid`, `pb-macro` (+`pb-dormant`), `pb-m-head`,
  `pb-m-chip`, `pb-m-name`, `pb-m-target`, `pb-t-dot`, `pb-m-rows`,
  `pb-m-mapped`
- **Keyframes**: `pb-rise`, `pb-wireflow`, `pb-breathe`, `pb-wavescroll`,
  `pb-optopulse`, `pb-ping`, `pb-dryping`, `pb-meterA`, `pb-meterB` —
  motion is parameter-truthful: compute durations/delays from real values
  (e.g. delay dots `animation-delay: n * time_ms`). Reduced-motion disables all.

## Tests

```sh
cd web && pnpm test          # vitest run (or: just web-test)
cd web && pnpm test:watch
```

Vitest + jsdom + Testing Library; setup in `web/test-setup.ts` (jest-dom
matchers, auto `cleanup`). Tests live next to sources as `<name>.test.ts(x)`
and must import from `"vitest"` explicitly (no globals).
