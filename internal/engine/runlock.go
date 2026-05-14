package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RunLock provides exclusive, advisory locking for a verk run.
// Only one process can hold the lock for a given run at a time.
// The OS automatically releases the lock if the process crashes.
//
// Platform implementations:
//   - Unix (Linux, macOS, etc.): runlock_unix.go — uses syscall.Flock
//   - Windows: runlock_windows.go — uses LockFileEx via golang.org/x/sys/windows
type RunLock struct {
	file *os.File
	path string
}

func ValidateArtifactIdentifier(id, label string) error {
	if id == "" {
		return fmt.Errorf("invalid %s: value is required", label)
	}
	if id == "." || id == ".." {
		return fmt.Errorf("invalid %s: contains path traversal", label)
	}
	if filepath.IsAbs(id) {
		return fmt.Errorf("invalid %s: contains absolute path", label)
	}
	if strings.Contains(id, "..") {
		return fmt.Errorf("invalid %s: contains path traversal", label)
	}
	if strings.ContainsAny(id, `/\`) {
		return fmt.Errorf("invalid %s: contains path separator", label)
	}
	if cleaned := filepath.Clean(id); cleaned != id {
		return fmt.Errorf("invalid %s: is not a clean identifier", label)
	}
	return nil
}

func runLockPath(repoRoot, runID string) (string, error) {
	if repoRoot == "" {
		return "", fmt.Errorf("repo root is required")
	}
	if err := ValidateArtifactIdentifier(runID, "run id"); err != nil {
		return "", err
	}
	return filepath.Join(repoRoot, ".verk", "runs", runID, "run.lock"), nil
}
