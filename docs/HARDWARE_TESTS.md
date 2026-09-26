# Hardware regression checks

A short standing smoke test for things that can only be verified with a
Launchkey MK4 + XR18 + audio interface physically connected. Run through
it any time after a rebuild or before a release.

## Regression checks (do these any time)

- Keys produce sound on every patch.
- Sustain pedal sustains.
- Mod wheel modulates (CC 1).
- On page 1 (MAIN): knobs 1/2/3 drive volume / reverb / compressor on
  every patch; knob 4 drives cutoff *only* on `type = "native"` patches
  (no-op on others).
- Top-row pads switch patches in the current bank of eight. With more than
  eight configured patches, Track ←/→ changes banks; check slots 9–16 and
  that empty slots in a partial bank are dark and inert.
- `overmind quit` returns the Launchkey to non-DAW (Custom/factory) mode.

## Knob pages (pending hardware verification)

The paged-knob UX (docs/ROADMAP.md §2, adapted) is code-complete and
unit-tested against driver fakes, but has never met the device. Verify:

- Scene ↑/↓ cycle the 5 pages (MAIN → OSC → FILTER → AMP → LFO/MOD,
  wrapping) on a native patch; the page name flashes on the screen and
  reverts to the patch name after ~800 ms.
- Bottom-row pads 1-5 indicate pages: active page orange, others dim
  white. The indicator follows Scene presses and survives a device
  power-cycle/reconnect.
- On a soundfont patch, Scene ↑/↓ flash "(native only)" and stay on
  MAIN; bottom-row pads 2-5 go dark.
- Knob 1 on each page sounds right on a native patch: MAIN=volume,
  OSC=osc1 level, FILTER=cutoff, AMP=amp attack, LFO/MOD=LFO rate
  (audible vibrato/wah once LFO>Pitch or LFO>Cutoff is raised).
- Knob labels/values on the screen are legible for every slot (16-char
  lines; e.g. "Osc1 Detune" / "+7 c").
- Transport Play toggles the audition player's last-used clip (run with
  `--play <clip>` first); shows PLAY/STOP, or "(no clip)" if nothing
  has played yet. Stop/Record/Loop/Rewind/FF/Shift do nothing. Track
  ←/→ changes patch banks when more than eight patches are configured.

## Launchkey 61 MK4 control inventory to verify

The Launchkey has two USB MIDI ports: the performance port sends played
notes and wheels, while the DAW port sends surface-control messages. The Novation Launchkey MK4 Programmer’s Reference Guide describes their
MIDI protocol; a local hardware reverse-engineering reference is also
available on this host at `~/projects/hw_hacking/docs/hardware_interfaces.md`.
This inventory is
what Polyclav currently decodes, not a claim that every physical button has
been observed on this particular unit:

| Physical controls | Polyclav use now | What to check |
|---|---|---|
| 61 keys, pitch and mod wheels, sustain input | Performance MIDI to synth/plugin | Verify which port carries each CC; organ expression expects CC11, not ordinary CC64 sustain. |
| 16 pads (two rows of eight) | Top row selects eight patch slots per bank; bottom row indicates knob pages | Check pad notes 96–103 / 112–119 on the DAW port. |
| 8 endless encoders | Five synth/chain pages | Check relative CC85–92 on channel 16. |
| 9 faders | Unmapped by default; optional mixer OSC or opt-in Potato Keys drawbars 1–9 | Check CC5–13 on channel 16 and that fader 9 does not change mixer volume in organ mode. |
| 9 fader buttons | Decoded, no application action yet | Check CC37–45 on channel 16. |
| Scene ↑/↓ and Track ←/→ | Knob-page navigation and patch-bank navigation, respectively | Check which printed arrow pair produces each event; button note numbers 104–105 and 102–103. |
| Play, Stop, Record, Loop, Rewind, Fast-forward, Shift | Play toggles audition; the rest are decoded but unused | Check button note numbers 115, 116, 117, 118, 113, 114, 106 respectively. |
| Octave controls, Scale/Arp/Chord controls, mode selectors | Device-side or unhandled by Polyclav's DAW event decoder | Capture messages before assigning host actions; some alter device behavior rather than emit a distinct DAW button. |

Use the committed read-only inventory probe to capture one Launchkey control at
a time and save a JSON report:

```sh
python3 scripts/launchkey_mk4_inventory.py --output scratch/launchkey-mk4-inventory.json
```

The probe auto-detects Launchkey ALSA ports with `aconnect -l`. If messages are
missing, compare the displayed capture sources with `aconnect -l` and rerun with
`--midi-port CLIENT:PORT` and/or `--daw-port CLIENT:PORT`. Send back
`scratch/launchkey-mk4-inventory.json`; for any surprising result, also mention
the physical label pressed and whether the probe recorded `captured`,
`no_event`, `skipped`, or `capture_error`.

## How to report back

In chat, list which checks behaved as expected and which didn't. For
anything broken, paste relevant log lines from `tail -f /tmp/polyclav.log`.
If hardware behavior is wrong but the log looks clean, that's a signal the
SysEx encoding is the gap -- flag it.

## No-hardware smoke test (headless, any PipeWire box)

Everything above needs the bench; this doesn't. The audition player plus
the web API exercise the full config → boot → audio → live-control →
shutdown path with no Launchkey, no explicitly configured XR18, and no keyboard.

1. Write a minimal config — one native patch, zero files needed:

   ```sh
   cat > /tmp/smoke.toml <<'EOF'
   [web]
   enabled = true            # serves on 127.0.0.1:8666

   [[patches]]
   name    = "moog"
   display = "Moog"
   type    = "native"
   engine  = "minimoog"
   EOF
   ```

2. Boot it looping the bass clip — you should hear the riff immediately:

   ```sh
   ./bin/polyclav --config /tmp/smoke.toml --play bass-riff --loop &
   ```

3. Status answers and reports the patch and transport:

   ```sh
   curl -s http://127.0.0.1:8666/api/status | jq '.params.patch, .player'
   # → "moog"   {"playing": true, "clip": "bass-riff", "loop": true, "tempo": 1}
   ```

4. Live synth control is audible mid-loop — open the filter envelope and
   add resonance; the riff changes character immediately (the response is
   the full synth state as JSON):

   ```sh
   curl -s -X PATCH http://127.0.0.1:8666/api/synth \
        -d '{"resonance": 0.8, "filter_env": {"amount": 0.4}}'
   ```

5. Clean shutdown: `kill %1` (SIGTERM). The player releases held notes
   before the audio engine stops, and the log ends with
   `shutdown complete`.

Report the same way as the hardware checks: which steps behaved, plus
log lines for anything that didn't.
