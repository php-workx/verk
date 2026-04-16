//go:build windows

package tkmd

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// withClaimAcquisitionLock acquires an exclusive advisory lock on the lock file
// at path+".lock", calls fn under the lock, then releases the lock and removes
// the lock file. Uses LockFileEx via golang.org/x/sys/windows for Windows.
func withClaimAcquisitionLock(path string, fn func() error) error {
	lockFile, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("open claim lock: %w", err)
	}
	ol := new(windows.Overlapped) // offset 0 — lock the first byte
	defer func() {
		_ = windows.UnlockFileEx(windows.Handle(lockFile.Fd()), 0, 1, 0, ol)
		_ = lockFile.Close()
		_ = os.Remove(path + ".lock")
	}()

	if err := windows.LockFileEx(
		windows.Handle(lockFile.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK, // blocking (no LOCKFILE_FAIL_IMMEDIATELY)
		0, 1, 0,
		ol,
	); err != nil {
		return fmt.Errorf("lock claim: %w", err)
	}
	return fn()
}
