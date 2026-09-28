#!/usr/bin/env python3
"""Keep a workspace log of the dev loop without changing process groups."""

from __future__ import annotations

from collections import namedtuple
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
import threading
import time

LOG_DIR = ".dev-logs"
POLL_SECONDS = 0.1
Process = namedtuple("Process", "name pid start_ticks ppid pgid sid")
AIR_MESSAGE = re.compile(r"\b(building|built|running|killing|watching|failed to build)\b", re.I)


def identity(process: Process) -> tuple[int, int]:
    return process.pid, process.start_ticks


def describe(process: Process) -> str:
    return (f"{process.name} pid={process.pid} start_ticks={process.start_ticks} "
            f"ppid={process.ppid} pgid={process.pgid} sid={process.sid}")


def process_snapshot(proc: Path = Path("/proc")) -> dict[tuple[int, int], Process]:
    """Read process identities and ancestry, never arguments or environment."""
    snapshot = {}
    try:
        entries = list(proc.iterdir())
    except OSError:
        return snapshot
    for entry in entries:
        if not entry.name.isdigit():
            continue
        try:
            stat = (entry / "stat").read_text()
            fields = stat[stat.rfind(")") + 2:].split()
            # stat fields after comm: state, ppid, pgrp, session, ... starttime.
            ppid, pgid, sid, start = map(int, (fields[1], fields[2], fields[3], fields[19]))
            raw = (entry / "cmdline").read_bytes().split(b"\0", 1)[0]
            name = Path(os.fsdecode(raw)).name if raw else ""
            snapshot[(int(entry.name), start)] = Process(name, int(entry.name), start, ppid, pgid, sid)
        except (OSError, IndexError, ValueError):
            # A process can exit, or /proc can become unreadable, during a scan.
            continue
    return snapshot


class Timeline:
    """Track only identities seen under this run's Hivemind/Air ancestry."""

    def __init__(self, hivemind_pid: int, emit, clock=time.time):
        self.hivemind_pid = hivemind_pid
        self.emit = emit
        self.clock = clock
        self.previous: dict[tuple[int, int], Process] = {}
        self.associated: set[tuple[int, int]] = set()
        self.window = 0

    def observe(self, snapshot: dict[tuple[int, int], Process]) -> None:
        now = self.clock()
        by_pid = {p.pid: p for p in snapshot.values()}
        associated = set(self.associated & snapshot.keys())
        # Follow parent chains at each poll, including intermediary build/shell
        # processes, but never infer ownership from a matching executable name.
        for process in snapshot.values():
            seen = set()
            parent = process.ppid
            while parent != self.hivemind_pid and parent in by_pid and parent not in seen:
                seen.add(parent)
                ancestor = by_pid[parent]
                if identity(ancestor) in associated:
                    break
                parent = ancestor.ppid
            if parent == self.hivemind_pid or (parent in by_pid and identity(by_pid[parent]) in associated):
                associated.add(identity(process))
        self.associated |= associated
        before = {k: v for k, v in self.previous.items() if k in self.associated and v.name in {"air", "polyclav"}}
        after = {k: v for k, v in snapshot.items() if k in associated and v.name in {"air", "polyclav"}}
        removed = before.keys() - after.keys()
        added = after.keys() - before.keys()
        if removed or added:
            self.window += 1
            self.emit(f"observed_transition at={now:.3f} window={self.window} "
                      f"old={','.join(f'{k[0]}:{k[1]}' for k in sorted(removed)) or 'none'} "
                      f"new={','.join(f'{k[0]}:{k[1]}' for k in sorted(added)) or 'none'} "
                      f"overlap={','.join(f'{k[0]}:{k[1]}' for k in sorted(before.keys() & after.keys())) or 'none'}")
        for key in sorted(removed):
            self.emit(f"observed_disappearance at={now:.3f} {describe(before[key])}")
        for key in sorted(added):
            if any(old.pid == key[0] for old in before.values()):
                self.emit(f"observed_identity_change at={now:.3f} {describe(after[key])}")
            self.emit(f"observed_start at={now:.3f} {describe(after[key])}")
        for key in sorted(before.keys() & after.keys()):
            if before[key] != after[key]:
                self.emit(f"observed_identity_change at={now:.3f} from=({describe(before[key])}) to=({describe(after[key])})")
        self.previous = snapshot

    def survivors(self) -> str:
        associated = [p for k, p in self.previous.items() if k in self.associated and p.name in {"air", "polyclav"}]
        unrelated = [p for k, p in self.previous.items() if k not in self.associated and p.name in {"air", "polyclav"}]
        return ("associated=" + ("; ".join(map(describe, sorted(associated))) or "none") +
                " unrelated_matches=" + ("; ".join(map(describe, sorted(unrelated))) or "none"))


def run(command: list[str], log_dir: Path) -> int:
    log_dir.mkdir(parents=True, exist_ok=True)
    path = log_dir / (time.strftime("%Y%m%d-%H%M%S") + f"-{os.getpid()}.log")
    with path.open("ab", buffering=0) as logfile:
        lock = threading.Lock()
        child: subprocess.Popen[bytes] | None = None
        finished = threading.Event()

        def record(message: str) -> None:
            line = f"[dev-log {time.strftime('%Y-%m-%dT%H:%M:%S%z')}] {message}\n".encode()
            with lock:
                logfile.write(line)
                sys.stdout.buffer.write(line)
                sys.stdout.buffer.flush()

        def on_signal(signum: int, _frame: object) -> None:
            # Avoid reentering Python's buffered stdout while it is writing.
            name = signal.Signals(signum).name
            action = "terminal_group_expected_no_forward" if signum == signal.SIGINT else "forward_to_hivemind_requested"
            line = f"[dev-log {time.strftime('%Y-%m-%dT%H:%M:%S%z')}] launcher_received={name} action={action} launcher_pid={os.getpid()}\n".encode()
            os.write(logfile.fileno(), line)
            os.write(sys.stdout.fileno(), line)
            # Ctrl-C from the terminal is sent to the entire foreground group.
            # Direct SIGTERM is forwarded because only this process may receive it.
            if signum == signal.SIGTERM and child is not None and child.poll() is None:
                try:
                    child.send_signal(signum)
                except ProcessLookupError:
                    pass

        signal.signal(signal.SIGINT, on_signal)
        signal.signal(signal.SIGTERM, on_signal)
        record(f"start launcher_pid={os.getpid()} pgid={os.getpgrp()} log={path} command={' '.join(command)}")
        try:
            child = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        except OSError as exc:
            record(f"start failed: {exc}")
            return 1
        try:
            child_group = os.getpgid(child.pid)
        except ProcessLookupError:
            child_group = "exited"
        record(f"hivemind_pid={child.pid} pgid={child_group}")
        timeline = Timeline(child.pid, record)

        def monitor() -> None:
            while not finished.is_set():
                timeline.observe(process_snapshot())
                finished.wait(POLL_SECONDS)

        watcher = threading.Thread(target=monitor, daemon=True)
        watcher.start()
        assert child.stdout is not None
        pending = b""
        try:
            while chunk := os.read(child.stdout.fileno(), 65536):
                with lock:
                    logfile.write(chunk)
                    sys.stdout.buffer.write(chunk)
                    sys.stdout.buffer.flush()
                # Process every complete line before bounding the unfinished tail.
                # A single pipe read may contain many lines and exceed 4096 bytes.
                lines = (pending + chunk).split(b"\n")
                pending = lines.pop()[-4096:]
                for line in lines:
                    text = line.decode(errors="replace")
                    if "daemon" in text.lower() and AIR_MESSAGE.search(text):
                        record(f"air_output_reported at={time.time():.3f} message={text[:512]!r}")
            result = child.wait()
        finally:
            finished.set()
            watcher.join()
            child.stdout.close()
        timeline.observe(process_snapshot())
        record(f"hivemind_exit={result} final_survivors {timeline.survivors()}")
        return result


if __name__ == "__main__":
    if len(sys.argv) < 2:
        raise SystemExit("usage: dev_log.py <command> [args...]")
    raise SystemExit(run(sys.argv[1:], Path(os.environ.get("POLYCLAV_DEV_LOG_DIR", LOG_DIR))))
