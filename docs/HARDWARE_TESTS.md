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

The paged-knob UX ([ROADMAP §2](./ROADMAP.md#2-launchkey-native-ux), adapted) is code-complete and
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

### Generic parameter browser (pending hardware verification)

With a CLAP patch selected, the two buttons beside the encoders
(DAW channel-1 CC 51 up / CC 52 down) page the encoders beyond the
curated pages into the plugin's own parameters. Verify:

- With a CLAP patch loaded and its parameter list published, press
  **encoder ▼** once: the screen flashes `MAIN` / `Page 1/1` first (only
  the host-allocated MAIN page exists for a non-native patch), then on
  the next press flashes `PARAMS` / `1/N`. Press **encoder ▲** to walk
  back, wrapping at both ends.
- The bottom-row indicator pads mark the parameter page position while
  a `PARAMS` page is showing (columns 0–4 only; columns 5–7 stay
  dark so user mixer pads are not touched).
- Turn an encoder on a `PARAMS` page: the screen shows the plugin's
  parameter name (module-prefixed when the name repeats across modules)
  and the new value, the plugin audio changes, and the value survives a
  patch switch (saved with the CLAP state). A stepped parameter moves one
  unit per detent; a wide continuous one sweeps in about one rotation.
- With a soundfont, LV2 or native patch, **encoder ▼** stays on the
  curated pages (native: the same five as Scene; others: MAIN only) — no
  crash, no phantom parameter page.
- Names and values are legible on the two 16-byte lines (long plugin
  names truncate rather than wrap).

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
| 9 fader buttons | On organ patches: buttons 1–8 illuminate with drawbar colors (B3 standard uses brown for 16′/5⅓′, white for 8′/4′/2′, and red for black mutation drawbars 2⅔′/1⅗′/1⅓′); button 1 cycles between 4 drawbar coloring modes; button 2 cycles 5 color schemes for default B3 drawbars; button 9 toggles Leslie Stop/Fast with dedicated Green status LED when running and Off when stopped. On non-organ patches, lights are off. | Observed CC37–45 on DAW channel **1**, press 127/release 0. Verify button 1 cycling, button 2 scheme cycling, button 9 rotary toggle, and B3 color palette emulation on hardware; printed shifted names are menu choices, not extra physical buttons. |
| Pad-bank ↑/↓, encoder ↑/↓, Track ←/→ | Pad-bank CC106/107 and Track CC103/102 are decoded for knob-page and patch-bank navigation; encoder CC51/52 pages the generic parameter browser (curated pages then a CLAP patch's own parameters) | Observed DAW channel-1 CC106/107 (pad-bank), CC51/52 (encoder), CC103/102 (Track). Shift+Track Left uses channel-1 CC109; Shift+Track Right uses channel-1 CC108. Each sends 127 on press and 0 on release. Shift itself reports channel-7 CC63. Verify host navigation on hardware. |
| Play, Stop, Record, Loop, Shift | DAW channel-1 CC115–118 decode as transport events; Play is intended to toggle audition | Observed DAW channel-1 CC115/116/117/118 for Play/Stop/Record/Loop; Shift is channel-7 CC63. The MK4 has no Rewind/Fast-forward controls. Shift+Undo sent Shift CC63 alongside ordinary Undo CC77. Verify Play starts/stops audition on hardware; the other transport actions remain inert. |
| Octave controls, Scale/Arp/Chord controls, mode selectors | Device-side or unhandled by Polyclav's DAW event decoder | Octave presses changed subsequent key note numbers but sent no distinct button event. Scale/Arp sent channel-7 CC74/73; Chord Map reported pad layout CC29=14. Fixed Chord sent no distinct button event in the capture. Test modes before assigning host actions. |

### What the published protocol can and cannot tell us

The [Novation Launchkey MK4 Programmer's Reference Guide, v3.0](https://fael-downloads-prod.focusrite.com/customer/prod/downloads/launchkey_mk4_programmer_s_reference_guide-pdf-en_0.pdf),
pp. 5–14 and 21–23, is a protocol specification, not a substitute for a
hardware check. In its DAW mode, it specifies the DAW USB port, the DAW-mode
entry/exit messages (`9F 0C 7F` / `9F 0C 00`), the pad note grid (96–103 and
112–119 on channel 1), encoder CC85–92 when relative output is enabled
(channel 16, centered on 64), fader CC5–13 (channel 16), and layout reports/selectors (channel-7
CC29/30/31). These agree with our captured input and supported layout. Its
DAW-mode surface diagram (p. 9) also identifies the Track and transport CC
numbers; in particular Play/Stop are CC115/116. The guide separates this
from *standalone* mode, in which DAW control buttons instead report on the
performance MIDI port and channel 16 (pp. 6–8). Mixing these modes or ports
would give a plausible-looking but unusable map.

One difference is worth retaining as an explicit hardware observation: the
specification's Volume-fader diagram (p. 13) places button CC37–45 on channel
16, whereas the 61-key unit's nine button press/release captures used the
DAW port on **channel 1**. The driver accepts both channels. The printed
shifted labels are *choices within a mode menu*, not separate buttons; don't
assign a physical control from a prompt name without observing the message.
The capture also shows channel-1 CC115/116 for Play/Stop. The driver now
parses these and the other captured transport and navigation CCs alongside
its existing channel-16 note support. The regression test verifies the
captured bytes reach the expected host event; a published number, passing
parser test, and lit debugger tile do not establish that the intended action
works on physical hardware. Check Play and bank/page navigation on the device.

For a different keyboard **without the device at hand**:

1. Obtain the manufacturer's exact model/firmware protocol guide and user
   guide. Record each port, operating mode, MIDI status/channel, control
   number, press/release and relative-value convention; distinguish input
   reports from commands sent *to* the device. Never treat its Custom modes
   as factory DAW defaults.
2. Cross-check a maintained host implementation or [Ardour's Launchkey MK4
   integration](https://manual.ardour.org/using-control-surfaces/Launchkey_mk4/)
   for practical behavior, but label its inferred messages separately from
   the vendor's specification. Linux can even present the two ports with
   [indistinguishable names](https://www.spinics.net/lists/alsa-devel/msg178771.html),
   so a port-name match alone does not identify DAW versus performance input.
3. Write parser/encoder tests from the documented byte sequences and a
   synthetic capture; build a read-only inspector that shows raw bytes,
   source port, timestamp, and parsed event. This supports a tentative
   mapping for documented controls, not a verified integration.
4. Ask an owner to capture each physical control once in a declared mode,
   including release, pressure/touch, reconnect, and mode switching; compare
   raw bytes before enabling outbound LED, screen, or layout commands. Verify
   visible output on the device, not just a successful MIDI send. Keep
   undocumented behavior, alternate firmware, and OS-specific port selection
   provisional until tested on the target hardware.

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

In the debugger, sweep the pitch wheel down, release it, then sweep it up and
release it. The displayed signed bend should reach about `-8192`, return to
`0` at the visible center line, reach about `8191`, and return to `0` again.
The mod wheel instead has a one-way `0`–`127` scale. Verify the encoder bank
buttons appear beside the encoders, pad bank buttons beside the pads, the
pad-right/Function buttons opposite the pads, and Track buttons below the
display; on a narrow screen these groups stack without page-wide scrolling.

To verify [DAW layout restoration](./USER_GUIDE.md#launchkey--supported-daw-layout),
start the daemon with the Launchkey connected. Select a different pad, encoder,
or fader layout on the keyboard, one area at a time. Confirm the daemon warns
with the reported and supported values, sends the corresponding channel-7
CC 29/30/31 correction, and the keyboard visibly returns to DAW pads,
Plugin encoders, or Volume faders. Repeat after unplugging and reconnecting.
After switching encoder layouts, turn encoder 1 and check **Recent raw input**:
Plugin with relative output should report channel-16 CC 85 (hex `BF 55`) with
values above or below 64, and the debugger's encoder-1 tile should respond.
If it instead reports CC 21 (hex `BF 15`) or a different channel/port, record
the raw line; a mode light or display message alone does not verify the encoder
output format. Then set `[launchkey].restore_daw_layout = false`, restart, and
confirm the warning remains but no layout correction is sent. Restore the
original setting and keyboard selection afterward. Do not overwrite the
keyboard's Custom modes.

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
