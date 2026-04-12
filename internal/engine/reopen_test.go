package engine

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"verk/internal/adapters/ticketstore/tkmd"
	"verk/internal/state"
)

func TestReopenTicket_BlockedToImplement(t *testing.T) {
	repoRoot := t.TempDir()
	runID := "run-reopen-blocked"
	ticketID := "ticket-blocked"

	writeOpRunFixture(t, repoRoot, runID, state.RunArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		Mode:         "epic",
		RootTicketID: "epic-1",
		Status:       state.EpicRunStatusBlocked,
		CurrentPhase: state.TicketPhaseBlocked,
		TicketIDs:    []string{ticketID},
		WaveIDs:      []string{"wave-1"},
	})
	writeWaveFixture(t, repoRoot, runID, state.WaveArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		WaveID:       "wave-1",
		Ordinal:      1,
		Status:       state.WaveStatusFailed,
		TicketIDs:    []string{ticketID},
	})
	writeTicketRunFixture(t, repoRoot, runID, TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:     ticketID,
		CurrentPhase: state.TicketPhaseBlocked,
		BlockReason:  "needs operator input",
	})
	writePlanFixture(t, repoRoot, runID, state.PlanArtifact{
		ArtifactMeta:             state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:                 ticketID,
		EffectiveReviewThreshold: state.SeverityP2,
	})
	writeTicketMarkdownFixture(t, repoRoot, tkmd.Ticket{
		ID:                 ticketID,
		Title:              "Blocked ticket",
		Status:             tkmd.StatusBlocked,
		OwnedPaths:         []string{"internal/app"},
		UnknownFrontmatter: map[string]any{"type": "task"},
	})

	if err := ReopenTicket(context.Background(), ReopenRequest{
		RepoRoot: repoRoot,
		RunID:    runID,
		TicketID: ticketID,
		ToPhase:  state.TicketPhaseImplement,
	}); err != nil {
		t.Fatalf("ReopenTicket returned error: %v", err)
	}

	var run state.RunArtifact
	if err := state.LoadJSON(runJSONPath(repoRoot, runID), &run); err != nil {
		t.Fatalf("load run.json: %v", err)
	}
	if run.Status != state.EpicRunStatusRunning {
		t.Fatalf("expected run to return to running, got %q", run.Status)
	}
	if len(run.AuditEvents) == 0 || run.AuditEvents[len(run.AuditEvents)-1].Type != "ticket_reopened" {
		t.Fatalf("expected reopen audit event, got %#v", run.AuditEvents)
	}

	var snapshot TicketRunSnapshot
	if err := state.LoadJSON(ticketSnapshotPath(repoRoot, runID, ticketID), &snapshot); err != nil {
		t.Fatalf("load ticket snapshot: %v", err)
	}
	if snapshot.CurrentPhase != state.TicketPhaseImplement {
		t.Fatalf("expected implement phase, got %q", snapshot.CurrentPhase)
	}
	if snapshot.BlockReason != "" {
		t.Fatalf("expected block reason to be cleared, got %q", snapshot.BlockReason)
	}

	ticket, err := tkmd.LoadTicket(ticketMarkdownPath(repoRoot, ticketID))
	if err != nil {
		t.Fatalf("load ticket markdown: %v", err)
	}
	if ticket.Status != tkmd.StatusOpen {
		t.Fatalf("expected ticket markdown status open, got %q", ticket.Status)
	}
}

func TestReopenTicket_ClosedRepairMarksWaveFailedReopened(t *testing.T) {
	repoRoot := t.TempDir()
	runID := "run-reopen-closed"
	ticketID := "ticket-closed"

	writeOpRunFixture(t, repoRoot, runID, state.RunArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		Mode:         "epic",
		RootTicketID: "epic-1",
		Status:       state.EpicRunStatusCompleted,
		CurrentPhase: state.TicketPhaseClosed,
		TicketIDs:    []string{ticketID},
		WaveIDs:      []string{"wave-1"},
	})
	writeWaveFixture(t, repoRoot, runID, state.WaveArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		WaveID:       "wave-1",
		Ordinal:      1,
		Status:       state.WaveStatusAccepted,
		TicketIDs:    []string{ticketID},
	})
	writeTicketRunFixture(t, repoRoot, runID, TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:     ticketID,
		CurrentPhase: state.TicketPhaseClosed,
		Closeout: &state.CloseoutArtifact{
			ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
			TicketID:     ticketID,
			Closable:     true,
		},
	})
	writePlanFixture(t, repoRoot, runID, state.PlanArtifact{
		ArtifactMeta:             state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:                 ticketID,
		EffectiveReviewThreshold: state.SeverityP2,
	})
	writeTicketMarkdownFixture(t, repoRoot, tkmd.Ticket{
		ID:                 ticketID,
		Title:              "Closed ticket",
		Status:             tkmd.StatusClosed,
		OwnedPaths:         []string{"internal/app"},
		UnknownFrontmatter: map[string]any{"type": "task"},
	})

	if err := ReopenTicket(context.Background(), ReopenRequest{
		RepoRoot: repoRoot,
		RunID:    runID,
		TicketID: ticketID,
		ToPhase:  state.TicketPhaseRepair,
	}); err != nil {
		t.Fatalf("ReopenTicket returned error: %v", err)
	}

	var wave state.WaveArtifact
	if err := state.LoadJSON(waveArtifactPath(repoRoot, runID, "wave-1"), &wave); err != nil {
		t.Fatalf("load wave artifact: %v", err)
	}
	if wave.Status != state.WaveStatusFailedReopened {
		t.Fatalf("expected reopened wave to be failed_reopened, got %q", wave.Status)
	}

	var snapshot TicketRunSnapshot
	if err := state.LoadJSON(ticketSnapshotPath(repoRoot, runID, ticketID), &snapshot); err != nil {
		t.Fatalf("load ticket snapshot: %v", err)
	}
	if snapshot.CurrentPhase != state.TicketPhaseRepair {
		t.Fatalf("expected repair phase, got %q", snapshot.CurrentPhase)
	}
}

func writeTicketMarkdownFixture(t *testing.T, repoRoot string, ticket tkmd.Ticket) {
	t.Helper()
	if err := tkmd.SaveTicket(filepath.Join(repoRoot, ".tickets", ticket.ID+".md"), ticket); err != nil {
		t.Fatalf("SaveTicket(%s): %v", ticket.ID, err)
	}
}

func writeOpRunFixture(t *testing.T, repoRoot, runID string, run state.RunArtifact) {
	t.Helper()
	run.CreatedAt = time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC)
	run.UpdatedAt = run.CreatedAt
	if err := state.SaveJSONAtomic(runJSONPath(repoRoot, runID), run); err != nil {
		t.Fatalf("save run fixture: %v", err)
	}
}

func writeTicketRunFixture(t *testing.T, repoRoot, runID string, snapshot TicketRunSnapshot) {
	t.Helper()
	snapshot.CreatedAt = time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC)
	snapshot.UpdatedAt = snapshot.CreatedAt
	if err := state.SaveJSONAtomic(ticketSnapshotPath(repoRoot, runID, snapshot.TicketID), snapshot); err != nil {
		t.Fatalf("save ticket fixture: %v", err)
	}
}

func writePlanFixture(t *testing.T, repoRoot, runID string, plan state.PlanArtifact) {
	t.Helper()
	if err := state.SaveJSONAtomic(planArtifactPath(repoRoot, runID, plan.TicketID), plan); err != nil {
		t.Fatalf("save plan fixture: %v", err)
	}
}

func writeWaveFixture(t *testing.T, repoRoot, runID string, wave state.WaveArtifact) {
	t.Helper()
	if err := state.SaveJSONAtomic(waveArtifactPath(repoRoot, runID, wave.WaveID), wave); err != nil {
		t.Fatalf("save wave fixture: %v", err)
	}
}
