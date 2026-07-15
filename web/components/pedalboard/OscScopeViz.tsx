import type { OscWave } from "@/lib/pedalboard/model";

/**
 * The oscillator card's signature module: a scope trace of the selected
 * waveform, drifting slowly left (`pb-scopescroll`, synth.extra.css). The
 * viewBox shows exactly 2 periods; the path carries 3 so the one-period
 * translate loops seamlessly. Coordinates are SVG user units, not --u.
 */
const PERIOD = 59; // 118 / 2 — two visible periods
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
  for (let k = 0; k < PERIODS; k++) {
    const x = k * PERIOD;
    switch (wave) {
      case "saw":
        pts.push([x, bot], [x + PERIOD, top], [x + PERIOD, bot]);
        break;
      case "square":
        pts.push(
          [x, top],
          [x + PERIOD / 2, top],
          [x + PERIOD / 2, bot],
          [x + PERIOD, bot],
          [x + PERIOD, top],
        );
        break;
      case "tri":
        pts.push([x, MID], [x + PERIOD / 4, top], [x + (3 * PERIOD) / 4, bot], [x + PERIOD, MID]);
        break;
      case "pulse":
        pts.push(
          [x, top],
          [x + PERIOD / 4, top],
          [x + PERIOD / 4, bot],
          [x + PERIOD, bot],
          [x + PERIOD, top],
        );
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
      <svg className="pb-scope-svg" viewBox="0 0 118 26" aria-hidden="true">
        <line className="pb-axis" x1="0" y1="13" x2="118" y2="13" />
        <g className="pb-scope">
          <path d={scopePath(wave)} strokeWidth="1.5" strokeLinejoin="round" />
        </g>
      </svg>
    </div>
  );
}
