# MIDI Probe: reverse-engineering a MIDI device you don't have in hand

> **Status:** shipped, 2026-07-08. A generic, device-agnostic MIDI
> capture/reverse-engineering tool built into the web dashboard
> (`internal/midiprobe`, `internal/web/probe.go`, `/midi-probe`). Works
> with *any* MIDI device — it was built to bring up support for an
> Arturia keyboard the maintainer doesn't own, and doubles as a live
> debugging tool for the already-supported Novation Launchkey.

## The problem this solves

Adding real support for a new MIDI controller normally requires the
maintainer to have the hardware in hand: plug it in, watch what bytes
come out, figure out the SysEx handshake, map every knob and pad. If
someone else has the hardware and the maintainer doesn't, that loop
breaks — unless the *someone else* can be handed a tool simple enough to
run without any polyclav context.

That's this: a page in polyclav's own web dashboard where a person with
the device just twiddles one control at a time, typing a short name for
each, and exports a single JSON file at the end. That file is everything
needed to write a real driver.

## Walkthrough (send this section to whoever has the hardware)

1. **Get polyclav running** on the Mac (or Linux box) with the device
   plugged in. See the platform's own install docs (`docs/INSTALL.md` /
   `docs/MACOS_PORT.md`) — you don't need a working soundfont or the
   audio side configured at all for this; the probe only needs MIDI.
2. **Just run `polyclav`.** No flags needed — the web dashboard is on by
   default now (`[web]` defaults to `enabled = true`). You'll see a line
   like:
   ```
   web ui starting url=http://127.0.0.1:8666/
   ```
   (If you have an old `polyclav.toml` from before with `enabled = false`
   written explicitly, delete that line, or run `polyclav --web on`.)
3. Open that URL **in a browser on the same machine** — it only listens
   on `127.0.0.1`, so that's the whole security model: no login, no
   remote access, nobody else on the network can reach it.
4. Click **"MIDI Probe"** in the top nav bar.
5. Under **Device Connection**, pick the device's ports from the two
   dropdowns and click **Connect**. If your device wasn't plugged in yet
   when the page loaded, click **Refresh ports** first.
   - Some devices (like the Novation Launchkey polyclav already supports)
     expose **more than one** MIDI port pair — a plain one and a
     "DAW"/control one. If nothing happens after connecting and
     twiddling something, click **Disconnect** and try the other pair.
6. Click **"Send Identity Request"** under Identity Request. Whatever
   comes back — or "no reply," which is normal for devices that don't
   implement this — gets recorded automatically either way.
7. **The main task, one control at a time:** for every physical knob,
   pad, fader, or button:
   - Type a short name for it (e.g. `Knob 1`, `Pad 3`, `Mod wheel`) into
     the **Capture & Label** box.
   - Click **Capture 2s**.
   - Immediately move or press **just that one control**, nothing else.
   - Watch the **Event Log** below — you should see a new row (or a few,
     for something like a fader that sends many values) tagged with your
     label.
   - If nothing shows up, that control might use a message type this
     tool doesn't decode cleanly yet — that's still useful to know. Note
     it and move on.
   - Repeat for every control on the device.
8. When you're done, click **"⬇ Export device profile (JSON)"** at the
   top of the Event Log. This downloads one file.
9. **Send that file back.** It contains every port name, the identity
   request result, every message observed with your labels, and enough
   raw detail to build real driver support without ever touching the
   hardware.

## What's in the exported JSON (`DeviceProfile`)

```jsonc
{
  "exportedAt": "2026-07-08T14:31:45Z",
  "inPort": "...", "outPort": "...",
  "allInPorts": [...], "allOutPorts": [...],  // full enumeration, for context
  "identity": {
    "manufacturerName": "Arturia",            // "" if unrecognized — raw bytes still shown
    "manufacturerId": "00206b", "familyCode": "...",
    "modelNumber": "...", "versionBytes": "...",
    "timedOut": false                          // true = no reply; not an error
  },
  "events": [
    { "seq": 0, "time": "...", "port": "...", "kind": "cc",
      "raw": "b0144c", "channel": 0, "data1": 20, "data2": 76,
      "label": "Knob 1" },
    ...
  ],
  "distinctLabels": ["Knob 1", "Pad 3", ...]
}
```

`kind` is one of `note-on`, `note-off`, `cc`, `program-change`,
`pitch-bend`, `aftertouch`, `poly-aftertouch`, `sysex`, or `other`
(unrecognized system-common/realtime bytes). `raw` is always the exact
hex bytes, regardless of whether `kind` could be decoded further.

## Architecture (for whoever picks this up next)

- **`internal/midiprobe`** — the device-agnostic core. `Session` opens an
  exact-named MIDI in/out pair (`SysEx` always on — the whole point is
  capturing raw SysEx, the opposite of `internal/midi`'s and
  `internal/launchkey`'s default-off SysEx handling), decodes every raw
  message, buffers a capped ring of recent events, tags events with a
  label during a capture window, sends the MIDI Universal Non-realtime
  Identity Request and decodes the reply (including 3-byte extended
  manufacturer IDs like Novation's `00 20 29` and Arturia's `00 20 6B`,
  which `gomidi/v2`'s own table doesn't cover), and exports everything.
- **`internal/web/probe.go`** — nine REST endpoints under `/api/probe/*`
  plus reuse of the existing shared SSE stream (`probe-status` /
  `probe-event` `Change` types on the same `controls.Hub` and
  `/api/events` — zero changes to `sse.go`).
- **`web/app/midi-probe/`** — the dashboard page and its components
  (`ProbePortPicker`, `ProbeEventLog`, `ProbeLabelCapture`,
  `ProbeIdentityCard`, `ProbeRawSend`).
- This does **not** touch `internal/midi` or `internal/launchkey` — those
  packages assume an already-known device's port conventions and a
  narrower event model (no raw SysEx). The probe is intentionally
  independent so it works on a device nobody has written a driver for
  yet.

### Testing note

`internal/midiprobe`'s `Session` takes an injectable port-open function,
so its lifecycle/ring-buffer/labeling/identity/export logic is fully
unit-tested with a fake — no MIDI hardware needed. Both
`internal/midiprobe` and `internal/web` additionally carry one
**skip-guarded** integration test that drives the *real* connection
against whatever software loopback the host exposes (on Linux, ALSA's
built-in "Midi Through" virtual port routes anything sent to it back to
itself). These skip cleanly wherever no such loopback exists (e.g. macOS
CI) — they only add confidence where one is available, never block CI
elsewhere.
