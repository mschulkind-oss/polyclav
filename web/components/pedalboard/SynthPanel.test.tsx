import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { SynthPanel } from "@/components/pedalboard/SynthPanel";
import type { ParamSpec } from "@/lib/pedalboard/model";
import { PATCHES } from "@/lib/pedalboard/model";

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

const native = PATCHES[0]; // Minimoog — native
const soundfont = PATCHES[1]; // Rhodes Mk I — soundfont
const sfz = PATCHES[3]; // Drawbar Organ — sfizz

describe("SynthPanel (ungated)", () => {
  it("hides the gate note and exposes the controls", () => {
    const { container } = render(<SynthPanel gated={false} patch={native} />);
    expect(screen.queryByRole("note")).toBeNull();
    expect(screen.getAllByRole("slider").length).toBeGreaterThan(0);
    expect(screen.getAllByRole("button", { name: "saw" })).toHaveLength(3);
    const body = container.querySelector(".pb-synth-body");
    expect(body).not.toBeNull();
    expect(body?.className).not.toContain("pb-gated");
    expect(body).not.toHaveAttribute("inert");
  });

  it("shows the Launchkey hint bar for the VOICE page", () => {
    render(<SynthPanel gated={false} patch={native} />);
    expect(screen.getByText("VOICE > Filter")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Edit on hardware" })).toBeInTheDocument();
  });
});

describe("SynthPanel (gated)", () => {
  it("shows the native-only note with the patch name and type", () => {
    render(<SynthPanel gated patch={soundfont} />);
    const note = screen.getByRole("note");
    expect(note.textContent).toContain("VOICE editor is native-only");
    expect(note.textContent).toContain("Rhodes Mk I is a soundfont");
    expect(note.textContent).toContain("Pick a native patch");
  });

  it("names whichever non-native patch is active", () => {
    render(<SynthPanel gated patch={sfz} />);
    expect(screen.getByRole("note").textContent).toContain("Drawbar Organ is a sfizz");
  });

  it("makes every control unreachable (dimmed, inert, hidden from the a11y tree)", () => {
    const { container } = render(<SynthPanel gated patch={soundfont} />);
    expect(screen.queryAllByRole("slider")).toHaveLength(0);
    expect(screen.queryByRole("button", { name: "saw" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Edit on hardware" })).toBeNull();
    const body = container.querySelector(".pb-synth-body");
    expect(body).not.toBeNull();
    expect(body?.className).toContain("pb-gated"); // pointer-events: none in synth.extra.css
    expect(body).toHaveAttribute("inert");
    expect(body).toHaveAttribute("aria-hidden", "true");
  });
});
