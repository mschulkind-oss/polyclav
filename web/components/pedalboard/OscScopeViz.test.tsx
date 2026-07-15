import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { OscScopeViz, scopePath } from "@/components/pedalboard/OscScopeViz";
import { OSC_WAVES } from "@/lib/pedalboard/model";

describe("scopePath", () => {
  it("draws a distinct trace per wave", () => {
    const paths = OSC_WAVES.map((w) => scopePath(w));
    expect(new Set(paths).size).toBe(OSC_WAVES.length);
  });

  it("starts at x=0 and covers 3 periods for a seamless one-period scroll", () => {
    for (const w of OSC_WAVES) {
      const d = scopePath(w);
      expect(d.startsWith("M0 ")).toBe(true);
      expect(d).toContain("177"); // 3 × 59-unit periods
    }
  });
});

describe("OscScopeViz", () => {
  it("renders the wave's path inside the scrolling pb-scope group", () => {
    const { container } = render(<OscScopeViz wave="saw" />);
    const path = container.querySelector(".pb-scope path");
    expect(path).not.toBeNull();
    expect(path?.getAttribute("d")).toBe(scopePath("saw"));
  });

  it("changes the path when the wave changes", () => {
    const { container, rerender } = render(<OscScopeViz wave="saw" />);
    const before = container.querySelector(".pb-scope path")?.getAttribute("d");
    rerender(<OscScopeViz wave="square" />);
    const after = container.querySelector(".pb-scope path")?.getAttribute("d");
    expect(after).not.toBe(before);
    expect(after).toBe(scopePath("square"));
  });
});
