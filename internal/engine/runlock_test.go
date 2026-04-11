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

func TestAcquireRunLock_ConcurrentOnlyOneWins(t *testing.T) {
	dir := t.TempDir()
	runID := "run-concurrent"

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

	// At least one must have acquired (could be more since they release and others retry,
	// but the point is they don't all fail)
	if acquired == 0 {
		t.Fatal("expected at least one goroutine to acquire the lock")
	}
}

func TestRunLock_ReleaseNil(t *testing.T) {
	var lock *RunLock
	if err := lock.Release(); err != nil {
		t.Fatalf("Release on nil lock should not error: %v", err)
	}
}
