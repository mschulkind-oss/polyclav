import { fireEvent, render, screen, within } from "@testing-library/react";
import { expect, test, vi } from "vitest";
import { Pedalboard } from "@/components/pedalboard/Pedalboard";

test("composes the rail: source → 4 wired strips → bus, plus hint and legend", () => {
  const { container } = render(<Pedalboard />);
  const rail = container.querySelector(".pb-railwrap .pb-rail");
  expect(rail).not.toBeNull();
  expect(rail?.querySelector(".pb-srcnode")).not.toBeNull();
  expect(rail?.querySelectorAll(".pb-strip")).toHaveLength(4);
  expect(rail?.querySelectorAll(".pb-wire")).toHaveLength(5);
  expect(rail?.querySelector(".pb-bus")).not.toBeNull();
  for (const name of ["Drive", "Chorus", "Trem", "Delay"]) {
    expect(screen.getByRole("button", { name: `Open ${name} in editor` })).toBeInTheDocument();
  }
  expect(container.querySelector(".pb-rail-hint")?.textContent).toContain(
    "Click a pedal to open it in the editor",
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
