"use client";

import { Fragment, type ReactNode, useState } from "react";
import { ChorusWaveViz } from "@/components/pedalboard/ChorusWaveViz";
import { CompCurveViz } from "@/components/pedalboard/CompCurveViz";
import { DelayTailViz } from "@/components/pedalboard/DelayTailViz";
import { DriveCurveViz } from "@/components/pedalboard/DriveCurveViz";
import { MasterCard } from "@/components/pedalboard/MasterCard";
import { PedalStrip, type StripReorder } from "@/components/pedalboard/PedalStrip";
import { ReverbBloomViz } from "@/components/pedalboard/ReverbBloomViz";
import { RoleLegend } from "@/components/pedalboard/RoleLegend";
import { SrcNode } from "@/components/pedalboard/SrcNode";
import { TremOptoViz } from "@/components/pedalboard/TremOptoViz";
import { Wire } from "@/components/pedalboard/Wire";
import { CHAIN, DEFAULT_PEDAL_ORDER, type PedalSpec } from "@/lib/pedalboard/model";
import { moveBy, moveRelative, normalizeOrder } from "@/lib/pedalboard/order";

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
 * The pedalboard screen: source node → wires → the pedal strips in display
 * order → the master-out card, all on one shared grid. Reorder the FX chain by
 * dragging a pedal's top bar onto another (or focus a strip and press ←/→);
 * collapse a pedal to a vertical strip to reclaim horizontal space. Every knob
 * is adjustable in place. Standalone the board owns its stomp/value/order
 * state; pages pass values/enabled/order + the on* callbacks so the editor and
 * the board share one source of truth.
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
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({});
  const [dragId, setDragId] = useState<string | null>(null);
  const [overId, setOverId] = useState<string | null>(null);

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
  const applyOrder = (next: string[]) => {
    if (onReorder) onReorder(next);
    else setLocalOrder(next);
  };
  const toggleCollapse = (id: string) => setCollapsed((c) => ({ ...c, [id]: !c[id] }));

  const stripDrag = (id: string): StripReorder => ({
    dragging: dragId === id,
    dropTarget: overId === id && dragId !== null && dragId !== id,
    position: `${orderIds.indexOf(id) + 1} of ${orderIds.length}`,
    handleProps: {
      draggable: true,
      onDragStart: (e) => {
        setDragId(id);
        e.dataTransfer.setData("text/plain", id);
        e.dataTransfer.effectAllowed = "move";
      },
      onDragEnd: () => {
        setDragId(null);
        setOverId(null);
      },
      onDragOver: (e) => {
        e.preventDefault();
        setOverId(id);
      },
      onDragLeave: () => setOverId((c) => (c === id ? null : c)),
      onDrop: (e) => {
        e.preventDefault();
        const from = dragId ?? e.dataTransfer.getData("text/plain");
        if (from) {
          const rect = e.currentTarget.getBoundingClientRect();
          const after = e.clientX > rect.left + rect.width / 2;
          applyOrder(moveRelative(orderIds, from, id, after));
        }
        setDragId(null);
        setOverId(null);
      },
    },
    onKey: (dir) => applyOrder(moveBy(orderIds, id, dir)),
  });

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
                  collapsed={collapsed[pedal.id] ?? false}
                  onToggleCollapse={() => toggleCollapse(pedal.id)}
                  reorder={stripDrag(pedal.id)}
                />
                <Wire />
              </Fragment>
            );
          })}
          <MasterCard values={valueMap} onParamChange={changeParam} />
        </div>
      </div>
      <p className="pb-rail-hint">
        Drag a pedal's top bar to reorder the FX chain · press ←/→ on a focused pedal · collapse a
        pedal to a strip to reclaim space · click to edit · stomp switches toggle bypass
      </p>
      <RoleLegend />
    </>
  );
}
