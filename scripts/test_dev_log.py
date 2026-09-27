"""Tests for the persistent development lifecycle log."""

import importlib.util
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("dev_log", Path(__file__).with_name("dev_log.py"))
dev_log = importlib.util.module_from_spec(spec)
spec.loader.exec_module(dev_log)


class DevLogTest(unittest.TestCase):
    def test_process_snapshot_records_parent_group_session_and_start(self):
        with tempfile.TemporaryDirectory() as directory:
            proc = Path(directory)
            for pid, name in [(123, "polyclav"), (124, "air"), (125, "python3")]:
                folder = proc / str(pid)
                folder.mkdir()
                (folder / "cmdline").write_bytes(f"/somewhere/{name}\0--web\0on\0".encode())
                fields = ["S", "12", "34", "56"] + ["0"] * 15 + ["789"]
                (folder / "stat").write_text(f"{pid} ({name}) " + " ".join(fields))
            self.assertEqual(dev_log.process_snapshot(proc), (
                "air pid=124 ppid=12 pgid=34 sid=56 start_ticks=789",
                "polyclav pid=123 ppid=12 pgid=34 sid=56 start_ticks=789",
            ))

    def test_output_and_exit_are_kept_in_workspace_log(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            result = subprocess.run(
                [sys.executable, str(Path(dev_log.__file__)), sys.executable, "-c", "print('fixture output')"],
                cwd=root, capture_output=True, timeout=10,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            logs = list((root / ".dev-logs").glob("*.log"))
            self.assertEqual(len(logs), 1)
            self.assertIn(b"fixture output", result.stdout)
            self.assertIn(b"fixture output", logs[0].read_bytes())
            self.assertIn(b"hivemind_exit=0", logs[0].read_bytes())
            self.assertIn(b"launcher_pid=", logs[0].read_bytes())


if __name__ == "__main__":
    unittest.main()
