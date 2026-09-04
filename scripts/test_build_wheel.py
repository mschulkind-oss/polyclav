#!/usr/bin/env python3
"""Tests for build_wheel.py's --exec-wrapper packaging.

Run: python3 scripts/test_build_wheel.py (also wired into `just test`).
"""

from __future__ import annotations

import sys
import tempfile
import unittest
import zipfile
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
import build_wheel  # noqa: E402


def fake_bin(dir_: Path, name: str) -> Path:
    p = dir_ / name
    p.write_bytes(b"#!/bin/sh\n# fake polyclav ELF\n")
    return p


def wheel_entries(zf: zipfile.ZipFile) -> dict[str, tuple[bytes, int]]:
    """arcname -> (data, mode) for every entry in the wheel."""
    out: dict[str, tuple[bytes, int]] = {}
    for info in zf.infolist():
        mode = (info.external_attr >> 16) & 0o7777
        out[info.filename] = (zf.read(info.filename), mode)
    return out


class ExecWrapperTest(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self._tmp.cleanup)
        self.dir = Path(self._tmp.name)
        self.bins = [fake_bin(self.dir, "polyclav"), fake_bin(self.dir, "polyclav-components")]
        self.readme = self.dir / "README.md"
        self.readme.write_text("# polyclav test\n")
        self.out = self.dir / "dist"

    def build(self, **kw):
        defaults = dict(
            version="0.0.0.dev0",
            platform_tag="manylinux_2_39_x86_64",
            binaries=self.bins,
            readme=self.readme,
            out_dir=self.out,
        )
        defaults.update(kw)
        wheel = build_wheel.build(**defaults)
        with zipfile.ZipFile(wheel) as zf:
            return wheel_entries(zf)

    def test_default_has_no_wrapper(self):
        entries = self.build()
        self.assertIn("polyclav-0.0.0.dev0.data/scripts/polyclav", entries)
        self.assertNotIn("polyclav-0.0.0.dev0.data/scripts/polyclav.bin", entries)
        # The raw binary ships as itself, executable.
        data, mode = entries["polyclav-0.0.0.dev0.data/scripts/polyclav"]
        self.assertEqual(data, self.bins[0].read_bytes())
        self.assertEqual(mode, 0o755)

    def test_wrapper_ships_bin_and_launcher(self):
        entries = self.build(exec_wrapper=True)
        scripts = "polyclav-0.0.0.dev0.data/scripts"
        # Raw binary renamed to .bin, byte-identical and executable.
        data, mode = entries[f"{scripts}/polyclav.bin"]
        self.assertEqual(data, self.bins[0].read_bytes())
        self.assertEqual(mode, 0o755)
        # ...and the launcher is an executable shell script in its place.
        launcher, lmode = entries[f"{scripts}/polyclav"]
        self.assertTrue(launcher.startswith(b"#!/bin/sh"), launcher[:40])
        self.assertEqual(lmode, 0o755)

    def test_wrapper_probes_before_exec(self):
        entries = self.build(exec_wrapper=True)
        launcher = entries["polyclav-0.0.0.dev0.data/scripts/polyclav"][0].decode()
        # Probes its own sibling .bin (not a hardcoded path) via ldd,
        # guarded so an ldd-less system just runs the binary.
        self.assertIn('BIN="$(dirname "$0")/polyclav.bin"', launcher)
        self.assertIn("command -v ldd", launcher)
        self.assertIn('ldd "$BIN"', launcher)
        # Happy path still execs with the caller's args.
        self.assertIn('exec "$BIN" "$@"', launcher)

    def test_wrapper_names_the_fix(self):
        entries = self.build(exec_wrapper=True)
        launcher = entries["polyclav-0.0.0.dev0.data/scripts/polyclav"][0].decode()
        for expected in [
            "apt install pipewire libasound2 liblilv-0-0",
            "dnf install pipewire alsa-lib lilv",
            "pacman -S pipewire alsa-lib lilv",
            "GLIBC",  # glibc-floor failure has its own hint
        ]:
            self.assertIn(expected, launcher)

    def test_wrapper_wraps_every_binary(self):
        entries = self.build(exec_wrapper=True)
        scripts = "polyclav-0.0.0.dev0.data/scripts"
        self.assertIn(f"{scripts}/polyclav-components.bin", entries)
        comp_launcher = entries[f"{scripts}/polyclav-components"][0].decode()
        self.assertIn('polyclav-components.bin', comp_launcher)

    def test_record_lists_every_shipped_file(self):
        entries = self.build(exec_wrapper=True)
        record = entries["polyclav-0.0.0.dev0.dist-info/RECORD"][0].decode()
        for arc in entries:
            if not arc.endswith("/RECORD"):
                self.assertIn(arc.split(",", 1)[0], record)


if __name__ == "__main__":
    unittest.main()
