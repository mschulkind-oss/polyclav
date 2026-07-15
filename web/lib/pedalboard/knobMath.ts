/**
 * Pure knob math for the Flat Modern pedalboard UI.
 *
 * Rendering canon (from the reference spec): every arc knob is an SVG circle
 * with pathLength="360" rotated 135deg, so a dasharray of "270 360" draws the
 * 270deg track and a value arc is simply `frac * 270` dash units long. The
 * pointer line is rotated from -135deg (min) to +135deg (max).
 */

import type { ParamSpec } from "@/lib/pedalboard/model";

/** Pointer angle at frac 0, in degrees from 12 o'clock. */
export const A0 = -135;
/** Total sweep of the knob, in degrees. */
export const SWEEP = 270;

export function clamp(v: number, min: number, max: number): number {
  return v < min ? min : v > max ? max : v;
}

/** Normalized position of `v` inside [min, max], clamped to 0..1. */
export function valueToFrac(v: number, min: number, max: number): number {
  return clamp((v - min) / (max - min), 0, 1);
}

/** Pointer rotation in degrees for a 0..1 frac: -135 (min) .. +135 (max). */
export function fracToAngle(f: number): number {
  return A0 + SWEEP * f;
}

export interface ArcDash {
  /** For the .pb-k-arc circle's stroke-dasharray attribute. */
  dasharray: string;
  /** For the .pb-k-arc circle's stroke-dashoffset attribute. */
  dashoffset: string;
}

/**
 * Dash geometry for the value arc on a pathLength-360 circle whose dash
 * pattern starts at the beginning of the 270deg sweep (the rotate(135) trick).
 *
 * Unipolar: the arc grows from the sweep start to `f`.
 * Bipolar: the arc grows from the sweep CENTER toward `f` (either direction).
 * The dash length never drops below 0.01 so the stroke-linecap dot cannot
 * render as a full circle; hide the arc entirely when the value is ~0.
 */
export function arcDash(f: number, bipolar = false): ArcDash {
  let start = 0;
  let len = f * SWEEP;
  if (bipolar) {
    start = Math.min(f, 0.5) * SWEEP;
    len = Math.abs(f - 0.5) * SWEEP;
  }
  return { dasharray: `${Math.max(len, 0.01)} 360`, dashoffset: `${-start}` };
}

/** SVG transform for the pointer line of a knob centered at (c, c). */
export function pointerTransform(f: number, c: number): string {
  return `rotate(${fracToAngle(f)} ${c} ${c})`;
}

/**
 * SVG transform for the gate notch: a small circle placed at (c, c + r)
 * rotated 45deg about the center lands exactly on the sweep start (value 0).
 */
export function gateNotchTransform(c: number): string {
  return `rotate(45 ${c} ${c})`;
}

/** Typographic minus (U+2212), matching the reference's &minus; readouts. */
const MINUS = "−";

/**
 * Display formatting per unit:
 *   "%"  -> rounded int + "%"          (e.g. "15%")
 *   "Hz" -> 1 decimal + " Hz"          (e.g. "0.9 Hz")
 *   "ms" -> rounded + " ms"            (e.g. "380 ms")
 *   "dB" -> 1 decimal, minus sign      (e.g. "−6.0 dB")
 *   ""   -> rounded int, no unit
 * fmt "hzlog" (checked first): position 0-100 -> hz = 20 * 1000^(pos/100);
 * below 1 kHz "460 Hz", at/above "2.4 kHz".
 */
export function formatValue(v: number, spec: Pick<ParamSpec, "unit" | "fmt">): string {
  if (spec.fmt === "hzlog") {
    const hz = 20 * 1000 ** (v / 100);
    return hz < 1000 ? `${Math.round(hz)} Hz` : `${(hz / 1000).toFixed(1)} kHz`;
  }
  switch (spec.unit) {
    case "%":
      return `${Math.round(v)}%`;
    case "Hz":
      return `${v.toFixed(1)} Hz`;
    case "ms":
      return `${Math.round(v)} ms`;
    case "dB": {
      const sign = v < 0 ? MINUS : "";
      return `${sign}${Math.abs(v).toFixed(1)} dB`;
    }
    default:
      return `${Math.round(v)}`;
  }
}

/**
 * Drag canon: ~200 scaled px of vertical travel = one full sweep of the range.
 * `dyPx` is UPWARD-positive pointer movement (startY - currentY).
 * `fine` (Shift) multiplies the travel by 5. `scale` is the current UI scale
 * (--pb-scale), so the feel is identical at every zoom level.
 */
export function dragValue(
  startVal: number,
  dyPx: number,
  min: number,
  max: number,
  fine: boolean,
  scale: number,
): number {
  const travel = 200 * scale * (fine ? 5 : 1);
  return clamp(startVal + (dyPx / travel) * (max - min), min, max);
}

/** Wheel canon: one notch steps 1% of the range (Shift: 0.2%); up = increase. */
export function wheelStep(
  v: number,
  deltaY: number,
  min: number,
  max: number,
  fine: boolean,
): number {
  const step = (max - min) * (fine ? 0.002 : 0.01) * (deltaY < 0 ? 1 : -1);
  return clamp(v + step, min, max);
}

/** Arrow-key canon: 1/100 of the range per press (Shift: 1/500). */
export function keyStep(v: number, dir: 1 | -1, min: number, max: number, fine: boolean): number {
  const step = (max - min) / (fine ? 500 : 100);
  return clamp(v + dir * step, min, max);
}
