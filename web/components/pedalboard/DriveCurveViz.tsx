import { polyPath } from "@/components/pedalboard/vizPath";

/**
 * Drive's signature module: a tanh transfer curve over the ±1 input axis with
 * a "heat" fill whose opacity grows with the drive amount (reference
 * `#drive-curve` / `#drive-fill`: opacity = 0.06 + amount × 0.5).
 */
const K = 1.9;
const CURVE = polyPath(118, (x) => {
  const u = (x / 118) * 2 - 1;
  return 13 - (Math.tanh(K * u) / Math.tanh(K)) * 9;
});

export interface DriveCurveVizProps {
  /** Drive amount, 0..100 (the raw `drive.amount` param value). */
  amount: number;
}

export function DriveCurveViz({ amount }: DriveCurveVizProps) {
  const heat = (0.06 + (amount / 100) * 0.5).toFixed(3);
  return (
    <svg viewBox="0 0 118 26" aria-hidden="true">
      <line className="pb-axis" x1="4" y1="13" x2="114" y2="13" />
      <path className="pb-fillpath" d={`${CURVE} L118 13 L0 13 Z`} style={{ opacity: heat }} />
      <path d={CURVE} strokeWidth="1.5" strokeLinejoin="round" />
    </svg>
  );
}
