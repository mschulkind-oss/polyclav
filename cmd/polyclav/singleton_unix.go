//go:build unix

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"
)

const daemonLockWait = 5 * time.Second

type daemonLock struct {
	file *os.File
	path string
}

func acquireDefaultDaemonLock(wait time.Duration) (*daemonLock, error) {
	path, err := daemonLockPath()
	if err != nil {
		return nil, err
	}
	return acquireDaemonLock(path, wait)
}

func daemonLockPath() (string, error) {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "polyclav", "polyclav.lock"), nil
	}
	if dir, err := os.UserCacheDir(); err == nil && dir != "" {
		return filepath.Join(dir, "polyclav", "polyclav.lock"), nil
	}
	return filepath.Join(os.TempDir(), "polyclav-"+strconv.Itoa(os.Getuid())+".lock"), nil
}

func acquireDaemonLock(path string, wait time.Duration) (*daemonLock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create singleton lock directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open singleton lock: %w", err)
	}
	lock := &daemonLock{file: file, path: path}

	deadline := time.Now().Add(wait)
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			if err := file.Truncate(0); err != nil {
				_ = lock.Close()
				return nil, fmt.Errorf("write singleton lock: %w", err)
			}
			if _, err := file.Seek(0, 0); err != nil {
				_ = lock.Close()
				return nil, fmt.Errorf("write singleton lock: %w", err)
			}
			if _, err := fmt.Fprintf(file, "%d\n", os.Getpid()); err != nil {
				_ = lock.Close()
				return nil, fmt.Errorf("write singleton lock: %w", err)
			}
			return lock, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = lock.Close()
			return nil, fmt.Errorf("acquire singleton lock: %w", err)
		}
		if wait <= 0 || !time.Now().Before(deadline) {
			_ = lock.Close()
			return nil, fmt.Errorf("another polyclav is already running (lock: %s)", path)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func (l *daemonLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	file := l.file
	l.file = nil
	var err error
	if runtime.GOOS != "windows" {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	}
	if cerr := file.Close(); err == nil {
		err = cerr
	}
	return err
}
