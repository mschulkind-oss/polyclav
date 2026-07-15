import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { Stomp } from "@/components/pedalboard/Stomp";

describe("Stomp", () => {
  it("renders engaged with the default label and aria-pressed", () => {
    render(<Stomp on onToggle={() => {}} />);
    const btn = screen.getByRole("button", { name: "On" });
    expect(btn).toHaveAttribute("aria-pressed", "true");
    expect(btn.className).toContain("pb-on");
  });

  it("renders bypassed and swaps the label with state", () => {
    const { rerender } = render(<Stomp on={false} onToggle={() => {}} />);
    const btn = screen.getByRole("button", { name: "Bypassed" });
    expect(btn).toHaveAttribute("aria-pressed", "false");
    expect(btn.className).not.toContain("pb-on");
    rerender(<Stomp on onToggle={() => {}} />);
    expect(btn).toHaveTextContent("On");
    expect(btn).toHaveAttribute("aria-pressed", "true");
  });

  it("supports custom labels (the delay's Parked)", () => {
    render(<Stomp on={false} onToggle={() => {}} labelOff="Parked" />);
    expect(screen.getByRole("button", { name: "Parked" })).toBeInTheDocument();
  });

  it("fires onToggle on click", () => {
    const onToggle = vi.fn();
    render(<Stomp on onToggle={onToggle} />);
    fireEvent.click(screen.getByRole("button"));
    expect(onToggle).toHaveBeenCalledTimes(1);
  });

  it("never bubbles the click to the enclosing strip", () => {
    const stripClick = vi.fn();
    render(
      // biome-ignore lint/a11y/noStaticElementInteractions: stands in for the clickable strip
      // biome-ignore lint/a11y/useKeyWithClickEvents: test fixture only cares about bubbling
      <div onClick={stripClick}>
        <Stomp on onToggle={() => {}} />
      </div>,
    );
    fireEvent.click(screen.getByRole("button"));
    expect(stripClick).not.toHaveBeenCalled();
  });

  it("adds pb-big for the editor hero", () => {
    render(<Stomp on onToggle={() => {}} big />);
    expect(screen.getByRole("button").className).toContain("pb-big");
  });

  it("renders the square stomp dot", () => {
    render(<Stomp on onToggle={() => {}} />);
    expect(screen.getByRole("button").querySelector(".pb-stomp-dot")).not.toBeNull();
  });
});
