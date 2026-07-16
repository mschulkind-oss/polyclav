import { render } from "@testing-library/react";
import { expect, test } from "vitest";
import { ReverbBloomViz } from "@/components/pedalboard/ReverbBloomViz";

function bloom(decay: number, tone: number, mix: number) {
  const { container } = render(<ReverbBloomViz decay={decay} tone={tone} mix={mix} />);
  const fill = container.querySelector<SVGPathElement>(".pb-fillpath");
  const stroke = container.querySelector<SVGPathElement>("path:not(.pb-fillpath)");
  const line = container.querySelector("line");
  return { fill, stroke, line, container };
}

/** Minimum y over the top edge = the bloom's peak (smaller y is taller). */
function peakY(d: string): number {
  let min = Number.POSITIVE_INFINITY;
  for (const m of d.matchAll(/[ML]\d+ (\d+\.\d+)/g)) min = Math.min(min, Number(m[1]));
  return min;
}
/** y at a specific x sample in an "M x y L x y …" path. */
function yAt(d: string, x: number): number {
  return Number(new RegExp(`[ML]${x} (\\d+\\.\\d+)`).exec(d)?.[1]);
}

test("fill opacity is 0.10 + mix/100 * 0.55", () => {
  expect(Number(bloom(2400, 50, 0).fill?.style.opacity)).toBeCloseTo(0.1, 3);
  expect(Number(bloom(2400, 50, 26).fill?.style.opacity)).toBeCloseTo(0.243, 3);
  expect(Number(bloom(2400, 50, 100).fill?.style.opacity)).toBeCloseTo(0.65, 3);
});

test("renders a dry-hit marker, a fill, and a stroked envelope", () => {
  const { fill, stroke, line } = bloom(2400, 50, 26);
  expect(fill).not.toBeNull();
  expect(stroke).not.toBeNull();
  expect(line).not.toBeNull();
});

test("brighter Tone makes a taller bloom", () => {
  const dark = peakY(bloom(2400, 0, 50).stroke?.getAttribute("d") ?? "");
  const bright = peakY(bloom(2400, 100, 50).stroke?.getAttribute("d") ?? "");
  expect(bright).toBeLessThan(dark); // taller = smaller peak y
});

test("longer Decay makes a wider bloom (still lifted far from the onset)", () => {
  const shortD = yAt(bloom(2400, 50, 50).stroke?.getAttribute("d") ?? "", 90);
  const longD = yAt(bloom(8000, 50, 50).stroke?.getAttribute("d") ?? "", 90);
  expect(longD).toBeLessThan(shortD); // still blooming (higher) at x=90
});
