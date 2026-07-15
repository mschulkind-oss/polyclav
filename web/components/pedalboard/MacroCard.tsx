"use client";

import type { CSSProperties } from "react";
import { GhostKnob } from "@/components/pedalboard/GhostKnob";
import { MacroKnob } from "@/components/pedalboard/MacroKnob";

/** Knob size (SVG user units) shared by assigned and dormant macro slots. */
const MACRO_KNOB_SIZE = 112;

export interface MacroAssignment {
  /** Macro name shown next to the chip, e.g. "Echo". */
  name: string;
  /** Target row text, e.g. "Delay · Mix". */
  targetLabel: string;
  /** CSS custom property carrying the target pedal's accent, e.g. "--pb-mint". */
  accentVar: string;
  /** Mapped range start, in target-percent. */
  rangeA: number;
  /** Mapped range end, in target-percent. */
  rangeB: number;
  /** Knob position 0–100 (the hardware macro sweep). */
  value: number;
  /** Double-click reset position; MacroKnob falls back to the first-rendered value. */
  defaultValue?: number;
  onChange: (value: number) => void;
}

export interface MacroCardProps {
  /** 1-based hardware slot index → chip label "M1"…"M8". */
  slotIx: number;
  /** Omit for a dormant (unassigned) slot. */
  assigned?: MacroAssignment;
}

/** Macro sweep 0–100 mapped into [rangeA, rangeB] target-percent. */
export function mappedValue(rangeA: number, rangeB: number, value: number): number {
  return rangeA + (value / 100) * (rangeB - rangeA);
}

/**
 * One Launchkey macro slot card. Assigned slots render the two-ring MacroKnob
 * (outer ring = mapped range), the target row, and the live mapped readout;
 * dormant slots render a ghost ring and "Assign on hardware" — no interactive
 * control at all.
 */
export function MacroCard({ slotIx, assigned }: MacroCardProps) {
  if (!assigned) {
    return (
      <article className="pb-macro pb-dormant">
        <div className="pb-m-head">
          <span className="pb-m-chip">M{slotIx}</span>
          <span className="pb-m-name">Unassigned</span>
        </div>
        <GhostKnob size={MACRO_KNOB_SIZE} />
        <div className="pb-m-target">
          <span className="pb-t-dot" />
          No target
        </div>
        <div className="pb-m-rows">
          <span>Range —</span>
          <span>Assign on hardware</span>
        </div>
      </article>
    );
  }

  const { name, targetLabel, accentVar, rangeA, rangeB, value, defaultValue, onChange } = assigned;
  const mapped = mappedValue(rangeA, rangeB, value);
  return (
    <article className="pb-macro" style={{ "--pb-accent": `var(${accentVar})` } as CSSProperties}>
      <div className="pb-m-head">
        <span className="pb-m-chip">M{slotIx}</span>
        <span className="pb-m-name">{name}</span>
      </div>
      <MacroKnob
        value={value}
        onChange={onChange}
        rangeA={rangeA}
        rangeB={rangeB}
        defaultValue={defaultValue}
        size={MACRO_KNOB_SIZE}
        label={`Macro ${slotIx} ${name}`}
      />
      <div className="pb-m-target">
        <span className="pb-t-dot" />
        {targetLabel}
      </div>
      <div className="pb-m-rows">
        <span>
          Range {rangeA}–{rangeB}%
        </span>
        <span className="pb-m-mapped pb-num">→ {mapped.toFixed(1)}%</span>
      </div>
    </article>
  );
}
