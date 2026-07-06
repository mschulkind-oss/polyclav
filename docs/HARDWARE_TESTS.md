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
- Top-row pads switch patches.
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
  has played yet. Stop/Record/Loop/Rewind/FF/Track/Shift do nothing.

## How to report back

In chat, list which checks behaved as expected and which didn't. For
anything broken, paste relevant log lines from `tail -f /tmp/polyclav.log`.
If hardware behavior is wrong but the log looks clean, that's a signal the
SysEx encoding is the gap -- flag it.

## No-hardware smoke test (headless, any PipeWire box)

Everything above needs the bench; this doesn't. The audition player plus
the web API exercise the full config → boot → audio → live-control →
shutdown path with no Launchkey, no XR18, and no keyboard.

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
