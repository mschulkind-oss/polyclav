import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { HwHintBar } from "@/components/pedalboard/HwHintBar";

const MAPPINGS = [
  { k: "K1", label: "Time" },
  { k: "K2", label: "Feedback" },
  { k: "K3", label: "Mix" },
];

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
});

describe("HwHintBar", () => {
  it("shows the hardware path and every knob mapping", () => {
    render(<HwHintBar path="FX > Delay" mappings={MAPPINGS} />);
    expect(screen.getByText("FX > Delay")).toHaveClass("pb-hw-path");
    for (const { k, label } of MAPPINGS) {
      expect(screen.getByText(k)).toHaveClass("pb-kchip");
      expect(screen.getByText(label, { exact: false })).toBeInTheDocument();
    }
  });

  it("flips the button to Sent for exactly 1.4 s", () => {
    const onEdit = vi.fn();
    render(<HwHintBar path="FX > Delay" mappings={MAPPINGS} onEditOnHardware={onEdit} />);
    const btn = screen.getByRole("button", { name: "Edit on hardware" });
    fireEvent.click(btn);
    expect(onEdit).toHaveBeenCalledTimes(1);
    expect(btn).toHaveTextContent("Sent to Launchkey");
    expect(btn.className).toContain("pb-sent");
    act(() => {
      vi.advanceTimersByTime(1399);
    });
    expect(btn).toHaveTextContent("Sent to Launchkey");
    act(() => {
      vi.advanceTimersByTime(1);
    });
    expect(btn).toHaveTextContent("Edit on hardware");
    expect(btn.className).not.toContain("pb-sent");
  });

  it("re-clicking restarts the sent window", () => {
    render(<HwHintBar path="FX > Delay" mappings={MAPPINGS} />);
    const btn = screen.getByRole("button");
    fireEvent.click(btn);
    act(() => {
      vi.advanceTimersByTime(1000);
    });
    fireEvent.click(btn);
    act(() => {
      vi.advanceTimersByTime(1399);
    });
    expect(btn).toHaveTextContent("Sent to Launchkey");
    act(() => {
      vi.advanceTimersByTime(1);
    });
    expect(btn).toHaveTextContent("Edit on hardware");
  });

  it("cleans up the pending timer on unmount", () => {
    const { unmount } = render(<HwHintBar path="FX > Delay" mappings={MAPPINGS} />);
    fireEvent.click(screen.getByRole("button"));
    expect(vi.getTimerCount()).toBe(1);
    unmount();
    expect(vi.getTimerCount()).toBe(0);
  });
});
