"use client";

import type { MouseEvent } from "react";

export interface StompProps {
  on: boolean;
  onToggle: () => void;
  /** Label while engaged. Default "On". */
  labelOn?: string;
  /** Label while bypassed. Default "Bypassed" (the delay uses "Parked"). */
  labelOff?: string;
  /** Editor-hero size (`.pb-big`). */
  big?: boolean;
}

/**
 * Stomp switch (reference `.stomp`): the square dot fills with the card's
 * accent when engaged. It lives inside clickable pedal strips, so clicks
 * never bubble up to the strip's open-editor handler.
 */
export function Stomp({ on, onToggle, labelOn = "On", labelOff = "Bypassed", big }: StompProps) {
  const cls = ["pb-stomp", on && "pb-on", big && "pb-big"].filter(Boolean).join(" ");
  const handleClick = (e: MouseEvent<HTMLButtonElement>) => {
    e.stopPropagation();
    onToggle();
  };
  return (
    <button type="button" className={cls} aria-pressed={on} onClick={handleClick}>
      <span className="pb-stomp-dot" />
      <span>{on ? labelOn : labelOff}</span>
    </button>
  );
}
