import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { FilterCurveViz, filterCurvePath } from "@/components/pedalboard/FilterCurveViz";

/** Smallest y in the path = the curve's highest point (SVG y grows down). */
function minY(d: string): number {
  const ys = d
    .replace(/^M/, "")
    .split(" L")
    .map((pair) => Number(pair.trim().split(" ")[1]));
  return Math.min(...ys);
}

function curveEl(container: HTMLElement): SVGPathElement {
  const paths = Array.from(container.querySelectorAll("path"));
  const curve = paths.find((p) => !p.classList.contains("pb-fillpath"));
  if (!curve) throw new Error("curve path not found");
  return curve;
}

describe("filterCurvePath", () => {
  it("moves the rolloff with cutoffPos", () => {
    expect(filterCurvePath(30, 20)).not.toBe(filterCurvePath(70, 20));
  });

  it("grows a resonance bump above the passband plateau", () => {
    const low = minY(filterCurvePath(62, 10));
    const high = minY(filterCurvePath(62, 90));
    expect(high).toBeLessThan(low);
  });
});

describe("FilterCurveViz", () => {
  it("re-renders curve and fill when cutoffPos changes", () => {
    const { container, rerender } = render(<FilterCurveViz cutoffPos={30} resonance={20} />);
    const curveBefore = curveEl(container).getAttribute("d");
    const fillBefore = container.querySelector(".pb-fillpath")?.getAttribute("d");
    rerender(<FilterCurveViz cutoffPos={70} resonance={20} />);
    expect(curveEl(container).getAttribute("d")).not.toBe(curveBefore);
    expect(container.querySelector(".pb-fillpath")?.getAttribute("d")).not.toBe(fillBefore);
  });

  it("re-renders when resonance rises, closing the soft fill to the baseline", () => {
    const { container, rerender } = render(<FilterCurveViz cutoffPos={62} resonance={10} />);
    const before = curveEl(container).getAttribute("d") ?? "";
    rerender(<FilterCurveViz cutoffPos={62} resonance={90} />);
    const after = curveEl(container).getAttribute("d") ?? "";
    expect(after).not.toBe(before);
    expect(minY(after)).toBeLessThan(minY(before));
    expect(container.querySelector(".pb-fillpath")?.getAttribute("d")).toMatch(/Z$/);
  });
});
