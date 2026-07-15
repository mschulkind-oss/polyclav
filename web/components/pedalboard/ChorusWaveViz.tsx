import type { CSSProperties } from "react";
import { polyPath } from "@/components/pedalboard/vizPath";

/**
 * Chorus's signature module: a stereo pair of sines 90° apart (26-unit
 * wavelength → 6.5-unit shift) scrolling at the true LFO rate — the
 * `pb-wavescroll` keyframes translate one wavelength per `--pb-wave-cycle`,
 * which this component sets to 1/rate seconds (reference `#chorus-wave`).
 */
const WAVELEN = 26;
const MAIN = polyPath(118 + WAVELEN, (x) => 13 - 5.4 * Math.sin((2 * Math.PI * x) / WAVELEN));
const PARTNER = polyPath(
  118 + WAVELEN,
  (x) => 13 - 5.4 * Math.sin((2 * Math.PI * (x - 6.5)) / WAVELEN),
);

export interface ChorusWaveVizProps {
  /** LFO rate in Hz — one scrolled wavelength per cycle. */
  rateHz: number;
}

export function ChorusWaveViz({ rateHz }: ChorusWaveVizProps) {
  const cycle = `${(1 / rateHz).toFixed(4)}s`;
  return (
    <svg viewBox="0 0 118 26" aria-hidden="true">
      <g className="pb-wave-anim" style={{ "--pb-wave-cycle": cycle } as CSSProperties}>
        <path className="pb-wave-b" d={PARTNER} strokeWidth="1.25" strokeLinejoin="round" />
        <path d={MAIN} strokeWidth="1.5" strokeLinejoin="round" />
      </g>
    </svg>
  );
}
