import { fireEvent, render, screen, within } from "@testing-library/react";
import { expect, test, vi } from "vitest";
import { Pedalboard } from "@/components/pedalboard/Pedalboard";

test("composes the rail: source → 6 wired strips → master, plus reorder bar, hint and legend", () => {
  const { container } = render(<Pedalboard />);
  const rail = container.querySelector(".pb-railwrap .pb-rail");
  expect(rail).not.toBeNull();
  expect(rail?.querySelector(".pb-srcnode")).not.toBeNull();
  expect(rail?.querySelectorAll(".pb-strip")).toHaveLength(6);
  expect(rail?.querySelectorAll(".pb-wire")).toHaveLength(7);
  expect(rail?.querySelector(".pb-bus")).not.toBeNull(); // the master-out card
  for (const name of ["Drive", "Chorus", "Trem", "Delay", "Comp", "Reverb"]) {
    expect(screen.getByRole("button", { name: `Open ${name} in editor` })).toBeInTheDocument();
  }
  expect(container.querySelector(".pb-reorder")).not.toBeNull();
  expect(container.querySelector(".pb-rail-hint")?.textContent).toContain(
    "Drag the chips above to reorder",
  );
  expect(container.querySelector(".pb-legend")).not.toBeNull();
});

test("opening state matches the reference: trem bypassed, delay parked", () => {
  render(<Pedalboard />);
  const strips = ["Drive", "Chorus", "Trem", "Delay"].map((n) =>
    screen.getByRole("button", { name: `Open ${n} in editor` }),
  );
  expect(strips[0]).not.toHaveClass("pb-bypassed");
  expect(strips[1]).not.toHaveClass("pb-bypassed");
  expect(strips[2]).toHaveClass("pb-bypassed");
  expect(strips[3]).toHaveClass("pb-bypassed");
  expect(screen.getByRole("button", { name: "Parked" })).toBeInTheDocument();
});

test("each pedal carries its signature module, driven by the mock values", () => {
  const { container } = render(<Pedalboard />);
  const strips = Array.from(container.querySelectorAll(".pb-strip"));
  expect(strips[0]?.querySelector(".pb-viz .pb-fillpath")).not.toBeNull(); // drive heat curve
  const wave = strips[1]?.querySelector<SVGGElement>(".pb-viz .pb-wave-anim");
  expect(wave?.style.getPropertyValue("--pb-wave-cycle")).toBe("1.1111s"); // 1 / 0.9 Hz
  const opto = strips[2]?.querySelector(".pb-viz svg");
  expect(opto?.querySelector(".pb-opto")).not.toBeNull();
  expect(opto).toHaveClass("pb-depth-zero"); // trem depth mock is 0 → lamp frozen
  expect(strips[3]?.querySelectorAll(".pb-viz .pb-strip-tail circle")).toHaveLength(3); // 35% fb
});

test("stomps toggle bypass; the delay tail starts pinging once engaged", () => {
  const { container } = render(<Pedalboard />);
  const delayStrip = screen.getByRole("button", { name: "Open Delay in editor" });
  const parkedDot = delayStrip.querySelector(".pb-strip-tail circle");
  expect((parkedDot as SVGCircleElement).style.animationDuration).toBe("");
  fireEvent.click(screen.getByRole("button", { name: "Parked" }));
  expect(delayStrip).not.toHaveClass("pb-bypassed");
  const liveDot = delayStrip.querySelector(".pb-strip-tail circle");
  expect((liveDot as SVGCircleElement).style.animationDuration).toBe("1900ms"); // 380 ms × 5
  expect((liveDot as SVGCircleElement).style.animationDelay).toBe("380ms");
  // stomping again re-parks it (drive and chorus stomps also read "On" — scope to the strip)
  fireEvent.click(within(delayStrip).getByRole("button", { name: "On" }));
  expect(delayStrip).toHaveClass("pb-bypassed");
  expect(container.querySelectorAll(".pb-strip.pb-bypassed")).toHaveLength(2);
});

test("controlled mode: values and bypass come from props, stomps report up", () => {
  const onToggle = vi.fn();
  render(
    <Pedalboard
      values={{ "delay.time_ms": 500 }}
      enabled={{ drive: false, chorus: true, trem: true, delay: true }}
      onToggle={onToggle}
    />,
  );
  const delayStrip = screen.getByRole("button", { name: "Open Delay in editor" });
  expect(delayStrip).not.toHaveClass("pb-bypassed");
  expect(delayStrip.querySelector(".pb-r-time .pb-p-val")?.textContent).toBe("500 ms");
  expect(screen.getByRole("button", { name: "Open Drive in editor" })).toHaveClass("pb-bypassed");
  fireEvent.click(within(delayStrip).getByRole("button", { name: "On" }));
  expect(onToggle).toHaveBeenCalledWith("delay");
  // controlled: the strip only flips when the owner changes the prop
  expect(delayStrip).not.toHaveClass("pb-bypassed");
});

test("standalone board knobs are live: chorus Rate retimes the wave viz", () => {
  const { container } = render(<Pedalboard />);
  const chorusStrip = screen.getByRole("button", { name: "Open Chorus in editor" });
  const rate = within(chorusStrip).getByRole("slider", { name: "Rate" });
  fireEvent.keyDown(rate, { key: "ArrowUp" }); // 0.9 + (5 − 0.05)/100 = 0.9495 Hz
  expect(chorusStrip.querySelector(".pb-r-time .pb-p-val")?.textContent).toBe("0.9 Hz");
  const wave = container.querySelector<SVGGElement>(".pb-viz .pb-wave-anim");
  expect(wave?.style.getPropertyValue("--pb-wave-cycle")).toBe("1.0532s"); // 1 / 0.9495 Hz
});

test("raising trem Depth above 0 un-freezes the opto lamp once engaged", () => {
  render(<Pedalboard />);
  const tremStrip = screen.getByRole("button", { name: "Open Trem in editor" });
  fireEvent.click(within(tremStrip).getByRole("button", { name: "Bypassed" })); // engage
  const opto = () => tremStrip.querySelector(".pb-viz svg");
  expect(opto()).toHaveClass("pb-depth-zero"); // engaged but depth still 0
  fireEvent.keyDown(within(tremStrip).getByRole("slider", { name: "Depth" }), { key: "ArrowUp" });
  expect(tremStrip.querySelector(".pb-r-shape .pb-p-val")?.textContent).toBe("1%");
  expect(opto()).not.toHaveClass("pb-depth-zero");
});

test("master knobs are live too and share the board's value state", () => {
  const { container } = render(<Pedalboard />);
  fireEvent.keyDown(screen.getByRole("slider", { name: "Master" }), { key: "ArrowUp" });
  const level = container.querySelector(".pb-bus-pair.pb-r1 .pb-param[data-role='level']");
  expect(level?.querySelector(".pb-p-val")?.textContent).toBe("81%"); // 80 + 1/100 of 0–100
});

test("controlled mode: board knob edits report up through onParamChange", () => {
  const onParamChange = vi.fn();
  const onOpenPedal = vi.fn();
  render(<Pedalboard values={{}} onParamChange={onParamChange} onOpenPedal={onOpenPedal} />);
  const delayStrip = screen.getByRole("button", { name: "Open Delay in editor" });
  const time = within(delayStrip).getByRole("slider", { name: "Time" });
  fireEvent.pointerDown(time, { clientY: 300, pointerId: 1 });
  fireEvent.pointerMove(time, { clientY: 300 - 200 * (44 / 120) }); // full sweep of the 44 mini
  fireEvent.pointerUp(time);
  fireEvent.click(time); // the click a completed drag synthesizes
  expect(onParamChange).toHaveBeenLastCalledWith("delay.time_ms", 1000);
  fireEvent.wheel(within(delayStrip).getByRole("slider", { name: "Feedback" }), { deltaY: -100 });
  expect(onParamChange.mock.lastCall?.[0]).toBe("delay.feedback");
  expect(onParamChange.mock.lastCall?.[1]).toBeCloseTo(35.9, 6); // 35 + 1% of 0–90
  // controlled: the display only moves via the values prop, never internally
  expect(delayStrip.querySelector(".pb-r-time .pb-p-val")?.textContent).toBe("380 ms");
  // and none of that knob traffic opened the editor
  expect(onOpenPedal).not.toHaveBeenCalled();
  fireEvent.click(delayStrip);
  expect(onOpenPedal).toHaveBeenCalledWith("delay");
});

test("clicking a strip reports the pedal id to open", () => {
  const onOpenPedal = vi.fn();
  render(<Pedalboard onOpenPedal={onOpenPedal} />);
  fireEvent.click(screen.getByRole("button", { name: "Open Chorus in editor" }));
  expect(onOpenPedal).toHaveBeenCalledWith("chorus");
  fireEvent.keyDown(screen.getByRole("button", { name: "Open Drive in editor" }), {
    key: "Enter",
  });
  expect(onOpenPedal).toHaveBeenCalledWith("drive");
});
