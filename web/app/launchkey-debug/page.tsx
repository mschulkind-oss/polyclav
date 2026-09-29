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
const transport = ["stop", "loop", "play", "record"];
const dawCommands = ["capture", "undo", "quantise", "metronome"];
const navigation = [
  "track-left",
  "track-right",
  "encoder-up",
  "encoder-down",
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
const shortLabel = (id: string) => {
  const numbered = /^(fader|fader-button|encoder|pad-(top|bottom))-(\d+)$/.exec(id);
  if (!numbered) return label(id);
  const prefix: Record<string, string> = {
    fader: "",
    "fader-button": "",
    encoder: "E",
    "pad-top": "",
    "pad-bottom": "",
  };
  return `${prefix[numbered[1]]}${numbered[3]}`;
};
const faders = Array.from({ length: 9 }, (_, i) => `fader-${i + 1}`);
const faderButtons = Array.from({ length: 9 }, (_, i) => `fader-button-${i + 1}`);
const encoders = Array.from({ length: 8 }, (_, i) => `encoder-${i + 1}`);

export default function LaunchkeyDebugPage() {
  const [sourceMode, setSourceModeState] = useState<SourceMode>("browser");
  const activeSource = useRef<SourceMode>("browser");
  const [browserLive, setBrowserLive] = useState(false);
  const [daemonLive, setDaemonLive] = useState(false);
  const [daemonLaunchkey, setDaemonLaunchkey] = useState<string | null>(null);
  const [error, setError] = useState("");
  const [inputs, setInputs] = useState<string[]>([]);
  const [history, setHistory] = useState<(DebugEvent & { seq: number })[]>([]);
  const [firstInputAnnouncement, setFirstInputAnnouncement] = useState("");
  const sequence = useRef(0);
  const [last, setLast] = useState<Record<string, DebugEvent>>({});
  const [held, setHeld] = useState<Record<string, boolean>>({});
  const [pressure, setPressure] = useState<Record<string, number>>({});
  // Encoders report relative steps, not an absolute shaft position. This
  // marker accumulates observed motion at 4° per step and starts at zero
  // for each source; its angle is not a physical knob position.
  const [encoderAngles, setEncoderAngles] = useState<Record<string, number>>({});
  const [report, setReport] = useState<DebugEvent[]>([]);
  const [cursor, setCursor] = useState(0);
  const [playing, setPlaying] = useState(false);
  const accessRef = useRef<MIDIAccess | null>(null);
  const browserConnectToken = useRef(0);
  const handlersRef = useRef<Map<MIDIInput, (event: MIDIMessageEvent) => void>>(new Map());

  const resetSurface = useCallback(() => {
    setHeld({});
    setPressure({});
    setEncoderAngles({});
    setLast({});
    setHistory([]);
    setFirstInputAnnouncement("");
    sequence.current = 0;
  }, []);

  const ingest = useCallback((mode: SourceMode, e: DebugEvent) => {
    if (activeSource.current !== mode) return;
    const control = e.control ?? controlForMessage(e);
    const event = { ...e, source: mode, control };
    if (control) {
      setLast((prev) => ({ ...prev, [control]: event }));
      if (control.startsWith("encoder-") && event.kind === "cc") {
        setEncoderAngles((prev) => ({
          ...prev,
          [control]: ((((prev[control] ?? 0) + (event.value - 64) * 4) % 360) + 360) % 360,
        }));
      }
      if (
        control.startsWith("pad-") &&
        ["note-on", "note-off", "poly-aftertouch"].includes(event.kind)
      ) {
        setPressure((prev) => {
          const next = { ...prev };
          if (event.kind === "poly-aftertouch") next[control] = event.value;
          else delete next[control];
          return next;
        });
      }
      if (
        event.kind === "note-on" ||
        event.kind === "note-off" ||
        (event.kind === "cc" &&
          event.value >= 0 &&
          (control.includes("button") ||
            transport.includes(control) ||
            navigation.includes(control) ||
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
    if (sequence.current === 1) setFirstInputAnnouncement("First input detected");
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
      setDaemonLaunchkey(null);
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
      setDaemonLaunchkey(null);
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
      es.addEventListener("snapshot", (ev: MessageEvent) => {
        if (disposed || activeSource.current !== "daemon") return;
        try {
          const snapshot = JSON.parse(ev.data);
          const state = snapshot?.devices?.launchkey;
          setDaemonLaunchkey(typeof state === "string" ? state : null);
        } catch {
          setDaemonLaunchkey(null);
        }
      });
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
    const position = recent
      ? `${Math.round(
          (Math.max(0, Math.min(recent.value, id === "pitch-wheel" ? 16383 : 127)) /
            (id === "pitch-wheel" ? 16383 : 127)) *
            100,
        )}%`
      : null;
    const padPressure = held[id] ? pressure[id] : undefined;
    const padStyle =
      padPressure === undefined
        ? undefined
        : {
            backgroundColor: `hsl(${Math.round(155 - (padPressure * 125) / 127)} 70% ${Math.round(25 + (padPressure * 7) / 127)}%)`,
          };
    return (
      <div
        key={id}
        className={`lk-control ${className}${held[id] ? " held" : hot ? " recent" : ""}`}
        data-control={id}
        data-pressure={padPressure}
        style={padStyle}
      >
        {className === "fader" && (
          <span className="lk-fader-track" aria-hidden="true">
            {position !== null && <span className="lk-fader-fill" style={{ height: position }} />}
          </span>
        )}
        {className === "wheel" && (
          <span className="lk-wheel-track" aria-hidden="true">
            {position !== null && <span className="lk-wheel-marker" style={{ bottom: position }} />}
          </span>
        )}
        {className === "encoder" && (
          <span className="lk-encoder-dial" aria-hidden="true">
            {encoderAngles[id] !== undefined && (
              <span
                className="lk-encoder-marker"
                style={{ transform: `rotate(${encoderAngles[id]}deg)` }}
              />
            )}
          </span>
        )}
        <span role="img" aria-label={label(id).replace(/^./, (c) => c.toUpperCase())}>
          {shortLabel(id)}
        </span>
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
          ? daemonLaunchkey === "absent"
            ? "SSE connected; Launchkey absent from daemon. DAW controls will not arrive. Check USB and daemon logs."
            : `SSE connected; Launchkey ${daemonLaunchkey ?? "state unknown"}. Waiting for midi-raw events.`
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
          {/* Scrolling long port names must remain reachable from the keyboard. */}
          {/* biome-ignore lint/a11y/noNoninteractiveTabindex: this status is a scrollable region. */}
          <div className="lk-latest" role="status" aria-live="off" tabIndex={0}>
            <strong>Latest input</strong>
            {history.length ? (
              <span>
                {sourceLabels[history[0].source ?? sourceMode]} ·{" "}
                {history[0].control ? label(history[0].control) : "unmapped"} · {history[0].kind} ·
                value {history[0].value} · {history[0].portName ?? history[0].port} (
                {history[0].port}) ·{" "}
                {history[0].channel ? `channel ${history[0].channel}` : "system"}
              </span>
            ) : (
              <span>Waiting for input</span>
            )}
          </div>
          <span className="lk-sr-only" aria-live="polite">
            {firstInputAnnouncement}
          </span>
          <div className="lk-source-note">
            <p>
              <strong>Read-only: this page cannot turn on DAW mode.</strong> Browser Web MIDI reads
              input ports only; it never sends MIDI. The daemon activates DAW mode when it opens the
              Launchkey DAW ports at startup; this page cannot confirm the keyboard's current DAW
              mode from a port listing. DAW ports appearing does not mean DAW mode is on.
            </p>
            <p>
              For browser input alone, run <code>just web-dev</code> and open{" "}
              <code>http://localhost:3000/app/launchkey-debug/</code>; click Browser Web MIDI and
              allow permission. This does not start the daemon. For daemon raw SSE, run{" "}
              <code>just dev</code> and open <code>http://localhost:5100/app/launchkey-debug/</code>
              ; select daemon raw SSE to read its existing event stream. Offline report replay needs
              neither the daemon nor MIDI permission.
            </p>
            <p>
              If nothing arrives, play a key or move a control, check the selected source and port
              status above, and confirm the keyboard is connected. DAW controls require the daemon
              to have opened its DAW ports; browser permission alone cannot enable them. Raw bytes
              remain under Recent raw input.
            </p>
            <p>
              Settings, Octave, and Fixed Chord gave no button MIDI in our capture; they can act
              locally on the keyboard. To check Octave or Fixed Chord, play one key afterward and
              inspect its notes rather than expecting those buttons to light up.
            </p>
          </div>
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
              <h2>9 faders · 1–9</h2>
              <div className="lk-faders">{faders.map((id) => tile(id, "fader"))}</div>
              <p className="lk-row-label">Fader buttons · 1–9</p>
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
                The dial accumulates relative steps from 12 o’clock; it is not the knob’s absolute
                position. Text shows the last signed step around center 64.
              </p>
            </section>

            <section className="lk-block lk-pads" aria-label="Pads and transport block">
              <h2>16 pads + transport</h2>
              <div className="lk-pad-matrix">
                {padRows.map((row) => (
                  <div key={row}>
                    <p className="lk-row-label">{row === "top" ? "Top row" : "Bottom row"} · 1–8</p>
                    <div className="lk-pad-row">
                      {Array.from({ length: 8 }, (_, i) => tile(`pad-${row}-${i + 1}`, "pad"))}
                    </div>
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

        <details className="wide lk-raw-details" open>
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
