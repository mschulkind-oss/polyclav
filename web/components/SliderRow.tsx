"use client";

import { useEffect, useRef, useState } from "react";
import { fmt2 } from "@/lib/format";

interface SliderRowProps {
  label: string;
  min: number;
  max: number;
  step: number;
  /** Server-side value (from snapshot/SSE). Undefined renders a "–" readout. */
  value: number | undefined;
  /** Readout formatter for the (local or server) numeric value. */
  format?: (v: number) => string;
  /** Fixed readout text overriding format — e.g. cutoff shows the SSE-fed Hz. */
  valueText?: string;
  disabled?: boolean;
  /** Bare mode: emit just input + readout (grid cells), no row wrapper/label. */
  bare?: boolean;
  /** Debounced (100ms) sender — called with the latest dragged value. */
  onSend: (v: number) => void;
}

const HOLD_MS = 400; // drag guard: ignore SSE echoes this long after the last input
const SEND_MS = 100; // debounce for the PATCH sender

/**
 * SliderRow is the one slider widget: label | range input | readout.
 * It owns both interaction policies from the interim dashboard — the
 * 100ms debounced send and the drag guard (while the user is moving the
 * thumb, and for 400ms after, the local value wins over server echoes so
 * SSE can't yank the thumb backwards mid-drag).
 */
export function SliderRow({
  label,
  min,
  max,
  step,
  value,
  format = fmt2,
  valueText,
  disabled,
  bare,
  onSend,
}: SliderRowProps) {
  const [local, setLocal] = useState<number | null>(null);
  const holdTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const sendTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const onSendRef = useRef(onSend);
  onSendRef.current = onSend;

  useEffect(
    () => () => {
      clearTimeout(holdTimer.current);
      clearTimeout(sendTimer.current);
    },
    [],
  );

  const shown = local ?? value;
  const readout = valueText ?? (shown === undefined ? "–" : format(shown));

  const input = (
    <input
      type="range"
      name={label}
      min={min}
      max={max}
      step={step}
      disabled={disabled}
      title={bare ? label : undefined}
      value={shown ?? min}
      onChange={(e) => {
        const v = e.currentTarget.valueAsNumber;
        if (Number.isNaN(v)) return;
        setLocal(v);
        clearTimeout(holdTimer.current);
        holdTimer.current = setTimeout(() => setLocal(null), HOLD_MS);
        clearTimeout(sendTimer.current);
        sendTimer.current = setTimeout(() => onSendRef.current(v), SEND_MS);
      }}
      aria-label={label}
    />
  );

  if (bare) {
    return (
      <>
        {input}
        <span className="val">{readout}</span>
      </>
    );
  }
  return (
    <div className={`row${disabled ? " disabled" : ""}`}>
      <span className="rowlabel">{label}</span>
      {input}
      <span className="val">{readout}</span>
    </div>
  );
}
