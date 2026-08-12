# Why MIDI device matching is by substring, not exact name

## Status: implemented

`[midi].allow_devices` entries match as a case-insensitive **substring** of
the port name — see `internal/midi/multiplexer.go` `classifyOne` /
`containsAny`. This note records why, because the reasoning is not obvious
from the code and the naive alternative (exact names) looks more correct
than it is.

Originally written when the setting was a denylist (`ignore_devices`) that
sat alongside a `port_match` filter; on 2026-08-11 the selection model
flipped to an allowlist and `port_match` was dropped, but the matching rule
and its rationale carried over unchanged.

## Problem

An entry identifies a keyboard. The obvious way to write one is to paste the
port name `polyclav midi list` prints — but that name ends in a volatile
` <client>:<port>` address (e.g. ` 36:0`) that ALSA reassigns on replug /
reboot / device-order changes. Under exact matching, a config that works
today silently stops working the next time the address shifts, and the
symptom is a keyboard that just doesn't play. A user config should not have
to name a hardware address that isn't stable.

## Evidence (the behavior that motivated this)

Casio connected, enumerating as `CASIO USB-MIDI:CASIO USB-MIDI MIDI 1 36:0`,
back when matching was exact:

```
# entry = "CASIO USB-MIDI:CASIO USB-MIDI MIDI 1"      (no address)
  <no match>   CASIO USB-MIDI:CASIO USB-MIDI MIDI 1 36:0

# entry = "CASIO USB-MIDI:CASIO USB-MIDI MIDI 1 36:0" (full string)
  match        CASIO USB-MIDI:CASIO USB-MIDI MIDI 1 36:0
```

Matching only worked when the fragile `36:0` was baked into the config.

## The rule

An entry identifies a device by its **stable** name, independent of the
trailing ALSA `NN:NN` address, by matching as a case-insensitive substring
of the port name. An empty entry is skipped rather than treated as a
match-everything wildcard — under an allowlist that would silently open
every port on the machine.

Both surfaces that *suggest* an entry trim the address before offering it,
so a copy-pasted suggestion is stable by construction: `suggestAllowEntry`
(`cmd/polyclav/main.go`, used by the startup banner) and `allowEntryFor`
(`web/components/MIDIDevicesCard.tsx`, used when ticking a checkbox).

## Acceptance test

With the Casio connected and:

```toml
[midi]
allow_devices = ["CASIO USB-MIDI"]
```

- `polyclav midi list` reports the Casio port as `ok`.
- It stays `ok` if the address changes (e.g. the port re-enumerates as
  `… MIDI 1 37:0`).
- Unrelated ports (Launchkey, X18/XR18) stay `off` — no accidental
  over-match.

## Where it lives (pointers, for orientation only)

- Match logic: `internal/midi/multiplexer.go` `classifyOne` / the lowercased
  substring list it builds (`lowerAll`) / `containsAny`.
- Port names come from `in.String()` — `internal/midi/midi.go` `portNames`.
- Config field + doc comment: `internal/config/config.go` (`AllowDevices`).
- CLI hint: `cmd/polyclav/midi.go` (`runMIDIList`).
