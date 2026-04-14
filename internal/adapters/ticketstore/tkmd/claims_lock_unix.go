//go:build unix

package tkmd

import (
	"fmt"
	"os"
	"syscall"
)

// withClaimAcquisitionLock acquires an exclusive advisory lock on the lock file
// at path+".lock", calls fn under the lock, then releases the lock and removes
// the lock file. Uses syscall.Flock (POSIX) for Unix platforms.
func withClaimAcquisitionLock(path string, fn func() error) error {
	lockFile, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("open claim lock: %w", err)
	}
	defer func() {
		_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		_ = lockFile.Close()
		_ = os.Remove(path + ".lock")
	}()

	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock claim: %w", err)
	}
	return fn()
}
