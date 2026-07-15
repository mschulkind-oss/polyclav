import { render } from "@testing-library/react";
import { expect, test } from "vitest";
import { DriveCurveViz } from "@/components/pedalboard/DriveCurveViz";

function fillOpacity(amount: number): number {
  const { container } = render(<DriveCurveViz amount={amount} />);
  const fill = container.querySelector<SVGPathElement>(".pb-fillpath");
  expect(fill).not.toBeNull();
  return Number(fill?.style.opacity);
}

test("heat fill opacity is 0.06 + amount/100 * 0.5", () => {
  expect(fillOpacity(15)).toBeCloseTo(0.135, 3);
  expect(fillOpacity(0)).toBeCloseTo(0.06, 3);
  expect(fillOpacity(100)).toBeCloseTo(0.56, 3);
});

test("fill opacity grows with amount", () => {
  expect(fillOpacity(80)).toBeGreaterThan(fillOpacity(15));
});

test("renders the tanh transfer curve over the axis", () => {
  const { container } = render(<DriveCurveViz amount={15} />);
  expect(container.querySelector(".pb-axis")).not.toBeNull();
  const curve = container.querySelector<SVGPathElement>("path:not(.pb-fillpath)");
  const d = curve?.getAttribute("d") ?? "";
  // x=0 → u=−1 → 13 + 9 = 22; x=118 → u=1 → 13 − 9 = 4; midpoint on the axis.
  expect(d.startsWith("M0 22.00")).toBe(true);
  expect(d.endsWith("L118 4.00")).toBe(true);
  expect(d).toContain("L58 13.");
  // the heat fill closes the same curve down to the axis
  const fill = container.querySelector(".pb-fillpath")?.getAttribute("d") ?? "";
  expect(fill).toBe(`${d} L118 13 L0 13 Z`);
});
