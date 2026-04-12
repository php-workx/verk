package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// RunLock provides exclusive, advisory locking for a verk run.
// Only one process can hold the lock for a given run at a time.
// The OS automatically releases the lock if the process crashes.
type RunLock struct {
	file *os.File
	path string
}

// AcquireRunLock attempts to acquire an exclusive, non-blocking lock for the
// given run. Returns an error immediately if the lock is already held by
// another process.
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

	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
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
	defer file.Close()

	// Try non-blocking lock — if it succeeds, nobody holds it
	err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return true // lock is held by another process
		}
		// Other errors (EBADF, EINVAL, etc.) — can't determine, assume not held
		return false
	}
	// We got the lock — release it immediately
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	return false
}

// Release releases the run lock.
func (l *RunLock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	err := l.file.Close()
	l.file = nil
	return err
}
