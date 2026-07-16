"use client";

import type { CSSProperties } from "react";
import { GhostKnob } from "@/components/pedalboard/GhostKnob";
import { MacroKnob } from "@/components/pedalboard/MacroKnob";
import { clamp, formatValue } from "@/lib/pedalboard/knobMath";
import { CHAIN, MASTER_PARAMS, type ParamSpec } from "@/lib/pedalboard/model";
import type { Macro } from "@/lib/types";

const SLOTS = 8;

interface Target {
  id: string;
  group: string;
  label: string;
  accentVar: string;
  spec: ParamSpec;
}

/** Every assignable board control, grouped by pedal + the master card. */
const MACRO_TARGETS: Target[] = [
  ...CHAIN.flatMap((p) =>
    p.params.map(
      (spec): Target => ({
        id: spec.id,
        group: p.label,
        label: `${p.label} · ${spec.label}`,
        accentVar: p.accentVar,
        spec,
      }),
    ),
  ),
  ...MASTER_PARAMS.map(
    (spec): Target => ({
      id: spec.id,
      group: "Master",
      label: `Master · ${spec.label}`,
      accentVar: "--pb-neutral",
      spec,
    }),
  ),
];
const TARGET_BY_ID: Record<string, Target> = Object.fromEntries(
  MACRO_TARGETS.map((t) => [t.id, t]),
);
const GROUPS = [...new Set(MACRO_TARGETS.map((t) => t.group))];

function safeNum(raw: string, fallback: number): number {
  const n = Number(raw);
  return Number.isFinite(n) ? clamp(n, 0, 100) : fallback;
}

export interface MacrosScreenProps {
  macros: Macro[];
  onSetMacros: (macros: Macro[]) => void;
  /** Current display value per board param id (drives the live knob position). */
  values: Record<string, number>;
  /** Set a board param (display units) — the macro knob drives its target. */
  setParam: (paramId: string, value: number) => void;
}

/**
 * The macros screen: eight slots, each assignable to any board control. min/max
 * are a sub-range of the target's full range in percent; the knob sweeps within
 * it and drives the target param live (via the shared wiring). Assignments are
 * daemon-persisted; the live sweep is derived from the target's current value.
 */
export function MacrosScreen({ macros, onSetMacros, values, setParam }: MacrosScreenProps) {
  const bySlot = new Map(macros.filter((m) => m.target).map((m) => [m.slot, m]));
  const assigned = bySlot.size;

  const commit = (slot: number, patch: Partial<Macro> | null) => {
    const map = new Map(bySlot);
    if (patch === null || patch.target === "") {
      map.delete(slot);
    } else {
      const cur = map.get(slot) ?? { slot, target: "", name: "", min: 0, max: 100 };
      map.set(slot, { ...cur, slot, ...patch });
    }
    onSetMacros([...map.values()].sort((a, b) => a.slot - b.slot));
  };

  return (
    <>
      <div className="pb-screen-head">
        <div>
          <div className="pb-kicker">Macros</div>
          <div className="pb-sub">
            Assign a macro to any board control; the knob sweeps its mapped range live
          </div>
        </div>
        <div className="pb-meta pb-num">
          {assigned} assigned · {SLOTS - assigned} free
        </div>
      </div>
      <div className="pb-macro-grid">
        {Array.from({ length: SLOTS }, (_, i) => i + 1).map((slot) => {
          const m = bySlot.get(slot);
          const target = m?.target ? TARGET_BY_ID[m.target] : undefined;
          const macroMin = m?.min ?? 0;
          const macroMax = m?.max ?? 100;
          const accent = target?.accentVar ?? "--pb-neutral";

          // Live sweep: where the target currently sits inside [macroMin, macroMax].
          let sweep = 0;
          let mappedVal = 0;
          if (target) {
            const { spec } = target;
            const full = spec.max - spec.min;
            const curPct =
              full === 0 ? 0 : ((values[spec.id] ?? spec.min) - spec.min) * (100 / full);
            sweep =
              macroMax === macroMin
                ? 0
                : clamp(((curPct - macroMin) / (macroMax - macroMin)) * 100, 0, 100);
            mappedVal = values[spec.id] ?? spec.min;
          }
          const drive = (sw: number) => {
            if (!target) return;
            const { spec } = target;
            const pct = macroMin + (sw / 100) * (macroMax - macroMin);
            setParam(spec.id, spec.min + (pct / 100) * (spec.max - spec.min));
          };

          const targetSelect = (
            <select
              className="pb-m-select"
              value={m?.target ?? ""}
              aria-label={`Macro ${slot} target`}
              onChange={(e) => {
                const id = e.target.value;
                commit(slot, id ? { target: id, min: 0, max: 100 } : { target: "" });
              }}
            >
              <option value="">— assign target —</option>
              {GROUPS.map((g) => (
                <optgroup key={g} label={g}>
                  {MACRO_TARGETS.filter((t) => t.group === g).map((t) => (
                    <option key={t.id} value={t.id}>
                      {t.label}
                    </option>
                  ))}
                </optgroup>
              ))}
            </select>
          );

          if (!target) {
            return (
              <article key={slot} className="pb-macro pb-dormant">
                <div className="pb-m-head">
                  <span className="pb-m-chip">M{slot}</span>
                  <span className="pb-m-name">Unassigned</span>
                </div>
                <GhostKnob size={112} />
                <div className="pb-m-target">
                  <span className="pb-t-dot" />
                  {targetSelect}
                </div>
                <div className="pb-m-rows">
                  <span>Range —</span>
                  <span>Pick a target</span>
                </div>
              </article>
            );
          }

          return (
            <article
              key={slot}
              className="pb-macro"
              style={{ "--pb-accent": `var(${accent})` } as CSSProperties}
            >
              <div className="pb-m-head">
                <span className="pb-m-chip">M{slot}</span>
                <input
                  className="pb-m-nameinput"
                  value={m?.name ?? ""}
                  placeholder="Name"
                  aria-label={`Macro ${slot} name`}
                  onChange={(e) => commit(slot, { name: e.target.value })}
                />
              </div>
              <MacroKnob
                value={sweep}
                onChange={drive}
                rangeA={macroMin}
                rangeB={macroMax}
                size={112}
                label={`Macro ${slot} ${m?.name || target.label}`}
              />
              <div className="pb-m-target">
                <span className="pb-t-dot" />
                {targetSelect}
              </div>
              <div className="pb-m-rows">
                <span className="pb-m-range">
                  <input
                    type="number"
                    className="pb-m-num pb-num"
                    value={macroMin}
                    aria-label="Range min percent"
                    onChange={(e) => commit(slot, { min: safeNum(e.target.value, macroMin) })}
                  />
                  <span>–</span>
                  <input
                    type="number"
                    className="pb-m-num pb-num"
                    value={macroMax}
                    aria-label="Range max percent"
                    onChange={(e) => commit(slot, { max: safeNum(e.target.value, macroMax) })}
                  />
                  <span>%</span>
                </span>
                <span className="pb-m-mapped pb-num">→ {formatValue(mappedVal, target.spec)}</span>
              </div>
            </article>
          );
        })}
      </div>
    </>
  );
}
