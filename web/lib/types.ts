// Wire types for the polyclav daemon API. internal/web/server.go owns
// these JSON shapes (snake_case); keep field names in sync with its
// *JSON structs and the controls hub's published Change payloads.

export interface Devices {
  launchkey: string;
  xr18: string;
}

export interface Patch {
  name: string;
  display: string;
  type: string;
  pad_color: number;
  gain_db: number;
  index: number;
}

// params.synth is deliberately loose: the synth card renders whatever
// keys arrive (docs/WEB_UI.md — params added later appear automatically).
// Scalars are sliders/selects/toggles, objects are sub-sections, arrays
// of objects are per-row groups (the oscillators).
export type SynthLeaf = number | string | boolean;
export type SynthGroup = Record<string, SynthLeaf>;
export type SynthField = SynthLeaf | SynthGroup | SynthGroup[];
export type Synth = Record<string, SynthField>;

export interface Params {
  patch: string;
  patch_display: string;
  volume: number;
  reverb: number;
  compressor: number;
  cutoff_pos: number;
  cutoff_hz: number;
  mastering_comp: number;
  limiter_ceiling_db: number;
  velocity_curve: string;
  synth: Synth;
}

export interface PlayerState {
  playing: boolean;
  clip: string;
  loop: boolean;
  tempo: number;
}

export interface Clip {
  id: string;
  name: string;
  description: string;
  poly_only: boolean;
  beats: number;
  ref_bpm: number;
}

export interface Status {
  version: string;
  devices: Devices;
  params: Params;
  patches: Patch[];
  player: PlayerState | null;
}

// ---- SSE event payloads (controls hub Change.Data) ----------------------

/** "params": {field:"volume"|"reverb"|"compressor", value} or {field:"cutoff", pos, hz}. */
export interface ParamsEvent {
  field: string;
  value?: number;
  pos?: number;
  hz?: number;
  patch?: string;
}

/** "patch": emitted on every select; native patches also carry cutoff + synth. */
export interface PatchEvent {
  name: string;
  display?: string;
  volume?: number;
  reverb?: number;
  compressor?: number;
  cutoff_pos?: number;
  cutoff_hz?: number;
  synth?: Synth;
}

/**
 * "synth": {field, ...}. Scalars carry {[field]: value}, filter_env carries
 * {filter_env: {...}}, osc rows carry {index, wave, octave, ...} flat.
 */
export interface SynthEvent {
  field: string;
  index?: number;
  [key: string]: unknown;
}

export interface MasteringEvent {
  comp_amount?: number;
  limiter_ceiling_db?: number;
}

export interface VelocityEvent {
  curve?: string;
}

/** "note": throttled NoteOn monitor — raw and velocity-remapped values. */
export interface NoteEvent {
  in: number;
  out: number;
  note?: number;
}

/** "device": hub deltas are {device, state}; snapshots use {launchkey, xr18}. */
export interface DeviceEvent {
  device?: string;
  state?: string;
  launchkey?: string;
  xr18?: string;
}

export interface VelocityPutResponse {
  curve: string;
  saved: boolean;
}
