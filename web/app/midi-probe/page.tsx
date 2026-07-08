"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { ProbeEventLog } from "@/components/ProbeEventLog";
import { ProbeIdentityCard } from "@/components/ProbeIdentityCard";
import { ProbeLabelCapture } from "@/components/ProbeLabelCapture";
import { ProbePortPicker } from "@/components/ProbePortPicker";
import { ProbeRawSend } from "@/components/ProbeRawSend";
import { api } from "@/lib/api";
import type { ProbeEvent, ProbeStatus } from "@/lib/types";
import { useSSE } from "@/lib/useSSE";

const initialStatus: ProbeStatus = {
  active: false,
  eventCount: 0,
  bufferCap: 0,
  labeling: false,
};

/**
 * Generic MIDI device reverse-engineering tool (docs/MIDI_PROBE.md): connect
 * to any MIDI port pair, watch every raw message live, tag controls with a
 * label one at a time, probe with a MIDI Identity Request, and export
 * everything as a JSON device profile to hand off to whoever builds the
 * real driver support.
 */
export default function MidiProbePage() {
  const [status, setStatus] = useState<ProbeStatus>(initialStatus);
  const eventSinkRef = useRef<((e: ProbeEvent) => void) | null>(null);
  const registerSink = useCallback((sink: (e: ProbeEvent) => void) => {
    eventSinkRef.current = sink;
  }, []);

  const connected = useSSE("/api/events", {
    "probe-status": (d) => setStatus(d as ProbeStatus),
    "probe-event": (d) => eventSinkRef.current?.(d as ProbeEvent),
  });

  useEffect(() => {
    api.probeStatus().then((s) => s && setStatus(s));
  }, []);

  return (
    <>
      <header>
        <h1>polyclav — MIDI Probe</h1>
        <span className="spacer" />
        <span className="chip">
          <span className={`dot${connected ? " ok" : ""}`} />
          {connected ? "live" : "reconnecting…"}
        </span>
        <a className="version" href="/app/">
          ← Dashboard
        </a>
      </header>
      <main>
        <ProbePortPicker status={status} onStatus={setStatus} />
        <ProbeLabelCapture status={status} />
        <ProbeIdentityCard active={status.active} />
        <ProbeRawSend active={status.active} />
        <section className="wide">
          <h2>
            Event Log
            {status.eventCount > 0 ? (
              <a className="version" href="/api/probe/export">
                {" "}
                ⬇ Export device profile (JSON)
              </a>
            ) : null}
          </h2>
          <ProbeEventLog registerSink={registerSink} />
        </section>
      </main>
    </>
  );
}
