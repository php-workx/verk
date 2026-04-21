package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"verk/internal/adapters/runtime"
	"verk/internal/adapters/ticketstore/tkmd"
	"verk/internal/policy"
	"verk/internal/state"

	runtimefake "verk/internal/adapters/runtime/fake"
)

func TestResumeRun_EmptyRepoRoot(t *testing.T) {
	_, err := ResumeRun(context.Background(), ResumeRequest{RunID: "x", RepoRoot: ""})
	if err == nil {
		t.Fatal("expected error for empty RepoRoot, got nil")
	}
	if err.Error() != "resume requires repo root" {
		t.Fatalf("unexpected error message: %q", err.Error())
	}
}

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

func TestResumeRun_ClosedPhase_NonClosable_BecomesBlocked(t *testing.T) {
	repoRoot := t.TempDir()
	runID := "run-closed-nonclose"
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
		Title:              "Non-closable ticket",
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
					ExitCode:   1,
					Passed:     false,
					StartedAt:  time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC),
					FinishedAt: time.Date(2026, 4, 2, 12, 0, 1, 0, time.UTC),
				},
			},
			Passed: false,
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
	if snapshot.CurrentPhase != state.TicketPhaseBlocked {
		t.Fatalf("expected Blocked phase, got %q", snapshot.CurrentPhase)
	}
	if snapshot.BlockReason == "" {
		t.Fatal("expected BlockReason to be set, got empty string")
	}
	if snapshot.Closeout == nil || snapshot.Closeout.Closable {
		t.Fatalf("expected non-closable closeout, got %#v", snapshot.Closeout)
	}
}

func TestResumeRun_ClosedPhase_Closable_StaysClosed(t *testing.T) {
	repoRoot := t.TempDir()
	runID := "run-closed-closable"
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
		Title:              "Closable ticket",
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
		BlockReason:  "stale-block-reason",
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
	if snapshot.CurrentPhase != state.TicketPhaseClosed {
		t.Fatalf("expected Closed phase, got %q", snapshot.CurrentPhase)
	}
	if snapshot.BlockReason != "" {
		t.Fatalf("expected BlockReason cleared, got %q", snapshot.BlockReason)
	}
	if snapshot.Closeout == nil || !snapshot.Closeout.Closable {
		t.Fatalf("expected closable closeout, got %#v", snapshot.Closeout)
	}
}

func TestResumeRun_CloseoutPhase_Closable_BecomesClosed(t *testing.T) {
	repoRoot := t.TempDir()
	runID := "run-closeout-closable"
	ticketID := "ticket-1"

	writeOpRunFixture(t, repoRoot, runID, state.RunArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		Mode:         "ticket",
		RootTicketID: ticketID,
		Status:       state.EpicRunStatusRunning,
		CurrentPhase: state.TicketPhaseCloseout,
		TicketIDs:    []string{ticketID},
	})
	writeTicketMarkdownFixture(t, repoRoot, tkmd.Ticket{
		ID:                 ticketID,
		Title:              "Closeout phase ticket",
		Status:             tkmd.StatusInProgress,
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
		CurrentPhase: state.TicketPhaseCloseout,
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
	if snapshot.CurrentPhase != state.TicketPhaseClosed {
		t.Fatalf("expected Closed phase, got %q", snapshot.CurrentPhase)
	}
	if snapshot.Closeout == nil || !snapshot.Closeout.Closable {
		t.Fatalf("expected closable closeout, got %#v", snapshot.Closeout)
	}
}

func TestResumeRun_ResumeFromVerifyPhase_UsesSavedVerificationCoverage(t *testing.T) {
	repoRoot := t.TempDir()
	runID := "run-verify-resume"
	ticketID := "ticket-1"

	writeOpRunFixture(t, repoRoot, runID, state.RunArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		Mode:         "ticket",
		RootTicketID: ticketID,
		Status:       state.EpicRunStatusRunning,
		CurrentPhase: state.TicketPhaseVerify,
		TicketIDs:    []string{ticketID},
	})
	writeTicketMarkdownFixture(t, repoRoot, tkmd.Ticket{
		ID:                 ticketID,
		Title:              "Verify phase ticket",
		Status:             tkmd.StatusInProgress,
		OwnedPaths:         []string{"internal/app"},
		AcceptanceCriteria: []string{"all checks pass"},
		ValidationCommands: []string{"echo resume verify"},
		UnknownFrontmatter: map[string]any{"type": "task"},
	})
	writePlanFixture(t, repoRoot, runID, state.PlanArtifact{
		ArtifactMeta:             state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:                 ticketID,
		ValidationCommands:       []string{"echo resume verify"},
		EffectiveReviewThreshold: state.SeverityP2,
	})
	coverage := state.ValidationCoverageArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		Scope:        state.ValidationScopeTicket,
		TicketID:     ticketID,
		DeclaredChecks: []state.ValidationCheck{{
			ID:      "declared-verify",
			Scope:   state.ValidationScopeTicket,
			Source:  state.ValidationCheckSourceDeclared,
			Command: "echo previous-fail",
			Reason:  "verification artifact was pending",
		}},
		ExecutedChecks: []state.ValidationCheckExecution{{
			CheckID:    "declared-verify",
			Result:     state.ValidationCheckResultFailed,
			StartedAt:  testRunTime(),
			FinishedAt: testRunTime().Add(time.Second),
		}},
		Closable:    false,
		BlockReason: "verification pending restart",
	}
	writeTicketRunFixture(t, repoRoot, runID, TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:     ticketID,
		CurrentPhase: state.TicketPhaseVerify,
		Verification: &state.VerificationArtifact{
			ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
			TicketID:     ticketID,
			Attempt:      1,
			Commands:     []string{"echo previous-fail"},
			Results: []state.VerificationResult{
				{
					Command:    "echo previous-fail",
					ExitCode:   1,
					Passed:     false,
					StartedAt:  testRunTime(),
					FinishedAt: testRunTime().Add(time.Second),
				},
			},
			Passed:             false,
			ValidationCoverage: &coverage,
		},
	})

	reviewArtifactPath := filepath.Join(repoRoot, "review-verify.json")
	if err := os.WriteFile(reviewArtifactPath, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write review artifact: %v", err)
	}
	reviewResult := runtime.ReviewResult{
		Status:             runtime.WorkerStatusDone,
		RetryClass:         runtime.RetryClassTerminal,
		LeaseID:            "lease-review-verify",
		StartedAt:          testRunTime(),
		FinishedAt:         testRunTime().Add(time.Second),
		ReviewStatus:       runtime.ReviewStatusPassed,
		Summary:            "clean",
		ResultArtifactPath: reviewArtifactPath,
	}
	adapter := &reflectingAdapter{
		reviewResults: []runtime.ReviewResult{reviewResult},
	}
	cfg := policy.DefaultConfig()
	cfg.Verification.QualityCommands = nil

	report, err := ResumeRun(context.Background(), ResumeRequest{
		RepoRoot: repoRoot,
		RunID:    runID,
		Adapter:  adapter,
		Config:   cfg,
	})
	if err != nil {
		t.Fatalf("ResumeRun returned error: %v", err)
	}

	if len(report.ResumedTickets) != 1 || report.ResumedTickets[0] != ticketID {
		t.Fatalf("expected resumed ticket %q, got %#v", ticketID, report.ResumedTickets)
	}
	if report.Status.CurrentPhase != state.TicketPhaseClosed {
		t.Fatalf("expected closed phase after rerun, got %q", report.Status.CurrentPhase)
	}

	var snapshot TicketRunSnapshot
	if err := state.LoadJSON(ticketSnapshotPath(repoRoot, runID, ticketID), &snapshot); err != nil {
		t.Fatalf("load ticket snapshot: %v", err)
	}
	if snapshot.CurrentPhase != state.TicketPhaseClosed {
		t.Fatalf("expected closed phase, got %q", snapshot.CurrentPhase)
	}
	if snapshot.Verification == nil || snapshot.Verification.Attempt <= 1 {
		t.Fatalf("expected verification to rerun during resume, got %#v", snapshot.Verification)
	}
	if snapshot.Verification.ValidationCoverage == nil {
		t.Fatalf("expected verification coverage after resumed verify run")
	}
	if len(snapshot.Verification.ValidationCoverage.DeclaredChecks) == 0 {
		t.Fatalf("expected declared checks in verification coverage")
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

// TestResumeRun_BlockedTicketIsReset verifies that a ticket in the blocked phase
// is reset to ready and included in re-execution when a blocked epic run is resumed.
// Regression test: isTerminalPhase used to treat blocked as terminal, causing the
// reset loop to skip blocked tickets and resume to exit immediately as blocked.
func TestResumeRun_BlockedTicketIsReset_EpicMode(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	runID := "run-blocked-reset"

	epic := epicTicket("epic-reset")
	mustSaveTicket(t, repoRoot, epic)

	// blocked child: should be reset and re-run
	blocked := epicChildTicket("ticket-blocked", epic.ID, tkmd.StatusBlocked, nil, []string{"internal/app"})
	mustSaveTicket(t, repoRoot, blocked)

	// closed child: should not be reset or re-run
	closed := epicChildTicket("ticket-closed", epic.ID, tkmd.StatusClosed, nil, []string{"docs"})
	mustSaveTicket(t, repoRoot, closed)

	writeOpRunFixture(t, repoRoot, runID, state.RunArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		Mode:         "epic",
		RootTicketID: epic.ID,
		Status:       state.EpicRunStatusBlocked,
		CurrentPhase: state.TicketPhaseBlocked,
		BaseCommit:   baseCommit,
		TicketIDs:    []string{blocked.ID, closed.ID},
		ResumeCursor: map[string]any{},
	})
	writeTicketRunFixture(t, repoRoot, runID, TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:     blocked.ID,
		CurrentPhase: state.TicketPhaseBlocked,
		BlockReason:  "non_convergent_verification: failed after 3 attempt(s)",
	})
	writeTicketRunFixture(t, repoRoot, runID, TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:     closed.ID,
		CurrentPhase: state.TicketPhaseClosed,
		Closeout:     &state.CloseoutArtifact{Closable: true},
	})
	writePlanFixture(t, repoRoot, runID, state.PlanArtifact{
		ArtifactMeta:             state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:                 blocked.ID,
		EffectiveReviewThreshold: state.SeverityP2,
	})
	writePlanFixture(t, repoRoot, runID, state.PlanArtifact{
		ArtifactMeta:             state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:                 closed.ID,
		EffectiveReviewThreshold: state.SeverityP2,
	})

	// Adapter returns a blocked result so we can verify re-execution happened
	// without needing a successful full ticket run.
	artifactPath := filepath.Join(repoRoot, "worker-result.json")
	if err := os.WriteFile(artifactPath, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	adapter := runtimefake.New([]runtime.WorkerResult{
		validWorkerResult("lease-run-blocked-reset-ticket-blocked", artifactPath),
	}, nil)

	cfg := policy.DefaultConfig()
	cfg.Verification.QualityCommands = nil // skip wave verification

	report, err := ResumeRun(context.Background(), ResumeRequest{
		RepoRoot: repoRoot,
		RunID:    runID,
		Adapter:  adapter,
		Config:   cfg,
	})
	if err != nil {
		t.Fatalf("ResumeRun returned error: %v", err)
	}

	// The blocked ticket must appear in ResumedTickets — it was reset and re-executed.
	found := false
	for _, tid := range report.ResumedTickets {
		if tid == blocked.ID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected %q in ResumedTickets, got %v", blocked.ID, report.ResumedTickets)
	}

	// The closed ticket must NOT appear in ResumedTickets.
	for _, tid := range report.ResumedTickets {
		if tid == closed.ID {
			t.Errorf("closed ticket %q should not appear in ResumedTickets", closed.ID)
		}
	}

	// Verify the adapter was actually called for the blocked ticket.
	reqs := adapter.WorkerRequests()
	if len(reqs) == 0 {
		t.Error("expected adapter to be called for the re-executed blocked ticket, got 0 calls")
	}
}
