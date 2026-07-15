"use client";

import { OSC_WAVES, type OscWave } from "@/lib/pedalboard/model";

/** One-period wave icons, SVG user units (rendered size via .pb-wavebtn svg). */
const ICONS: Record<OscWave, string> = {
  saw: "M1 9 L6 3 V9 L11 3",
  square: "M1 9 V3 H6 V9 H11 V3",
  tri: "M1 6 L3.5 3 L8.5 9 L11 6",
  pulse: "M1 9 V3 H4 V9 H10 V3 H11",
};

export interface WaveSelectorProps {
  value: OscWave;
  onChange: (wave: OscWave) => void;
  /** Accessible group label, e.g. "Osc 1 wave". */
  label: string;
}

/**
 * Segmented waveform selector: four icon buttons, one per OSC_WAVES entry,
 * with `aria-pressed` marking the active wave.
 */
export function WaveSelector({ value, onChange, label }: WaveSelectorProps) {
  return (
    // biome-ignore lint/a11y/useSemanticElements: a segmented toggle strip, not a form-control group — fieldset semantics/styling don't apply (same precedent as RoleLegend)
    <div className="pb-waveseg" role="group" aria-label={label}>
      {OSC_WAVES.map((w) => (
        <button
          key={w}
          type="button"
          className="pb-wavebtn"
          aria-pressed={w === value}
          aria-label={w}
          title={w}
          onClick={() => onChange(w)}
        >
          <svg viewBox="0 0 12 12" aria-hidden="true">
            <path
              d={ICONS[w]}
              fill="none"
              stroke="currentColor"
              strokeWidth="1.2"
              strokeLinecap="round"
              strokeLinejoin="round"
            />
          </svg>
        </button>
      ))}
    </div>
  );
}
