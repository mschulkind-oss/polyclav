"use client";

import { Led } from "@/components/pedalboard/Led";
import { MiniKnob } from "@/components/pedalboard/MiniKnob";
import { ROLE_NAMES, RoleGlyph } from "@/components/pedalboard/RoleGlyph";
import { formatValue } from "@/lib/pedalboard/knobMath";
import { MASTER_PARAMS } from "@/lib/pedalboard/model";

export interface MasterCardProps {
  /** Value overrides per param id; defaults to each spec's defaultValue. */
  values?: Record<string, number>;
  /** Makes the master minis adjustable in place; omitted, they stay readouts. */
  onParamChange?: (paramId: string, value: number) => void;
}

/**
 * The master-out card at the end of the rail (reference `.bus`, now the true
 * stereo terminus). The old bus's Comp and Reverb graduated into their own
 * pedals; what remains is the master fader and the brick-wall limiter ceiling,
 * both live (see lib/pedalboard/wiring.ts). Shares the strip grid template so
 * its header meters, knob pair and "Post · stereo out" footer stay aligned
 * with every pedal to its left.
 */
export function MasterCard({ values, onParamChange }: MasterCardProps) {
  return (
    <article className="pb-bus">
      <div className="pb-bus-top">
        <Led on />
        <h3>Master</h3>
        <svg className="pb-meters" viewBox="0 0 14 26" aria-hidden="true">
          <rect className="pb-mL" x="2" y="2" width="3.5" height="22" rx="1" />
          <rect className="pb-mR" x="8.5" y="2" width="3.5" height="22" rx="1" />
        </svg>
      </div>
      <div className="pb-bus-pair pb-r1">
        {MASTER_PARAMS.map((param) => {
          const value = values?.[param.id] ?? param.defaultValue;
          return (
            <div key={param.id} className="pb-param" data-role={param.role}>
              <MiniKnob
                spec={param}
                value={value}
                onChange={onParamChange && ((v: number) => onParamChange(param.id, v))}
              />
              <div className="pb-p-name" title={ROLE_NAMES[param.role]}>
                <RoleGlyph role={param.role} />
                {param.label}
              </div>
              <div className="pb-p-val pb-num">{formatValue(value, param)}</div>
            </div>
          );
        })}
      </div>
      <div className="pb-bus-sub">Post · stereo out</div>
    </article>
  );
}
