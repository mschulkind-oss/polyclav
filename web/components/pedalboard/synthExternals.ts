/**
 * The synth screen's imports from the knob package, funneled through this
 * one module so the synth tests can `vi.mock` a single seam and run without
 * the real knob files (the packages are built in parallel); the page bundle
 * re-exports the real components.
 *
 * Contract (see Knob.tsx / MiniKnob.tsx):
 *   Knob     — interactive arc knob. { spec: ParamSpec; value: number;
 *              onChange(v: number); size?: number;
 *              sizeClass?: "md" | "lg" | "xl"; disabled?: boolean }
 *   MiniKnob — display-only mini (aria-hidden, no interaction).
 *              { spec: ParamSpec; value: number; size?: number }
 */
export { Knob } from "@/components/pedalboard/Knob";
export { MiniKnob } from "@/components/pedalboard/MiniKnob";
