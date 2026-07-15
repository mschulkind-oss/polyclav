import { render } from "@testing-library/react";
import { expect, test } from "vitest";
import { TremOptoViz } from "@/components/pedalboard/TremOptoViz";

test("lamp pulses at the true rate via --pb-trem-cycle", () => {
  const { container } = render(<TremOptoViz rateHz={5.2} active />);
  const svg = container.querySelector("svg");
  expect(svg?.style.getPropertyValue("--pb-trem-cycle")).toBe("192ms");
  expect(container.querySelector(".pb-opto")).not.toBeNull();
});

test("inactive freezes the lamp: pb-depth-zero class only when !active", () => {
  const inactive = render(<TremOptoViz rateHz={5.2} active={false} />);
  expect(inactive.container.querySelector("svg")).toHaveClass("pb-depth-zero");
  const active = render(<TremOptoViz rateHz={5.2} active />);
  expect(active.container.querySelector("svg")).not.toHaveClass("pb-depth-zero");
});

test("draws the ghost wave remapped to x 24..114 and the flat live line", () => {
  const { container } = render(<TremOptoViz rateHz={5.2} active={false} />);
  const ghost = container.querySelector(".pb-trem-ghost")?.getAttribute("d") ?? "";
  expect(ghost.startsWith("M24 13.00")).toBe(true);
  expect(ghost.endsWith("L114.0 15.64")).toBe(true); // x=88 → 13 − 6.5·sin(2π·88/30)
  const line = container.querySelector("line");
  expect(line?.getAttribute("x1")).toBe("24");
  expect(line?.getAttribute("x2")).toBe("114");
});
