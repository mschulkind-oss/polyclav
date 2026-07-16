import { describe, expect, it } from "vitest";
import type { ParamSpec } from "@/lib/pedalboard/model";
import { CHAIN, MASTER_PARAMS, PAD_SLOTS, PATCHES, SYNTH } from "@/lib/pedalboard/model";

function allSynthParams(): ParamSpec[] {
  const envs = [SYNTH.filterEnv, SYNTH.ampEnv].flatMap((e) => [e.a, e.d, e.s, e.r]);
  const oscs = SYNTH.oscs.flatMap((o) => [o.octave, o.detuneCents, o.level]);
  const { cutoffPos, resonance, envAmount, kbdTrack } = SYNTH.filter;
  const { rateHz, depth, toPitch, toCutoff, toAmp } = SYNTH.lfo;
  return [
    ...oscs,
    cutoffPos,
    resonance,
    envAmount,
    kbdTrack,
    ...envs,
    rateHz,
    depth,
    toPitch,
    toCutoff,
    toAmp,
  ];
}

describe("CHAIN", () => {
  it("models the six-pedal chain in signal order", () => {
    expect(CHAIN.map((p) => p.id)).toEqual(["drive", "chorus", "trem", "delay", "comp", "reverb"]);
    expect(CHAIN.map((p) => p.slot)).toEqual(["01", "02", "03", "04", "05", "06"]);
  });
  it("assigns one accent var per pedal", () => {
    expect(CHAIN.map((p) => p.accentVar)).toEqual([
      "--pb-amber",
      "--pb-cyan",
      "--pb-violet",
      "--pb-mint",
      "--pb-lime",
      "--pb-rose",
    ]);
  });
  it("marks the true-bypass knobs as gates", () => {
    const gates = CHAIN.flatMap((p) => p.params.filter((q) => q.gate).map((q) => q.id));
    expect(gates).toEqual([
      "drive.amount",
      "chorus.mix",
      "trem.depth",
      "delay.mix",
      "comp.amount",
      "comp.glue",
      "reverb.mix",
    ]);
  });
  it("keeps at most one param per role per pedal (row alignment contract)", () => {
    for (const pedal of CHAIN) {
      const roles = pedal.params.map((p) => p.role);
      expect(new Set(roles).size).toBe(roles.length);
    }
  });
});

describe("param invariants", () => {
  const params = [...CHAIN.flatMap((p) => p.params), ...MASTER_PARAMS, ...allSynthParams()];
  it("keeps every defaultValue inside [min, max]", () => {
    for (const p of params) {
      expect(p.min, p.id).toBeLessThan(p.max);
      expect(p.defaultValue, p.id).toBeGreaterThanOrEqual(p.min);
      expect(p.defaultValue, p.id).toBeLessThanOrEqual(p.max);
    }
  });
  it("uses unique param ids", () => {
    const ids = params.map((p) => p.id);
    expect(new Set(ids).size).toBe(ids.length);
  });
  it("gate params bottom out at 0 (true bypass)", () => {
    for (const p of params.filter((q) => q.gate)) {
      expect(p.min, p.id).toBe(0);
    }
  });
});

describe("MASTER_PARAMS", () => {
  it("models the master fader and the brick-wall limiter ceiling", () => {
    const level = MASTER_PARAMS.find((p) => p.id === "master.level");
    const ceiling = MASTER_PARAMS.find((p) => p.id === "master.ceiling");
    expect(level).toMatchObject({ min: 0, max: 100, defaultValue: 80, unit: "%" });
    expect(ceiling).toMatchObject({ min: -12, max: 0, defaultValue: -0.3, unit: "dB" });
  });
});

describe("PATCHES", () => {
  it("fills 5 of the 8 pad slots", () => {
    expect(PATCHES).toHaveLength(5);
    expect(PAD_SLOTS).toBe(8);
  });
  it("types every patch engine", () => {
    expect(PATCHES.map((p) => p.type)).toEqual([
      "native",
      "soundfont",
      "soundfont",
      "sfizz",
      "soundfont",
    ]);
  });
});

describe("SYNTH", () => {
  it("has three oscillators with typed waves", () => {
    expect(SYNTH.oscs).toHaveLength(3);
    expect(SYNTH.oscs.map((o) => o.wave)).toEqual(["saw", "saw", "square"]);
  });
  it("displays cutoff logarithmically", () => {
    expect(SYNTH.filter.cutoffPos.fmt).toBe("hzlog");
  });
  it("uses ms envelopes with level sustain", () => {
    for (const e of [SYNTH.filterEnv, SYNTH.ampEnv]) {
      expect(e.a.unit).toBe("ms");
      expect(e.d.unit).toBe("ms");
      expect(e.r.unit).toBe("ms");
      expect(e.s.unit).toBe("%");
      expect(e.s.max).toBe(100);
    }
  });
});
