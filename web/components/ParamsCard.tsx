"use client";

import { api } from "@/lib/api";
import { fmtHz } from "@/lib/format";
import { SliderRow } from "./SliderRow";

interface ParamsCardProps {
  volume: number | undefined;
  reverb: number | undefined;
  compressor: number | undefined;
  cutoffPos: number | undefined;
  cutoffHz: number | undefined;
  /** Cutoff is live only on native-synth patches. */
  isNative: boolean;
}

/** Per-patch knobs: volume / reverb / compressor / (native-only) cutoff. */
export function ParamsCard({
  volume,
  reverb,
  compressor,
  cutoffPos,
  cutoffHz,
  isNative,
}: ParamsCardProps) {
  return (
    <>
      <SliderRow
        label="Volume"
        min={0}
        max={1}
        step={0.01}
        value={volume}
        onSend={(v) => api.patchParams({ volume: v })}
      />
      <SliderRow
        label="Reverb"
        min={0}
        max={1}
        step={0.01}
        value={reverb}
        onSend={(v) => api.patchParams({ reverb: v })}
      />
      <SliderRow
        label="Compressor"
        min={0}
        max={1}
        step={0.01}
        value={compressor}
        onSend={(v) => api.patchParams({ compressor: v })}
      />
      <SliderRow
        label="Cutoff"
        min={0}
        max={1}
        step={0.005}
        value={cutoffPos}
        // The readout is always the SSE-fed frequency, not the 0..1 position.
        valueText={cutoffHz === undefined ? "–" : fmtHz(cutoffHz)}
        disabled={!isNative}
        onSend={(v) => api.patchParams({ cutoff_pos: v })}
      />
      <p className="hint">Cutoff is live only on native-synth patches.</p>
    </>
  );
}
