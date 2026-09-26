import { describe, expect, it } from "vitest";
import { controlForMessage, decodeMessage, flattenInventory } from "./launchkeyDebug";

describe("Launchkey debug decoding", () => {
  it("distinguishes DAW button CCs from mode and fader messages", () => {
    expect(controlForMessage(decodeMessage([0xb0, 103, 127], "DAW"))).toBe("track-left");
    expect(controlForMessage(decodeMessage([0xb0, 37, 127], "DAW"))).toBe("fader-button-1");
    expect(controlForMessage(decodeMessage([0xb6, 29, 4], "DAW"))).toBe("pad-layout");
    expect(controlForMessage(decodeMessage([0xbf, 5, 62], "DAW"))).toBe("fader-1");
    expect(controlForMessage(decodeMessage([0xb0, 103, 127], "MIDI"))).toBeNull();
  });

  it("maps per-pad pressure, release and keybed notes without treating pressure as a new press", () => {
    expect(controlForMessage(decodeMessage([0x90, 96, 82], "DAW"))).toBe("pad-top-1");
    const pressure = decodeMessage([0xa0, 96, 54], "DAW");
    expect(pressure.kind).toBe("poly-aftertouch");
    expect(controlForMessage(pressure)).toBe("pad-top-1");
    expect(controlForMessage(decodeMessage([0x93, 60, 0], "MIDI"))).toBe("key-60");
  });

  it("recognizes pressure from older inventories that recorded it as raw", () => {
    const events = flattenInventory({
      controls: [
        {
          events: [
            {
              port_role: "daw",
              kind: "raw",
              channel: null,
              raw_line: "32:1 Polyphonic aftertouch 0, note 96, value 78",
              raw_values: { note: 96, value: 78 },
            },
          ],
        },
      ],
    });
    expect(events[0].kind).toBe("poly-aftertouch");
    expect(events[0].channel).toBe(1);
    expect(events[0].control).toBe("pad-top-1");
  });

  it("replays parsed inventory data in time order and includes pressure", () => {
    const report = {
      controls: [
        {
          events: [
            {
              elapsed_ms: 12,
              port_role: "daw",
              kind: "poly_aftertouch",
              channel: 1,
              raw_values: { note: 98, value: 45 },
            },
          ],
        },
        {
          events: [
            {
              elapsed_ms: 3,
              port_role: "daw",
              kind: "cc",
              channel: 1,
              raw_values: { controller: 103, value: 127 },
            },
          ],
        },
      ],
    };
    const events = flattenInventory(report);
    expect(events.map((e) => e.control)).toEqual(["track-left", "pad-top-3"]);
    expect(events[1].value).toBe(45);
  });
});
