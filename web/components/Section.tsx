"use client";

import type { ReactNode } from "react";
import { api } from "@/lib/api";
import type { PlayerState } from "@/lib/types";

interface DemoButtonProps {
  clip: string;
  player: PlayerState | null;
}

/**
 * Per-section ▶ demo button (docs/AUDITION.md): toggles the mapped clip
 * looping at tempo 1. Highlight state reflects the live player via SSE,
 * so a demo started from any surface lights up the matching section.
 * Rendered only when the daemon has a player wired.
 */
export function DemoButton({ clip, player }: DemoButtonProps) {
  if (!player) return null;
  const playing = player.playing && player.clip === clip;
  return (
    <button
      type="button"
      className={`demo${playing ? " playing" : ""}`}
      title={`demo: ${clip} (loop)`}
      onClick={(e) => {
        e.stopPropagation();
        if (playing) api.playerStop();
        else api.playerPlay(clip, true, 1);
      }}
    >
      ▶
    </button>
  );
}

interface SectionProps {
  title: string;
  /** Clip id for the header demo button; omit for sections without one. */
  demoClip?: string;
  player?: PlayerState | null;
  wide?: boolean;
  children: ReactNode;
}

/** One dashboard card: uppercase header (with optional demo ▶) + body. */
export function Section({ title, demoClip, player, wide, children }: SectionProps) {
  return (
    <section className={wide ? "wide" : undefined}>
      <h2>
        {title}
        {demoClip ? <DemoButton clip={demoClip} player={player ?? null} /> : null}
      </h2>
      {children}
    </section>
  );
}
