// Value formatters shared by the sliders and readouts. Mirrors the
// interim dashboard's fmt* helpers so readouts look identical.

export const fmt2 = (v: number): string => v.toFixed(2);

export const fmtHz = (hz: number): string =>
  hz >= 1000 ? `${(hz / 1000).toFixed(2)} kHz` : `${Math.round(hz)} Hz`;

export const fmtDb = (v: number): string => `${v.toFixed(1)} dB`;

export const fmtTempo = (t: number): string => `${t.toFixed(2)}×`;

export const fmtSec = (v: number): string =>
  v >= 1 ? `${v.toFixed(2)} s` : `${Math.round(v * 1000)} ms`;

export const fmtCents = (v: number): string => `${v > 0 ? "+" : ""}${Math.round(v)}¢`;
