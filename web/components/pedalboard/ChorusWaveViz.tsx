import type { CSSProperties } from "react";
import { polyPath } from "@/components/pedalboard/vizPath";

/**
 * Chorus's signature module: a stereo pair of sines 90° apart (26-unit
 * wavelength → 6.5-unit shift) scrolling at the true LFO rate. Seamless-loop
 * canon: the paths overdraw one full wavelength past the 118-unit viewBox,
 * the svg clips the overdraw (`pb-scroll-clip`), and `pb-wavescroll`
 * translates by exactly `--pb-scroll-period` (set here to the drawn
 * wavelength) per `--pb-wave-cycle` = 1/rate seconds (reference
 * `#chorus-wave`).
 */
export const CHORUS_WAVELEN = 26;
/** viewBox width in SVG user units — the visible scroll window. */
const VIEW_W = 118;
const MAIN = polyPath(
  VIEW_W + CHORUS_WAVELEN,
  (x) => 13 - 5.4 * Math.sin((2 * Math.PI * x) / CHORUS_WAVELEN),
);
const PARTNER = polyPath(
  VIEW_W + CHORUS_WAVELEN,
  (x) => 13 - 5.4 * Math.sin((2 * Math.PI * (x - CHORUS_WAVELEN / 4)) / CHORUS_WAVELEN),
);

export interface ChorusWaveVizProps {
  /** LFO rate in Hz — one scrolled wavelength per cycle. */
  rateHz: number;
}

export function ChorusWaveViz({ rateHz }: ChorusWaveVizProps) {
  const cycle = `${(1 / rateHz).toFixed(4)}s`;
  return (
    <svg className="pb-scroll-clip" viewBox={`0 0 ${VIEW_W} 26`} aria-hidden="true">
      <g
        className="pb-wave-anim"
        style={
          {
            "--pb-wave-cycle": cycle,
            "--pb-scroll-period": `${CHORUS_WAVELEN}px`,
          } as CSSProperties
        }
      >
        <path className="pb-wave-b" d={PARTNER} strokeWidth="1.25" strokeLinejoin="round" />
        <path d={MAIN} strokeWidth="1.5" strokeLinejoin="round" />
      </g>
    </svg>
  );
}
