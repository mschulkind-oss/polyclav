"use client";

import {
  ArcPair,
  KNOB_ARC_HIDE_LEN,
  KnobPointer,
  knobSizeStyle,
  splitReadout,
  useKnobInteraction,
} from "@/components/pedalboard/knobCore";
import { formatValue, gateNotchTransform, valueToFrac } from "@/lib/pedalboard/knobMath";
import type { ParamSpec } from "@/lib/pedalboard/model";

export interface KnobProps {
  spec: ParamSpec;
  value: number;
  onChange: (v: number) => void;
  /** Diameter in SVG viewBox units, rendered at calc(var(--u) * size). Default 120. */
  size?: number;
  /**
   * Readout type scale — "lg" (default) is the bare .pb-knob size; "md"/"xl"
   * add .pb-md/.pb-xl (the reference's editor uses xl 164 / lg 124, macros md 112).
   */
  sizeClass?: "md" | "lg" | "xl";
  disabled?: boolean;
}

/**
 * The interactive arc knob (reference `bigKnobSvg` + `initKnob`): a 270°
 * pathLength-360 track rotated 135°, an accent value arc, a pointer line
 * rotated −135°..+135°, and the big-number readout. Full interaction canon:
 * vertical pointer-capture drag (~200 scaled px = full sweep), Shift = 5×
 * fine, wheel 1% (Shift 0.2%), double-click = spec.defaultValue, arrow keys.
 * `spec.gate` renders the hollow notch at sweep start (0 = true bypass);
 * `spec.bipolar` grows the arc from the sweep center; `spec.fmt === "hzlog"`
 * reads out via formatValue's 20 Hz – 20 kHz log mapping.
 */
export function Knob({
  spec,
  value,
  onChange,
  size = 120,
  sizeClass = "lg",
  disabled = false,
}: KnobProps) {
  const { ref, dragging, handlers } = useKnobInteraction({
    value,
    min: spec.min,
    max: spec.max,
    defaultValue: spec.defaultValue,
    onChange,
    disabled,
  });

  const c = size / 2;
  const r = c - 6;
  const frac = valueToFrac(value, spec.min, spec.max);
  const read = splitReadout(value, spec);
  const cls = [
    "pb-knob",
    sizeClass === "md" ? "pb-md" : "",
    sizeClass === "xl" ? "pb-xl" : "",
    dragging ? "pb-dragging" : "",
  ]
    .filter(Boolean)
    .join(" ");

  return (
    <div
      ref={ref}
      className={cls}
      style={knobSizeStyle(size)}
      role="slider"
      tabIndex={disabled ? -1 : 0}
      aria-label={spec.label}
      aria-valuemin={spec.min}
      aria-valuemax={spec.max}
      aria-valuenow={value}
      aria-valuetext={formatValue(value, spec)}
      aria-disabled={disabled || undefined}
      {...handlers}
    >
      <svg viewBox={`0 0 ${size} ${size}`} aria-hidden="true">
        <ArcPair c={c} r={r} frac={frac} bipolar={spec.bipolar} hideLenBelow={KNOB_ARC_HIDE_LEN} />
        {spec.gate ? (
          <circle
            className="pb-k-gate"
            cx={c}
            cy={c + r}
            r={1.8}
            transform={gateNotchTransform(c)}
          />
        ) : null}
        <KnobPointer c={c} frac={frac} y1={c - r + 6} y2={c - r * 0.5} />
      </svg>
      <div className="pb-k-read">
        <span className="pb-k-num pb-num">{read.num}</span>
        <span className="pb-k-unit">{read.unit}</span>
      </div>
    </div>
  );
}
