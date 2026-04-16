package engine

import (
	"os"
	"path/filepath"
	"testing"
	"verk/internal/state"
)

func TestUpdateRunStatusFromTickets_ComputesPhaseFromActiveTickets(t *testing.T) {
	run := &state.RunArtifact{}

	// When all tickets are closed, the run is completed.
	updateRunStatusFromTickets(run, map[string]TicketRunSnapshot{
		"tk-1": {CurrentPhase: state.TicketPhaseClosed},
		"tk-2": {CurrentPhase: state.TicketPhaseClosed},
	})
	if run.Status != state.EpicRunStatusCompleted {
		t.Fatalf("expected completed, got %q", run.Status)
	}
	if run.CurrentPhase != state.TicketPhaseClosed {
		t.Fatalf("expected closed phase, got %q", run.CurrentPhase)
	}

	// When one ticket is in review and another in implement, the run phase
	// should be review (higher priority).
	updateRunStatusFromTickets(run, map[string]TicketRunSnapshot{
		"tk-1": {CurrentPhase: state.TicketPhaseImplement},
		"tk-2": {CurrentPhase: state.TicketPhaseReview},
	})
	if run.Status != state.EpicRunStatusRunning {
		t.Fatalf("expected running, got %q", run.Status)
	}
	if run.CurrentPhase != state.TicketPhaseReview {
		t.Fatalf("expected review phase (highest priority), got %q", run.CurrentPhase)
	}

	// Verify phase priority: closeout > repair > review > verify > implement.
	tests := []struct {
		name      string
		tickets   map[string]TicketRunSnapshot
		wantPhase state.TicketPhase
	}{
		{
			name: "implement alone",
			tickets: map[string]TicketRunSnapshot{
				"tk-1": {CurrentPhase: state.TicketPhaseImplement},
			},
			wantPhase: state.TicketPhaseImplement,
		},
		{
			name: "verify beats implement",
			tickets: map[string]TicketRunSnapshot{
				"tk-1": {CurrentPhase: state.TicketPhaseImplement},
				"tk-2": {CurrentPhase: state.TicketPhaseVerify},
			},
			wantPhase: state.TicketPhaseVerify,
		},
		{
			name: "review beats verify",
			tickets: map[string]TicketRunSnapshot{
				"tk-1": {CurrentPhase: state.TicketPhaseVerify},
				"tk-2": {CurrentPhase: state.TicketPhaseReview},
			},
			wantPhase: state.TicketPhaseReview,
		},
		{
			name: "repair beats review",
			tickets: map[string]TicketRunSnapshot{
				"tk-1": {CurrentPhase: state.TicketPhaseReview},
				"tk-2": {CurrentPhase: state.TicketPhaseRepair},
			},
			wantPhase: state.TicketPhaseRepair,
		},
		{
			name: "closeout beats repair",
			tickets: map[string]TicketRunSnapshot{
				"tk-1": {CurrentPhase: state.TicketPhaseRepair},
				"tk-2": {CurrentPhase: state.TicketPhaseCloseout},
			},
			wantPhase: state.TicketPhaseCloseout,
		},
		{
			name: "closed tickets do not affect active phase",
			tickets: map[string]TicketRunSnapshot{
				"tk-1": {CurrentPhase: state.TicketPhaseClosed},
				"tk-2": {CurrentPhase: state.TicketPhaseVerify},
			},
			wantPhase: state.TicketPhaseVerify,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run := &state.RunArtifact{}
			updateRunStatusFromTickets(run, tt.tickets)
			if run.CurrentPhase != tt.wantPhase {
				t.Errorf("expected phase %q, got %q", tt.wantPhase, run.CurrentPhase)
			}
		})
	}
}

func TestUpdateRunStatusFromTickets_BlockedTakesPrecedence(t *testing.T) {
	run := &state.RunArtifact{}

	// Blocked takes precedence over all other phases.
	updateRunStatusFromTickets(run, map[string]TicketRunSnapshot{
		"tk-1": {CurrentPhase: state.TicketPhaseCloseout},
		"tk-2": {CurrentPhase: state.TicketPhaseBlocked},
	})
	if run.Status != state.EpicRunStatusBlocked {
		t.Fatalf("expected blocked, got %q", run.Status)
	}
	if run.CurrentPhase != state.TicketPhaseBlocked {
		t.Fatalf("expected blocked phase, got %q", run.CurrentPhase)
	}
}

func TestUpdateRunStatusFromTickets_NilRunIsNoop(t *testing.T) {
	// Should not panic.
	updateRunStatusFromTickets(nil, map[string]TicketRunSnapshot{
		"tk-1": {CurrentPhase: state.TicketPhaseImplement},
	})
}

func TestUpdateRunStatusFromTickets_AllClosedIsCompleted(t *testing.T) {
	run := &state.RunArtifact{}
	updateRunStatusFromTickets(run, map[string]TicketRunSnapshot{
		"tk-1": {CurrentPhase: state.TicketPhaseClosed},
		"tk-2": {CurrentPhase: state.TicketPhaseClosed},
		"tk-3": {CurrentPhase: state.TicketPhaseClosed},
	})
	if run.Status != state.EpicRunStatusCompleted {
		t.Fatalf("expected completed, got %q", run.Status)
	}
	if run.CurrentPhase != state.TicketPhaseClosed {
		t.Fatalf("expected closed, got %q", run.CurrentPhase)
	}
}

// makeTicketsDir creates a temporary run directory with a tickets subdirectory
// and returns the repoRoot and runID so discoverRunTicketIDs can be called.
func makeTicketsDir(t *testing.T) (repoRoot, runID string) {
	t.Helper()
	repoRoot = t.TempDir()
	runID = "run-test"
	ticketsPath := filepath.Join(repoRoot, ".verk", "runs", runID, "tickets")
	if err := os.MkdirAll(ticketsPath, 0o755); err != nil {
		t.Fatalf("setup tickets dir: %v", err)
	}
	return repoRoot, runID
}

func TestDiscoverRunTicketIDs_WithPlanJSON(t *testing.T) {
	repoRoot, runID := makeTicketsDir(t)
	ticketsPath := filepath.Join(repoRoot, ".verk", "runs", runID, "tickets")

	// ticket-a has both plan.json and ticket-run.json (existing behaviour).
	ticketA := filepath.Join(ticketsPath, "ticket-a")
	if err := os.MkdirAll(ticketA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ticketA, "plan.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ticketA, "ticket-run.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	ids, err := discoverRunTicketIDs(repoRoot, runID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 1 || ids[0] != "ticket-a" {
		t.Fatalf("expected [ticket-a], got %v", ids)
	}
}

func TestDiscoverRunTicketIDs_NoPlanJSON(t *testing.T) {
	repoRoot, runID := makeTicketsDir(t)
	ticketsPath := filepath.Join(repoRoot, ".verk", "runs", runID, "tickets")

	// ticket-b has ticket-run.json but NO plan.json — previously missed.
	ticketB := filepath.Join(ticketsPath, "ticket-b")
	if err := os.MkdirAll(ticketB, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ticketB, "ticket-run.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	ids, err := discoverRunTicketIDs(repoRoot, runID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 1 || ids[0] != "ticket-b" {
		t.Fatalf("expected [ticket-b], got %v", ids)
	}
}

func TestDiscoverRunTicketIDs_EmptyDirectory(t *testing.T) {
	repoRoot, runID := makeTicketsDir(t)
	ticketsPath := filepath.Join(repoRoot, ".verk", "runs", runID, "tickets")

	// ticket-c is an empty directory.
	if err := os.MkdirAll(filepath.Join(ticketsPath, "ticket-c"), 0o755); err != nil {
		t.Fatal(err)
	}

	ids, err := discoverRunTicketIDs(repoRoot, runID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 1 || ids[0] != "ticket-c" {
		t.Fatalf("expected [ticket-c], got %v", ids)
	}
}

func TestDiscoverRunTicketIDs_RegularFilesExcluded(t *testing.T) {
	repoRoot, runID := makeTicketsDir(t)
	ticketsPath := filepath.Join(repoRoot, ".verk", "runs", runID, "tickets")

	// A regular file at the tickets level must not appear in results.
	if err := os.WriteFile(filepath.Join(ticketsPath, "not-a-ticket.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Also add a valid ticket directory so the result is not empty.
	if err := os.MkdirAll(filepath.Join(ticketsPath, "ticket-d"), 0o755); err != nil {
		t.Fatal(err)
	}

	ids, err := discoverRunTicketIDs(repoRoot, runID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 1 || ids[0] != "ticket-d" {
		t.Fatalf("expected [ticket-d], got %v", ids)
	}
}

func TestDiscoverRunTicketIDs_NoTicketsDirectory(t *testing.T) {
	repoRoot := t.TempDir()
	runID := "run-missing"
	// Do NOT create the tickets directory.

	_, err := discoverRunTicketIDs(repoRoot, runID)
	if err == nil {
		t.Fatal("expected error when tickets directory does not exist, got nil")
	}
}
