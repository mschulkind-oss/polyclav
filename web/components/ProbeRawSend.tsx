"use client";

import { useState } from "react";
import { api, errorMessage } from "@/lib/api";

interface ProbeRawSendProps {
  active: boolean;
}

/** "Try arbitrary hex against an unknown device" — no framing is added,
 * so a SysEx message must include F0...F7 in the pasted hex itself. */
export function ProbeRawSend({ active }: ProbeRawSendProps) {
  const [hex, setHex] = useState("");
  const [error, setError] = useState("");
  const [sentBytes, setSentBytes] = useState<number | null>(null);

  const send = async () => {
    setError("");
    setSentBytes(null);
    const r = await api.probeSend(hex);
    if (r?.ok) {
      const body = await r.json();
      setSentBytes(body.bytes ?? null);
    } else if (r) {
      setError(await errorMessage(r));
    }
  };

  return (
    <section>
      <h2>Send Raw Hex</h2>
      <div className="row">
        <span className="rowlabel">Bytes</span>
        <input
          type="text"
          aria-label="Raw MIDI bytes, hex"
          placeholder="F0 00 20 29 01 F7"
          value={hex}
          disabled={!active}
          onChange={(e) => setHex(e.currentTarget.value)}
        />
        <span />
      </div>
      <div className="btnrow">
        <button type="button" onClick={send} disabled={!active || !hex.trim()}>
          Send
        </button>
        {sentBytes !== null ? <span className="hint">sent {sentBytes} bytes</span> : null}
      </div>
      {error ? <div className="errbox">{error}</div> : null}
      <p className="hint">
        Spaces are ignored. A SysEx message must include the F0...F7 framing yourself — nothing is
        added automatically.
      </p>
    </section>
  );
}
