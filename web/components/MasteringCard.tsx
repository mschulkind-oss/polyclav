"use client";

import { api } from "@/lib/api";
import { fmtDb } from "@/lib/format";
import { SliderRow } from "./SliderRow";

interface MasteringCardProps {
  compAmount: number | undefined;
  limiterCeilingDb: number | undefined;
}

/** Mastering chain: bus compressor amount + limiter ceiling. */
export function MasteringCard({ compAmount, limiterCeilingDb }: MasteringCardProps) {
  return (
    <>
      <SliderRow
        label="Comp amount"
        min={0}
        max={1}
        step={0.01}
        value={compAmount}
        onSend={(v) => api.patchMastering({ comp_amount: v })}
      />
      <SliderRow
        label="Ceiling"
        min={-12}
        max={0}
        step={0.1}
        value={limiterCeilingDb}
        format={fmtDb}
        onSend={(v) => api.patchMastering({ limiter_ceiling_db: v })}
      />
    </>
  );
}
