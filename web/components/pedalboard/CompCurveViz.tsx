/**
 * Comp's signature module: the live compression transfer curve. Input level
 * (−48..0 dBFS) runs left→right against a unity diagonal; the accent curve is
 * the engine's own law keyed to the Comp amount — as it turns up the threshold
 * slides left, makeup lifts the whole line, and the upper segment flattens
 * toward the fixed 4:1 slope. The heat wedge between the curve and unity is
 * literally the gain being removed. At Comp 0 the curve collapses onto unity —
 * the bit-exact bypass the gate promises (reference: DriveCurveViz idiom,
 * law from audio-core/src/dsp/compressor.rs).
 */

/** Fixed engine character (compressor.rs): 4:1, 6 dB soft knee. */
const RATIO = 4;
const KNEE_DB = 6;
const SLOPE = 1 - 1 / RATIO;

/** Input dBFS (−48..0) → viewBox x (4..114). */
function xOfDb(db: number): number {
  return 4 + ((db + 48) / 48) * 110;
}
/** Level dBFS (−48..+6) → viewBox y (22..4), clamped into the band. */
function yOfDb(db: number): number {
  const y = 22 - ((db + 48) / 54) * 18;
  return y < 3 ? 3 : y > 23 ? 23 : y;
}

export interface CompCurveVizProps {
  /** Comp amount, 0..100 (the raw `comp.amount` param value). */
  amount: number;
}

export function CompCurveViz({ amount }: CompCurveVizProps) {
  const a = amount / 100;
  // amount folds threshold + makeup, matching compressor.rs's single knob.
  const threshold = 6 - 30 * a; // +6 dBFS (nothing compresses) → −24 dBFS
  const makeup = 9 * a; // up to +9 dB restored
  const kneeHalf = KNEE_DB / 2;

  let curve = "";
  for (let db = -48; db <= 0.001; db += 2) {
    const over = db - threshold;
    let gr: number;
    if (over <= -kneeHalf) gr = 0;
    else if (over >= kneeHalf) gr = SLOPE * over;
    else gr = (SLOPE * (over + kneeHalf) * (over + kneeHalf)) / (2 * KNEE_DB); // soft knee
    const out = db - gr + makeup;
    curve += `${db === -48 ? "M" : " L"}${xOfDb(db).toFixed(0)} ${yOfDb(out).toFixed(2)}`;
  }
  const heat = (0.06 + a * 0.5).toFixed(3);
  const close = `L${xOfDb(0).toFixed(0)} ${yOfDb(0).toFixed(2)} L${xOfDb(-48).toFixed(0)} ${yOfDb(-48).toFixed(2)} Z`;
  return (
    <svg viewBox="0 0 118 26" aria-hidden="true">
      <line className="pb-axis" x1={xOfDb(-48)} y1={yOfDb(-48)} x2={xOfDb(0)} y2={yOfDb(0)} />
      <path className="pb-fillpath" d={`${curve} ${close}`} style={{ opacity: heat }} />
      <path d={curve} strokeWidth="1.5" strokeLinejoin="round" />
    </svg>
  );
}
