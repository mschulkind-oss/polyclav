import { Led } from "@/components/pedalboard/Led";

/**
 * The SYNTH source card at the head of the rail (reference `.srcnode`).
 * Shares the strip grid template, so its header, waveform glyph (row 3) and
 * "8 voices · poly" footer (row 6) align with every pedal's rows.
 */
export function SrcNode() {
  return (
    <article className="pb-srcnode">
      <div className="pb-src-top">
        <Led on />
        <h3>Synth</h3>
      </div>
      <svg className="pb-src-glyph" viewBox="0 0 56 22" aria-hidden="true">
        <path
          d="M2 17 L11 5 V17 L20 5 V17 L29 5 V17 L38 5 V17 L47 5 V17 L54 8"
          fill="none"
          stroke="currentColor"
          strokeWidth="1.3"
          strokeLinejoin="round"
        />
      </svg>
      <div className="pb-src-sub">8 voices · poly</div>
    </article>
  );
}
