import { Led } from "@/components/pedalboard/Led";
import { RoleGlyph } from "@/components/pedalboard/RoleGlyph";
import { MiniKnob } from "@/components/pedalboard/synthExternals";
import { formatValue } from "@/lib/pedalboard/knobMath";
import { type EnvSpec, type Role, SYNTH } from "@/lib/pedalboard/model";

/** A/D/R are times (clock glyph); S is a level (fader-bars glyph). */
const STAGES: ReadonlyArray<{ key: keyof EnvSpec; letter: string; role: Role }> = [
  { key: "a", letter: "A", role: "time" },
  { key: "d", letter: "D", role: "time" },
  { key: "s", letter: "S", role: "level" },
  { key: "r", letter: "R", role: "time" },
];

function EnvRow({ tag, env }: { tag: string; env: EnvSpec }) {
  return (
    <div className="pb-env-row">
      <span className="pb-env-tag">{tag}</span>
      {STAGES.map(({ key, letter, role }) => {
        const spec = env[key];
        return (
          <div
            className="pb-env-cell"
            key={spec.id}
            title={`${spec.label} — ${formatValue(spec.defaultValue, spec)}`}
          >
            <MiniKnob spec={spec} value={spec.defaultValue} />
            <div className="pb-p-name">
              <RoleGlyph role={role} />
              {letter}
            </div>
          </div>
        );
      })}
    </div>
  );
}

/**
 * "Envelopes" card: FILTER and AMP rows of A/D/S/R display minis, values
 * straight from the SYNTH model defaults (static mock data).
 */
export function EnvSection() {
  return (
    <article className="pb-scard">
      <div className="pb-scard-top">
        <Led on />
        <h3>Envelopes</h3>
        <span className="pb-slot-ix">ADSR ×2</span>
      </div>
      <EnvRow tag="Filter" env={SYNTH.filterEnv} />
      <EnvRow tag="Amp" env={SYNTH.ampEnv} />
    </article>
  );
}
