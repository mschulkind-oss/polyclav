"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import {
  controlForMessage,
  type DebugEvent,
  decodeMessage,
  flattenInventory,
} from "@/lib/launchkeyDebug";
import "./style.css";

const groups: [string, string[]][] = [
  ["Encoders", Array.from({ length: 8 }, (_, i) => `encoder-${i + 1}`)],
  ["Faders", Array.from({ length: 9 }, (_, i) => `fader-${i + 1}`)],
  ["Fader buttons", Array.from({ length: 9 }, (_, i) => `fader-button-${i + 1}`)],
  [
    "Display / navigation",
    [
      "track-left",
      "track-right",
      "display-up",
      "display-down",
      "shift",
      "pad-up",
      "pad-down",
      "pad-right",
      "function",
      "settings",
      "octave-minus",
      "octave-plus",
    ],
  ],
  [
    "Pad layouts / features",
    [
      "pad-layout",
      "encoder-layout",
      "fader-layout",
      "scale",
      "arp",
      "latch",
      "chord-map",
      "fixed-chord",
    ],
  ],
  [
    "DAW / transport",
    [
      "capture",
      "undo",
      "quantise",
      "metronome",
      "stop",
      "loop",
      "play",
      "record",
      "rewind",
      "fast-forward",
    ],
  ],
  ["Performance", ["pitch-wheel", "mod-wheel", "sustain"]],
];
const blackNotes = new Set([1, 3, 6, 8, 10]);
const padRows = ["top", "bottom"] as const;
const label = (name: string) => name.replaceAll("-", " ");
const padModes: Record<number, string> = {
  1: "Drum",
  2: "DAW",
  4: "User Chord",
  5: "Custom 1",
  6: "Custom 2",
  7: "Custom 3",
  8: "Custom 4",
  13: "Arp Pattern",
  14: "Chord Map",
  15: "DAW Drum",
};

export default function LaunchkeyDebugPage() {
  const [live, setLive] = useState(false);
  const [error, setError] = useState("");
  const [inputs, setInputs] = useState<string[]>([]);
  const [history, setHistory] = useState<(DebugEvent & { seq: number })[]>([]);
  const sequence = useRef(0);
  const [last, setLast] = useState<Record<string, DebugEvent>>({});
  const [held, setHeld] = useState<Record<string, boolean>>({});
  const [report, setReport] = useState<DebugEvent[]>([]);
  const [cursor, setCursor] = useState(0);
  const [playing, setPlaying] = useState(false);
  const accessRef = useRef<MIDIAccess | null>(null);
  const handlersRef = useRef<Map<MIDIInput, (event: MIDIMessageEvent) => void>>(new Map());

  const ingest = useCallback((e: DebugEvent) => {
    const control = controlForMessage(e);
    if (control) {
      setLast((prev) => ({ ...prev, [control]: e }));
      if (
        e.kind === "note-on" ||
        e.kind === "note-off" ||
        (e.kind === "cc" && e.channel === 1 && e.port === "DAW")
      ) {
        setHeld((prev) => ({
          ...prev,
          [control]: e.kind === "note-on" || (e.kind === "cc" && e.value >= 64),
        }));
      }
    }
    setHistory((prev) => [{ ...e, seq: ++sequence.current }, ...prev].slice(0, 32));
  }, []);

  const disconnect = useCallback(() => {
    for (const [input, handler] of handlersRef.current)
      input.removeEventListener("midimessage", handler);
    handlersRef.current.clear();
    if (accessRef.current)
      accessRef.current.removeEventListener("statechange", onStateChange.current);
    accessRef.current = null;
    setLive(false);
    setInputs([]);
    setHeld({});
  }, []);

  const refreshRef = useRef<() => void>(() => {});
  const onStateChange = useRef(() => refreshRef.current());
  refreshRef.current = () => {
    const access = accessRef.current;
    if (!access) return;
    const current = Array.from(access.inputs.values()).filter((input) =>
      /launchkey.*mk4/i.test(input.name ?? ""),
    );
    const names: string[] = [];
    for (const input of current) {
      const role = /daw|midi 2/i.test(input.name ?? "") ? "DAW" : "MIDI";
      names.push(`${input.name ?? "Unknown"} (${role})`);
      if (handlersRef.current.has(input)) continue;
      const handler = (event: MIDIMessageEvent) => {
        if (event.data)
          ingest(
            decodeMessage(Array.from(event.data), role, event.timeStamp + performance.timeOrigin),
          );
      };
      input.addEventListener("midimessage", handler);
      handlersRef.current.set(input, handler);
    }
    for (const [input, handler] of handlersRef.current) {
      if (current.includes(input)) continue;
      input.removeEventListener("midimessage", handler);
      handlersRef.current.delete(input);
    }
    setInputs(names);
  };

  useEffect(() => () => disconnect(), [disconnect]);
  useEffect(() => {
    if (!playing || cursor >= report.length) {
      if (playing && cursor >= report.length) setPlaying(false);
      return;
    }
    const timer = window.setTimeout(() => {
      ingest(report[cursor]);
      setCursor((n) => n + 1);
    }, 35);
    return () => window.clearTimeout(timer);
  }, [playing, cursor, report, ingest]);

  async function connect() {
    setError("");
    if (!("requestMIDIAccess" in navigator)) {
      setError(
        "Web MIDI is unavailable in this browser. Use Chromium on localhost, or load an inventory JSON below.",
      );
      return;
    }
    try {
      // sysex:false; no MIDIOutput is opened, and this page never sends messages.
      const access = await navigator.requestMIDIAccess({ sysex: false });
      accessRef.current = access;
      access.addEventListener("statechange", onStateChange.current);
      refreshRef.current();
      setLive(true);
    } catch (cause) {
      setError(`Could not open read-only MIDI inputs: ${String(cause)}`);
    }
  }

  async function importReport(file: File | undefined) {
    if (!file) return;
    try {
      const events = flattenInventory(JSON.parse(await file.text()));
      setPlaying(false);
      setReport(events);
      setCursor(0);
      setHistory([]);
      setHeld({});
      setLast({});
      setError("");
    } catch (cause) {
      setError(`Cannot load inventory: ${String(cause)}`);
    }
  }

  function step() {
    if (cursor >= report.length) return;
    ingest(report[cursor]);
    setCursor((n) => n + 1);
  }

  function tile(id: string) {
    const recent = last[id];
    const hot = recent && Date.now() - recent.time < 1500;
    return (
      <div
        key={id}
        className={`lk-control${held[id] ? " held" : hot ? " recent" : ""}`}
        data-control={id}
      >
        <span>{label(id)}</span>
        <small>
          {recent
            ? `${recent.kind} · ${id === "pad-layout" ? (padModes[recent.value] ?? recent.value) : recent.value}`
            : "—"}
        </small>
      </div>
    );
  }

  return (
    <>
      <header>
        <h1>polyclav — Launchkey 61 debugger</h1>
        <span className="spacer" />
        <a className="version" href="/app/">
          ← Dashboard
        </a>
      </header>
      <main className="lk-debug">
        <section className="wide">
          <h2>Read-only input</h2>
          <p className="hint">
            Observe both Launchkey MK4 input ports without opening Polyclav audio or sending MIDI.
            Browser MIDI permission is required for live input. This is a debug view, not an
            instrument controller.
          </p>
          <button type="button" onClick={live ? disconnect : connect}>
            {live ? "Disconnect MIDI" : "Connect MIDI inputs"}
          </button>
          <span className="lk-ports">
            {live
              ? inputs.length
                ? inputs.join(" · ")
                : "No Launchkey MK4 inputs found"
              : "Offline"}
          </span>
          <p className="hint">
            Or load a JSON file from the guided <code>launchkey_mk4_inventory.py</code> script; step
            through each captured event without any device connection.
          </p>
          <input
            aria-label="Load inventory JSON"
            type="file"
            accept=".json,application/json"
            onChange={(e) => void importReport(e.target.files?.[0])}
          />
          {report.length > 0 && (
            <div className="lk-replay">
              <button type="button" onClick={step} disabled={cursor >= report.length || playing}>
                Next event
              </button>
              <button
                type="button"
                onClick={() => setPlaying((v) => !v)}
                disabled={cursor >= report.length}
              >
                {playing ? "Pause" : "Play report"}
              </button>
              <span>
                {cursor} / {report.length} events (compressed replay, not original timing)
              </span>
            </div>
          )}
          {error && <p role="alert">{error}</p>}
        </section>
        <section className="wide">
          <h2>Keybed · MIDI notes 24–84</h2>
          <div className="lk-keys" role="img" aria-label="61-key keyboard">
            {Array.from({ length: 61 }, (_, i) => {
              const note = i + 24;
              return (
                <div
                  key={note}
                  className={`lk-key ${blackNotes.has(note % 12) ? "black" : "white"}${held[`key-${note}`] ? " held" : ""}`}
                  data-control={`key-${note}`}
                  title={`MIDI note ${note}`}
                >
                  {note}
                </div>
              );
            })}
          </div>
        </section>
        <section className="wide">
          <h2>Pads · polyphonic pressure</h2>
          {padRows.map((row) => (
            <div className="lk-pad-row" key={row}>
              {Array.from({ length: 8 }, (_, i) => tile(`pad-${row}-${i + 1}`))}
            </div>
          ))}
        </section>
        {groups.map(([name, controls]) => (
          <section className="wide" key={name}>
            <h2>{name}</h2>
            <div className="lk-grid">{controls.map(tile)}</div>
          </section>
        ))}
        <section className="wide">
          <h2>Recent raw input</h2>
          <div className="lk-event-list" aria-live="off">
            {history.length === 0 ? (
              <p className="hint">No input yet.</p>
            ) : (
              history.map((e) => (
                <div key={e.seq}>
                  <code>
                    {e.port} ch{e.channel} {e.kind} {e.control ?? "unmapped"} {e.value} · {e.raw}
                  </code>
                </div>
              ))
            )}
          </div>
        </section>
      </main>
    </>
  );
}
