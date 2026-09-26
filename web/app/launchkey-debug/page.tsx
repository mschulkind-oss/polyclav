"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import {
  controlForMessage,
  type DebugEvent,
  decodeBackendRaw,
  decodeMessage,
  encoderStep,
  flattenInventory,
  type SourceMode,
} from "@/lib/launchkeyDebug";
import "./style.css";

const blackNotes = new Set([1, 3, 6, 8, 10]);
const padRows = ["top", "bottom"] as const;
const sourceLabels: Record<SourceMode, string> = {
  browser: "Browser Web MIDI",
  daemon: "daemon raw SSE",
  offline: "offline report",
};
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
const transport = ["rewind", "fast-forward", "stop", "loop", "play", "record"];
const dawCommands = ["capture", "undo", "quantise", "metronome"];
const navigation = [
  "track-left",
  "track-right",
  "display-up",
  "display-down",
  "shift",
  "settings",
  "function",
  "pad-up",
  "pad-down",
  "pad-right",
];
const modeControls = [
  "pad-layout",
  "encoder-layout",
  "fader-layout",
  "scale",
  "arp",
  "latch",
  "chord-map",
  "fixed-chord",
];

const label = (name: string) => name.replaceAll("-", " ");
const faders = Array.from({ length: 9 }, (_, i) => `fader-${i + 1}`);
const faderButtons = Array.from({ length: 9 }, (_, i) => `fader-button-${i + 1}`);
const encoders = Array.from({ length: 8 }, (_, i) => `encoder-${i + 1}`);

export default function LaunchkeyDebugPage() {
  const [sourceMode, setSourceModeState] = useState<SourceMode>("browser");
  const activeSource = useRef<SourceMode>("browser");
  const [browserLive, setBrowserLive] = useState(false);
  const [daemonLive, setDaemonLive] = useState(false);
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
  const browserConnectToken = useRef(0);
  const handlersRef = useRef<Map<MIDIInput, (event: MIDIMessageEvent) => void>>(new Map());

  const resetSurface = useCallback(() => {
    setHeld({});
    setLast({});
    setHistory([]);
    sequence.current = 0;
  }, []);

  const ingest = useCallback((mode: SourceMode, e: DebugEvent) => {
    if (activeSource.current !== mode) return;
    const control = e.control ?? controlForMessage(e);
    const event = { ...e, source: mode, control };
    if (control) {
      setLast((prev) => ({ ...prev, [control]: event }));
      if (
        event.kind === "note-on" ||
        event.kind === "note-off" ||
        (event.kind === "cc" &&
          event.value >= 0 &&
          (control.includes("button") ||
            transport.includes(control) ||
            dawCommands.includes(control) ||
            control === "sustain"))
      ) {
        setHeld((prev) => ({
          ...prev,
          [control]: event.kind === "note-on" || (event.kind === "cc" && event.value >= 64),
        }));
      }
    }
    setHistory((prev) => [{ ...event, seq: ++sequence.current }, ...prev].slice(0, 80));
  }, []);

  const disconnectBrowser = useCallback(() => {
    browserConnectToken.current += 1;
    for (const [input, handler] of handlersRef.current)
      input.removeEventListener("midimessage", handler);
    handlersRef.current.clear();
    if (accessRef.current)
      accessRef.current.removeEventListener("statechange", onStateChange.current);
    accessRef.current = null;
    setBrowserLive(false);
    setInputs([]);
  }, []);

  const setSourceMode = useCallback(
    (mode: SourceMode) => {
      activeSource.current = mode;
      setSourceModeState(mode);
      setPlaying(false);
      setError("");
      resetSurface();
      if (mode !== "browser") disconnectBrowser();
    },
    [disconnectBrowser, resetSurface],
  );

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
            "browser",
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

  useEffect(() => () => disconnectBrowser(), [disconnectBrowser]);

  useEffect(() => {
    if (sourceMode !== "daemon") {
      setDaemonLive(false);
      return;
    }
    let disposed = false;
    let retry: ReturnType<typeof setTimeout> | undefined;
    let es: EventSource | null = null;
    const connect = () => {
      es = new EventSource("/api/events");
      es.onopen = () => {
        if (!disposed) setDaemonLive(true);
      };
      es.onerror = () => {
        if (!disposed) setDaemonLive(false);
        if (!disposed && es?.readyState === EventSource.CLOSED) {
          es.close();
          es = null;
          retry = setTimeout(connect, 2000);
        }
      };
      es.addEventListener("midi-raw", (ev: MessageEvent) => {
        if (disposed || activeSource.current !== "daemon") return;
        let payload: unknown;
        try {
          payload = JSON.parse(ev.data);
        } catch {
          return;
        }
        const decoded = decodeBackendRaw(payload);
        if (decoded) ingest("daemon", decoded);
      });
    };
    connect();
    return () => {
      disposed = true;
      setDaemonLive(false);
      if (retry !== undefined) clearTimeout(retry);
      es?.close();
    };
  }, [sourceMode, ingest]);

  useEffect(() => {
    if (!playing || cursor >= report.length || activeSource.current !== "offline") {
      if (playing && cursor >= report.length) setPlaying(false);
      return;
    }
    const timer = window.setTimeout(() => {
      ingest("offline", report[cursor]);
      setCursor((n) => n + 1);
    }, 35);
    return () => window.clearTimeout(timer);
  }, [playing, cursor, report, ingest]);

  async function connectBrowser() {
    setSourceMode("browser");
    setError("");
    if (!("requestMIDIAccess" in navigator)) {
      setError(
        "Web MIDI is unavailable in this browser. Use Chromium on localhost, daemon raw SSE, or load an inventory JSON below.",
      );
      return;
    }
    try {
      browserConnectToken.current += 1;
      const token = browserConnectToken.current;
      // sysex:false; no MIDIOutput is opened, and this page never sends messages.
      const access = await navigator.requestMIDIAccess({ sysex: false });
      if (browserConnectToken.current !== token || activeSource.current !== "browser") return;
      accessRef.current = access;
      access.addEventListener("statechange", onStateChange.current);
      refreshRef.current();
      setBrowserLive(true);
    } catch (cause) {
      setError(`Could not open read-only MIDI inputs: ${String(cause)}`);
    }
  }

  function disconnectCurrent() {
    if (sourceMode === "browser") disconnectBrowser();
    setSourceMode("offline");
  }

  async function importReport(file: File | undefined) {
    if (!file) return;
    try {
      const events = flattenInventory(JSON.parse(await file.text()));
      setSourceMode("offline");
      setPlaying(false);
      setReport(events);
      setCursor(0);
      setError("");
    } catch (cause) {
      setError(`Cannot load inventory: ${String(cause)}`);
    }
  }

  function step() {
    if (cursor >= report.length) return;
    ingest("offline", report[cursor]);
    setCursor((n) => n + 1);
  }

  function valueText(id: string, recent: DebugEvent | undefined) {
    if (!recent) return "—";
    if (id.startsWith("encoder-")) return `step ${encoderStep(recent.value)}`;
    if (id === "pad-layout") return padModes[recent.value] ?? recent.value;
    if (recent.kind === "poly-aftertouch") return `pressure ${recent.value}`;
    if (recent.kind === "pitch-bend") return `${recent.value}`;
    return `${recent.value}`;
  }

  function tile(id: string, className = "") {
    const recent = last[id];
    const hot = recent && Date.now() - recent.time < 1500;
    return (
      <div
        key={id}
        className={`lk-control ${className}${held[id] ? " held" : hot ? " recent" : ""}`}
        data-control={id}
      >
        <span>{label(id)}</span>
        <small>{recent ? `${recent.kind} · ${valueText(id, recent)}` : "—"}</small>
      </div>
    );
  }

  const status =
    sourceMode === "browser"
      ? browserLive
        ? inputs.length
          ? inputs.join(" · ")
          : "Browser MIDI connected; no Launchkey MK4 inputs found"
        : "Browser MIDI disconnected"
      : sourceMode === "daemon"
        ? daemonLive
          ? "Daemon SSE connected; waiting for midi-raw events"
          : "Daemon SSE reconnecting or disconnected"
        : report.length
          ? `Offline report loaded: ${cursor} / ${report.length} events`
          : "Offline; load a report or choose a live source";

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
        <section className="lk-panel" aria-label="Launchkey 61 physical debug surface">
          <div className="lk-source-strip">
            <fieldset className="lk-source-buttons">
              <legend>Input source</legend>
              {(Object.keys(sourceLabels) as SourceMode[]).map((mode) => (
                <button
                  key={mode}
                  type="button"
                  className={sourceMode === mode ? "active" : ""}
                  onClick={() => {
                    if (mode === "browser") void connectBrowser();
                    else setSourceMode(mode);
                  }}
                >
                  {sourceLabels[mode]}
                </button>
              ))}
            </fieldset>
            <button type="button" onClick={disconnectCurrent}>
              Disconnect
            </button>
            <span className={`lk-dot ${browserLive || daemonLive ? "on" : ""}`} />
            <span className="lk-ports">{status}</span>
          </div>
          <p className="hint lk-source-note">
            Read-only debug view. Browser mode opens input ports only and never sends MIDI. Daemon
            mode listens to the daemon's existing <code>midi-raw</code> SSE stream, including
            unsupported raw events and port names, without opening another ALSA connection.
          </p>
          {error && <p role="alert">{error}</p>}

          <div className="lk-body">
            <section className="lk-block lk-left-block" aria-label="Left performance block">
              <h2>Performance</h2>
              <div className="lk-wheels">
                {tile("pitch-wheel", "wheel")}
                {tile("mod-wheel", "wheel")}
              </div>
              <div className="lk-mini-row">
                {tile("sustain")}
                {tile("octave-minus")}
                {tile("octave-plus")}
              </div>
            </section>

            <section className="lk-block lk-mixer" aria-label="Mixer block">
              <h2>9 faders</h2>
              <div className="lk-faders">{faders.map((id) => tile(id, "fader"))}</div>
              <div className="lk-fader-buttons">
                {faderButtons.map((id) => tile(id, "fader-button"))}
              </div>
            </section>

            <section className="lk-block lk-nav" aria-label="Display and navigation block">
              <h2>Display / nav</h2>
              <div className="lk-display">Launchkey display</div>
              <div className="lk-grid compact">{navigation.map((id) => tile(id))}</div>
              <div className="lk-grid compact modes">{modeControls.map((id) => tile(id))}</div>
            </section>

            <section className="lk-block lk-encoders" aria-label="Encoder block">
              <h2>8 encoders · relative steps</h2>
              <div className="lk-encoder-row">{encoders.map((id) => tile(id, "encoder"))}</div>
              <p className="hint">
                Values are displayed as signed steps around center 64, not speed.
              </p>
            </section>

            <section className="lk-block lk-pads" aria-label="Pads and transport block">
              <h2>16 pads + transport</h2>
              <div className="lk-pad-matrix">
                {padRows.map((row) => (
                  <div className="lk-pad-row" key={row}>
                    {Array.from({ length: 8 }, (_, i) => tile(`pad-${row}-${i + 1}`, "pad"))}
                  </div>
                ))}
              </div>
              <div className="lk-transport">{transport.map((id) => tile(id))}</div>
              <div className="lk-daw-commands">{dawCommands.map((id) => tile(id))}</div>
            </section>
          </div>

          <section className="lk-block lk-keybed" aria-label="61-key keybed">
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
        </section>

        <section className="wide lk-offline-tools">
          <h2>Offline report replay</h2>
          <p className="hint">
            Load a JSON file from the guided <code>launchkey_mk4_inventory.py</code> script; step
            through captured events without any device connection.
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
                onClick={() => {
                  setSourceMode("offline");
                  setPlaying((v) => !v);
                }}
                disabled={cursor >= report.length}
              >
                {playing ? "Pause" : "Play report"}
              </button>
              <span>
                {cursor} / {report.length} events (compressed replay, not original timing)
              </span>
            </div>
          )}
        </section>

        <details className="wide lk-raw-details">
          <summary>Recent raw input</summary>
          <div className="lk-event-list" aria-live="off">
            {history.length === 0 ? (
              <p className="hint">No input yet.</p>
            ) : (
              history.map((e) => (
                <div key={e.seq}>
                  <code>
                    {sourceLabels[e.source ?? sourceMode]} · {e.portName ? `${e.portName} ` : ""}
                    {e.port} {e.channel ? `ch${e.channel}` : "system"} {e.kind}{" "}
                    {e.control ?? "unmapped"} {e.value} · {e.raw}
                  </code>
                </div>
              ))
            )}
          </div>
        </details>
      </main>
    </>
  );
}
