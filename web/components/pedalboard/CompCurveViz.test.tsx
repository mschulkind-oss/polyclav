import { render } from "@testing-library/react";
import { expect, test } from "vitest";
import { CompCurveViz } from "@/components/pedalboard/CompCurveViz";

function parts(amount: number) {
  const { container } = render(<CompCurveViz amount={amount} />);
  const fill = container.querySelector<SVGPathElement>(".pb-fillpath");
  const curve = container.querySelector<SVGPathElement>("path:not(.pb-fillpath)");
  const axis = container.querySelector(".pb-axis");
  return { fill, curve, axis };
}

test("heat fill opacity is 0.06 + amount/100 * 0.5", () => {
  expect(Number(parts(0).fill?.style.opacity)).toBeCloseTo(0.06, 3);
  expect(Number(parts(35).fill?.style.opacity)).toBeCloseTo(0.235, 3);
  expect(Number(parts(100).fill?.style.opacity)).toBeCloseTo(0.56, 3);
});

test("draws a unity reference diagonal", () => {
  expect(parts(50).axis).not.toBeNull();
});

test("at Comp 0 the transfer curve collapses onto unity (bypass)", () => {
  const d = parts(0).curve?.getAttribute("d") ?? "";
  // input −48 → out −48 → (x4, y22); input 0 → out 0 → (x114, y6): the 1:1 line.
  expect(d.startsWith("M4 22.00")).toBe(true);
  expect(d.endsWith("L114 6.00")).toBe(true);
});

test("turning Comp up lifts the curve above unity (makeup + compression)", () => {
  // At Comp 100, the quietest input is lifted by makeup: out(−48) > −48, so its
  // y sits above (smaller than) unity's y=22 at x=4.
  const d = parts(100).curve?.getAttribute("d") ?? "";
  const firstY = Number(/^M4 (\d+\.\d+)/.exec(d)?.[1]);
  expect(firstY).toBeLessThan(22);
});
