"use client";

import {
  ArcPair,
  KnobPointer,
  knobSizeStyle,
  MINI_ARC_HIDE_LEN,
} from "@/components/pedalboard/knobCore";
import { valueToFrac } from "@/lib/pedalboard/knobMath";
import type { ParamSpec } from "@/lib/pedalboard/model";

export interface MiniKnobProps {
  spec: ParamSpec;
  value: number;
  /** Diameter in SVG viewBox units. Default 36; the delay strip's Time mini uses 44. */
  size?: number;
}

/**
 * Display-only mini knob (reference `miniSvg`): the pedal strips' and bus
 * card's value indicators. No interaction, no tab stop — the adjacent
 * .pb-p-val text carries the value, so the whole thing is aria-hidden.
 * `spec.bipolar` arcs grow from the sweep center (bus gain); the arc hides
 * once its dash length drops under 0.75 so the round linecap never lingers.
 */
export function MiniKnob({ spec, value, size = 36 }: MiniKnobProps) {
  const c = size / 2;
  const r = c - 3.5;
  const frac = valueToFrac(value, spec.min, spec.max);
  return (
    <div className="pb-mini" style={knobSizeStyle(size)} aria-hidden="true">
      <svg viewBox={`0 0 ${size} ${size}`} aria-hidden="true">
        <ArcPair c={c} r={r} frac={frac} bipolar={spec.bipolar} hideLenBelow={MINI_ARC_HIDE_LEN} />
        <KnobPointer c={c} frac={frac} y1={c - r + 3} y2={c - r * 0.42} />
      </svg>
    </div>
  );
}
