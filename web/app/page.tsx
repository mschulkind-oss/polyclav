"use client";

import { useEffect, useReducer, useRef, useState } from "react";
import { ConfigCard } from "@/components/ConfigCard";
import { MasteringCard } from "@/components/MasteringCard";
import { MIDIDevicesCard } from "@/components/MIDIDevicesCard";
import { ParamsCard } from "@/components/ParamsCard";
import { PatchGrid } from "@/components/PatchGrid";
import { Section } from "@/components/Section";
import { SynthCard } from "@/components/SynthCard";
import { TransportCard } from "@/components/TransportCard";
import { VelocityCard } from "@/components/VelocityCard";
import { api } from "@/lib/api";
import { applySynthEvent } from "@/lib/synthMerge";
import type {
  Clip,
  DeviceEvent,
  Devices,
  MasteringEvent,
  NoteEvent,
  ParamsEvent,
  Patch,
  PatchEvent,
  PlayerState,
  Status,
  Synth,
  SynthEvent,
  VelocityEvent,
} from "@/lib/types";
import { useSSE } from "@/lib/useSSE";

// ---- dashboard state ----------------------------------------------------
//
// One reducer over the SSE stream: the snapshot event seeds everything,
// deltas patch individual slices. Slider components keep their own
// short-lived local state for the drag guard, so the reducer can apply
// every server echo unconditionally.

interface Dash {
  version: string;
  devices: Devices;
  patches: Patch[];
  current: string;
  volume?: number;
  reverb?: number;
  compressor?: number;
  cutoffPos?: number;
  cutoffHz?: number;
  masteringComp?: number;
  limiterCeilingDb?: number;
  synth: Synth | null;
  velocityLabel: string;
  player: PlayerState | null;
  hasPlayer: boolean;
}

const initialDash: Dash = {
  version: "",
  devices: { launchkey: "unknown", xr18: "unknown" },
  patches: [],
  current: "",
  synth: null,
  velocityLabel: "",
  player: null,
  hasPlayer: false,
};

type Action =
  | { t: "snapshot"; s: Status }
  | { t: "params"; d: ParamsEvent }
  | { t: "patch"; d: PatchEvent }
  | { t: "synth"; d: SynthEvent }
  | { t: "mastering"; d: MasteringEvent }
  | { t: "player"; d: PlayerState }
  | { t: "velocity"; label: string }
  | { t: "device"; d: DeviceEvent };

function reducer(dash: Dash, a: Action): Dash {
  switch (a.t) {
    case "snapshot": {
      const p = a.s.params;
      return {
        version: a.s.version,
        devices: a.s.devices,
        patches: a.s.patches ?? [],
        current: p.patch,
        volume: p.volume,
        reverb: p.reverb,
        compressor: p.compressor,
        cutoffPos: p.cutoff_pos,
        cutoffHz: p.cutoff_hz,
        masteringComp: p.mastering_comp,
        limiterCeilingDb: p.limiter_ceiling_db,
        synth: p.synth ?? null,
        velocityLabel: p.velocity_curve || dash.velocityLabel,
        player: a.s.player,
        hasPlayer: a.s.player !== null && a.s.player !== undefined,
      };
    }
    case "params": {
      if (a.d.field === "cutoff") {
        return {
          ...dash,
          cutoffPos: typeof a.d.pos === "number" ? a.d.pos : dash.cutoffPos,
          cutoffHz: typeof a.d.hz === "number" ? a.d.hz : dash.cutoffHz,
        };
      }
      if (typeof a.d.value !== "number") return dash;
      switch (a.d.field) {
        case "volume":
          return { ...dash, volume: a.d.value };
        case "reverb":
          return { ...dash, reverb: a.d.value };
        case "compressor":
          return { ...dash, compressor: a.d.value };
        default:
          return dash;
      }
    }
    case "patch": {
      const next: Dash = { ...dash, current: a.d.name };
      if (typeof a.d.volume === "number") next.volume = a.d.volume;
      if (typeof a.d.reverb === "number") next.reverb = a.d.reverb;
      if (typeof a.d.compressor === "number") next.compressor = a.d.compressor;
      if (typeof a.d.cutoff_pos === "number") next.cutoffPos = a.d.cutoff_pos;
      if (typeof a.d.cutoff_hz === "number") next.cutoffHz = a.d.cutoff_hz;
      if (a.d.synth) next.synth = a.d.synth;
      return next;
    }
    case "synth":
      return dash.synth ? { ...dash, synth: applySynthEvent(dash.synth, a.d) } : dash;
    case "mastering":
      return {
        ...dash,
        masteringComp: typeof a.d.comp_amount === "number" ? a.d.comp_amount : dash.masteringComp,
        limiterCeilingDb:
          typeof a.d.limiter_ceiling_db === "number"
            ? a.d.limiter_ceiling_db
            : dash.limiterCeilingDb,
      };
    case "player":
      return { ...dash, player: a.d, hasPlayer: true };
    case "velocity":
      return { ...dash, velocityLabel: a.label };
    case "device": {
      if (a.d.device === "launchkey" || a.d.device === "xr18") {
        return {
          ...dash,
          devices: { ...dash.devices, [a.d.device]: a.d.state ?? "unknown" },
        };
      }
      return {
        ...dash,
        devices: {
          launchkey: a.d.launchkey ?? dash.devices.launchkey,
          xr18: a.d.xr18 ?? dash.devices.xr18,
        },
      };
    }
  }
}

// ---- the page --------------------------------------------------------------

export default function Page() {
  const [dash, dispatch] = useReducer(reducer, initialDash);
  const [clips, setClips] = useState<Clip[] | null>(null);
  const clipsRequested = useRef(false);
  // The velocity canvas registers its dot-plotter here; SSE "note"
  // events bypass the reducer (they're a visualization, not state).
  const noteSink = useRef<((n: NoteEvent) => void) | null>(null);

  const connected = useSSE("/api/events", {
    snapshot: (d) => dispatch({ t: "snapshot", s: d as Status }),
    params: (d) => dispatch({ t: "params", d: d as ParamsEvent }),
    patch: (d) => dispatch({ t: "patch", d: d as PatchEvent }),
    synth: (d) => dispatch({ t: "synth", d: d as SynthEvent }),
    mastering: (d) => dispatch({ t: "mastering", d: d as MasteringEvent }),
    player: (d) => dispatch({ t: "player", d: d as PlayerState }),
    velocity: (d) => {
      const c = (d as VelocityEvent).curve;
      if (typeof c === "string") dispatch({ t: "velocity", label: c });
    },
    note: (d) => {
      const n = d as NoteEvent;
      if (typeof n.in === "number" && typeof n.out === "number") noteSink.current?.(n);
    },
    device: (d) => dispatch({ t: "device", d: d as DeviceEvent }),
  });

  // Clip list, fetched once the snapshot says a player is wired. A
  // failed fetch leaves clips null and the transport hidden (the daemon
  // degrades the same way).
  useEffect(() => {
    if (dash.hasPlayer && !clipsRequested.current) {
      clipsRequested.current = true;
      api.clips().then(setClips);
    }
  }, [dash.hasPlayer]);

  const currentPatch = dash.patches.find((p) => p.name === dash.current);
  const isNative = currentPatch?.type === "native";
  const currentDisplay = currentPatch
    ? currentPatch.display || currentPatch.name
    : dash.current || "—";

  return (
    <>
      <header>
        <span className={`dot${connected ? " ok" : ""}`} title="SSE connection" />
        <h1>polyclav</h1>
        <span className="chip">
          launchkey <b>{dash.devices.launchkey}</b>
        </span>
        <span className="chip">
          xr18 <b>{dash.devices.xr18}</b>
        </span>
        <span className="chip">
          patch <b>{currentDisplay}</b>
        </span>
        <span className="spacer" />
        <span className="version">{dash.version}</span>
      </header>
      <main>
        <Section title="Patches" demoClip="arp" player={dash.player}>
          <PatchGrid patches={dash.patches} current={dash.current || null} />
        </Section>
        <Section title="Patch params" demoClip="arp" player={dash.player}>
          <ParamsCard
            volume={dash.volume}
            reverb={dash.reverb}
            compressor={dash.compressor}
            cutoffPos={dash.cutoffPos}
            cutoffHz={dash.cutoffHz}
            isNative={isNative}
          />
        </Section>
        {isNative && dash.synth ? (
          <Section title="Native synth" demoClip="bass-riff" player={dash.player}>
            <SynthCard synth={dash.synth} />
          </Section>
        ) : null}
        <Section title="Mastering" demoClip="sustain-chord" player={dash.player}>
          <MasteringCard compAmount={dash.masteringComp} limiterCeilingDb={dash.limiterCeilingDb} />
        </Section>
        <Section title="Velocity" demoClip="vel-ramp" player={dash.player}>
          <VelocityCard
            active={dash.velocityLabel}
            onActive={(label) => dispatch({ t: "velocity", label })}
            noteSink={noteSink}
          />
        </Section>
        <Section title="MIDI devices">
          <MIDIDevicesCard />
        </Section>
        {dash.player && clips && clips.length > 0 ? (
          <Section title="Audition">
            <TransportCard
              clips={clips}
              player={dash.player}
              onPlayer={(p) => dispatch({ t: "player", d: p })}
            />
          </Section>
        ) : null}
        <Section title="Config" wide>
          <ConfigCard />
        </Section>
      </main>
    </>
  );
}
