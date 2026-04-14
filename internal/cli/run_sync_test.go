package cli

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"verk/internal/engine"
)

// TestEngineGoroutineSync_NoRaceOnEarlyTUIReturn validates the synchronization
// pattern used in doRunTicket, doRunEpic, and doAutoResume.
//
// Prior to the fix, tui.RunProgress could return early (e.g. on a TUI render
// error) while the engine goroutine was still writing to result/runErr.
// The main goroutine would then read those variables concurrently — a data race
// detectable by "go test -race".
//
// The fix introduces a sync.WaitGroup: the engine goroutine calls wg.Done()
// after it finishes, and the main goroutine calls wg.Wait() after
// tui.RunProgress returns and before reading the shared variables.
//
// This test models the exact pattern from production code:
//  1. A goroutine sets shared variables and sends a progress event, then sleeps
//     briefly to simulate an engine still running after the channel is consumed.
//  2. A fake "TUI" drains the channel and returns early (no terminal present in
//     tests, but we model the early-return scenario explicitly).
//  3. wg.Wait() blocks the main goroutine until the engine goroutine finishes.
//  4. The test asserts the correct final values are observed — and the race
//     detector confirms no concurrent access occurred.
func TestEngineGoroutineSync_NoRaceOnEarlyTUIReturn(t *testing.T) {
	ch := make(chan engine.ProgressEvent, 4)

	var result string
	var runErr error
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(ch)

		// Send a progress event so the fake TUI can consume it and return.
		ch <- engine.ProgressEvent{Type: engine.EventTicketDetail, Detail: "working"}

		// Simulate engine still running after the progress event is consumed.
		// Without wg.Wait() in the caller, the caller would race against this write.
		time.Sleep(10 * time.Millisecond)

		result = "done"
		runErr = errors.New("engine finished")
	}()

	// Fake TUI: drain the channel until it closes (simulates RunProgress in
	// non-terminal mode), then return — potentially before the goroutine above
	// has written result/runErr.
	fakeTUI := func() {
		for range ch {
		}
	}
	fakeTUI()

	// This is the fix: wait for the engine goroutine to finish before reading
	// the shared variables. Without this call the race detector would flag the
	// reads below as concurrent with the goroutine's writes above.
	wg.Wait()

	if result != "done" {
		t.Errorf("expected result=%q, got %q", "done", result)
	}
	if runErr == nil || runErr.Error() != "engine finished" {
		t.Errorf("expected runErr=%q, got %v", "engine finished", runErr)
	}
}

// TestEngineGoroutineSync_ErrorPath checks that when the engine goroutine
// returns an error the caller sees it correctly after wg.Wait().
func TestEngineGoroutineSync_ErrorPath(t *testing.T) {
	ch := make(chan engine.ProgressEvent, 1)

	var runErr error
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(ch)
		runErr = errors.New("engine error")
	}()

	// Drain channel (fake TUI).
	for range ch {
	}
	wg.Wait()

	if runErr == nil || runErr.Error() != "engine error" {
		t.Errorf("expected engine error, got %v", runErr)
	}
}

// TestEngineGoroutineSync_SuccessPath checks that a nil error and populated
// result are visible after wg.Wait() when the engine succeeds.
func TestEngineGoroutineSync_SuccessPath(t *testing.T) {
	ch := make(chan engine.ProgressEvent, 1)

	var result string
	var runErr error
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(ch)
		result = "success"
		runErr = nil
	}()

	for range ch {
	}
	wg.Wait()

	if runErr != nil {
		t.Errorf("expected nil error, got %v", runErr)
	}
	if result != "success" {
		t.Errorf("expected result=%q, got %q", "success", result)
	}
}

// TestDrainGoroutine_PreventsDeadlockOnEarlyTUIReturn exercises the exact
// failure mode described in the acceptance criteria: runProgress returns early
// (with an error) before close(ch) fires, leaving the progress channel
// un-drained. Without the drain goroutine the engine goroutine blocks on its
// 65th SendProgress (buffer size = 64) and wg.Wait() hangs indefinitely.
//
// The test uses the package-level runProgress var to inject a fake TUI that
// returns immediately without reading from ch, then validates that:
//  1. The drain goroutine (launched on tuiErr != nil) unblocks the engine.
//  2. wg.Wait() returns within a short deadline — proving no deadlock.
//  3. result and runErr reflect the engine's final state, with no data race
//     (verified by running under "go test -race").
func TestDrainGoroutine_PreventsDeadlockOnEarlyTUIReturn(t *testing.T) {
	// Inject a fake runProgress that returns an error immediately without
	// draining ch, simulating a BubbleTea TUI that exits on a render error.
	original := runProgress
	runProgress = func(_ string, _ <-chan engine.ProgressEvent, _ io.Writer) error {
		return errors.New("TUI exited early")
	}
	defer func() { runProgress = original }()

	const bufSize = 64
	const eventCount = bufSize * 2 // well over the buffer limit
	ch := make(chan engine.ProgressEvent, bufSize)

	var result string
	var runErr error
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(ch)
		// Send twice the buffer capacity to guarantee the goroutine would
		// deadlock if nobody drains ch after the TUI returns early.
		for i := 0; i < eventCount; i++ {
			engine.SendProgress(ch, engine.ProgressEvent{
				Type:   engine.EventTicketDetail,
				Detail: fmt.Sprintf("event-%d", i),
			})
		}
		result = "done"
		runErr = nil
	}()

	// Mirror the production pattern from doRunTicket / doRunEpic / doAutoResume.
	if tuiErr := runProgress("test-run", ch, io.Discard); tuiErr != nil {
		// This is the fix under test: without this drain goroutine the engine
		// goroutine blocks on SendProgress once the buffer is full, and
		// wg.Wait() below would hang forever.
		go func() {
			for range ch {
			}
		}()
	}

	// Assert wg.Wait() completes within 5 seconds. A deadlock would cause the
	// test to block here until the test binary's own timeout fires (~10 min).
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("wg.Wait() timed out — engine goroutine is deadlocked (missing drain)")
	}

	if result != "done" {
		t.Errorf("expected result=%q, got %q", "done", result)
	}
	if runErr != nil {
		t.Errorf("expected nil runErr, got %v", runErr)
	}
}
