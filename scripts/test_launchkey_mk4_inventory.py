#!/usr/bin/env python3
"""Unit tests for launchkey_mk4_inventory.py.

Run: python3 scripts/test_launchkey_mk4_inventory.py
"""

from __future__ import annotations

import argparse
import json
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).parent))
import launchkey_mk4_inventory as inv  # noqa: E402


ACONNECT_FIXTURE = """
client 0: 'System' [type=kernel]
    0 'Timer           '
client 20: 'Unrelated Controller' [type=kernel,card=1]
    0 'Unrelated MIDI 1'
client 32: 'Launchkey MK4 61' [type=kernel,card=2]
    0 'Launchkey MK4 61 MIDI 1'
    1 'Launchkey MK4 61 MIDI 2'
client 33: 'Launchkey MK4 61' [type=kernel,card=3]
    0 'Launchkey MK4 61 MIDI 1'
    1 'Launchkey MK4 61 DAW'
"""


class PortParserTest(unittest.TestCase):
    def test_parse_launchkey_ports_preserves_addresses_and_roles(self):
        ports = inv.parse_aconnect(ACONNECT_FIXTURE)
        self.assertEqual([p.address for p in ports], ["32:0", "32:1", "33:0", "33:1"])
        self.assertEqual([p.role for p in ports], ["midi", "daw", "midi", "daw"])
        self.assertEqual(ports[1].port_name, "Launchkey MK4 61 MIDI 2")

    def test_parse_handles_duplicate_launchkey_names_without_collapsing(self):
        ports = inv.parse_aconnect(ACONNECT_FIXTURE)
        by_client = {p.client_id for p in ports}
        self.assertEqual(by_client, {32, 33})


class AseqdumpParserTest(unittest.TestCase):
    def test_parse_note_cc_pitchbend_sysex_and_realtime(self):
        roles = {"32:0": "midi", "32:1": "daw"}
        note = inv.parse_aseqdump_line("32:0   Note on                channel 0, note 60, velocity 100", roles, 10, 9)
        cc = inv.parse_aseqdump_line("32:1   Control change         channel 15, controller 85, value 65", roles, 10, 9)
        bend = inv.parse_aseqdump_line("32:0   Pitch bend             channel 0, value -123", roles, 10, 9)
        sysex = inv.parse_aseqdump_line("32:1   System exclusive       F0 00 20 29 02 0F F7", roles, 10, 9)
        clock = inv.parse_aseqdump_line("32:0   Clock", roles, 10, 9)

        self.assertEqual(note.kind, "note_on")
        self.assertEqual(note.channel, 1)
        self.assertEqual(note.port_role, "midi")
        self.assertEqual(note.raw_values["note"], 60)
        self.assertEqual(note.raw_values["note_name"], "C4")
        self.assertEqual(cc.kind, "cc")
        self.assertEqual(cc.channel, 16)
        self.assertEqual(cc.port_role, "daw")
        self.assertEqual(cc.raw_values["controller"], 85)
        self.assertEqual(cc.raw_values["value"], 65)
        self.assertEqual(bend.kind, "pitch_bend")
        self.assertEqual(bend.raw_values["value"], -123)
        self.assertEqual(sysex.kind, "sysex")
        self.assertIsNone(clock)

    def test_parse_normal_aseqdump_header_channel_column_body(self):
        roles = {"32:0": "midi", "32:1": "daw"}
        note = inv.parse_aseqdump_line("32:0   Note on                0, note 60, velocity 100", roles, 10, 9)
        cc = inv.parse_aseqdump_line("32:1   Control change         15, controller 85, value 65", roles, 10, 9)

        self.assertIsNotNone(note)
        self.assertEqual(note.channel, 1)
        self.assertEqual(note.kind, "note_on")
        self.assertIsNotNone(cc)
        self.assertEqual(cc.channel, 16)
        self.assertEqual(cc.kind, "cc")

    def test_polyphonic_pad_pressure_retains_channel_note_and_value(self):
        event = inv.parse_aseqdump_line(
            "32:1   Polyphonic aftertouch   0, note 98, value 54", {"32:1": "daw"}, 10, 9
        )
        self.assertEqual(event.kind, "poly_aftertouch")
        self.assertEqual(event.channel, 1)
        self.assertEqual(event.raw_values["note"], 98)
        self.assertEqual(event.raw_values["value"], 54)

    def test_press_release_pairing_is_evidence_not_inference(self):
        roles = {"32:1": "daw"}
        events = [
            inv.parse_aseqdump_line("32:1   Note on                channel 15, note 115, velocity 127", roles, 1, 0),
            inv.parse_aseqdump_line("32:1   Note on                channel 15, note 115, velocity 0", roles, 2, 0),
            inv.parse_aseqdump_line("32:1   Control change         channel 15, controller 45, value 127", roles, 3, 0),
            inv.parse_aseqdump_line("32:1   Control change         channel 15, controller 45, value 0", roles, 4, 0),
        ]
        summary = inv.summarize_pairs([e for e in events if e is not None])
        self.assertEqual(summary["event_count"], 4)
        self.assertEqual(summary["paired_press_release_count"], 2)


class InventoryRunTest(unittest.TestCase):
    def test_capture_failure_is_recorded_as_capture_error_not_no_event(self):
        with tempfile.TemporaryDirectory() as tmp:
            output = Path(tmp) / "report.json"
            args = argparse.Namespace(
                device_regex="Launchkey MK4",
                midi_port=None,
                daw_port=None,
                seconds=0.1,
                output=str(output),
            )
            port = inv.Port("32:0", 32, 0, "Launchkey MK4", "Launchkey MK4 MIDI 1", "midi")
            capture = inv.CaptureResult([], {"returncode": 1, "stderr": "invalid port"})

            with (
                mock.patch.object(inv, "discover_ports", return_value=[port]),
                mock.patch.object(inv, "capture_once", return_value=capture),
                mock.patch.object(inv, "prompt_choice", side_effect=["", "q"]),
            ):
                self.assertEqual(inv.run_inventory(args), 0)

            report = json.loads(output.read_text())
            first = report["controls"][0]
            self.assertEqual(first["status"], "capture_error")
            self.assertEqual(first["capture_error"], {"returncode": 1, "stderr": "invalid port"})
            self.assertEqual(first["summary"]["event_count"], 0)


class PromptListTest(unittest.TestCase):
    def test_follow_up_walks_shift_modes_and_restores_daw(self):
        prompts = list(inv.iter_controls("follow-up"))
        joined = "\n".join(item["control"] for item in prompts)
        self.assertGreaterEqual(len(prompts), 38)
        self.assertLess(len(prompts), 70)
        for number in range(1, 10):
            self.assertIn(f"Shift + fader button {number} ", joined)
        for mode in ("DAW", "Drum", "DAW Drum", "User Chord", "Arp Pattern", "Chord Map", "Custom 1", "Custom 2", "Custom 3", "Custom 4"):
            self.assertIn(f"select {mode};", joined)
        for mode in ("Plugin", "Mixer", "Sends", "Transport", "Custom 1", "Custom 2", "Custom 3", "Custom 4"):
            self.assertIn(f"encoder menu: select {mode};", joined)
        for expected in ("Shift + fader button", "Shift + pad", "DAW", "Drum", "User Chord", "Arp Pattern", "Custom 4", "aftertouch", "Scale", "Chord Map", "Arp", "Fixed Chord", "Latch"):
            self.assertIn(expected, joined)
        self.assertEqual(prompts[-1]["control"], "Return pads to DAW layout via Shift menu; press top-row pad 1 and release")

    def test_prompt_list_covers_photographed_and_previously_omitted_controls(self):
        prompts = "\n".join(item["control"] for item in inv.iter_controls())
        for expected in [
            "Pitch wheel",
            "Sustain pedal",
            "Fader button 9",
            "Display/encoder up arrow",
            "Settings",
            "Pad-bank up arrow",
            "Pad right arrow",
            "Function",
            "Scale",
            "Chord Map",
            "Arp",
            "Fixed Chord",
            "Capture MIDI",
            "Quantise",
            "Metronome",
            "Stop",
            "Loop",
            "Play",
            "Record",
        ]:
            self.assertIn(expected, prompts)


if __name__ == "__main__":
    unittest.main()
