import { describe, expect, it } from "vitest";
import {
  A0,
  arcDash,
  clamp,
  dragValue,
  formatValue,
  fracToAngle,
  gateNotchTransform,
  keyStep,
  pointerTransform,
  SWEEP,
  valueToFrac,
  wheelStep,
} from "@/lib/pedalboard/knobMath";

describe("constants", () => {
  it("uses the -135..+135 sweep canon", () => {
    expect(A0).toBe(-135);
    expect(SWEEP).toBe(270);
  });
});

describe("clamp", () => {
  it("passes through in-range values", () => {
    expect(clamp(5, 0, 10)).toBe(5);
  });
  it("clamps below and above", () => {
    expect(clamp(-3, 0, 10)).toBe(0);
    expect(clamp(42, 0, 10)).toBe(10);
  });
  it("handles negative ranges", () => {
    expect(clamp(-30, -24, 24)).toBe(-24);
    expect(clamp(0, -48, 0)).toBe(0);
  });
});

describe("valueToFrac", () => {
  it("maps min to 0, max to 1, midpoint to 0.5", () => {
    expect(valueToFrac(0, 0, 100)).toBe(0);
    expect(valueToFrac(100, 0, 100)).toBe(1);
    expect(valueToFrac(50, 0, 100)).toBe(0.5);
  });
  it("handles offset ranges", () => {
    expect(valueToFrac(380, 1, 1000)).toBeCloseTo(379 / 999, 10);
    expect(valueToFrac(0, -24, 24)).toBe(0.5);
    expect(valueToFrac(-6, -48, 0)).toBeCloseTo(0.875, 10);
  });
  it("clamps out-of-range values to 0..1", () => {
    expect(valueToFrac(-10, 0, 100)).toBe(0);
    expect(valueToFrac(200, 0, 100)).toBe(1);
  });
});

describe("fracToAngle", () => {
  it("sweeps -135deg .. +135deg", () => {
    expect(fracToAngle(0)).toBe(-135);
    expect(fracToAngle(0.5)).toBe(0);
    expect(fracToAngle(1)).toBe(135);
  });
  it("is linear in between", () => {
    expect(fracToAngle(0.25)).toBeCloseTo(-67.5, 10);
    expect(fracToAngle(0.75)).toBeCloseTo(67.5, 10);
  });
});

describe("arcDash (unipolar)", () => {
  it("draws frac*270 dash units from the sweep start", () => {
    expect(arcDash(0.5)).toEqual({ dasharray: "135 360", dashoffset: "0" });
    expect(arcDash(1)).toEqual({ dasharray: "270 360", dashoffset: "0" });
  });
  it("never lets the dash length reach zero (linecap dot guard)", () => {
    expect(arcDash(0)).toEqual({ dasharray: "0.01 360", dashoffset: "0" });
  });
});

describe("arcDash (bipolar)", () => {
  it("draws nothing at center", () => {
    expect(arcDash(0.5, true)).toEqual({ dasharray: "0.01 360", dashoffset: "-135" });
  });
  it("grows from center toward max", () => {
    // f=1: arc occupies the upper half of the sweep, offset back to center.
    expect(arcDash(1, true)).toEqual({ dasharray: "135 360", dashoffset: "-135" });
    expect(arcDash(0.75, true)).toEqual({ dasharray: "67.5 360", dashoffset: "-135" });
  });
  it("grows from center toward min", () => {
    // f=0: arc runs from the sweep start up to center (no offset).
    expect(arcDash(0, true)).toEqual({ dasharray: "135 360", dashoffset: "0" });
    expect(arcDash(0.25, true)).toEqual({ dasharray: "67.5 360", dashoffset: "-67.5" });
  });
});

describe("pointerTransform / gateNotchTransform", () => {
  it("rotates the pointer about the knob center", () => {
    expect(pointerTransform(0, 50)).toBe("rotate(-135 50 50)");
    expect(pointerTransform(0.5, 50)).toBe("rotate(0 50 50)");
    expect(pointerTransform(1, 18)).toBe("rotate(135 18 18)");
  });
  it("parks the gate notch at the sweep start", () => {
    expect(gateNotchTransform(62)).toBe("rotate(45 62 62)");
  });
});

describe("formatValue", () => {
  it("formats percentages as rounded ints", () => {
    expect(formatValue(15, { unit: "%" })).toBe("15%");
    expect(formatValue(34.6, { unit: "%" })).toBe("35%");
    expect(formatValue(0, { unit: "%" })).toBe("0%");
  });
  it("formats Hz with one decimal", () => {
    expect(formatValue(0.9, { unit: "Hz" })).toBe("0.9 Hz");
    expect(formatValue(5.25, { unit: "Hz" })).toBe("5.3 Hz");
    expect(formatValue(15, { unit: "Hz" })).toBe("15.0 Hz");
  });
  it("formats ms rounded", () => {
    expect(formatValue(380, { unit: "ms" })).toBe("380 ms");
    expect(formatValue(380.4, { unit: "ms" })).toBe("380 ms");
    expect(formatValue(999.6, { unit: "ms" })).toBe("1000 ms");
  });
  it("formats dB with one decimal and a real minus sign", () => {
    expect(formatValue(-6, { unit: "dB" })).toBe("−6.0 dB");
    expect(formatValue(0, { unit: "dB" })).toBe("0.0 dB");
    expect(formatValue(3.25, { unit: "dB" })).toBe("3.3 dB");
    expect(formatValue(-47.96, { unit: "dB" })).toBe("−48.0 dB");
  });
  it("formats unitless values as rounded ints", () => {
    expect(formatValue(2, { unit: "" })).toBe("2");
    expect(formatValue(-12.4, { unit: "" })).toBe("-12");
  });
  it("maps hzlog positions onto 20 Hz – 20 kHz", () => {
    expect(formatValue(0, { unit: "", fmt: "hzlog" })).toBe("20 Hz");
    expect(formatValue(100, { unit: "", fmt: "hzlog" })).toBe("20.0 kHz");
    // 20 * 1000^0.5 = 632.45… -> integer Hz below 1 kHz
    expect(formatValue(50, { unit: "", fmt: "hzlog" })).toBe("632 Hz");
    // 20 * 1000^0.75 = 3557.5… -> one-decimal kHz at/above 1 kHz
    expect(formatValue(75, { unit: "", fmt: "hzlog" })).toBe("3.6 kHz");
  });
});

describe("dragValue", () => {
  it("maps 200px of upward travel to the full range at scale 1", () => {
    expect(dragValue(0, 200, 0, 100, false, 1)).toBe(100);
    expect(dragValue(50, 100, 0, 100, false, 1)).toBe(100);
    expect(dragValue(50, -100, 0, 100, false, 1)).toBe(0);
  });
  it("moves proportionally for partial travel", () => {
    expect(dragValue(50, 20, 0, 100, false, 1)).toBeCloseTo(60, 10);
    expect(dragValue(380, -50, 1, 1000, false, 1)).toBeCloseTo(380 - (50 / 200) * 999, 10);
  });
  it("fine mode (Shift) needs 5x the travel", () => {
    expect(dragValue(50, 100, 0, 100, true, 1)).toBeCloseTo(60, 10);
  });
  it("scales the travel with the UI scale", () => {
    expect(dragValue(50, 100, 0, 100, false, 2)).toBeCloseTo(75, 10);
    expect(dragValue(50, 100, 0, 100, false, 0.5)).toBe(100);
  });
  it("clamps at the range ends", () => {
    expect(dragValue(90, 500, 0, 100, false, 1)).toBe(100);
    expect(dragValue(-20, -500, -24, 24, false, 1)).toBe(-24);
  });
});

describe("wheelStep", () => {
  it("steps 1% of the range per notch, wheel-up increases", () => {
    expect(wheelStep(50, -1, 0, 100, false)).toBeCloseTo(51, 10);
    expect(wheelStep(50, 1, 0, 100, false)).toBeCloseTo(49, 10);
  });
  it("steps 0.2% in fine mode", () => {
    expect(wheelStep(50, -1, 0, 100, true)).toBeCloseTo(50.2, 10);
    expect(wheelStep(50, 1, 0, 100, true)).toBeCloseTo(49.8, 10);
  });
  it("uses the range, not absolute units", () => {
    expect(wheelStep(380, -1, 1, 1000, false)).toBeCloseTo(380 + 9.99, 10);
  });
  it("clamps at the range ends", () => {
    expect(wheelStep(99.9, -1, 0, 100, false)).toBe(100);
    expect(wheelStep(0.05, 1, 0, 100, false)).toBe(0);
  });
});

describe("keyStep", () => {
  it("steps 1/100 of the range per press", () => {
    expect(keyStep(50, 1, 0, 100, false)).toBeCloseTo(51, 10);
    expect(keyStep(50, -1, 0, 100, false)).toBeCloseTo(49, 10);
  });
  it("steps 1/500 in fine mode", () => {
    expect(keyStep(50, 1, 0, 100, true)).toBeCloseTo(50.2, 10);
    expect(keyStep(50, -1, 0, 100, true)).toBeCloseTo(49.8, 10);
  });
  it("works on offset and negative ranges", () => {
    expect(keyStep(0.1, 1, 0.1, 8, false)).toBeCloseTo(0.1 + 7.9 / 100, 10);
    expect(keyStep(-6, -1, -48, 0, false)).toBeCloseTo(-6.48, 10);
  });
  it("clamps at the range ends", () => {
    expect(keyStep(99.9, 1, 0, 100, false)).toBe(100);
    expect(keyStep(0.02, -1, 0, 100, false)).toBe(0);
  });
});
