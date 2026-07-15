import { fireEvent, render, screen } from "@testing-library/react";
import { expect, test, vi } from "vitest";
import { BusCard } from "@/components/pedalboard/BusCard";

test("bus packs its four params as pairs into rows 2–3 of the shared grid", () => {
  const { container } = render(<BusCard />);
  const r1 = container.querySelector(".pb-bus-pair.pb-r1");
  expect(r1?.textContent).toContain("Gain");
  expect(r1?.textContent).toContain("Comp");
  const r2 = container.querySelector(".pb-bus-pair.pb-r2");
  expect(r2?.textContent).toContain("Reverb");
  expect(r2?.textContent).toContain("Master");
  expect(container.querySelectorAll(".pb-bus-pair .pb-param")).toHaveLength(4);
});

test("readouts use the canon formatting, including the U+2212 minus", () => {
  const { container } = render(<BusCard />);
  const vals = Array.from(container.querySelectorAll(".pb-p-val")).map((v) => v.textContent);
  expect(vals).toEqual(["0.0 dB", "35%", "18%", "−6.0 dB"]);
});

test("meters live in the header; footer says Post · stereo out", () => {
  const { container } = render(<BusCard />);
  const meters = container.querySelector(".pb-bus-top .pb-meters");
  expect(meters?.querySelector(".pb-mL")).not.toBeNull();
  expect(meters?.querySelector(".pb-mR")).not.toBeNull();
  expect(screen.getByRole("heading", { name: "Bus" })).toBeInTheDocument();
  expect(screen.getByText("Post · stereo out")).toHaveClass("pb-bus-sub");
});

test("gain's bipolar arc grows from the sweep center (0 dB → zero-length at 135°)", () => {
  const { container } = render(<BusCard />);
  const gainArc = container.querySelector(".pb-bus-pair.pb-r1 .pb-param .pb-mini .pb-k-arc");
  expect(gainArc?.getAttribute("stroke-dasharray")).toBe("0.01 360");
  expect(gainArc?.getAttribute("stroke-dashoffset")).toBe("-135");
});

test("value overrides replace the spec defaults", () => {
  const { container } = render(<BusCard values={{ "bus.master": -12 }} />);
  const vals = Array.from(container.querySelectorAll(".pb-p-val")).map((v) => v.textContent);
  expect(vals[3]).toBe("−12.0 dB");
  expect(vals[0]).toBe("0.0 dB");
});

test("minis stay display-only without onParamChange", () => {
  render(<BusCard />);
  expect(screen.queryAllByRole("slider")).toHaveLength(0);
});

test("onParamChange makes all four bus minis adjustable and reports edits by id", () => {
  const onParamChange = vi.fn();
  render(<BusCard onParamChange={onParamChange} />);
  const sliders = screen.getAllByRole("slider");
  expect(sliders.map((s) => s.getAttribute("aria-label"))).toEqual([
    "Gain",
    "Comp",
    "Reverb",
    "Master",
  ]);
  fireEvent.keyDown(screen.getByRole("slider", { name: "Comp" }), { key: "ArrowUp" });
  expect(onParamChange).toHaveBeenLastCalledWith("bus.comp", 36); // 35 + 1/100 of 0–100
  fireEvent.wheel(screen.getByRole("slider", { name: "Gain" }), { deltaY: -100 });
  expect(onParamChange).toHaveBeenLastCalledWith("bus.gain", 0.48); // 0 + 1% of ±24 dB
});

test("bus knob edits render back through controlled values", () => {
  const { container, rerender } = render(<BusCard values={{ "bus.comp": 35 }} />);
  rerender(<BusCard values={{ "bus.comp": 62 }} />);
  const vals = Array.from(container.querySelectorAll(".pb-p-val")).map((v) => v.textContent);
  expect(vals[1]).toBe("62%");
});
