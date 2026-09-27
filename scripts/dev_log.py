#!/usr/bin/env python3
"""Keep a workspace log of the dev loop without changing process groups."""

from __future__ import annotations

import os
from pathlib import Path
import signal
import subprocess
import sys
import threading
import time

LOG_DIR = ".dev-logs"


def process_snapshot(proc: Path = Path("/proc")) -> tuple[str, ...]:
    """Record identities, not environment or command arguments (which may be secret)."""
    entries = []
    for entry in proc.iterdir():
        if not entry.name.isdigit():
            continue
        try:
            raw = (entry / "cmdline").read_bytes().split(b"\0")
            if not raw or not raw[0]:
                continue
            name = Path(os.fsdecode(raw[0])).name
            # Include the Air process and the daemon. Exclude other processes
            # with polyclav in an argument (editors, tests, shells, etc.).
            if name not in {"air", "polyclav"}:
                continue
            stat = (entry / "stat").read_text()
            fields = stat[stat.rfind(")") + 2 :].split()
            # stat fields after comm: state, ppid, pgrp, session, ... starttime.
            entries.append(f"{name} pid={entry.name} ppid={fields[1]} pgid={fields[2]} sid={fields[3]} start_ticks={fields[19]}")
        except (OSError, IndexError, ValueError):
            # A process can exit at any point during a /proc scan.
            continue
    return tuple(sorted(entries))


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
            line = f"[dev-log {time.strftime('%Y-%m-%dT%H:%M:%S%z')}] signal={signal.Signals(signum).name} launcher_pid={os.getpid()}\n".encode()
            os.write(logfile.fileno(), line)
            os.write(sys.stdout.fileno(), line)
            # Ctrl-C from the terminal is sent to our entire foreground group,
            # including Hivemind. Do not send it twice. Direct SIGTERM is
            # forwarded because only this process may have received it.
            if signum == signal.SIGTERM and child is not None and child.poll() is None:
                child.send_signal(signum)

        signal.signal(signal.SIGINT, on_signal)
        signal.signal(signal.SIGTERM, on_signal)
        record(f"start launcher_pid={os.getpid()} pgid={os.getpgrp()} log={path} command={' '.join(command)}")
        try:
            # No setsid/Setpgid: Hivemind must remain in the terminal's group.
            child = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        except OSError as exc:
            record(f"start failed: {exc}")
            return 1
        try:
            child_group = os.getpgid(child.pid)
        except ProcessLookupError:
            # Hivemind may fail before we can sample its process group.
            child_group = "exited"
        record(f"hivemind_pid={child.pid} pgid={child_group}")

        def monitor() -> None:
            previous: tuple[str, ...] | None = None
            while not finished.is_set():
                current = process_snapshot()
                if current != previous:
                    record("processes: " + ("; ".join(current) or "none"))
                    previous = current
                finished.wait(0.5)

        watcher = threading.Thread(target=monitor, daemon=True)
        watcher.start()
        assert child.stdout is not None
        try:
            while chunk := os.read(child.stdout.fileno(), 65536):
                with lock:
                    logfile.write(chunk)
                    sys.stdout.buffer.write(chunk)
                    sys.stdout.buffer.flush()
            result = child.wait()
        finally:
            finished.set()
            watcher.join()
            child.stdout.close()
        record(f"hivemind_exit={result} remaining_processes=" + ("; ".join(process_snapshot()) or "none"))
        return result


if __name__ == "__main__":
    if len(sys.argv) < 2:
        raise SystemExit("usage: dev_log.py <command> [args...]")
    raise SystemExit(run(sys.argv[1:], Path(os.environ.get("POLYCLAV_DEV_LOG_DIR", LOG_DIR))))
