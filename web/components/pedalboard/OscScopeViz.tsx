import type { CSSProperties } from "react";
import type { OscWave } from "@/lib/pedalboard/model";

/**
 * The oscillator card's signature module: a scope trace of the selected
 * waveform, drifting slowly left (`pb-scopescroll`, synth.extra.css). The
 * viewBox shows exactly 2 periods; the path carries 3 and the svg clips the
 * overdraw (`pb-scroll-clip`) so the one-period translate — exactly
 * `--pb-scroll-period`, set below — loops seamlessly. Coordinates are SVG
 * user units, not --u.
 */
export const SCOPE_PERIOD = 59; // 118 / 2 — two visible periods
const PERIODS = 3;
const MID = 13;
const AMP = 8;

const fmt = (v: number) => `${Math.round(v * 100) / 100}`;

type Pt = readonly [number, number];

function toPath(pts: readonly Pt[]): string {
  let d = "";
  let prev: Pt | undefined;
  for (const p of pts) {
    if (prev && prev[0] === p[0] && prev[1] === p[1]) continue;
    d += `${d ? " L" : "M"}${fmt(p[0])} ${fmt(p[1])}`;
    prev = p;
  }
  return d;
}

/** Piecewise-linear scope trace for a wave — exact corners, no sampling. */
export function scopePath(wave: OscWave): string {
  const top = MID - AMP;
  const bot = MID + AMP;
  const pts: Pt[] = [];
  const p = SCOPE_PERIOD;
  for (let k = 0; k < PERIODS; k++) {
    const x = k * p;
    switch (wave) {
      case "saw":
        pts.push([x, bot], [x + p, top], [x + p, bot]);
        break;
      case "square":
        pts.push([x, top], [x + p / 2, top], [x + p / 2, bot], [x + p, bot], [x + p, top]);
        break;
      case "tri":
        pts.push([x, MID], [x + p / 4, top], [x + (3 * p) / 4, bot], [x + p, MID]);
        break;
      case "pulse":
        pts.push([x, top], [x + p / 4, top], [x + p / 4, bot], [x + p, bot], [x + p, top]);
        break;
    }
  }
  return toPath(pts);
}

export interface OscScopeVizProps {
  /** Wave to trace — the oscillator card follows osc 1. */
  wave: OscWave;
}

export function OscScopeViz({ wave }: OscScopeVizProps) {
  return (
    <div className="pb-viz pb-scope-viz" aria-hidden="true">
      <svg className="pb-scope-svg pb-scroll-clip" viewBox="0 0 118 26" aria-hidden="true">
        <line className="pb-axis" x1="0" y1="13" x2="118" y2="13" />
        <g
          className="pb-scope"
          style={{ "--pb-scroll-period": `${SCOPE_PERIOD}px` } as CSSProperties}
        >
          <path d={scopePath(wave)} strokeWidth="1.5" strokeLinejoin="round" />
        </g>
      </svg>
    </div>
  );
}
