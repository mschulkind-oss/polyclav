/** Read-only Launchkey debugger: observed DAW-port messages, not outgoing MIDI. */
export type DebugEvent = {
  time: number;
  port: "DAW" | "MIDI";
  kind: string;
  channel: number; // 1-based
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
  else if (family === 0xe0) kind = "pitch-bend";
  const event: DebugEvent = {
    time,
    port,
    kind,
    channel,
    number,
    value: kind === "note-off" ? 0 : value,
    control: null,
    raw: bytes.map((b) => b.toString(16).padStart(2, "0")).join(" "),
  };
  event.control = controlForMessage(event);
  return event;
}

type InventoryEvent = {
  timestamp?: string;
  elapsed_ms?: number;
  port_role?: string;
  kind?: string;
  channel?: number;
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
