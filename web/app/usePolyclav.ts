"use client";

import { useCallback, useEffect, useReducer, useRef } from "react";
import { INITIAL_ENABLED } from "@/components/pedalboard/Pedalboard";
import { api } from "@/lib/api";
import { CHAIN, DEFAULT_PEDAL_ORDER, MASTER_PARAMS } from "@/lib/pedalboard/model";
import { normalizeOrder } from "@/lib/pedalboard/order";
import {
  type Endpoint,
  PARAM_WIRING,
  SOFT_BYPASS_PARAMS,
  STAGE_ENABLE,
  WIRE_BY_BACKEND,
} from "@/lib/pedalboard/wiring";
import { applySynthEvent } from "@/lib/synthMerge";
import type {
  ChainEvent,
  ChainState,
  Clip,
  DeviceEvent,
  Devices,
  MasteringEvent,
  NoteEvent,
  ParamsEvent,
  Patch,
  PatchEvent,
  PlayerState,
  Status,
  Synth,
  SynthEvent,
  VelocityEvent,
} from "@/lib/types";
import { useSSE } from "@/lib/useSSE";

const ORDER_KEY = "polyclav-pedal-order";
/** How long a locally-edited param ignores server echoes (drag guard). */
const GUARD_MS = 500;
/** Debounce window for batched param sends. */
const SEND_MS = 90;

/** backend stage id (drive/chorus/tremolo/delay) -> UI pedal id (…/trem/…). */
const STAGE_TO_PEDAL: Record<string, string> = Object.fromEntries(
  Object.entries(STAGE_ENABLE).map(([pedal, stage]) => [stage, pedal]),
);

/** Spec defaults in display units for every board + master param. */
function defaultChainValues(): Record<string, number> {
  const all = [...CHAIN.flatMap((p) => p.params), ...MASTER_PARAMS];
  return Object.fromEntries(all.map((p) => [p.id, p.defaultValue]));
}

export interface PolyclavState {
  version: string;
  devices: Devices;
  patches: Patch[];
  current: string;
  synth: Synth | null;
  velocityLabel: string;
  player: PlayerState | null;
  hasPlayer: boolean;
  cutoffPos?: number;
  cutoffHz?: number;
  /** Display-unit values keyed by model param id (pedals + master). */
  chainValues: Record<string, number>;
  /** Bypass map keyed by pedal id. */
  enabled: Record<string, boolean>;
  /** Pedal display order (ids). */
  order: string[];
}

const initialState: PolyclavState = {
  version: "",
  devices: { launchkey: "unknown", xr18: "unknown" },
  patches: [],
  current: "",
  synth: null,
  velocityLabel: "",
  player: null,
  hasPlayer: false,
  chainValues: defaultChainValues(),
  enabled: INITIAL_ENABLED,
  order: DEFAULT_PEDAL_ORDER,
};

type Action =
  | { t: "snapshot"; s: Status }
  | { t: "chainSnapshot"; c: ChainState }
  | { t: "values"; patch: Record<string, number> }
  | { t: "enable"; pedalId: string; on: boolean }
  | { t: "order"; order: string[] }
  | { t: "current"; name: string }
  | { t: "cutoff"; pos?: number; hz?: number }
  | { t: "setSynth"; synth: Synth }
  | { t: "synth"; d: SynthEvent }
  | { t: "player"; d: PlayerState }
  | { t: "velocity"; label: string }
  | { t: "device"; d: DeviceEvent };

/** Fold a map of backend "endpoint:field" values (engine units) into display values. */
function foldBackend(
  values: Record<string, number>,
  endpoint: Endpoint,
  fields: Record<string, number>,
): Record<string, number> {
  const next = { ...values };
  for (const [field, engine] of Object.entries(fields)) {
    const id = WIRE_BY_BACKEND[`${endpoint}:${field}`];
    const w = id ? PARAM_WIRING[id] : undefined;
    if (id && w) next[id] = w.fromEngine(engine);
  }
  return next;
}

function reducer(state: PolyclavState, a: Action): PolyclavState {
  switch (a.t) {
    case "snapshot": {
      const p = a.s.params;
      const params: Record<string, number> = {};
      if (typeof p.volume === "number") params.volume = p.volume;
      if (typeof p.reverb === "number") params.reverb = p.reverb;
      if (typeof p.compressor === "number") params.compressor = p.compressor;
      const mastering: Record<string, number> = {};
      if (typeof p.mastering_comp === "number") mastering.comp_amount = p.mastering_comp;
      if (typeof p.limiter_ceiling_db === "number")
        mastering.limiter_ceiling_db = p.limiter_ceiling_db;
      let chainValues = foldBackend(state.chainValues, "params", params);
      chainValues = foldBackend(chainValues, "mastering", mastering);
      return {
        ...state,
        version: a.s.version,
        devices: a.s.devices,
        patches: a.s.patches ?? [],
        current: p.patch,
        synth: p.synth ?? null,
        velocityLabel: p.velocity_curve || state.velocityLabel,
        player: a.s.player,
        hasPlayer: a.s.player != null,
        cutoffPos: p.cutoff_pos,
        cutoffHz: p.cutoff_hz,
        chainValues,
      };
    }
    case "chainSnapshot": {
      const values = { ...state.chainValues };
      const enabled = { ...state.enabled };
      for (const stage of a.c.stages) {
        const pedalId = STAGE_TO_PEDAL[stage.id] ?? stage.id;
        enabled[pedalId] = stage.enabled;
        for (const param of stage.params) {
          const id = WIRE_BY_BACKEND[`chain:${param.id}`];
          const w = id ? PARAM_WIRING[id] : undefined;
          if (id && w) values[id] = w.fromEngine(param.value);
        }
      }
      return { ...state, chainValues: values, enabled };
    }
    case "values":
      return { ...state, chainValues: { ...state.chainValues, ...a.patch } };
    case "enable":
      return { ...state, enabled: { ...state.enabled, [a.pedalId]: a.on } };
    case "order":
      return { ...state, order: normalizeOrder(a.order, DEFAULT_PEDAL_ORDER) };
    case "current":
      return { ...state, current: a.name };
    case "cutoff":
      return {
        ...state,
        cutoffPos: typeof a.pos === "number" ? a.pos : state.cutoffPos,
        cutoffHz: typeof a.hz === "number" ? a.hz : state.cutoffHz,
      };
    case "setSynth":
      return { ...state, synth: a.synth };
    case "synth":
      return state.synth ? { ...state, synth: applySynthEvent(state.synth, a.d) } : state;
    case "player":
      return { ...state, player: a.d, hasPlayer: true };
    case "velocity":
      return { ...state, velocityLabel: a.label };
    case "device": {
      if (a.d.device === "launchkey" || a.d.device === "xr18") {
        return { ...state, devices: { ...state.devices, [a.d.device]: a.d.state ?? "unknown" } };
      }
      return {
        ...state,
        devices: {
          launchkey: a.d.launchkey ?? state.devices.launchkey,
          xr18: a.d.xr18 ?? state.devices.xr18,
        },
      };
    }
  }
}

export interface Polyclav {
  connected: boolean;
  state: PolyclavState;
  clips: Clip[] | null;
  /** SSE "note" sink registration for the velocity monitor. */
  noteSink: React.RefObject<((n: NoteEvent) => void) | null>;
  selectPatch: (name: string) => void;
  /** Turn a board/master knob (display units, model param id). */
  setParam: (paramId: string, value: number) => void;
  /** Stomp a pedal. */
  togglePedal: (pedalId: string) => void;
  /** Reorder the pedals (display order). */
  reorder: (order: string[]) => void;
  /** Native-synth filter cutoff position (0..1). */
  setCutoff: (pos: number) => void;
  /** Locally reflect a velocity-curve label change (from VelocityCard). */
  setVelocityLabel: (label: string) => void;
  /** Reflect a player-state change (from TransportCard). */
  setPlayer: (p: PlayerState) => void;
}

/**
 * The live data spine for the pedalboard dashboard: one reducer fed by the SSE
 * stream, optimistic local edits with a per-param echo guard, and debounced,
 * endpoint-batched writes back through lib/pedalboard/wiring.ts. Falls back to
 * local-only chain state when /api/chain is absent (older daemon).
 */
export function usePolyclav(): Polyclav {
  const [state, dispatch] = useReducer(reducer, initialState);
  const clipsRef = useRef<Clip[] | null>(null);
  const clipsRequested = useRef(false);
  const [, force] = useReducer((n: number) => n + 1, 0);
  const noteSink = useRef<((n: NoteEvent) => void) | null>(null);

  // Latest-state refs for the send/guard machinery (avoid stale closures).
  const enabledRef = useRef(state.enabled);
  enabledRef.current = state.enabled;
  const valuesRef = useRef(state.chainValues);
  valuesRef.current = state.chainValues;

  const guard = useRef<Record<string, number>>({});
  const pending = useRef<Map<string, number>>(new Map());
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

  const now = () => (typeof performance !== "undefined" ? performance.now() : 0);
  // Stable so the send callbacks can list it as a dependency.
  const touch = useCallback((id: string) => {
    guard.current[id] = (typeof performance !== "undefined" ? performance.now() : 0) + GUARD_MS;
  }, []);
  const guarded = (id: string) => now() < (guard.current[id] ?? 0);
  // A soft-bypass pedal (comp/reverb) stomped off holds its engine param at 0
  // while the UI keeps the stored display value; ignore the resulting 0 echo so
  // re-enabling restores the real setting (chain pedals park server-side, so
  // this only matters for the /api/params + /api/mastering routed params).
  const parked = (id: string) => {
    const pedalId = id.split(".")[0];
    return Boolean(SOFT_BYPASS_PARAMS[pedalId]) && !(enabledRef.current[pedalId] ?? true);
  };

  const flush = useCallback(() => {
    const byEndpoint: Record<Endpoint, Record<string, number>> = {
      params: {},
      mastering: {},
      chain: {},
    };
    for (const [id, disp] of pending.current) {
      const w = PARAM_WIRING[id];
      if (!w) continue; // UI-only param
      const pedalId = id.split(".")[0];
      // comp/reverb have no engine enable: while bypassed keep the engine at 0.
      if (SOFT_BYPASS_PARAMS[pedalId] && !(enabledRef.current[pedalId] ?? true)) continue;
      byEndpoint[w.endpoint][w.field] = w.toEngine(disp);
    }
    pending.current.clear();
    if (Object.keys(byEndpoint.params).length) api.patchParams(byEndpoint.params);
    if (Object.keys(byEndpoint.mastering).length) api.patchMastering(byEndpoint.mastering);
    if (Object.keys(byEndpoint.chain).length)
      api.patchChain(byEndpoint.chain as Record<string, number>);
  }, []);

  const scheduleSend = useCallback(
    (id: string, value: number) => {
      pending.current.set(id, value);
      if (timer.current) clearTimeout(timer.current);
      timer.current = setTimeout(flush, SEND_MS);
    },
    [flush],
  );

  // Drop any queued debounced write (its value belongs to the old patch).
  const cancelPending = useCallback(() => {
    pending.current.clear();
    if (timer.current) clearTimeout(timer.current);
  }, []);

  // Never let a queued flush fire after the component unmounts.
  useEffect(() => () => cancelPending(), [cancelPending]);

  const connected = useSSE("/api/events", {
    snapshot: (d) => dispatch({ t: "snapshot", s: d as Status }),
    params: (d) => {
      const e = d as ParamsEvent;
      if (e.field === "cutoff") {
        if (!guarded("cutoff")) dispatch({ t: "cutoff", pos: e.pos, hz: e.hz });
        return;
      }
      const id = WIRE_BY_BACKEND[`params:${e.field}`];
      const w = id ? PARAM_WIRING[id] : undefined;
      if (id && w && typeof e.value === "number" && !guarded(id) && !parked(id)) {
        dispatch({ t: "values", patch: { [id]: w.fromEngine(e.value) } });
      }
    },
    mastering: (d) => {
      const e = d as MasteringEvent;
      const patch: Record<string, number> = {};
      for (const [field, val] of Object.entries(e)) {
        if (typeof val !== "number") continue;
        const id = WIRE_BY_BACKEND[`mastering:${field}`];
        const w = id ? PARAM_WIRING[id] : undefined;
        if (id && w && !guarded(id) && !parked(id)) patch[id] = w.fromEngine(val);
      }
      if (Object.keys(patch).length) dispatch({ t: "values", patch });
    },
    chain: (d) => {
      const e = d as ChainEvent;
      if (e.field.endsWith(".enabled")) {
        const stage = e.field.slice(0, -".enabled".length);
        const pedalId = STAGE_TO_PEDAL[stage] ?? stage;
        if (typeof e.value === "boolean") dispatch({ t: "enable", pedalId, on: e.value });
        return;
      }
      const id = WIRE_BY_BACKEND[`chain:${e.field}`];
      const w = id ? PARAM_WIRING[id] : undefined;
      if (id && w && typeof e.value === "number" && !guarded(id)) {
        dispatch({ t: "values", patch: { [id]: w.fromEngine(e.value) } });
      }
    },
    patch: (d) => {
      const e = d as PatchEvent & { chain?: Record<string, Record<string, number | boolean>> };
      dispatch({ t: "current", name: e.name });
      // A patch switch loads that patch's stored settings — clear the guards and
      // any pending debounced send (a queued edit belongs to the OLD patch) so
      // the fresh values always win, then apply everything the event carries.
      guard.current = {};
      cancelPending();
      const patch: Record<string, number> = {};
      const add = (endpoint: Endpoint, field: string, val: unknown) => {
        if (typeof val !== "number") return;
        const id = WIRE_BY_BACKEND[`${endpoint}:${field}`];
        const w = id ? PARAM_WIRING[id] : undefined;
        if (id && w) patch[id] = w.fromEngine(val);
      };
      add("params", "volume", e.volume);
      add("params", "reverb", e.reverb);
      add("params", "compressor", e.compressor);
      // comp/reverb have no per-patch engine enable; the engine loads the new
      // patch's real reverb/compressor values, so un-park their client stomp.
      dispatch({ t: "enable", pedalId: "comp", on: true });
      dispatch({ t: "enable", pedalId: "reverb", on: true });
      if (typeof e.cutoff_pos === "number" || typeof e.cutoff_hz === "number") {
        dispatch({ t: "cutoff", pos: e.cutoff_pos, hz: e.cutoff_hz });
      }
      // The daemon folds the chain nested by stage:
      // {chorus: {enabled, rate_hz, depth, mix}, delay: {...}, …}.
      if (e.chain) {
        for (const [stage, block] of Object.entries(e.chain)) {
          if (typeof block !== "object" || block === null) continue;
          for (const [leaf, val] of Object.entries(block)) {
            if (leaf === "enabled") {
              if (typeof val === "boolean")
                dispatch({ t: "enable", pedalId: STAGE_TO_PEDAL[stage] ?? stage, on: val });
            } else {
              add("chain", `${stage}.${leaf}`, val);
            }
          }
        }
      }
      if (e.synth) dispatch({ t: "setSynth", synth: e.synth });
      if (Object.keys(patch).length) dispatch({ t: "values", patch });
    },
    synth: (d) => dispatch({ t: "synth", d: d as SynthEvent }),
    player: (d) => dispatch({ t: "player", d: d as PlayerState }),
    velocity: (d) => {
      const c = (d as VelocityEvent).curve;
      if (typeof c === "string") dispatch({ t: "velocity", label: c });
    },
    note: (d) => {
      const n = d as NoteEvent;
      if (typeof n.in === "number" && typeof n.out === "number") noteSink.current?.(n);
    },
    device: (d) => dispatch({ t: "device", d: d as DeviceEvent }),
  });

  // Seed the chain registry once (schema + per-patch values + enables + order).
  // Absent endpoint (older daemon) leaves the local defaults in place.
  useEffect(() => {
    if (!connected) return;
    let alive = true;
    api.chain().then((c) => {
      if (alive && c) dispatch({ t: "chainSnapshot", c });
    });
    return () => {
      alive = false;
    };
  }, [connected]);

  // Restore the display order from localStorage on mount.
  useEffect(() => {
    const raw = window.localStorage.getItem(ORDER_KEY);
    if (!raw) return;
    try {
      const stored = JSON.parse(raw) as string[];
      if (Array.isArray(stored)) dispatch({ t: "order", order: stored });
    } catch {
      // ignore a corrupt entry
    }
  }, []);

  // Clip list, fetched once the snapshot reports a player.
  useEffect(() => {
    if (state.hasPlayer && !clipsRequested.current) {
      clipsRequested.current = true;
      api.clips().then((c) => {
        clipsRef.current = c;
        force();
      });
    }
  }, [state.hasPlayer]);

  const selectPatch = useCallback(
    (name: string) => {
      cancelPending();
      dispatch({ t: "current", name });
      api.selectPatch(name);
    },
    [cancelPending],
  );

  const setParam = useCallback(
    (id: string, value: number) => {
      touch(id);
      dispatch({ t: "values", patch: { [id]: value } });
      scheduleSend(id, value);
    },
    [scheduleSend, touch],
  );

  const setCutoff = useCallback(
    (pos: number) => {
      touch("cutoff");
      dispatch({ t: "cutoff", pos });
      api.patchParams({ cutoff_pos: pos });
    },
    [touch],
  );

  const togglePedal = useCallback((pedalId: string) => {
    const on = !(enabledRef.current[pedalId] ?? true);
    dispatch({ t: "enable", pedalId, on });
    const stage = STAGE_ENABLE[pedalId];
    if (stage) {
      api.patchChain({ [`${stage}.enabled`]: on });
      return;
    }
    // Soft bypass (comp/reverb): push live params to 0 or restore the stored value.
    for (const paramId of SOFT_BYPASS_PARAMS[pedalId] ?? []) {
      const w = PARAM_WIRING[paramId];
      if (!w) continue;
      const engine = on ? w.toEngine(valuesRef.current[paramId] ?? 0) : 0;
      const body = { [w.field]: engine };
      if (w.endpoint === "mastering") api.patchMastering(body);
      else if (w.endpoint === "params") api.patchParams(body);
    }
  }, []);

  const reorder = useCallback((order: string[]) => {
    const next = normalizeOrder(order, DEFAULT_PEDAL_ORDER);
    dispatch({ t: "order", order: next });
    window.localStorage.setItem(ORDER_KEY, JSON.stringify(next));
    // Best-effort: sync the chain-stage subset the daemon knows about.
    const subset = next.filter((id) => STAGE_ENABLE[id]).map((id) => STAGE_ENABLE[id]);
    if (subset.length) api.patchChain({ order: subset });
  }, []);

  const setVelocityLabel = useCallback((label: string) => dispatch({ t: "velocity", label }), []);
  const setPlayer = useCallback((p: PlayerState) => dispatch({ t: "player", d: p }), []);

  return {
    connected,
    state,
    clips: clipsRef.current,
    noteSink,
    selectPatch,
    setParam,
    togglePedal,
    reorder,
    setCutoff,
    setVelocityLabel,
    setPlayer,
  };
}
