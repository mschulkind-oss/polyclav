import { render } from "@testing-library/react";
import { expect, test } from "vitest";
import { LFO_WAVELEN, LfoSection, lfoTriPath } from "@/components/pedalboard/LfoSection";
import { pathMaxX } from "@/components/pedalboard/vizPath";
import { SYNTH } from "@/lib/pedalboard/model";

test("scroll cycle is 1/rate seconds via --pb-wave-cycle", () => {
  const { container } = render(<LfoSection />);
  const g = container.querySelector<SVGGElement>(".pb-wave-anim");
  const expected = `${(1 / SYNTH.lfo.rateHz.defaultValue).toFixed(4)}s`;
  expect(g?.style.getPropertyValue("--pb-wave-cycle")).toBe(expected);
});

test("seamless loop: clipped window, one-period overdraw, translate = drawn wavelength", () => {
  const { container } = render(<LfoSection />);
  const svg = container.querySelector(".pb-scope-viz svg");
  // the overdraw must be clipped — `:where(.pb-root) svg` leaves overflow visible
  expect(svg?.classList.contains("pb-scroll-clip")).toBe(true);
  const viewW = Number(svg?.getAttribute("viewBox")?.split(" ")[2]);
  const g = container.querySelector<SVGGElement>(".pb-wave-anim");
  // the keyframe translates by --pb-scroll-period; it must equal the drawn wavelength
  expect(g?.style.getPropertyValue("--pb-scroll-period")).toBe(`${LFO_WAVELEN}px`);
  const d = g?.querySelector("path")?.getAttribute("d") ?? "";
  expect(pathMaxX(d)).toBeGreaterThanOrEqual(viewW + LFO_WAVELEN);
});

test("triangle path overdraws one wavelength at any depth", () => {
  for (const depth of [0, 37, 100]) {
    expect(pathMaxX(lfoTriPath(depth))).toBeGreaterThanOrEqual(118 + LFO_WAVELEN);
  }
});
