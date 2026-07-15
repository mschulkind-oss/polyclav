import { fireEvent, render, screen, within } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import {
  OscSection,
  type OscSectionProps,
  type OscValues,
} from "@/components/pedalboard/OscSection";
import type { ParamSpec } from "@/lib/pedalboard/model";
import { SYNTH } from "@/lib/pedalboard/model";

interface StubKnobProps {
  spec: ParamSpec;
  value: number;
  onChange?: (v: number) => void;
}

// The knob package is built in parallel; mock the seam so this test runs
// against the documented contract without the real files. The stubs mirror
// the contract: Knob is a slider, MiniKnob is aria-hidden display-only.
vi.mock("@/components/pedalboard/synthExternals", async () => {
  const { createElement } = await import("react");
  const KnobStub = ({ spec, value }: StubKnobProps) =>
    createElement("div", {
      role: "slider",
      tabIndex: 0,
      "aria-label": spec.label,
      "aria-valuemin": spec.min,
      "aria-valuemax": spec.max,
      "aria-valuenow": value,
    });
  const MiniKnobStub = ({ spec, value }: StubKnobProps) =>
    createElement("div", {
      className: "pb-mini",
      "aria-hidden": true,
      "data-param": spec.id,
      "data-value": value,
    });
  return { Knob: KnobStub, MiniKnob: MiniKnobStub };
});

function Host() {
  const [oscs, setOscs] = useState<OscValues[]>(() =>
    SYNTH.oscs.map((o) => ({
      wave: o.wave,
      octave: o.octave.defaultValue,
      detune: o.detuneCents.defaultValue,
      level: o.level.defaultValue,
    })),
  );
  const onChange: OscSectionProps["onChange"] = (ix, field, v) => {
    setOscs((prev) => prev.map((o, i) => (i === ix ? Object.assign({}, o, { [field]: v }) : o)));
  };
  return <OscSection oscs={oscs} onChange={onChange} />;
}

const scopeD = (container: HTMLElement) =>
  container.querySelector(".pb-scope path")?.getAttribute("d");

describe("OscSection", () => {
  it("renders one wave selector, stepper, and mini pair per oscillator", () => {
    const { container } = render(<Host />);
    for (const n of [1, 2, 3]) {
      expect(screen.getByRole("group", { name: `Osc ${n} wave` })).toBeInTheDocument();
      expect(screen.getByRole("group", { name: `Osc ${n} octave` })).toBeInTheDocument();
    }
    expect(container.querySelectorAll(".pb-mini")).toHaveLength(6); // detune + level × 3
  });

  it("switches aria-pressed and the scope path when osc 1's wave changes", () => {
    const { container } = render(<Host />);
    const group = screen.getByRole("group", { name: "Osc 1 wave" });
    const saw = within(group).getByRole("button", { name: "saw" });
    const square = within(group).getByRole("button", { name: "square" });
    expect(saw).toHaveAttribute("aria-pressed", "true");
    expect(square).toHaveAttribute("aria-pressed", "false");
    const before = scopeD(container);

    fireEvent.click(square);
    expect(square).toHaveAttribute("aria-pressed", "true");
    expect(saw).toHaveAttribute("aria-pressed", "false");
    expect(scopeD(container)).not.toBe(before);
  });

  it("keeps the scope on osc 1 when another oscillator's wave changes", () => {
    const { container } = render(<Host />);
    const before = scopeD(container);
    const group3 = screen.getByRole("group", { name: "Osc 3 wave" });
    fireEvent.click(within(group3).getByRole("button", { name: "tri" }));
    expect(within(group3).getByRole("button", { name: "tri" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    expect(scopeD(container)).toBe(before);
  });

  it("wires the octave steppers with model defaults and bounds", () => {
    render(<Host />);
    const oct1 = screen.getByRole("group", { name: "Osc 1 octave" });
    expect(within(oct1).getByText("0")).toBeInTheDocument();
    fireEvent.click(within(oct1).getByRole("button", { name: "Osc 1 octave up" }));
    expect(within(oct1).getByText("+1")).toBeInTheDocument();

    const oct3 = screen.getByRole("group", { name: "Osc 3 octave" });
    expect(within(oct3).getByText("-1")).toBeInTheDocument(); // SYNTH osc 3 default
  });
});
