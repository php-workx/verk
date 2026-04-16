//go:build windows

package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// AcquireRunLock attempts to acquire an exclusive, non-blocking lock for the
// given run. Returns an error immediately if the lock is already held by
// another process.
//
// Uses LockFileEx with LOCKFILE_EXCLUSIVE_LOCK|LOCKFILE_FAIL_IMMEDIATELY to
// provide the same semantics as the Unix flock implementation: non-blocking,
// exclusive, automatically released when the process exits.
func AcquireRunLock(repoRoot, runID string) (*RunLock, error) {
	dir := filepath.Join(repoRoot, ".verk", "runs", runID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create run dir: %w", err)
	}

	lockPath := filepath.Join(dir, "run.lock")
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open run lock: %w", err)
	}

	ol := new(windows.Overlapped) // offset 0 — lock the first byte
	err = windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, // reserved, must be zero
		1, // nNumberOfBytesToLockLow
		0, // nNumberOfBytesToLockHigh
		ol,
	)
	if err != nil {
		_ = file.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, fmt.Errorf("run %s is already being executed by another process", runID)
		}
		return nil, fmt.Errorf("acquire run lock for %s: %w", runID, err)
	}

	return &RunLock{file: file, path: lockPath}, nil
}

// IsRunLockHeld checks if the run lock is currently held by another process.
// Returns true if the lock is held, false if it's free or doesn't exist.
func IsRunLockHeld(repoRoot, runID string) bool {
	lockPath := filepath.Join(repoRoot, ".verk", "runs", runID, "run.lock")
	file, err := os.OpenFile(lockPath, os.O_RDWR, 0o644)
	if err != nil {
		return false // lock file doesn't exist = not held
	}
	defer func() { _ = file.Close() }()

	ol := new(windows.Overlapped) // offset 0 — probe the first byte
	err = windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0,
		ol,
	)
	if err != nil {
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return true // lock is held by another process
		}
		// Other errors — can't determine, assume not held
		return false
	}
	// Got the lock — release it immediately
	_ = windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, ol)
	return false
}

// Release releases the run lock.
func (l *RunLock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	ol := new(windows.Overlapped) // must match the range locked in AcquireRunLock
	_ = windows.UnlockFileEx(windows.Handle(l.file.Fd()), 0, 1, 0, ol)
	err := l.file.Close()
	l.file = nil
	return err
}
