import { render } from "@testing-library/react";
import { expect, test } from "vitest";
import {
  MasteringCurveViz,
  type MasteringCurveVizProps,
} from "@/components/pedalboard/MasteringCurveViz";

/** A settled mastering state: 8:1, 30 dB range, 2 dB headroom, −6 dB ceiling. */
const BASE: MasteringCurveVizProps = {
  glue: 0,
  ratio: 8,
  rangeDb: 30,
  headroomDb: 2,
  ceilingDb: -6,
};

function parts(over: Partial<MasteringCurveVizProps> = {}) {
  const { container } = render(<MasteringCurveViz {...BASE} {...over} />);
  const fill = container.querySelector<SVGPathElement>(".pb-fillpath");
  // The law is the only bare path (the limiter carries .pb-limit).
  const curve = container.querySelector<SVGPathElement>("path:not(.pb-fillpath)");
  const axis = container.querySelector(".pb-axis");
  const limit = container.querySelector<SVGPathElement>(".pb-limit");
  const op = container.querySelector(".pb-op-point");
  const gr = container.querySelector<SVGRectElement>(".pb-gr");
  return { container, fill, curve, axis, limit, op, gr };
}

test("heat fill opacity is 0.06 + glue/100 * 0.5", () => {
  expect(Number(parts({ glue: 0 }).fill?.style.opacity)).toBeCloseTo(0.06, 3);
  expect(Number(parts({ glue: 35 }).fill?.style.opacity)).toBeCloseTo(0.235, 3);
  expect(Number(parts({ glue: 100 }).fill?.style.opacity)).toBeCloseTo(0.56, 3);
});

test("draws the unity reference diagonal corner to corner", () => {
  const axis = parts().axis;
  expect(axis).not.toBeNull();
  expect(axis?.getAttribute("x1")).toBe("4");
  expect(axis?.getAttribute("y1")).toBe("22");
  expect(axis?.getAttribute("x2")).toBe("114");
  expect(axis?.getAttribute("y2")).toBe("6");
});

test("at Glue 0 the curve is unity up to the ceiling then flattens", () => {
  const d = parts({ glue: 0 }).curve?.getAttribute("d") ?? "";
  // Input −48 → out −48 → (x4, y22): the 1:1 line starts on the diagonal.
  expect(d.startsWith("M4 22.00")).toBe(true);
  // At the ceiling (−6) it is still exactly unity: (x100, y8).
  expect(d).toContain("L100 8.00");
  // Past the ceiling the limiter holds the output flat on the brick wall, so
  // the endpoint lands exactly on the ceiling's y (8.00) at x114.
  expect(d.endsWith("L114 8.00")).toBe(true);
  // No sampled point rides above the ceiling: every y sits on or below the
  // ceiling's y (a smaller y would be a louder output past the wall).
  const ys = [...d.matchAll(/L\d+ (\d+\.\d+)/g)].map((m) => Number(m[1]));
  expect(ys.length).toBeGreaterThan(0);
  for (const y of ys) expect(y).toBeGreaterThanOrEqual(8);
});

test("raising Glue lowers the threshold and lifts the quiet end", () => {
  const dry = parts({ glue: 0 }).curve?.getAttribute("d") ?? "";
  const wet = parts({ glue: 100 }).curve?.getAttribute("d") ?? "";
  const firstY = (d: string) => Number(/^M4 (\d+\.\d+)/.exec(d)?.[1]);
  // Unity leaves −48 dB untouched (y22); Glue 100's makeup lifts it (smaller y).
  expect(firstY(dry)).toBeCloseTo(22, 2);
  expect(firstY(wet)).toBeLessThan(22);
});

test("ratio changes the compressed slope", () => {
  const soft = parts({ glue: 100, ratio: 2 }).curve?.getAttribute("d") ?? "";
  const hard = parts({ glue: 100, ratio: 8 }).curve?.getAttribute("d") ?? "";
  expect(soft).not.toBe(hard);
  // 2:1 compresses least, so its top end reaches past the ceiling and clamps
  // onto the wall; 8:1's reduction keeps it below the ceiling under this makeup.
  expect(soft.endsWith("L114 8.00")).toBe(true);
  expect(hard.endsWith("L114 8.42")).toBe(true);
});

test("rangeDb changes where compression starts inside the window", () => {
  const narrow = parts({ glue: 100, rangeDb: 12 }).curve?.getAttribute("d") ?? "";
  const wide = parts({ glue: 100, rangeDb: 48 }).curve?.getAttribute("d") ?? "";
  expect(narrow).not.toBe(wide);
});

test("ceilingDb shifts the limiter brick wall (path d endpoints)", () => {
  const low = parts({ glue: 100, ceilingDb: -12 }).limit?.getAttribute("d");
  const high = parts({ glue: 100, ceilingDb: 0 }).limit?.getAttribute("d");
  expect(low).toBe("M86.50 10.00 L114 10.00");
  expect(high).toBe("M114.00 6.00 L114 6.00");
});

test("ceilingDb shifts the knee origin of the curve", () => {
  const low = parts({ glue: 100, ceilingDb: -12 }).curve?.getAttribute("d");
  const high = parts({ glue: 100, ceilingDb: 0 }).curve?.getAttribute("d");
  expect(low).not.toBe(high);
});

test("the operating point appears only with both live levels", () => {
  expect(parts({ inDb: -24 }).op).toBeNull();
  expect(parts({ outDb: -18 }).op).toBeNull();
  expect(parts().op).toBeNull();
  const op = parts({ inDb: -24, outDb: -18 }).op;
  expect(op?.getAttribute("cx")).toBe("59.00");
  expect(op?.getAttribute("cy")).toBe("12.00");
});

test("the gain meter fill grows with gainDb", () => {
  const small = Number(parts({ gainDb: 2 }).gr?.getAttribute("width"));
  const big = Number(parts({ gainDb: 8 }).gr?.getAttribute("width"));
  expect(big).toBeGreaterThan(small);
  // A reduction widens the meter by the same magnitude (a GR meter).
  expect(Number(parts({ gainDb: -8 }).gr?.getAttribute("width"))).toBeCloseTo(big, 3);
  expect(Number(parts().gr?.getAttribute("width"))).toBe(0);
});
