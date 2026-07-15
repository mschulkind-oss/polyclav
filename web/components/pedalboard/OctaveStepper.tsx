"use client";

import { clamp } from "@/lib/pedalboard/knobMath";

/** Octave display: explicit sign for positive offsets ("+1", "0", "-2"). */
export function formatOctave(v: number): string {
  return v > 0 ? `+${v}` : `${v}`;
}

export interface OctaveStepperProps {
  value: number;
  onChange: (v: number) => void;
  /** Accessible group label, e.g. "Osc 1 octave". */
  label: string;
  /** Integer bounds, default −2 … +2. */
  min?: number;
  max?: number;
}

/**
 * − value + stepper for the oscillator octave offset, clamped to min…max.
 * The buttons disable at the bounds, so the value can never leave the range.
 */
export function OctaveStepper({ value, onChange, label, min = -2, max = 2 }: OctaveStepperProps) {
  return (
    // biome-ignore lint/a11y/useSemanticElements: a − value + stepper cluster, not a form-control group — fieldset semantics/styling don't apply (same precedent as RoleLegend)
    <div className="pb-oct" role="group" aria-label={label}>
      <button
        type="button"
        className="pb-oct-btn"
        aria-label={`${label} down`}
        disabled={value <= min}
        onClick={() => onChange(clamp(value - 1, min, max))}
      >
        &minus;
      </button>
      <span className="pb-oct-val pb-num" aria-live="polite">
        {formatOctave(value)}
      </span>
      <button
        type="button"
        className="pb-oct-btn"
        aria-label={`${label} up`}
        disabled={value >= max}
        onClick={() => onChange(clamp(value + 1, min, max))}
      >
        +
      </button>
    </div>
  );
}
