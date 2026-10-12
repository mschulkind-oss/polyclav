"use client";

import { type CSSProperties, type ReactNode, type RefObject, useEffect, useState } from "react";
import { ChorusWaveViz } from "@/components/pedalboard/ChorusWaveViz";
import { DriveCurveViz } from "@/components/pedalboard/DriveCurveViz";
import { EchoTailViz } from "@/components/pedalboard/EchoTailViz";
import { Knob } from "@/components/pedalboard/Knob";
import { Led } from "@/components/pedalboard/Led";
import {
  MasteringCurveViz,
  type MasteringCurveVizProps,
} from "@/components/pedalboard/MasteringCurveViz";
import { ReverbBloomViz } from "@/components/pedalboard/ReverbBloomViz";
import { GateDot, ROLE_NAMES, RoleGlyph } from "@/components/pedalboard/RoleGlyph";
import { Stomp } from "@/components/pedalboard/Stomp";
import { TremOptoViz } from "@/components/pedalboard/TremOptoViz";
import { formatValue } from "@/lib/pedalboard/knobMath";
import type { ParamSpec, PedalSpec } from "@/lib/pedalboard/model";
import { MASTER_COMP_PARAMS } from "@/lib/pedalboard/model";
import type { LevelsEvent } from "@/lib/types";

/** Min–max caption under an editor knob, e.g. "1 – 1000 ms" / "0 – 90%". */
export function rangeLabel(spec: ParamSpec): string {
  const span = `${spec.min} – ${spec.max}`;
  if (spec.unit === "%") return `${span}%`;
  if (spec.unit === "") return span;
  return `${span} ${spec.unit}`;
}

/** One-word kind for the hero identity subtitle. */
const PEDAL_KIND: Record<string, string> = {
  drive: "Waveshaper drive",
  chorus: "Stereo chorus",
  trem: "Optical tremolo",
  delay: "Stereo echo",
  comp: "Dynamics",
  reverb: "Algorithmic reverb",
};
/** The pedal's signature knob — rendered oversized (xl) at the head of the row. */
const SIGNATURE_PARAM: Record<string, string> = {
  drive: "drive.amount",
  chorus: "chorus.rate",
  trem: "trem.rate",
  delay: "delay.time_ms",
  comp: "comp.amount",
  reverb: "reverb.mix",
};
/** Caption for the signature tail band. */
const TAIL_LABEL: Record<string, string> = {
  drive: "Drive curve",
  chorus: "Chorus motion",
  trem: "Tremolo motion",
  delay: "Echo tail",
  comp: "Gain curve",
  reverb: "Reverb tail",
};

export interface PedalEditorProps {
  pedal: PedalSpec;
  /** Value per param id; ids not present fall back to the spec defaults. */
  values: Record<string, number>;
  /** false = parked (bypassed, settings kept). */
  enabled: boolean;
  /** Fired with the model param id and new value on any knob change. */
  onChange: (paramId: string, value: number) => void;
  onStomp: () => void;
  onReset: () => void;
  /** Breadcrumb "Pedalboard" link. */
  onBack: () => void;
  /** SSE "levels" sink (Polyclav hook) — drives the live mastering curve. */
  levelsSink?: RefObject<((e: LevelsEvent) => void) | null>;
}

/** dB from a linear value × 1000; a 0 reading (no callback yet) stays silent. */
function milliToDb(milli: number): number {
  if (!(milli > 0)) return Number.NEGATIVE_INFINITY;
  return 20 * Math.log10(milli / 1000);
}

/**
 * The live half of the mastering curve: subscribes to the Polyclav levels sink
 * and feeds the pure viz the operating point and gain meter. With no sink (or
 * before the first callback) the viz falls back to the static law alone.
 */
function MasteringCurveLive({
  levelsSink,
  ...viz
}: MasteringCurveVizProps & {
  levelsSink?: RefObject<((e: LevelsEvent) => void) | null>;
}) {
  const [live, setLive] = useState<{
    inDb: number;
    outDb: number;
    gainDb: number;
    limiterDb: number;
  } | null>(null);
  useEffect(() => {
    if (!levelsSink) return;
    levelsSink.current = (e: LevelsEvent) => {
      setLive({
        inDb: milliToDb(e.comp_in_peak_milli),
        outDb: milliToDb(e.comp_out_peak_milli),
        gainDb: milliToDb(e.comp_min_gain_milli),
        limiterDb: milliToDb(e.limiter_min_gain_milli),
      });
    };
    return () => {
      levelsSink.current = null;
    };
  }, [levelsSink]);
  return <MasteringCurveViz {...viz} {...(live ?? {})} />;
}

/** The pedal's parameter-truthful signature module for the editor tail. */
function tailViz(
  pedal: PedalSpec,
  v: (id: string) => number,
  levelsSink?: RefObject<((e: LevelsEvent) => void) | null>,
): ReactNode {
  switch (pedal.id) {
    case "delay":
      return (
        <EchoTailViz
          timeMs={v("delay.time_ms")}
          feedback={v("delay.feedback")}
          mix={v("delay.mix")}
        />
      );
    case "drive":
      return (
        <div className="pb-editor-viz">
          <DriveCurveViz amount={v("drive.amount")} />
        </div>
      );
    case "chorus":
      return (
        <div className="pb-editor-viz">
          <ChorusWaveViz rateHz={v("chorus.rate")} />
        </div>
      );
    case "trem":
      return (
        <div className="pb-editor-viz">
          <TremOptoViz rateHz={v("trem.rate")} active={v("trem.depth") > 0} />
        </div>
      );
    case "comp":
      return (
        <div className="pb-editor-viz">
          <MasteringCurveLive
            levelsSink={levelsSink}
            glue={v("comp.glue")}
            ratio={v("comp.mstr_ratio")}
            rangeDb={v("comp.mstr_range")}
            headroomDb={v("comp.mstr_headroom")}
            ceilingDb={v("master.ceiling")}
          />
        </div>
      );
    case "reverb":
      return (
        <div className="pb-editor-viz">
          <ReverbBloomViz decay={v("reverb.decay")} tone={v("reverb.tone")} mix={v("reverb.mix")} />
        </div>
      );
    default:
      return null;
  }
}

/**
 * The pedal hero editor (reference screen 2, generalised to every pedal): an
 * identity column with the big stomp + reset, a knob row (the pedal's
 * signature param oversized), and a parameter-truthful signature tail. Fully
 * controlled — all values and state transitions come from props.
 */
export function PedalEditor({
  pedal,
  values,
  enabled,
  onChange,
  onStomp,
  onReset,
  onBack,
  levelsSink,
}: PedalEditorProps) {
  const params = pedal.id === "comp" ? [...pedal.params, ...MASTER_COMP_PARAMS] : pedal.params;
  const v = (id: string): number =>
    values[id] ?? params.find((p) => p.id === id)?.defaultValue ?? 0;
  const sigId = SIGNATURE_PARAM[pedal.id];
  const caption = params.map((p) => `${formatValue(v(p.id), p)} ${p.label}`).join(" · ");
  const accent = { "--pb-accent": `var(${pedal.accentVar})` } as CSSProperties;
  return (
    <>
      <div className="pb-screen-head">
        <div className="pb-crumb">
          <button type="button" className="pb-crumb-link" onClick={onBack}>
            Pedalboard
          </button>
          <span className="pb-sep">/</span>
          <span className="pb-crumb-cur">{pedal.label}</span>
        </div>
        <div className={`pb-chip${enabled ? " pb-live" : ""}`}>
          {enabled ? "Engaged" : "Parked — settings kept"}
        </div>
      </div>

      <article className={`pb-hero${enabled ? "" : " pb-bypassed"}`} style={accent}>
        <div className="pb-hero-main">
          <div className="pb-hero-id">
            <div className="pb-hero-title">
              <h2>{pedal.label.toUpperCase()}</h2>
              <Led on={enabled} />
            </div>
            <div className="pb-hero-rule" />
            <div className="pb-hero-sub">
              {PEDAL_KIND[pedal.id] ?? "Pedal"} · slot {pedal.slot}
            </div>
            <Stomp
              big
              on={enabled}
              labelOff={pedal.id === "delay" ? "Parked" : "Bypassed"}
              onToggle={onStomp}
            />
            <button type="button" className="pb-ghostbtn" onClick={onReset}>
              <svg viewBox="0 0 12 12" aria-hidden="true">
                <path
                  d="M10.2 6a4.2 4.2 0 1 1-1.35-3.1M10.2 .8v2.6H7.6"
                  fill="none"
                  stroke="currentColor"
                  strokeWidth="1.2"
                  strokeLinecap="round"
                  strokeLinejoin="round"
                />
              </svg>
              Reset to defaults
            </button>
          </div>

          <div>
            <div className="pb-hero-knobs">
              {params.map((spec) => {
                const isSig = spec.id === sigId;
                return (
                  <div key={spec.id} className="pb-kgroup">
                    <div className="pb-k-label" title={ROLE_NAMES[spec.role]}>
                      <RoleGlyph role={spec.role} />
                      {spec.label}
                      {spec.gate ? <GateDot /> : null}
                    </div>
                    <Knob
                      spec={spec}
                      value={v(spec.id)}
                      onChange={(val) => onChange(spec.id, val)}
                      size={isSig ? 164 : 124}
                      sizeClass={isSig ? "xl" : "lg"}
                    />
                    <div className="pb-k-minmax pb-num">{rangeLabel(spec)}</div>
                  </div>
                );
              })}
            </div>
            <div className="pb-knob-hint">
              Drag vertically · shift = fine · scroll steps · double-click resets
            </div>
          </div>
        </div>

        <div className="pb-hero-tail">
          <div className="pb-tail-head">
            <span className="pb-kicker">{TAIL_LABEL[pedal.id] ?? "Response"}</span>
            <span className="pb-tail-caption pb-num">{caption}</span>
          </div>
          {tailViz(pedal, v, levelsSink)}
        </div>
      </article>
    </>
  );
}
