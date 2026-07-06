"use client";

import { useEffect, useRef, useState } from "react";

interface SelectFieldProps {
  /** Server-side value; the select snaps to it except right after a local pick. */
  value: string | undefined;
  options: { value: string; label: string }[];
  title?: string;
  /** Immediate (undebounced) sender — selects are discrete, not draggy. */
  onSend: (v: string) => void;
}

const HOLD_MS = 400;

/**
 * SelectField is the discrete-control twin of SliderRow: sends
 * immediately on change, but holds the picked value for 400ms so the
 * select doesn't flicker back to the stale server value before the SSE
 * echo lands.
 */
export function SelectField({ value, options, title, onSend }: SelectFieldProps) {
  const [local, setLocal] = useState<string | null>(null);
  const holdTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

  useEffect(() => () => clearTimeout(holdTimer.current), []);

  return (
    <select
      name={title}
      title={title}
      aria-label={title}
      value={local ?? value ?? ""}
      onChange={(e) => {
        const v = e.currentTarget.value;
        setLocal(v);
        clearTimeout(holdTimer.current);
        holdTimer.current = setTimeout(() => setLocal(null), HOLD_MS);
        onSend(v);
      }}
    >
      {options.map((o) => (
        <option key={o.value} value={o.value}>
          {o.label}
        </option>
      ))}
    </select>
  );
}
