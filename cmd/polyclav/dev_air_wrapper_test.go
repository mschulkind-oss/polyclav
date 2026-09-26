//go:build linux

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestDevAirWrapperCleansProcessGroupOnInterrupt(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required for dev-air-wrapper regression test")
	}
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pids")
	fake := filepath.Join(dir, "fake-daemon.py")
	if err := os.WriteFile(fake, []byte(fakeDaemonPython), 0o755); err != nil {
		t.Fatalf("write fake daemon: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join("..", "..", "scripts", "dev-air-wrapper"), fake, pidFile)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start wrapper: %v", err)
	}
	defer cleanupPIDs(t, pidFile)

	pids := waitForPIDs(t, pidFile, 3)
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("signal wrapper: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wrapper wait: %v", err)
	}
	for _, pid := range pids {
		if processExistsAfter(pid, 500*time.Millisecond) {
			t.Fatalf("pid %d survived wrapper interrupt cleanup; pids=%v", pid, pids)
		}
	}
}

const fakeDaemonPython = `#!/usr/bin/env python3
import os, signal, subprocess, sys, time
pid_file = sys.argv[1]
def append_pid():
    with open(pid_file, "a", encoding="utf-8") as f:
        f.write(str(os.getpid()) + "\n")
        f.flush()
def ignore(signum, frame):
    pass
signal.signal(signal.SIGINT, ignore)
append_pid()
if os.environ.get("POLYCLAV_FAKE_CHILD") == "1":
    subprocess.Popen([sys.executable, __file__, pid_file], env={**os.environ, "POLYCLAV_FAKE_GRANDCHILD":"1"})
    while True:
        time.sleep(1)
if os.environ.get("POLYCLAV_FAKE_GRANDCHILD") == "1":
    while True:
        time.sleep(1)
subprocess.Popen([sys.executable, __file__, pid_file], env={**os.environ, "POLYCLAV_FAKE_CHILD":"1"})
while True:
    time.sleep(1)
`

func waitForPIDs(t *testing.T, path string, want int) []int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(path)
		var pids []int
		for _, line := range strings.Fields(string(data)) {
			pid, err := strconv.Atoi(line)
			if err != nil {
				t.Fatalf("bad pid %q: %v", line, err)
			}
			pids = append(pids, pid)
		}
		if len(pids) >= want {
			return pids
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d pids in %s", want, path)
	return nil
}

func processExistsAfter(pid int, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for {
		if !processExists(pid) || processIsZombie(pid) {
			return false
		}
		if !time.Now().Before(deadline) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func processExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

func processIsZombie(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	fields := strings.Fields(string(data))
	return len(fields) >= 3 && fields[2] == "Z"
}

func cleanupPIDs(t *testing.T, path string) {
	t.Helper()
	data, _ := os.ReadFile(path)
	for _, line := range strings.Fields(string(data)) {
		pid, err := strconv.Atoi(line)
		if err != nil {
			continue
		}
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}
