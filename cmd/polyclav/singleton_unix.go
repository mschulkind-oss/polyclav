//go:build unix

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// errLockHeld marks the "another process owns this lock" case, kept distinct
// from "this lock path is unusable" so acquireDaemonLocks can skip an
// unusable path but refuse on a held one.
var errLockHeld = errors.New("another polyclav is already running")

// daemonLock holds every lock file the daemon could create. Holding more than
// one is what makes the guard survive environment differences — see
// daemonLockPaths.
type daemonLock struct {
	files []*os.File
}

// daemonLockPaths returns every candidate singleton lock for this user,
// highest-preference first. The daemon holds ALL of them it can create, so
// two instances launched from different environments — a login shell with
// XDG_RUNTIME_DIR set versus a service that has none — still contend on at
// least one shared file. The lock is keyed on the uid alone, never the
// working directory or the environment, so it also spans checkouts,
// workspaces, and a dev build versus an installed one.
func daemonLockPaths() []string {
	var paths []string
	add := func(p string) {
		if p == "" {
			return
		}
		for _, existing := range paths {
			if existing == p {
				return
			}
		}
		paths = append(paths, p)
	}
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		add(filepath.Join(dir, "polyclav", "polyclav.lock"))
	}
	if dir, err := os.UserCacheDir(); err == nil && dir != "" {
		add(filepath.Join(dir, "polyclav", "polyclav.lock"))
	}
	// Fixed /tmp path, deliberately NOT os.TempDir(): TMPDIR differs between
	// a login shell and a service, and this is the one path guaranteed to be
	// the same everywhere, so it is the backstop that keeps every instance
	// colliding even when the two above resolve differently.
	add(filepath.Join("/tmp", "polyclav-"+strconv.Itoa(os.Getuid())+".lock"))
	return paths
}

// acquireDefaultDaemonLock takes the per-user singleton lock immediately:
// every candidate path that can be created is held, and the first one already
// held by another process is a fatal error (no wait — an overlap is a
// deployment mistake, not a handoff to wait out).
func acquireDefaultDaemonLock() (*daemonLock, error) {
	return acquireDaemonLocks(daemonLockPaths())
}

// acquireDaemonLocks takes as many of paths as it can. A path it cannot
// create (missing directory, permissions) is skipped; a path another process
// holds is fatal, releasing anything already taken.
func acquireDaemonLocks(paths []string) (*daemonLock, error) {
	lock := &daemonLock{}
	var lastErr error
	for _, path := range paths {
		file, err := holdLockFile(path, 0)
		if err == nil {
			lock.files = append(lock.files, file)
			continue
		}
		if errors.Is(err, errLockHeld) {
			_ = lock.Close()
			return nil, err
		}
		lastErr = err
	}
	if len(lock.files) == 0 {
		if lastErr == nil {
			lastErr = errors.New("no candidate lock paths")
		}
		return nil, fmt.Errorf("could not create a polyclav singleton lock: %w", lastErr)
	}
	return lock, nil
}

// acquireDaemonLock takes a single lock path, waiting up to wait for a holder
// to release it. The daemon path uses acquireDefaultDaemonLock (no wait);
// this exists for the tests and any caller that wants a handoff window.
func acquireDaemonLock(path string, wait time.Duration) (*daemonLock, error) {
	file, err := holdLockFile(path, wait)
	if err != nil {
		return nil, err
	}
	return &daemonLock{files: []*os.File{file}}, nil
}

// holdLockFile creates and flock's path, writing the owning PID. A held path
// returns errLockHeld (with the holder described when it can be); a path that
// cannot be created returns a plain error.
func holdLockFile(path string, wait time.Duration) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create singleton lock directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open singleton lock: %w", err)
	}

	deadline := time.Now().Add(wait)
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			if err := writeLockOwner(file); err != nil {
				_ = file.Close()
				return nil, err
			}
			return file, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = file.Close()
			return nil, fmt.Errorf("acquire singleton lock: %w", err)
		}
		if wait <= 0 || !time.Now().Before(deadline) {
			holder := describeLockHolder(path)
			_ = file.Close()
			if holder != "" {
				return nil, fmt.Errorf("%w — held by %s (lock: %s)", errLockHeld, holder, path)
			}
			return nil, fmt.Errorf("%w (lock: %s)", errLockHeld, path)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// writeLockOwner records the owning PID so a refused start can name the
// process holding the lock.
func writeLockOwner(file *os.File) error {
	if err := file.Truncate(0); err != nil {
		return fmt.Errorf("write singleton lock: %w", err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		return fmt.Errorf("write singleton lock: %w", err)
	}
	if _, err := fmt.Fprintf(file, "%d\n", os.Getpid()); err != nil {
		return fmt.Errorf("write singleton lock: %w", err)
	}
	return nil
}

// describeLockHolder reads the owning PID from a held lock file and, where
// the OS exposes it, that process's command line — so the error says which
// polyclav to stop.
func describeLockHolder(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return ""
	}
	if cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); err == nil {
		// argv is NUL-separated and may contain newlines; collapse to one line
		// and bound the length so the error stays a single readable line.
		cmd := strings.Join(strings.Fields(strings.ReplaceAll(string(cmdline), "\x00", " ")), " ")
		if len(cmd) > 120 {
			cmd = cmd[:117] + "..."
		}
		if cmd != "" {
			return fmt.Sprintf("pid %d (%s)", pid, cmd)
		}
	}
	return fmt.Sprintf("pid %d", pid)
}

func (l *daemonLock) Close() error {
	if l == nil {
		return nil
	}
	var firstErr error
	for _, file := range l.files {
		if runtime.GOOS != "windows" {
			if err := syscall.Flock(int(file.Fd()), syscall.LOCK_UN); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		if err := file.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	l.files = nil
	return firstErr
}
