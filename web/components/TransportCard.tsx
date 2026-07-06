"use client";

import { useRef, useState } from "react";
import { api } from "@/lib/api";
import { fmtTempo } from "@/lib/format";
import type { Clip, PlayerState } from "@/lib/types";
import { SliderRow } from "./SliderRow";

interface TransportCardProps {
  clips: Clip[];
  player: PlayerState;
  onPlayer: (p: PlayerState) => void;
}

/**
 * Audition transport (docs/AUDITION.md): clip picker + tempo + loop +
 * play/stop. The clip picker and loop checkbox are the user's staging
 * area for the NEXT play, so live player state never overwrites them —
 * only the indicator and tempo track SSE.
 */
export function TransportCard({ clips, player, onPlayer }: TransportCardProps) {
  const [clip, setClip] = useState<string>(clips[0]?.id ?? "");
  const [loop, setLoop] = useState(false);
  // Last tempo the user dragged to; the server echo catches up via SSE,
  // but Play must honor a drag that is still in its debounce window.
  const tempoRef = useRef<number | null>(null);

  const play = async () => {
    const st = await api.playerPlay(clip, loop, tempoRef.current ?? player.tempo);
    if (st) onPlayer(st);
  };
  const stop = async () => {
    const st = await api.playerStop();
    if (st) onPlayer(st);
  };

  return (
    <>
      <div className="row">
        <span className="rowlabel">Clip</span>
        <select
          name="clip"
          aria-label="Clip"
          value={clip}
          onChange={(e) => setClip(e.currentTarget.value)}
        >
          {clips.map((c) => (
            <option key={c.id} value={c.id}>
              {c.name}
              {c.poly_only ? " (poly patches)" : ""}
            </option>
          ))}
        </select>
        <span />
      </div>
      <SliderRow
        label="Tempo"
        min={0.25}
        max={2}
        step={0.05}
        value={player.tempo}
        format={fmtTempo}
        onSend={(t) => {
          tempoRef.current = t;
          api.playerTempo(t);
        }}
      />
      <div className="transport-controls">
        <label className="loop">
          <input
            type="checkbox"
            name="loop"
            checked={loop}
            onChange={(e) => setLoop(e.currentTarget.checked)}
          />{" "}
          loop
        </label>
        <button type="button" onClick={play}>
          Play
        </button>
        <button type="button" onClick={stop}>
          Stop
        </button>
        <span className={`dot${player.playing ? " on" : ""}`} />
        <span className="play-ind-label">
          {player.playing ? `playing ${player.clip}` : "stopped"}
        </span>
      </div>
    </>
  );
}
