"use client";

import type { RefObject } from "react";
import { ConfigCard } from "@/components/ConfigCard";
import { MIDIDevicesCard } from "@/components/MIDIDevicesCard";
import { TransportCard } from "@/components/TransportCard";
import { VelocityCard } from "@/components/VelocityCard";
import type { Clip, NoteEvent, PlayerState } from "@/lib/types";

export interface SystemScreenProps {
  velocityLabel: string;
  onVelocity: (label: string) => void;
  noteSink: RefObject<((n: NoteEvent) => void) | null>;
  clips: Clip[] | null;
  player: PlayerState | null;
  onPlayer: (p: PlayerState) => void;
}

/**
 * The System screen: the shipped utility cards — velocity curve + live note
 * monitor, MIDI devices, audition transport, and the config editor — dropped
 * into the pedalboard chrome via .pb-panel wrappers (themed by the legacy-card
 * bridge in composer.extra.css). Audition only mounts when a player + clips
 * exist, exactly like the old dashboard.
 */
export function SystemScreen({
  velocityLabel,
  onVelocity,
  noteSink,
  clips,
  player,
  onPlayer,
}: SystemScreenProps) {
  return (
    <>
      <div className="pb-screen-head">
        <div>
          <div className="pb-kicker">System</div>
          <div className="pb-sub">Velocity · MIDI devices · audition · config</div>
        </div>
      </div>
      <div className="pb-syswrap">
        <div className="pb-panel">
          <div className="pb-panel-head">
            <h3>Velocity</h3>
            <span className="pb-panel-sub">curve + live monitor</span>
          </div>
          <VelocityCard active={velocityLabel} onActive={onVelocity} noteSink={noteSink} />
        </div>
        <div className="pb-panel">
          <div className="pb-panel-head">
            <h3>MIDI devices</h3>
          </div>
          <MIDIDevicesCard />
        </div>
        {player && clips && clips.length > 0 ? (
          <div className="pb-panel">
            <div className="pb-panel-head">
              <h3>Audition</h3>
            </div>
            <TransportCard clips={clips} player={player} onPlayer={onPlayer} />
          </div>
        ) : null}
        <div className="pb-panel pb-panel-wide">
          <div className="pb-panel-head">
            <h3>Config</h3>
            <span className="pb-panel-sub">polyclav.toml</span>
          </div>
          <ConfigCard />
        </div>
      </div>
    </>
  );
}
