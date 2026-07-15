"use client";

import { useState } from "react";
import { EnvSection } from "@/components/pedalboard/EnvSection";
import { FilterSection, type FilterValues } from "@/components/pedalboard/FilterSection";
import { HwHintBar, type HwMapping } from "@/components/pedalboard/HwHintBar";
import { LfoSection } from "@/components/pedalboard/LfoSection";
import { type OscField, OscSection, type OscValues } from "@/components/pedalboard/OscSection";
import type { PatchSpec } from "@/lib/pedalboard/model";
import { SYNTH } from "@/lib/pedalboard/model";

export interface SynthPanelProps {
  /**
   * True when the active patch is not native: the VOICE editor renders
   * dimmed and unreachable behind a gate note, mirroring the Launchkey's
   * native-only VOICE gating.
   */
  gated: boolean;
  /** The active patch — its name and type drive the gate note. */
  patch: PatchSpec;
}

const HW_MAPPINGS: HwMapping[] = [
  { k: "K1", label: "Cutoff" },
  { k: "K2", label: "Resonance" },
  { k: "K3", label: "Env Amt" },
  { k: "K4", label: "Kbd Track" },
];

/**
 * The synth (VOICE) screen: Oscillators / Filter / Envelopes / LFO cards in
 * a responsive grid plus the Launchkey hint bar. All values are static mock
 * state seeded from the SYNTH model defaults.
 */
export function SynthPanel({ gated, patch }: SynthPanelProps) {
  const [oscs, setOscs] = useState<OscValues[]>(() =>
    SYNTH.oscs.map((o) => ({
      wave: o.wave,
      octave: o.octave.defaultValue,
      detune: o.detuneCents.defaultValue,
      level: o.level.defaultValue,
    })),
  );
  const [filter, setFilter] = useState<FilterValues>(() => ({
    cutoffPos: SYNTH.filter.cutoffPos.defaultValue,
    resonance: SYNTH.filter.resonance.defaultValue,
    envAmount: SYNTH.filter.envAmount.defaultValue,
    kbdTrack: SYNTH.filter.kbdTrack.defaultValue,
  }));
  const handleOsc = <F extends OscField>(ix: number, field: F, v: OscValues[F]) => {
    setOscs((prev) => prev.map((o, i) => (i === ix ? Object.assign({}, o, { [field]: v }) : o)));
  };

  return (
    <section className="pb-synthpanel">
      {gated ? (
        <p className="pb-gate-note" role="note">
          VOICE editor is native-only — {patch.name} is a {patch.type}. Pick a native patch to edit
          the voice.
        </p>
      ) : null}
      <div
        className={gated ? "pb-synth-body pb-gated" : "pb-synth-body"}
        aria-hidden={gated || undefined}
        inert={gated || undefined}
      >
        <div className="pb-synth-grid">
          <OscSection oscs={oscs} onChange={handleOsc} />
          <FilterSection
            filter={filter}
            onChange={(f, v) => setFilter((p) => ({ ...p, [f]: v }))}
          />
          <EnvSection />
          <LfoSection />
        </div>
        <HwHintBar path="VOICE > Filter" mappings={HW_MAPPINGS} />
      </div>
    </section>
  );
}
