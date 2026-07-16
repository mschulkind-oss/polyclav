/**
 * The whole pedal card is a click/keyboard target that must CONTAIN the stomp
 * <button> (reference: `<article role="button" data-nav>`), and nested native
 * buttons are invalid HTML — so the card stays an article with an explicit
 * role, keyboard handling and tabindex instead of a <button>.
 */
// biome-ignore-all lint/a11y/useSemanticElements: see header comment
// biome-ignore-all lint/a11y/noNoninteractiveElementToInteractiveRole: see header comment

"use client";

import type { CSSProperties, DragEventHandler, KeyboardEvent, ReactNode } from "react";
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

/** Reorder wiring for the strip's top bar (the drag handle). Built by Pedalboard. */
export interface StripReorder {
  dragging: boolean;
  dropTarget: boolean;
  /** "3 of 6", for the handle's aria-label. */
  position: string;
  handleProps: {
    draggable: true;
    onDragStart: DragEventHandler<HTMLDivElement>;
    onDragEnd: () => void;
    onDragOver: DragEventHandler<HTMLDivElement>;
    onDragLeave: () => void;
    onDrop: DragEventHandler<HTMLDivElement>;
  };
  /** Keyboard nudge left/right one slot. */
  onKey: (dir: -1 | 1) => void;
}

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
  /** Shrunk to a vertical strip (view-only convenience for horizontal scroll). */
  collapsed?: boolean;
  onToggleCollapse?: () => void;
  /** Drag/keyboard reorder wired onto the top bar; omit to disable reordering. */
  reorder?: StripReorder;
}

/**
 * A pedal strip on the rail (reference `.strip`) — THE alignment-contract
 * card. Its TOP BAR is the drag handle (reorder the FX chain) and carries the
 * collapse toggle; below it, one mini knob per param placed by its ROLE's grid
 * row, the pedal's signature module, and the stomp. Clicking the body opens
 * the editor (or, when collapsed, expands the strip); the stomp and header
 * controls never bubble up to that. Collapsed, it becomes a narrow vertical
 * strip so pedals you're not poking at get out of the horizontal scroll's way.
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
  collapsed = false,
  onToggleCollapse,
  reorder,
}: PedalStripProps) {
  const activate = () => (collapsed ? onToggleCollapse?.() : onOpen());
  const handleKeyDown = (e: KeyboardEvent<HTMLElement>) => {
    if (e.target !== e.currentTarget) return; // keys inside (e.g. the stomp) keep their meaning
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      activate();
    } else if (reorder && e.key === "ArrowLeft") {
      e.preventDefault();
      reorder.onKey(-1);
    } else if (reorder && e.key === "ArrowRight") {
      e.preventDefault();
      reorder.onKey(1);
    }
  };

  const cls = ["pb-strip"];
  if (!enabled) cls.push("pb-bypassed");
  if (collapsed) cls.push("pb-collapsed");
  if (reorder?.dragging) cls.push("pb-dragging");
  else if (reorder?.dropTarget) cls.push("pb-drop");

  const collapseBtn = onToggleCollapse ? (
    <button
      type="button"
      className="pb-strip-collapse"
      draggable={false}
      aria-label={`${collapsed ? "Expand" : "Collapse"} ${pedal.label}`}
      title={collapsed ? "Expand" : "Collapse to a strip"}
      onClick={(e) => {
        e.stopPropagation();
        onToggleCollapse();
      }}
      onDragStart={(e) => e.preventDefault()}
    >
      {collapsed ? "»" : "«"}
    </button>
  ) : null;

  return (
    <article
      className={cls.join(" ")}
      role="button"
      tabIndex={0}
      aria-label={collapsed ? `${pedal.label} — collapsed` : `Open ${pedal.label} in editor`}
      style={{ "--pb-accent": `var(${pedal.accentVar})` } as CSSProperties}
      onClick={activate}
      onKeyDown={handleKeyDown}
    >
      <div
        className="pb-strip-top"
        title={reorder ? `Drag to reorder · ${pedal.label} ${reorder.position}` : undefined}
        {...reorder?.handleProps}
      >
        <Led on={enabled} />
        {!collapsed && <h3>{pedal.label}</h3>}
        {collapseBtn}
        {!collapsed && <span className="pb-slot-ix pb-num">{pedal.slot}</span>}
      </div>

      {collapsed ? (
        <>
          <div className="pb-strip-vname" aria-hidden="true">
            {pedal.label}
          </div>
          <Stomp on={enabled} onToggle={onStomp} labelOff={labelOff} />
        </>
      ) : (
        <>
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
        </>
      )}
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
