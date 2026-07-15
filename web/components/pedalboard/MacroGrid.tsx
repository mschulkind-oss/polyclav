"use client";

import { useState } from "react";
import { MacroCard } from "@/components/pedalboard/MacroCard";

/** Launchkey macro knobs M1–M8. */
const MACRO_SLOTS = 8;

interface MacroSlotDef {
  name: string;
  targetLabel: string;
  accentVar: string;
  rangeA: number;
  rangeB: number;
  defaultValue: number;
}

/** The mock assignments from the reference spec (M4–M8 dormant). */
export const MACRO_ASSIGNMENTS: MacroSlotDef[] = [
  {
    name: "Echo",
    targetLabel: "Delay · Mix",
    accentVar: "--pb-mint",
    rangeA: 0,
    rangeB: 60,
    defaultValue: 64,
  },
  {
    name: "Shimmer",
    targetLabel: "Chorus · Depth",
    accentVar: "--pb-cyan",
    rangeA: 0,
    rangeB: 100,
    defaultValue: 31,
  },
  {
    name: "Grit",
    targetLabel: "Drive · Amount",
    accentVar: "--pb-amber",
    rangeA: 0,
    rangeB: 80,
    defaultValue: 78,
  },
];

/** 1-based hardware slot numbers M1…M8 — stable identity for keys. */
const SLOT_NUMBERS = Array.from({ length: MACRO_SLOTS }, (_, i) => i + 1);

/**
 * The macros screen: head with the assigned/free tally plus the 8-slot grid.
 * Holds the macro sweep positions as local playground state so the assigned
 * knobs (and their mapped readouts) stay interactive with static mock data.
 */
export function MacroGrid() {
  const [values, setValues] = useState<number[]>(() =>
    MACRO_ASSIGNMENTS.map((m) => m.defaultValue),
  );
  const free = MACRO_SLOTS - MACRO_ASSIGNMENTS.length;
  return (
    <>
      <div className="pb-screen-head">
        <div>
          <div className="pb-kicker">Macros</div>
          <div className="pb-sub">
            Launchkey macro knobs M1–M8 · each sweep is scaled to its mapped range
          </div>
        </div>
        <div className="pb-meta pb-num">
          {MACRO_ASSIGNMENTS.length} assigned · {free} free
        </div>
      </div>
      <div className="pb-macro-grid">
        {SLOT_NUMBERS.map((slot) => {
          const def = MACRO_ASSIGNMENTS[slot - 1];
          return (
            <MacroCard
              key={`m${slot}`}
              slotIx={slot}
              assigned={
                def
                  ? {
                      ...def,
                      value: values[slot - 1],
                      onChange: (v) =>
                        setValues((prev) => prev.map((x, j) => (j === slot - 1 ? v : x))),
                    }
                  : undefined
              }
            />
          );
        })}
      </div>
    </>
  );
}
