/** Read-only Launchkey debugger: observed input messages, never outgoing MIDI. */
export type SourceMode = "browser" | "daemon" | "offline";
export type PortRole = "DAW" | "MIDI" | "unknown";

export type DebugEvent = {
  time: number;
  port: PortRole;
  portName?: string;
  source?: "browser" | "daemon" | "offline";
  daemonSource?: string;
  kind: string;
  channel: number; // 1-based for display; 0 when unknown/not channelized
  number: number;
  value: number;
  control: string | null;
  raw: string;
};

const dawButtons: Record<number, string> = {
  37: "fader-button-1",
  38: "fader-button-2",
  39: "fader-button-3",
  40: "fader-button-4",
  41: "fader-button-5",
  42: "fader-button-6",
  43: "fader-button-7",
  44: "fader-button-8",
  45: "fader-button-9",
  51: "display-up",
  52: "display-down",
  74: "capture",
  75: "quantise",
  76: "metronome",
  77: "undo",
  102: "track-right",
  103: "track-left",
  104: "pad-right",
  105: "function",
  106: "pad-up",
  107: "pad-down",
  115: "play",
  116: "stop",
  117: "record",
  118: "loop",
};
const featureControls: Record<number, string> = {
  29: "pad-layout",
  30: "encoder-layout",
  31: "fader-layout",
  63: "shift",
  73: "arp",
  74: "scale",
  88: "latch",
};

export function controlForMessage(
  event: Pick<DebugEvent, "port" | "kind" | "channel" | "number">,
): string | null {
  const { port, kind, channel, number } = event;
  if (port === "DAW") {
    if (
      (kind === "note-on" || kind === "note-off" || kind === "poly-aftertouch") &&
      channel === 1
    ) {
      if (number >= 96 && number <= 103) return `pad-top-${number - 95}`;
      if (number >= 112 && number <= 119) return `pad-bottom-${number - 111}`;
    }
    if (kind === "cc") {
      if (channel === 1) return dawButtons[number] ?? null;
      if (channel === 7) return featureControls[number] ?? null;
      if (channel === 16) {
        if (number >= 5 && number <= 13) return `fader-${number - 4}`;
        if (number >= 37 && number <= 45) return `fader-button-${number - 36}`;
        if (number >= 85 && number <= 92) return `encoder-${number - 84}`;
      }
    }
  }
  if (
    port === "MIDI" &&
    (kind === "note-on" || kind === "note-off") &&
    number >= 0 &&
    number <= 127
  ) {
    return `key-${number}`;
  }
  if (port === "MIDI" && kind === "cc") {
    if (number === 1) return "mod-wheel";
    if (number === 64) return "sustain";
  }
  if (port === "MIDI" && kind === "pitch-bend") return "pitch-wheel";
  return null;
}

export function rawHex(bytes: readonly number[]): string {
  return bytes.map((b) => b.toString(16).padStart(2, "0")).join(" ");
}

export function normalizeRaw(raw: unknown): string {
  if (typeof raw !== "string") return "";
  const compact = raw.replace(/\s+/g, "").toLowerCase();
  if (compact.length >= 2 && compact.length % 2 === 0 && /^[0-9a-f]+$/.test(compact)) {
    return compact.match(/../g)?.join(" ") ?? raw;
  }
  return raw;
}

export function roleFromDaemon(source: unknown, portName: unknown): PortRole {
  if (source === "launchkey-daw") return "DAW";
  if (source === "performance") return "MIDI";
  if (typeof portName === "string") {
    if (/\bdaw\b|midi 2/i.test(portName)) return "DAW";
    if (/midi/i.test(portName)) return "MIDI";
  }
  return "unknown";
}

export function decodeMessage(
  bytes: readonly number[],
  port: "DAW" | "MIDI",
  time = Date.now(),
): DebugEvent {
  const status = bytes[0] ?? 0;
  const family = status & 0xf0;
  const channel = (status & 0x0f) + 1;
  const number = bytes[1] ?? 0;
  const value = bytes[2] ?? 0;
  let kind = "other";
  if (family === 0x90) kind = value === 0 ? "note-off" : "note-on";
  else if (family === 0x80) kind = "note-off";
  else if (family === 0xa0) kind = "poly-aftertouch";
  else if (family === 0xb0) kind = "cc";
  else if (family === 0xc0) kind = "program-change";
  else if (family === 0xd0) kind = "aftertouch";
  else if (family === 0xe0) kind = "pitch-bend";
  const event: DebugEvent = {
    time,
    port,
    source: "browser",
    kind,
    channel,
    number,
    value: kind === "note-off" ? 0 : value,
    control: null,
    raw: rawHex(bytes),
  };
  event.control = controlForMessage(event);
  return event;
}

type BackendRaw = {
  time?: string | number;
  port?: string;
  source?: string;
  kind?: string;
  raw?: string;
  channel?: number; // daemon sends 0-based when present
  data1?: number;
  data2?: number;
  bend?: number;
};

export function decodeBackendRaw(payload: unknown, now = Date.now()): DebugEvent | null {
  if (!payload || typeof payload !== "object") return null;
  const data = payload as BackendRaw;
  const kind = typeof data.kind === "string" ? data.kind : "other";
  const portName = typeof data.port === "string" ? data.port : "";
  const role = roleFromDaemon(data.source, portName);
  const time = typeof data.time === "string" ? Date.parse(data.time) : Number(data.time ?? now);
  const event: DebugEvent = {
    time: Number.isFinite(time) ? time : now,
    port: role,
    portName,
    source: "daemon",
    daemonSource: typeof data.source === "string" ? data.source : undefined,
    kind,
    channel: typeof data.channel === "number" ? data.channel + 1 : 0,
    number: typeof data.data1 === "number" ? data.data1 : 0,
    value: typeof data.data2 === "number" ? data.data2 : 0,
    control: null,
    raw: normalizeRaw(data.raw),
  };
  if ((kind === "aftertouch" || kind === "program-change") && typeof data.data1 === "number") {
    event.value = data.data1;
  }
  if (kind === "pitch-bend" && typeof data.bend === "number") event.value = data.bend;
  event.control = controlForMessage(event);
  return event;
}

export function encoderStep(value: number): string {
  const delta = value - 64;
  if (delta === 0) return "0";
  return delta > 0 ? `+${delta}` : `${delta}`;
}

type InventoryEvent = {
  timestamp?: string;
  elapsed_ms?: number;
  port_role?: string;
  kind?: string;
  channel?: number | null;
  raw_values?: Record<string, number | string>;
  raw_line?: string;
};
export function flattenInventory(report: unknown): DebugEvent[] {
  if (
    !report ||
    typeof report !== "object" ||
    !("controls" in report) ||
    !Array.isArray(report.controls)
  ) {
    throw new Error("Expected a Launchkey inventory JSON file with a controls array");
  }
  const events: DebugEvent[] = [];
  for (const control of report.controls) {
    if (!control || typeof control !== "object" || !Array.isArray(control.events)) continue;
    for (const item of control.events as InventoryEvent[]) {
      if (!item || (item.port_role !== "daw" && item.port_role !== "midi")) continue;
      const port = item.port_role === "daw" ? "DAW" : "MIDI";
      const legacyPressure =
        item.kind === "raw" && /Polyphonic aftertouch\s+(\d+),\s*note/i.exec(item.raw_line ?? "");
      const kind = legacyPressure ? "poly-aftertouch" : (item.kind ?? "raw").replaceAll("_", "-");
      const values = item.raw_values ?? {};
      const number = Number(values.note ?? values.controller ?? 0);
      const value = Number(values.velocity ?? values.value ?? 0);
      const event: DebugEvent = {
        time: item.timestamp ? Date.parse(item.timestamp) : (item.elapsed_ms ?? 0),
        port,
        source: "offline",
        kind,
        channel: item.channel ?? (legacyPressure ? Number(legacyPressure[1]) + 1 : 0),
        number,
        value,
        control: null,
        raw: item.raw_line ?? "",
      };
      if (!Number.isFinite(event.time)) continue;
      event.control = controlForMessage(event);
      events.push(event);
    }
  }
  return events.sort((a, b) => a.time - b.time);
}
