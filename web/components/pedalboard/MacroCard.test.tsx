import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { MacroCard, mappedValue } from "@/components/pedalboard/MacroCard";

// The MacroKnob's drag machinery is the knob package's own test domain, so it
// is mocked with a native range input (role "slider"). The dormant GhostKnob
// renders for real — the assigned/dormant role distinction under test.
vi.mock("@/components/pedalboard/MacroKnob", () => ({
  MacroKnob: (props: {
    value: number;
    onChange: (v: number) => void;
    rangeA: number;
    rangeB: number;
    defaultValue?: number;
    size?: number;
    label?: string;
  }) => (
    <input
      type="range"
      aria-label={props.label}
      min={0}
      max={100}
      value={props.value}
      data-size={props.size}
      data-range-a={props.rangeA}
      data-range-b={props.rangeB}
      onChange={(e) => props.onChange(Number(e.currentTarget.value))}
    />
  ),
}));

const ASSIGNED = {
  name: "Echo",
  targetLabel: "Delay · Mix",
  accentVar: "--pb-mint",
  rangeA: 0,
  rangeB: 60,
  value: 64,
  defaultValue: 64,
};

describe("MacroCard (assigned)", () => {
  it("shows the mapped readout rangeA + v/100*(rangeB-rangeA) with one decimal", () => {
    render(<MacroCard slotIx={1} assigned={{ ...ASSIGNED, onChange: vi.fn() }} />);
    expect(screen.getByText("→ 38.4%")).toHaveClass("pb-m-mapped");
    render(
      <MacroCard
        slotIx={2}
        assigned={{ ...ASSIGNED, rangeB: 100, value: 31, onChange: vi.fn() }}
      />,
    );
    expect(screen.getByText("→ 31.0%")).toBeInTheDocument();
  });

  it("renders chip, name, target row, range row, and the accent variable", () => {
    const { container } = render(
      <MacroCard slotIx={1} assigned={{ ...ASSIGNED, onChange: vi.fn() }} />,
    );
    expect(screen.getByText("M1")).toHaveClass("pb-m-chip");
    expect(screen.getByText("Echo")).toHaveClass("pb-m-name");
    expect(screen.getByText("Delay · Mix")).toHaveClass("pb-m-target");
    expect(screen.getByText("Range 0–60%")).toBeInTheDocument();
    const card = container.querySelector<HTMLElement>(".pb-macro");
    expect(card).not.toHaveClass("pb-dormant");
    expect(card?.style.getPropertyValue("--pb-accent")).toBe("var(--pb-mint)");
  });

  it("passes the sweep + mapped range to MacroKnob and routes onChange", () => {
    const onChange = vi.fn();
    render(<MacroCard slotIx={1} assigned={{ ...ASSIGNED, onChange }} />);
    const knob = screen.getByRole("slider", { name: "Macro 1 Echo" });
    expect(knob).toHaveAttribute("data-size", "112");
    expect(knob).toHaveAttribute("data-range-a", "0");
    expect(knob).toHaveAttribute("data-range-b", "60");
    fireEvent.change(knob, { target: { value: "50" } });
    expect(onChange).toHaveBeenCalledWith(50);
  });
});

describe("MacroCard (dormant)", () => {
  it("has no slider role — a ghost ring and 'Assign on hardware' instead", () => {
    const { container } = render(<MacroCard slotIx={4} />);
    expect(screen.queryByRole("slider")).toBeNull();
    const ghost = container.querySelector(".pb-knob-ghost");
    expect(ghost).not.toBeNull();
    expect(ghost).toHaveTextContent("—");
    expect(screen.getByText("M4")).toBeInTheDocument();
    expect(screen.getByText("Unassigned")).toBeInTheDocument();
    expect(screen.getByText("No target")).toBeInTheDocument();
    expect(screen.getByText("Range —")).toBeInTheDocument();
    expect(screen.getByText("Assign on hardware")).toBeInTheDocument();
    expect(container.querySelector(".pb-macro")).toHaveClass("pb-dormant");
  });
});

describe("mappedValue", () => {
  it("linearly maps the 0–100 sweep into the target range", () => {
    expect(mappedValue(0, 60, 64)).toBeCloseTo(38.4);
    expect(mappedValue(0, 100, 31)).toBeCloseTo(31);
    expect(mappedValue(0, 80, 78)).toBeCloseTo(62.4);
    expect(mappedValue(20, 40, 50)).toBeCloseTo(30);
    expect(mappedValue(0, 60, 0)).toBe(0);
    expect(mappedValue(0, 60, 100)).toBe(60);
  });
});
