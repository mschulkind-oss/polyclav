/**
 * Mastering comp's signature module: the live transfer curve of the master-bus
 * compressor with the brick-wall limiter behind it. Input level (−48..0 dBFS)
 * runs left→right against a unity diagonal; the accent curve is the engine's
 * own law keyed to Glue — the amount drops the threshold below the limiter
 * Ceiling, makeup is tied to that drop, and the Ceiling is both the knee
 * origin and the limiter's flat brick wall. The heat wedge between the curve
 * and the ceiling is literally the headroom being consumed. At Glue 0 the
 * curve collapses onto unity up to the ceiling (reference: CompCurveViz idiom,
 * law from audio-core/src/dsp/mastering.rs).
 */

/** Ceiling clamps to [−12, 0]; the band top gives makeup and the ceiling room. */
const TOP_DB = 6;
const MIN_DB = -48;
const BAND_DB = TOP_DB - MIN_DB;

/** Soft-knee ceiling (mastering.rs MAX_KNEE_DB). */
const MAX_KNEE_DB = 6;

/** Input dBFS (−48..0) → viewBox x (4..114). */
function xOfDb(db: number): number {
  return 4 + ((db + 48) / 48) * 110;
}
/** Level dBFS (−48..+6) → viewBox y (22..4), clamped into the band. */
function yOfDb(db: number): number {
  const y = 22 - ((db - MIN_DB) / BAND_DB) * 18;
  return y < 3 ? 3 : y > 23 ? 23 : y;
}

/** Gain meter strip under the plot: 24 dB spans its full width. */
const METER_X = 4;
const METER_Y = 23.6;
const METER_W = 110;
const METER_H = 1.6;
const METER_MAX_DB = 24;

export interface MasteringCurveVizProps {
  /** Master-bus compressor amount, 0..100 (`comp.glue`). */
  glue: number;
  /** Compression ratio, 1..20 (`comp.mstr_ratio`). */
  ratio: number;
  /** Threshold drop below the ceiling at Glue 100, in dB (`comp.mstr_range`). */
  rangeDb: number;
  /** Headroom the fixed makeup leaves under the ceiling, in dB. */
  headroomDb: number;
  /** Limiter ceiling, −12..0 dBFS (`master.ceiling`). */
  ceilingDb: number;
  /** Live mastering input peak in dBFS, when available. */
  inDb?: number;
  /** Live mastering output peak in dBFS, when available. */
  outDb?: number;
  /** Live net (worst-case) mastering gain in dB, when available. */
  gainDb?: number;
  /** Live limiter gain in dB — drives the brick-wall emphasis, when available. */
  limiterDb?: number;
}

export function MasteringCurveViz({
  glue,
  ratio,
  rangeDb,
  headroomDb,
  ceilingDb,
  inDb,
  outDb,
  gainDb,
  limiterDb,
}: MasteringCurveVizProps) {
  const a = glue / 100;
  const windowDb = rangeDb * a;
  const threshold = ceilingDb - windowDb;
  const kneeDb = Math.min(windowDb, MAX_KNEE_DB);
  const kneeHalf = kneeDb / 2;
  const slope = 1 - 1 / ratio;
  const makeup = Math.max(windowDb * slope - headroomDb, 0);

  // Soft-knee gain reduction for `over` dB above threshold (mastering.rs).
  const grAt = (over: number): number => {
    if (over <= -kneeHalf) return 0;
    if (over >= kneeHalf) return slope * over;
    return (slope * (over + kneeHalf) * (over + kneeHalf)) / (2 * kneeDb);
  };
  const outAt = (db: number): number => db - grAt(db - threshold) + makeup;

  let curve = "";
  for (let db = MIN_DB; db <= 0.001; db += 2) {
    const out = outAt(db);
    curve += `${db === MIN_DB ? "M" : " L"}${xOfDb(db).toFixed(0)} ${yOfDb(out).toFixed(2)}`;
  }
  // Close the wedge along the ceiling — the headroom the compressor consumes.
  const ceilingY = yOfDb(ceilingDb).toFixed(2);
  const close = `L${xOfDb(0).toFixed(0)} ${ceilingY} L${xOfDb(MIN_DB).toFixed(0)} ${ceilingY} Z`;
  const heat = (0.06 + a * 0.5).toFixed(3);

  // Brick wall: the limiter holds every input above the ceiling at the ceiling.
  const limit = `M${xOfDb(ceilingDb).toFixed(2)} ${ceilingY} L${xOfDb(0).toFixed(0)} ${ceilingY}`;
  const kneeY = yOfDb(ceilingDb - 3).toFixed(2);
  const limiting = typeof limiterDb === "number" && Number.isFinite(limiterDb) && limiterDb < -0.05;

  const showOp =
    typeof inDb === "number" &&
    Number.isFinite(inDb) &&
    typeof outDb === "number" &&
    Number.isFinite(outDb);

  const grWidth =
    typeof gainDb === "number" && Number.isFinite(gainDb)
      ? Math.min(Math.abs(gainDb) / METER_MAX_DB, 1) * METER_W
      : 0;

  return (
    <svg viewBox="0 0 118 26" aria-hidden="true">
      <line className="pb-axis" x1={xOfDb(MIN_DB)} y1={yOfDb(MIN_DB)} x2={xOfDb(0)} y2={yOfDb(0)} />
      <path d={curve} strokeWidth="1.5" strokeLinejoin="round" />
      <path className="pb-fillpath" d={`${curve} ${close}`} style={{ opacity: heat }} />
      <path className="pb-limit" d={limit} opacity={limiting ? 1 : 0.55} />
      <line
        className="pb-knee"
        x1={xOfDb(MIN_DB)}
        y1={kneeY}
        x2={xOfDb(0)}
        y2={kneeY}
        strokeDasharray="2 2"
      />
      {showOp ? (
        <circle
          className="pb-op-point"
          cx={xOfDb(inDb).toFixed(2)}
          cy={yOfDb(outDb).toFixed(2)}
          r="1.6"
        />
      ) : null}
      <rect
        className="pb-gr-track"
        x={METER_X}
        y={METER_Y}
        width={METER_W}
        height={METER_H}
        rx="0.6"
      />
      <rect className="pb-gr" x={METER_X} y={METER_Y} width={grWidth} height={METER_H} rx="0.6" />
    </svg>
  );
}
