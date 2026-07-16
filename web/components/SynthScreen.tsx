"use client";

import { SliderRow } from "@/components/SliderRow";
import { SynthCard } from "@/components/SynthCard";
import { fmtHz } from "@/lib/format";
import type { Synth } from "@/lib/types";

export interface SynthScreenProps {
  synth: Synth | null;
  isNative: boolean;
  cutoffPos?: number;
  cutoffHz?: number;
  onCutoff: (pos: number) => void;
  patchName: string;
}

/**
 * The Synth screen: the native voice engine, wired live to /api/synth (the
 * schema-driven SynthCard) plus the filter cutoff (a /api/params field, not
 * part of the synth block). Non-native patches show a note instead — the
 * native controls only apply to the native engine.
 */
export function SynthScreen({
  synth,
  isNative,
  cutoffPos,
  cutoffHz,
  onCutoff,
  patchName,
}: SynthScreenProps) {
  return (
    <>
      <div className="pb-screen-head">
        <div>
          <div className="pb-kicker">Voice</div>
          <div className="pb-sub">Native synth engine · oscillators · filter · envelopes · LFO</div>
        </div>
        <div className="pb-meta pb-num">{patchName}</div>
      </div>
      {isNative && synth ? (
        <div className="pb-syswrap">
          <div className="pb-panel pb-panel-wide">
            <div className="pb-panel-head">
              <h3>Filter</h3>
              <span className="pb-panel-sub">native cutoff</span>
            </div>
            <SliderRow
              label="Cutoff"
              min={0}
              max={1}
              step={0.005}
              value={cutoffPos}
              valueText={cutoffHz != null ? fmtHz(cutoffHz) : undefined}
              onSend={onCutoff}
            />
          </div>
          <div className="pb-panel pb-panel-wide">
            <div className="pb-panel-head">
              <h3>Voice parameters</h3>
              <span className="pb-panel-sub">live · /api/synth</span>
            </div>
            <SynthCard synth={synth} />
          </div>
        </div>
      ) : (
        <div className="pb-panel">
          <div className="pb-panel-head">
            <h3>Voice</h3>
          </div>
          <p className="pb-sub">
            The native voice controls appear when a native-synth patch is selected. “{patchName}” is
            a sampled / hosted patch, shaped instead by the pedalboard and master chain.
          </p>
        </div>
      )}
    </>
  );
}
