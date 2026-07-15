import { fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { MiniKnob } from "@/components/pedalboard/MiniKnob";
import type { ParamSpec } from "@/lib/pedalboard/model";
import { BUS_PARAMS } from "@/lib/pedalboard/model";

function spec(over: Partial<ParamSpec> = {}): ParamSpec {
  return {
    id: "test.depth",
    label: "Depth",
    role: "shape",
    min: 0,
    max: 100,
    defaultValue: 45,
    unit: "%",
    ...over,
  };
}

function q(container: HTMLElement, sel: string): Element {
  const el = container.querySelector(sel);
  if (!el) throw new Error(`missing ${sel}`);
  return el;
}

describe("MiniKnob chrome", () => {
  it("is display-only: aria-hidden, no slider role, no tab stop", () => {
    const { container, queryByRole } = render(<MiniKnob spec={spec()} value={45} />);
    const el = q(container, ".pb-mini");
    expect(el.getAttribute("aria-hidden")).toBe("true");
    expect(el.hasAttribute("tabindex")).toBe(false);
    expect(queryByRole("slider")).toBeNull();
  });

  it("defaults to 36 viewBox units sized via --u", () => {
    const { container } = render(<MiniKnob spec={spec()} value={45} />);
    const el = q(container, ".pb-mini") as HTMLElement;
    expect(el.style.width).toBe("calc(var(--u) * 36)");
    expect(el.style.height).toBe("calc(var(--u) * 36)");
    expect(q(container, "svg").getAttribute("viewBox")).toBe("0 0 36 36");
    expect(q(container, ".pb-k-track").getAttribute("r")).toBe("14.5"); // c - 3.5
  });

  it("supports the delay-time 44 size", () => {
    const { container } = render(<MiniKnob spec={spec()} value={45} size={44} />);
    expect((q(container, ".pb-mini") as HTMLElement).style.width).toBe("calc(var(--u) * 44)");
    expect(q(container, "svg").getAttribute("viewBox")).toBe("0 0 44 44");
    expect(q(container, ".pb-k-track").getAttribute("r")).toBe("18.5");
  });
});

describe("MiniKnob unipolar arc", () => {
  it("draws frac * 270 dash units from the sweep start", () => {
    const { container } = render(<MiniKnob spec={spec()} value={25} />);
    const arc = q(container, ".pb-k-arc") as SVGElement;
    expect(arc.getAttribute("stroke-dasharray")).toBe("67.5 360");
    expect(arc.getAttribute("stroke-dashoffset")).toBe("0");
    expect(arc.style.opacity).toBe("1");
  });

  it("hides the arc when the dash length drops under 0.75", () => {
    const { container } = render(<MiniKnob spec={spec()} value={0.2} />);
    const arc = q(container, ".pb-k-arc") as SVGElement;
    const len = Number.parseFloat((arc.getAttribute("stroke-dasharray") ?? "").split(" ")[0]);
    expect(len).toBeCloseTo(0.54, 6);
    expect(arc.style.opacity).toBe("0");
  });

  it("keeps the 0.01 minimum dash at exactly zero", () => {
    const { container } = render(<MiniKnob spec={spec()} value={0} />);
    const arc = q(container, ".pb-k-arc") as SVGElement;
    expect(arc.getAttribute("stroke-dasharray")).toBe("0.01 360");
    expect(arc.style.opacity).toBe("0");
  });

  it("rotates the pointer with the value", () => {
    const { container } = render(<MiniKnob spec={spec()} value={25} />);
    const ptr = q(container, ".pb-k-ptr");
    expect(ptr.getAttribute("transform")).toBe("rotate(-67.5 18 18)");
    expect(ptr.getAttribute("y1")).toBe("6.5"); // c - r + 3
    expect(ptr.getAttribute("y2")).toBe("11.9"); // c - r * 0.42
  });

  it("clamps out-of-range values to the sweep", () => {
    const { container } = render(<MiniKnob spec={spec()} value={999} />);
    expect(q(container, ".pb-k-arc").getAttribute("stroke-dasharray")).toBe("270 360");
    expect(q(container, ".pb-k-ptr").getAttribute("transform")).toBe("rotate(135 18 18)");
  });
});

/** Stateful wrapper so drags/wheels/keys feed back into the controlled value. */
function Harness({ spec: s, initial, size }: { spec: ParamSpec; initial?: number; size?: number }) {
  const [v, setV] = useState(initial ?? s.defaultValue);
  return <MiniKnob spec={s} value={v} onChange={setV} size={size} />;
}

const mini = () => screen.getByRole("slider");
const now = () => Number(mini().getAttribute("aria-valuenow"));

describe("MiniKnob display-only mode (no onChange)", () => {
  it("stays inert: no drag class, wheel scrolls the page normally", () => {
    const { container } = render(<MiniKnob spec={spec()} value={45} />);
    const el = q(container, ".pb-mini") as HTMLElement;
    fireEvent.pointerDown(el, { clientY: 300, pointerId: 1 });
    expect(el.classList.contains("pb-dragging")).toBe(false);
    expect(fireEvent.wheel(el, { deltaY: -100 })).toBe(true); // not prevented
    expect(el.style.cursor).toBe("");
  });
});

describe("MiniKnob interactive mode (onChange)", () => {
  it("exposes the full aria slider contract and an ns-resize cursor", () => {
    render(<Harness spec={spec()} />);
    const el = mini();
    expect(el.getAttribute("aria-label")).toBe("Depth");
    expect(el.getAttribute("aria-valuemin")).toBe("0");
    expect(el.getAttribute("aria-valuemax")).toBe("100");
    expect(el.getAttribute("aria-valuenow")).toBe("45");
    expect(el.getAttribute("aria-valuetext")).toBe("45%");
    expect(el.getAttribute("tabindex")).toBe("0");
    expect(el.getAttribute("aria-hidden")).toBeNull();
    expect((el as HTMLElement).style.cursor).toBe("ns-resize");
    expect((el as HTMLElement).style.touchAction).toBe("none");
  });

  it("keeps the mini chrome: sized via --u, same svg, svg stays aria-hidden", () => {
    const { container } = render(<Harness spec={spec()} />);
    const el = mini() as HTMLElement;
    expect(el.classList.contains("pb-mini")).toBe(true);
    expect(el.style.width).toBe("calc(var(--u) * 36)");
    expect(q(container, "svg").getAttribute("aria-hidden")).toBe("true");
    expect(q(container, ".pb-k-track").getAttribute("r")).toBe("14.5");
  });

  it("drags with travel scaled to its size: 36/120 of the 200px canon = 60px sweep", () => {
    render(<Harness spec={spec()} />);
    const el = mini();
    fireEvent.pointerDown(el, { clientY: 300, pointerId: 1 });
    expect(el.classList.contains("pb-dragging")).toBe(true);
    fireEvent.pointerMove(el, { clientY: 270 }); // 30px up = half the 60px sweep
    expect(now()).toBe(95);
    fireEvent.pointerMove(el, { clientY: 285 }); // 15px up from start
    expect(now()).toBe(70);
    fireEvent.pointerUp(el);
    expect(el.classList.contains("pb-dragging")).toBe(false);
    fireEvent.pointerMove(el, { clientY: 100 }); // released — no longer tracking
    expect(now()).toBe(70);
  });

  it("scales the travel to the delay-time 44 mini too", () => {
    render(<Harness spec={spec()} initial={0} size={44} />);
    const el = mini();
    const travel = 200 * (44 / 120);
    fireEvent.pointerDown(el, { clientY: 300, pointerId: 1 });
    fireEvent.pointerMove(el, { clientY: 300 - travel / 2 });
    expect(now()).toBeCloseTo(50, 6);
  });

  it("clamps drags at min and max", () => {
    render(<Harness spec={spec()} />);
    const el = mini();
    fireEvent.pointerDown(el, { clientY: 300, pointerId: 1 });
    fireEvent.pointerMove(el, { clientY: 100 });
    expect(now()).toBe(100);
    fireEvent.pointerMove(el, { clientY: 900 });
    expect(now()).toBe(0);
  });

  it("moves ~5x less with Shift held (fine mode)", () => {
    render(<Harness spec={spec()} />);
    const el = mini();
    fireEvent.pointerDown(el, { clientY: 300, pointerId: 1 });
    fireEvent.pointerMove(el, { clientY: 270, shiftKey: true }); // 30px / (60*5)
    expect(now()).toBe(55);
  });

  it("captures the pointer when the platform supports it", () => {
    const original = Element.prototype.setPointerCapture;
    const spy = vi.fn();
    Element.prototype.setPointerCapture = spy;
    try {
      render(<Harness spec={spec()} />);
      fireEvent.pointerDown(mini(), { clientY: 100, pointerId: 7 });
      expect(spy).toHaveBeenCalledWith(7);
    } finally {
      if (original) Element.prototype.setPointerCapture = original;
      else Reflect.deleteProperty(Element.prototype, "setPointerCapture");
    }
  });

  it("steps 1% per wheel notch on a non-passive listener (Shift 0.2%), clamped", () => {
    render(<Harness spec={spec()} />);
    const el = mini();
    // fireEvent returns false when preventDefault was called (non-passive listener)
    expect(fireEvent.wheel(el, { deltaY: -100 })).toBe(false);
    expect(now()).toBe(46);
    fireEvent.wheel(el, { deltaY: 100 });
    expect(now()).toBe(45);
    fireEvent.wheel(el, { deltaY: -100, shiftKey: true });
    expect(now()).toBeCloseTo(45.2, 6);
  });

  it("clamps wheel steps at the range edges", () => {
    render(<Harness spec={spec()} initial={100} />);
    fireEvent.wheel(mini(), { deltaY: -100 });
    expect(now()).toBe(100);
  });

  it("double-click resets to spec.defaultValue", () => {
    render(<Harness spec={spec()} initial={80} />);
    fireEvent.doubleClick(mini());
    expect(now()).toBe(45);
  });

  it("steps 1/100 of the range on arrows (Shift 1/500), clamped", () => {
    render(<Harness spec={spec()} initial={50} />);
    const el = mini();
    fireEvent.keyDown(el, { key: "ArrowUp" });
    expect(now()).toBe(51);
    fireEvent.keyDown(el, { key: "ArrowRight" });
    expect(now()).toBe(52);
    fireEvent.keyDown(el, { key: "ArrowDown" });
    expect(now()).toBe(51);
    fireEvent.keyDown(el, { key: "ArrowLeft" });
    expect(now()).toBe(50);
    fireEvent.keyDown(el, { key: "ArrowUp", shiftKey: true });
    expect(now()).toBeCloseTo(50.2, 6);
    fireEvent.keyDown(el, { key: "a" });
    expect(now()).toBeCloseTo(50.2, 6);
  });

  it("contains its events: pointerdown, clicks and a drag's click never bubble", () => {
    const parentClick = vi.fn();
    const parentPointerDown = vi.fn();
    render(
      // biome-ignore lint/a11y/noStaticElementInteractions: stands in for the clickable strip
      // biome-ignore lint/a11y/useKeyWithClickEvents: test fixture only cares about bubbling
      <div onClick={parentClick} onPointerDown={parentPointerDown}>
        <Harness spec={spec()} />
      </div>,
    );
    const el = mini();
    fireEvent.pointerDown(el, { clientY: 300, pointerId: 1 });
    fireEvent.pointerMove(el, { clientY: 280 });
    fireEvent.pointerUp(el);
    fireEvent.click(el); // the click a completed drag synthesizes
    expect(now()).toBeCloseTo(78.333333, 5); // the drag still landed
    fireEvent.doubleClick(el); // reset stays contained too
    expect(now()).toBe(45);
    expect(parentPointerDown).not.toHaveBeenCalled();
    expect(parentClick).not.toHaveBeenCalled();
  });
});

describe("MiniKnob bipolar arc (bus gain style)", () => {
  const gain = BUS_PARAMS[0]; // -24..+24 dB, bipolar

  it("hides at center with the minimum dash", () => {
    const { container } = render(<MiniKnob spec={gain} value={0} />);
    const arc = q(container, ".pb-k-arc") as SVGElement;
    expect(arc.getAttribute("stroke-dasharray")).toBe("0.01 360");
    expect(arc.getAttribute("stroke-dashoffset")).toBe("-135");
    expect(arc.style.opacity).toBe("0");
  });

  it("grows clockwise from center for positive values", () => {
    const { container } = render(<MiniKnob spec={gain} value={12} />);
    const arc = q(container, ".pb-k-arc") as SVGElement;
    expect(arc.getAttribute("stroke-dasharray")).toBe("67.5 360");
    expect(arc.getAttribute("stroke-dashoffset")).toBe("-135");
    expect(arc.style.opacity).toBe("1");
    expect(q(container, ".pb-k-ptr").getAttribute("transform")).toBe("rotate(67.5 18 18)");
  });

  it("grows counter-clockwise from center for negative values", () => {
    const { container } = render(<MiniKnob spec={gain} value={-12} />);
    const arc = q(container, ".pb-k-arc") as SVGElement;
    expect(arc.getAttribute("stroke-dasharray")).toBe("67.5 360");
    expect(arc.getAttribute("stroke-dashoffset")).toBe("-67.5");
    expect(q(container, ".pb-k-ptr").getAttribute("transform")).toBe("rotate(-67.5 18 18)");
  });
});
