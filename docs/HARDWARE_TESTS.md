# Hardware regression checks

A short standing smoke test for things that can only be verified with a
Launchkey MK4 + XR18 + audio interface physically connected. Run through
it any time after a rebuild or before a release.

## Regression checks (do these any time)

- Keys produce sound on every patch.
- Sustain pedal sustains.
- Mod wheel modulates (CC 1).
- Knobs 1/2/3 drive volume / reverb / compressor.
- Knob 4 drives cutoff *only* on `type = "native"` patches (no-op on
  others); knobs 5-8 are intentionally unbound.
- Top-row pads switch patches.
- `overmind quit` returns the Launchkey to non-DAW (Custom/factory) mode.

## How to report back

In chat, list which checks behaved as expected and which didn't. For
anything broken, paste relevant log lines from `tail -f /tmp/polyclav.log`.
If hardware behavior is wrong but the log looks clean, that's a signal the
SysEx encoding is the gap -- flag it.
