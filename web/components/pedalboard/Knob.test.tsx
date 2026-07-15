import { fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { Knob } from "@/components/pedalboard/Knob";
import type { ParamSpec } from "@/lib/pedalboard/model";

function spec(over: Partial<ParamSpec> = {}): ParamSpec {
  return {
    id: "test.mix",
    label: "Mix",
    role: "blend",
    min: 0,
    max: 100,
    defaultValue: 25,
    unit: "%",
    ...over,
  };
}

/** Stateful wrapper so drags/wheels/keys feed back into the controlled value. */
function Harness({
  spec: s,
  initial,
  size,
  sizeClass,
}: {
  spec: ParamSpec;
  initial?: number;
  size?: number;
  sizeClass?: "md" | "lg" | "xl";
}) {
  const [v, setV] = useState(initial ?? s.defaultValue);
  return <Knob spec={s} value={v} onChange={setV} size={size} sizeClass={sizeClass} />;
}

const knob = () => screen.getByRole("slider");
const now = () => Number(knob().getAttribute("aria-valuenow"));

function q(container: HTMLElement, sel: string): Element {
  const el = container.querySelector(sel);
  if (!el) throw new Error(`missing ${sel}`);
  return el;
}

describe("Knob slider semantics", () => {
  it("exposes the full aria slider contract", () => {
    render(<Harness spec={spec()} />);
    const el = knob();
    expect(el.getAttribute("aria-label")).toBe("Mix");
    expect(el.getAttribute("aria-valuemin")).toBe("0");
    expect(el.getAttribute("aria-valuemax")).toBe("100");
    expect(el.getAttribute("aria-valuenow")).toBe("25");
    expect(el.getAttribute("aria-valuetext")).toBe("25%");
    expect(el.getAttribute("tabindex")).toBe("0");
    expect(el.getAttribute("aria-disabled")).toBeNull();
  });

  it("sizes the element with --u so the whole system scales", () => {
    const { container } = render(<Harness spec={spec()} size={164} />);
    const el = q(container, ".pb-knob") as HTMLElement;
    expect(el.style.width).toBe("calc(var(--u) * 164)");
    expect(el.style.height).toBe("calc(var(--u) * 164)");
    expect(q(container, "svg").getAttribute("viewBox")).toBe("0 0 164 164");
  });

  it("defaults to size 120 (the reference default)", () => {
    const { container } = render(<Harness spec={spec()} />);
    expect((q(container, ".pb-knob") as HTMLElement).style.width).toBe("calc(var(--u) * 120)");
  });

  it("maps sizeClass to .pb-md / .pb-xl with bare .pb-knob as lg", () => {
    const { container: md } = render(<Harness spec={spec()} sizeClass="md" />);
    expect(q(md, ".pb-knob").classList.contains("pb-md")).toBe(true);
    const { container: xl } = render(<Harness spec={spec()} sizeClass="xl" />);
    expect(q(xl, ".pb-knob").classList.contains("pb-xl")).toBe(true);
    const { container: lg } = render(<Harness spec={spec()} sizeClass="lg" />);
    const cls = q(lg, ".pb-knob").classList;
    expect(cls.contains("pb-md")).toBe(false);
    expect(cls.contains("pb-xl")).toBe(false);
  });
});

describe("Knob arc rendering (knobMath-driven)", () => {
  it("draws the 270-degree track rotated 135 degrees", () => {
    const { container } = render(<Harness spec={spec()} size={124} />);
    const track = q(container, ".pb-k-track");
    expect(track.getAttribute("cx")).toBe("62");
    expect(track.getAttribute("cy")).toBe("62");
    expect(track.getAttribute("r")).toBe("56");
    expect(track.getAttribute("transform")).toBe("rotate(135 62 62)");
    expect(track.getAttribute("pathLength")).toBe("360");
    expect(track.getAttribute("stroke-dasharray")).toBe("270 360");
  });

  it("draws the value arc as frac * 270 dash units", () => {
    const { container } = render(<Harness spec={spec()} size={124} />);
    const arc = q(container, ".pb-k-arc") as SVGElement;
    expect(arc.getAttribute("stroke-dasharray")).toBe("67.5 360");
    expect(arc.getAttribute("stroke-dashoffset")).toBe("0");
    expect(arc.style.opacity).toBe("1");
  });

  it("hides the arc near zero (frac < 0.004) but keeps the 0.01 min dash", () => {
    const { container } = render(<Harness spec={spec()} initial={0} size={124} />);
    const arc = q(container, ".pb-k-arc") as SVGElement;
    expect(arc.getAttribute("stroke-dasharray")).toBe("0.01 360");
    expect(arc.style.opacity).toBe("0");
  });

  it("rotates the pointer -135..+135 degrees with the value", () => {
    const { container } = render(<Harness spec={spec()} size={124} />);
    const ptr = q(container, ".pb-k-ptr");
    // frac 0.25 -> -135 + 67.5
    expect(ptr.getAttribute("transform")).toBe("rotate(-67.5 62 62)");
    expect(ptr.getAttribute("x1")).toBe("62");
    expect(ptr.getAttribute("y1")).toBe("12.0"); // c - r + 6
    expect(ptr.getAttribute("y2")).toBe("34.0"); // c - r * 0.5
  });

  it("renders the gate notch at sweep start for gate specs only", () => {
    const { container: gated } = render(<Harness spec={spec({ gate: true })} size={124} />);
    const notch = q(gated, ".pb-k-gate");
    expect(notch.getAttribute("cx")).toBe("62");
    expect(notch.getAttribute("cy")).toBe("118"); // c + r
    expect(notch.getAttribute("r")).toBe("1.8");
    expect(notch.getAttribute("transform")).toBe("rotate(45 62 62)");
    const { container: plain } = render(<Harness spec={spec()} size={124} />);
    expect(plain.querySelector(".pb-k-gate")).toBeNull();
  });

  it("grows bipolar arcs from the sweep center", () => {
    const s = spec({ min: -24, max: 24, defaultValue: 0, unit: "dB", bipolar: true });
    const { container } = render(<Harness spec={s} initial={12} size={124} />);
    const arc = q(container, ".pb-k-arc") as SVGElement;
    // frac 0.75: start at center (135), length 67.5
    expect(arc.getAttribute("stroke-dasharray")).toBe("67.5 360");
    expect(arc.getAttribute("stroke-dashoffset")).toBe("-135");
    expect(arc.style.opacity).toBe("1");
  });

  it("hides a bipolar arc when the value sits at center", () => {
    const s = spec({ min: -24, max: 24, defaultValue: 0, unit: "dB", bipolar: true });
    const { container } = render(<Harness spec={s} initial={0} size={124} />);
    expect((q(container, ".pb-k-arc") as SVGElement).style.opacity).toBe("0");
  });
});

describe("Knob readout formatting", () => {
  it("splits number and unit into pb-k-num / pb-k-unit", () => {
    const { container } = render(
      <Harness spec={spec({ unit: "ms", min: 1, max: 1000, defaultValue: 380 })} />,
    );
    expect(q(container, ".pb-k-num").textContent).toBe("380");
    expect(q(container, ".pb-k-unit").textContent).toBe("ms");
  });

  it("formats dB with the typographic minus", () => {
    const s = spec({ unit: "dB", min: -48, max: 0, defaultValue: -6 });
    const { container } = render(<Harness spec={s} />);
    expect(q(container, ".pb-k-num").textContent).toBe("−6.0");
    expect(q(container, ".pb-k-unit").textContent).toBe("dB");
    expect(knob().getAttribute("aria-valuetext")).toBe("−6.0 dB");
  });

  it("renders an empty unit line for unitless specs", () => {
    const s = spec({ unit: "", min: -50, max: 50, defaultValue: 7 });
    const { container } = render(<Harness spec={s} />);
    expect(q(container, ".pb-k-num").textContent).toBe("7");
    expect(q(container, ".pb-k-unit").textContent).toBe("");
  });

  it("shows hzlog values in Hz below 1k and kHz above", () => {
    const s = spec({ unit: "", fmt: "hzlog", min: 0, max: 100, defaultValue: 40 });
    const { container, unmount } = render(<Harness spec={s} />);
    expect(q(container, ".pb-k-num").textContent).toBe("317");
    expect(q(container, ".pb-k-unit").textContent).toBe("Hz");
    expect(knob().getAttribute("aria-valuetext")).toBe("317 Hz");
    unmount();
    const { container: hi } = render(<Harness spec={s} initial={62} />);
    expect(q(hi, ".pb-k-num").textContent).toBe("1.4");
    expect(q(hi, ".pb-k-unit").textContent).toBe("kHz");
    expect(knob().getAttribute("aria-valuetext")).toBe("1.4 kHz");
  });
});

describe("Knob drag canon", () => {
  it("turns with vertical drag: ~200px = full sweep, upward = increase", () => {
    render(<Harness spec={spec()} />);
    const el = knob();
    fireEvent.pointerDown(el, { clientY: 300, pointerId: 1 });
    expect(el.classList.contains("pb-dragging")).toBe(true);
    fireEvent.pointerMove(el, { clientY: 200 }); // 100px up = half sweep
    expect(now()).toBe(75);
    fireEvent.pointerMove(el, { clientY: 260 }); // 40px up from start
    expect(now()).toBe(45);
    fireEvent.pointerUp(el);
    expect(el.classList.contains("pb-dragging")).toBe(false);
  });

  it("clamps drags at min and max", () => {
    render(<Harness spec={spec()} />);
    const el = knob();
    fireEvent.pointerDown(el, { clientY: 300, pointerId: 1 });
    fireEvent.pointerMove(el, { clientY: -900 });
    expect(now()).toBe(100);
    fireEvent.pointerMove(el, { clientY: 1200 });
    expect(now()).toBe(0);
  });

  it("moves ~5x less with Shift held (fine mode)", () => {
    render(<Harness spec={spec()} />);
    const el = knob();
    fireEvent.pointerDown(el, { clientY: 300, pointerId: 1 });
    fireEvent.pointerMove(el, { clientY: 200, shiftKey: true }); // 100px / (200*5)
    expect(now()).toBeCloseTo(35, 6);
  });

  it("ignores pointer moves without a preceding pointerdown", () => {
    render(<Harness spec={spec()} />);
    const el = knob();
    fireEvent.pointerMove(el, { clientY: 0 });
    expect(now()).toBe(25);
  });

  it("stops tracking after pointerup / pointercancel", () => {
    render(<Harness spec={spec()} />);
    const el = knob();
    fireEvent.pointerDown(el, { clientY: 300, pointerId: 1 });
    fireEvent.pointerUp(el);
    fireEvent.pointerMove(el, { clientY: 100 });
    expect(now()).toBe(25);
    fireEvent.pointerDown(el, { clientY: 300, pointerId: 1 });
    fireEvent.pointerCancel(el);
    expect(el.classList.contains("pb-dragging")).toBe(false);
    fireEvent.pointerMove(el, { clientY: 100 });
    expect(now()).toBe(25);
  });

  it("captures the pointer when the platform supports it", () => {
    const original = Element.prototype.setPointerCapture;
    const spy = vi.fn();
    Element.prototype.setPointerCapture = spy;
    try {
      render(<Harness spec={spec()} />);
      fireEvent.pointerDown(knob(), { clientY: 100, pointerId: 7 });
      expect(spy).toHaveBeenCalledWith(7);
    } finally {
      if (original) Element.prototype.setPointerCapture = original;
      else Reflect.deleteProperty(Element.prototype, "setPointerCapture");
    }
  });
});

describe("Knob wheel canon", () => {
  it("steps 1% per notch and prevents page scroll", () => {
    render(<Harness spec={spec()} />);
    const el = knob();
    // fireEvent returns false when preventDefault was called (non-passive listener)
    expect(fireEvent.wheel(el, { deltaY: -100 })).toBe(false);
    expect(now()).toBe(26);
    fireEvent.wheel(el, { deltaY: 100 });
    expect(now()).toBe(25);
  });

  it("steps 0.2% with Shift held", () => {
    render(<Harness spec={spec()} />);
    fireEvent.wheel(knob(), { deltaY: -100, shiftKey: true });
    expect(now()).toBeCloseTo(25.2, 6);
  });

  it("clamps wheel steps at the range edges", () => {
    render(<Harness spec={spec()} initial={100} />);
    fireEvent.wheel(knob(), { deltaY: -100 });
    expect(now()).toBe(100);
  });
});

describe("Knob keyboard canon", () => {
  it("steps 1/100 of the range on arrows (up/right +, down/left -)", () => {
    render(<Harness spec={spec()} initial={50} />);
    const el = knob();
    fireEvent.keyDown(el, { key: "ArrowUp" });
    expect(now()).toBe(51);
    fireEvent.keyDown(el, { key: "ArrowRight" });
    expect(now()).toBe(52);
    fireEvent.keyDown(el, { key: "ArrowDown" });
    expect(now()).toBe(51);
    fireEvent.keyDown(el, { key: "ArrowLeft" });
    expect(now()).toBe(50);
  });

  it("steps 1/500 with Shift held", () => {
    render(<Harness spec={spec()} initial={50} />);
    fireEvent.keyDown(knob(), { key: "ArrowUp", shiftKey: true });
    expect(now()).toBeCloseTo(50.2, 6);
  });

  it("clamps at the edges and ignores other keys", () => {
    render(<Harness spec={spec()} initial={100} />);
    const el = knob();
    fireEvent.keyDown(el, { key: "ArrowUp" });
    expect(now()).toBe(100);
    fireEvent.keyDown(el, { key: "a" });
    fireEvent.keyDown(el, { key: "PageUp" });
    expect(now()).toBe(100);
  });
});

describe("Knob double-click", () => {
  it("resets to spec.defaultValue", () => {
    render(<Harness spec={spec()} initial={60} />);
    const el = knob();
    fireEvent.doubleClick(el);
    expect(now()).toBe(25);
  });
});

describe("Knob disabled", () => {
  it("ignores every input and leaves the tab order", () => {
    const onChange = vi.fn();
    render(<Knob spec={spec()} value={40} onChange={onChange} disabled />);
    const el = knob();
    expect(el.getAttribute("tabindex")).toBe("-1");
    expect(el.getAttribute("aria-disabled")).toBe("true");
    fireEvent.pointerDown(el, { clientY: 300, pointerId: 1 });
    expect(el.classList.contains("pb-dragging")).toBe(false);
    fireEvent.pointerMove(el, { clientY: 100 });
    fireEvent.wheel(el, { deltaY: -100 });
    fireEvent.keyDown(el, { key: "ArrowUp" });
    fireEvent.doubleClick(el);
    expect(onChange).not.toHaveBeenCalled();
  });

  it("lets wheel events scroll the page when disabled", () => {
    render(<Knob spec={spec()} value={40} onChange={vi.fn()} disabled />);
    expect(fireEvent.wheel(knob(), { deltaY: -100 })).toBe(true); // not prevented
  });
});
