/**
 * Pure pedal-order helpers for the reorder bar. Order is a list of pedal ids;
 * these move one id relative to another (drag) or by a step (keyboard). The
 * DSP signal path is fixed in the Rust engine, so this is the editing/display
 * arrangement — see docs/PEDALBOARD_UI.md and lib/pedalboard/model.ts.
 */

/** Move `dragId` to sit just before (`after=false`) or after (`after=true`) `targetId`. */
export function moveRelative(
  order: string[],
  dragId: string,
  targetId: string,
  after: boolean,
): string[] {
  if (dragId === targetId) return order;
  const without = order.filter((id) => id !== dragId);
  let at = without.indexOf(targetId);
  if (at < 0) return order;
  if (after) at += 1;
  without.splice(at, 0, dragId);
  return without;
}

/** Shift `id` by `delta` positions, clamped to the ends (keyboard reorder). */
export function moveBy(order: string[], id: string, delta: number): string[] {
  const from = order.indexOf(id);
  if (from < 0) return order;
  const to = Math.max(0, Math.min(order.length - 1, from + delta));
  if (to === from) return order;
  const next = order.slice();
  const [moved] = next.splice(from, 1);
  next.splice(to, 0, moved);
  return next;
}

/**
 * Reconcile a persisted/echoed order against the known pedal ids: keep known
 * ids in their given order, drop unknown ones, and append any pedal the stored
 * order never mentioned (so a newly-added pedal shows up at the end rather than
 * vanishing).
 */
export function normalizeOrder(order: string[], known: string[]): string[] {
  const knownSet = new Set(known);
  const seen = new Set<string>();
  const out: string[] = [];
  for (const id of order) {
    if (knownSet.has(id) && !seen.has(id)) {
      out.push(id);
      seen.add(id);
    }
  }
  for (const id of known) if (!seen.has(id)) out.push(id);
  return out;
}
