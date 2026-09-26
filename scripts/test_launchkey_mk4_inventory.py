#!/usr/bin/env python3
"""Unit tests for launchkey_mk4_inventory.py.

Run: python3 scripts/test_launchkey_mk4_inventory.py
"""

from __future__ import annotations

import sys
import unittest
from pathlib import Path

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


class PromptListTest(unittest.TestCase):
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
