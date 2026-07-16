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

// ---- /api/chain (post-synth pedal registry: schema + live values) -------
// Emitted by GET /api/chain; deltas arrive as SSE "chain" events
// {field, value, patch}. Engine units (0..1, Hz, ms); the UI converts via
// lib/pedalboard/wiring.ts.

export interface ChainParamState {
  id: string;
  label: string;
  unit: string;
  min: number;
  max: number;
  default: number;
  step: number;
  taper: string;
  gate: boolean;
  value: number;
}

export interface ChainStageState {
  id: string;
  label: string;
  kind: string;
  enabled: boolean;
  params: ChainParamState[];
}

export interface ChainState {
  patch: string;
  order: string[];
  stages: ChainStageState[];
}

/** "chain": {field:"chorus.mix"|"chorus.enabled", value:number|boolean, patch}
 * for params/enables, or {field:"order", order:string[]} for a reorder. */
export interface ChainEvent {
  field: string;
  value?: number | boolean;
  patch?: string;
  order?: string[];
}

// ---- /api/macros (8 macro-slot assignments; SSE "macros") ---------------

/** One macro slot assignment — the daemon stores these; the web drives the
 * target board param live. min/max are percent-of-range [0,100] — a sub-range
 * of the target's full span, not its display units. */
export interface Macro {
  slot: number; // 1..8
  target: string; // board param id (e.g. "delay.mix"); "" = unassigned
  name: string;
  min: number;
  max: number;
}

/** "macros": full assignment list on any change. */
export interface MacrosEvent {
  macros?: Macro[];
}

// ---- /api/hwmap (Launchkey knob-page reference; read-only) --------------

/** One knob page: its name + 8 slot labels ("" = an unbound knob). */
export interface HwPage {
  name: string;
  knobs: string[];
}

export interface HwMap {
  pages: HwPage[];
  /** Human descriptions of the non-knob surface (pad row, transport). */
  pads: string;
  transport: string;
  /** Note about surfaces not yet wired (Categories × Pages nav). */
  note: string;
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

// ---- MIDI devices panel (internal/web/mididevices.go) -------------------

/** "notes" (sending), "daw" (Launchkey control surface, never a note source),
 * "ignored" (in the ignore list), "restricted" (port_match set and this
 * port doesn't match it) -- mirrors internal/midi.PortStatus exactly. */
export type MIDIDeviceStatus = "notes" | "daw" | "ignored" | "restricted";

export interface MIDIDevice {
  name: string;
  status: MIDIDeviceStatus;
}

export interface MIDIDevicesResponse {
  devices: MIDIDevice[];
  match: string;
}

export interface MIDIDevicesPutResponse {
  ignore: string[];
  saved: boolean;
}

// ---- MIDI probe (internal/web/probe.go, internal/midiprobe) -------------
// Field names are camelCase here (probe.go's own JSON tags), unlike the
// rest of this file's daemon API which is snake_case — probe.go is a
// separate, newer surface that intentionally didn't inherit that
// convention (see docs/MIDI_PROBE.md).

export interface ProbePorts {
  ins: string[];
  outs: string[];
}

export interface ProbeStatus {
  active: boolean;
  inPort?: string;
  outPort?: string;
  startedAt?: string;
  eventCount: number;
  bufferCap: number;
  labeling: boolean;
  labelText?: string;
  labelEndsAt?: string;
}

/** "probe-event" SSE payload / GET /api/probe/events entries. */
export interface ProbeEvent {
  seq: number;
  time: string;
  port: string;
  kind: string;
  raw: string;
  channel?: number;
  data1?: number;
  data2?: number;
  bend?: number;
  label?: string;
}

export interface IdentityResult {
  requestSentAt: string;
  replyRaw?: string;
  receivedAt?: string;
  manufacturerId?: string;
  manufacturerName?: string;
  familyCode?: string;
  modelNumber?: string;
  versionBytes?: string;
  timedOut: boolean;
}

export interface DeviceProfile {
  exportedAt: string;
  inPort: string;
  outPort: string;
  allInPorts: string[];
  allOutPorts: string[];
  identity?: IdentityResult;
  events: ProbeEvent[];
  distinctLabels: string[];
}
