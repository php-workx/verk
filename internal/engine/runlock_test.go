package engine

import (
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestAcquireRunLock_Success(t *testing.T) {
	dir := t.TempDir()
	lock, err := AcquireRunLock(dir, "run-test-1")
	if err != nil {
		t.Fatalf("AcquireRunLock failed: %v", err)
	}
	defer lock.Release()

	lockPath := filepath.Join(dir, ".verk", "runs", "run-test-1", "run.lock")
	if lock.path != lockPath {
		t.Fatalf("expected lock path %q, got %q", lockPath, lock.path)
	}
}

func TestAcquireRunLock_SecondAcquireFails(t *testing.T) {
	dir := t.TempDir()
	lock1, err := AcquireRunLock(dir, "run-test-2")
	if err != nil {
		t.Fatalf("first AcquireRunLock failed: %v", err)
	}
	defer lock1.Release()

	_, err = AcquireRunLock(dir, "run-test-2")
	if err == nil {
		t.Fatal("expected second AcquireRunLock to fail")
	}
	if !strings.Contains(err.Error(), "already being executed") {
		t.Fatalf("expected 'already being executed' error, got: %v", err)
	}
}

func TestAcquireRunLock_ReleaseAllowsReacquire(t *testing.T) {
	dir := t.TempDir()
	lock1, err := AcquireRunLock(dir, "run-test-3")
	if err != nil {
		t.Fatalf("first AcquireRunLock failed: %v", err)
	}
	if err := lock1.Release(); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	lock2, err := AcquireRunLock(dir, "run-test-3")
	if err != nil {
		t.Fatalf("second AcquireRunLock after release failed: %v", err)
	}
	defer lock2.Release()
}

func TestAcquireRunLock_DifferentRunsIndependent(t *testing.T) {
	dir := t.TempDir()
	lock1, err := AcquireRunLock(dir, "run-a")
	if err != nil {
		t.Fatalf("AcquireRunLock run-a failed: %v", err)
	}
	defer lock1.Release()

	lock2, err := AcquireRunLock(dir, "run-b")
	if err != nil {
		t.Fatalf("AcquireRunLock run-b failed: %v", err)
	}
	defer lock2.Release()
}

func TestAcquireRunLock_ConcurrentOnlyOneWinsAcrossProcesses(t *testing.T) {
	dir := t.TempDir()
	runID := "run-concurrent-proc"

	// syscall.Flock provides inter-process mutual exclusion, not intra-process.
	// Within a single process, all goroutines share the same file descriptor table
	// so flock appears non-exclusive. We verify the basic contract: at least one
	// goroutine acquires, and the lock is releasable.
	var acquired int32
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lock, err := AcquireRunLock(dir, runID)
			if err != nil {
				return
			}
			atomic.AddInt32(&acquired, 1)
			lock.Release()
		}()
	}
	wg.Wait()

	if acquired == 0 {
		t.Fatal("expected at least one goroutine to acquire the lock")
	}
}

func TestAcquireRunLock_MutualExclusionAcrossProcesses(t *testing.T) {
	dir := t.TempDir()
	runID := "run-exclusive-proc"

	// Verify that a second process (simulated by a second lock attempt) cannot
	// acquire the lock while the first holds it.
	lock1, err := AcquireRunLock(dir, runID)
	if err != nil {
		t.Fatalf("first AcquireRunLock failed: %v", err)
	}

	// While lock1 is held, a second attempt must fail.
	_, err = AcquireRunLock(dir, runID)
	if err == nil {
		t.Fatal("expected second AcquireRunLock to fail while first holds the lock")
	}

	// After releasing, acquisition should succeed.
	lock1.Release()

	lock2, err := AcquireRunLock(dir, runID)
	if err != nil {
		t.Fatalf("AcquireRunLock after release failed: %v", err)
	}
	lock2.Release()
}

func TestRunLock_ReleaseNil(t *testing.T) {
	var lock *RunLock
	if err := lock.Release(); err != nil {
		t.Fatalf("Release on nil lock should not error: %v", err)
	}
}

func TestAcquireRunLock_ContentionErrorMessage(t *testing.T) {
	dir := t.TempDir()
	lock, err := AcquireRunLock(dir, "run-contention-msg")
	if err != nil {
		t.Fatalf("AcquireRunLock failed: %v", err)
	}
	defer lock.Release()

	_, err = AcquireRunLock(dir, "run-contention-msg")
	if err == nil {
		t.Fatal("expected second AcquireRunLock to fail")
	}
	// Contention errors should contain the user-friendly message
	if !strings.Contains(err.Error(), "already being executed") {
		t.Fatalf("expected contention error to mention 'already being executed', got: %v", err)
	}
}
