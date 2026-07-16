// Typed fetch wrappers over the daemon's REST API. All paths are
// same-origin absolute (no basePath prefix): in production the daemon
// serves both /app/ and /api/; in dev, next.config.ts rewrites /api/*
// to the daemon on 127.0.0.1:8666.

import type {
  ChainState,
  Clip,
  IdentityResult,
  MIDIDevicesResponse,
  PlayerState,
  ProbePorts,
  ProbeStatus,
  Status,
  VelocityPutResponse,
} from "./types";

async function request(method: string, url: string, body?: unknown): Promise<Response | null> {
  try {
    const r = await fetch(url, {
      method,
      headers: body !== undefined ? { "Content-Type": "application/json" } : undefined,
      body: body !== undefined ? JSON.stringify(body) : undefined,
    });
    if (!r.ok) console.warn(method, url, "->", r.status);
    return r;
  } catch (e) {
    console.warn(method, url, "failed:", e);
    return null;
  }
}

async function json<T>(r: Response | null): Promise<T | null> {
  if (!r?.ok) return null;
  try {
    return (await r.json()) as T;
  } catch {
    return null;
  }
}

/** Extract the {"error": ...} message from a non-2xx response body. */
export async function errorMessage(r: Response): Promise<string> {
  const m = await r
    .json()
    .then((v) => (v as { error?: string }).error)
    .catch(() => undefined);
  return m || `error ${r.status}`;
}

export const api = {
  status: async (): Promise<Status | null> => json<Status>(await request("GET", "/api/status")),

  selectPatch: (name: string): Promise<Response | null> =>
    request("POST", `/api/patches/${encodeURIComponent(name)}/select`),

  /** PATCH /api/params — partial body, e.g. {volume: 0.8} or {cutoff_pos: 0.5}. */
  patchParams: (body: Record<string, number>): Promise<Response | null> =>
    request("PATCH", "/api/params", body),

  /** PATCH /api/synth — partial body shaped by the schema-driven synth card. */
  patchSynth: (body: unknown): Promise<Response | null> => request("PATCH", "/api/synth", body),

  patchMastering: (body: Record<string, number>): Promise<Response | null> =>
    request("PATCH", "/api/mastering", body),

  /** GET /api/chain — post-synth pedal registry + live values; null if the
   * daemon predates the endpoint (the board degrades to local-only chain state). */
  chain: async (): Promise<ChainState | null> =>
    json<ChainState>(await request("GET", "/api/chain")),

  /** PATCH /api/chain — flat body, e.g. {"chorus.mix":0.3} / {"delay.enabled":false} /
   * {"order":["delay","chorus",...]}. Engine units; see lib/pedalboard/wiring.ts. */
  patchChain: (body: Record<string, number | boolean | string[]>): Promise<Response | null> =>
    request("PATCH", "/api/chain", body),

  clips: async (): Promise<Clip[] | null> => json<Clip[]>(await request("GET", "/api/clips")),

  playerPlay: async (clip: string, loop: boolean, tempo: number): Promise<PlayerState | null> =>
    json<PlayerState>(await request("POST", "/api/player", { clip, loop, tempo })),

  playerStop: async (): Promise<PlayerState | null> =>
    json<PlayerState>(await request("POST", "/api/player/stop")),

  playerTempo: (tempo: number): Promise<Response | null> =>
    request("POST", "/api/player/tempo", { tempo }),

  /** GET /api/config — the TOML text, or null when unavailable. */
  configGet: async (): Promise<string | null> => {
    const r = await request("GET", "/api/config");
    if (!r?.ok) return null;
    return r.text();
  },

  /** PUT /api/config — full TOML text; returns the raw Response (422 carries the validation error). */
  configPut: async (toml: string): Promise<Response | null> => {
    try {
      return await fetch("/api/config", {
        method: "PUT",
        headers: { "Content-Type": "text/plain; charset=utf-8" },
        body: toml,
      });
    } catch (e) {
      console.warn("PUT /api/config failed:", e);
      return null;
    }
  },

  /** PUT /api/velocity — returns the raw Response so callers can show 400/409 bodies. */
  velocityPut: (body: unknown): Promise<Response | null> => request("PUT", "/api/velocity", body),

  /** GET /api/midi/devices — every currently-connected port + its classification. */
  midiDevices: async (): Promise<MIDIDevicesResponse | null> =>
    json<MIDIDevicesResponse>(await request("GET", "/api/midi/devices")),

  /** PUT /api/midi/devices {ignore, save} — returns the raw Response so callers can show 404/409/503 bodies. */
  midiDevicesPut: (ignore: string[], save: boolean): Promise<Response | null> =>
    request("PUT", "/api/midi/devices", { ignore, save }),

  // ---- MIDI probe (docs/MIDI_PROBE.md) ----------------------------------

  probePorts: async (): Promise<ProbePorts | null> =>
    json<ProbePorts>(await request("GET", "/api/probe/ports")),

  probeStatus: async (): Promise<ProbeStatus | null> =>
    json<ProbeStatus>(await request("GET", "/api/probe/status")),

  /** Returns the raw Response so callers can show 404/409 bodies (unknown port / already connected). */
  probeConnect: (inPort: string, outPort: string, bufferCap?: number): Promise<Response | null> =>
    request("POST", "/api/probe/connect", { inPort, outPort, bufferCap }),

  probeDisconnect: (): Promise<Response | null> => request("POST", "/api/probe/disconnect"),

  /** Returns the raw Response so callers can show 400/409 bodies (empty label / already labeling / not connected). */
  probeLabel: (label: string, windowMs: number): Promise<Response | null> =>
    request("POST", "/api/probe/label", { label, windowMs }),

  probeIdentity: async (timeoutMs: number): Promise<IdentityResult | null> =>
    json<IdentityResult>(await request("POST", "/api/probe/identity", { timeoutMs })),

  /** Returns the raw Response so callers can show 400/409 bodies (bad hex / not connected). */
  probeSend: (hex: string): Promise<Response | null> => request("POST", "/api/probe/send", { hex }),

  /** GET /api/probe/export is a plain download link (server sets Content-Disposition), not fetched here. */
};

export type { MIDIDevicesResponse, VelocityPutResponse };
