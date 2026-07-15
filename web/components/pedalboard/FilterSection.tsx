"use client";

import { FilterCurveViz } from "@/components/pedalboard/FilterCurveViz";
import { Led } from "@/components/pedalboard/Led";
import { RoleGlyph } from "@/components/pedalboard/RoleGlyph";
import { Knob, MiniKnob } from "@/components/pedalboard/synthExternals";
import { formatValue } from "@/lib/pedalboard/knobMath";
import { SYNTH } from "@/lib/pedalboard/model";

export type FilterField = "cutoffPos" | "resonance" | "envAmount" | "kbdTrack";
export type FilterValues = Record<FilterField, number>;

export interface FilterSectionProps {
  filter: FilterValues;
  onChange: (field: FilterField, v: number) => void;
}

/** Signed readout for the bipolar env-amount mini. */
function formatSigned(v: number, spec: Parameters<typeof formatValue>[1]): string {
  return v > 0 ? `+${formatValue(v, spec)}` : formatValue(v, spec);
}

/**
 * "Filter" card: interactive cutoff (hzlog) and resonance knobs, env-amount
 * and keyboard-tracking minis, and the signature lowpass-curve viz that
 * re-renders as cutoff/resonance move.
 */
export function FilterSection({ filter, onChange }: FilterSectionProps) {
  const f = SYNTH.filter;
  return (
    <article className="pb-scard">
      <div className="pb-scard-top">
        <Led on />
        <h3>Filter</h3>
        <span className="pb-slot-ix">LP 24</span>
      </div>
      <FilterCurveViz cutoffPos={filter.cutoffPos} resonance={filter.resonance} />
      <div className="pb-filter-knobs">
        <div className="pb-kgroup">
          <div className="pb-k-label">
            <RoleGlyph role={f.cutoffPos.role} />
            Cutoff
          </div>
          <Knob
            spec={f.cutoffPos}
            value={filter.cutoffPos}
            onChange={(v: number) => onChange("cutoffPos", v)}
            size={116}
            sizeClass="md"
          />
          <div className="pb-k-minmax pb-num">20 Hz – 20 kHz</div>
        </div>
        <div className="pb-kgroup">
          <div className="pb-k-label">
            <RoleGlyph role={f.resonance.role} />
            Resonance
          </div>
          <Knob
            spec={f.resonance}
            value={filter.resonance}
            onChange={(v: number) => onChange("resonance", v)}
            size={84}
            sizeClass="md"
          />
          <div className="pb-k-minmax pb-num">0 – 100%</div>
        </div>
      </div>
      <div className="pb-filter-minis">
        <div className="pb-param">
          <MiniKnob spec={f.envAmount} value={filter.envAmount} />
          <div className="pb-p-name">
            <RoleGlyph role={f.envAmount.role} />
            Env Amt
          </div>
          <div className="pb-p-val pb-num">{formatSigned(filter.envAmount, f.envAmount)}</div>
        </div>
        <div className="pb-param">
          <MiniKnob spec={f.kbdTrack} value={filter.kbdTrack} />
          <div className="pb-p-name">
            <RoleGlyph role={f.kbdTrack.role} />
            Kbd Track
          </div>
          <div className="pb-p-val pb-num">{formatValue(filter.kbdTrack, f.kbdTrack)}</div>
        </div>
      </div>
    </article>
  );
}
