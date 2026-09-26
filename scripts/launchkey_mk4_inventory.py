#!/usr/bin/env python3
"""Guided, read-only Launchkey MK4 control inventory.

The script only runs ALSA read-side commands (`aconnect -l`, `aseqdump -p`). It
never sends MIDI, never opens audio, and never changes PipeWire/ALSA state.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import select
import subprocess
import sys
import time
from dataclasses import asdict, dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Callable, Iterable

NOTE_NAMES = ["C", "C#", "D", "D#", "E", "F", "F#", "G", "G#", "A", "A#", "B"]
CLOCK_WORDS = ("Clock", "Active Sensing", "Tick", "Sensing")


@dataclass(frozen=True)
class Port:
    address: str
    client_id: int
    port_id: int
    client_name: str
    port_name: str
    role: str


@dataclass(frozen=True)
class MidiEvent:
    timestamp: str
    elapsed_ms: int
    source: str | None
    port_role: str | None
    kind: str
    channel: int | None
    raw_line: str
    raw_values: dict[str, int | str]


@dataclass(frozen=True)
class CaptureResult:
    events: list[MidiEvent]
    error: dict[str, int | str] | None = None


def note_name(num: int) -> str:
    return f"{NOTE_NAMES[num % 12]}{num // 12 - 1}"


def parse_aconnect(text: str, device_re: str = "Launchkey MK4") -> list[Port]:
    ports: list[Port] = []
    current_id: int | None = None
    current_name = ""
    wanted = re.compile(device_re, re.I)
    client_re = re.compile(r"^client\s+(\d+):\s+'([^']+)'", re.I)
    port_re = re.compile(r"^\s*(\d+)\s+'([^']+)'", re.I)

    for line in text.splitlines():
        if m := client_re.match(line):
            current_id = int(m.group(1))
            current_name = m.group(2)
            continue
        if current_id is None:
            continue
        if not wanted.search(current_name) and not wanted.search(line):
            continue
        if m := port_re.match(line):
            port_id = int(m.group(1))
            port_name = m.group(2)
            ports.append(
                Port(
                    address=f"{current_id}:{port_id}",
                    client_id=current_id,
                    port_id=port_id,
                    client_name=current_name,
                    port_name=port_name,
                    role=classify_port(port_id, port_name),
                )
            )
    return ports


def classify_port(port_id: int, port_name: str) -> str:
    lowered = port_name.lower()
    if "daw" in lowered or "control" in lowered or "midi 2" in lowered or port_id == 1:
        return "daw"
    if "midi" in lowered or port_id == 0:
        return "midi"
    return "unknown"


def discover_ports(run: Callable[..., subprocess.CompletedProcess[str]], device_re: str) -> list[Port]:
    proc = run(["aconnect", "-l"], check=True, text=True, capture_output=True)
    return parse_aconnect(proc.stdout, device_re)


def parse_aseqdump_line(
    line: str, role_by_source: dict[str, str] | None = None, now: float | None = None, started: float | None = None
) -> MidiEvent | None:
    raw = line.rstrip("\n")
    stripped = raw.strip()
    if not stripped or stripped.startswith(("Source", "Waiting")):
        return None
    if any(word in stripped for word in CLOCK_WORDS):
        # Realtime clock/active-sensing noise is intentionally ignored.
        return None

    timestamp = datetime.now(timezone.utc).isoformat()
    elapsed_ms = int(((now if now is not None else time.monotonic()) - (started if started is not None else time.monotonic())) * 1000)
    role_by_source = role_by_source or {}

    m = re.match(r"^(\d+:\d+)\s+(.+?)\s*$", stripped)
    source = m.group(1) if m else None
    body = m.group(2) if m else stripped
    kind = "raw"
    channel: int | None = None
    values: dict[str, int | str] = {}

    channel_match = re.search(r"\bchannel\s+(\d+)\b|\bCh\s+(\d+)\b", body, re.I)
    if channel_match:
        # ALSA aseqdump prints channels as 0-based. Store human MIDI channel 1-16.
        channel = int(channel_match.group(1) or channel_match.group(2)) + 1
    else:
        # Normal aseqdump rows use a header column named "Ch" and then place
        # the 0-based MIDI channel as the first comma-separated value in the
        # event body, e.g. "Note on 0, note 60, velocity 100".
        leading_channel = re.match(
            r"^(?:Note on|Note off|Control change|Controller|Pitch bend|Polyphonic aftertouch|Key pressure)\s+(-?\d+)\s*,", body, re.I
        )
        if leading_channel:
            raw_channel = int(leading_channel.group(1))
            if 0 <= raw_channel <= 15:
                channel = raw_channel + 1

    if re.search(r"Note on", body, re.I):
        kind = "note_on"
    elif re.search(r"Note off", body, re.I):
        kind = "note_off"
    elif re.search(r"Control change|Controller", body, re.I):
        kind = "cc"
    elif re.search(r"Polyphonic aftertouch|Key pressure", body, re.I):
        kind = "poly_aftertouch"
    elif re.search(r"Pitch bend", body, re.I):
        kind = "pitch_bend"
    elif re.search(r"System exclusive|SysEx", body, re.I):
        kind = "sysex"
    elif re.search(r"Start|Continue|Stop|Song|Tune|Reset|Quarter frame", body, re.I):
        kind = "realtime_or_system"

    for key, patterns in {
        "note": [r"note\s+(\d+)"],
        "velocity": [r"velocity\s+(\d+)", r"vel\s+(\d+)"],
        "controller": [r"controller\s+(\d+)", r"param\s+(\d+)"],
        "value": [r"value\s+(-?\d+)", r"val\s+(-?\d+)"],
    }.items():
        for pattern in patterns:
            found = re.search(pattern, body, re.I)
            if found:
                values[key] = int(found.group(1))
                break
    if "note" in values:
        values["note_name"] = note_name(int(values["note"]))

    return MidiEvent(
        timestamp=timestamp,
        elapsed_ms=max(0, elapsed_ms),
        source=source,
        port_role=role_by_source.get(source or ""),
        kind=kind,
        channel=channel,
        raw_line=raw,
        raw_values=values,
    )


def summarize_pairs(events: list[MidiEvent]) -> dict[str, object]:
    pairs: list[dict[str, object]] = []
    open_events: dict[tuple[str | None, str, int | None, int | None], MidiEvent] = {}
    for event in events:
        num = event.raw_values.get("note") or event.raw_values.get("controller")
        key = (event.source, event.kind, event.channel, int(num) if isinstance(num, int) else None)
        value = event.raw_values.get("velocity", event.raw_values.get("value"))
        if event.kind == "note_on" and isinstance(value, int) and value > 0:
            open_events[key] = event
        elif event.kind in ("note_off", "note_on") and isinstance(value, int) and value == 0:
            press = open_events.pop((event.source, "note_on", event.channel, key[3]), None)
            if press:
                pairs.append({"press": asdict(press), "release": asdict(event)})
        elif event.kind == "cc" and isinstance(value, int) and value > 0:
            open_events[key] = event
        elif event.kind == "cc" and isinstance(value, int) and value == 0:
            press = open_events.pop(key, None)
            if press:
                pairs.append({"press": asdict(press), "release": asdict(event)})
    return {"event_count": len(events), "paired_press_release_count": len(pairs), "pairs": pairs}


CONTROL_GROUPS: list[tuple[str, list[str]]] = [
    ("Performance: keybed/wheels/pedal", [
        "Keybed: play and release one low key, one middle key, and one high key",
        "Pitch wheel: move down, center, up, center",
        "Modulation wheel: move min to max and back",
        "Sustain pedal: press and release if connected; otherwise skip",
        "Expression/other pedal: move/press if connected; otherwise skip",
        "Octave minus: press/release, then play one key to check note-number shift",
        "Octave plus: press/release, then play one key to check note-number shift",
    ]),
    ("Faders and fader buttons", [
        *[f"Fader {i}: move min to max to middle" for i in range(1, 10)],
        "Fader button 1 / Volume: press and release",
        "Fader button 2 / Custom 1: press and release",
        "Fader button 3 / Custom 2: press and release",
        "Fader button 4 / Custom 3: press and release",
        "Fader button 5 / Custom 4: press and release",
        "Fader button 6 / Part A: press and release",
        "Fader button 7 / Part B: press and release",
        "Fader button 8 / Zones/Split: press and release",
        "Fader button 9 / Layer or Arm-Select area: press and release",
    ]),
    ("Encoders, display, settings, navigation", [
        *[f"Encoder {i}: rotate one tick left, then one tick right" for i in range(1, 9)],
        "Track left: press and release",
        "Track right: press and release",
        "Display/encoder up arrow: press and release",
        "Display/encoder down arrow: press and release",
        "Shift: press and release",
        "Settings: press and release; exit any menu without changing settings",
    ]),
    ("Pads and pad-mode controls", [
        *[f"Pad top row {i}: press and release" for i in range(1, 9)],
        *[f"Pad bottom row {i}: press and release" for i in range(1, 9)],
        "Pad-bank up arrow: press and release",
        "Pad-bank down arrow: press and release",
        "Pad right arrow: press and release",
        "Function: press and release",
        "Pad mode DAW: press and release",
        "Pad mode Drum: press and release",
        "Pad mode User Chord: press and release",
        "Pad mode Arp Pattern: press and release",
        "Pad mode Custom 1: press and release",
        "Pad mode Custom 2: press and release",
        "Pad mode Custom 3: press and release",
        "Pad mode Custom 4: press and release",
    ]),
    ("Scale, chord, and arp block", [
        "Scale: press and release; if silent, play one key afterward",
        "Chord Map: press and release; if silent, play one key afterward",
        "Arp: press and release; if silent, play one key afterward",
        "Fixed Chord: press and release; if silent, play one key afterward",
        "Latch shift-layer if accessible: press and release; if silent, play one key afterward",
    ]),
    ("DAW workflow buttons", [
        "Capture MIDI: press and release",
        "Undo: press and release",
        "Shift + Undo / Redo: press Shift, press Undo, release Undo, release Shift",
        "Quantise: press and release",
        "Metronome: press and release",
    ]),
    ("Transport", [
        "Stop: press and release",
        "Loop: press and release",
        "Play: press and release",
        "Record: press and release",
        "Rewind if present or shift-layer accessible: press and release; otherwise skip",
        "Fast-forward if present or shift-layer accessible: press and release; otherwise skip",
    ]),
]


# Each capture is passive: the operator enters modes with the keyboard's own
# buttons and returns to DAW layout afterward. Mode labels are not extra
# physical buttons. Longer windows allow a selection followed by a pad/key.
FOLLOW_UP_GROUPS: list[tuple[str, list[str]]] = [
    ("Shift and layout menus", [
        "Shift + fader button 1 (Volume): hold Shift, tap button, release Shift; move fader 1",
        "Shift + fader button 2 (Custom 1): hold Shift, tap button, release Shift; move fader 1",
        "Shift + fader button 5 (Custom 4): hold Shift, tap button, release Shift; move fader 1",
        "Shift + fader button 1 (Volume): restore Volume fader layout",
        "Shift + encoder layout selection: select Plugin, rotate encoder 1; then select another available encoder mode and rotate encoder 1",
        "Shift + encoder layout selection: restore Plugin mode",
    ]),
    ("Pad modes: enter using Shift menu and press one pad", [
        *[f"Shift + pad menu: select {mode}; release Shift, press top-row pad 1, hold for aftertouch, release"
          for mode in ("DAW", "Drum", "User Chord", "Arp Pattern", "Custom 1", "Custom 2", "Custom 3", "Custom 4")],
        "Shift + pad menu: restore DAW layout before pressure capture",
        "Pad aftertouch: in DAW layout press top-row pad 3 lightly, increase pressure, then release",
    ]),
    ("Feature toggles and key output", [
        "Scale: press once, play and release same key; press again, play and release same key",
        "Chord Map: select mode, press a pad and play/release one key; leave Chord Map mode",
        "Arp: press once, play and release a key; press again to turn off (no audio)",
        "Fixed Chord: hold button and play key if required to define chord; then play/release one key; clear if changed",
        "Latch: with Arp active, use Shift + Arp or labeled Latch button; play/release a key, toggle off and stop Arp",
        "Shift + Undo / Redo: hold Shift, tap Undo, release Shift; compare with ordinary Undo",
        "Shift + transport rewind and fast-forward if physically labeled; otherwise skip",
        "Return pads to DAW layout via Shift menu; press top-row pad 1 and release",
    ]),
]


def iter_controls(preset: str = "full") -> Iterable[dict[str, str]]:
    groups = FOLLOW_UP_GROUPS if preset == "follow-up" else CONTROL_GROUPS
    for group, controls in groups:
        for control in controls:
            yield {"group": group, "control": control}


def capture_once(port_csv: str, role_by_source: dict[str, str], seconds: float) -> CaptureResult:
    proc = subprocess.Popen(
        ["aseqdump", "-p", port_csv], stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, bufsize=1
    )
    events: list[MidiEvent] = []
    started = time.monotonic()
    try:
        while time.monotonic() - started < seconds:
            if proc.poll() is not None:
                break
            if proc.stdout is None:
                break
            ready, _, _ = select.select([proc.stdout], [], [], 0.1)
            if not ready:
                continue
            line = proc.stdout.readline()
            if not line:
                break
            event = parse_aseqdump_line(line, role_by_source, time.monotonic(), started)
            if event:
                events.append(event)
                print(format_event(event))
    finally:
        if proc.poll() is None:
            proc.terminate()
        try:
            return_code = proc.wait(timeout=1)
        except subprocess.TimeoutExpired:
            proc.kill()
            return_code = proc.wait(timeout=1)

    stderr = ""
    if proc.stderr is not None:
        stderr = proc.stderr.read().strip()
    if return_code != 0:
        return CaptureResult(events, {"returncode": return_code, "stderr": stderr})
    return CaptureResult(events)


def format_event(event: MidiEvent) -> str:
    role = event.port_role or event.source or "unknown"
    values = " ".join(f"{k}={v}" for k, v in event.raw_values.items())
    ch = f" ch={event.channel}" if event.channel else ""
    return f"  [{event.elapsed_ms:05d}ms {role}] {event.kind}{ch} {values}".rstrip()


def prompt_choice(prompt: str) -> str:
    return input(prompt).strip().lower()


def run_inventory(args: argparse.Namespace) -> int:
    run = subprocess.run
    ports = discover_ports(run, args.device_regex)
    if args.midi_port:
        ports.append(Port(args.midi_port, -1, -1, "override", "override MIDI", "midi"))
    if args.daw_port:
        ports.append(Port(args.daw_port, -1, -1, "override", "override DAW", "daw"))

    # Prefer explicit overrides for each role, then discovered ports.
    by_role: dict[str, Port] = {}
    for role, override in (("midi", args.midi_port), ("daw", args.daw_port)):
        if override:
            by_role[role] = Port(override, -1, -1, "override", f"override {role}", role)
    for port in ports:
        by_role.setdefault(port.role, port)

    capture_ports = [p for role, p in by_role.items() if role in {"midi", "daw"}]
    if not capture_ports:
        print("error: no Launchkey MIDI/DAW ports found; pass --midi-port and/or --daw-port", file=sys.stderr)
        return 2

    role_by_source = {p.address: p.role for p in capture_ports}
    port_csv = ",".join(p.address for p in capture_ports)
    report = {
        "schema": "polyclav.launchkey_mk4_inventory.v1",
        "created_at": datetime.now(timezone.utc).isoformat(),
        "safety": "Read-only ALSA capture via aseqdump; no MIDI/audio/control messages are sent.",
        "device_regex": args.device_regex,
        "preset": getattr(args, "preset", "full"),
        "ports": [asdict(p) for p in ports],
        "capture_ports": [asdict(p) for p in capture_ports],
        "controls": [],
    }

    print("Launchkey MK4 guided inventory (read-only).")
    print(f"Capturing ALSA source(s): {port_csv}")
    print("For each prompt: Enter=capture, s=skip/no physical control, q=save and quit.")
    print("Do not infer device-local behavior from silence; the report records no-event evidence.\n")

    for item in iter_controls(getattr(args, "preset", "full")):
        print(f"\n[{item['group']}] {item['control']}")
        choice = prompt_choice(f"Press Enter when ready for a {args.seconds:.1f}s capture (s/q): ")
        record: dict[str, object] = {**item, "status": "captured", "events": []}
        if choice == "q":
            break
        if choice == "s":
            record["status"] = "skipped"
            record["summary"] = {"event_count": 0, "paired_press_release_count": 0, "pairs": []}
            report["controls"].append(record)
            continue
        capture = capture_once(port_csv, role_by_source, args.seconds)
        events = capture.events
        record["events"] = [asdict(e) for e in events]
        record["status"] = "capture_error" if capture.error else ("no_event" if not events else "captured")
        if capture.error:
            record["capture_error"] = capture.error
        record["summary"] = summarize_pairs(events)
        if capture.error:
            print(f"  capture error from aseqdump: {capture.error}")
        elif not events:
            print("  no MIDI event observed in this capture window")
        report["controls"].append(record)

    out = Path(args.output)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps(report, indent=2) + "\n")
    print(f"\nSaved machine-readable report: {out}")
    return 0


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--device-regex", default="Launchkey MK4", help="regex used to find ALSA Launchkey ports")
    parser.add_argument("--midi-port", help="override performance/MIDI ALSA source, e.g. 32:0")
    parser.add_argument("--daw-port", help="override DAW/control ALSA source, e.g. 32:1")
    parser.add_argument("--seconds", type=float, default=5.0, help="capture duration per prompted control")
    parser.add_argument("--output", default="scratch/launchkey-mk4-inventory.json", help="JSON report path")
    parser.add_argument("--preset", choices=("full", "follow-up"), default="full", help="follow-up captures Shift/menu/mode behavior without repeating all 83 controls")
    parser.add_argument("--list-prompts", action="store_true", help="print the deterministic prompt list and exit")
    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    if args.list_prompts:
        for item in iter_controls(args.preset):
            print(f"{item['group']}\t{item['control']}")
        return 0
    return run_inventory(args)


if __name__ == "__main__":
    raise SystemExit(main())
