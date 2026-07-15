/**
 * Design-system model for the Flat Modern pedalboard UI.
 *
 * Source of truth for all values: docs/mockups/pedalboard-style-b-flat-modern.html.
 * Static mock data only — the /app/mockup playground never talks to the API.
 */

/** Param roles — same role always renders in the same grid row (see README). */
export type Role = "time" | "shape" | "blend" | "level";

export interface ParamSpec {
  /** Stable id, namespaced like "delay.time_ms". */
  id: string;
  label: string;
  role: Role;
  min: number;
  max: number;
  defaultValue: number;
  unit: "%" | "Hz" | "ms" | "dB" | "";
  /** Gate — knob at 0 is a true bypass (renders the gate notch + gate dot). */
  gate?: boolean;
  /** Bipolar — arc fills from the sweep center instead of the start. */
  bipolar?: boolean;
  /** Special display formats. "hzlog": value 0-100 maps to 20 Hz – 20 kHz. */
  fmt?: "hzlog";
}

export interface PedalSpec {
  id: string;
  label: string;
  /** Slot index string as displayed ("01" … "04"). */
  slot: string;
  /** CSS custom property carrying the pedal's accent color, e.g. "--pb-amber". */
  accentVar: string;
  params: ParamSpec[];
}

/** The real polyclav post-synth chain, values from the reference spec. */
export const CHAIN: PedalSpec[] = [
  {
    id: "drive",
    label: "Drive",
    slot: "01",
    accentVar: "--pb-amber",
    params: [
      {
        id: "drive.amount",
        label: "Drive",
        role: "blend",
        min: 0,
        max: 100,
        defaultValue: 15,
        unit: "%",
        gate: true,
      },
    ],
  },
  {
    id: "chorus",
    label: "Chorus",
    slot: "02",
    accentVar: "--pb-cyan",
    params: [
      {
        id: "chorus.rate",
        label: "Rate",
        role: "time",
        min: 0.1,
        max: 8,
        defaultValue: 0.9,
        unit: "Hz",
      },
      {
        id: "chorus.depth",
        label: "Depth",
        role: "shape",
        min: 0,
        max: 100,
        defaultValue: 45,
        unit: "%",
      },
      {
        id: "chorus.mix",
        label: "Mix",
        role: "blend",
        min: 0,
        max: 100,
        defaultValue: 30,
        unit: "%",
        gate: true,
      },
    ],
  },
  {
    id: "trem",
    label: "Trem",
    slot: "03",
    accentVar: "--pb-violet",
    params: [
      {
        id: "trem.rate",
        label: "Rate",
        role: "time",
        min: 0.1,
        max: 15,
        defaultValue: 5.2,
        unit: "Hz",
      },
      {
        id: "trem.depth",
        label: "Depth",
        role: "shape",
        min: 0,
        max: 100,
        defaultValue: 0,
        unit: "%",
        gate: true,
      },
    ],
  },
  {
    id: "delay",
    label: "Delay",
    slot: "04",
    accentVar: "--pb-mint",
    params: [
      {
        id: "delay.time_ms",
        label: "Time",
        role: "time",
        min: 1,
        max: 1000,
        defaultValue: 380,
        unit: "ms",
      },
      {
        id: "delay.feedback",
        label: "Feedback",
        role: "shape",
        min: 0,
        max: 90,
        defaultValue: 35,
        unit: "%",
      },
      {
        id: "delay.mix",
        label: "Mix",
        role: "blend",
        min: 0,
        max: 100,
        defaultValue: 25,
        unit: "%",
        gate: true,
      },
    ],
  },
];

/** Stereo bus card params (accent is the neutral tone, set by .pb-bus in CSS). */
export const BUS_PARAMS: ParamSpec[] = [
  {
    id: "bus.gain",
    label: "Gain",
    role: "level",
    min: -24,
    max: 24,
    defaultValue: 0,
    unit: "dB",
    bipolar: true,
  },
  { id: "bus.comp", label: "Comp", role: "shape", min: 0, max: 100, defaultValue: 35, unit: "%" },
  {
    id: "bus.reverb",
    label: "Reverb",
    role: "blend",
    min: 0,
    max: 100,
    defaultValue: 18,
    unit: "%",
  },
  {
    id: "bus.master",
    label: "Master",
    role: "level",
    min: -48,
    max: 0,
    defaultValue: -6,
    unit: "dB",
  },
];

export type PatchType = "native" | "soundfont" | "sfizz";

export interface PatchSpec {
  name: string;
  type: PatchType;
  color: string;
}

/** The pad row renders PAD_SLOTS slots; slots beyond PATCHES.length are empty. */
export const PAD_SLOTS = 8;

export const PATCHES: PatchSpec[] = [
  { name: "Minimoog", type: "native", color: "#4c7dff" },
  { name: "Rhodes Mk I", type: "soundfont", color: "#37b563" },
  { name: "Grand Piano", type: "soundfont", color: "#c8503e" },
  { name: "Drawbar Organ", type: "sfizz", color: "#b28de0" },
  { name: "Solina Strings", type: "soundfont", color: "#e8963c" },
];

// ---------------------------------------------------------------------------
// Synth voice model (synth screen). Every continuous control is a ParamSpec so
// knob components consume it directly; defaultValue doubles as the mock value.
// ---------------------------------------------------------------------------

export const OSC_WAVES = ["saw", "square", "tri", "pulse"] as const;
export type OscWave = (typeof OSC_WAVES)[number];

export interface OscillatorSpec {
  id: string;
  wave: OscWave;
  /** Octave offset, integer steps. */
  octave: ParamSpec;
  detuneCents: ParamSpec;
  level: ParamSpec;
}

export interface FilterSpec {
  /** Knob position 0-100 displayed logarithmically as 20 Hz – 20 kHz. */
  cutoffPos: ParamSpec;
  resonance: ParamSpec;
  envAmount: ParamSpec;
  kbdTrack: ParamSpec;
}

export interface EnvSpec {
  a: ParamSpec;
  d: ParamSpec;
  s: ParamSpec;
  r: ParamSpec;
}

export interface LfoSpec {
  rateHz: ParamSpec;
  depth: ParamSpec;
  toPitch: ParamSpec;
  toCutoff: ParamSpec;
  toAmp: ParamSpec;
}

export interface SynthSpec {
  oscs: [OscillatorSpec, OscillatorSpec, OscillatorSpec];
  filter: FilterSpec;
  filterEnv: EnvSpec;
  ampEnv: EnvSpec;
  lfo: LfoSpec;
}

function osc(
  n: 1 | 2 | 3,
  wave: OscWave,
  octave: number,
  detuneCents: number,
  level: number,
): OscillatorSpec {
  const p = `synth.osc${n}`;
  return {
    id: `${p}`,
    wave,
    octave: {
      id: `${p}.octave`,
      label: "Octave",
      role: "shape",
      min: -2,
      max: 2,
      defaultValue: octave,
      unit: "",
      bipolar: true,
    },
    detuneCents: {
      id: `${p}.detune`,
      label: "Detune",
      role: "shape",
      min: -50,
      max: 50,
      defaultValue: detuneCents,
      unit: "",
      bipolar: true,
    },
    level: {
      id: `${p}.level`,
      label: "Level",
      role: "level",
      min: 0,
      max: 100,
      defaultValue: level,
      unit: "%",
    },
  };
}

function env(prefix: string, a: number, d: number, s: number, r: number): EnvSpec {
  return {
    a: {
      id: `${prefix}.a`,
      label: "Attack",
      role: "time",
      min: 1,
      max: 10000,
      defaultValue: a,
      unit: "ms",
    },
    d: {
      id: `${prefix}.d`,
      label: "Decay",
      role: "time",
      min: 1,
      max: 10000,
      defaultValue: d,
      unit: "ms",
    },
    s: {
      id: `${prefix}.s`,
      label: "Sustain",
      role: "level",
      min: 0,
      max: 100,
      defaultValue: s,
      unit: "%",
    },
    r: {
      id: `${prefix}.r`,
      label: "Release",
      role: "time",
      min: 1,
      max: 10000,
      defaultValue: r,
      unit: "ms",
    },
  };
}

export const SYNTH: SynthSpec = {
  oscs: [osc(1, "saw", 0, 0, 85), osc(2, "saw", 0, 7, 70), osc(3, "square", -1, -5, 45)],
  filter: {
    cutoffPos: {
      id: "synth.filter.cutoff",
      label: "Cutoff",
      role: "shape",
      min: 0,
      max: 100,
      defaultValue: 62,
      unit: "",
      fmt: "hzlog",
    },
    resonance: {
      id: "synth.filter.resonance",
      label: "Resonance",
      role: "shape",
      min: 0,
      max: 100,
      defaultValue: 22,
      unit: "%",
    },
    envAmount: {
      id: "synth.filter.envAmount",
      label: "Env Amt",
      role: "shape",
      min: -100,
      max: 100,
      defaultValue: 35,
      unit: "%",
      bipolar: true,
    },
    kbdTrack: {
      id: "synth.filter.kbdTrack",
      label: "Kbd Track",
      role: "shape",
      min: 0,
      max: 100,
      defaultValue: 50,
      unit: "%",
    },
  },
  filterEnv: env("synth.fenv", 5, 240, 40, 320),
  ampEnv: env("synth.aenv", 3, 180, 75, 260),
  lfo: {
    rateHz: {
      id: "synth.lfo.rate",
      label: "Rate",
      role: "time",
      min: 0.05,
      max: 30,
      defaultValue: 5.5,
      unit: "Hz",
    },
    depth: {
      id: "synth.lfo.depth",
      label: "Depth",
      role: "shape",
      min: 0,
      max: 100,
      defaultValue: 25,
      unit: "%",
    },
    toPitch: {
      id: "synth.lfo.toPitch",
      label: "To Pitch",
      role: "shape",
      min: 0,
      max: 100,
      defaultValue: 0,
      unit: "%",
    },
    toCutoff: {
      id: "synth.lfo.toCutoff",
      label: "To Cutoff",
      role: "shape",
      min: 0,
      max: 100,
      defaultValue: 40,
      unit: "%",
    },
    toAmp: {
      id: "synth.lfo.toAmp",
      label: "To Amp",
      role: "shape",
      min: 0,
      max: 100,
      defaultValue: 0,
      unit: "%",
    },
  },
};
