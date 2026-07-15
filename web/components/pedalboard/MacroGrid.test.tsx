import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { MacroGrid } from "@/components/pedalboard/MacroGrid";

// The MacroKnob's drag machinery is the knob package's own test domain, so it
// is mocked with a native range input (see MacroCard.test.tsx). GhostKnob
// renders for real in the dormant slots.
vi.mock("@/components/pedalboard/MacroKnob", () => ({
  MacroKnob: (props: { value: number; onChange: (v: number) => void; label?: string }) => (
    <input
      type="range"
      aria-label={props.label}
      min={0}
      max={100}
      value={props.value}
      onChange={(e) => props.onChange(Number(e.currentTarget.value))}
    />
  ),
}));

describe("MacroGrid", () => {
  it("renders 8 slots: 3 assigned with reference data, 5 dormant", () => {
    const { container } = render(<MacroGrid />);
    expect(container.querySelectorAll(".pb-macro")).toHaveLength(8);
    expect(container.querySelectorAll(".pb-macro.pb-dormant")).toHaveLength(5);
    expect(screen.getAllByText("Unassigned")).toHaveLength(5);
    expect(container.querySelectorAll(".pb-knob-ghost")).toHaveLength(5);
    for (const chip of ["M1", "M2", "M3", "M4", "M5", "M6", "M7", "M8"]) {
      expect(screen.getByText(chip)).toBeInTheDocument();
    }
  });

  it("assigns M1 Echo, M2 Shimmer, M3 Grit to their targets and ranges", () => {
    render(<MacroGrid />);
    expect(screen.getByText("Echo")).toBeInTheDocument();
    expect(screen.getByText("Delay · Mix")).toBeInTheDocument();
    expect(screen.getByText("Range 0–60%")).toBeInTheDocument();
    expect(screen.getByText("Shimmer")).toBeInTheDocument();
    expect(screen.getByText("Chorus · Depth")).toBeInTheDocument();
    expect(screen.getByText("Range 0–100%")).toBeInTheDocument();
    expect(screen.getByText("Grit")).toBeInTheDocument();
    expect(screen.getByText("Drive · Amount")).toBeInTheDocument();
    expect(screen.getByText("Range 0–80%")).toBeInTheDocument();
  });

  it("shows the reference mock values through their mapped readouts", () => {
    render(<MacroGrid />);
    expect(screen.getByRole("slider", { name: "Macro 1 Echo" })).toHaveValue("64");
    expect(screen.getByText("→ 38.4%")).toBeInTheDocument(); // 64 → 0–60
    expect(screen.getByRole("slider", { name: "Macro 2 Shimmer" })).toHaveValue("31");
    expect(screen.getByText("→ 31.0%")).toBeInTheDocument(); // 31 → 0–100
    expect(screen.getByRole("slider", { name: "Macro 3 Grit" })).toHaveValue("78");
    expect(screen.getByText("→ 62.4%")).toBeInTheDocument(); // 78 → 0–80
  });

  it("heads the screen with the assigned/free tally", () => {
    render(<MacroGrid />);
    expect(screen.getByText("3 assigned · 5 free")).toHaveClass("pb-meta");
    expect(screen.getByText("Macros")).toHaveClass("pb-kicker");
    expect(
      screen.getByText("Launchkey macro knobs M1–M8 · each sweep is scaled to its mapped range"),
    ).toBeInTheDocument();
  });

  it("holds macro state: turning a knob updates its mapped readout", () => {
    render(<MacroGrid />);
    fireEvent.change(screen.getByRole("slider", { name: "Macro 1 Echo" }), {
      target: { value: "50" },
    });
    expect(screen.getByText("→ 30.0%")).toBeInTheDocument(); // 50 → 0–60
    expect(screen.queryByText("→ 38.4%")).toBeNull();
    // the other macros are untouched
    expect(screen.getByText("→ 31.0%")).toBeInTheDocument();
    expect(screen.getByText("→ 62.4%")).toBeInTheDocument();
  });
});
