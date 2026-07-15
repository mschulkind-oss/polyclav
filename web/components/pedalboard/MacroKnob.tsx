"use client";

import { useEffect, useRef } from "react";
import {
  ArcPair,
  KNOB_ARC_HIDE_LEN,
  KnobPointer,
  knobSizeStyle,
  splitReadout,
  useKnobInteraction,
} from "@/components/pedalboard/knobCore";
import { formatValue, pointerTransform, SWEEP, valueToFrac } from "@/lib/pedalboard/knobMath";

export interface MacroKnobProps {
  /** Macro position, 0–100 (%). */
  value: number;
  onChange: (v: number) => void;
  /** Mapped range start, in % of the target param. */
  rangeA: number;
  /** Mapped range end, in % of the target param. */
  rangeB: number;
  /** Fires with rangeA + value/100 · (rangeB − rangeA) on mount and whenever it changes. */
  onMapped?: (mapped: number) => void;
  /** Double-click reset. Defaults to the first-rendered value (the reference's data-default). */
  defaultValue?: number;
  /** Diameter in SVG viewBox units. Default 112 (the reference's macro size). */
  size?: number;
  /** Accessible name, e.g. "Macro 1 Echo". */
  label?: string;
  disabled?: boolean;
}

/**
 * Dual-ring macro knob (reference `macroSvg` + `initKnob`'s isMacro branch).
 * Outer ring: ghost 270° track, a brighter accent segment marking the mapped
 * range (rangeA..rangeB as dash length/offset), and a tick rotated to the
 * MAPPED value. Inner ring: the standard interactive knob over 0–100%, with
 * the exact Knob interaction canon (drag / Shift fine / wheel / double-click
 * / arrows). Accent color comes from the surrounding card's --pb-accent.
 */
export function MacroKnob({
  value,
  onChange,
  rangeA,
  rangeB,
  onMapped,
  defaultValue,
  size = 112,
  label = "Macro",
  disabled = false,
}: MacroKnobProps) {
  const initialValue = useRef(value);
  const { ref, dragging, handlers } = useKnobInteraction({
    value,
    min: 0,
    max: 100,
    defaultValue: defaultValue ?? initialValue.current,
    onChange,
    disabled,
  });

  const mapped = rangeA + (value / 100) * (rangeB - rangeA);
  const onMappedRef = useRef(onMapped);
  onMappedRef.current = onMapped;
  useEffect(() => {
    onMappedRef.current?.(mapped);
  }, [mapped]);

  const c = size / 2;
  const rO = c - 6;
  const rI = rO - 9;
  const rot = `rotate(135 ${c} ${c})`;
  const frac = valueToFrac(value, 0, 100);
  const read = splitReadout(value, { unit: "%" });

  return (
    <div
      ref={ref}
      className={`pb-knob pb-md${dragging ? " pb-dragging" : ""}`}
      style={knobSizeStyle(size)}
      role="slider"
      tabIndex={disabled ? -1 : 0}
      aria-label={label}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={value}
      aria-valuetext={formatValue(value, { unit: "%" })}
      aria-disabled={disabled || undefined}
      {...handlers}
    >
      <svg viewBox={`0 0 ${size} ${size}`} aria-hidden="true">
        <circle
          className="pb-k-otrack"
          cx={c}
          cy={c}
          r={rO}
          transform={rot}
          pathLength={360}
          strokeDasharray="270 360"
          fill="none"
        />
        <circle
          className="pb-k-orange"
          cx={c}
          cy={c}
          r={rO}
          transform={rot}
          pathLength={360}
          strokeDasharray={`${((rangeB - rangeA) / 100) * SWEEP} 360`}
          strokeDashoffset={`${-(rangeA / 100) * SWEEP}`}
          fill="none"
        />
        <line
          className="pb-k-otick"
          x1={c}
          y1={c - rO - 4}
          x2={c}
          y2={c - rO + 4}
          transform={pointerTransform(mapped / 100, c)}
        />
        <ArcPair c={c} r={rI} frac={frac} hideLenBelow={KNOB_ARC_HIDE_LEN} />
        <KnobPointer c={c} frac={frac} y1={c - rI + 6} y2={c - rI * 0.5} />
      </svg>
      <div className="pb-k-read">
        <span className="pb-k-num pb-num">{read.num}</span>
        <span className="pb-k-unit">{read.unit}</span>
      </div>
    </div>
  );
}
