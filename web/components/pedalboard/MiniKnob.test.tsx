import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
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
