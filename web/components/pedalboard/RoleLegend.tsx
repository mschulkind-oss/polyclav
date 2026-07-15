import { GateDot, ROLE_NAMES, RoleGlyph } from "@/components/pedalboard/RoleGlyph";
import type { Role } from "@/lib/pedalboard/model";

const LEGEND_ROLES: Role[] = ["time", "shape", "blend", "level"];

/**
 * The control legend line (reference `#legend`): every role glyph with its
 * name, plus the gate marker. Sits under the rail on the board screen.
 */
export function RoleLegend() {
  return (
    // biome-ignore lint/a11y/useSemanticElements: a passive glyph legend, not a form-control group — fieldset semantics/styling don't apply
    <div className="pb-legend" role="group" aria-label="Control legend">
      {LEGEND_ROLES.map((role) => (
        <span className="pb-lg-item" key={role}>
          <RoleGlyph role={role} />
          {ROLE_NAMES[role]}
        </span>
      ))}
      <span className="pb-lg-item">
        <GateDot />
        gate — knob at 0 is a true bypass
      </span>
    </div>
  );
}
