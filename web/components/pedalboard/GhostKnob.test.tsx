import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { GhostKnob } from "@/components/pedalboard/GhostKnob";

function q(container: HTMLElement, sel: string): Element {
  const el = container.querySelector(sel);
  if (!el) throw new Error(`missing ${sel}`);
  return el;
}

describe("GhostKnob", () => {
  it("renders the dormant ring at 112 units by default", () => {
    const { container } = render(<GhostKnob />);
    const el = q(container, ".pb-knob-ghost") as HTMLElement;
    expect(el.style.width).toBe("calc(var(--u) * 112)");
    expect(el.style.height).toBe("calc(var(--u) * 112)");
    expect(q(container, "svg").getAttribute("viewBox")).toBe("0 0 112 112");
  });

  it("draws only the 270-degree ghost track (r = c - 15)", () => {
    const { container } = render(<GhostKnob />);
    const track = q(container, ".pb-k-track");
    expect(track.getAttribute("cx")).toBe("56");
    expect(track.getAttribute("r")).toBe("41");
    expect(track.getAttribute("transform")).toBe("rotate(135 56 56)");
    expect(track.getAttribute("stroke-dasharray")).toBe("270 360");
    expect(container.querySelector(".pb-k-arc")).toBeNull();
    expect(container.querySelector(".pb-k-ptr")).toBeNull();
  });

  it("shows the em-dash readout", () => {
    const { container } = render(<GhostKnob />);
    expect(q(container, ".pb-k-read .pb-k-num").textContent).toBe("—");
  });

  it("is inert: aria-hidden, no slider role, no tab stop", () => {
    const { container, queryByRole } = render(<GhostKnob />);
    const el = q(container, ".pb-knob-ghost");
    expect(el.getAttribute("aria-hidden")).toBe("true");
    expect(el.hasAttribute("tabindex")).toBe(false);
    expect(queryByRole("slider")).toBeNull();
  });

  it("accepts a custom size", () => {
    const { container } = render(<GhostKnob size={90} />);
    expect((q(container, ".pb-knob-ghost") as HTMLElement).style.width).toBe("calc(var(--u) * 90)");
    expect(q(container, ".pb-k-track").getAttribute("r")).toBe("30");
  });
});
