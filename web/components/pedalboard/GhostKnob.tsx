"use client";

import { knobSizeStyle } from "@/components/pedalboard/knobCore";

export interface GhostKnobProps {
  /** Diameter in SVG viewBox units. Default 112 (matches the macro knobs it stands in for). */
  size?: number;
}

/**
 * Dormant macro slot (reference `ghostSvg`): a faint 270° track ring with an
 * em-dash readout — "this slot exists, nothing is assigned". Purely
 * decorative, so it is aria-hidden and has no slider semantics.
 */
export function GhostKnob({ size = 112 }: GhostKnobProps) {
  const c = size / 2;
  const r = c - 15;
  return (
    <div className="pb-knob-ghost" style={knobSizeStyle(size)} aria-hidden="true">
      <svg viewBox={`0 0 ${size} ${size}`} aria-hidden="true">
        <circle
          className="pb-k-track"
          cx={c}
          cy={c}
          r={r}
          transform={`rotate(135 ${c} ${c})`}
          pathLength={360}
          strokeDasharray="270 360"
          fill="none"
        />
      </svg>
      <div className="pb-k-read">
        <span className="pb-k-num">—</span>
      </div>
    </div>
  );
}
