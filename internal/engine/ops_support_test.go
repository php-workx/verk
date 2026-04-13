package engine

import (
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
