package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"verk/internal/adapters/ticketstore/epos"
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
	writeTicketMarkdownFixture(t, repoRoot, epos.Ticket{
		ID:                 ticketID,
		Title:              "Blocked ticket",
		Status:             epos.StatusBlocked,
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

	ticket, err := epos.LoadTicket(ticketMarkdownPath(repoRoot, ticketID))
	if err != nil {
		t.Fatalf("load ticket markdown: %v", err)
	}
	if ticket.Status != epos.StatusReady {
		t.Fatalf("expected ticket markdown status ready, got %q", ticket.Status)
	}
}

func TestReopenTicket_RejectsUnsafeIdentifiersBeforeArtifactWrite(t *testing.T) {
	repoRoot := t.TempDir()
	tests := []struct {
		name     string
		runID    string
		ticketID string
		want     string
	}{
		{name: "run id", runID: "../escaped", ticketID: "ticket-safe", want: "invalid run id"},
		{name: "ticket id", runID: "run-safe", ticketID: "../escaped", want: "invalid ticket id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ReopenTicket(context.Background(), ReopenRequest{
				RepoRoot: repoRoot,
				RunID:    tt.runID,
				TicketID: tt.ticketID,
				ToPhase:  state.TicketPhaseImplement,
			})
			if err == nil {
				t.Fatal("expected unsafe identifier to be rejected")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected %q error, got: %v", tt.want, err)
			}
			if _, statErr := os.Stat(filepath.Join(repoRoot, ".verk", "escaped")); !os.IsNotExist(statErr) {
				t.Fatalf("unsafe identifier touched escaped artifact path: %v", statErr)
			}
		})
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
	writeTicketMarkdownFixture(t, repoRoot, epos.Ticket{
		ID:                 ticketID,
		Title:              "Closed ticket",
		Status:             epos.StatusClosed,
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

func TestReopenTicket_UpdatesTicketStoreAfterCommit(t *testing.T) {
	// Verifies G7: ticket markdown is mutated AFTER the atomic commit succeeds,
	// not before. When ReopenTicket succeeds, the ticket store should reflect
	// the "open" status.
	repoRoot := t.TempDir()
	runID := "run-reopen-g7"
	ticketID := "ticket-g7"

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
	writeTicketMarkdownFixture(t, repoRoot, epos.Ticket{
		ID:                 ticketID,
		Title:              "G7 ticket",
		Status:             epos.StatusBlocked,
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

	// After successful reopen, the ticket markdown should be updated to ready.
	ticket, err := epos.LoadTicket(ticketMarkdownPath(repoRoot, ticketID))
	if err != nil {
		t.Fatalf("load ticket markdown: %v", err)
	}
	if ticket.Status != epos.StatusReady {
		t.Fatalf("expected ticket status ready after reopen, got %q", ticket.Status)
	}
}

func TestReopenTicket_CommitFailureDoesNotMutateTicketStore(t *testing.T) {
	// G7 regression test: If WriteTransitionCommit fails, setTicketReady must
	// NOT be called. The ticket markdown should remain in its original state.
	repoRoot := t.TempDir()
	runID := "run-reopen-fail"
	ticketID := "ticket-fail"

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
	writeTicketMarkdownFixture(t, repoRoot, epos.Ticket{
		ID:                 ticketID,
		Title:              "Fail ticket",
		Status:             epos.StatusBlocked,
		OwnedPaths:         []string{"internal/app"},
		UnknownFrontmatter: map[string]any{"type": "task"},
	})

	// Record the original run.json content so we can corrupt it after reading.
	runPath := runJSONPath(repoRoot, runID)
	originalRunData, err := os.ReadFile(runPath)
	if err != nil {
		t.Fatalf("read original run.json: %v", err)
	}

	// Corrupt the run.json to make WriteTransitionCommit fail when it tries
	// to marshal + write the updated run artifact. We do this by writing
	// invalid JSON that can be loaded (LoadJSON tolerates extra data) but
	// whose re-serialization after update would still succeed. Instead, we
	// make the run artifact file read-only so the atomic write fails.
	// Remove write permissions from the run.json directory to force the write to fail.
	runDir := filepath.Dir(runPath)
	// Make the run.json file read-only to cause WriteTransitionCommit to fail.
	if err := os.Chmod(runPath, 0o444); err != nil {
		t.Fatalf("chmod run.json read-only: %v", err)
	}
	// Also make the directory read-only so SaveJSONAtomic can't create temp files.
	if err := os.Chmod(runDir, 0o555); err != nil {
		t.Fatalf("chmod run dir read-only: %v", err)
	}
	defer func() {
		_ = os.Chmod(runDir, 0o755)
		_ = os.Chmod(runPath, 0o644)
	}()

	err = ReopenTicket(context.Background(), ReopenRequest{
		RepoRoot: repoRoot,
		RunID:    runID,
		TicketID: ticketID,
		ToPhase:  state.TicketPhaseImplement,
	})
	if err == nil {
		t.Fatal("expected ReopenTicket to fail when WriteTransitionCommit cannot write")
	}

	// Restore permissions and verify the ticket markdown was NOT mutated.
	_ = os.Chmod(runDir, 0o755)
	_ = os.Chmod(runPath, 0o644)

	// Restore original run.json so we can read the ticket markdown path.
	_ = os.WriteFile(runPath, originalRunData, 0o644)

	// The critical check: ticket markdown should still be "blocked", NOT "open".
	ticket, err := epos.LoadTicket(ticketMarkdownPath(repoRoot, ticketID))
	if err != nil {
		t.Fatalf("load ticket markdown after failure: %v", err)
	}
	if ticket.Status != epos.StatusBlocked {
		t.Fatalf("G7 violation: ticket markdown was mutated to %q even though commit failed — should still be blocked", ticket.Status)
	}
}

func TestReopenTicket_MultiTicket_RunPhaseNotRolledBack(t *testing.T) {
	// Regression: when ticket-a is in review and ticket-b is reopened to
	// implement, the run phase must remain review (the most advanced active
	// phase), not regress to implement.
	repoRoot := t.TempDir()
	runID := "run-multi-reopen"
	ticketA := "ticket-a"
	ticketB := "ticket-b"

	writeOpRunFixture(t, repoRoot, runID, state.RunArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		Mode:         "epic",
		RootTicketID: "epic-1",
		Status:       state.EpicRunStatusBlocked,
		CurrentPhase: state.TicketPhaseBlocked,
		TicketIDs:    []string{ticketA, ticketB},
		WaveIDs:      []string{"wave-1"},
	})
	writeWaveFixture(t, repoRoot, runID, state.WaveArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		WaveID:       "wave-1",
		Ordinal:      1,
		Status:       state.WaveStatusFailed,
		TicketIDs:    []string{ticketB},
	})
	// ticket-a is already in review — it is the more advanced active phase.
	writeTicketRunFixture(t, repoRoot, runID, TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:     ticketA,
		CurrentPhase: state.TicketPhaseReview,
	})
	writePlanFixture(t, repoRoot, runID, state.PlanArtifact{
		ArtifactMeta:             state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:                 ticketA,
		EffectiveReviewThreshold: state.SeverityP2,
	})
	writeTicketMarkdownFixture(t, repoRoot, epos.Ticket{
		ID:                 ticketA,
		Title:              "Ticket A in review",
		Status:             epos.StatusOpen,
		OwnedPaths:         []string{"internal/app"},
		UnknownFrontmatter: map[string]any{"type": "task"},
	})
	// ticket-b is blocked and will be reopened to implement.
	writeTicketRunFixture(t, repoRoot, runID, TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:     ticketB,
		CurrentPhase: state.TicketPhaseBlocked,
		BlockReason:  "needs operator input",
	})
	writePlanFixture(t, repoRoot, runID, state.PlanArtifact{
		ArtifactMeta:             state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:                 ticketB,
		EffectiveReviewThreshold: state.SeverityP2,
	})
	writeTicketMarkdownFixture(t, repoRoot, epos.Ticket{
		ID:                 ticketB,
		Title:              "Ticket B blocked",
		Status:             epos.StatusBlocked,
		OwnedPaths:         []string{"internal/app"},
		UnknownFrontmatter: map[string]any{"type": "task"},
	})

	if err := ReopenTicket(context.Background(), ReopenRequest{
		RepoRoot: repoRoot,
		RunID:    runID,
		TicketID: ticketB,
		ToPhase:  state.TicketPhaseImplement,
	}); err != nil {
		t.Fatalf("ReopenTicket returned error: %v", err)
	}

	var run state.RunArtifact
	if err := state.LoadJSON(runJSONPath(repoRoot, runID), &run); err != nil {
		t.Fatalf("load run.json: %v", err)
	}
	if run.Status != state.EpicRunStatusRunning {
		t.Fatalf("expected run status running, got %q", run.Status)
	}
	// Review (priority 3) beats implement (priority 1): run must stay at review.
	if run.CurrentPhase != state.TicketPhaseReview {
		t.Fatalf("expected run phase review (ticket-a is still in review), got %q", run.CurrentPhase)
	}
}

func TestReopenTicket_SingleTicket_CurrentPhaseSet(t *testing.T) {
	// When a single ticket is reopened to implement, the run phase must be
	// implement (not left as whatever the previous direct assignment set it to).
	repoRoot := t.TempDir()
	runID := "run-single-phase"
	ticketID := "ticket-single"

	writeOpRunFixture(t, repoRoot, runID, state.RunArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		Mode:         "epic",
		RootTicketID: "epic-1",
		Status:       state.EpicRunStatusBlocked,
		CurrentPhase: state.TicketPhaseBlocked,
		TicketIDs:    []string{ticketID},
		WaveIDs:      []string{"wave-single"},
	})
	writeWaveFixture(t, repoRoot, runID, state.WaveArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		WaveID:       "wave-single",
		Ordinal:      1,
		Status:       state.WaveStatusFailed,
		TicketIDs:    []string{ticketID},
	})
	writeTicketRunFixture(t, repoRoot, runID, TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:     ticketID,
		CurrentPhase: state.TicketPhaseBlocked,
		BlockReason:  "operator input needed",
	})
	writePlanFixture(t, repoRoot, runID, state.PlanArtifact{
		ArtifactMeta:             state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:                 ticketID,
		EffectiveReviewThreshold: state.SeverityP2,
	})
	writeTicketMarkdownFixture(t, repoRoot, epos.Ticket{
		ID:                 ticketID,
		Title:              "Single ticket",
		Status:             epos.StatusBlocked,
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
		t.Fatalf("expected run status running, got %q", run.Status)
	}
	if run.CurrentPhase != state.TicketPhaseImplement {
		t.Fatalf("expected run phase implement, got %q", run.CurrentPhase)
	}
}

func TestReopenTicket_MultiTicket_BothReopened_MostAdvancedPhaseWins(t *testing.T) {
	// When two tickets in a run are both reopened (in separate calls) — one
	// to implement and the other to repair — the run phase should reflect
	// repair (higher priority) after the second reopen.
	repoRoot := t.TempDir()
	runID := "run-both-reopened"
	ticketA := "ticket-aa"
	ticketB := "ticket-bb"

	writeOpRunFixture(t, repoRoot, runID, state.RunArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		Mode:         "epic",
		RootTicketID: "epic-1",
		Status:       state.EpicRunStatusBlocked,
		CurrentPhase: state.TicketPhaseBlocked,
		TicketIDs:    []string{ticketA, ticketB},
		WaveIDs:      []string{"wave-both"},
	})
	writeWaveFixture(t, repoRoot, runID, state.WaveArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		WaveID:       "wave-both",
		Ordinal:      1,
		Status:       state.WaveStatusFailed,
		TicketIDs:    []string{ticketA, ticketB},
	})
	writeTicketRunFixture(t, repoRoot, runID, TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:     ticketA,
		CurrentPhase: state.TicketPhaseBlocked,
		BlockReason:  "blocked-a",
	})
	writePlanFixture(t, repoRoot, runID, state.PlanArtifact{
		ArtifactMeta:             state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:                 ticketA,
		EffectiveReviewThreshold: state.SeverityP2,
	})
	writeTicketMarkdownFixture(t, repoRoot, epos.Ticket{
		ID:                 ticketA,
		Title:              "Ticket AA",
		Status:             epos.StatusBlocked,
		OwnedPaths:         []string{"internal/app"},
		UnknownFrontmatter: map[string]any{"type": "task"},
	})
	writeTicketRunFixture(t, repoRoot, runID, TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:     ticketB,
		CurrentPhase: state.TicketPhaseBlocked,
		BlockReason:  "blocked-b",
	})
	writePlanFixture(t, repoRoot, runID, state.PlanArtifact{
		ArtifactMeta:             state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:                 ticketB,
		EffectiveReviewThreshold: state.SeverityP2,
	})
	writeTicketMarkdownFixture(t, repoRoot, epos.Ticket{
		ID:                 ticketB,
		Title:              "Ticket BB",
		Status:             epos.StatusBlocked,
		OwnedPaths:         []string{"internal/app"},
		UnknownFrontmatter: map[string]any{"type": "task"},
	})

	// Reopen ticket-a to implement.
	if err := ReopenTicket(context.Background(), ReopenRequest{
		RepoRoot: repoRoot,
		RunID:    runID,
		TicketID: ticketA,
		ToPhase:  state.TicketPhaseImplement,
	}); err != nil {
		t.Fatalf("first ReopenTicket returned error: %v", err)
	}

	// Before the second reopen, update ticket-b's markdown so it can be
	// loaded as blocked again (the first reopen only touched ticket-a).
	// Nothing to do: ticket-b is still blocked in its snapshot.

	// Reopen ticket-b to repair (higher priority than implement).
	if err := ReopenTicket(context.Background(), ReopenRequest{
		RepoRoot: repoRoot,
		RunID:    runID,
		TicketID: ticketB,
		ToPhase:  state.TicketPhaseRepair,
	}); err != nil {
		t.Fatalf("second ReopenTicket returned error: %v", err)
	}

	var run state.RunArtifact
	if err := state.LoadJSON(runJSONPath(repoRoot, runID), &run); err != nil {
		t.Fatalf("load run.json: %v", err)
	}
	if run.Status != state.EpicRunStatusRunning {
		t.Fatalf("expected run status running, got %q", run.Status)
	}
	// repair (priority 4) beats implement (priority 1).
	if run.CurrentPhase != state.TicketPhaseRepair {
		t.Fatalf("expected run phase repair (most advanced of implement+repair), got %q", run.CurrentPhase)
	}
}

// TestDefaultReopenTargetForSnapshot_FailedRetryable verifies that a snapshot
// with Outcome=failed_retryable is retryable and defaults to implement.
func TestDefaultReopenTargetForSnapshot_FailedRetryable(t *testing.T) {
	snap := TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1},
		TicketID:     "ticket-fr",
		CurrentPhase: state.TicketPhaseBlocked,
		Outcome:      state.TicketOutcomeFailedRetryable,
	}
	target, ok := DefaultReopenTargetForSnapshot(snap)
	if !ok {
		t.Fatal("expected failed_retryable to be automatically retryable")
	}
	if target != state.TicketPhaseImplement {
		t.Fatalf("expected implement retry target, got %q", target)
	}
}

// TestDefaultReopenTargetForSnapshot_FailedRetryableVerifyPhase verifies that
// failed_retryable always reopens to implement regardless of CurrentPhase.
func TestDefaultReopenTargetForSnapshot_FailedRetryableVerifyPhase(t *testing.T) {
	snap := TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1},
		TicketID:     "ticket-fr-verify",
		CurrentPhase: state.TicketPhaseVerify,
		Outcome:      state.TicketOutcomeFailedRetryable,
	}
	target, ok := DefaultReopenTargetForSnapshot(snap)
	if !ok {
		t.Fatal("expected failed_retryable in verify phase to be automatically retryable")
	}
	if target != state.TicketPhaseImplement {
		t.Fatalf("expected implement retry target for verify phase, got %q", target)
	}
}

// TestDefaultReopenTargetForSnapshot_NeedsDecision verifies that
// needs_decision outcome is not automatically retryable.
func TestDefaultReopenTargetForSnapshot_NeedsDecision(t *testing.T) {
	snap := TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1},
		TicketID:     "ticket-nd",
		CurrentPhase: state.TicketPhaseBlocked,
		Outcome:      state.TicketOutcomeNeedsDecision,
	}
	target, ok := DefaultReopenTargetForSnapshot(snap)
	if ok {
		t.Fatalf("expected needs_decision to NOT be automatically retryable, got target %q", target)
	}
	if target != "" {
		t.Fatalf("expected empty target for needs_decision, got %q", target)
	}
}

// TestDefaultReopenTargetForSnapshot_Blocked verifies that outcome=blocked is
// not automatically retryable.
func TestDefaultReopenTargetForSnapshot_Blocked(t *testing.T) {
	snap := TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1},
		TicketID:     "ticket-blk",
		CurrentPhase: state.TicketPhaseBlocked,
		Outcome:      state.TicketOutcomeBlocked,
	}
	target, ok := DefaultReopenTargetForSnapshot(snap)
	if ok {
		t.Fatalf("expected outcome=blocked to NOT be automatically retryable, got target %q", target)
	}
	if target != "" {
		t.Fatalf("expected empty target for outcome=blocked, got %q", target)
	}
}

// TestDefaultReopenTargetForSnapshot_EmptyOutcomeFallback verifies that when
// no outcome is set, the function falls back to DefaultReopenTargetForPhase.
// This is the backward-compatibility path for legacy snapshots.
func TestDefaultReopenTargetForSnapshot_EmptyOutcomeFallback(t *testing.T) {
	tests := []struct {
		name        string
		phase       state.TicketPhase
		wantTarget  state.TicketPhase
		wantRetryOK bool
	}{
		{
			name:        "blocked phase falls back to implement",
			phase:       state.TicketPhaseBlocked,
			wantTarget:  state.TicketPhaseImplement,
			wantRetryOK: true,
		},
		{
			name:        "closed phase falls back to repair",
			phase:       state.TicketPhaseClosed,
			wantTarget:  state.TicketPhaseRepair,
			wantRetryOK: true,
		},
		{
			name:        "implement phase is not retryable by default",
			phase:       state.TicketPhaseImplement,
			wantTarget:  "",
			wantRetryOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snap := TicketRunSnapshot{
				ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1},
				TicketID:     "ticket-legacy",
				CurrentPhase: tt.phase,
				Outcome:      "", // legacy: no outcome
			}
			target, ok := DefaultReopenTargetForSnapshot(snap)
			if ok != tt.wantRetryOK {
				t.Fatalf("retryable=%v, want %v", ok, tt.wantRetryOK)
			}
			if target != tt.wantTarget {
				t.Fatalf("target=%q, want %q", target, tt.wantTarget)
			}
		})
	}
}

func writeTicketMarkdownFixture(t *testing.T, repoRoot string, ticket epos.Ticket) {
	t.Helper()
	if err := epos.SaveTicket(filepath.Join(repoRoot, ".tickets", ticket.ID+".md"), ticket); err != nil {
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
