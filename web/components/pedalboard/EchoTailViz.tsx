import type { ReactElement } from "react";

/** Ruler scale: SVG user units per millisecond (reference `renderTail` PX). */
const PX_PER_MS = 0.26;
/** X of the dry ping / time zero. */
const X0 = 18;
/** Y of the ruler baseline. */
const MID = 26;
/** Number of echo repeats drawn. */
const ECHOES = 4;

export interface EchoTailVizProps {
  /** Delay time in ms (model units, 1–1000). */
  timeMs: number;
  /** Feedback percent (model units, 0–90). */
  feedback: number;
  /** Mix percent (model units, 0–100). */
  mix: number;
}

interface EchoDot {
  n: number;
  cx: string;
  r: string;
  opacity: string;
}

/**
 * The pedal editor's big echo-tail visual: a 0.26 px/ms time ruler with a
 * dry-ping line at t0 and echo dots spaced by the true delay time, faded by
 * feedback x mix. Motion is parameter-truthful: every animation cycle is
 * max(time * 5, 220) ms and dot n pings n * time ms after the dry hit.
 * Live (mint) vs bypassed (grey) comes from the parent card's class —
 * `.pb-hero:not(.pb-bypassed) .pb-tail-wrap` colors via currentColor.
 */
export function EchoTailViz({ timeMs, feedback, mix }: EchoTailVizProps) {
  const fb = feedback / 100;
  const mx = mix / 100;
  const cycleMs = Math.max(timeMs * 5, 220);

  const ticks: ReactElement[] = [];
  const tickLabels: ReactElement[] = [];
  for (let ms = 250; ms <= 4000; ms += 250) {
    const x = (X0 + ms * PX_PER_MS).toFixed(1);
    const major = ms % 1000 === 0;
    ticks.push(
      <line key={ms} className="pb-t-tick" x1={x} y1={MID} x2={x} y2={MID + (major ? 5 : 3)} />,
    );
    if (major) {
      tickLabels.push(
        <text key={ms} className="pb-t-lab pb-num" x={x} y={MID + 17} textAnchor="middle">
          {`${ms / 1000} s`}
        </text>,
      );
    }
  }

  const dots: EchoDot[] = [];
  for (let n = 1; n <= ECHOES; n++) {
    const opacity = Math.min(1, fb ** (n * 0.7) * (0.55 + mx * 0.8));
    if (opacity < 0.015) continue;
    dots.push({
      n,
      cx: (X0 + n * timeMs * PX_PER_MS).toFixed(1),
      r: (7 * Math.max(fb, 0.2) ** (n * 0.28)).toFixed(2),
      opacity: opacity.toFixed(3),
    });
  }

  return (
    <div className="pb-tail-wrap">
      <svg
        viewBox="0 0 1100 52"
        width="100%"
        height={52}
        preserveAspectRatio="xMinYMid meet"
        aria-hidden="true"
      >
        <line className="pb-t-base" x1={6} y1={MID} x2={1094} y2={MID} />
        {ticks}
        {tickLabels}
        <line
          className="pb-t-dry"
          x1={X0}
          y1={9}
          x2={X0}
          y2={43}
          style={{ animationDuration: `${cycleMs}ms` }}
        />
        {dots.map((d) => (
          <circle
            key={d.n}
            className="pb-e-dot"
            cx={d.cx}
            cy={MID}
            r={d.r}
            opacity={d.opacity}
            style={{ animationDuration: `${cycleMs}ms`, animationDelay: `${d.n * timeMs}ms` }}
          />
        ))}
        <text
          className="pb-t-ms pb-num"
          x={(X0 + timeMs * PX_PER_MS).toFixed(1)}
          y={12}
          textAnchor="middle"
        >
          {`+${Math.round(timeMs)} ms`}
        </text>
      </svg>
    </div>
  );
}
