import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { EchoTailViz } from "@/components/pedalboard/EchoTailViz";

function renderTail(timeMs = 380, feedback = 35, mix = 25) {
  const { container } = render(<EchoTailViz timeMs={timeMs} feedback={feedback} mix={mix} />);
  return container;
}

describe("EchoTailViz", () => {
  it("spaces echo dots by the true delay time: cx = 18 + n * t * 0.26", () => {
    const container = renderTail(380, 35, 25);
    const dots = container.querySelectorAll(".pb-e-dot");
    expect(dots).toHaveLength(4);
    const expected = [1, 2, 3, 4].map((n) => (18 + n * 380 * 0.26).toFixed(1));
    expect([...dots].map((d) => d.getAttribute("cx"))).toEqual(expected);
    expect(expected).toEqual(["116.8", "215.6", "314.4", "413.2"]);
  });

  it("fades dots by feedback x mix and sizes them by feedback", () => {
    const container = renderTail(380, 35, 25);
    const first = container.querySelector(".pb-e-dot");
    // op1 = min(1, 0.35^0.7 * (0.55 + 0.25*0.8)) and r1 = 7 * 0.35^0.28
    expect(first?.getAttribute("opacity")).toBe(
      Math.min(1, 0.35 ** 0.7 * (0.55 + 0.25 * 0.8)).toFixed(3),
    );
    expect(first?.getAttribute("r")).toBe((7 * 0.35 ** 0.28).toFixed(2));
  });

  it("draws no echo dots at zero feedback", () => {
    const container = renderTail(380, 0, 25);
    expect(container.querySelectorAll(".pb-e-dot")).toHaveLength(0);
    // the dry ping is still there
    expect(container.querySelector(".pb-t-dry")).not.toBeNull();
  });

  it("puts ruler ticks every 250 ms with majors + labels at whole seconds", () => {
    const container = renderTail();
    const ticks = container.querySelectorAll(".pb-t-tick");
    expect(ticks).toHaveLength(16); // 250..4000 step 250
    const majors = [...ticks].filter((t) => t.getAttribute("y2") === "31");
    const minors = [...ticks].filter((t) => t.getAttribute("y2") === "29");
    expect(majors).toHaveLength(4);
    expect(minors).toHaveLength(12);
    // 1 s major sits at x = 18 + 1000 * 0.26
    expect(majors[0]?.getAttribute("x1")).toBe("278.0");
    const labels = container.querySelectorAll(".pb-t-lab");
    expect([...labels].map((l) => l.textContent)).toEqual(["1 s", "2 s", "3 s", "4 s"]);
  });

  it("labels the first echo with +N ms at its ruler position", () => {
    const container = renderTail(380, 35, 25);
    const label = container.querySelector(".pb-t-ms");
    expect(label?.textContent).toBe("+380 ms");
    expect(label?.getAttribute("x")).toBe("116.8");
  });

  it("animates every cycle at max(t * 5, 220) ms with n * t delays", () => {
    const container = renderTail(380, 35, 25);
    const dry = container.querySelector<SVGLineElement>(".pb-t-dry");
    expect(dry?.style.animationDuration).toBe("1900ms");
    const dots = container.querySelectorAll<SVGCircleElement>(".pb-e-dot");
    for (const [i, dot] of [...dots].entries()) {
      expect(dot.style.animationDuration).toBe("1900ms");
      expect(dot.style.animationDelay).toBe(`${(i + 1) * 380}ms`);
    }
  });

  it("floors the animation cycle at 220 ms for short delay times", () => {
    const container = renderTail(20, 35, 25);
    const dry = container.querySelector<SVGLineElement>(".pb-t-dry");
    expect(dry?.style.animationDuration).toBe("220ms");
  });
});
