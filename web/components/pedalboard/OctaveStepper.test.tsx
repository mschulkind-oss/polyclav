import { fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it } from "vitest";
import { formatOctave, OctaveStepper } from "@/components/pedalboard/OctaveStepper";

function Host({ initial }: { initial: number }) {
  const [v, setV] = useState(initial);
  return <OctaveStepper label="Octave" value={v} onChange={setV} />;
}

describe("formatOctave", () => {
  it("signs positives, keeps zero and negatives plain", () => {
    expect(formatOctave(1)).toBe("+1");
    expect(formatOctave(2)).toBe("+2");
    expect(formatOctave(0)).toBe("0");
    expect(formatOctave(-2)).toBe("-2");
  });
});

describe("OctaveStepper", () => {
  it("steps up and formats +N", () => {
    render(<Host initial={0} />);
    const up = screen.getByRole("button", { name: "Octave up" });
    fireEvent.click(up);
    expect(screen.getByText("+1")).toBeInTheDocument();
  });

  it("clamps at +2 (up disables and further clicks do nothing)", () => {
    render(<Host initial={1} />);
    const up = screen.getByRole("button", { name: "Octave up" });
    fireEvent.click(up);
    expect(screen.getByText("+2")).toBeInTheDocument();
    expect(up).toBeDisabled();
    fireEvent.click(up);
    expect(screen.getByText("+2")).toBeInTheDocument();
  });

  it("clamps at -2 (down disables and further clicks do nothing)", () => {
    render(<Host initial={-1} />);
    const down = screen.getByRole("button", { name: "Octave down" });
    fireEvent.click(down);
    expect(screen.getByText("-2")).toBeInTheDocument();
    expect(down).toBeDisabled();
    fireEvent.click(down);
    expect(screen.getByText("-2")).toBeInTheDocument();
  });
});
