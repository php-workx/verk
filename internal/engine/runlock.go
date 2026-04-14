package engine

import "os"

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
