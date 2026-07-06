"use client";

import { api } from "@/lib/api";
import { fmt2, fmtCents, fmtSec } from "@/lib/format";
import type { Synth, SynthField, SynthGroup, SynthLeaf } from "@/lib/types";
import { SelectField } from "./SelectField";
import { SliderRow } from "./SliderRow";

// ---- static schema hints -------------------------------------------------
//
// The synth card is SCHEMA-DRIVEN: it renders whatever params.synth
// carries, so params added to the daemon later appear here automatically.
// These maps only refine presentation for the fields we know about —
// slider ranges, formatters, enum options, labels. Unknown numeric fields
// fall back to a generic 0..1 slider; unknown strings render read-only.
// Keys are "field", "group.field", or "arrayField.field".

interface NumSpec {
  min: number;
  max: number;
  step: number;
  fmt: (v: number) => string;
}

const GENERIC: NumSpec = { min: 0, max: 1, step: 0.01, fmt: fmt2 };

const RANGES: Record<string, NumSpec> = {
  resonance: { min: 0, max: 0.95, step: 0.005, fmt: fmt2 },
  glide: { min: 0, max: 5, step: 0.01, fmt: fmtSec },
  noise: { min: 0, max: 1, step: 0.01, fmt: fmt2 },
  "filter_env.attack": { min: 0, max: 2, step: 0.001, fmt: fmtSec },
  "filter_env.decay": { min: 0, max: 2, step: 0.001, fmt: fmtSec },
  "filter_env.sustain": { min: 0, max: 1, step: 0.01, fmt: fmt2 },
  "filter_env.release": { min: 0, max: 3, step: 0.001, fmt: fmtSec },
  "filter_env.amount": { min: 0, max: 1, step: 0.01, fmt: fmt2 },
  "osc.detune_cents": { min: -100, max: 100, step: 1, fmt: fmtCents },
  "osc.level": { min: 0, max: 1, step: 0.01, fmt: fmt2 },
};

/** String fields rendered as selects; anything else read-only. */
const ENUMS: Record<string, string[]> = {
  "osc.wave": ["saw", "square", "pulse"],
};

/** Integer fields rendered as selects (value list in display order). */
const INT_SELECTS: Record<string, number[]> = {
  "osc.octave": [2, 1, 0, -1, -2],
};

const LABELS: Record<string, string> = {
  filter_env: "Filter envelope",
  "filter_env.amount": "Env amount",
  osc: "Oscillators",
};

/** Scalars listed here render first, in this order; unknowns follow in wire order. */
const SCALAR_ORDER = ["resonance", "glide", "noise"];

const humanize = (key: string): string => {
  const s = key.replace(/_/g, " ");
  return s.charAt(0).toUpperCase() + s.slice(1);
};

const label = (path: string, fallbackKey: string): string => LABELS[path] ?? humanize(fallbackKey);

const isLeaf = (v: SynthField | undefined): v is SynthLeaf =>
  typeof v === "number" || typeof v === "string";
const isGroup = (v: SynthField | undefined): v is SynthGroup =>
  typeof v === "object" && v !== null && !Array.isArray(v);

// ---- generic field renderers ----------------------------------------------

function NumField({
  path,
  name,
  value,
  send,
}: {
  path: string;
  name: string;
  value: number;
  send: (v: number) => void;
}) {
  const spec = RANGES[path] ?? GENERIC;
  return (
    <SliderRow
      label={label(path, name)}
      min={spec.min}
      max={spec.max}
      step={spec.step}
      value={value}
      format={spec.fmt}
      onSend={send}
    />
  );
}

function StrField({
  path,
  name,
  value,
  send,
}: {
  path: string;
  name: string;
  value: string;
  send: (v: string) => void;
}) {
  const options = ENUMS[path];
  if (!options) {
    // Unknown string field: display-only until a range-map entry teaches
    // the UI its options.
    return (
      <div className="row">
        <span className="rowlabel">{label(path, name)}</span>
        <span />
        <span className="val">{value}</span>
      </div>
    );
  }
  return (
    <div className="row">
      <span className="rowlabel">{label(path, name)}</span>
      <SelectField
        title={name}
        value={value}
        options={options.map((o) => ({ value: o, label: o }))}
        onSend={send}
      />
      <span />
    </div>
  );
}

// ---- the card ---------------------------------------------------------------

interface SynthCardProps {
  synth: Synth;
}

/**
 * Native-synth params, built from the /api/status params.synth JSON:
 * top-level scalars first (known order, then unknowns), then object
 * groups (filter envelope) as sub-sections, then arrays of objects (the
 * oscillators) as compact rows. PATCH bodies mirror the JSON shape:
 * {key: v}, {group: {key: v}}, {arr: [{index, key: v}]}.
 */
export function SynthCard({ synth }: SynthCardProps) {
  const keys = Object.keys(synth);
  const scalarKeys = [
    ...SCALAR_ORDER.filter((k) => isLeaf(synth[k])),
    ...keys.filter((k) => isLeaf(synth[k]) && !SCALAR_ORDER.includes(k)),
  ];
  const groupKeys = keys.filter((k) => isGroup(synth[k]));
  const arrayKeys = keys.filter((k) => Array.isArray(synth[k]));

  return (
    <>
      {scalarKeys.map((k) => {
        const v = synth[k];
        if (typeof v === "number") {
          return (
            <NumField
              key={k}
              path={k}
              name={k}
              value={v}
              send={(nv) => api.patchSynth({ [k]: nv })}
            />
          );
        }
        return (
          <StrField
            key={k}
            path={k}
            name={k}
            value={String(v)}
            send={(nv) => api.patchSynth({ [k]: nv })}
          />
        );
      })}

      {groupKeys.map((gk) => {
        const group = synth[gk] as SynthGroup;
        return (
          <div key={gk}>
            <h3>{label(gk, gk)}</h3>
            {Object.keys(group).map((sub) => {
              const v = group[sub];
              const path = `${gk}.${sub}`;
              if (typeof v === "number") {
                return (
                  <NumField
                    key={sub}
                    path={path}
                    name={sub}
                    value={v}
                    send={(nv) => api.patchSynth({ [gk]: { [sub]: nv } })}
                  />
                );
              }
              return (
                <StrField
                  key={sub}
                  path={path}
                  name={sub}
                  value={String(v)}
                  send={(nv) => api.patchSynth({ [gk]: { [sub]: nv } })}
                />
              );
            })}
          </div>
        );
      })}

      {arrayKeys.map((ak) => {
        const rows = synth[ak] as SynthGroup[];
        return (
          <div key={ak}>
            <h3>{label(ak, ak)}</h3>
            {rows.map((row, i) => (
              <OscRow
                // Rows are positional by wire contract (osc index 0..2).
                key={`${ak}${
                  // biome-ignore lint/suspicious/noArrayIndexKey: index IS the row identity
                  i
                }`}
                arrayKey={ak}
                index={i}
                row={row}
              />
            ))}
            {ak === "osc" ? (
              <p className="hint">Per-osc: wave · octave · detune (¢) · level.</p>
            ) : null}
          </div>
        );
      })}
    </>
  );
}

/**
 * One array-entry row (an oscillator). Enum and int-select fields render
 * inline in the compact grid; known numeric fields follow as
 * slider+readout pairs; unknown numeric fields get their own generic
 * rows below so a new per-osc param still shows up.
 */
function OscRow({ arrayKey, index, row }: { arrayKey: string; index: number; row: SynthGroup }) {
  const send = (field: string, v: SynthLeaf) =>
    api.patchSynth({ [arrayKey]: [{ index, [field]: v }] });

  const keys = Object.keys(row);
  const enumKeys = keys.filter((k) => ENUMS[`${arrayKey}.${k}`] && typeof row[k] === "string");
  const intKeys = keys.filter((k) => INT_SELECTS[`${arrayKey}.${k}`] && typeof row[k] === "number");
  const knownNums = keys.filter(
    (k) => typeof row[k] === "number" && RANGES[`${arrayKey}.${k}`] && !intKeys.includes(k),
  );
  const unknownNums = keys.filter(
    (k) => typeof row[k] === "number" && !RANGES[`${arrayKey}.${k}`] && !intKeys.includes(k),
  );

  return (
    <>
      <div className="oscrow">
        <span className="osclabel">{`${arrayKey.toUpperCase()} ${index + 1}`}</span>
        {enumKeys.map((k) => (
          <SelectField
            key={k}
            title={k}
            value={String(row[k])}
            options={(ENUMS[`${arrayKey}.${k}`] ?? []).map((o) => ({ value: o, label: o }))}
            onSend={(v) => send(k, v)}
          />
        ))}
        {intKeys.map((k) => (
          <SelectField
            key={k}
            title={k}
            value={String(row[k])}
            options={(INT_SELECTS[`${arrayKey}.${k}`] ?? []).map((n) => ({
              value: String(n),
              label: `${n > 0 ? "+" : ""}${n} oct`,
            }))}
            onSend={(v) => send(k, Number.parseInt(v, 10))}
          />
        ))}
        {knownNums.map((k) => (
          <OscSlider
            key={k}
            path={`${arrayKey}.${k}`}
            name={k}
            value={row[k] as number}
            send={send}
          />
        ))}
      </div>
      {unknownNums.map((k) => (
        <NumField
          key={k}
          path={`${arrayKey}.${k}`}
          name={`${arrayKey.toUpperCase()} ${index + 1} ${k}`}
          value={row[k] as number}
          send={(v) => send(k, v)}
        />
      ))}
    </>
  );
}

/** Compact slider+readout pair for the osc grid (no label column). */
function OscSlider({
  path,
  name,
  value,
  send,
}: {
  path: string;
  name: string;
  value: number;
  send: (field: string, v: number) => void;
}) {
  const spec = RANGES[path] ?? GENERIC;
  return (
    <SliderRow
      label={name}
      min={spec.min}
      max={spec.max}
      step={spec.step}
      value={value}
      format={spec.fmt}
      bare
      onSend={(v) => send(name, v)}
    />
  );
}
