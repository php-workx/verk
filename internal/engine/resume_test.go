package engine

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"verk/internal/adapters/ticketstore/tkmd"
	"verk/internal/state"
)

func TestResumeRun_BlocksOnClaimDivergence(t *testing.T) {
	repoRoot := t.TempDir()
	runID := "run-divergence"
	ticketID := "ticket-1"

	writeOpRunFixture(t, repoRoot, runID, state.RunArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		Mode:         "epic",
		RootTicketID: "epic-1",
		Status:       state.EpicRunStatusRunning,
		CurrentPhase: state.TicketPhaseImplement,
		TicketIDs:    []string{ticketID},
	})
	writeTicketRunFixture(t, repoRoot, runID, TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:     ticketID,
		CurrentPhase: state.TicketPhaseImplement,
		Implementation: &state.ImplementationArtifact{
			ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
			TicketID:     ticketID,
			LeaseID:      "lease-live",
		},
	})
	writePlanFixture(t, repoRoot, runID, state.PlanArtifact{
		ArtifactMeta:             state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:                 ticketID,
		EffectiveReviewThreshold: state.SeverityP2,
	})
	writeTicketMarkdownFixture(t, repoRoot, tkmd.Ticket{
		ID:                 ticketID,
		Title:              "Diverged ticket",
		Status:             tkmd.StatusInProgress,
		OwnedPaths:         []string{"internal/app"},
		UnknownFrontmatter: map[string]any{"type": "task"},
	})

	live := state.ClaimArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:     ticketID,
		OwnerRunID:   runID,
		LeaseID:      "lease-live",
		LeasedAt:     time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC),
		ExpiresAt:    time.Date(2026, 4, 2, 12, 30, 0, 0, time.UTC),
		State:        "active",
	}
	durable := live
	durable.LeaseID = "lease-durable"
	if err := state.SaveJSONAtomic(liveClaimPath(repoRoot, ticketID), live); err != nil {
		t.Fatalf("save live claim: %v", err)
	}
	if err := state.SaveJSONAtomic(durableClaimPath(repoRoot, runID, ticketID), durable); err != nil {
		t.Fatalf("save durable claim: %v", err)
	}

	report, err := ResumeRun(context.Background(), ResumeRequest{RepoRoot: repoRoot, RunID: runID})
	if err != nil {
		t.Fatalf("ResumeRun returned error: %v", err)
	}
	if report.Run.Status != state.EpicRunStatusBlocked {
		t.Fatalf("expected blocked run, got %q", report.Run.Status)
	}
	if !report.Status.ClaimDivergence {
		t.Fatal("expected claim divergence to be reported")
	}

	var run state.RunArtifact
	if err := state.LoadJSON(runJSONPath(repoRoot, runID), &run); err != nil {
		t.Fatalf("load run.json: %v", err)
	}
	if run.Status != state.EpicRunStatusBlocked {
		t.Fatalf("expected persisted blocked run, got %q", run.Status)
	}
	if len(run.AuditEvents) == 0 || !strings.Contains(run.AuditEvents[len(run.AuditEvents)-1].Type, "resume_claim_divergence") {
		t.Fatalf("expected divergence audit event, got %#v", run.AuditEvents)
	}
}

func TestResumeRun_RepairsCommittedTransitionAfterCrash(t *testing.T) {
	repoRoot := t.TempDir()
	runID := "run-closeout-repair"
	ticketID := "ticket-1"

	writeOpRunFixture(t, repoRoot, runID, state.RunArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		Mode:         "ticket",
		RootTicketID: ticketID,
		Status:       state.EpicRunStatusCompleted,
		CurrentPhase: state.TicketPhaseClosed,
		TicketIDs:    []string{ticketID},
	})
	writeTicketMarkdownFixture(t, repoRoot, tkmd.Ticket{
		ID:                 ticketID,
		Title:              "Recovered ticket",
		Status:             tkmd.StatusClosed,
		OwnedPaths:         []string{"internal/app"},
		AcceptanceCriteria: []string{"all checks pass"},
		ValidationCommands: []string{"go test ./..."},
		UnknownFrontmatter: map[string]any{"type": "task"},
	})
	writePlanFixture(t, repoRoot, runID, state.PlanArtifact{
		ArtifactMeta:             state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:                 ticketID,
		AcceptanceCriteria:       []string{"all checks pass"},
		ValidationCommands:       []string{"go test ./..."},
		EffectiveReviewThreshold: state.SeverityP2,
	})
	writeTicketRunFixture(t, repoRoot, runID, TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:     ticketID,
		CurrentPhase: state.TicketPhaseClosed,
		Verification: &state.VerificationArtifact{
			ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
			TicketID:     ticketID,
			Attempt:      1,
			Commands:     []string{"go test ./..."},
			Results: []state.VerificationResult{
				{
					Command:    "go test ./...",
					ExitCode:   0,
					Passed:     true,
					StartedAt:  time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC),
					FinishedAt: time.Date(2026, 4, 2, 12, 0, 1, 0, time.UTC),
				},
			},
			Passed: true,
		},
		Review: &state.ReviewFindingsArtifact{
			ArtifactMeta:             state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
			TicketID:                 ticketID,
			Attempt:                  1,
			ReviewerRuntime:          "codex",
			Summary:                  "clean",
			EffectiveReviewThreshold: state.SeverityP2,
			Passed:                   true,
		},
	})

	report, err := ResumeRun(context.Background(), ResumeRequest{RepoRoot: repoRoot, RunID: runID})
	if err != nil {
		t.Fatalf("ResumeRun returned error: %v", err)
	}
	if len(report.RecoveredTickets) != 1 || report.RecoveredTickets[0] != ticketID {
		t.Fatalf("expected ticket recovery, got %#v", report.RecoveredTickets)
	}

	var snapshot TicketRunSnapshot
	if err := state.LoadJSON(ticketSnapshotPath(repoRoot, runID, ticketID), &snapshot); err != nil {
		t.Fatalf("load ticket snapshot: %v", err)
	}
	if snapshot.Closeout == nil || !snapshot.Closeout.Closable {
		t.Fatalf("expected repaired closeout in snapshot, got %#v", snapshot.Closeout)
	}
	if _, err := os.Stat(closeoutArtifactPath(repoRoot, runID, ticketID)); err != nil {
		t.Fatalf("expected closeout artifact to be rebuilt: %v", err)
	}
}

func TestReloadTicketSnapshots_UpdatesStalePhases(t *testing.T) {
	repoRoot := t.TempDir()
	runID := "run-reload"

	// Set up an epic run with two tickets
	writeOpRunFixture(t, repoRoot, runID, state.RunArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		Mode:         "epic",
		RootTicketID: "epic-1",
		Status:       state.EpicRunStatusBlocked,
		CurrentPhase: state.TicketPhaseBlocked,
		TicketIDs:    []string{"ticket-1", "ticket-2"},
	})

	// Write initial "blocked" and "implement" phases
	writeTicketRunFixture(t, repoRoot, runID, TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:     "ticket-1",
		CurrentPhase: state.TicketPhaseBlocked,
		BlockReason:  "failed verification",
	})
	writeTicketRunFixture(t, repoRoot, runID, TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:     "ticket-2",
		CurrentPhase: state.TicketPhaseImplement,
	})

	// Load artifacts — snapshots will have stale phases
	artifacts, err := loadRunArtifacts(repoRoot, runID)
	if err != nil {
		t.Fatalf("loadRunArtifacts: %v", err)
	}
	if artifacts.Tickets["ticket-1"].CurrentPhase != state.TicketPhaseBlocked {
		t.Fatalf("expected stale blocked phase for ticket-1, got %q", artifacts.Tickets["ticket-1"].CurrentPhase)
	}

	// Simulate: RunTicket wrote updated snapshots to disk (both closed)
	writeTicketRunFixture(t, repoRoot, runID, TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:     "ticket-1",
		CurrentPhase: state.TicketPhaseClosed,
	})
	writeTicketRunFixture(t, repoRoot, runID, TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:     "ticket-2",
		CurrentPhase: state.TicketPhaseClosed,
	})

	// Before reload, in-memory data still has stale phases
	if artifacts.Tickets["ticket-1"].CurrentPhase != state.TicketPhaseBlocked {
		t.Fatalf("expected stale blocked phase before reload, got %q", artifacts.Tickets["ticket-1"].CurrentPhase)
	}

	// Reload snapshots from disk
	reloadTicketSnapshots(repoRoot, runID, artifacts.Tickets)

	// After reload, phases should match on-disk state
	if artifacts.Tickets["ticket-1"].CurrentPhase != state.TicketPhaseClosed {
		t.Fatalf("expected closed phase for ticket-1 after reload, got %q", artifacts.Tickets["ticket-1"].CurrentPhase)
	}
	if artifacts.Tickets["ticket-2"].CurrentPhase != state.TicketPhaseClosed {
		t.Fatalf("expected closed phase for ticket-2 after reload, got %q", artifacts.Tickets["ticket-2"].CurrentPhase)
	}

	// Now updateRunStatusFromTickets should see all tickets closed
	run := artifacts.Run
	updateRunStatusFromTickets(&run, artifacts.Tickets)
	if run.Status != state.EpicRunStatusCompleted {
		t.Fatalf("expected completed run after reload, got %q", run.Status)
	}
}
