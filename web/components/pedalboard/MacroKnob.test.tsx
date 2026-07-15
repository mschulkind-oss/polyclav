import { fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { MacroKnob } from "@/components/pedalboard/MacroKnob";

function Harness({
  initial = 64,
  rangeA = 0,
  rangeB = 60,
  onMapped,
  defaultValue,
  size,
  label,
}: {
  initial?: number;
  rangeA?: number;
  rangeB?: number;
  onMapped?: (m: number) => void;
  defaultValue?: number;
  size?: number;
  label?: string;
}) {
  const [v, setV] = useState(initial);
  return (
    <MacroKnob
      value={v}
      onChange={setV}
      rangeA={rangeA}
      rangeB={rangeB}
      onMapped={onMapped}
      defaultValue={defaultValue}
      size={size}
      label={label}
    />
  );
}

const knob = () => screen.getByRole("slider");
const now = () => Number(knob().getAttribute("aria-valuenow"));

function q(container: HTMLElement, sel: string): Element {
  const el = container.querySelector(sel);
  if (!el) throw new Error(`missing ${sel}`);
  return el;
}

function rotationOf(el: Element): number {
  const m = /rotate\((-?[\d.]+(?:e-?\d+)?)\s/.exec(el.getAttribute("transform") ?? "");
  if (!m) throw new Error(`no rotation in ${el.getAttribute("transform")}`);
  return Number(m[1]);
}

describe("MacroKnob dual-ring rendering", () => {
  it("renders as a pb-md knob at 112 units by default", () => {
    const { container } = render(<Harness />);
    const el = q(container, ".pb-knob") as HTMLElement;
    expect(el.classList.contains("pb-md")).toBe(true);
    expect(el.style.width).toBe("calc(var(--u) * 112)");
    expect(q(container, "svg").getAttribute("viewBox")).toBe("0 0 112 112");
  });

  it("draws the outer ghost ring outside the inner arc pair", () => {
    const { container } = render(<Harness />);
    const outer = q(container, ".pb-k-otrack");
    expect(outer.getAttribute("r")).toBe("50"); // c - 6
    expect(outer.getAttribute("transform")).toBe("rotate(135 56 56)");
    expect(outer.getAttribute("stroke-dasharray")).toBe("270 360");
    expect(q(container, ".pb-k-track").getAttribute("r")).toBe("41"); // rO - 9
    expect(q(container, ".pb-k-arc").getAttribute("r")).toBe("41");
  });

  it("marks the mapped range on the outer ring as dash length/offset", () => {
    const { container } = render(<Harness rangeA={25} rangeB={75} />);
    const seg = q(container, ".pb-k-orange");
    const [len] = (seg.getAttribute("stroke-dasharray") ?? "").split(" ");
    expect(Number.parseFloat(len)).toBeCloseTo(135, 6); // 50% of 270
    expect(Number.parseFloat(seg.getAttribute("stroke-dashoffset") ?? "")).toBeCloseTo(-67.5, 6);
  });

  it("starts the range segment at the sweep start when rangeA is 0", () => {
    const { container } = render(<Harness rangeA={0} rangeB={100} />);
    const seg = q(container, ".pb-k-orange");
    expect(seg.getAttribute("stroke-dasharray")).toBe("270 360");
    expect(seg.getAttribute("stroke-dashoffset")).toBe("0");
  });

  it("rotates the outer tick to the MAPPED value, not the raw value", () => {
    const { container } = render(<Harness initial={50} rangeA={20} rangeB={80} />);
    // mapped = 20 + 0.5 * 60 = 50 -> angle 0
    expect(q(container, ".pb-k-otick").getAttribute("transform")).toBe("rotate(0 56 56)");
    const { container: c2 } = render(<Harness initial={64} rangeA={0} rangeB={60} />);
    // mapped = 38.4 -> -135 + 270 * 0.384
    expect(rotationOf(q(c2, ".pb-k-otick"))).toBeCloseTo(-31.32, 5);
  });

  it("shows the raw percent readout", () => {
    const { container } = render(<Harness initial={31} rangeA={0} rangeB={100} />);
    expect(q(container, ".pb-k-num").textContent).toBe("31");
    expect(q(container, ".pb-k-unit").textContent).toBe("%");
    expect(knob().getAttribute("aria-valuetext")).toBe("31%");
  });
});

describe("MacroKnob mapped-value callback", () => {
  it("reports rangeA + v/100 * (rangeB - rangeA) on mount", () => {
    const onMapped = vi.fn();
    render(<Harness initial={64} rangeA={0} rangeB={60} onMapped={onMapped} />);
    expect(onMapped).toHaveBeenCalledTimes(1);
    expect(onMapped.mock.calls[0][0]).toBeCloseTo(38.4, 10);
  });

  it("reports again whenever the mapped value changes", () => {
    const onMapped = vi.fn();
    render(<Harness initial={50} rangeA={20} rangeB={80} onMapped={onMapped} />);
    expect(onMapped.mock.calls[0][0]).toBeCloseTo(50, 10);
    fireEvent.wheel(knob(), { deltaY: -100 }); // 50 -> 51 -> mapped 50.6
    expect(onMapped).toHaveBeenCalledTimes(2);
    expect(onMapped.mock.calls[1][0]).toBeCloseTo(50.6, 10);
  });
});

describe("MacroKnob inner knob interaction (identical to Knob)", () => {
  it("exposes slider semantics over the fixed 0-100 macro range", () => {
    render(<Harness initial={64} label="Macro 1 Echo" />);
    const el = knob();
    expect(el.getAttribute("aria-label")).toBe("Macro 1 Echo");
    expect(el.getAttribute("aria-valuemin")).toBe("0");
    expect(el.getAttribute("aria-valuemax")).toBe("100");
    expect(el.getAttribute("aria-valuenow")).toBe("64");
  });

  it("drags vertically with the 200px-per-sweep canon and clamps", () => {
    render(<Harness initial={25} />);
    const el = knob();
    fireEvent.pointerDown(el, { clientY: 300, pointerId: 1 });
    expect(el.classList.contains("pb-dragging")).toBe(true);
    fireEvent.pointerMove(el, { clientY: 200 });
    expect(now()).toBe(75);
    fireEvent.pointerMove(el, { clientY: -900 });
    expect(now()).toBe(100);
    fireEvent.pointerUp(el);
    expect(el.classList.contains("pb-dragging")).toBe(false);
  });

  it("wheel-steps 1% (Shift 0.2%)", () => {
    render(<Harness initial={64} />);
    fireEvent.wheel(knob(), { deltaY: -100 });
    expect(now()).toBe(65);
    fireEvent.wheel(knob(), { deltaY: 100, shiftKey: true });
    expect(now()).toBeCloseTo(64.8, 6);
  });

  it("double-click resets to the defaultValue prop", () => {
    render(<Harness initial={64} defaultValue={10} />);
    fireEvent.doubleClick(knob());
    expect(now()).toBe(10);
  });

  it("double-click falls back to the first-rendered value", () => {
    render(<Harness initial={64} />);
    fireEvent.wheel(knob(), { deltaY: -100 });
    expect(now()).toBe(65);
    fireEvent.doubleClick(knob());
    expect(now()).toBe(64);
  });

  it("arrow keys step the raw value", () => {
    render(<Harness initial={64} />);
    fireEvent.keyDown(knob(), { key: "ArrowUp" });
    expect(now()).toBe(65);
    fireEvent.keyDown(knob(), { key: "ArrowDown", shiftKey: true });
    expect(now()).toBeCloseTo(64.8, 6);
  });

  it("ignores all input when disabled", () => {
    const onChange = vi.fn();
    render(<MacroKnob value={64} onChange={onChange} rangeA={0} rangeB={60} disabled />);
    const el = knob();
    expect(el.getAttribute("tabindex")).toBe("-1");
    fireEvent.pointerDown(el, { clientY: 300, pointerId: 1 });
    fireEvent.pointerMove(el, { clientY: 100 });
    fireEvent.wheel(el, { deltaY: -100 });
    fireEvent.keyDown(el, { key: "ArrowUp" });
    fireEvent.doubleClick(el);
    expect(onChange).not.toHaveBeenCalled();
  });
});
