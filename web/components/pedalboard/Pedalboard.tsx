"use client";

import { Fragment, type ReactNode, useState } from "react";
import { BusCard } from "@/components/pedalboard/BusCard";
import { ChorusWaveViz } from "@/components/pedalboard/ChorusWaveViz";
import { DelayTailViz } from "@/components/pedalboard/DelayTailViz";
import { DriveCurveViz } from "@/components/pedalboard/DriveCurveViz";
import { PedalStrip } from "@/components/pedalboard/PedalStrip";
import { RoleLegend } from "@/components/pedalboard/RoleLegend";
import { SrcNode } from "@/components/pedalboard/SrcNode";
import { TremOptoViz } from "@/components/pedalboard/TremOptoViz";
import { Wire } from "@/components/pedalboard/Wire";
import { CHAIN, type PedalSpec } from "@/lib/pedalboard/model";

export interface PedalboardProps {
  /** A strip asked to open in the editor (click / Enter / Space). */
  onOpenPedal?: (pedalId: string) => void;
  /**
   * Controlled param values keyed by param id ("delay.time_ms" …); ids not in
   * the map fall back to the spec defaults. Omit for the static mock values.
   */
  values?: Record<string, number>;
  /** Controlled bypass map keyed by pedal id — pair with onToggle. */
  enabled?: Record<string, boolean>;
  /** Fired with the pedal id on stomp instead of flipping internal state. */
  onToggle?: (pedalId: string) => void;
  /**
   * Fired with the param id ("delay.time_ms", "bus.gain" …) when any board
   * knob is turned, instead of updating internal state — pair with `values`.
   */
  onParamChange?: (paramId: string, value: number) => void;
}

/**
 * The reference's opening state: trem and delay start bypassed/parked.
 * Exported so the playground page can seed its shared controlled state.
 */
export const INITIAL_ENABLED: Record<string, boolean> = {
  drive: true,
  chorus: true,
  trem: false,
  delay: false,
};

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
    default:
      return null;
  }
}

/**
 * The pedalboard screen (reference screen 1): source node → wires → the four
 * pedal strips with their signature modules → stereo bus, all on one shared
 * grid so every role row lines up, plus the rail hint and the role legend.
 * Every knob on the board is adjustable in place — the editor is never
 * required just to turn one. Standalone it owns its stomp and value state
 * (seeded from the spec defaults); the playground page passes
 * values/enabled/onToggle/onParamChange so the editor screen and the board
 * share one source of truth.
 */
export function Pedalboard({
  onOpenPedal,
  values,
  enabled,
  onToggle,
  onParamChange,
}: PedalboardProps) {
  const [localEnabled, setLocalEnabled] = useState(INITIAL_ENABLED);
  const [localValues, setLocalValues] = useState<Record<string, number>>({});
  const enabledMap = enabled ?? localEnabled;
  const valueMap = values ?? localValues;
  const toggle = (pedalId: string) => {
    if (onToggle) onToggle(pedalId);
    else setLocalEnabled((prev) => ({ ...prev, [pedalId]: !(prev[pedalId] ?? true) }));
  };
  const changeParam = (paramId: string, value: number) => {
    if (onParamChange) onParamChange(paramId, value);
    else setLocalValues((prev) => ({ ...prev, [paramId]: value }));
  };
  return (
    <>
      <div className="pb-screen-head">
        <div>
          <div className="pb-kicker">Signal chain</div>
          <div className="pb-sub">Post-synth chain · 4 pedals · stereo bus</div>
        </div>
        <div className="pb-meta pb-num">48 kHz · 128 smp · CPU 3.1%</div>
      </div>
      <div className="pb-railwrap">
        <div className="pb-rail">
          <SrcNode />
          <Wire />
          {CHAIN.map((pedal) => {
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
          <BusCard values={valueMap} onParamChange={changeParam} />
        </div>
      </div>
      <p className="pb-rail-hint">
        Click a pedal to open it in the editor · drag its knobs to tweak in place · stomp switches
        toggle bypass · parked pedals keep their settings
      </p>
      <RoleLegend />
    </>
  );
}
