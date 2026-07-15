import type { Role } from "@/lib/pedalboard/model";

/** Human names per role — glyph tooltips and legend text (reference ROLE_NAMES). */
export const ROLE_NAMES: Record<Role, string> = {
  time: "Time / rate",
  shape: "Intensity",
  blend: "Wet / dry blend",
  level: "Level",
};

export interface RoleGlyphProps {
  role: Role;
}

/**
 * Role glyphs (reference GLYPHS) — one shape per job, everywhere:
 * time = clock hand · shape = intensity ramp · blend = half-filled circle ·
 * level = fader bars. Coordinates are SVG user units, deliberately not --u;
 * the rendered size comes from `.pb-glyph`.
 */
export function RoleGlyph({ role }: RoleGlyphProps) {
  switch (role) {
    case "time":
      return (
        <svg className="pb-glyph" viewBox="0 0 10 10" aria-hidden="true">
          <circle cx="5" cy="5" r="4" fill="none" stroke="currentColor" />
          <path
            d="M5 5 V2.4 M5 5 L6.8 6.4"
            stroke="currentColor"
            fill="none"
            strokeLinecap="round"
          />
        </svg>
      );
    case "shape":
      return (
        <svg className="pb-glyph" viewBox="0 0 10 10" aria-hidden="true">
          <path d="M1.5 8.5 L8.5 8.5 L8.5 1.5 Z" fill="currentColor" opacity="0.8" />
        </svg>
      );
    case "blend":
      return (
        <svg className="pb-glyph" viewBox="0 0 10 10" aria-hidden="true">
          <circle cx="5" cy="5" r="4" fill="none" stroke="currentColor" />
          <path d="M5 1 A4 4 0 0 1 5 9 Z" fill="currentColor" />
        </svg>
      );
    case "level":
      return (
        <svg className="pb-glyph" viewBox="0 0 10 10" aria-hidden="true">
          <path
            d="M2 8.5 V4.5 M5 8.5 V1.5 M8 8.5 V6"
            stroke="currentColor"
            fill="none"
            strokeLinecap="round"
            strokeWidth="1.4"
          />
        </svg>
      );
  }
}

/** Gate marker (reference `.gate-dot`): knob at 0 is a true bypass. */
export function GateDot() {
  return <span className="pb-gate-dot" title="Gate — 0 is a true bypass" />;
}
