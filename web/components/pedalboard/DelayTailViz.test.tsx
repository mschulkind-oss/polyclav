import { render } from "@testing-library/react";
import { expect, test } from "vitest";
import { DelayTailViz } from "@/components/pedalboard/DelayTailViz";

function dots(el: HTMLElement): SVGCircleElement[] {
  return Array.from(el.querySelectorAll("circle"));
}

test("dots sit at cx = 8 + (8 + t·0.048)·n with opacity fb^n·0.9", () => {
  const t = 380;
  const fb = 0.35;
  const { container } = render(<DelayTailViz timeMs={t} feedback={fb} live />);
  const circles = dots(container);
  // fb^4·0.9 ≈ 0.014 < 0.03 → the 4th echo is culled
  expect(circles).toHaveLength(3);
  const spacing = 8 + t * 0.048;
  circles.forEach((c, i) => {
    const n = i + 1;
    expect(c.getAttribute("cx")).toBe((8 + spacing * n).toFixed(1));
    expect(c.getAttribute("opacity")).toBe((fb ** n * 0.9).toFixed(3));
    expect(c.getAttribute("cy")).toBe("10");
  });
  expect(circles.map((c) => c.getAttribute("cx"))).toEqual(["34.2", "60.5", "86.7"]);
});

test("live dots ping on a max(t·5, 220) ms cycle, echo n delayed n·t ms", () => {
  const { container } = render(<DelayTailViz timeMs={380} feedback={0.35} live />);
  dots(container).forEach((c, i) => {
    expect(c.style.animationDuration).toBe("1900ms");
    expect(c.style.animationDelay).toBe(`${(i + 1) * 380}ms`);
  });
  // short times clamp to the 220 ms floor
  const fast = render(<DelayTailViz timeMs={20} feedback={0.35} live />);
  expect(dots(fast.container)[0]?.style.animationDuration).toBe("220ms");
});

test("parked (not live) dots carry no ping timing", () => {
  const { container } = render(<DelayTailViz timeMs={380} feedback={0.35} live={false} />);
  const circles = dots(container);
  expect(circles).toHaveLength(3);
  for (const c of circles) {
    expect(c.style.animationDuration).toBe("");
    expect(c.style.animationDelay).toBe("");
  }
});

test("feedback shapes the tail: more echoes survive at high feedback, none at 0", () => {
  const high = render(<DelayTailViz timeMs={380} feedback={0.9} live />);
  expect(dots(high.container)).toHaveLength(4);
  const none = render(<DelayTailViz timeMs={380} feedback={0} live />);
  expect(dots(none.container)).toHaveLength(0);
  // the dry-signal tick line always remains
  expect(none.container.querySelectorAll("line")).toHaveLength(1);
});
