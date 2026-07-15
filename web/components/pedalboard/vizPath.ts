/**
 * Shared polyline builder for the signature-viz modules (reference `polyPath`):
 * samples `fn` every 2 SVG user units across 0..w and emits an "M x y L x y …"
 * path string. `mapX` lets a module remap the sample x into final viewBox
 * coordinates (the trem ghost squeezes samples 0..88 into 24..114); y values
 * always render with 2 decimals, matching the reference output exactly.
 */
export function polyPath(
  w: number,
  fn: (x: number) => number,
  mapX: (x: number) => number | string = (x) => x,
): string {
  let d = "";
  for (let x = 0; x <= w; x += 2) {
    d += `${x ? " L" : "M"}${mapX(x)} ${fn(x).toFixed(2)}`;
  }
  return d;
}

/**
 * Largest x coordinate in an absolute M/L path — the drawn x-extent. The
 * scrolling vizzes' seamless-loop canon requires extent ≥ viewBox width +
 * scroll period; their regression tests assert it with this.
 */
export function pathMaxX(d: string): number {
  let max = Number.NEGATIVE_INFINITY;
  for (const m of d.matchAll(/[ML]\s*(-?\d+(?:\.\d+)?)/g)) {
    max = Math.max(max, Number(m[1]));
  }
  return max;
}
