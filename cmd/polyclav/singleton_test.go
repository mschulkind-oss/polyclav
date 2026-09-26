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
