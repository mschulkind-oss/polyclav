import { polyPath } from "@/components/pedalboard/vizPath";

/**
 * Reverb's signature module: an exponential-decay "bloom" in the DelayTailViz
 * idiom. A dry-hit marker at t0, a short fixed pre-delay gap, then a filled
 * envelope that reaches further right with longer Decay, brightens with wetter
 * Mix, and grows taller/crisper with higher Tone (HF damping made visual). At
 * Mix 0 the bloom fades to nothing — the bit-exact bypass the gate promises.
 */
const MID = 13;
const X0 = 8;
/** Pre-delay gap: the tail onset sits a few units past the dry hit. */
const ONSET = X0 + 6;

function clamp01(v: number): number {
  return v < 0 ? 0 : v > 1 ? 1 : v;
}

export interface ReverbBloomVizProps {
  /** Decay time in ms (model units, 200–8000). */
  decay: number;
  /** Tone / HF damping percent (model units, 0–100). */
  tone: number;
  /** Mix percent (model units, 0–100). */
  mix: number;
}

export function ReverbBloomViz({ decay, tone, mix }: ReverbBloomVizProps) {
  const dnorm = clamp01((decay - 200) / (8000 - 200));
  const reach = ONSET + (112 - ONSET) * dnorm; // longer decay → wider bloom
  const tau = Math.max((reach - ONSET) * 0.5, 6); // decay time constant
  const amp = 9 * (0.5 + tone / 200); // brighter Tone → taller, crisper bloom
  const bloom = (x: number) => (x <= ONSET ? 0 : amp * Math.exp(-(x - ONSET) / tau));
  const top = polyPath(118, (x) => MID - bloom(x));
  const heat = (0.1 + (mix / 100) * 0.55).toFixed(3);
  return (
    <svg viewBox="0 0 118 26" aria-hidden="true">
      <line x1={X0} y1="5" x2={X0} y2="21" strokeWidth="2" strokeLinecap="round" opacity="0.85" />
      <path
        className="pb-fillpath"
        d={`${top} L118 ${MID} L${ONSET} ${MID} Z`}
        style={{ opacity: heat }}
      />
      <path d={top} strokeWidth="1.2" strokeLinejoin="round" />
    </svg>
  );
}
