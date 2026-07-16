"use client";

import { Fragment, type ReactNode, useState } from "react";
import { ChorusWaveViz } from "@/components/pedalboard/ChorusWaveViz";
import { CompCurveViz } from "@/components/pedalboard/CompCurveViz";
import { DelayTailViz } from "@/components/pedalboard/DelayTailViz";
import { DriveCurveViz } from "@/components/pedalboard/DriveCurveViz";
import { MasterCard } from "@/components/pedalboard/MasterCard";
import { PedalStrip } from "@/components/pedalboard/PedalStrip";
import { ReorderBar } from "@/components/pedalboard/ReorderBar";
import { ReverbBloomViz } from "@/components/pedalboard/ReverbBloomViz";
import { RoleLegend } from "@/components/pedalboard/RoleLegend";
import { SrcNode } from "@/components/pedalboard/SrcNode";
import { TremOptoViz } from "@/components/pedalboard/TremOptoViz";
import { Wire } from "@/components/pedalboard/Wire";
import { CHAIN, DEFAULT_PEDAL_ORDER, type PedalSpec } from "@/lib/pedalboard/model";
import { normalizeOrder } from "@/lib/pedalboard/order";

export interface PedalboardProps {
  /** A strip asked to open in the editor (click / Enter / Space). */
  onOpenPedal?: (pedalId: string) => void;
  /**
   * Controlled param values keyed by param id ("delay.time_ms", "master.level"
   * …); ids not in the map fall back to the spec defaults.
   */
  values?: Record<string, number>;
  /** Controlled bypass map keyed by pedal id — pair with onToggle. */
  enabled?: Record<string, boolean>;
  /** Fired with the pedal id on stomp instead of flipping internal state. */
  onToggle?: (pedalId: string) => void;
  /** Fired with the param id when any board knob (pedal or master) is turned. */
  onParamChange?: (paramId: string, value: number) => void;
  /** Controlled pedal display order (ids). Omit for internal state. */
  order?: string[];
  /** Fired with the new id order on drag/keyboard reorder — pair with `order`. */
  onReorder?: (order: string[]) => void;
  /** Right-side meta line in the screen header. */
  meta?: ReactNode;
}

/**
 * The reference's opening state: trem and delay start bypassed/parked, comp
 * and reverb start engaged. Exported so pages can seed shared controlled state.
 */
export const INITIAL_ENABLED: Record<string, boolean> = {
  drive: true,
  chorus: true,
  trem: false,
  delay: false,
  comp: true,
  reverb: true,
};

function pedalById(id: string): PedalSpec | undefined {
  return CHAIN.find((p) => p.id === id);
}

function pedalValues(pedal: PedalSpec, overrides?: Record<string, number>): Record<string, number> {
  return Object.fromEntries(pedal.params.map((p) => [p.id, overrides?.[p.id] ?? p.defaultValue]));
}

/** Each pedal's one personal module, driven by its true param values. */
function signatureViz(pedal: PedalSpec, values: Record<string, number>, on: boolean): ReactNode {
  switch (pedal.id) {
    case "drive":
      return <DriveCurveViz amount={values["drive.amount"]} />;
    case "chorus":
      return <ChorusWaveViz rateHz={values["chorus.rate"]} />;
    case "trem":
      return <TremOptoViz rateHz={values["trem.rate"]} active={on && values["trem.depth"] > 0} />;
    case "delay":
      return (
        <DelayTailViz
          timeMs={values["delay.time_ms"]}
          feedback={values["delay.feedback"] / 100}
          live={on}
        />
      );
    case "comp":
      return <CompCurveViz amount={values["comp.amount"]} />;
    case "reverb":
      return (
        <ReverbBloomViz
          decay={values["reverb.decay"]}
          tone={values["reverb.tone"]}
          mix={values["reverb.mix"]}
        />
      );
    default:
      return null;
  }
}

/**
 * The pedalboard screen: a reorder bar, then source node → wires → the pedal
 * strips in display order → the master-out card, all on one shared grid so
 * every role row lines up. Every knob is adjustable in place. Standalone it
 * owns its stomp/value/order state (seeded from spec defaults); pages pass
 * values/enabled/order + the on* callbacks so the editor screen and the board
 * share one source of truth.
 */
export function Pedalboard({
  onOpenPedal,
  values,
  enabled,
  onToggle,
  onParamChange,
  order,
  onReorder,
  meta,
}: PedalboardProps) {
  const [localEnabled, setLocalEnabled] = useState(INITIAL_ENABLED);
  const [localValues, setLocalValues] = useState<Record<string, number>>({});
  const [localOrder, setLocalOrder] = useState<string[]>(DEFAULT_PEDAL_ORDER);
  const enabledMap = enabled ?? localEnabled;
  const valueMap = values ?? localValues;
  const orderIds = normalizeOrder(order ?? localOrder, DEFAULT_PEDAL_ORDER);
  const orderedPedals = orderIds.map(pedalById).filter((p): p is PedalSpec => p !== undefined);

  const toggle = (pedalId: string) => {
    if (onToggle) onToggle(pedalId);
    else setLocalEnabled((prev) => ({ ...prev, [pedalId]: !(prev[pedalId] ?? true) }));
  };
  const changeParam = (paramId: string, value: number) => {
    if (onParamChange) onParamChange(paramId, value);
    else setLocalValues((prev) => ({ ...prev, [paramId]: value }));
  };
  const reorder = (next: string[]) => {
    if (onReorder) onReorder(next);
    else setLocalOrder(next);
  };

  return (
    <>
      <div className="pb-screen-head">
        <div>
          <div className="pb-kicker">Signal chain</div>
          <div className="pb-sub">
            Post-synth chain · {orderedPedals.length} pedals · stereo out
          </div>
        </div>
        <div className="pb-meta pb-num">{meta ?? "48 kHz · 128 smp · stereo"}</div>
      </div>
      <ReorderBar pedals={orderedPedals} enabled={enabledMap} onReorder={reorder} />
      <div className="pb-railwrap">
        <div className="pb-rail">
          <SrcNode />
          <Wire />
          {orderedPedals.map((pedal) => {
            const vals = pedalValues(pedal, valueMap);
            const on = enabledMap[pedal.id] ?? true;
            return (
              <Fragment key={pedal.id}>
                <PedalStrip
                  pedal={pedal}
                  values={vals}
                  enabled={on}
                  onStomp={() => toggle(pedal.id)}
                  onOpen={() => onOpenPedal?.(pedal.id)}
                  extra={signatureViz(pedal, vals, on)}
                  labelOff={pedal.id === "delay" ? "Parked" : undefined}
                  miniSizes={pedal.id === "delay" ? { "delay.time_ms": 44 } : undefined}
                  onParamChange={changeParam}
                />
                <Wire />
              </Fragment>
            );
          })}
          <MasterCard values={valueMap} onParamChange={changeParam} />
        </div>
      </div>
      <p className="pb-rail-hint">
        Drag the chips above to reorder · click a pedal to open it · drag its knobs to tweak in
        place · stomp switches toggle bypass · parked pedals keep their settings
      </p>
      <RoleLegend />
    </>
  );
}
