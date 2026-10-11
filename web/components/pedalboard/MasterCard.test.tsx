import { fireEvent, render, screen } from "@testing-library/react";
import { expect, test, vi } from "vitest";
import { MasterCard } from "@/components/pedalboard/MasterCard";

test("separates the two volume knobs from the limiter ceiling", () => {
  const { container } = render(<MasterCard />);
  const r1 = container.querySelector(".pb-bus-pair.pb-r1");
  const r2 = container.querySelector(".pb-bus-pair.pb-r2");
  expect(r1?.textContent).toContain("Patch");
  expect(r1?.textContent).toContain("Master");
  expect(r1?.textContent).not.toContain("Ceiling");
  expect(r2?.textContent).toContain("Ceiling");
  expect(container.querySelectorAll(".pb-bus-pair .pb-param")).toHaveLength(3);
});

test("readouts use the canon formatting, including the U+2212 minus", () => {
  const { container } = render(<MasterCard />);
  const vals = Array.from(container.querySelectorAll(".pb-p-val")).map((v) => v.textContent);
  expect(vals).toEqual(["100%", "80%", "−0.3 dB"]);
});

test("meters live in the header; footer says Post · stereo out", () => {
  const { container } = render(<MasterCard />);
  const meters = container.querySelector(".pb-bus-top .pb-meters");
  expect(meters?.querySelector(".pb-mL")).not.toBeNull();
  expect(meters?.querySelector(".pb-mR")).not.toBeNull();
  expect(screen.getByRole("heading", { name: "Master" })).toBeInTheDocument();
  expect(screen.getByText("Post · stereo out")).toHaveClass("pb-bus-sub");
});

test("value overrides replace the spec defaults", () => {
  const { container } = render(<MasterCard values={{ "patch.level": 60, "master.ceiling": -3 }} />);
  const vals = Array.from(container.querySelectorAll(".pb-p-val")).map((v) => v.textContent);
  expect(vals).toEqual(["60%", "80%", "−3.0 dB"]);
});

test("minis stay display-only without onParamChange", () => {
  render(<MasterCard />);
  expect(screen.queryAllByRole("slider")).toHaveLength(0);
});

test("onParamChange makes every mini adjustable and reports edits by id", () => {
  const onParamChange = vi.fn();
  render(<MasterCard onParamChange={onParamChange} />);
  const sliders = screen.getAllByRole("slider");
  expect(sliders.map((s) => s.getAttribute("aria-label"))).toEqual(["Patch", "Master", "Ceiling"]);
  fireEvent.keyDown(screen.getByRole("slider", { name: "Patch" }), { key: "ArrowDown" });
  expect(onParamChange).toHaveBeenLastCalledWith("patch.level", 98); // 100 - 1/100 of 0–200
  fireEvent.keyDown(screen.getByRole("slider", { name: "Master" }), { key: "ArrowUp" });
  expect(onParamChange).toHaveBeenLastCalledWith("master.level", 84); // 80 + 1/100 of 0–400
});
