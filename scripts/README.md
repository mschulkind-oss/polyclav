# polyclav scripts

- [`components-capture.js`](components-capture.js) — browser shim that logs WebMIDI traffic to/from a Launchkey. Usage is documented in its own header comment.
- [`launchkey_mk4_inventory.py`](launchkey_mk4_inventory.py) — guided, non-invasive Launchkey MK4 control inventory. It uses `aconnect -l` for discovery and `aseqdump -p` for read-only capture; it never sends MIDI, starts audio, or changes device settings.

  Safe host invocation:

  ```sh
  cd /path/to/polyclav
  python3 scripts/launchkey_mk4_inventory.py \
    --output scratch/launchkey-mk4-inventory.json
  ```

  If ALSA discovery is ambiguous, pass explicit source ports from `aconnect -l`:

  ```sh
  python3 scripts/launchkey_mk4_inventory.py \
    --midi-port 32:0 \
    --daw-port 32:1 \
    --output scratch/launchkey-mk4-inventory.json
  ```

  The script walks every photographed control with deterministic prompts and records timestamped note, CC, polyphonic pad pressure, SysEx, pitch-bend, and system/realtime evidence per port. A prompt with no observed event is saved as `no_event`; do not treat that as proof of a MIDI event. To share results for driver mapping, send the generated JSON report from `scratch/launchkey-mk4-inventory.json`.

  After the full inventory, use the shorter Shift/layout/feature pass to inspect controls whose meaning changes with keyboard mode. The names printed after `Shift + pad menu` are **choices on the physical pad menu**, not separate buttons. Check the prompt list first; press Enter to capture each scenario and `s` if the control is absent. Select modes on the keyboard itself; the program never sends commands. Use a longer window when you need to select a mode and play a key. Return pads and faders to DAW/Volume mode when done. Choosing Custom modes or setting a chord can change the keyboard's local state; do not do this during a performance.

  ```sh
  python3 scripts/launchkey_mk4_inventory.py --preset follow-up --list-prompts
  python3 scripts/launchkey_mk4_inventory.py --preset follow-up --seconds 12 \
    --output scratch/launchkey-mk4-follow-up.json
  ```
