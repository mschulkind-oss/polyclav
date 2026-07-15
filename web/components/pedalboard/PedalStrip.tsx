/**
 * The whole pedal card is a click/keyboard target that must CONTAIN the stomp
 * <button> (reference: `<article role="button" data-nav>`), and nested native
 * buttons are invalid HTML — so the card stays an article with an explicit
 * role, keyboard handling and tabindex instead of a <button>.
 */
// biome-ignore-all lint/a11y/useSemanticElements: see header comment
// biome-ignore-all lint/a11y/noNoninteractiveElementToInteractiveRole: see header comment

"use client";

import type { CSSProperties, KeyboardEvent, ReactNode } from "react";
import { Led } from "@/components/pedalboard/Led";
import { MiniKnob } from "@/components/pedalboard/MiniKnob";
import { GateDot, ROLE_NAMES, RoleGlyph } from "@/components/pedalboard/RoleGlyph";
import { Stomp } from "@/components/pedalboard/Stomp";
import { formatValue } from "@/lib/pedalboard/knobMath";
import type { ParamSpec, PedalSpec, Role } from "@/lib/pedalboard/model";

/** The alignment contract's role rows: the same param ROLE on every card. */
const STRIP_ROWS: { role: Role; row: number }[] = [
  { role: "time", row: 2 },
  { role: "shape", row: 3 },
  { role: "blend", row: 4 },
];

export interface PedalStripProps {
  pedal: PedalSpec;
  /** Current value per param id. */
  values: Record<string, number>;
  enabled: boolean;
  onStomp: () => void;
  onOpen: () => void;
  /** Signature module rendered in the pb-viz row. */
  extra?: ReactNode;
  /** Stomp label while bypassed — the delay parks instead ("Parked"). */
  labelOff?: string;
  /** Mini-knob size overrides per param id (the delay's Time mini is 44). */
  miniSizes?: Record<string, number>;
  /**
   * Makes every param mini adjustable in place (full knob canon); omitted,
   * the minis stay display-only readouts. Knob interaction is contained —
   * it never triggers onOpen.
   */
  onParamChange?: (paramId: string, value: number) => void;
}

/**
 * A pedal strip on the rail (reference `.strip`) — THE alignment-contract
 * card. Header, one mini knob per param placed by its ROLE's grid row (time /
 * intensity / blend), a faint dashed ring where a role is deliberately absent,
 * the pedal's signature module in the viz band, and the stomp pinned last.
 * The card itself is a button: click / Enter / Space opens the editor; the
 * stomp toggles bypass without opening (its click never bubbles up).
 */
export function PedalStrip({
  pedal,
  values,
  enabled,
  onStomp,
  onOpen,
  extra,
  labelOff,
  miniSizes,
  onParamChange,
}: PedalStripProps) {
  const handleKeyDown = (e: KeyboardEvent<HTMLElement>) => {
    if (e.target !== e.currentTarget) return; // keys inside (e.g. the stomp) keep their meaning
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      onOpen();
    }
  };
  return (
    <article
      className={enabled ? "pb-strip" : "pb-strip pb-bypassed"}
      role="button"
      tabIndex={0}
      aria-label={`Open ${pedal.label} in editor`}
      style={{ "--pb-accent": `var(${pedal.accentVar})` } as CSSProperties}
      onClick={onOpen}
      onKeyDown={handleKeyDown}
    >
      <div className="pb-strip-top">
        <Led on={enabled} />
        <h3>{pedal.label}</h3>
        <span className="pb-slot-ix pb-num">{pedal.slot}</span>
      </div>
      {STRIP_ROWS.map(({ role, row }) => {
        const param = pedal.params.find((p) => p.role === role);
        if (!param) {
          return (
            <div
              key={role}
              className="pb-slot-empty"
              style={{ "--pb-row": String(row) } as CSSProperties}
              aria-hidden="true"
            />
          );
        }
        return (
          <StripParam
            key={role}
            param={param}
            value={values[param.id] ?? param.defaultValue}
            size={miniSizes?.[param.id]}
            onChange={onParamChange && ((v: number) => onParamChange(param.id, v))}
          />
        );
      })}
      <div className="pb-viz">{extra}</div>
      <Stomp on={enabled} onToggle={onStomp} labelOff={labelOff} />
    </article>
  );
}

function StripParam({
  param,
  value,
  size,
  onChange,
}: {
  param: ParamSpec;
  value: number;
  size?: number;
  onChange?: (v: number) => void;
}) {
  return (
    <div className={`pb-param pb-r-${param.role}`} data-role={param.role}>
      <MiniKnob spec={param} value={value} size={size} onChange={onChange} />
      <div className="pb-p-name" title={ROLE_NAMES[param.role]}>
        <RoleGlyph role={param.role} />
        {param.label}
        {param.gate ? <GateDot /> : null}
      </div>
      <div className="pb-p-val pb-num">{formatValue(value, param)}</div>
    </div>
  );
}
