/**
 * The filter card's signature module: a lowpass magnitude curve on a log
 * frequency axis. The rolloff is a sigmoid centered at the cutoff position
 * (0–100 = 20 Hz – 20 kHz, matching knobMath's "hzlog"), and resonance adds
 * an exponential bump peaking at the cutoff. Curve, soft fill, and the
 * cutoff marker all re-render as the knobs move.
 */
const W = 220;
const H = 60;
const BASE_Y = 50; // -inf dB baseline
const GAIN = 34; // unity-gain plateau height above the baseline
const ROLLOFF = 5; // sigmoid steepness, in cutoff-position units
const SIGMA = 4; // resonance bump width, in cutoff-position units
const PEAK = 0.9; // resonance bump height at res = 100%

export function filterCurvePath(cutoffPos: number, resonance: number): string {
  const res = resonance / 100;
  let d = "";
  for (let x = 0; x <= W; x += 2) {
    const pos = (x / W) * 100;
    const rolloff = 1 / (1 + Math.exp((pos - cutoffPos) / ROLLOFF));
    const bump = res * PEAK * Math.exp(-((pos - cutoffPos) ** 2) / (2 * SIGMA * SIGMA));
    const y = Math.max(BASE_Y - (rolloff + bump) * GAIN, 3);
    d += `${x ? " L" : "M"}${x} ${y.toFixed(2)}`;
  }
  return d;
}

export interface FilterCurveVizProps {
  /** Cutoff knob position 0–100 (displayed as 20 Hz – 20 kHz). */
  cutoffPos: number;
  /** Resonance 0–100%. */
  resonance: number;
}

export function FilterCurveViz({ cutoffPos, resonance }: FilterCurveVizProps) {
  const curve = filterCurvePath(cutoffPos, resonance);
  const fcX = Math.min(Math.max((cutoffPos / 100) * W, 0), W).toFixed(1);
  return (
    <div className="pb-viz pb-fcurve" aria-hidden="true">
      <svg viewBox={`0 0 ${W} ${H}`} aria-hidden="true">
        <line className="pb-axis" x1="0" y1={BASE_Y} x2={W} y2={BASE_Y} />
        <line className="pb-axis" x1={fcX} y1="6" x2={fcX} y2={BASE_Y} />
        <path
          className="pb-fillpath"
          d={`${curve} L${W} ${BASE_Y} L0 ${BASE_Y} Z`}
          style={{ opacity: 0.06 + (resonance / 100) * 0.18 }}
        />
        <path d={curve} strokeWidth="1.5" strokeLinejoin="round" />
      </svg>
    </div>
  );
}
