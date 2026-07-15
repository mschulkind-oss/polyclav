import { act, fireEvent, render, renderHook, screen } from "@testing-library/react";
import type { CSSProperties } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  DEFAULT_SCALE,
  SCALES,
  ScaleControl,
  useUiScale,
} from "@/components/pedalboard/ScaleControl";

const KEY = "polyclav-ui-scale";

beforeEach(() => {
  window.localStorage.clear();
});

describe("useUiScale", () => {
  it("defaults to 110% and persists it (the reference writes on load too)", () => {
    const { result } = renderHook(() => useUiScale());
    expect(result.current.scale).toBe(1.1);
    expect(window.localStorage.getItem(KEY)).toBe("1.1");
  });

  it("restores a stored scale on mount", () => {
    window.localStorage.setItem(KEY, "1.3");
    const { result } = renderHook(() => useUiScale());
    expect(result.current.scale).toBe(1.3);
    expect(window.localStorage.getItem(KEY)).toBe("1.3");
  });

  it("ignores values that are not on the ladder", () => {
    window.localStorage.setItem(KEY, "2.5");
    const { result } = renderHook(() => useUiScale());
    expect(result.current.scale).toBe(DEFAULT_SCALE);
  });

  it("steps the ladder, persists, and clamps at both ends", () => {
    const { result } = renderHook(() => useUiScale());
    act(() => result.current.inc());
    expect(result.current.scale).toBe(1.2);
    expect(window.localStorage.getItem(KEY)).toBe("1.2");
    for (let i = 0; i < 10; i++) {
      act(() => result.current.inc());
    }
    expect(result.current.scale).toBe(SCALES[SCALES.length - 1]);
    expect(window.localStorage.getItem(KEY)).toBe("1.4");
    for (let i = 0; i < 20; i++) {
      act(() => result.current.dec());
    }
    expect(result.current.scale).toBe(SCALES[0]);
    expect(window.localStorage.getItem(KEY)).toBe("0.8");
  });

  it("reset returns to the 110% default", () => {
    const { result } = renderHook(() => useUiScale());
    act(() => result.current.dec());
    act(() => result.current.dec());
    act(() => result.current.reset());
    expect(result.current.scale).toBe(1.1);
    expect(window.localStorage.getItem(KEY)).toBe("1.1");
  });

  it("set snaps to the nearest ladder step", () => {
    const { result } = renderHook(() => useUiScale());
    act(() => result.current.set(1.32));
    expect(result.current.scale).toBe(1.3);
  });
});

describe("ScaleControl", () => {
  it("shows the percent readout", () => {
    render(<ScaleControl scale={1.2} onScale={() => {}} />);
    expect(screen.getByTitle("Click to reset to 110%")).toHaveTextContent("120%");
  });

  it("A+ / A− emit the neighboring ladder values", () => {
    const onScale = vi.fn();
    render(<ScaleControl scale={1} onScale={onScale} />);
    fireEvent.click(screen.getByRole("button", { name: "Larger UI" }));
    expect(onScale).toHaveBeenLastCalledWith(1.1);
    fireEvent.click(screen.getByRole("button", { name: "Smaller UI" }));
    expect(onScale).toHaveBeenLastCalledWith(0.9);
  });

  it("clamps at the ladder ends", () => {
    const onScale = vi.fn();
    const { rerender } = render(<ScaleControl scale={1.4} onScale={onScale} />);
    fireEvent.click(screen.getByRole("button", { name: "Larger UI" }));
    expect(onScale).toHaveBeenLastCalledWith(1.4);
    rerender(<ScaleControl scale={0.8} onScale={onScale} />);
    fireEvent.click(screen.getByRole("button", { name: "Smaller UI" }));
    expect(onScale).toHaveBeenLastCalledWith(0.8);
  });

  it("clicking the readout resets to the 110% default", () => {
    const onScale = vi.fn();
    render(<ScaleControl scale={1.3} onScale={onScale} />);
    fireEvent.click(screen.getByTitle("Click to reset to 110%"));
    expect(onScale).toHaveBeenCalledWith(DEFAULT_SCALE);
  });

  it("carries the fixed-position styling hook with no inline sizing", () => {
    const { container } = render(<ScaleControl scale={1} onScale={() => {}} />);
    const ctl = container.querySelector<HTMLElement>(".pb-scalectl");
    expect(ctl).not.toBeNull();
    // Plain-px sizing lives entirely in the .pb-scalectl CSS class — the
    // component must not carry inline styles that could track --pb-scale.
    expect(ctl?.getAttribute("style")).toBeNull();
    for (const b of Array.from(ctl?.querySelectorAll("button") ?? [])) {
      expect(b.getAttribute("style")).toBeNull();
    }
  });
});

/** Pairs the hook with the control the way the page does. */
function Harness() {
  const ui = useUiScale();
  return (
    <div
      className="pb-root"
      data-testid="scale-target"
      style={{ "--pb-scale": String(ui.scale) } as CSSProperties}
    >
      <ScaleControl scale={ui.scale} onScale={ui.set} />
    </div>
  );
}

describe("ScaleControl wired to a scale target via useUiScale", () => {
  it("A+ moves --pb-scale on the target and leaves the control's own style untouched", () => {
    render(<Harness />);
    const target = screen.getByTestId("scale-target");
    const ctl = target.querySelector<HTMLElement>(".pb-scalectl");
    expect(target.style.getPropertyValue("--pb-scale")).toBe("1.1");
    const ctlStyleBefore = ctl?.getAttribute("style") ?? null;
    fireEvent.click(screen.getByRole("button", { name: "Larger UI" }));
    expect(target.style.getPropertyValue("--pb-scale")).toBe("1.2");
    // The fixed, px-sized control must not restyle itself when zoom changes.
    expect(ctl?.getAttribute("style") ?? null).toBe(ctlStyleBefore);
    expect(ctlStyleBefore).toBeNull();
  });
});
