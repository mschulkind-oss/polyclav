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
what Polyclav currently decodes versus the read-only September 2026 inventory.
The CC messages below are **observations**, not a claim that the daemon
handles them correctly yet:

| Physical controls | Polyclav use now | What to check |
|---|---|---|
| 61 keys, pitch and mod wheels, sustain input | Performance MIDI to synth/plugin | Verify which port carries each CC; organ expression expects CC11, not ordinary CC64 sustain. |
| 16 pads (two rows of eight) | Top row selects eight patch slots per bank; bottom row indicates knob pages | Check pad notes 96–103 / 112–119 on the DAW port. |
| 8 endless encoders | Five synth/chain pages | Check relative CC85–92 on channel 16. |
| 9 faders | Unmapped by default; optional mixer OSC or opt-in Potato Keys drawbars 1–9 | Check CC5–13 on channel 16 and that fader 9 does not change mixer volume in organ mode. |
| 9 fader buttons | On organ patches: buttons 1–8 illuminate with drawbar colors (B3 standard uses brown for 16′/5⅓′, white for 8′/4′/2′, and red for black mutation drawbars 2⅔′/1⅗′/1⅓′); button 1 cycles between 4 drawbar coloring modes; button 9 toggles Leslie Stop/Fast with dedicated Green status LED when running and Off when stopped. On non-organ patches, lights are off. | Observed CC37–45 on DAW channel **1**, press 127/release 0. Verify button 1 cycling, button 9 rotary toggle, and B3 color palette emulation on hardware; printed shifted names are menu choices, not extra physical buttons. |
| Pad-bank ↑/↓, display ↑/↓, Track ←/→ | Knob-page navigation and patch-bank navigation are intended, but decoder expects channel-16 note messages | Observed DAW channel-1 CC106/107 (pad-bank), CC51/52 (display), CC103/102 (Track). Their roles must be reconciled before declaring navigation verified. |
| Play, Stop, Record, Loop, Rewind, Fast-forward, Shift | Play is intended to toggle audition; decoder expects channel-16 notes | Observed DAW channel-1 CC115/116/117/118 for Play/Stop/Record/Loop; Shift is channel-7 CC63. Rewind/fast-forward were skipped. Shift+Undo sent Shift CC63 alongside ordinary Undo CC77. |
| Octave controls, Scale/Arp/Chord controls, mode selectors | Device-side or unhandled by Polyclav's DAW event decoder | Octave presses changed subsequent key note numbers but sent no distinct button event. Scale/Arp sent channel-7 CC74/73; Chord Map reported pad layout CC29=14. Fixed Chord sent no distinct button event in the capture. Test modes before assigning host actions. |

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

To investigate the skipped mode controls without repeating every fader/key,
run the targeted pass with longer windows:

```sh
python3 scripts/launchkey_mk4_inventory.py --preset follow-up --list-prompts
python3 scripts/launchkey_mk4_inventory.py --preset follow-up --seconds 12 \
  --output scratch/launchkey-mk4-follow-up.json
```

In a `Shift + pad menu` prompt, hold the keyboard's Shift button and
select the **named mode** using the physical pad menu, then release Shift and
try the specified pad/key. A mode name is not a separate button. If a mode is
not available, skip it. The script sends nothing; pad/fader modes and chord
settings can change locally on the keyboard, so restore DAW/Volume at the end.
The probe now records pad polyphonic aftertouch as a separate event with note
and value. For a visual read-only debug view without starting the audio daemon,
run standalone `just web-dev` and open
`http://localhost:3000/app/launchkey-debug/`. Under full `just dev`, hivemind
assigns the web process `PORT=5100`, so use
`http://localhost:5100/app/launchkey-debug/` instead. In either mode, `/`
intentionally 404s because the Next app lives under `/app/`. Chromium's Web
MIDI permission permits dual-port live input; alternatively load either
inventory JSON locally and step through it without any MIDI permission. The
visual view never transmits MIDI or starts audio. Controls that report no
message cannot light up from MIDI alone.

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
