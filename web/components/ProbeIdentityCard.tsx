"use client";

import { useState } from "react";
import { api } from "@/lib/api";
import type { IdentityResult } from "@/lib/types";

interface ProbeIdentityCardProps {
  active: boolean;
}

/** Universal MIDI Identity Request/Reply (docs/MIDI_PROBE.md) — not every
 * device implements this; a "no reply" result is still useful information,
 * not an error. */
export function ProbeIdentityCard({ active }: ProbeIdentityCardProps) {
  const [result, setResult] = useState<IdentityResult | null>(null);
  const [pending, setPending] = useState(false);

  const send = async () => {
    setPending(true);
    setResult(null);
    const r = await api.probeIdentity(2000);
    setPending(false);
    setResult(r);
  };

  return (
    <section>
      <h2>Identity Request</h2>
      <div className="btnrow">
        <button type="button" onClick={send} disabled={!active || pending}>
          {pending ? "Waiting for reply…" : "Send Identity Request"}
        </button>
      </div>
      {result ? (
        result.timedOut ? (
          <p className="hint">
            No reply — this device may not support the standard Identity Request. That is still
            useful information; note it and move on.
          </p>
        ) : (
          <div className="grid">
            <div>
              <span className="rowlabel">Manufacturer</span>
              <div className="val">
                {result.manufacturerName || "(unknown)"} <code>{result.manufacturerId}</code>
              </div>
            </div>
            {result.familyCode ? (
              <div>
                <span className="rowlabel">Family</span>
                <div className="val">
                  <code>{result.familyCode}</code>
                </div>
              </div>
            ) : null}
            {result.modelNumber ? (
              <div>
                <span className="rowlabel">Model</span>
                <div className="val">
                  <code>{result.modelNumber}</code>
                </div>
              </div>
            ) : null}
            {result.versionBytes ? (
              <div>
                <span className="rowlabel">Version</span>
                <div className="val">
                  <code>{result.versionBytes}</code>
                </div>
              </div>
            ) : null}
            <div>
              <span className="rowlabel">Raw reply</span>
              <div className="val">
                <code>{result.replyRaw}</code>
              </div>
            </div>
          </div>
        )
      ) : null}
    </section>
  );
}
