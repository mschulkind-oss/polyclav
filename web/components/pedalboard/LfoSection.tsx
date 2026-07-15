import type { CSSProperties } from "react";
import { Led } from "@/components/pedalboard/Led";
import { RoleGlyph } from "@/components/pedalboard/RoleGlyph";
import { MiniKnob } from "@/components/pedalboard/synthExternals";
import { formatValue } from "@/lib/pedalboard/knobMath";
import { SYNTH } from "@/lib/pedalboard/model";

export type LfoField = "rateHz" | "depth" | "toPitch" | "toCutoff" | "toAmp";
export type LfoValues = Record<LfoField, number>;

export const LFO_DEFAULTS: LfoValues = {
  rateHz: SYNTH.lfo.rateHz.defaultValue,
  depth: SYNTH.lfo.depth.defaultValue,
  toPitch: SYNTH.lfo.toPitch.defaultValue,
  toCutoff: SYNTH.lfo.toCutoff.defaultValue,
  toAmp: SYNTH.lfo.toAmp.defaultValue,
};

export interface LfoSectionProps {
  /** Current values; defaults to the SYNTH model defaults (static mock). */
  lfo?: LfoValues;
}

/** Triangle wavelength in SVG user units — the scroll period `pb-wavescroll` translates by. */
export const LFO_WAVELEN = 26;
/** viewBox width in SVG user units — the visible scroll window. */
const VIEW_W = 118;

/**
 * Triangle wave overdrawn ≥ one wavelength past the visible window so the
 * one-wavelength `pb-wavescroll` translate loops seamlessly (the svg clips
 * the overdraw via `pb-scroll-clip`).
 */
export function lfoTriPath(depth: number): string {
  const wl = LFO_WAVELEN;
  const mid = 13;
  const amp = 2 + (depth / 100) * 8;
  const top = (mid - amp).toFixed(2);
  const bot = (mid + amp).toFixed(2);
  let d = `M0 ${mid}`;
  for (let x = 0; x < VIEW_W + wl; x += wl) {
    d += ` L${x + wl / 4} ${top} L${x + (3 * wl) / 4} ${bot} L${x + wl} ${mid}`;
  }
  return d;
}

function LfoMini({ field, lfo }: { field: LfoField; lfo: LfoValues }) {
  const spec = SYNTH.lfo[field];
  return (
    <div className="pb-param">
      <MiniKnob spec={spec} value={lfo[field]} />
      <div className="pb-p-name">
        <RoleGlyph role={spec.role} />
        {spec.label}
      </div>
      <div className="pb-p-val pb-num">{formatValue(lfo[field], spec)}</div>
    </div>
  );
}

/**
 * "LFO" card: rate/depth minis, the three routing minis, and a scrolling
 * triangle-wave signature viz. Motion is parameter-truthful: the scroll
 * cycle is 1/rate seconds (reference `--pb-wave-cycle` canon) and the
 * triangle's amplitude follows depth.
 */
export function LfoSection({ lfo = LFO_DEFAULTS }: LfoSectionProps = {}) {
  const s = SYNTH.lfo;
  const cycle = `${(1 / Math.max(lfo.rateHz, s.rateHz.min)).toFixed(4)}s`;
  return (
    <article className="pb-scard">
      <div className="pb-scard-top">
        <Led on />
        <h3>LFO</h3>
        <span className="pb-slot-ix pb-num">{formatValue(lfo.rateHz, s.rateHz)}</span>
      </div>
      <div className="pb-viz pb-scope-viz" aria-hidden="true">
        <svg className="pb-scope-svg pb-scroll-clip" viewBox="0 0 118 26" aria-hidden="true">
          <g
            className="pb-wave-anim"
            style={
              {
                "--pb-wave-cycle": cycle,
                "--pb-scroll-period": `${LFO_WAVELEN}px`,
              } as CSSProperties
            }
          >
            <path d={lfoTriPath(lfo.depth)} strokeWidth="1.5" strokeLinejoin="round" />
          </g>
        </svg>
      </div>
      <div className="pb-lfo-row">
        <LfoMini field="rateHz" lfo={lfo} />
        <LfoMini field="depth" lfo={lfo} />
      </div>
      <div className="pb-lfo-row">
        <LfoMini field="toPitch" lfo={lfo} />
        <LfoMini field="toCutoff" lfo={lfo} />
        <LfoMini field="toAmp" lfo={lfo} />
      </div>
    </article>
  );
}
