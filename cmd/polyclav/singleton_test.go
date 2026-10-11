//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDaemonLockExcludesSecondOwnerAndReleases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polyclav.lock")

	first, err := acquireDaemonLock(path, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer first.Close()

	if _, err := acquireDaemonLock(path, 20*time.Millisecond); err == nil {
		t.Fatal("second acquire succeeded while first lock is held")
	} else if !strings.Contains(err.Error(), "another polyclav is already running") {
		t.Fatalf("second acquire error = %q, want clear singleton message", err)
	}

	if err := first.Close(); err != nil {
		t.Fatalf("close first lock: %v", err)
	}
	second, err := acquireDaemonLock(path, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("second acquire after release: %v", err)
	}
	defer second.Close()
}

func TestDaemonLockWritesOwningPID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "polyclav.lock")
	lock, err := acquireDaemonLock(path, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer lock.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read lock file: %v", err)
	}
	if got, want := strings.TrimSpace(string(data)), strconv.Itoa(os.Getpid()); got != want {
		t.Fatalf("lock file PID = %q, want %q", got, want)
	}
}

func TestDaemonLockWaitsForRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polyclav.lock")
	first, err := acquireDaemonLock(path, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	done := make(chan struct{})
	go func() {
		time.Sleep(40 * time.Millisecond)
		_ = first.Close()
		close(done)
	}()

	second, err := acquireDaemonLock(path, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("acquire after waiting for release: %v", err)
	}
	defer second.Close()
	<-done
}

// The daemon holds every candidate path, so two instances in different
// environments collide on whichever path they share — a dev shell with
// XDG_RUNTIME_DIR and a service without one still exclude each other.
func TestDaemonLocksExcludeAcrossCandidatePaths(t *testing.T) {
	paths := []string{
		filepath.Join(t.TempDir(), "runtime", "polyclav.lock"),
		filepath.Join(t.TempDir(), "tmp", "polyclav-1000.lock"),
	}
	first, err := acquireDaemonLocks(paths)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer first.Close()

	// A second instance whose candidate list is a SUBSET (no XDG_RUNTIME_DIR,
	// so only the tmp path) must still be refused, and immediately.
	start := time.Now()
	if _, err := acquireDaemonLocks(paths[1:]); err == nil {
		t.Fatal("second acquire sharing one candidate path succeeded")
	} else if !strings.Contains(err.Error(), "another polyclav is already running") {
		t.Fatalf("error = %q, want the singleton message", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("refusal took %v, want immediate", elapsed)
	}

	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	second, err := acquireDaemonLocks(paths[1:])
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	defer second.Close()
}

// A candidate that cannot be created is skipped; the rest still guard.
func TestDaemonLocksSkipUnusablePath(t *testing.T) {
	good := filepath.Join(t.TempDir(), "polyclav.lock")
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	unusable := filepath.Join(blocker, "polyclav.lock") // parent is a file

	lock, err := acquireDaemonLocks([]string{unusable, good})
	if err != nil {
		t.Fatalf("acquire with one unusable path: %v", err)
	}
	defer lock.Close()
	if len(lock.files) != 1 {
		t.Fatalf("held %d locks, want 1 (the usable path)", len(lock.files))
	}
	if _, err := acquireDaemonLocks([]string{good}); err == nil {
		t.Fatal("second acquire on the usable path succeeded")
	}
}

// The candidate list is per-uid, includes the fixed /tmp backstop, and has no
// duplicates.
func TestDaemonLockPaths(t *testing.T) {
	paths := daemonLockPaths()
	if len(paths) == 0 {
		t.Fatal("no candidate lock paths")
	}
	wantBackstop := filepath.Join("/tmp", "polyclav-"+strconv.Itoa(os.Getuid())+".lock")
	found := false
	seen := map[string]bool{}
	for _, p := range paths {
		if seen[p] {
			t.Fatalf("duplicate candidate path %q", p)
		}
		seen[p] = true
		if p == wantBackstop {
			found = true
		}
	}
	if !found {
		t.Fatalf("candidates %v missing the /tmp backstop %q", paths, wantBackstop)
	}
}

// A refused start names the holder when the lock file records one.
func TestDescribeLockHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "polyclav.lock")
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	if got := describeLockHolder(path); !strings.Contains(got, "pid "+strconv.Itoa(os.Getpid())) {
		t.Fatalf("describeLockHolder = %q, want it to name pid %d", got, os.Getpid())
	}
	if got := describeLockHolder(filepath.Join(t.TempDir(), "missing")); got != "" {
		t.Fatalf("missing-file holder = %q, want empty", got)
	}
}
