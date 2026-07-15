"use client";

import type { MouseEvent, PointerEvent as ReactPointerEvent } from "react";
import {
  ArcPair,
  KnobPointer,
  knobSizeStyle,
  MINI_ARC_HIDE_LEN,
  useKnobInteraction,
} from "@/components/pedalboard/knobCore";
import { formatValue, valueToFrac } from "@/lib/pedalboard/knobMath";
import type { ParamSpec } from "@/lib/pedalboard/model";

/** The big Knob's default diameter — the drag-travel canon is calibrated to it. */
const KNOB_REFERENCE_SIZE = 120;

export interface MiniKnobProps {
  spec: ParamSpec;
  value: number;
  /** Diameter in SVG viewBox units. Default 36; the delay strip's Time mini uses 44. */
  size?: number;
  /**
   * Make the mini adjustable in place: the full knob interaction canon with
   * drag travel scaled down to the mini's diameter, plus slider semantics.
   * Omit for the classic display-only, aria-hidden readout.
   */
  onChange?: (v: number) => void;
}

/**
 * Mini knob (reference `miniSvg`): the pedal strips' and bus card's value
 * indicators. Without `onChange` it is display-only — no interaction, no tab
 * stop — and the adjacent .pb-p-val text carries the value, so the whole
 * thing is aria-hidden. With `onChange` it becomes a real slider so the board
 * is adjustable without opening the editor; it lives inside clickable strips,
 * so pointerdown/click/double-click never bubble up to the open-editor
 * handler (the Stomp pattern). `spec.bipolar` arcs grow from the sweep center
 * (bus gain); the arc hides once its dash length drops under 0.75 so the
 * round linecap never lingers.
 */
export function MiniKnob({ spec, value, size = 36, onChange }: MiniKnobProps) {
  if (onChange) {
    return <InteractiveMini spec={spec} value={value} size={size} onChange={onChange} />;
  }
  return (
    <div className="pb-mini" style={knobSizeStyle(size)} aria-hidden="true">
      <MiniSvg spec={spec} value={value} size={size} />
    </div>
  );
}

/** The shared arc + pointer SVG, identical in both modes. */
function MiniSvg({ spec, value, size }: { spec: ParamSpec; value: number; size: number }) {
  const c = size / 2;
  const r = c - 3.5;
  const frac = valueToFrac(value, spec.min, spec.max);
  return (
    <svg viewBox={`0 0 ${size} ${size}`} aria-hidden="true">
      <ArcPair c={c} r={r} frac={frac} bipolar={spec.bipolar} hideLenBelow={MINI_ARC_HIDE_LEN} />
      <KnobPointer c={c} frac={frac} y1={c - r + 3} y2={c - r * 0.42} />
    </svg>
  );
}

/**
 * The adjustable flavor: full interaction canon via useKnobInteraction
 * (pointer-capture vertical drag with travel scaled to the mini's size,
 * Shift = 5× fine, wheel steps on a non-passive listener, double-click =
 * spec default, arrow keys) plus the ARIA slider contract.
 */
function InteractiveMini({
  spec,
  value,
  size,
  onChange,
}: {
  spec: ParamSpec;
  value: number;
  size: number;
  onChange: (v: number) => void;
}) {
  const { ref, dragging, handlers } = useKnobInteraction({
    value,
    min: spec.min,
    max: spec.max,
    defaultValue: spec.defaultValue,
    onChange,
    disabled: false,
    travelScale: size / KNOB_REFERENCE_SIZE,
  });

  // Containment: the mini sits inside a card whose click opens the editor.
  // Stop pointerdown and click at the knob root so turning a knob (or the
  // click a completed drag synthesizes) never opens the editor.
  const onPointerDown = (e: ReactPointerEvent<HTMLDivElement>) => {
    e.stopPropagation();
    handlers.onPointerDown(e);
  };
  const onClick = (e: MouseEvent<HTMLDivElement>) => {
    e.stopPropagation();
  };
  const onDoubleClick = (e: MouseEvent<HTMLDivElement>) => {
    e.stopPropagation();
    handlers.onDoubleClick();
  };

  return (
    <div
      ref={ref}
      className={dragging ? "pb-mini pb-dragging" : "pb-mini"}
      style={{ ...knobSizeStyle(size), cursor: "ns-resize", touchAction: "none" }}
      role="slider"
      tabIndex={0}
      aria-label={spec.label}
      aria-valuemin={spec.min}
      aria-valuemax={spec.max}
      aria-valuenow={value}
      aria-valuetext={formatValue(value, spec)}
      {...handlers}
      onPointerDown={onPointerDown}
      onClick={onClick}
      onDoubleClick={onDoubleClick}
      onKeyDown={handlers.onKeyDown}
    >
      <MiniSvg spec={spec} value={value} size={size} />
    </div>
  );
}
