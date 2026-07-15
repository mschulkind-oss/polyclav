import type { CSSProperties } from "react";
import { polyPath } from "@/components/pedalboard/vizPath";

/**
 * Trem's signature module: the opto lamp throbbing at the true rate
 * (`--pb-trem-cycle` = 1/rate), a flat "live" line, and a dashed ghost of the
 * wave the depth knob would carve. When inactive (bypassed or depth 0) the
 * `pb-depth-zero` class freezes the lamp dim via `pedalboard.css`
 * (reference `.opto` / `#trem-ghost`).
 */
const GHOST = polyPath(
  88,
  (x) => 13 - 6.5 * Math.sin((2 * Math.PI * x) / 30),
  (x) => (x === 0 ? 24 : ((x * 90) / 88 + 24).toFixed(1)),
);

export interface TremOptoVizProps {
  /** Tremolo rate in Hz — one lamp pulse per cycle. */
  rateHz: number;
  /** False when the pedal is bypassed or depth is 0: the lamp freezes dim. */
  active: boolean;
}

export function TremOptoViz({ rateHz, active }: TremOptoVizProps) {
  const cycle = `${Math.round(1000 / rateHz)}ms`;
  return (
    <svg
      viewBox="0 0 118 26"
      className={active ? undefined : "pb-depth-zero"}
      style={{ "--pb-trem-cycle": cycle } as CSSProperties}
      aria-hidden="true"
    >
      <circle className="pb-opto" cx="12" cy="13" r="4" />
      <path className="pb-trem-ghost" d={GHOST} strokeWidth="1.25" />
      <line x1="24" y1="13" x2="114" y2="13" strokeWidth="1.5" strokeLinecap="round" />
    </svg>
  );
}
