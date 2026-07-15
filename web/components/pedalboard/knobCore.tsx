"use client";

/**
 * Shared internals for the knob family (Knob / MiniKnob / MacroKnob /
 * GhostKnob). Not part of the public component API — import the components.
 *
 * Everything here is a direct port of the reference spec's SVG builders
 * (`arcPair`, `pointerLine`, `sizeEl`) and its interaction canon (`initKnob`):
 * docs/mockups/pedalboard-style-b-flat-modern.html.
 */

import type {
  CSSProperties,
  KeyboardEvent as ReactKeyboardEvent,
  PointerEvent as ReactPointerEvent,
  RefObject,
} from "react";
import { useEffect, useRef, useState } from "react";
import {
  arcDash,
  dragValue,
  formatValue,
  keyStep,
  pointerTransform,
  SWEEP,
  wheelStep,
} from "@/lib/pedalboard/knobMath";
import type { ParamSpec } from "@/lib/pedalboard/model";

/** Big-knob value arcs hide below this dash length (reference: frac < 0.004). */
export const KNOB_ARC_HIDE_LEN = 0.004 * SWEEP;
/** Mini value arcs hide below this dash length (reference: len < 0.75). */
export const MINI_ARC_HIDE_LEN = 0.75;

/** The reference's `sizeEl`: render `size` viewBox units at calc(var(--u) * size). */
export function knobSizeStyle(size: number): CSSProperties {
  const px = `calc(var(--u) * ${size})`;
  return { width: px, height: px };
}

/**
 * Split a formatValue readout into the big number (`.pb-k-num`) and the small
 * unit line (`.pb-k-unit`) under it: "380 ms" → {num:"380", unit:"ms"},
 * "15%" → {num:"15", unit:"%"}, "2.4 kHz" → {num:"2.4", unit:"kHz"}.
 */
export function splitReadout(
  v: number,
  spec: Pick<ParamSpec, "unit" | "fmt">,
): { num: string; unit: string } {
  const full = formatValue(v, spec);
  const sp = full.lastIndexOf(" ");
  if (sp !== -1) return { num: full.slice(0, sp), unit: full.slice(sp + 1) };
  if (full.endsWith("%")) return { num: full.slice(0, -1), unit: "%" };
  return { num: full, unit: "" };
}

/** Numeric --pb-scale of the enclosing .pb-root, so drag feel is zoom-invariant. */
function readScale(el: HTMLElement): number {
  const root = el.closest(".pb-root");
  if (!root) return 1;
  const n = Number.parseFloat(getComputedStyle(root).getPropertyValue("--pb-scale"));
  return Number.isFinite(n) && n > 0 ? n : 1;
}

export interface ArcPairProps {
  /** Center of the knob (the SVG is a size×size square, c = size / 2). */
  c: number;
  r: number;
  /** Normalized 0..1 value position (valueToFrac). */
  frac: number;
  /** Bipolar arcs grow from the sweep center instead of the start. */
  bipolar?: boolean;
  /** Hide the value arc below this dash length (else the linecap dot lingers). */
  hideLenBelow: number;
}

/**
 * The reference's `arcPair()`: 270° track + value arc, both pathLength-360
 * circles rotated 135° so dash units are degrees of sweep.
 */
export function ArcPair({ c, r, frac, bipolar = false, hideLenBelow }: ArcPairProps) {
  const rot = `rotate(135 ${c} ${c})`;
  const { dasharray, dashoffset } = arcDash(frac, bipolar);
  const len = (bipolar ? Math.abs(frac - 0.5) : frac) * SWEEP;
  return (
    <>
      <circle
        className="pb-k-track"
        cx={c}
        cy={c}
        r={r}
        transform={rot}
        pathLength={360}
        strokeDasharray="270 360"
        fill="none"
      />
      <circle
        className="pb-k-arc"
        cx={c}
        cy={c}
        r={r}
        transform={rot}
        pathLength={360}
        strokeDasharray={dasharray}
        strokeDashoffset={dashoffset}
        fill="none"
        strokeLinecap="round"
        style={{ opacity: len < hideLenBelow ? 0 : 1 }}
      />
    </>
  );
}

export interface KnobPointerProps {
  c: number;
  frac: number;
  /** Outer end of the pointer line (reference: big c−r+6, mini c−r+3). */
  y1: number;
  /** Inner end of the pointer line (reference: big c−r·0.5, mini c−r·0.42). */
  y2: number;
}

/** The reference's `pointerLine()`, rotated −135°..+135° with the value. */
export function KnobPointer({ c, frac, y1, y2 }: KnobPointerProps) {
  return (
    <line
      className="pb-k-ptr"
      x1={c}
      y1={y1.toFixed(1)}
      x2={c}
      y2={y2.toFixed(1)}
      transform={pointerTransform(frac, c)}
    />
  );
}

export interface KnobInteractionOpts {
  value: number;
  min: number;
  max: number;
  /** Double-click resets to this. */
  defaultValue: number;
  onChange: (v: number) => void;
  disabled: boolean;
}

export interface KnobHandlers {
  onPointerDown: (e: ReactPointerEvent<HTMLDivElement>) => void;
  onPointerMove: (e: ReactPointerEvent<HTMLDivElement>) => void;
  onPointerUp: () => void;
  onPointerCancel: () => void;
  onDoubleClick: () => void;
  onKeyDown: (e: ReactKeyboardEvent<HTMLDivElement>) => void;
}

export interface KnobInteraction {
  /** Attach to the knob's root element (also hosts the non-passive wheel listener). */
  ref: RefObject<HTMLDivElement | null>;
  /** True while the pointer is captured — render the .pb-dragging class. */
  dragging: boolean;
  /** Spread onto the knob's root element. */
  handlers: KnobHandlers;
}

/**
 * The full interaction canon from the reference's `initKnob()`: vertical
 * pointer-capture drag (~200 scaled px = one full sweep), Shift = 5× fine,
 * wheel steps 1% (Shift 0.2%; preventDefault needs a non-passive native
 * listener — React registers wheel passively), double-click = default,
 * ArrowUp/Right and ArrowDown/Left step 1/100 of the range (Shift 1/500).
 */
export function useKnobInteraction(opts: KnobInteractionOpts): KnobInteraction {
  const ref = useRef<HTMLDivElement>(null);
  const [dragging, setDragging] = useState(false);
  const dragStart = useRef<{ y: number; value: number } | null>(null);

  // Latest-props ref so the native wheel listener (and long drags) never go stale.
  const live = useRef(opts);
  live.current = opts;

  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const onWheel = (e: WheelEvent) => {
      const s = live.current;
      if (s.disabled) return;
      e.preventDefault();
      s.onChange(wheelStep(s.value, e.deltaY, s.min, s.max, e.shiftKey));
    };
    el.addEventListener("wheel", onWheel, { passive: false });
    return () => el.removeEventListener("wheel", onWheel);
  }, []);

  const endDrag = () => {
    dragStart.current = null;
    setDragging(false);
  };

  const handlers: KnobHandlers = {
    onPointerDown: (e) => {
      if (live.current.disabled) return;
      dragStart.current = { y: e.clientY, value: live.current.value };
      try {
        // jsdom has no pointer capture, and per spec a stale pointerId throws.
        e.currentTarget.setPointerCapture?.(e.pointerId);
      } catch {
        // Capture is best-effort; the drag math works without it.
      }
      setDragging(true);
      e.preventDefault();
    },
    onPointerMove: (e) => {
      const start = dragStart.current;
      const s = live.current;
      if (!start || s.disabled) return;
      const dyUp = start.y - e.clientY;
      s.onChange(
        dragValue(start.value, dyUp, s.min, s.max, e.shiftKey, readScale(e.currentTarget)),
      );
    },
    onPointerUp: endDrag,
    onPointerCancel: endDrag,
    onDoubleClick: () => {
      const s = live.current;
      if (s.disabled) return;
      s.onChange(s.defaultValue);
    },
    onKeyDown: (e) => {
      const s = live.current;
      if (s.disabled) return;
      const dir: 1 | -1 | 0 =
        e.key === "ArrowUp" || e.key === "ArrowRight"
          ? 1
          : e.key === "ArrowDown" || e.key === "ArrowLeft"
            ? -1
            : 0;
      if (dir === 0) return;
      e.preventDefault();
      s.onChange(keyStep(s.value, dir, s.min, s.max, e.shiftKey));
    },
  };

  return { ref, dragging, handlers };
}
