"use client";

import { useEffect, useState } from "react";
import { api, errorMessage } from "@/lib/api";
import type { ProbeStatus } from "@/lib/types";

interface ProbeLabelCaptureProps {
  status: ProbeStatus;
}

/**
 * The core reverse-engineering UX (docs/MIDI_PROBE.md): type a name for a
 * physical control, click Capture once, then move or press JUST that one
 * control on the device within the window. Every event ingested while the
 * window is open gets tagged with the label server-side
 * (Session.BeginLabel) — this component only drives the countdown display.
 */
export function ProbeLabelCapture({ status }: ProbeLabelCaptureProps) {
  const [label, setLabel] = useState("");
  const [error, setError] = useState("");
  const [remainingMs, setRemainingMs] = useState(0);

  useEffect(() => {
    if (!status.labeling || !status.labelEndsAt) {
      setRemainingMs(0);
      return;
    }
    const endsAt = new Date(status.labelEndsAt).getTime();
    const tick = () => setRemainingMs(Math.max(0, endsAt - Date.now()));
    tick();
    const id = setInterval(tick, 100);
    return () => clearInterval(id);
  }, [status.labeling, status.labelEndsAt]);

  const capture = async () => {
    setError("");
    const r = await api.probeLabel(label, 2000);
    if (!r?.ok && r) setError(await errorMessage(r));
  };

  const active = status.labeling && remainingMs > 0;

  return (
    <section>
      <h2>Capture &amp; Label</h2>
      <div className="row">
        <span className="rowlabel">Label</span>
        <input
          type="text"
          aria-label="Control label"
          placeholder='e.g. "Knob 1", "Mod wheel"'
          value={label}
          disabled={!status.active || active}
          onChange={(e) => setLabel(e.currentTarget.value)}
        />
        <span />
      </div>
      <div className="btnrow">
        <button
          type="button"
          className={active ? "active" : undefined}
          disabled={!status.active || !label.trim() || active}
          onClick={capture}
        >
          {active ? `Capturing… ${(remainingMs / 1000).toFixed(1)}s` : "Capture 2s"}
        </button>
      </div>
      {error ? <div className="errbox">{error}</div> : null}
      <p className="hint">
        Type a name, click Capture, then immediately move or press just that one control. Whatever
        arrives in the next two seconds gets tagged with the label in the event log below.
      </p>
    </section>
  );
}
