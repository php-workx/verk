package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"verk/internal/adapters/ticketstore/tkmd"
	"verk/internal/state"
)

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

	saveTicket = func(_ string, _ tkmd.Ticket) error { return nil }
	saveJSONAtomic = func(_ string, _ any) error {
		return errors.New("disk full")
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
	if !errors.Is(err, errors.New("disk full")) {
		// errors.Is won't match a plain New; check wrapping via Unwrap.
		var wrapped interface{ Unwrap() error }
		if errors.As(err, &wrapped) {
			if wrapped.Unwrap() == nil || wrapped.Unwrap().Error() != "disk full" {
				t.Errorf("expected wrapped cause %q, got %v", "disk full", wrapped.Unwrap())
			}
		}
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
