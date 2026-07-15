import { fireEvent, render, screen, within } from "@testing-library/react";
import { expect, test, vi } from "vitest";
import { PedalStrip, type PedalStripProps } from "@/components/pedalboard/PedalStrip";
import { CHAIN, type PedalSpec } from "@/lib/pedalboard/model";

function pedal(id: string): PedalSpec {
  const p = CHAIN.find((x) => x.id === id);
  if (!p) throw new Error(`no pedal ${id} in CHAIN`);
  return p;
}

function defaults(p: PedalSpec): Record<string, number> {
  return Object.fromEntries(p.params.map((x) => [x.id, x.defaultValue]));
}

function renderStrip(id: string, over: Partial<PedalStripProps> = {}) {
  const p = pedal(id);
  return render(
    <PedalStrip
      pedal={p}
      values={defaults(p)}
      enabled
      onStomp={() => {}}
      onOpen={() => {}}
      {...over}
    />,
  );
}

test("every param sits in its role's grid row (alignment contract)", () => {
  const { container } = renderStrip("chorus");
  const time = container.querySelector(".pb-param.pb-r-time");
  expect(time?.textContent).toContain("Rate");
  expect(time?.textContent).toContain("0.9 Hz");
  const shape = container.querySelector(".pb-param.pb-r-shape");
  expect(shape?.textContent).toContain("Depth");
  expect(shape?.textContent).toContain("45%");
  const blend = container.querySelector(".pb-param.pb-r-blend");
  expect(blend?.textContent).toContain("Mix");
  expect(blend?.textContent).toContain("30%");
  // full pedal → no dashed placeholder rings
  expect(container.querySelectorAll(".pb-slot-empty")).toHaveLength(0);
  // labels carry the role tooltip from the glyph canon
  expect(time?.querySelector(".pb-p-name")).toHaveAttribute("title", "Time / rate");
});

test("missing roles render deliberately-empty slots pinned to their rows", () => {
  // drive has only a blend-role param → rows 2 (time) and 3 (shape) stay ringed
  const drive = renderStrip("drive");
  const empties = Array.from(drive.container.querySelectorAll<HTMLElement>(".pb-slot-empty"));
  expect(empties).toHaveLength(2);
  expect(empties.map((e) => e.style.getPropertyValue("--pb-row"))).toEqual(["2", "3"]);
  expect(empties.every((e) => e.getAttribute("aria-hidden") === "true")).toBe(true);
  expect(drive.container.querySelector(".pb-param.pb-r-blend")).not.toBeNull();
  drive.unmount();
  // trem has time + shape but no blend → row 4 stays ringed
  const trem = renderStrip("trem");
  const tremEmpties = Array.from(trem.container.querySelectorAll<HTMLElement>(".pb-slot-empty"));
  expect(tremEmpties.map((e) => e.style.getPropertyValue("--pb-row"))).toEqual(["4"]);
});

test("stomp click toggles bypass without opening the editor", () => {
  const onOpen = vi.fn();
  const onStomp = vi.fn();
  renderStrip("chorus", { onOpen, onStomp });
  fireEvent.click(screen.getByRole("button", { name: "On" }));
  expect(onStomp).toHaveBeenCalledTimes(1);
  expect(onOpen).not.toHaveBeenCalled();
});

test("click, Enter and Space on the card open the editor", () => {
  const onOpen = vi.fn();
  renderStrip("chorus", { onOpen });
  const card = screen.getByRole("button", { name: "Open Chorus in editor" });
  fireEvent.click(card);
  expect(onOpen).toHaveBeenCalledTimes(1);
  fireEvent.keyDown(card, { key: "Enter" });
  expect(onOpen).toHaveBeenCalledTimes(2);
  fireEvent.keyDown(card, { key: " " });
  expect(onOpen).toHaveBeenCalledTimes(3);
  fireEvent.keyDown(card, { key: "x" });
  expect(onOpen).toHaveBeenCalledTimes(3);
});

test("bypass state: pb-bypassed class and stomp label reflect enabled", () => {
  const on = renderStrip("chorus");
  const card = on.container.querySelector(".pb-strip");
  expect(card).not.toHaveClass("pb-bypassed");
  expect(screen.getByRole("button", { name: "On" })).toHaveAttribute("aria-pressed", "true");
  on.unmount();
  const off = renderStrip("chorus", { enabled: false });
  expect(off.container.querySelector(".pb-strip")).toHaveClass("pb-bypassed");
  expect(screen.getByRole("button", { name: "Bypassed" })).toHaveAttribute("aria-pressed", "false");
});

test("delay parks: labelOff passes through and its Time mini renders oversized", () => {
  const { container } = renderStrip("delay", {
    enabled: false,
    labelOff: "Parked",
    miniSizes: { "delay.time_ms": 44 },
  });
  expect(screen.getByRole("button", { name: "Parked" })).toBeInTheDocument();
  const timeMini = container.querySelector<HTMLElement>(".pb-r-time .pb-mini");
  expect(timeMini?.style.width).toBe("calc(var(--u) * 44)");
  const fbMini = container.querySelector<HTMLElement>(".pb-r-shape .pb-mini");
  expect(fbMini?.style.width).toBe("calc(var(--u) * 36)");
});

test("minis stay display-only without onParamChange", () => {
  renderStrip("chorus");
  expect(screen.queryAllByRole("slider")).toHaveLength(0);
});

test("onParamChange turns every param mini into a slider", () => {
  renderStrip("chorus", { onParamChange: () => {} });
  const card = screen.getByRole("button", { name: "Open Chorus in editor" });
  expect(
    within(card)
      .getAllByRole("slider")
      .map((s) => s.getAttribute("aria-label")),
  ).toEqual(["Rate", "Depth", "Mix"]);
});

test("dragging a param knob reports its id and never opens the editor", () => {
  const onOpen = vi.fn();
  const onParamChange = vi.fn();
  renderStrip("chorus", { onOpen, onParamChange });
  const rate = screen.getByRole("slider", { name: "Rate" });
  fireEvent.pointerDown(rate, { clientY: 300, pointerId: 1 });
  fireEvent.pointerMove(rate, { clientY: 270 }); // half the 60px mini sweep of 0.1–8 Hz
  fireEvent.pointerUp(rate);
  fireEvent.click(rate); // the click a completed drag synthesizes
  expect(onParamChange).toHaveBeenCalledTimes(1);
  expect(onParamChange.mock.calls[0][0]).toBe("chorus.rate");
  expect(onParamChange.mock.calls[0][1]).toBeCloseTo(4.85, 6); // 0.9 + 7.9 / 2
  expect(onOpen).not.toHaveBeenCalled();
});

test("wheel and keys on a strip knob report the right param id", () => {
  const onOpen = vi.fn();
  const onParamChange = vi.fn();
  renderStrip("chorus", { onOpen, onParamChange });
  fireEvent.wheel(screen.getByRole("slider", { name: "Mix" }), { deltaY: -100 });
  expect(onParamChange).toHaveBeenLastCalledWith("chorus.mix", 31); // 30 + 1% of 0–100
  fireEvent.keyDown(screen.getByRole("slider", { name: "Depth" }), { key: "ArrowDown" });
  expect(onParamChange).toHaveBeenLastCalledWith("chorus.depth", 44); // 45 − 1/100 of 0–100
  expect(onOpen).not.toHaveBeenCalled();
});

test("gate params carry the gate dot; non-gate params do not", () => {
  const { container } = renderStrip("chorus");
  expect(container.querySelector(".pb-r-blend .pb-gate-dot")).not.toBeNull();
  expect(container.querySelector(".pb-r-shape .pb-gate-dot")).toBeNull();
});

test("card accent and chrome come from the pedal spec", () => {
  const { container } = renderStrip("chorus", { extra: <span data-testid="sig" /> });
  const card = container.querySelector<HTMLElement>(".pb-strip");
  expect(card?.style.getPropertyValue("--pb-accent")).toBe("var(--pb-cyan)");
  expect(card?.querySelector(".pb-strip-top h3")?.textContent).toBe("Chorus");
  expect(card?.querySelector(".pb-slot-ix")?.textContent).toBe("02");
  expect(card?.querySelector(".pb-led")).not.toBeNull();
  // the signature module renders inside the viz band
  expect(card?.querySelector(".pb-viz [data-testid='sig']")).not.toBeNull();
});
