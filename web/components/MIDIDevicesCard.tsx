"use client";

import { useCallback, useEffect, useState } from "react";
import { api, errorMessage } from "@/lib/api";
import type { MIDIDevice } from "@/lib/types";

/**
 * MIDI devices panel (docs/USER_GUIDE.md "[midi] — which keyboards send
 * notes"): every connected port, live. `[midi].allow_devices` is an
 * ALLOWLIST — a checked box means "this keyboard may send notes", and
 * with nothing checked polyclav makes no sound at all. That empty state
 * is the default on a fresh install, so the panel says so loudly rather
 * than looking like an ordinary empty list.
 *
 * `port_match`-restricted rows are shown but not checkable (selecting
 * one would change nothing until port_match is cleared). DAW-role and
 * loopback rows ARE checkable — the heuristics are advisory here, and
 * deliberately selecting a DAW port is a documented workflow — but they
 * carry a warning chip.
 *
 * Apply (session) hits SetAllow immediately without touching the file;
 * Save additionally persists allow_devices into polyclav.toml — the
 * exact Apply/Save split VelocityCard already established for the global
 * velocity curve.
 */

/** Matches the volatile " <client>:<port>" address ALSA appends to a port name. */
const ALSA_ADDR_SUFFIX = /\s+\d+:\d+$/;

/**
 * Trims a port name down to the stable fragment worth storing, mirroring
 * cmd/polyclav's suggestAllowEntry: that trailing ALSA address shifts on
 * replug/reboot, so storing it verbatim would silently stop matching
 * later (docs/MIDI_DEVICE_MATCHING.md).
 */
export function allowEntryFor(portName: string): string {
  const trimmed = portName.replace(ALSA_ADDR_SUFFIX, "").trim();
  return trimmed === "" ? portName : trimmed;
}

/** Case-insensitive substring test — the same rule the daemon applies. */
export function allowMatches(entry: string, portName: string): boolean {
  return entry !== "" && portName.toLowerCase().includes(entry.toLowerCase());
}

export function MIDIDevicesCard() {
  const [devices, setDevices] = useState<MIDIDevice[] | null>(null);
  const [match, setMatch] = useState("");
  const [allow, setAllow] = useState<string[]>([]);
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
    setAllow(r.allow ?? []);
    setError(null);
  }, []);

  useEffect(() => {
    refresh();
  }, [refresh]);

  const isSelected = (name: string) => allow.some((entry) => allowMatches(entry, name));

  /**
   * Selecting stores the trimmed port name; deselecting drops EVERY
   * entry that matched this port, not just an exact-name one — an entry
   * may be a hand-written substring, and leaving it behind would make
   * the box tick itself straight back on.
   */
  const toggle = (name: string) => {
    setStatus("");
    setAllow((cur) =>
      cur.some((entry) => allowMatches(entry, name))
        ? cur.filter((entry) => !allowMatches(entry, name))
        : [...cur, allowEntryFor(name)],
    );
  };

  const apply = async (save: boolean) => {
    const r = await api.midiDevicesPut(allow, save);
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

  // Entries naming hardware that isn't plugged in right now have no
  // device row to hang a checkbox off. Surface them anyway: they're
  // still live config, and hiding them would silently drop them on Save.
  const offline = allow.filter((entry) => !devices.some((d) => allowMatches(entry, d.name)));
  const nothingSelected = allow.length === 0;

  return (
    <>
      {nothingSelected ? (
        <div className="errbox">
          <b>No MIDI devices are selected — nothing will make sound.</b> This is an allowlist: check
          a keyboard below, then Apply or Save.
        </div>
      ) : null}
      {match ? (
        <p className="hint">
          [midi].port_match is set to <b>{match}</b> — ports that don&apos;t contain it can never
          send notes, whatever you check here.
        </p>
      ) : null}
      {devices.length === 0 ? (
        <p className="hint">No MIDI input ports found. Plug in a keyboard and hit Refresh.</p>
      ) : (
        <ul className="midi-device-list">
          {devices.map((d) => {
            const checkable = d.status !== "restricted";
            return (
              <li key={d.name} className="midi-device-row">
                <label
                  className={
                    checkable ? "midi-device-label" : "midi-device-label midi-device-disabled"
                  }
                >
                  <input
                    type="checkbox"
                    checked={checkable && isSelected(d.name)}
                    disabled={!checkable}
                    onChange={() => checkable && toggle(d.name)}
                  />
                  {d.name}
                </label>
                <span className="chip">
                  {d.status === "restricted"
                    ? "restricted by port_match"
                    : d.status === "daw"
                      ? "DAW control surface — not keys"
                      : d.status === "loopback"
                        ? "loopback port — not a keyboard"
                        : d.status === "notes"
                          ? "sending notes"
                          : "not selected"}
                </span>
              </li>
            );
          })}
        </ul>
      )}
      {offline.length > 0 ? (
        <p className="hint">
          Also selected, but not connected right now: {offline.map((e) => `"${e}"`).join(", ")}.
          These stay in the list and start sending notes the moment they&apos;re plugged in.
        </p>
      ) : null}
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
        Checked keyboards start sending notes immediately on Apply. Selection is by a stable
        fragment of the port name, so it survives a replug even though the trailing ALSA address
        changes — and a device you haven&apos;t checked stays silent no matter when it&apos;s
        plugged in.
      </p>
    </>
  );
}
