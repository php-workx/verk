package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"verk/internal/adapters/ticketstore/tkmd"
	"verk/internal/engine"
	"verk/internal/state"
)

// TestDoRunTicket_FinalSaveFailure injects a stubbed engine execution and a
// failing final SaveJSONAtomic call so doRunTicket returns the wrapped error and
// never prints the success status line.
func TestDoRunTicket_FinalSaveFailure(t *testing.T) {
	dir := t.TempDir()
	initCLITestRepo(t, dir)

	ticketsDir := filepath.Join(dir, ".tickets")
	if err := os.MkdirAll(ticketsDir, 0o755); err != nil {
		t.Fatalf("mkdir .tickets: %v", err)
	}
	ticket := tkmd.Ticket{
		ID:     "ver-final-save",
		Title:  "Final persistence failure test",
		Status: tkmd.StatusReady,
	}
	if err := tkmd.SaveTicket(filepath.Join(ticketsDir, "ver-final-save.md"), ticket); err != nil {
		t.Fatalf("save ticket: %v", err)
	}

	origRunTicket := runTicket
	origRunProgress := runProgress
	origSaveJSONAtomic := saveJSONAtomic
	origSaveTicket := saveTicket
	defer func() {
		runTicket = origRunTicket
		runProgress = origRunProgress
		saveJSONAtomic = origSaveJSONAtomic
		saveTicket = origSaveTicket
	}()

	runTicket = func(_ context.Context, _ engine.RunTicketRequest) (engine.RunTicketResult, error) {
		return engine.RunTicketResult{Snapshot: engine.TicketRunSnapshot{CurrentPhase: state.TicketPhaseClosed}}, nil
	}
	runProgress = func(_ string, ch <-chan engine.ProgressEvent, _ io.Writer, _ func()) error {
		for range ch {
		}
		return nil
	}
	saveTicket = func(_ string, _ tkmd.Ticket) error { return nil }

	errDiskFull := errors.New("disk full")
	callCount := 0
	saveJSONAtomic = func(_ string, _ any) error {
		callCount++
		if callCount == 2 { // Fail only the final persistence after the engine completes.
			return errDiskFull
		}
		return nil
	}

	t.Chdir(dir)

	var stdout, stderr bytes.Buffer
	runID, err := doRunTicket(&stdout, &stderr, "ver-final-save")
	if err == nil {
		t.Fatal("expected doRunTicket to return an error")
	}
	if !strings.Contains(err.Error(), "persist run state") {
		t.Fatalf("expected wrapped persist error, got %v", err)
	}
	if !errors.Is(err, errDiskFull) {
		t.Fatalf("expected underlying disk full error, got %v", err)
	}
	if runID == "" {
		t.Fatal("expected non-empty runID even on persistence failure")
	}
	if stdout.Len() > 0 {
		t.Fatalf("expected no success output on stdout, got %q", stdout.String())
	}
	if callCount != 2 {
		t.Fatalf("expected SaveJSONAtomic to be called twice, saw %d", callCount)
	}
}

// TestFinalizeRun_SaveJSONAtomicFailure verifies that when SaveJSONAtomic fails,
// finalizeRun returns a wrapped error ("persist run state: ...") and does NOT
// print the success status line. doRunTicket calls finalizeRun and propagates
// its error unchanged, so this test covers the doRunTicket acceptance criterion:
// "inject SaveJSONAtomic failure → doRunTicket returns wrapped error, no success
// message printed."
func TestFinalizeRun_SaveJSONAtomicFailure(t *testing.T) {
	// Arrange: SaveTicket succeeds, SaveJSONAtomic fails.
	originalSaveJSONAtomic := saveJSONAtomic
	originalSaveTicket := saveTicket
	defer func() {
		saveJSONAtomic = originalSaveJSONAtomic
		saveTicket = originalSaveTicket
	}()

	errDiskFull := errors.New("disk full")
	saveTicket = func(_ string, _ tkmd.Ticket) error { return nil }
	saveJSONAtomic = func(_ string, _ any) error {
		return errDiskFull
	}

	var stdout, stderr bytes.Buffer
	run := state.RunArtifact{}
	run.Status = state.EpicRunStatusCompleted
	run.CurrentPhase = state.TicketPhaseClosed

	// Act
	err := finalizeRun(&stdout, &stderr, "/tmp/ticket.md", "/tmp/run.json", tkmd.Ticket{}, run)

	// Assert: error is non-nil and wraps "persist run state"
	if err == nil {
		t.Fatal("expected error from SaveJSONAtomic failure, got nil")
	}
	if !strings.Contains(err.Error(), "persist run state") {
		t.Errorf("expected error to contain %q, got %q", "persist run state", err.Error())
	}
	if !errors.Is(err, errDiskFull) {
		t.Errorf("expected wrapped cause %q, got %v", "disk full", err)
	}

	// Assert: no success status line printed.
	if stdout.Len() > 0 {
		t.Errorf("expected no output on success writer, got %q", stdout.String())
	}
}

// TestFinalizeRun_SaveTicketFailure verifies that when SaveTicket fails,
// finalizeRun logs a warning to errw, returns a wrapped error ("save ticket: …"),
// and does NOT print the success status line.
func TestFinalizeRun_SaveTicketFailure(t *testing.T) {
	originalSaveJSONAtomic := saveJSONAtomic
	originalSaveTicket := saveTicket
	defer func() {
		saveJSONAtomic = originalSaveJSONAtomic
		saveTicket = originalSaveTicket
	}()

	saveTicket = func(_ string, _ tkmd.Ticket) error {
		return errors.New("permission denied")
	}
	saveJSONAtomic = func(_ string, _ any) error { return nil }

	var stdout, stderr bytes.Buffer
	run := state.RunArtifact{}
	run.Status = state.EpicRunStatusCompleted
	run.CurrentPhase = state.TicketPhaseClosed

	err := finalizeRun(&stdout, &stderr, "/tmp/ticket.md", "/tmp/run.json", tkmd.Ticket{}, run)

	if err == nil {
		t.Fatal("expected error from SaveTicket failure, got nil")
	}
	if !strings.Contains(err.Error(), "save ticket") {
		t.Errorf("expected error to contain %q, got %q", "save ticket", err.Error())
	}
	// Warning must be logged to errw.
	if !strings.Contains(stderr.String(), "warning") {
		t.Errorf("expected warning in stderr, got %q", stderr.String())
	}
	// No success line on stdout.
	if stdout.Len() > 0 {
		t.Errorf("expected no output on success writer, got %q", stdout.String())
	}
}

// TestFinalizeRun_BothSucceed verifies the happy path: both saves succeed and
// the status line is printed.
func TestFinalizeRun_BothSucceed(t *testing.T) {
	originalSaveJSONAtomic := saveJSONAtomic
	originalSaveTicket := saveTicket
	defer func() {
		saveJSONAtomic = originalSaveJSONAtomic
		saveTicket = originalSaveTicket
	}()

	saveTicket = func(_ string, _ tkmd.Ticket) error { return nil }
	saveJSONAtomic = func(_ string, _ any) error { return nil }

	var stdout, stderr bytes.Buffer
	run := state.RunArtifact{}
	run.Status = state.EpicRunStatusCompleted
	run.CurrentPhase = state.TicketPhaseClosed

	err := finalizeRun(&stdout, &stderr, "/tmp/ticket.md", "/tmp/run.json", tkmd.Ticket{}, run)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	// Status line must be printed.
	out := stdout.String()
	if !strings.Contains(out, "status=") {
		t.Errorf("expected status line in stdout, got %q", out)
	}
	if !strings.Contains(out, "phase=") {
		t.Errorf("expected phase in stdout, got %q", out)
	}
	// No noise on stderr.
	if stderr.Len() > 0 {
		t.Errorf("expected empty stderr, got %q", stderr.String())
	}
}
