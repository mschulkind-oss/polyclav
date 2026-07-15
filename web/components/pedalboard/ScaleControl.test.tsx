import { act, fireEvent, render, renderHook, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { SCALES, ScaleControl, useUiScale } from "@/components/pedalboard/ScaleControl";

const KEY = "polyclav-ui-scale";

beforeEach(() => {
  window.localStorage.clear();
});

describe("useUiScale", () => {
  it("defaults to 100% and persists it (the reference writes on load too)", () => {
    const { result } = renderHook(() => useUiScale());
    expect(result.current.scale).toBe(1);
    expect(window.localStorage.getItem(KEY)).toBe("1");
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
    expect(result.current.scale).toBe(1);
  });

  it("steps the ladder, persists, and clamps at both ends", () => {
    const { result } = renderHook(() => useUiScale());
    act(() => result.current.inc());
    expect(result.current.scale).toBe(1.1);
    expect(window.localStorage.getItem(KEY)).toBe("1.1");
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

  it("reset returns to 100%", () => {
    const { result } = renderHook(() => useUiScale());
    act(() => result.current.dec());
    act(() => result.current.reset());
    expect(result.current.scale).toBe(1);
    expect(window.localStorage.getItem(KEY)).toBe("1");
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
    expect(screen.getByTitle("Click to reset to 100%")).toHaveTextContent("120%");
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

  it("clicking the readout resets to 100%", () => {
    const onScale = vi.fn();
    render(<ScaleControl scale={1.3} onScale={onScale} />);
    fireEvent.click(screen.getByTitle("Click to reset to 100%"));
    expect(onScale).toHaveBeenCalledWith(1);
  });
});
