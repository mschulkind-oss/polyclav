"""Tests for the persistent development lifecycle log."""

import importlib.util
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time
import unittest

spec = importlib.util.spec_from_file_location("dev_log", Path(__file__).with_name("dev_log.py"))
dev_log = importlib.util.module_from_spec(spec)
spec.loader.exec_module(dev_log)


def proc_entry(proc, pid, name, start, ppid=12, pgid=34, sid=56):
    folder = proc / str(pid)
    folder.mkdir(exist_ok=True)
    (folder / "cmdline").write_bytes(f"/somewhere/{name}\0--web\0on\0".encode())
    fields = ["S", str(ppid), str(pgid), str(sid)] + ["0"] * 15 + [str(start)]
    (folder / "stat").write_text(f"{pid} ({name}) " + " ".join(fields))


class DevLogTest(unittest.TestCase):
    def test_snapshot_filters_vanishing_and_missing_proc_files(self):
        with tempfile.TemporaryDirectory() as directory:
            proc = Path(directory)
            proc_entry(proc, 123, "polyclav", 789)
            proc_entry(proc, 124, "air", 790)
            proc_entry(proc, 125, "python3", 791)
            (proc / "124" / "stat").unlink()
            (proc / "126").mkdir()
            snapshot = dev_log.process_snapshot(proc)
            self.assertEqual(set(snapshot), {(123, 789), (125, 791)})
            self.assertEqual(dev_log.describe(snapshot[123, 789]),
                             "polyclav pid=123 start_ticks=789 ppid=12 pgid=34 sid=56")
            self.assertEqual(dev_log.process_snapshot(proc / "gone"), {})

    def test_timeline_replacement_overlap_reuse_reparent_and_survivors(self):
        lines = []
        ticks = iter([1., 1.5, 2., 3., 4., 5., 6.])
        timeline = dev_log.Timeline((10, 100), lines.append, lambda: next(ticks))
        with tempfile.TemporaryDirectory() as directory:
            proc = Path(directory)

            def sample(*entries):
                for folder in proc.iterdir():
                    for file in folder.iterdir():
                        file.unlink()
                    folder.rmdir()
                for args in entries:
                    proc_entry(proc, *args)
                timeline.observe(dev_log.process_snapshot(proc))

            unrelated = (90, "polyclav", 900, 1)
            sample((10, "hivemind", 100, 1), (20, "air", 200, 10),
                   (30, "polyclav", 300, 20), unrelated)
            sample((20, "air", 200, 10), (30, "polyclav", 300, 20), unrelated)
            self.assertEqual(len(lines), 3)  # 100-ms polls with no changes stay quiet.
            sample((20, "air", 200, 10), (30, "polyclav", 300, 20),
                   (31, "polyclav", 310, 20), unrelated)
            sample((20, "air", 200, 10), (31, "polyclav", 310, 20), unrelated)
            sample((20, "air", 200, 10), (30, "polyclav", 301, 20),
                   (31, "polyclav", 310, 1240), unrelated)
            sample((30, "polyclav", 301, 1240), (31, "polyclav", 310, 1240), unrelated)
            sample((31, "polyclav", 310, 1240), unrelated)

        text = "\n".join(lines)
        self.assertIn("observed_start at=1.000 polyclav pid=30 start_ticks=300", text)
        self.assertIn("observed_disappearance at=3.000 polyclav pid=30 start_ticks=300", text)
        self.assertIn("observed_start at=4.000 polyclav pid=30 start_ticks=301", text)
        self.assertIn("observed_identity_change at=4.000 from=(polyclav pid=31", text)
        self.assertIn("ppid=1240", text)
        self.assertIn("overlap=20:200,30:300", text)
        self.assertEqual(text.count("observed_transition"), 6)
        self.assertNotIn("pid=90", text)
        survivors = timeline.survivors()
        self.assertIn("associated=polyclav pid=31 start_ticks=310", survivors)
        self.assertIn("unrelated_matches=polyclav pid=90 start_ticks=900", survivors)
        self.assertNotIn("pid=30", survivors)

    def test_hivemind_pid_reuse_does_not_claim_new_children(self):
        lines = []
        timeline = dev_log.Timeline((10, 100), lines.append, lambda: 1.0)
        root = dev_log.Process("hivemind", 10, 100, 1, 10, 10)
        air = dev_log.Process("air", 20, 200, 10, 20, 10)
        timeline.observe({dev_log.identity(p): p for p in (root, air)})
        reused = dev_log.Process("hivemind", 10, 101, 1, 10, 10)
        unrelated = dev_log.Process("air", 21, 210, 10, 21, 10)
        timeline.observe({dev_log.identity(p): p for p in (reused, unrelated)})
        self.assertNotIn("pid=21", "\n".join(lines))
        self.assertIn("unrelated_matches=air pid=21", timeline.survivors())

    def test_output_and_exit_are_kept_in_workspace_log(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            result = subprocess.run(
                [sys.executable, str(Path(dev_log.__file__)), sys.executable, "-c", "print('daemon | building...'); print('daemon | running...'); print('fixture output')"],
                cwd=root, capture_output=True, timeout=10,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            logs = list((root / ".dev-logs").glob("*.log"))
            self.assertEqual(len(logs), 1)
            self.assertIn(b"fixture output", result.stdout)
            self.assertIn(b"fixture output", logs[0].read_bytes())
            self.assertIn(b"air_output_reported at=", logs[0].read_bytes())
            self.assertEqual(logs[0].read_bytes().count(b"air_output_reported"), 2)
            self.assertNotIn(b"air_sent_signal", logs[0].read_bytes())
            self.assertIn(b"hivemind_exit=0 final_survivors associated=none", logs[0].read_bytes())

    def test_many_output_lines_in_one_read_are_all_reported(self):
        with tempfile.TemporaryDirectory() as directory:
            script = "print('daemon | building...'); print('x' * 5000); print('daemon | running...')"
            result = subprocess.run(
                [sys.executable, str(Path(dev_log.__file__)), sys.executable, "-c", script],
                cwd=directory, capture_output=True, timeout=10,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            log = next((Path(directory) / ".dev-logs").glob("*.log")).read_text()
            self.assertEqual(log.count("air_output_reported"), 2)

    def test_launcher_reports_signal_intent_not_daemon_delivery(self):
        for sig, action in [(signal.SIGINT, b"terminal_group_expected_no_forward"),
                            (signal.SIGTERM, b"forward_to_hivemind_requested")]:
            with self.subTest(sig=sig), tempfile.TemporaryDirectory() as directory:
                # For SIGINT a direct signal to the launcher is intentionally NOT
                # forwarded; terminate the child separately to finish the fixture.
                proc = subprocess.Popen(
                    [sys.executable, str(Path(dev_log.__file__)), sys.executable, "-c",
                     "import time; time.sleep(10)"], cwd=directory,
                    stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                )
                try:
                    logdir = Path(directory) / ".dev-logs"
                    deadline = time.monotonic() + 5
                    while not list(logdir.glob("*.log")) or b"hivemind_pid=" not in list(logdir.glob("*.log"))[0].read_bytes():
                        self.assertLess(time.monotonic(), deadline)
                        time.sleep(0.01)
                    proc.send_signal(sig)
                    if sig == signal.SIGINT:
                        # Read child PID from the recorded launcher line.
                        import os
                        content = list(logdir.glob("*.log"))[0].read_text()
                        pid = int(content.split("hivemind_pid=")[1].split()[0])
                        os.kill(pid, signal.SIGTERM)
                    proc.communicate(timeout=5)
                    log = list(logdir.glob("*.log"))[0].read_bytes()
                    self.assertIn(b"launcher_received=" + sig.name.encode(), log)
                    self.assertIn(action, log)
                    self.assertIn(b"hivemind_exit=", log)
                    self.assertNotIn(b"air_sent_signal", log)
                finally:
                    if proc.poll() is None:
                        proc.kill()
                        proc.communicate()


if __name__ == "__main__":
    unittest.main()
