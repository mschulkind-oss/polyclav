# Handoff: make `[midi].ignore_devices` robust to the ALSA port address

## Status: implemented

`ignore_devices` entries now match as a case-insensitive substring of the
port name, same as `port_match` — see `internal/midi/multiplexer.go`
`classifyOne`/`containsAny`. Kept below as the original problem writeup.

## Problem

`[midi].ignore_devices` is meant to exclude a specific keyboard from sending
notes. Today it only matches the **exact, full** ALSA-seq port name — and that
name ends in a volatile ` <client>:<port>` address (e.g. ` 36:0`) that ALSA
reassigns on replug / reboot / device-order changes. So an exclusion that works
today silently stops working the next time the address shifts. A user config
should not have to name a hardware address that isn't stable.

## Evidence (current behavior)

Casio connected, enumerating as `CASIO USB-MIDI:CASIO USB-MIDI MIDI 1 36:0`:

```
# ignore_devices = ["CASIO USB-MIDI:CASIO USB-MIDI MIDI 1"]   (no address)
  ok         CASIO USB-MIDI:CASIO USB-MIDI MIDI 1 36:0        <- NOT excluded

# ignore_devices = ["CASIO USB-MIDI:CASIO USB-MIDI MIDI 1 36:0"] (full string)
  ignored    CASIO USB-MIDI:CASIO USB-MIDI MIDI 1 36:0        <- excluded
```

Exclusion only works when the fragile `36:0` is baked into the config.

## Desired function (not prescribing implementation)

An `ignore_devices` entry should identify a device by its **stable** name,
independent of the trailing ALSA `NN:NN` address. Matching an entry as a
case-insensitive **substring** of the port name is the natural model — it's
exactly how `port_match` already behaves, so the two knobs become symmetric
(one substring allow-filter, one substring deny-filter). Any approach that
makes a stable name match is fine; substring is the obvious one.

## Acceptance test

With the Casio connected and:

```toml
[midi]
ignore_devices = ["CASIO USB-MIDI"]
```

- `polyclav midi list` reports the Casio port as `ignored`.
- It stays `ignored` if the address changes (e.g. the port re-enumerates as
  `… MIDI 1 37:0`).
- The Launchkey and X18/XR18 ports remain `ok` (no accidental over-match).

## Keep consistent while you're in there

- `docs/USER_GUIDE.md` `ignore_devices` section and the `polyclav.example.toml`
  `[midi]` comment currently imply "exact name" — update to reflect substring.
- The `polyclav midi list` hint text says "use the exact names above" — soften
  it (a stable substring now suffices).
- The web-UI managed `ignore_devices` block round-trip must still work.

## Where it lives (pointers, for orientation only)

- Match logic: `internal/midi/multiplexer.go` `classifyOne` / the lowercased
  substring list it builds (`lowerAll`). `port_match`'s substring path is
  right next to it as the model it mirrors.
- Port names come from `in.String()` — `internal/midi/midi.go` `portNames`.
- Config field + doc comment: `internal/config/config.go` (`IgnoreDevices`).
- CLI hint: `cmd/polyclav/midi.go` (`runMIDIList`).
