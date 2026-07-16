/**
 * The seam between the Flat Modern design system (display units, model.ts) and
 * the polyclav daemon API. Each param id declares which REST surface carries
 * it, the backend field name, and the display<->engine unit conversion. The
 * board is fully live wherever a param appears here; params NOT listed are
 * UI-only for now (no engine param yet) and simply hold local state.
 */

/** Which REST surface a param lands on. */
export type Endpoint = "params" | "mastering" | "chain";

export interface ParamWire {
  endpoint: Endpoint;
  /** Backend field (/api/params, /api/mastering) or chain param id (/api/chain). */
  field: string;
  /** Display value -> engine value (e.g. 0..100 % -> 0..1). */
  toEngine: (display: number) => number;
  /** Engine value (SSE echo / GET snapshot) -> display value. */
  fromEngine: (engine: number) => number;
}

const same = (v: number) => v;
const pct = (v: number) => v / 100; // display % -> 0..1
const unpct = (v: number) => v * 100; // 0..1 -> display %

/**
 * Live wiring per model param id. Absent ids are UI-only until the chain
 * registry grows a matching engine param (docs/PEDALBOARD_UI.md Piece 1):
 * comp.attack, reverb.decay, reverb.tone.
 */
export const PARAM_WIRING: Record<string, ParamWire> = {
  // Drive pedal — via /api/chain so its stomp parks the value (drive.amount
  // aliases the drive_pedal atomic + state key in the engine registry).
  "drive.amount": { endpoint: "chain", field: "drive.amount", toEngine: pct, fromEngine: unpct },
  // Chorus / tremolo / delay -> /api/chain (engine ids differ from the UI ids).
  "chorus.rate": { endpoint: "chain", field: "chorus.rate_hz", toEngine: same, fromEngine: same },
  "chorus.depth": { endpoint: "chain", field: "chorus.depth", toEngine: pct, fromEngine: unpct },
  "chorus.mix": { endpoint: "chain", field: "chorus.mix", toEngine: pct, fromEngine: unpct },
  "trem.rate": { endpoint: "chain", field: "tremolo.rate_hz", toEngine: same, fromEngine: same },
  "trem.depth": { endpoint: "chain", field: "tremolo.depth", toEngine: pct, fromEngine: unpct },
  "delay.time_ms": { endpoint: "chain", field: "delay.time_ms", toEngine: same, fromEngine: same },
  "delay.feedback": {
    endpoint: "chain",
    field: "delay.feedback",
    toEngine: pct,
    fromEngine: unpct,
  },
  "delay.mix": { endpoint: "chain", field: "delay.mix", toEngine: pct, fromEngine: unpct },
  // Comp pedal — main amount is the per-patch compressor; glue is the master glue.
  "comp.amount": { endpoint: "params", field: "compressor", toEngine: pct, fromEngine: unpct },
  "comp.glue": { endpoint: "mastering", field: "comp_amount", toEngine: pct, fromEngine: unpct },
  // Reverb pedal — only the wet send is a live engine param today.
  "reverb.mix": { endpoint: "params", field: "reverb", toEngine: pct, fromEngine: unpct },
  // Master-out card.
  "master.level": { endpoint: "params", field: "volume", toEngine: pct, fromEngine: unpct },
  "master.ceiling": {
    endpoint: "mastering",
    field: "limiter_ceiling_db",
    toEngine: same,
    fromEngine: same,
  },
};

/** Reverse index "endpoint:field" -> model param id, for applying SSE echoes. */
export const WIRE_BY_BACKEND: Record<string, string> = Object.fromEntries(
  Object.entries(PARAM_WIRING).map(([id, w]) => [`${w.endpoint}:${w.field}`, id]),
);

/**
 * Pedals with a real per-pedal enable on /api/chain: the UI pedal id maps to
 * the engine stage id (trem -> tremolo). Their stomp sends
 * {"<stage>.enabled": bool} and the engine parks the gate param (settings
 * survive). Pedals absent here (comp, reverb) have no engine enable.
 */
export const STAGE_ENABLE: Record<string, string> = {
  drive: "drive",
  chorus: "chorus",
  trem: "tremolo",
  delay: "delay",
};

/**
 * Pedals with no engine enable realise their stomp in the client: while
 * bypassed the listed param ids are pushed to engine 0, and restored to their
 * stored display value on re-enable — the same "park, keep settings" behaviour
 * the chain gate gives the other pedals.
 */
export const SOFT_BYPASS_PARAMS: Record<string, string[]> = {
  comp: ["comp.amount", "comp.glue"],
  reverb: ["reverb.mix"],
};
