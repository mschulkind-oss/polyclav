"use client";

import { EchoTailViz } from "@/components/pedalboard/EchoTailViz";
import { Knob } from "@/components/pedalboard/Knob";
import { Led } from "@/components/pedalboard/Led";
import { GateDot, ROLE_NAMES, RoleGlyph } from "@/components/pedalboard/RoleGlyph";
import { Stomp } from "@/components/pedalboard/Stomp";
import { CHAIN, type ParamSpec, type PedalSpec } from "@/lib/pedalboard/model";

function requirePedal(id: string): PedalSpec {
  const pedal = CHAIN.find((p) => p.id === id);
  if (!pedal) throw new Error(`pedal ${id} missing from CHAIN`);
  return pedal;
}

function requireParam(pedal: PedalSpec, id: string): ParamSpec {
  const param = pedal.params.find((p) => p.id === id);
  if (!param) throw new Error(`param ${id} missing from ${pedal.id}`);
  return param;
}

const DELAY = requirePedal("delay");

export interface PedalEditorValues {
  time: number;
  feedback: number;
  mix: number;
}

interface EditorKnobDef {
  spec: ParamSpec;
  valueKey: keyof PedalEditorValues;
  size: number;
  sizeClass?: "md" | "lg" | "xl";
}

/** The knob row: Time is the delay's signature OVERSIZED knob (xl, 164). */
const KNOBS: EditorKnobDef[] = [
  { spec: requireParam(DELAY, "delay.time_ms"), valueKey: "time", size: 164, sizeClass: "xl" },
  { spec: requireParam(DELAY, "delay.feedback"), valueKey: "feedback", size: 124 },
  { spec: requireParam(DELAY, "delay.mix"), valueKey: "mix", size: 124 },
];

/** Min–max caption under an editor knob, e.g. "1 – 1000 ms" / "0 – 90%". */
export function rangeLabel(spec: ParamSpec): string {
  const span = `${spec.min} – ${spec.max}`;
  if (spec.unit === "%") return `${span}%`;
  if (spec.unit === "") return span;
  return `${span} ${spec.unit}`;
}

export interface PedalEditorProps {
  values: PedalEditorValues;
  /** false = parked (bypassed, settings kept). */
  enabled: boolean;
  /** Fired with the model param id ("delay.time_ms" | "delay.feedback" | "delay.mix"). */
  onChange: (paramId: string, value: number) => void;
  onStomp: () => void;
  onReset: () => void;
  /** Breadcrumb "Pedalboard" link. */
  onBack: () => void;
}

/**
 * The delay hero editor (screen 2 of the reference): identity column with the
 * big stomp, the oversized-Time knob row, and the parameter-truthful echo
 * tail. Fully controlled — all values and state transitions come from props.
 */
export function PedalEditor({
  values,
  enabled,
  onChange,
  onStomp,
  onReset,
  onBack,
}: PedalEditorProps) {
  const caption = `${Math.round(values.time)} ms · ${Math.round(values.feedback)}% feedback · ${Math.round(values.mix)}% mix`;
  return (
    <>
      <div className="pb-screen-head">
        <div className="pb-crumb">
          <button type="button" className="pb-crumb-link" onClick={onBack}>
            Pedalboard
          </button>
          <span className="pb-sep">/</span>
          <span className="pb-crumb-cur">{DELAY.label}</span>
        </div>
        <div className={`pb-chip${enabled ? " pb-live" : ""}`}>
          {enabled ? "Engaged" : "Parked — settings kept"}
        </div>
      </div>

      <article className={`pb-hero${enabled ? "" : " pb-bypassed"}`}>
        <div className="pb-hero-main">
          <div className="pb-hero-id">
            <div className="pb-hero-title">
              <h2>{DELAY.label.toUpperCase()}</h2>
              <Led on={enabled} />
            </div>
            <div className="pb-hero-rule" />
            <div className="pb-hero-sub">Stereo echo · slot {DELAY.slot}</div>
            <Stomp big on={enabled} labelOff="Parked" onToggle={onStomp} />
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
              {KNOBS.map(({ spec, valueKey, size, sizeClass }) => (
                <div key={spec.id} className="pb-kgroup">
                  <div className="pb-k-label" title={ROLE_NAMES[spec.role]}>
                    <RoleGlyph role={spec.role} />
                    {spec.label}
                    {spec.gate ? <GateDot /> : null}
                  </div>
                  <Knob
                    spec={spec}
                    value={values[valueKey]}
                    onChange={(v) => onChange(spec.id, v)}
                    size={size}
                    sizeClass={sizeClass}
                  />
                  <div className="pb-k-minmax pb-num">{rangeLabel(spec)}</div>
                </div>
              ))}
            </div>
            <div className="pb-knob-hint">
              Drag vertically · shift = fine · scroll steps · double-click resets
            </div>
          </div>
        </div>

        <div className="pb-hero-tail">
          <div className="pb-tail-head">
            <span className="pb-kicker">Echo tail</span>
            <span className="pb-tail-caption pb-num">{caption}</span>
          </div>
          <EchoTailViz timeMs={values.time} feedback={values.feedback} mix={values.mix} />
        </div>
      </article>
    </>
  );
}
