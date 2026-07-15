"use client";

import { useEffect, useRef, useState } from "react";

export interface HwMapping {
  /** Hardware knob chip, e.g. "K1". */
  k: string;
  /** Mapped parameter label, e.g. "Time". */
  label: string;
}

export interface HwHintBarProps {
  /** Hardware page path, e.g. "FX > Delay". */
  path: string;
  mappings: HwMapping[];
  onEditOnHardware?: () => void;
}

const SENT_MS = 1400;

/**
 * Launchkey hint bar (reference `.hwbar`): where the current pedal lives on
 * the hardware and which knobs map to which params. The button flips to
 * "Sent to Launchkey" for 1.4 s (reference edit-hw handler); the timer is
 * cleaned up on unmount.
 */
export function HwHintBar({ path, mappings, onEditOnHardware }: HwHintBarProps) {
  const [sent, setSent] = useState(false);
  const timer = useRef<number | undefined>(undefined);

  useEffect(() => () => window.clearTimeout(timer.current), []);

  const handleClick = () => {
    onEditOnHardware?.();
    setSent(true);
    window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => setSent(false), SENT_MS);
  };

  return (
    <article className="pb-hwbar">
      <div className="pb-hw-left">
        <span className="pb-kicker">Launchkey</span>
        <span className="pb-hw-path">{path}</span>
        {mappings.map((m) => (
          <span className="pb-hw-map" key={m.k}>
            <span className="pb-kchip">{m.k}</span> {m.label}
          </span>
        ))}
      </div>
      <button
        type="button"
        className={sent ? "pb-hw-btn pb-sent" : "pb-hw-btn"}
        onClick={handleClick}
      >
        {sent ? "Sent to Launchkey" : "Edit on hardware"}
      </button>
    </article>
  );
}
