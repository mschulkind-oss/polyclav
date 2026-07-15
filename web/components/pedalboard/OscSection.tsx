"use client";

import { Led } from "@/components/pedalboard/Led";
import { OctaveStepper } from "@/components/pedalboard/OctaveStepper";
import { OscScopeViz } from "@/components/pedalboard/OscScopeViz";
import { RoleGlyph } from "@/components/pedalboard/RoleGlyph";
import { MiniKnob } from "@/components/pedalboard/synthExternals";
import { WaveSelector } from "@/components/pedalboard/WaveSelector";
import { formatValue } from "@/lib/pedalboard/knobMath";
import { type OscWave, SYNTH } from "@/lib/pedalboard/model";

export interface OscValues {
  wave: OscWave;
  octave: number;
  detune: number;
  level: number;
}

export type OscField = keyof OscValues;

export interface OscSectionProps {
  /** Current values, one entry per SYNTH.oscs. */
  oscs: readonly OscValues[];
  /**
   * Change requests. Wave and octave fire from their controls; detune and
   * level render as display-only MiniKnobs (the design system's minis take
   * no input), so those fields fire only from a future editor surface.
   */
  onChange: <F extends OscField>(ix: number, field: F, v: OscValues[F]) => void;
}

/** Detune readout in cents, signed like the bipolar knob it labels. */
function formatDetune(v: number): string {
  return `${v > 0 ? "+" : ""}${Math.round(v)} ct`;
}

/** Column headers carry each param's role glyph, straight from the model. */
const COLUMNS = [
  { label: "Oct", role: SYNTH.oscs[0].octave.role },
  { label: "Detune", role: SYNTH.oscs[0].detuneCents.role },
  { label: "Level", role: SYNTH.oscs[0].level.role },
] as const;

/**
 * "Oscillators" card: the scope signature (follows osc 1's wave), a
 * column-header row, and one row per oscillator — wave selector, octave
 * stepper, detune mini (bipolar), level mini.
 */
export function OscSection({ oscs, onChange }: OscSectionProps) {
  const scopeWave = oscs[0]?.wave ?? "saw";
  return (
    <article className="pb-scard pb-synth-osc">
      <div className="pb-scard-top">
        <Led on />
        <h3>Oscillators</h3>
        <span className="pb-slot-ix pb-num">3 OSC</span>
      </div>
      <OscScopeViz wave={scopeWave} />
      <div className="pb-osc-cols" aria-hidden="true">
        <span />
        <span>Wave</span>
        {COLUMNS.map((col) => (
          <span key={col.label}>
            {col.role ? <RoleGlyph role={col.role} /> : null}
            {col.label}
          </span>
        ))}
      </div>
      {SYNTH.oscs.map((spec, ix) => {
        const v = oscs[ix];
        if (!v) return null;
        return (
          <div className="pb-osc-row" key={spec.id}>
            <span className="pb-osc-ix pb-num">{ix + 1}</span>
            <WaveSelector
              label={`Osc ${ix + 1} wave`}
              value={v.wave}
              onChange={(w) => onChange(ix, "wave", w)}
            />
            <OctaveStepper
              label={`Osc ${ix + 1} octave`}
              value={v.octave}
              min={spec.octave.min}
              max={spec.octave.max}
              onChange={(n) => onChange(ix, "octave", n)}
            />
            <div className="pb-param">
              <MiniKnob spec={spec.detuneCents} value={v.detune} />
              <div className="pb-p-val pb-num">{formatDetune(v.detune)}</div>
            </div>
            <div className="pb-param">
              <MiniKnob spec={spec.level} value={v.level} />
              <div className="pb-p-val pb-num">{formatValue(v.level, spec.level)}</div>
            </div>
          </div>
        );
      })}
    </article>
  );
}
