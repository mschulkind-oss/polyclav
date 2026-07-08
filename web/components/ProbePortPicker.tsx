"use client";

import { useCallback, useEffect, useState } from "react";
import { api, errorMessage } from "@/lib/api";
import type { ProbeStatus } from "@/lib/types";

interface ProbePortPickerProps {
  status: ProbeStatus;
  onStatus: (s: ProbeStatus) => void;
}

/**
 * Port enumeration + connect/disconnect for the MIDI probe. Ports are
 * matched by exact name (a plain dropdown, not substring/role matching
 * like internal/midi.PickPortName) — the whole point of this tool is the
 * user doesn't know the device's port conventions yet.
 */
export function ProbePortPicker({ status, onStatus }: ProbePortPickerProps) {
  const [ins, setIns] = useState<string[]>([]);
  const [outs, setOuts] = useState<string[]>([]);
  const [inPort, setInPort] = useState("");
  const [outPort, setOutPort] = useState("");
  const [error, setError] = useState("");

  const refresh = useCallback(async () => {
    const p = await api.probePorts();
    if (!p) return;
    setIns(p.ins);
    setOuts(p.outs);
    setInPort((cur) => cur || p.ins[0] || "");
    setOutPort((cur) => cur || p.outs[0] || "");
  }, []);

  useEffect(() => {
    refresh();
  }, [refresh]);

  const connect = async () => {
    setError("");
    const r = await api.probeConnect(inPort, outPort);
    if (r?.ok) {
      onStatus(await r.json());
    } else if (r) {
      setError(await errorMessage(r));
    }
  };

  const disconnect = async () => {
    setError("");
    const r = await api.probeDisconnect();
    if (r?.ok) onStatus(await r.json());
  };

  return (
    <section>
      <h2>Device Connection</h2>
      <div className="row">
        <span className="rowlabel">In port</span>
        <select
          aria-label="MIDI input port"
          value={inPort}
          disabled={status.active}
          onChange={(e) => setInPort(e.currentTarget.value)}
        >
          {ins.length === 0 ? <option value="">(none found)</option> : null}
          {ins.map((n) => (
            <option key={n} value={n}>
              {n}
            </option>
          ))}
        </select>
        <span />
      </div>
      <div className="row">
        <span className="rowlabel">Out port</span>
        <select
          aria-label="MIDI output port"
          value={outPort}
          disabled={status.active}
          onChange={(e) => setOutPort(e.currentTarget.value)}
        >
          {outs.length === 0 ? <option value="">(none found)</option> : null}
          {outs.map((n) => (
            <option key={n} value={n}>
              {n}
            </option>
          ))}
        </select>
        <span />
      </div>
      <div className="btnrow">
        <button type="button" onClick={refresh} disabled={status.active}>
          Refresh ports
        </button>
        {status.active ? (
          <button type="button" onClick={disconnect}>
            Disconnect
          </button>
        ) : (
          <button type="button" onClick={connect} disabled={!inPort || !outPort}>
            Connect
          </button>
        )}
        <span className={`dot${status.active ? " on" : ""}`} />
        <span className="play-ind-label">
          {status.active ? `connected: ${status.inPort}` : "not connected"}
        </span>
      </div>
      {error ? <div className="errbox">{error}</div> : null}
      <p className="hint">
        Plugged in a new device? Click Refresh ports. Some devices (like the Launchkey) expose more
        than one in/out pair — if nothing happens after connecting, try Disconnect and pick the
        other pair.
      </p>
    </section>
  );
}
