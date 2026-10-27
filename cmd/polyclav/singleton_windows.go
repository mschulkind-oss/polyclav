//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// errLockHeld marks the "another process owns this lock" case, kept distinct
// from "this lock path is unusable" so acquireDefaultDaemonLock can tell the
// two apart — the same distinction singleton_unix.go draws with flock's
// EWOULDBLOCK versus a permissions error.
var errLockHeld = errors.New("another polyclav is already running")

// errorSharingViolation is Win32 ERROR_SHARING_VIOLATION (32), which Go's
// syscall package does not export. It is what a second CreateFile gets when the
// first holder opened with dwShareMode 0.
const errorSharingViolation syscall.Errno = 32

// daemonLock is the Windows counterpart of the flock-based type in
// singleton_unix.go. It holds an exclusive, non-shared handle on one file:
// Windows refuses a second CreateFile with dwShareMode 0, and the kernel drops
// the handle when the process exits, so a crashed daemon does not leave a
// stale lock behind the way a plain PID file would.
type daemonLock struct {
	file *os.File
	path string
}

// daemonLockPaths returns the single lock path this platform uses. Unlike the
// unix version there is no XDG_RUNTIME_DIR-vs-home split to contend with: every
// Windows process of this user sees the same %LocalAppData%.
func daemonLockPaths() []string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return nil
	}
	return []string{filepath.Join(dir, "polyclav", "daemon.lock")}
}

// acquireDefaultDaemonLock takes the per-user singleton lock immediately.
func acquireDefaultDaemonLock() (*daemonLock, error) {
	paths := daemonLockPaths()
	if len(paths) == 0 {
		// No resolvable cache dir. Running without the guard beats refusing to
		// start, and matches the unix version's "skip an unusable path" rule.
		return &daemonLock{}, nil
	}
	return acquireDaemonLock(paths[0])
}

// acquireDaemonLock opens path with no sharing permitted. A second process gets
// ERROR_SHARING_VIOLATION, which is reported as errLockHeld so the caller can
// say "another polyclav is already running" rather than surfacing a raw
// Win32 error.
func acquireDaemonLock(path string) (*daemonLock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create lock dir: %w", err)
	}

	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("lock path %q: %w", path, err)
	}

	handle, err := syscall.CreateFile(
		name,
		syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		0, // dwShareMode 0: exclusive — this IS the lock.
		nil,
		syscall.OPEN_ALWAYS,
		syscall.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	// Judge by the HANDLE, not by err. With OPEN_ALWAYS on an existing file,
	// CreateFile returns a perfectly good handle while setting last error to
	// ERROR_ALREADY_EXISTS, and Go's wrapper surfaces that as a non-nil error.
	// Checking err first would make every run after the first look like a
	// failure.
	if handle == syscall.InvalidHandle {
		if errors.Is(err, errorSharingViolation) ||
			errors.Is(err, syscall.ERROR_ACCESS_DENIED) {
			return nil, errLockHeld
		}
		return nil, fmt.Errorf("open lock %s: %w", path, err)
	}

	return &daemonLock{
		file: os.NewFile(uintptr(handle), path),
		path: path,
	}, nil
}

// Close releases the lock. Safe on a nil/zero lock, and safe to call twice,
// because main defers it on every return path.
func (l *daemonLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	f := l.file
	l.file = nil
	return f.Close()
}
