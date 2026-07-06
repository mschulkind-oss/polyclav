"use client";

import { type RefObject, useCallback, useEffect, useRef, useState } from "react";
import { api, errorMessage } from "@/lib/api";
import { fmt2 } from "@/lib/format";
import type { NoteEvent, VelocityPutResponse } from "@/lib/types";

// Velocity curve editor (docs/VELOCITY_CURVES.md "Live tweaking").
// Edits are staged locally and only hit the daemon on Apply (session) or
// Save (config write-back) — the explicit-save contract from the doc.

const PRESET_GAMMA: Record<string, number> = { soft: 0.6, linear: 1.0, hard: 1.6 };
const VP = 14; // canvas padding, px
const W = 340;
const H = 240;
const DOT_MS = 2000; // live monitor dots fade over 2s

type Mode = "gamma" | "points";

interface VelState {
  mode: Mode;
  curve: string; // preset name or "custom"
  gamma: number;
  outMin: number; // 0 = the daemon's defaults (1 / 127)
  outMax: number;
  points: [number, number][];
  drag: number; // control-point index being dragged, -1 when idle
  dots: { in: number; out: number; t: number }[];
  raf: number;
}

// velMap mirrors internal/velocity.Curve.Apply exactly (interpolate or
// power, round, clamp, floor 1) so the drawn curve IS the applied curve.
function velMap(s: VelState, v: number): number {
  if (v === 0) return 0;
  let out: number;
  if (s.mode === "points") {
    const pts = s.points;
    out = pts[pts.length - 1]?.[1] ?? 127;
    for (let i = 1; i < pts.length; i++) {
      const p1 = pts[i];
      const p0 = pts[i - 1];
      if (p1 && p0 && v <= p1[0]) {
        out = p0[1] + ((p1[1] - p0[1]) * (v - p0[0])) / (p1[0] - p0[0]);
        break;
      }
    }
    out = Math.round(out);
  } else {
    out = Math.round(127 * (v / 127) ** s.gamma);
  }
  out = Math.max(out, s.outMin || 1);
  out = Math.min(out, s.outMax || 127);
  return Math.max(out, 1);
}

const velXY = (inV: number, outV: number): [number, number] => [
  VP + (inV / 127) * (W - 2 * VP),
  H - VP - (outV / 127) * (H - 2 * VP),
];

const velFromXY = (x: number, y: number): [number, number] => {
  const inV = Math.round(((x - VP) / (W - 2 * VP)) * 127);
  const outV = Math.round(((H - VP - y) / (H - 2 * VP)) * 127);
  return [Math.min(127, Math.max(0, inV)), Math.min(127, Math.max(0, outV))];
};

function cssVar(name: string, fallback: string): string {
  const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  return v || fallback;
}

interface VelocityCardProps {
  /** Active curve label (from snapshot / SSE "velocity" events). */
  active: string;
  onActive: (label: string) => void;
  /** The page's SSE "note" handler calls through this ref (fading dots). */
  noteSink: RefObject<((n: NoteEvent) => void) | null>;
}

export function VelocityCard({ active, onActive, noteSink }: VelocityCardProps) {
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const velRef = useRef<VelState>({
    mode: "gamma",
    curve: "linear",
    gamma: 1.0,
    outMin: 0,
    outMax: 0,
    points: [
      [0, 0],
      [64, 64],
      [127, 127],
    ],
    drag: -1,
    dots: [],
    raf: 0,
  });

  // React mirrors of the bits the DOM renders; the canvas reads velRef.
  const [mode, setMode] = useState<Mode>("gamma");
  const [curve, setCurve] = useState("linear");
  const [gamma, setGamma] = useState(1.0);
  const [outMin, setOutMin] = useState(0);
  const [outMax, setOutMax] = useState(0);
  const [error, setError] = useState<string | null>(null);

  const draw = useCallback(() => {
    const canvas = canvasRef.current;
    const ctx = canvas?.getContext("2d");
    if (!ctx) return;
    const s = velRef.current;
    ctx.clearRect(0, 0, W, H);
    // grid
    ctx.strokeStyle = cssVar("--border", "#888");
    ctx.lineWidth = 1;
    ctx.globalAlpha = 0.6;
    for (const g of [0, 32, 64, 96, 127]) {
      const [x] = velXY(g, 0);
      const [, y] = velXY(0, g);
      ctx.beginPath();
      ctx.moveTo(x, VP);
      ctx.lineTo(x, H - VP);
      ctx.stroke();
      ctx.beginPath();
      ctx.moveTo(VP, y);
      ctx.lineTo(W - VP, y);
      ctx.stroke();
    }
    ctx.globalAlpha = 1;
    // curve
    const accent = cssVar("--accent", "#3d6bff");
    ctx.strokeStyle = accent;
    ctx.lineWidth = 2;
    ctx.beginPath();
    for (let v = 1; v <= 127; v++) {
      const [x, y] = velXY(v, velMap(s, v));
      if (v === 1) ctx.moveTo(x, y);
      else ctx.lineTo(x, y);
    }
    ctx.stroke();
    // draggable control points
    if (s.mode === "points") {
      for (let i = 0; i < s.points.length; i++) {
        const p = s.points[i];
        if (!p) continue;
        const [x, y] = velXY(p[0], p[1]);
        ctx.beginPath();
        ctx.arc(x, y, 5, 0, Math.PI * 2);
        ctx.fillStyle = i === s.drag ? cssVar("--ok", "#2fb35f") : accent;
        ctx.fill();
      }
    }
    // live monitor dots, fading over 2s
    const now = performance.now();
    ctx.fillStyle = cssVar("--ok", "#2fb35f");
    for (const d of s.dots) {
      const age = now - d.t;
      if (age > DOT_MS) continue;
      const [x, y] = velXY(d.in, d.out);
      ctx.globalAlpha = 1 - age / DOT_MS;
      ctx.beginPath();
      ctx.arc(x, y, 4, 0, Math.PI * 2);
      ctx.fill();
    }
    ctx.globalAlpha = 1;
  }, []);

  // Note dots + initial paint + theme-change repaint.
  useEffect(() => {
    const animate = () => {
      const s = velRef.current;
      s.dots = s.dots.filter((d) => performance.now() - d.t <= DOT_MS);
      draw();
      s.raf = s.dots.length ? requestAnimationFrame(animate) : 0;
    };
    noteSink.current = (n: NoteEvent) => {
      const s = velRef.current;
      s.dots.push({ in: n.in, out: n.out, t: performance.now() });
      if (s.dots.length > 64) s.dots.shift();
      if (!s.raf) s.raf = requestAnimationFrame(animate);
    };
    draw();
    const mq = window.matchMedia("(prefers-color-scheme: dark)");
    const onTheme = () => draw();
    mq.addEventListener("change", onTheme);
    return () => {
      noteSink.current = null;
      mq.removeEventListener("change", onTheme);
      if (velRef.current.raf) cancelAnimationFrame(velRef.current.raf);
      velRef.current.raf = 0;
    };
  }, [draw, noteSink]);

  const setModeBoth = (m: Mode) => {
    velRef.current.mode = m;
    setMode(m);
    draw();
  };

  const pickPreset = (name: string) => {
    const g = PRESET_GAMMA[name] ?? 1.0;
    const s = velRef.current;
    s.curve = name;
    s.gamma = g;
    s.mode = "gamma";
    setCurve(name);
    setGamma(g);
    setMode("gamma");
    draw();
  };

  const onGamma = (v: number) => {
    const s = velRef.current;
    s.gamma = v;
    s.curve = "custom"; // touching the slider leaves the preset
    s.mode = "gamma";
    setGamma(v);
    setCurve("custom");
    setMode("gamma");
    draw();
  };

  const onClamp = (which: "min" | "max", raw: string) => {
    const v = Math.max(0, Math.min(127, Number.parseInt(raw, 10) || 0));
    const s = velRef.current;
    if (which === "min") {
      s.outMin = v;
      setOutMin(v);
    } else {
      s.outMax = v;
      setOutMax(v);
    }
    draw();
  };

  // ---- canvas point editing ------------------------------------------------
  // Client-side constraints keep the staged points LOOSELY valid (first
  // pinned at [0,0], last x at 127, xs strictly increasing, ys
  // non-decreasing); the server re-validates on Apply/Save.

  const canvasPos = (e: React.PointerEvent | React.MouseEvent): [number, number] => {
    const canvas = canvasRef.current;
    if (!canvas) return [0, 0];
    const r = canvas.getBoundingClientRect();
    return [((e.clientX - r.left) * W) / r.width, ((e.clientY - r.top) * H) / r.height];
  };

  const hitPoint = (x: number, y: number): number => {
    const pts = velRef.current.points;
    for (let i = 0; i < pts.length; i++) {
      const p = pts[i];
      if (!p) continue;
      const [px, py] = velXY(p[0], p[1]);
      if (Math.hypot(px - x, py - y) <= 9) return i;
    }
    return -1;
  };

  const onPointerDown = (e: React.PointerEvent<HTMLCanvasElement>) => {
    const s = velRef.current;
    if (s.mode !== "points") return;
    e.preventDefault();
    const [x, y] = canvasPos(e);
    let i = hitPoint(x, y);
    if (i < 0) {
      if (s.points.length >= 16) return;
      const [inV, outV] = velFromXY(x, y);
      const at = s.points.findIndex((p) => p[0] >= inV);
      if (at <= 0 || s.points.some((p) => p[0] === inV)) return;
      const lo = s.points[at - 1]?.[1] ?? 0;
      const hi = s.points[at]?.[1] ?? 127;
      s.points.splice(at, 0, [inV, Math.min(Math.max(outV, lo), hi)]);
      i = at;
    }
    s.drag = i;
    e.currentTarget.setPointerCapture(e.pointerId);
    draw();
  };

  const onPointerMove = (e: React.PointerEvent<HTMLCanvasElement>) => {
    const s = velRef.current;
    if (s.mode !== "points" || s.drag < 0) return;
    const i = s.drag;
    const pts = s.points;
    if (i === 0) return; // [0,0] is fixed: NoteOn vel 0 must stay NoteOff
    const prev = pts[i - 1];
    if (!prev) return;
    const [x, y] = canvasPos(e);
    let [inV, outV] = velFromXY(x, y);
    if (i === pts.length - 1) {
      inV = 127; // last x pinned: full input coverage
    } else {
      const next = pts[i + 1];
      if (!next) return;
      inV = Math.min(Math.max(inV, prev[0] + 1), next[0] - 1);
    }
    const lo = prev[1];
    const hi = i === pts.length - 1 ? 127 : (pts[i + 1]?.[1] ?? 127);
    pts[i] = [inV, Math.min(Math.max(outV, lo), hi)];
    draw();
  };

  const onPointerUp = () => {
    velRef.current.drag = -1;
    draw();
  };

  const onDoubleClick = (e: React.MouseEvent<HTMLCanvasElement>) => {
    const s = velRef.current;
    if (s.mode !== "points") return;
    const [x, y] = canvasPos(e);
    const i = hitPoint(x, y);
    if (i > 0 && i < s.points.length - 1) {
      s.points.splice(i, 1);
      draw();
    }
  };

  // ---- apply / save ----------------------------------------------------------

  const body = (save: boolean): Record<string, unknown> => {
    const s = velRef.current;
    const b: Record<string, unknown> = {};
    if (s.mode === "points") {
      b.points = s.points.map((p) => [p[0], p[1]]);
    } else if (s.curve === "custom") {
      b.curve = "custom";
      b.gamma = s.gamma;
    } else {
      b.curve = s.curve;
    }
    if (s.outMin > 0) b.out_min = s.outMin;
    if (s.outMax > 0) b.out_max = s.outMax;
    if (save) b.save = true;
    return b;
  };

  const sendCurve = async (save: boolean) => {
    const r = await api.velocityPut(body(save));
    if (!r) {
      setError("request failed");
      return;
    }
    if (r.ok) {
      setError(null);
      const m = (await r.json()) as VelocityPutResponse;
      onActive(`${m.curve}${m.saved ? " · saved" : " · session"}`);
    } else {
      setError(await errorMessage(r));
    }
  };

  return (
    <>
      <div className="btnrow">
        {Object.keys(PRESET_GAMMA).map((name) => (
          <button
            key={name}
            type="button"
            className={mode === "gamma" && curve === name ? "active" : undefined}
            onClick={() => pickPreset(name)}
          >
            {name}
          </button>
        ))}
        <button
          type="button"
          className={mode === "points" ? "active" : undefined}
          onClick={() => setModeBoth("points")}
        >
          points
        </button>
      </div>
      <div className="row">
        <span className="rowlabel">Gamma</span>
        <input
          type="range"
          name="gamma"
          min={0.3}
          max={3}
          step={0.01}
          value={gamma}
          aria-label="Gamma"
          onChange={(e) => onGamma(e.currentTarget.valueAsNumber)}
        />
        <span className="val">{fmt2(gamma)}</span>
      </div>
      <div className="row">
        <span className="rowlabel">Out clamp</span>
        <span className="numpair">
          <input
            type="number"
            name="out_min"
            min={0}
            max={127}
            value={outMin}
            title="out_min (0 = default 1)"
            onChange={(e) => onClamp("min", e.currentTarget.value)}
          />
          <span>…</span>
          <input
            type="number"
            name="out_max"
            min={0}
            max={127}
            value={outMax}
            title="out_max (0 = default 127)"
            onChange={(e) => onClamp("max", e.currentTarget.value)}
          />
        </span>
        <span />
      </div>
      <canvas
        ref={canvasRef}
        className="vel-canvas"
        width={W}
        height={H}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={onPointerUp}
        onDoubleClick={onDoubleClick}
      />
      <div className="btnrow">
        <button type="button" onClick={() => sendCurve(false)}>
          Apply (session)
        </button>
        <button type="button" onClick={() => sendCurve(true)}>
          Save
        </button>
        <span className="vel-active">{active}</span>
      </div>
      {error ? <div className="errbox">{error}</div> : null}
      <p className="hint">
        Points mode: click to add, drag to move, double-click to remove. Played notes appear as
        fading dots at (in, out).
      </p>
      <p className="hint">
        Per-patch velocity overrides in polyclav.toml still win: switching patches re-resolves the
        curve from config, replacing session edits.
      </p>
    </>
  );
}
