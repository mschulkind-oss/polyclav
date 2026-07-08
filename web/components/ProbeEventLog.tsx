"use client";

import { useEffect, useState } from "react";
import type { ProbeEvent } from "@/lib/types";

const MAX_ROWS = 500;

interface ProbeEventLogProps {
  /** Called once on mount with a stable sink the parent's SSE handler
   * invokes directly for every "probe-event" — avoids routing a
   * potentially rapid stream of MIDI events (a knob sweep) through a
   * whole-page reducer/re-render, mirroring the existing dashboard's
   * note-event monitor pattern. */
  registerSink: (sink: (e: ProbeEvent) => void) => void;
}

function kindClass(kind: string): string {
  switch (kind) {
    case "note-on":
    case "note-off":
      return "ev-note";
    case "cc":
      return "ev-cc";
    case "sysex":
      return "ev-sysex";
    case "pitch-bend":
      return "ev-pitchbend";
    default:
      return "ev-other";
  }
}

function decoded(e: ProbeEvent): string {
  const parts: string[] = [];
  if (e.channel !== undefined) parts.push(`ch${e.channel + 1}`);
  if (e.data1 !== undefined) parts.push(`d1=${e.data1}`);
  if (e.data2 !== undefined) parts.push(`d2=${e.data2}`);
  if (e.bend !== undefined) parts.push(`bend=${e.bend}`);
  return parts.join(" ");
}

/** Live, newest-first log of every raw MIDI message on the connected
 * port, fed by SSE via registerSink (see ProbeEventLogProps). */
export function ProbeEventLog({ registerSink }: ProbeEventLogProps) {
  const [events, setEvents] = useState<ProbeEvent[]>([]);

  useEffect(() => {
    registerSink((e) => {
      setEvents((prev) => {
        const next = [e, ...prev];
        return next.length > MAX_ROWS ? next.slice(0, MAX_ROWS) : next;
      });
    });
    // registerSink is a stable useCallback from the parent (page.tsx), so
    // this only re-registers if the parent identity actually changes.
  }, [registerSink]);

  if (events.length === 0) {
    return <p className="hint">No messages yet — connect a device and twiddle a control.</p>;
  }

  return (
    <div className="probe-log">
      <table>
        <thead>
          <tr>
            <th>Time</th>
            <th>Kind</th>
            <th>Decoded</th>
            <th>Raw</th>
            <th>Label</th>
          </tr>
        </thead>
        <tbody>
          {events.map((e) => (
            <tr key={e.seq}>
              <td className="probe-time">{new Date(e.time).toLocaleTimeString()}</td>
              <td>
                <span className={`ev-kind ${kindClass(e.kind)}`}>{e.kind}</span>
              </td>
              <td className="probe-decoded">{decoded(e)}</td>
              <td>
                <code>{e.raw}</code>
              </td>
              <td>{e.label ? <span className="chip">{e.label}</span> : null}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
