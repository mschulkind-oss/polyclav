import type { ReactNode } from "react";

/**
 * Delay's signature module: echo dots on a miniature time ruler — spaced by
 * the true delay time, faded and shrunk by feedback, and (when live) pinging
 * in rhythm: the `pb-ping` cycle is max(time × 5, 220) ms and echo n fires
 * n × time ms late (reference `renderStripTail`).
 */
export interface DelayTailVizProps {
  /** Delay time in ms. */
  timeMs: number;
  /** Feedback as a 0..1 fraction. */
  feedback: number;
  /** True when the pedal is engaged — dots carry the ping animation timing. */
  live: boolean;
}

export function DelayTailViz({ timeMs, feedback, live }: DelayTailVizProps) {
  const x0 = 8;
  const spacing = 8 + timeMs * 0.048;
  const cycleMs = Math.max(timeMs * 5, 220);
  const dots: ReactNode[] = [];
  for (let n = 1; n <= 4; n++) {
    const opacity = feedback ** n * 0.9;
    if (opacity < 0.03) continue;
    const r = 4.2 * Math.max(feedback, 0.15) ** (n * 0.28);
    dots.push(
      <circle
        key={n}
        cx={(x0 + spacing * n).toFixed(1)}
        cy="10"
        r={r.toFixed(2)}
        opacity={opacity.toFixed(3)}
        style={
          live
            ? { animationDuration: `${cycleMs}ms`, animationDelay: `${n * timeMs}ms` }
            : undefined
        }
      />,
    );
  }
  return (
    <svg className="pb-strip-tail" viewBox="0 0 118 20" aria-hidden="true">
      <line x1={x0} y1="4" x2={x0} y2="16" strokeWidth="2" strokeLinecap="round" opacity="0.85" />
      {dots}
    </svg>
  );
}
