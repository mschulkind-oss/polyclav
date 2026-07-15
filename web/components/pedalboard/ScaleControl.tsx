"use client";

import { useCallback, useEffect, useState } from "react";

/** The UI-scale ladder (reference SCALES) — legal values for --pb-scale. */
export const SCALES = [0.8, 0.9, 1, 1.1, 1.2, 1.3, 1.4] as const;

const SCALE_KEY = "polyclav-ui-scale";
const DEFAULT_IX = SCALES.indexOf(1);

/** Index of the ladder step closest to `scale`. */
function nearestIx(scale: number): number {
  let best = 0;
  for (let i = 1; i < SCALES.length; i++) {
    if (Math.abs(SCALES[i] - scale) < Math.abs(SCALES[best] - scale)) best = i;
  }
  return best;
}

export interface UiScale {
  /** Current scale — the page sets it inline on .pb-root as --pb-scale. */
  scale: number;
  inc: () => void;
  dec: () => void;
  /** Back to 100%. */
  reset: () => void;
  /** Snap to the ladder step nearest `v` (wires ScaleControl's onScale). */
  set: (v: number) => void;
}

/**
 * Owns the UI-scale ladder, persisted to localStorage under
 * "polyclav-ui-scale" (reference applyScale). The stored value is read in an
 * effect so statically-exported HTML hydrates cleanly at the default 100%.
 */
export function useUiScale(): UiScale {
  const [ix, setIx] = useState(DEFAULT_IX);

  useEffect(() => {
    const raw = window.localStorage.getItem(SCALE_KEY);
    if (raw === null) return;
    const stored = (SCALES as readonly number[]).indexOf(Number.parseFloat(raw));
    if (stored >= 0) setIx(stored);
  }, []);

  useEffect(() => {
    window.localStorage.setItem(SCALE_KEY, String(SCALES[ix]));
  }, [ix]);

  const inc = useCallback(() => setIx((i) => Math.min(SCALES.length - 1, i + 1)), []);
  const dec = useCallback(() => setIx((i) => Math.max(0, i - 1)), []);
  const reset = useCallback(() => setIx(DEFAULT_IX), []);
  const set = useCallback((v: number) => setIx(nearestIx(v)), []);

  return { scale: SCALES[ix], inc, dec, reset, set };
}

export interface ScaleControlProps {
  /** Current --pb-scale value (controlled; one of SCALES). */
  scale: number;
  /** Receives the next ladder value on A− / A+ / readout-reset clicks. */
  onScale: (v: number) => void;
}

/**
 * A− / percent / A+ control (reference `.scalectl`) — "the people can
 * choose" knob for the whole interface. Controlled: pair it with
 * useUiScale() (`onScale={set}`) and put `--pb-scale: scale` inline on
 * .pb-root; every dimension in the system derives from it.
 */
export function ScaleControl({ scale, onScale }: ScaleControlProps) {
  const ix = nearestIx(scale);
  return (
    <div className="pb-scalectl" title="UI scale — everything resizes together">
      <button
        type="button"
        aria-label="Smaller UI"
        onClick={() => onScale(SCALES[Math.max(0, ix - 1)])}
      >
        A−
      </button>
      <button
        type="button"
        className="pb-scale-val pb-num"
        title="Click to reset to 100%"
        onClick={() => onScale(1)}
      >
        {Math.round(scale * 100)}%
      </button>
      <button
        type="button"
        aria-label="Larger UI"
        onClick={() => onScale(SCALES[Math.min(SCALES.length - 1, ix + 1)])}
      >
        A+
      </button>
    </div>
  );
}
