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

// This is a no-audio fixture: the fake daemon only writes PIDs and ignores
// SIGINT, so Air must exercise its bounded SIGKILL cleanup on reload and exit.
func TestDevJustHivemindAirReloadAndInterrupt(t *testing.T) {
	airPath, err := exec.LookPath("air")
	if err != nil {
		t.Skip("Air not installed")
	}
	if resolved, err := exec.Command("mise", "which", "air").Output(); err == nil {
		airPath = strings.TrimSpace(string(resolved))
	}
	if _, err := exec.LookPath("hivemind"); err != nil {
		t.Skip("Hivemind not installed")
	}
	justPath, err := exec.LookPath("just")
	if err != nil {
		t.Skip("just not installed")
	}
	if resolved, err := exec.Command("mise", "which", "just").Output(); err == nil {
		justPath = strings.TrimSpace(string(resolved))
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("Python not installed")
	}
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pids")
	fake := filepath.Join(dir, "fake-daemon.py")
	if err := os.WriteFile(fake, []byte(fakeDaemonPython), 0o755); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join("..", "..", ".air.toml"))
	if err != nil {
		t.Fatal(err)
	}
	config := string(src)
	for _, replacement := range [][2]string{
		{`cmd = "just build-bin"`, `cmd = "true"`},
		{`bin = "./bin/polyclav"`, fmt.Sprintf(`bin = %q`, fake)},
		{`args_bin = ["--web", "on"]`, fmt.Sprintf(`args_bin = [%q]`, pidFile)},
		{`kill_delay = "4s"`, `kill_delay = "1s"`},
	} {
		if !strings.Contains(config, replacement[0]) {
			t.Fatalf("Air config no longer matches fixture: %s", replacement[0])
		}
		config = strings.Replace(config, replacement[0], replacement[1], 1)
	}
	if err := os.WriteFile(filepath.Join(dir, ".air.toml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fixture.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Procfile.dev"), []byte("daemon: exec "+airPath+" -c .air.toml\nweb: exec sleep 3600\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	launcher, err := os.ReadFile(filepath.Join("..", "..", "scripts", "dev_log.py"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "scripts", "dev_log.py"), launcher, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Justfile"), []byte("dev:\n    exec python3 scripts/dev_log.py hivemind Procfile.dev\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	logFile, err := os.Create(filepath.Join(dir, "supervisor.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	defer func() {
		if t.Failed() {
			data, _ := os.ReadFile(logFile.Name())
			t.Logf("supervisor log:\n%s", data)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, justPath, "dev")
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); cleanupPIDs(t, pidFile) }()
	first := waitForPIDs(t, pidFile, 3)
	// A watched Go edit triggers an Air restart; the three old PIDs must go.
	if err := os.WriteFile(filepath.Join(dir, "fixture.go"), []byte("package main\n// reload\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	all := waitForPIDs(t, pidFile, 6)
	for _, pid := range first {
		if processExistsAfter(pid, 3*time.Second) {
			t.Fatalf("reload left PID %d running (all=%v)", pid, all)
		}
	}
	// Terminal Ctrl-C signals the foreground group, including just's shell
	// and Hivemind; signaling only the Hivemind PID is a weaker test.
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil && ctx.Err() != nil {
		t.Fatalf("just dev did not stop on SIGINT: %v", err)
	}
	for _, pid := range all {
		if processExistsAfter(pid, 3*time.Second) {
			t.Fatalf("Ctrl-C left PID %d running (all=%v)", pid, all)
		}
	}
	logs, err := filepath.Glob(filepath.Join(dir, ".dev-logs", "*.log"))
	if err != nil || len(logs) != 1 {
		t.Fatalf("persistent dev logs = %v, err = %v", logs, err)
	}
	logged, err := os.ReadFile(logs[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"hivemind_pid=", "air pid=", "hivemind_exit="} {
		if !strings.Contains(string(logged), expected) {
			t.Errorf("persistent dev log missing %q", expected)
		}
	}
	if !strings.Contains(string(logged), "signal=SIGINT") && !strings.Contains(string(logged), "launcher_received=SIGINT") {
		t.Errorf("persistent dev log missing SIGINT notification")
	}
	if !strings.Contains(string(logged), "processes:") && !strings.Contains(string(logged), "observed_") {
		t.Errorf("persistent dev log missing process lifecycle observations")
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
	deadline := time.Now().Add(5 * time.Second)
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
		if err == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}
