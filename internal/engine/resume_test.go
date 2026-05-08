package engine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"verk/internal/adapters/runtime"
	"verk/internal/adapters/ticketstore/epos"
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

func TestResumeRun_DoesNotOverwriteExistingWaveArtifactAfterCursorCrash(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	runID := "run-resume-wave-cursor-lag"
	if _, err := ensureIntegrationBaseRef(repoRoot, runID, baseCommit); err != nil {
		t.Fatalf("seed integration base ref: %v", err)
	}

	root := epicTicket("epic-resume-wave-cursor-lag")
	first := epicChildTicket("ticket-prior-wave", root.ID, epos.StatusClosed, nil, []string{"prior.txt"})
	second := epicChildTicket("ticket-after-crash", root.ID, epos.StatusReady, nil, []string{"after.txt"})
	mustSaveTicket(t, repoRoot, root)
	mustSaveTicket(t, repoRoot, first)
	mustSaveTicket(t, repoRoot, second)

	writeOpRunFixture(t, repoRoot, runID, state.RunArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		Mode:         "epic",
		RootTicketID: root.ID,
		Status:       state.EpicRunStatusRunning,
		CurrentPhase: state.TicketPhaseImplement,
		BaseCommit:   baseCommit,
		TicketIDs:    []string{first.ID, second.ID},
		ResumeCursor: map[string]any{
			"wave_ordinal": 0,
		},
	})
	writeTicketRunFixture(t, repoRoot, runID, TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:     first.ID,
		CurrentPhase: state.TicketPhaseClosed,
		Closeout: &state.CloseoutArtifact{
			ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
			TicketID:     first.ID,
			Closable:     true,
		},
	})
	writeTicketRunFixture(t, repoRoot, runID, TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:     second.ID,
		CurrentPhase: state.TicketPhaseImplement,
	})
	writeWaveFixture(t, repoRoot, runID, state.WaveArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		WaveID:       "wave-1",
		Ordinal:      1,
		Status:       state.WaveStatusRunning,
		TicketIDs:    []string{first.ID},
		Acceptance: map[string]any{
			"sentinel": "do-not-overwrite",
		},
	})

	start := epicTestStart()
	adapter := functionAdapter{
		runWorker: func(ctx context.Context, req runtime.WorkerRequest) (runtime.WorkerResult, error) {
			if req.TicketID != second.ID {
				return runtime.WorkerResult{}, fmt.Errorf("unexpected resumed ticket %q", req.TicketID)
			}
			return runtime.WorkerResult{
				Status:             runtime.WorkerStatusBlocked,
				RetryClass:         runtime.RetryClassRetryable,
				BlockReason:        "stop after scheduling resumed wave",
				LeaseID:            req.LeaseID,
				StartedAt:          start,
				FinishedAt:         start.Add(time.Second),
				ResultArtifactPath: filepath.Join(repoRoot, "blocked-worker.json"),
			}, nil
		},
		runReviewer: func(ctx context.Context, req runtime.ReviewRequest) (runtime.ReviewResult, error) {
			return runtime.ReviewResult{}, fmt.Errorf("reviewer should not run for blocked worker")
		},
	}

	report, err := ResumeRun(context.Background(), ResumeRequest{
		RepoRoot: repoRoot,
		RunID:    runID,
		Adapter:  adapter,
		Config:   policy.DefaultConfig(),
	})
	if err != nil {
		t.Fatalf("ResumeRun returned error: %v", err)
	}
	if report.Run.Status != state.EpicRunStatusBlocked {
		t.Fatalf("expected blocked run after resumed worker blocks, got %q", report.Run.Status)
	}

	var preserved state.WaveArtifact
	if err := state.LoadJSON(waveArtifactPath(repoRoot, runID, "wave-1"), &preserved); err != nil {
		t.Fatalf("load original wave artifact: %v", err)
	}
	if got := preserved.Acceptance["sentinel"]; got != "do-not-overwrite" {
		t.Fatalf("resume overwrote existing wave-1 artifact, acceptance=%#v", preserved.Acceptance)
	}

	var resumedWave state.WaveArtifact
	if err := state.LoadJSON(waveArtifactPath(repoRoot, runID, "wave-2"), &resumedWave); err != nil {
		t.Fatalf("expected resume to write next wave artifact instead of reusing wave-1: %v", err)
	}
	if resumedWave.Ordinal != 2 || resumedWave.WaveID != "wave-2" {
		t.Fatalf("unexpected resumed wave identity: id=%q ordinal=%d", resumedWave.WaveID, resumedWave.Ordinal)
	}
	if len(resumedWave.TicketIDs) != 1 || resumedWave.TicketIDs[0] != second.ID {
		t.Fatalf("expected resumed wave to contain %q, got %#v", second.ID, resumedWave.TicketIDs)
	}
}

func TestResumeRun_CompletesPendingWaveIntegrationTransaction(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	runID := "run-pending-wave-transaction"

	root := epicTicket("epic-pending-wave-transaction")
	child := epicChildTicket("ticket-pending-wave-transaction", root.ID, epos.StatusClosed, nil, []string{"integrated.txt"})
	mustSaveTicket(t, repoRoot, root)
	mustSaveTicket(t, repoRoot, child)

	integration, err := prepareWaveIntegration(context.Background(), repoRoot, runID, "", baseCommit)
	if err != nil {
		t.Fatalf("prepare integration worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(integration.WorktreePath(), "integrated.txt"), []byte("integrated\n"), 0o644); err != nil {
		t.Fatalf("write pending integrated file: %v", err)
	}

	cursor := map[string]any{
		"wave_ordinal":          1,
		"last_wave_base_commit": baseCommit,
	}
	setPendingWaveIntegration(cursor, pendingWaveIntegrationTransaction{
		WaveID:       "wave-1",
		BaseCommit:   baseCommit,
		ChangedFiles: []string{"integrated.txt"},
		WorktreePath: integration.WorktreePath(),
	})
	writeOpRunFixture(t, repoRoot, runID, state.RunArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		Mode:         "epic",
		RootTicketID: root.ID,
		Status:       state.EpicRunStatusRunning,
		CurrentPhase: state.TicketPhaseImplement,
		BaseCommit:   baseCommit,
		TicketIDs:    []string{child.ID},
		WaveIDs:      []string{"wave-1"},
		ResumeCursor: cursor,
	})
	writeTicketRunFixture(t, repoRoot, runID, TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:     child.ID,
		CurrentPhase: state.TicketPhaseClosed,
		Closeout:     &state.CloseoutArtifact{Closable: true},
	})
	writeWaveFixture(t, repoRoot, runID, state.WaveArtifact{
		ArtifactMeta:   state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		WaveID:         "wave-1",
		Ordinal:        1,
		Status:         state.WaveStatusAccepted,
		TicketIDs:      []string{child.ID},
		ActualScope:    []string{"integrated.txt"},
		WaveBaseCommit: baseCommit,
		Acceptance: map[string]any{
			"wave_verification_passed": true,
		},
	})

	report, err := ResumeRun(context.Background(), ResumeRequest{
		RepoRoot: repoRoot,
		RunID:    runID,
		Adapter:  newReflectingAdapter(0),
		Config:   policy.DefaultConfig(),
	})
	if err != nil {
		t.Fatalf("ResumeRun returned error: %v", err)
	}
	if report.Run.Status != state.EpicRunStatusCompleted {
		t.Fatalf("expected completed run, got %q", report.Run.Status)
	}
	if got, err := os.ReadFile(filepath.Join(repoRoot, "integrated.txt")); err != nil || string(got) != "integrated\n" {
		t.Fatalf("expected pending integration applied to main, got %q err=%v", string(got), err)
	}
	if _, ok := pendingWaveVerificationID(report.Run.ResumeCursor); ok {
		t.Fatalf("expected pending marker cleared, cursor=%v", report.Run.ResumeCursor)
	}
	if _, ok := report.Run.ResumeCursor["pending_wave_integration"]; ok {
		t.Fatalf("expected pending transaction cleared, cursor=%v", report.Run.ResumeCursor)
	}
	newBaseHead, err := gitRevParse(repoRoot, integrationBaseRef(runID))
	if err != nil {
		t.Fatalf("resolve hidden base: %v", err)
	}
	if newBaseHead == baseCommit {
		t.Fatalf("expected hidden base to advance after completing pending transaction")
	}
	if got := report.Run.ResumeCursor["last_wave_base_commit"]; got != newBaseHead {
		t.Fatalf("expected last_wave_base_commit %s, got %#v", newBaseHead, got)
	}
}

func TestResumeRun_PendingVerificationPassedDoesNotClearBeforeCommitAndMainApply(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	runID := "run-pending-wave-missing-transaction"

	root := epicTicket("epic-pending-wave-missing-transaction")
	child := epicChildTicket("ticket-pending-wave-missing-transaction", root.ID, epos.StatusClosed, nil, []string{"integrated.txt"})
	mustSaveTicket(t, repoRoot, root)
	mustSaveTicket(t, repoRoot, child)

	writeOpRunFixture(t, repoRoot, runID, state.RunArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		Mode:         "epic",
		RootTicketID: root.ID,
		Status:       state.EpicRunStatusRunning,
		CurrentPhase: state.TicketPhaseImplement,
		BaseCommit:   baseCommit,
		TicketIDs:    []string{child.ID},
		WaveIDs:      []string{"wave-1"},
		ResumeCursor: map[string]any{
			"wave_ordinal":              1,
			"last_wave_base_commit":     baseCommit,
			"pending_wave_verification": "wave-1",
		},
	})
	writeTicketRunFixture(t, repoRoot, runID, TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:     child.ID,
		CurrentPhase: state.TicketPhaseClosed,
		Closeout:     &state.CloseoutArtifact{Closable: true},
	})
	writeWaveFixture(t, repoRoot, runID, state.WaveArtifact{
		ArtifactMeta:   state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		WaveID:         "wave-1",
		Ordinal:        1,
		Status:         state.WaveStatusAccepted,
		TicketIDs:      []string{child.ID},
		ActualScope:    []string{"integrated.txt"},
		WaveBaseCommit: baseCommit,
		Acceptance: map[string]any{
			"wave_verification_passed": true,
		},
	})

	_, err := ResumeRun(context.Background(), ResumeRequest{
		RepoRoot: repoRoot,
		RunID:    runID,
		Adapter:  newReflectingAdapter(0),
		Config:   policy.DefaultConfig(),
	})
	if err == nil {
		t.Fatal("expected ResumeRun to reject passed pending wave without transaction")
	}
	if !strings.Contains(err.Error(), "no durable integration transaction") {
		t.Fatalf("expected durable transaction error, got %v", err)
	}

	var run state.RunArtifact
	if err := state.LoadJSON(runJSONPath(repoRoot, runID), &run); err != nil {
		t.Fatalf("load run artifact: %v", err)
	}
	if pending, ok := pendingWaveVerificationID(run.ResumeCursor); !ok || pending != "wave-1" {
		t.Fatalf("expected pending marker preserved, cursor=%v", run.ResumeCursor)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "integrated.txt")); !os.IsNotExist(err) {
		t.Fatalf("main tree should not be mutated without transaction, stat err=%v", err)
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
	writeTicketMarkdownFixture(t, repoRoot, epos.Ticket{
		ID:                 ticketID,
		Title:              "Diverged ticket",
		Status:             epos.StatusInProgress,
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
	writeTicketMarkdownFixture(t, repoRoot, epos.Ticket{
		ID:                 ticketID,
		Title:              "Recovered ticket",
		Status:             epos.StatusClosed,
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
	writeTicketMarkdownFixture(t, repoRoot, epos.Ticket{
		ID:                 ticketID,
		Title:              "Non-closable ticket",
		Status:             epos.StatusClosed,
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
	if snapshot.Closeout == nil {
		t.Fatalf("expected closeout to be repaired, got %#v", snapshot.Closeout)
	}
	if snapshot.Closeout.FailedGate == "" {
		t.Fatalf("expected non-empty FailedGate, got %#v", snapshot.Closeout)
	}
	if snapshot.BlockReason != snapshot.Closeout.FailedGate {
		t.Fatalf("expected BlockReason %q, got %q", snapshot.Closeout.FailedGate, snapshot.BlockReason)
	}
	if snapshot.Closeout.Closable {
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
	writeTicketMarkdownFixture(t, repoRoot, epos.Ticket{
		ID:                 ticketID,
		Title:              "Closable ticket",
		Status:             epos.StatusClosed,
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
	if snapshot.Closeout == nil {
		t.Fatalf("expected repaired closeout, got %#v", snapshot.Closeout)
	}
	if !snapshot.Closeout.Closable {
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
	writeTicketMarkdownFixture(t, repoRoot, epos.Ticket{
		ID:                 ticketID,
		Title:              "Closeout phase ticket",
		Status:             epos.StatusInProgress,
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
	if snapshot.BlockReason != "" {
		t.Fatalf("expected BlockReason cleared, got %q", snapshot.BlockReason)
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
	writeTicketMarkdownFixture(t, repoRoot, epos.Ticket{
		ID:                 ticketID,
		Title:              "Verify phase ticket",
		Status:             epos.StatusInProgress,
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
	worktreeRoot, err := ResolveWorktreeRoot(repoRoot)
	if err != nil {
		t.Fatalf("resolve worktree root: %v", err)
	}

	epic := epicTicket("epic-reset")
	mustSaveTicket(t, repoRoot, epic)

	// blocked child: should be reset and re-run
	blocked := epicChildTicket("ticket-blocked", epic.ID, epos.StatusBlocked, nil, []string{"internal/app"})
	mustSaveTicket(t, repoRoot, blocked)

	// closed child: should not be reset or re-run
	closed := epicChildTicket("ticket-closed", epic.ID, epos.StatusClosed, nil, []string{"docs"})
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
	artifactPath := filepath.Join(t.TempDir(), "worker-result.json")
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
	if got := reqs[0].WorktreePath; got == "" {
		t.Fatal("expected resumed epic ticket to use an isolated worktree path, got empty path")
	} else {
		expectedPrefix := filepath.Join(worktreeRoot, runID) + string(filepath.Separator)
		if !strings.HasPrefix(got, expectedPrefix) {
			t.Fatalf("expected resumed worktree path under %q, got %q", expectedPrefix, got)
		}
		if got == repoRoot {
			t.Fatalf("expected resumed epic ticket to avoid repo root %q, got %q", repoRoot, got)
		}
	}
}

func TestResumeRun_IntegratesClosedSiblingWhenWaveHasBlockedTicket(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	runID := "run-resume-partial-wave"
	cfg := policy.DefaultConfig()
	cfg.Scheduler.MaxConcurrency = 2
	cfg.Verification.QualityCommands = nil
	cfg.Verification.WaveCommands = nil

	epic := epicTicket("epic-resume-partial")
	closed := epicChildTicket("ticket-resume-closed", epic.ID, epos.StatusBlocked, nil, []string{"closed.txt"})
	blocked := epicChildTicket("ticket-resume-blocked", epic.ID, epos.StatusBlocked, nil, []string{"blocked.txt"})
	mustSaveTicket(t, repoRoot, epic)
	mustSaveTicket(t, repoRoot, closed)
	mustSaveTicket(t, repoRoot, blocked)

	writeOpRunFixture(t, repoRoot, runID, state.RunArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		Mode:         "epic",
		RootTicketID: epic.ID,
		Status:       state.EpicRunStatusBlocked,
		CurrentPhase: state.TicketPhaseBlocked,
		BaseCommit:   baseCommit,
		TicketIDs:    []string{closed.ID, blocked.ID},
		ResumeCursor: map[string]any{},
	})
	for _, ticketID := range []string{closed.ID, blocked.ID} {
		writeTicketRunFixture(t, repoRoot, runID, TicketRunSnapshot{
			ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
			TicketID:     ticketID,
			CurrentPhase: state.TicketPhaseBlocked,
			BlockReason:  "retry me",
		})
		writePlanFixture(t, repoRoot, runID, state.PlanArtifact{
			ArtifactMeta:             state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
			TicketID:                 ticketID,
			EffectiveReviewThreshold: state.SeverityP2,
		})
	}

	start := epicTestStart()
	adapter := functionAdapter{
		runWorker: func(ctx context.Context, req runtime.WorkerRequest) (runtime.WorkerResult, error) {
			switch req.TicketID {
			case closed.ID:
				if err := os.WriteFile(filepath.Join(req.WorktreePath, "closed.txt"), []byte("closed output\n"), 0o644); err != nil {
					return runtime.WorkerResult{}, err
				}
				return runtime.WorkerResult{
					Status:             runtime.WorkerStatusDone,
					RetryClass:         runtime.RetryClassTerminal,
					LeaseID:            req.LeaseID,
					StartedAt:          start,
					FinishedAt:         start.Add(time.Second),
					ResultArtifactPath: filepath.Join(repoRoot, closed.ID+".worker.json"),
				}, nil
			case blocked.ID:
				if err := os.WriteFile(filepath.Join(req.WorktreePath, "blocked.txt"), []byte("blocked output\n"), 0o644); err != nil {
					return runtime.WorkerResult{}, err
				}
				return runtime.WorkerResult{
					Status:             runtime.WorkerStatusBlocked,
					RetryClass:         runtime.RetryClassRetryable,
					BlockReason:        "still blocked",
					LeaseID:            req.LeaseID,
					StartedAt:          start,
					FinishedAt:         start.Add(time.Second),
					ResultArtifactPath: filepath.Join(repoRoot, blocked.ID+".worker.json"),
				}, nil
			default:
				return runtime.WorkerResult{}, fmt.Errorf("unexpected ticket %q", req.TicketID)
			}
		},
		runReviewer: func(ctx context.Context, req runtime.ReviewRequest) (runtime.ReviewResult, error) {
			if req.TicketID != closed.ID {
				return runtime.ReviewResult{}, fmt.Errorf("unexpected review for %q", req.TicketID)
			}
			return runtime.ReviewResult{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            req.LeaseID,
				StartedAt:          start.Add(2 * time.Second),
				FinishedAt:         start.Add(3 * time.Second),
				ReviewStatus:       runtime.ReviewStatusPassed,
				Summary:            "clean",
				ResultArtifactPath: filepath.Join(repoRoot, closed.ID+".review.json"),
			}, nil
		},
	}

	report, err := ResumeRun(context.Background(), ResumeRequest{
		RepoRoot: repoRoot,
		RunID:    runID,
		Adapter:  adapter,
		Config:   cfg,
	})
	if err != nil {
		t.Fatalf("ResumeRun returned error: %v", err)
	}
	if report.Run.Status != state.EpicRunStatusBlocked {
		t.Fatalf("expected resumed run to remain blocked, got %q", report.Run.Status)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "closed.txt")); err != nil {
		t.Fatalf("expected closed sibling output integrated to main: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "blocked.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected blocked sibling output to stay out of main, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, ".verk", "runs", runID, "tickets", blocked.ID, "worktree.diff")); err != nil {
		t.Fatalf("expected blocked sibling diff artifact: %v", err)
	}
}

func TestResumeRun_CleansIntegrationWorktreeAfterWaveIntegration(t *testing.T) {
	repoRoot := t.TempDir()
	worktreeRoot := t.TempDir()
	t.Setenv("VERK_WORKTREE_ROOT", worktreeRoot)
	baseCommit := initEpicRepo(t, repoRoot)
	runID := "run-resume-clean-integration"
	cfg := policy.DefaultConfig()
	cfg.Verification.QualityCommands = nil
	cfg.Verification.WaveCommands = nil

	epic := epicTicket("epic-resume-clean-integration")
	child := epicChildTicket("ticket-resume-clean-integration", epic.ID, epos.StatusBlocked, nil, []string{"resumed.txt"})
	mustSaveTicket(t, repoRoot, epic)
	mustSaveTicket(t, repoRoot, child)

	writeOpRunFixture(t, repoRoot, runID, state.RunArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		Mode:         "epic",
		RootTicketID: epic.ID,
		Status:       state.EpicRunStatusBlocked,
		CurrentPhase: state.TicketPhaseBlocked,
		BaseCommit:   baseCommit,
		TicketIDs:    []string{child.ID},
		ResumeCursor: map[string]any{},
	})
	writeTicketRunFixture(t, repoRoot, runID, TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:     child.ID,
		CurrentPhase: state.TicketPhaseBlocked,
		BlockReason:  "retry me",
	})
	writePlanFixture(t, repoRoot, runID, state.PlanArtifact{
		ArtifactMeta:             state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:                 child.ID,
		EffectiveReviewThreshold: state.SeverityP2,
	})

	start := epicTestStart()
	adapter := functionAdapter{
		runWorker: func(ctx context.Context, req runtime.WorkerRequest) (runtime.WorkerResult, error) {
			if strings.TrimSpace(req.WorktreePath) == "" {
				return runtime.WorkerResult{}, fmt.Errorf("worker did not receive worktree path")
			}
			if err := os.WriteFile(filepath.Join(req.WorktreePath, "resumed.txt"), []byte("resumed output\n"), 0o644); err != nil {
				return runtime.WorkerResult{}, err
			}
			return runtime.WorkerResult{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            req.LeaseID,
				StartedAt:          start,
				FinishedAt:         start.Add(time.Second),
				ResultArtifactPath: filepath.Join(repoRoot, child.ID+".worker.json"),
			}, nil
		},
		runReviewer: func(ctx context.Context, req runtime.ReviewRequest) (runtime.ReviewResult, error) {
			return runtime.ReviewResult{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            req.LeaseID,
				StartedAt:          start.Add(2 * time.Second),
				FinishedAt:         start.Add(3 * time.Second),
				ReviewStatus:       runtime.ReviewStatusPassed,
				Summary:            "clean",
				ResultArtifactPath: filepath.Join(repoRoot, child.ID+".review.json"),
			}, nil
		},
	}

	report, err := ResumeRun(context.Background(), ResumeRequest{
		RepoRoot: repoRoot,
		RunID:    runID,
		Adapter:  adapter,
		Config:   cfg,
	})
	if err != nil {
		t.Fatalf("ResumeRun returned error: %v", err)
	}
	if report.Run.Status != state.EpicRunStatusCompleted {
		t.Fatalf("expected completed resumed run, got %q", report.Run.Status)
	}
	resolvedWorktreeRoot, err := ResolveWorktreeRoot(repoRoot)
	if err != nil {
		t.Fatalf("resolve worktree root: %v", err)
	}
	integrationPath := filepath.Join(resolvedWorktreeRoot, runID, "_integration")
	if _, statErr := os.Stat(integrationPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected resume integration worktree to be removed, got %q: %v", integrationPath, statErr)
	}
}

func TestResumeRun_BlocksWhenFailedTicketDiffPersistenceFails(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	runID := "run-resume-diff-persist-fail"
	cfg := policy.DefaultConfig()
	cfg.Verification.QualityCommands = nil
	cfg.Verification.WaveCommands = nil

	epic := epicTicket("epic-resume-diff-persist-fail")
	blocked := epicChildTicket("ticket-resume-diff-persist-fail", epic.ID, epos.StatusBlocked, nil, []string{"blocked.txt"})
	mustSaveTicket(t, repoRoot, epic)
	mustSaveTicket(t, repoRoot, blocked)

	writeOpRunFixture(t, repoRoot, runID, state.RunArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		Mode:         "epic",
		RootTicketID: epic.ID,
		Status:       state.EpicRunStatusBlocked,
		CurrentPhase: state.TicketPhaseBlocked,
		BaseCommit:   baseCommit,
		TicketIDs:    []string{blocked.ID},
		ResumeCursor: map[string]any{},
	})
	writeTicketRunFixture(t, repoRoot, runID, TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:     blocked.ID,
		CurrentPhase: state.TicketPhaseBlocked,
		BlockReason:  "retry me",
	})
	writePlanFixture(t, repoRoot, runID, state.PlanArtifact{
		ArtifactMeta:             state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:                 blocked.ID,
		EffectiveReviewThreshold: state.SeverityP2,
	})
	diffPath := filepath.Join(repoRoot, ".verk", "runs", runID, "tickets", blocked.ID, "worktree.diff")
	if err := os.MkdirAll(diffPath, 0o755); err != nil {
		t.Fatalf("seed unwritable diff artifact path: %v", err)
	}

	start := epicTestStart()
	adapter := functionAdapter{
		runWorker: func(ctx context.Context, req runtime.WorkerRequest) (runtime.WorkerResult, error) {
			if err := os.WriteFile(filepath.Join(req.WorktreePath, "blocked.txt"), []byte("blocked output\n"), 0o644); err != nil {
				return runtime.WorkerResult{}, err
			}
			return runtime.WorkerResult{
				Status:             runtime.WorkerStatusBlocked,
				RetryClass:         runtime.RetryClassRetryable,
				BlockReason:        "still blocked",
				LeaseID:            req.LeaseID,
				StartedAt:          start,
				FinishedAt:         start.Add(time.Second),
				ResultArtifactPath: filepath.Join(repoRoot, blocked.ID+".worker.json"),
			}, nil
		},
	}

	_, err := ResumeRun(context.Background(), ResumeRequest{
		RepoRoot: repoRoot,
		RunID:    runID,
		Adapter:  adapter,
		Config:   cfg,
	})
	if err == nil {
		t.Fatal("expected ResumeRun to fail when blocked ticket diff persistence fails")
	}
	if !strings.Contains(err.Error(), "persist diff artifact") {
		t.Fatalf("expected error to contain %q, got %v", "persist diff artifact", err)
	}
}

func TestResumeRun_DoesNotMutateMainWhenWaveCommitFails(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	runID := "run-resume-commit-fail"
	cfg := policy.DefaultConfig()
	cfg.Verification.QualityCommands = nil
	cfg.Verification.WaveCommands = nil

	epic := epicTicket("epic-resume-commit-fail")
	child := epicChildTicket("ticket-resume-commit-fail", epic.ID, epos.StatusBlocked, nil, []string{"integrated.txt"})
	mustSaveTicket(t, repoRoot, epic)
	mustSaveTicket(t, repoRoot, child)

	writeOpRunFixture(t, repoRoot, runID, state.RunArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		Mode:         "epic",
		RootTicketID: epic.ID,
		Status:       state.EpicRunStatusBlocked,
		CurrentPhase: state.TicketPhaseBlocked,
		BaseCommit:   baseCommit,
		TicketIDs:    []string{child.ID},
		ResumeCursor: map[string]any{},
	})
	writeTicketRunFixture(t, repoRoot, runID, TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:     child.ID,
		CurrentPhase: state.TicketPhaseBlocked,
		BlockReason:  "retry me",
	})
	writePlanFixture(t, repoRoot, runID, state.PlanArtifact{
		ArtifactMeta:             state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:                 child.ID,
		EffectiveReviewThreshold: state.SeverityP2,
	})

	start := epicTestStart()
	lockPath := filepath.Join(repoRoot, ".git", "refs", "verk", "runs", runID, "base.lock")
	t.Cleanup(func() { _ = os.Remove(lockPath) })
	adapter := functionAdapter{
		runWorker: func(ctx context.Context, req runtime.WorkerRequest) (runtime.WorkerResult, error) {
			if err := os.WriteFile(filepath.Join(req.WorktreePath, "integrated.txt"), []byte("integrated\n"), 0o644); err != nil {
				return runtime.WorkerResult{}, err
			}
			if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
				return runtime.WorkerResult{}, err
			}
			if err := os.WriteFile(lockPath, []byte("locked\n"), 0o644); err != nil {
				return runtime.WorkerResult{}, err
			}
			return runtime.WorkerResult{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            req.LeaseID,
				StartedAt:          start,
				FinishedAt:         start.Add(time.Second),
				ResultArtifactPath: filepath.Join(repoRoot, child.ID+".worker.json"),
			}, nil
		},
		runReviewer: func(ctx context.Context, req runtime.ReviewRequest) (runtime.ReviewResult, error) {
			return runtime.ReviewResult{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            req.LeaseID,
				StartedAt:          start.Add(2 * time.Second),
				FinishedAt:         start.Add(3 * time.Second),
				ReviewStatus:       runtime.ReviewStatusPassed,
				Summary:            "clean",
				ResultArtifactPath: filepath.Join(repoRoot, child.ID+".review.json"),
			}, nil
		},
	}

	_, err := ResumeRun(context.Background(), ResumeRequest{
		RepoRoot: repoRoot,
		RunID:    runID,
		Adapter:  adapter,
		Config:   cfg,
	})
	if err == nil {
		t.Fatal("expected ResumeRun to fail when hidden base ref cannot advance")
	}
	if _, statErr := os.Stat(filepath.Join(repoRoot, "integrated.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("main tree was mutated before hidden base advanced: %v", statErr)
	}
}

func TestResumeRun_DoesNotAdvanceHiddenBaseWhenMainApplyFails(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	runID := "run-resume-main-apply-fail"
	cfg := policy.DefaultConfig()
	cfg.Verification.QualityCommands = nil
	cfg.Verification.WaveCommands = nil

	epic := epicTicket("epic-resume-main-apply-fail")
	child := epicChildTicket("ticket-resume-main-apply-fail", epic.ID, epos.StatusBlocked, nil, []string{"integrated.txt"})
	mustSaveTicket(t, repoRoot, epic)
	mustSaveTicket(t, repoRoot, child)

	writeOpRunFixture(t, repoRoot, runID, state.RunArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		Mode:         "epic",
		RootTicketID: epic.ID,
		Status:       state.EpicRunStatusBlocked,
		CurrentPhase: state.TicketPhaseBlocked,
		BaseCommit:   baseCommit,
		TicketIDs:    []string{child.ID},
		ResumeCursor: map[string]any{},
	})
	writeTicketRunFixture(t, repoRoot, runID, TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:     child.ID,
		CurrentPhase: state.TicketPhaseBlocked,
		BlockReason:  "retry me",
	})
	writePlanFixture(t, repoRoot, runID, state.PlanArtifact{
		ArtifactMeta:             state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:                 child.ID,
		EffectiveReviewThreshold: state.SeverityP2,
	})

	start := epicTestStart()
	lockPath := filepath.Join(repoRoot, ".git", "index.lock")
	t.Cleanup(func() { _ = os.Remove(lockPath) })
	adapter := functionAdapter{
		runWorker: func(ctx context.Context, req runtime.WorkerRequest) (runtime.WorkerResult, error) {
			if req.TicketID != child.ID {
				return runtime.WorkerResult{}, fmt.Errorf("unexpected worker ticket %q", req.TicketID)
			}
			if err := os.WriteFile(filepath.Join(req.WorktreePath, "integrated.txt"), []byte("integrated\n"), 0o644); err != nil {
				return runtime.WorkerResult{}, err
			}
			if err := os.WriteFile(lockPath, []byte("locked\n"), 0o644); err != nil {
				return runtime.WorkerResult{}, err
			}
			return runtime.WorkerResult{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            req.LeaseID,
				StartedAt:          start,
				FinishedAt:         start.Add(time.Second),
				ResultArtifactPath: filepath.Join(repoRoot, child.ID+".worker.json"),
			}, nil
		},
		runReviewer: func(ctx context.Context, req runtime.ReviewRequest) (runtime.ReviewResult, error) {
			return runtime.ReviewResult{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            req.LeaseID,
				StartedAt:          start.Add(2 * time.Second),
				FinishedAt:         start.Add(3 * time.Second),
				ReviewStatus:       runtime.ReviewStatusPassed,
				Summary:            "clean",
				ResultArtifactPath: filepath.Join(repoRoot, child.ID+".review.json"),
			}, nil
		},
	}

	_, err := ResumeRun(context.Background(), ResumeRequest{
		RepoRoot: repoRoot,
		RunID:    runID,
		Adapter:  adapter,
		Config:   cfg,
	})
	if err == nil {
		t.Fatal("expected ResumeRun to fail when applying integrated delta to main")
	}
	if _, statErr := os.Stat(filepath.Join(repoRoot, "integrated.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("main tree was mutated despite apply failure: %v", statErr)
	}
	newBaseHead, parseErr := gitRevParse(repoRoot, integrationBaseRef(runID))
	if parseErr != nil {
		t.Fatalf("expected hidden base ref to remain available: %v", parseErr)
	}
	if newBaseHead != baseCommit {
		t.Fatalf("hidden base advanced before main apply succeeded: got %s, want %s", newBaseHead, baseCommit)
	}

	var run state.RunArtifact
	if err := state.LoadJSON(runJSONPath(repoRoot, runID), &run); err != nil {
		t.Fatalf("load run artifact: %v", err)
	}
	if pending, ok := pendingWaveVerificationID(run.ResumeCursor); !ok || pending != "wave-1" {
		t.Fatalf("expected pending wave verification to remain for retry, cursor=%v", run.ResumeCursor)
	}
	if got, ok := run.ResumeCursor["last_wave_base_commit"].(string); ok && got != baseCommit {
		t.Fatalf("last_wave_base_commit advanced before main apply succeeded: %q", got)
	}
}

func TestResumeRun_AppliesPostRepairFilesToMain(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	runID := "run-resume-post-repair-main"
	cfg := policy.DefaultConfig()
	cfg.Verification.QualityCommands = nil
	cfg.Verification.WaveCommands = []policy.QualityCommand{{Path: ".", Run: []string{"test -f repair.txt"}}}
	cfg.Policy.MaxWaveRepairCycles = 1

	epic := epicTicket("epic-resume-post-repair-main")
	child := epicChildTicket("ticket-resume-post-repair-main", epic.ID, epos.StatusBlocked, nil, []string{"primary.txt"})
	mustSaveTicket(t, repoRoot, epic)
	mustSaveTicket(t, repoRoot, child)

	writeOpRunFixture(t, repoRoot, runID, state.RunArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		Mode:         "epic",
		RootTicketID: epic.ID,
		Status:       state.EpicRunStatusBlocked,
		CurrentPhase: state.TicketPhaseBlocked,
		BaseCommit:   baseCommit,
		TicketIDs:    []string{child.ID},
		ResumeCursor: map[string]any{},
	})
	writeTicketRunFixture(t, repoRoot, runID, TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:     child.ID,
		CurrentPhase: state.TicketPhaseBlocked,
		BlockReason:  "retry me",
	})
	writePlanFixture(t, repoRoot, runID, state.PlanArtifact{
		ArtifactMeta:             state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:                 child.ID,
		EffectiveReviewThreshold: state.SeverityP2,
	})

	start := epicTestStart()
	adapter := functionAdapter{
		runWorker: func(ctx context.Context, req runtime.WorkerRequest) (runtime.WorkerResult, error) {
			if req.WaveID != "" {
				if err := os.WriteFile(filepath.Join(req.WorktreePath, "repair.txt"), []byte("repair\n"), 0o644); err != nil {
					return runtime.WorkerResult{}, err
				}
				return runtime.WorkerResult{
					Status:             runtime.WorkerStatusDone,
					RetryClass:         runtime.RetryClassTerminal,
					LeaseID:            req.LeaseID,
					StartedAt:          start,
					FinishedAt:         start.Add(time.Second),
					ResultArtifactPath: filepath.Join(repoRoot, "wave-repair.worker.json"),
				}, nil
			}
			if req.TicketID != child.ID {
				return runtime.WorkerResult{}, fmt.Errorf("unexpected worker ticket %q", req.TicketID)
			}
			if err := os.WriteFile(filepath.Join(req.WorktreePath, "primary.txt"), []byte("primary\n"), 0o644); err != nil {
				return runtime.WorkerResult{}, err
			}
			return runtime.WorkerResult{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            req.LeaseID,
				StartedAt:          start,
				FinishedAt:         start.Add(time.Second),
				ResultArtifactPath: filepath.Join(repoRoot, child.ID+".worker.json"),
			}, nil
		},
		runReviewer: func(ctx context.Context, req runtime.ReviewRequest) (runtime.ReviewResult, error) {
			return runtime.ReviewResult{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            req.LeaseID,
				StartedAt:          start.Add(2 * time.Second),
				FinishedAt:         start.Add(3 * time.Second),
				ReviewStatus:       runtime.ReviewStatusPassed,
				Summary:            "clean",
				ResultArtifactPath: filepath.Join(repoRoot, "review.json"),
			}, nil
		},
	}

	report, err := ResumeRun(context.Background(), ResumeRequest{
		RepoRoot: repoRoot,
		RunID:    runID,
		Adapter:  adapter,
		Config:   cfg,
	})
	if err != nil {
		t.Fatalf("ResumeRun returned error: %v", err)
	}
	if report.Run.Status != state.EpicRunStatusCompleted {
		t.Fatalf("expected completed run, got %q", report.Run.Status)
	}
	for _, path := range []string{"primary.txt", "repair.txt"} {
		if _, err := os.Stat(filepath.Join(repoRoot, path)); err != nil {
			t.Fatalf("expected %s applied to main: %v", path, err)
		}
	}
}

func TestResumeRun_ReconstructsHiddenIntegrationBaseAndLaterWaveSeesAcceptedFiles(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	runID := "run-resume-hidden-base"
	cfg := policy.DefaultConfig()
	cfg.Verification.QualityCommands = nil

	epic := epicTicket("epic-resume-hidden-base")
	mustSaveTicket(t, repoRoot, epic)
	first := epicChildTicket("ticket-resume-wave-1", epic.ID, epos.StatusClosed, nil, []string{"wave1.txt"})
	second := epicChildTicket("ticket-resume-wave-2", epic.ID, epos.StatusBlocked, []string{first.ID}, []string{"wave2.txt"})
	mustSaveTicket(t, repoRoot, first)
	mustSaveTicket(t, repoRoot, second)

	hiddenBaseCommit := createDetachedWorktreeCommit(t, repoRoot, baseCommit, map[string]string{
		"wave1.txt": "wave one\n",
	})
	if err := os.WriteFile(filepath.Join(repoRoot, "wave1.txt"), []byte("wave one\n"), 0o644); err != nil {
		t.Fatalf("seed main tree accepted wave 1 output: %v", err)
	}
	mustRunGit(t, repoRoot, "add", "wave1.txt")

	writeOpRunFixture(t, repoRoot, runID, state.RunArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		Mode:         "epic",
		RootTicketID: epic.ID,
		Status:       state.EpicRunStatusBlocked,
		CurrentPhase: state.TicketPhaseBlocked,
		BaseCommit:   baseCommit,
		TicketIDs:    []string{first.ID, second.ID},
		WaveIDs:      []string{"wave-1"},
		ResumeCursor: map[string]any{
			"wave_ordinal":                1,
			"last_wave_base_commit":       hiddenBaseCommit,
			"wave_baseline_changed_files": []string{},
		},
	})
	writeTicketRunFixture(t, repoRoot, runID, TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:     first.ID,
		CurrentPhase: state.TicketPhaseClosed,
		Closeout:     &state.CloseoutArtifact{Closable: true},
	})
	writeTicketRunFixture(t, repoRoot, runID, TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:     second.ID,
		CurrentPhase: state.TicketPhaseBlocked,
		BlockReason:  "retry me",
	})
	writePlanFixture(t, repoRoot, runID, state.PlanArtifact{
		ArtifactMeta:             state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:                 first.ID,
		EffectiveReviewThreshold: state.SeverityP2,
	})
	writePlanFixture(t, repoRoot, runID, state.PlanArtifact{
		ArtifactMeta:             state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:                 second.ID,
		EffectiveReviewThreshold: state.SeverityP2,
	})
	writeWaveFixture(t, repoRoot, runID, state.WaveArtifact{
		ArtifactMeta:   state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		WaveID:         "wave-1",
		Ordinal:        1,
		Status:         state.WaveStatusAccepted,
		TicketIDs:      []string{first.ID},
		WaveBaseCommit: baseCommit,
		StartedAt:      epicTestStart(),
		FinishedAt:     epicTestStart().Add(time.Second),
		Acceptance: map[string]any{
			"wave_verification_passed": true,
		},
	})

	start := epicTestStart()
	workerCalls := 0
	reviewerCalls := 0
	adapter := functionAdapter{
		runWorker: func(ctx context.Context, req runtime.WorkerRequest) (runtime.WorkerResult, error) {
			workerCalls++
			content, err := os.ReadFile(filepath.Join(req.WorktreePath, "wave1.txt"))
			if err != nil {
				return runtime.WorkerResult{}, fmt.Errorf("resume worker missing accepted wave 1 file: %w", err)
			}
			if strings.TrimSpace(string(content)) != "wave one" {
				return runtime.WorkerResult{}, fmt.Errorf("resume worker saw unexpected wave1 content %q", string(content))
			}
			if err := os.WriteFile(filepath.Join(req.WorktreePath, "wave2.txt"), []byte("wave two\n"), 0o644); err != nil {
				return runtime.WorkerResult{}, err
			}
			return runtime.WorkerResult{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            req.LeaseID,
				StartedAt:          start,
				FinishedAt:         start.Add(time.Second),
				ResultArtifactPath: filepath.Join(repoRoot, "resume-wave-2.worker.json"),
			}, nil
		},
		runReviewer: func(ctx context.Context, req runtime.ReviewRequest) (runtime.ReviewResult, error) {
			reviewerCalls++
			return runtime.ReviewResult{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            req.LeaseID,
				StartedAt:          start.Add(2 * time.Second),
				FinishedAt:         start.Add(3 * time.Second),
				ReviewStatus:       runtime.ReviewStatusPassed,
				Summary:            "clean",
				ResultArtifactPath: filepath.Join(repoRoot, req.TicketID+".review.json"),
			}, nil
		},
	}

	report, err := ResumeRun(context.Background(), ResumeRequest{
		RepoRoot: repoRoot,
		RunID:    runID,
		Adapter:  adapter,
		Config:   cfg,
	})
	if err != nil {
		t.Fatalf("ResumeRun returned error: %v", err)
	}
	if report.Run.Status != state.EpicRunStatusCompleted {
		t.Fatalf("expected completed resumed run, got %q (workerCalls=%d reviewerCalls=%d)", report.Run.Status, workerCalls, reviewerCalls)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "wave2.txt")); err != nil {
		t.Fatalf("expected resumed wave output in main tree: %v", err)
	}
	if _, err := gitOutput(repoRoot, "show-ref", "--verify", integrationBaseRef(runID)); err != nil {
		t.Fatalf("expected reconstructed hidden integration base ref: %v", err)
	}
}

func TestResumeRun_FailsWhenIntegrationBaseCannotBeReconstructed(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	runID := "run-resume-missing-hidden-base"
	cfg := policy.DefaultConfig()
	cfg.Verification.QualityCommands = nil

	epic := epicTicket("epic-resume-missing-hidden-base")
	mustSaveTicket(t, repoRoot, epic)
	first := epicChildTicket("ticket-missing-base-closed", epic.ID, epos.StatusClosed, nil, []string{"wave1.txt"})
	second := epicChildTicket("ticket-missing-base-blocked", epic.ID, epos.StatusBlocked, []string{first.ID}, []string{"wave2.txt"})
	mustSaveTicket(t, repoRoot, first)
	mustSaveTicket(t, repoRoot, second)

	writeOpRunFixture(t, repoRoot, runID, state.RunArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		Mode:         "epic",
		RootTicketID: epic.ID,
		Status:       state.EpicRunStatusBlocked,
		CurrentPhase: state.TicketPhaseBlocked,
		BaseCommit:   baseCommit,
		TicketIDs:    []string{first.ID, second.ID},
		WaveIDs:      []string{"wave-1"},
		ResumeCursor: map[string]any{
			"wave_ordinal": 1,
		},
	})
	writeTicketRunFixture(t, repoRoot, runID, TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:     first.ID,
		CurrentPhase: state.TicketPhaseClosed,
		Closeout:     &state.CloseoutArtifact{Closable: true},
	})
	writeTicketRunFixture(t, repoRoot, runID, TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:     second.ID,
		CurrentPhase: state.TicketPhaseBlocked,
		BlockReason:  "retry me",
	})
	writePlanFixture(t, repoRoot, runID, state.PlanArtifact{
		ArtifactMeta:             state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:                 first.ID,
		EffectiveReviewThreshold: state.SeverityP2,
	})
	writePlanFixture(t, repoRoot, runID, state.PlanArtifact{
		ArtifactMeta:             state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:                 second.ID,
		EffectiveReviewThreshold: state.SeverityP2,
	})
	writeWaveFixture(t, repoRoot, runID, state.WaveArtifact{
		ArtifactMeta:   state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		WaveID:         "wave-1",
		Ordinal:        1,
		Status:         state.WaveStatusAccepted,
		TicketIDs:      []string{first.ID},
		WaveBaseCommit: baseCommit,
		StartedAt:      epicTestStart(),
		FinishedAt:     epicTestStart().Add(time.Second),
		Acceptance: map[string]any{
			"wave_verification_passed": true,
		},
	})

	adapter := runtimefake.New([]runtime.WorkerResult{
		validWorkerResult("lease-should-not-run", filepath.Join(repoRoot, "should-not-run.worker.json")),
	}, nil)

	_, err := ResumeRun(context.Background(), ResumeRequest{
		RepoRoot: repoRoot,
		RunID:    runID,
		Adapter:  adapter,
		Config:   cfg,
	})
	if err == nil {
		t.Fatal("expected resume to fail when hidden integration base cannot be reconstructed")
	}
	if !strings.Contains(err.Error(), "reconstruct integration base") {
		t.Fatalf("expected integration base reconstruction error, got %v", err)
	}
	if got := adapter.WorkerRequests(); len(got) != 0 {
		t.Fatalf("expected no workers to run when integration base reconstruction fails, got %d", len(got))
	}
}

func createDetachedWorktreeCommit(t *testing.T, repoRoot, baseCommit string, files map[string]string) string {
	t.Helper()

	worktreePath := filepath.Join(t.TempDir(), "commit-worktree")
	mustRunGit(t, repoRoot, "worktree", "add", "--detach", worktreePath, baseCommit)
	t.Cleanup(func() {
		_ = exec.Command("git", "-C", repoRoot, "worktree", "remove", "--force", worktreePath).Run()
	})
	for rel, content := range files {
		full := filepath.Join(worktreePath, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	mustRunGit(t, worktreePath, "add", ".")
	mustRunGit(t, worktreePath, "commit", "-m", "hidden wave base")
	head, err := gitOutput(worktreePath, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse hidden wave base head: %v", err)
	}
	mustRunGit(t, repoRoot, "worktree", "remove", "--force", worktreePath)
	return strings.TrimSpace(head)
}
