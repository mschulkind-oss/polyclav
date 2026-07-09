"use client";

import { useCallback, useEffect, useState } from "react";
import { api, errorMessage } from "@/lib/api";
import type { MIDIDevice } from "@/lib/types";

/**
 * MIDI devices panel (docs/USER_GUIDE.md "[midi] — which keyboards send
 * notes"): every connected port, live. Checkable rows toggle
 * ignore/un-ignore; DAW-role and port_match-restricted rows are shown
 * but not checkable (toggling either would do nothing — see
 * internal/midi.PortStatus). Apply (session) hits SetIgnore immediately
 * without touching the file; Save additionally persists ignore_devices
 * into polyclav.toml — the exact Apply/Save split VelocityCard already
 * established for the global velocity curve.
 */
export function MIDIDevicesCard() {
  const [devices, setDevices] = useState<MIDIDevice[] | null>(null);
  const [match, setMatch] = useState("");
  const [ignored, setIgnored] = useState<Set<string>>(new Set());
  const [error, setError] = useState<string | null>(null);
  const [status, setStatus] = useState<string>("");

  const refresh = useCallback(async () => {
    const r = await api.midiDevices();
    if (!r) {
      setError("request failed");
      return;
    }
    setDevices(r.devices);
    setMatch(r.match);
    setIgnored(new Set(r.devices.filter((d) => d.status === "ignored").map((d) => d.name)));
    setError(null);
  }, []);

  useEffect(() => {
    refresh();
  }, [refresh]);

  const toggle = (name: string) => {
    setIgnored((cur) => {
      const next = new Set(cur);
      if (next.has(name)) next.delete(name);
      else next.add(name);
      return next;
    });
  };

  const apply = async (save: boolean) => {
    const r = await api.midiDevicesPut(Array.from(ignored), save);
    if (!r) {
      setError("request failed");
      return;
    }
    if (r.ok) {
      setError(null);
      setStatus(save ? "saved" : "applied (session)");
      await refresh();
    } else {
      setError(await errorMessage(r));
    }
  };

  if (devices === null) {
    return error ? <div className="errbox">{error}</div> : <p className="hint">Loading…</p>;
  }
  if (devices.length === 0) {
    return <p className="hint">No MIDI input ports found. Plug in a keyboard and reload.</p>;
  }

  return (
    <>
      {match ? (
        <p className="hint">
          [midi].port_match is set to <b>{match}</b> — only matching ports are listed as sending
          notes; the checkboxes below still control the ignore list on top of that restriction.
        </p>
      ) : null}
      <ul className="midi-device-list">
        {devices.map((d) => {
          const checkable = d.status !== "daw" && d.status !== "restricted";
          const checked = checkable && !ignored.has(d.name);
          return (
            <li key={d.name} className="midi-device-row">
              <label
                className={
                  checkable ? "midi-device-label" : "midi-device-label midi-device-disabled"
                }
              >
                <input
                  type="checkbox"
                  checked={checkable ? checked : false}
                  disabled={!checkable}
                  onChange={() => checkable && toggle(d.name)}
                />
                {d.name}
              </label>
              <span className="chip">
                {d.status === "daw"
                  ? "DAW control surface"
                  : d.status === "restricted"
                    ? "restricted by port_match"
                    : d.status === "ignored"
                      ? "ignored"
                      : "sending notes"}
              </span>
            </li>
          );
        })}
      </ul>
      <div className="btnrow">
        <button type="button" onClick={() => apply(false)}>
          Apply (session)
        </button>
        <button type="button" onClick={() => apply(true)}>
          Save
        </button>
        <button type="button" onClick={refresh}>
          Refresh
        </button>
        <span className="vel-active">{status}</span>
      </div>
      {error ? <div className="errbox">{error}</div> : null}
      <p className="hint">
        Unchecked keyboards stop sending notes immediately on Apply. A device NOT listed here yet
        (plugged in later) always sends notes by default — this is a denylist, not an allowlist.
      </p>
    </>
  );
}
