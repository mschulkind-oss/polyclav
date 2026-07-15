import { Led } from "@/components/pedalboard/Led";
import { MiniKnob } from "@/components/pedalboard/MiniKnob";
import { ROLE_NAMES, RoleGlyph } from "@/components/pedalboard/RoleGlyph";
import { formatValue } from "@/lib/pedalboard/knobMath";
import type { ParamSpec } from "@/lib/pedalboard/model";
import { BUS_PARAMS } from "@/lib/pedalboard/model";

export interface BusCardProps {
  /** Value overrides per param id; defaults to each spec's defaultValue. */
  values?: Record<string, number>;
}

/**
 * The stereo bus card at the end of the rail (reference `.bus`). Shares the
 * strips' grid template — its two param pairs pack into the time/intensity
 * rows (2–3), level meters breathe in the header, and the "Post · stereo out"
 * footer lands on the stomp row so everything stays aligned.
 */
export function BusCard({ values }: BusCardProps) {
  const pairs: ParamSpec[][] = [BUS_PARAMS.slice(0, 2), BUS_PARAMS.slice(2, 4)];
  return (
    <article className="pb-bus">
      <div className="pb-bus-top">
        <Led on />
        <h3>Bus</h3>
        <svg className="pb-meters" viewBox="0 0 14 26" aria-hidden="true">
          <rect className="pb-mL" x="2" y="2" width="3.5" height="22" rx="1" />
          <rect className="pb-mR" x="8.5" y="2" width="3.5" height="22" rx="1" />
        </svg>
      </div>
      {pairs.map((pair, i) => (
        <div key={pair[0]?.id} className={`pb-bus-pair pb-r${i + 1}`}>
          {pair.map((param) => {
            const value = values?.[param.id] ?? param.defaultValue;
            return (
              <div key={param.id} className="pb-param" data-role={param.role}>
                <MiniKnob spec={param} value={value} />
                <div className="pb-p-name" title={ROLE_NAMES[param.role]}>
                  <RoleGlyph role={param.role} />
                  {param.label}
                </div>
                <div className="pb-p-val pb-num">{formatValue(value, param)}</div>
              </div>
            );
          })}
        </div>
      ))}
      <div className="pb-bus-sub">Post · stereo out</div>
    </article>
  );
}
