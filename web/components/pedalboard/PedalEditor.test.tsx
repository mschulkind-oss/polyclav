import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { PedalEditor, rangeLabel } from "@/components/pedalboard/PedalEditor";
import { CHAIN, type ParamSpec, type PedalSpec } from "@/lib/pedalboard/model";

// The Knob's drag machinery (pointer capture, wheel, keys) is the knob
// package's own test domain, so it is mocked with a native range input:
// changing it is this suite's stand-in for the drag interaction. The simple
// presentational chrome (Stomp, Led, RoleGlyph, GateDot) renders for real.
vi.mock("@/components/pedalboard/Knob", () => ({
  Knob: (props: {
    spec: ParamSpec;
    value: number;
    onChange: (v: number) => void;
    size?: number;
    sizeClass?: "md" | "lg" | "xl";
  }) => (
    <input
      type="range"
      aria-label={props.spec.label}
      min={props.spec.min}
      max={props.spec.max}
      value={props.value}
      data-param={props.spec.id}
      data-size={props.size}
      data-size-class={props.sizeClass ?? "lg"}
      data-gate={props.spec.gate ? "true" : "false"}
      onChange={(e) => props.onChange(Number(e.currentTarget.value))}
    />
  ),
}));

const DELAY = CHAIN.find((p) => p.id === "delay") as PedalSpec;
const REVERB = CHAIN.find((p) => p.id === "reverb") as PedalSpec;
const VALUES = { "delay.time_ms": 380, "delay.feedback": 35, "delay.mix": 25 };

function setup(enabled = false, pedal: PedalSpec = DELAY, values: Record<string, number> = VALUES) {
  const handlers = {
    onChange: vi.fn(),
    onStomp: vi.fn(),
    onReset: vi.fn(),
    onBack: vi.fn(),
  };
  const view = render(
    <PedalEditor pedal={pedal} values={values} enabled={enabled} {...handlers} />,
  );
  return { ...handlers, view };
}

describe("PedalEditor", () => {
  it("routes each knob's change (drag) to onChange with the model param id", () => {
    const { onChange } = setup();
    fireEvent.change(screen.getByRole("slider", { name: "Time" }), { target: { value: "500" } });
    expect(onChange).toHaveBeenLastCalledWith("delay.time_ms", 500);
    fireEvent.change(screen.getByRole("slider", { name: "Feedback" }), { target: { value: "50" } });
    expect(onChange).toHaveBeenLastCalledWith("delay.feedback", 50);
    fireEvent.change(screen.getByRole("slider", { name: "Mix" }), { target: { value: "60" } });
    expect(onChange).toHaveBeenLastCalledWith("delay.mix", 60);
    expect(onChange).toHaveBeenCalledTimes(3);
  });

  it("gives the signature param (Time) the oversized xl knob and gates only Mix", () => {
    setup();
    const time = screen.getByRole("slider", { name: "Time" });
    expect(time).toHaveAttribute("data-size", "164");
    expect(time).toHaveAttribute("data-size-class", "xl");
    expect(time).toHaveAttribute("data-gate", "false");
    for (const name of ["Feedback", "Mix"]) {
      const knob = screen.getByRole("slider", { name });
      expect(knob).toHaveAttribute("data-size", "124");
      expect(knob).toHaveAttribute("data-size-class", "lg");
    }
    expect(screen.getByRole("slider", { name: "Mix" })).toHaveAttribute("data-gate", "true");
    const gateDots = document.querySelectorAll(".pb-k-label .pb-gate-dot");
    expect(gateDots).toHaveLength(1);
    expect(gateDots[0]?.parentElement).toHaveTextContent("Mix");
  });

  it("labels the knob row with role glyphs, ranges, and the interaction hint", () => {
    const { view } = setup();
    expect(view.container.querySelectorAll(".pb-k-label .pb-glyph")).toHaveLength(3);
    const labels = [...view.container.querySelectorAll(".pb-k-label")];
    expect(labels.map((l) => l.getAttribute("title"))).toEqual([
      "Time / rate",
      "Intensity",
      "Wet / dry blend",
    ]);
    expect(screen.getByText("1 – 1000 ms")).toBeInTheDocument();
    expect(screen.getByText("0 – 90%")).toBeInTheDocument();
    expect(screen.getByText("0 – 100%")).toBeInTheDocument();
    expect(
      screen.getByText("Drag vertically · shift = fine · scroll steps · double-click resets"),
    ).toBeInTheDocument();
  });

  it("fires onReset from the ghost reset button", () => {
    const { onReset } = setup();
    fireEvent.click(screen.getByRole("button", { name: /reset to defaults/i }));
    expect(onReset).toHaveBeenCalledTimes(1);
  });

  it("fires onBack from the breadcrumb Pedalboard link", () => {
    const { onBack } = setup();
    fireEvent.click(screen.getByRole("button", { name: "Pedalboard" }));
    expect(onBack).toHaveBeenCalledTimes(1);
  });

  it("flips the stomp label Parked/On and the status chip with enabled", () => {
    const { view, onStomp, ...handlers } = setup(false);
    const stomp = screen.getByRole("button", { name: "Parked" });
    expect(stomp).toHaveClass("pb-stomp", "pb-big");
    expect(stomp).not.toHaveClass("pb-on");
    expect(stomp).toHaveAttribute("aria-pressed", "false");
    const parkedChip = screen.getByText("Parked — settings kept");
    expect(parkedChip).toHaveClass("pb-chip");
    expect(parkedChip).not.toHaveClass("pb-live");
    expect(document.querySelector(".pb-hero")).toHaveClass("pb-bypassed");

    fireEvent.click(stomp);
    expect(onStomp).toHaveBeenCalledTimes(1);

    view.rerender(
      <PedalEditor pedal={DELAY} values={VALUES} enabled onStomp={onStomp} {...handlers} />,
    );
    const onStompBtn = screen.getByRole("button", { name: "On" });
    expect(onStompBtn).toHaveClass("pb-stomp", "pb-on", "pb-big");
    expect(onStompBtn).toHaveAttribute("aria-pressed", "true");
    const liveChip = screen.getByText("Engaged");
    expect(liveChip).toHaveClass("pb-chip", "pb-live");
    expect(document.querySelector(".pb-hero")).not.toHaveClass("pb-bypassed");
  });

  it("renders the identity column: title, LED, sub line, breadcrumb current", () => {
    const { view } = setup();
    expect(screen.getByRole("heading", { level: 2, name: "DELAY" })).toBeInTheDocument();
    expect(view.container.querySelector(".pb-hero-title .pb-led")).not.toBeNull();
    expect(screen.getByText("Stereo echo · slot 04")).toBeInTheDocument();
    expect(screen.getByText("Delay")).toHaveClass("pb-crumb-cur");
  });

  it("captions the echo tail from the live values and renders the viz", () => {
    setup();
    expect(screen.getByText("380 ms Time · 35% Feedback · 25% Mix")).toBeInTheDocument();
    const dots = document.querySelectorAll(".pb-e-dot");
    expect(dots.length).toBeGreaterThan(0);
    expect(dots[0]?.getAttribute("cx")).toBe("116.8");
  });

  it("generalises to any pedal: reverb gets its own knobs, signature and viz", () => {
    setup(true, REVERB, { "reverb.decay": 2400, "reverb.tone": 50, "reverb.mix": 26 });
    // reverb's signature knob is Mix (its one live engine param).
    expect(screen.getByRole("slider", { name: "Mix" })).toHaveAttribute("data-size-class", "xl");
    expect(screen.getByRole("slider", { name: "Decay" })).toBeInTheDocument();
    expect(screen.getByRole("slider", { name: "Tone" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 2, name: "REVERB" })).toBeInTheDocument();
    expect(screen.getByText("Algorithmic reverb · slot 06")).toBeInTheDocument();
    // the reverb bloom viz renders (fill path) inside the editor tail wrapper.
    expect(document.querySelector(".pb-editor-viz .pb-fillpath")).not.toBeNull();
  });
});

describe("rangeLabel", () => {
  it("formats the delay params like the reference", () => {
    const labels = DELAY.params.map(rangeLabel);
    expect(labels).toEqual(["1 – 1000 ms", "0 – 90%", "0 – 100%"]);
  });
});
