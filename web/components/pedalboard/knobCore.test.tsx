import { describe, expect, it } from "vitest";
import {
  KNOB_ARC_HIDE_LEN,
  knobSizeStyle,
  MINI_ARC_HIDE_LEN,
  splitReadout,
} from "@/components/pedalboard/knobCore";

describe("splitReadout", () => {
  it("splits every unit into number + unit line", () => {
    expect(splitReadout(15, { unit: "%" })).toEqual({ num: "15", unit: "%" });
    expect(splitReadout(0.9, { unit: "Hz" })).toEqual({ num: "0.9", unit: "Hz" });
    expect(splitReadout(380, { unit: "ms" })).toEqual({ num: "380", unit: "ms" });
    expect(splitReadout(-6, { unit: "dB" })).toEqual({ num: "−6.0", unit: "dB" });
    expect(splitReadout(3.4, { unit: "" })).toEqual({ num: "3", unit: "" });
  });

  it("splits hzlog readouts below and above 1 kHz", () => {
    expect(splitReadout(40, { unit: "", fmt: "hzlog" })).toEqual({ num: "317", unit: "Hz" });
    expect(splitReadout(62, { unit: "", fmt: "hzlog" })).toEqual({ num: "1.4", unit: "kHz" });
  });
});

describe("arc hide thresholds", () => {
  it("matches the reference cutoffs", () => {
    expect(KNOB_ARC_HIDE_LEN).toBeCloseTo(1.08, 10); // frac 0.004 of the 270 sweep
    expect(MINI_ARC_HIDE_LEN).toBe(0.75);
  });
});

describe("knobSizeStyle", () => {
  it("derives width and height from --u", () => {
    expect(knobSizeStyle(124)).toEqual({
      width: "calc(var(--u) * 124)",
      height: "calc(var(--u) * 124)",
    });
  });
});
