import type { Synth, SynthEvent, SynthField, SynthGroup } from "./types";

/**
 * applySynthEvent merges one SSE "synth" delta into the cached synth
 * block, following the hub's payload convention so future fields keep
 * working unmodified:
 *
 *   - array rows ({field:"osc", index, ...values}) merge the flat values
 *     into synth[field][index];
 *   - everything else carries the new value under its own field name
 *     ({field:"resonance", resonance: v} / {field:"filter_env",
 *     filter_env: {...}}) — objects merge, scalars replace.
 */
export function applySynthEvent(synth: Synth, ev: SynthEvent): Synth {
  const next: Synth = { ...synth };
  const cur = next[ev.field];

  if (Array.isArray(cur) && typeof ev.index === "number") {
    const { field: _field, index, ...values } = ev;
    if (index < 0 || index >= cur.length) return synth;
    const arr = cur.slice();
    arr[index] = { ...(arr[index] ?? {}), ...(values as SynthGroup) };
    next[ev.field] = arr;
    return next;
  }

  const v = (ev as Record<string, unknown>)[ev.field];
  if (v === undefined) return synth;
  if (
    cur !== undefined &&
    typeof cur === "object" &&
    !Array.isArray(cur) &&
    typeof v === "object" &&
    v !== null
  ) {
    next[ev.field] = { ...cur, ...(v as SynthGroup) };
  } else {
    next[ev.field] = v as SynthField;
  }
  return next;
}
