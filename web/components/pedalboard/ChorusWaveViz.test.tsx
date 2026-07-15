import { render } from "@testing-library/react";
import { expect, test } from "vitest";
import { CHORUS_WAVELEN, ChorusWaveViz } from "@/components/pedalboard/ChorusWaveViz";
import { pathMaxX } from "@/components/pedalboard/vizPath";

test("scroll cycle is 1/rate seconds via --pb-wave-cycle", () => {
  const { container } = render(<ChorusWaveViz rateHz={0.9} />);
  const g = container.querySelector<SVGGElement>(".pb-wave-anim");
  expect(g).not.toBeNull();
  expect(g?.style.getPropertyValue("--pb-wave-cycle")).toBe(`${(1 / 0.9).toFixed(4)}s`);
});

test("cycle tracks the rate", () => {
  const { container } = render(<ChorusWaveViz rateHz={4} />);
  const g = container.querySelector<SVGGElement>(".pb-wave-anim");
  expect(g?.style.getPropertyValue("--pb-wave-cycle")).toBe("0.2500s");
});

test("stereo pair: two sines a quarter wavelength (90°) apart", () => {
  const { container } = render(<ChorusWaveViz rateHz={0.9} />);
  const partner = container.querySelector<SVGPathElement>(".pb-wave-b");
  const main = container.querySelector<SVGPathElement>(".pb-wave-anim path:not(.pb-wave-b)");
  const mainD = main?.getAttribute("d") ?? "";
  const partnerD = partner?.getAttribute("d") ?? "";
  // main starts on the midline; the −6.5 (26/4) shifted partner starts at the crest
  expect(mainD.startsWith("M0 13.00")).toBe(true);
  expect(partnerD.startsWith("M0 18.40")).toBe(true);
  // one extra 26-unit wavelength so the one-wavelength scroll loops seamlessly:
  // x=26 repeats x=0, and the final sample x=144 repeats x=118
  expect(mainD).toContain("L26 13.00");
  expect(mainD).toContain("L118 14.29");
  expect(mainD.endsWith("L144 14.29")).toBe(true);
});

test("seamless loop: clipped window, one-period overdraw, translate = drawn wavelength", () => {
  const { container } = render(<ChorusWaveViz rateHz={0.9} />);
  const svg = container.querySelector("svg");
  // the overdraw must be clipped — `:where(.pb-root) svg` leaves overflow visible
  expect(svg?.classList.contains("pb-scroll-clip")).toBe(true);
  const viewW = Number(svg?.getAttribute("viewBox")?.split(" ")[2]);
  const g = container.querySelector<SVGGElement>(".pb-wave-anim");
  // the keyframe translates by --pb-scroll-period; it must equal the drawn wavelength
  expect(g?.style.getPropertyValue("--pb-scroll-period")).toBe(`${CHORUS_WAVELEN}px`);
  const paths = Array.from(g?.querySelectorAll("path") ?? []);
  expect(paths).toHaveLength(2);
  for (const p of paths) {
    expect(pathMaxX(p.getAttribute("d") ?? "")).toBeGreaterThanOrEqual(viewW + CHORUS_WAVELEN);
  }
});
