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

func TestRunTicket_HappyPath(t *testing.T) {
	repoRoot := t.TempDir()
	cfg := policy.DefaultConfig()
	cfg.Runtime.WorkerTimeoutMinutes = 7
	cfg.Runtime.ReviewerTimeoutMinutes = 9
	cfg.Runtime.AuthEnvVars = []string{"VERK_API_KEY"}
	ticket := testTicket("ver-happy")
	plan, claim := testPlanAndClaim(t, repoRoot, ticket, cfg, "run-happy", "lease-happy", []string{`true`})

	started, finished := testRunTimes()
	adapter := runtimefake.New(
		[]runtime.WorkerResult{
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            claim.LeaseID,
				StartedAt:          started,
				FinishedAt:         finished,
				TokenUsage:         &state.RuntimeTokenUsage{InputTokens: 100, CachedInputTokens: 60, OutputTokens: 20, TotalTokens: 120},
				ActivityStats:      &state.RuntimeActivityStats{EventCount: 5, CommandCount: 2, AgentMessageCount: 1},
				ResultArtifactPath: filepath.Join(repoRoot, "worker.json"),
			},
		},
		[]runtime.ReviewResult{
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            claim.LeaseID,
				StartedAt:          finished.Add(time.Second),
				FinishedAt:         finished.Add(2 * time.Second),
				ReviewStatus:       runtime.ReviewStatusPassed,
				Summary:            "clean",
				TokenUsage:         &state.RuntimeTokenUsage{InputTokens: 200, CachedInputTokens: 150, OutputTokens: 30, TotalTokens: 230},
				ActivityStats:      &state.RuntimeActivityStats{EventCount: 4, CommandCount: 1, AgentMessageCount: 1},
				ResultArtifactPath: filepath.Join(repoRoot, "review.json"),
			},
		},
	)

	result, err := RunTicket(context.Background(), RunTicketRequest{
		RepoRoot: repoRoot,
		RunID:    "run-happy",
		Ticket:   ticket,
		Plan:     plan,
		Claim:    claim,
		Adapter:  adapter,
		Config:   cfg,
	})
	if err != nil {
		t.Fatalf("RunTicket returned error: %v", err)
	}
	if result.Snapshot.CurrentPhase != state.TicketPhaseClosed {
		t.Fatalf("expected closed phase, got %q", result.Snapshot.CurrentPhase)
	}
	if result.Snapshot.Outcome != state.TicketOutcomeClosed {
		t.Fatalf("expected closed outcome, got %q", result.Snapshot.Outcome)
	}
	if result.Snapshot.Closeout == nil || !result.Snapshot.Closeout.Closable {
		t.Fatalf("expected closable closeout, got %#v", result.Snapshot.Closeout)
	}
	if result.Snapshot.BlockReason != "" {
		t.Fatalf("expected no block reason, got %q", result.Snapshot.BlockReason)
	}
	if got := result.Snapshot.Implementation; got == nil || got.TokenUsage == nil || got.TokenUsage.InputTokens != 100 || got.ActivityStats == nil || got.ActivityStats.CommandCount != 2 {
		t.Fatalf("expected implementation runtime telemetry to persist, got %#v", got)
	}
	if got := result.Snapshot.Review; got == nil || got.StartedAt.IsZero() || got.FinishedAt.IsZero() || got.TokenUsage == nil || got.TokenUsage.InputTokens != 200 || got.ActivityStats == nil || got.ActivityStats.CommandCount != 1 || len(got.Artifacts) != 1 {
		t.Fatalf("expected review timing and runtime telemetry to persist, got %#v", got)
	}

	snapshotPath := filepath.Join(repoRoot, ".verk", "runs", "run-happy", "tickets", ticket.ID, "ticket-run.json")
	if _, err := os.Stat(snapshotPath); err != nil {
		t.Fatalf("expected snapshot file to exist: %v", err)
	}
	var persistedSnapshot TicketRunSnapshot
	if err := state.LoadJSON(snapshotPath, &persistedSnapshot); err != nil {
		t.Fatalf("load persisted snapshot: %v", err)
	}
	if persistedSnapshot.Outcome != state.TicketOutcomeClosed {
		t.Fatalf("expected persisted closed outcome, got %q", persistedSnapshot.Outcome)
	}

	durableClaimPath := filepath.Join(repoRoot, ".verk", "runs", "run-happy", "claims", "claim-"+ticket.ID+".json")
	var durableClaim state.ClaimArtifact
	if err := state.LoadJSON(durableClaimPath, &durableClaim); err != nil {
		t.Fatalf("load durable claim: %v", err)
	}
	if durableClaim.State != "released" {
		t.Fatalf("expected released claim state, got %q", durableClaim.State)
	}
	if durableClaim.ReleaseReason != "completed" {
		t.Fatalf("expected completed release reason, got %q", durableClaim.ReleaseReason)
	}
	if len(adapter.WorkerRequests()) != 1 {
		t.Fatalf("expected 1 worker request, got %d", len(adapter.WorkerRequests()))
	}
	if len(adapter.ReviewRequests()) != 1 {
		t.Fatalf("expected 1 review request, got %d", len(adapter.ReviewRequests()))
	}
	if got := adapter.WorkerRequests()[0].ExecutionConfig; got.WorkerTimeoutMinutes != 7 || got.ReviewerTimeoutMinutes != 9 || len(got.AuthEnvVars) != 1 || got.AuthEnvVars[0] != "VERK_API_KEY" {
		t.Fatalf("unexpected worker execution config: %#v", got)
	}
	if got := adapter.ReviewRequests()[0].ExecutionConfig; got.WorkerTimeoutMinutes != 7 || got.ReviewerTimeoutMinutes != 9 || len(got.AuthEnvVars) != 1 || got.AuthEnvVars[0] != "VERK_API_KEY" {
		t.Fatalf("unexpected review execution config: %#v", got)
	}
}

func TestExecuteVerification_AdvisoryDerivedFailureTriggersBestEffortRepair(t *testing.T) {
	repoRoot := t.TempDir()
	installFailingRuff(t)

	cfg := policy.DefaultConfig()
	cfg.Policy.MaxImplementationAttempts = 3

	stubToolSignals(t, ToolSignals{HasRuff: true})

	st := newVerificationTestState(t, "run-advisory-repair", "ver-advisory-repair", cfg, 1)
	st.repoRoot = repoRoot
	st.implementation = &state.ImplementationArtifact{
		ChangedFiles: []string{"tests/test_smoke.py"},
	}

	blocked, err := st.executeVerification(context.Background(), repoRoot)
	if err != nil {
		t.Fatalf("executeVerification: %v", err)
	}
	if blocked {
		t.Fatalf("expected advisory-only derived failure not to block")
	}
	if st.currentPhase != state.TicketPhaseImplement {
		t.Fatalf("expected best-effort advisory repair to return to implement phase, got %q", st.currentPhase)
	}
	if len(st.repairCycles) != 1 {
		t.Fatalf("expected one best-effort repair cycle, got %d", len(st.repairCycles))
	}
	if len(st.repairCycles[0].TriggerCheckIDs) != 1 {
		t.Fatalf("expected one triggering advisory check id, got %#v", st.repairCycles[0].TriggerCheckIDs)
	}
	if st.verification == nil || st.verification.ValidationCoverage == nil {
		t.Fatalf("expected verification coverage to be recorded")
	}
	if !st.verification.Passed {
		t.Fatalf("expected advisory-only failure to keep verification artifact passed")
	}
	if len(st.verification.ValidationCoverage.UnresolvedBlockers) != 0 {
		t.Fatalf("expected no unresolved blockers for advisory-only failure, got %#v", st.verification.ValidationCoverage.UnresolvedBlockers)
	}
}

func TestAdvisoryFailingCheckIDs_DeclaredCheckWinsIDCollision(t *testing.T) {
	coverage := state.ValidationCoverageArtifact{
		DeclaredChecks: []state.ValidationCheck{{
			ID:       "same-id",
			Command:  "just check",
			Advisory: false,
		}},
		DerivedChecks: []state.ValidationCheck{{
			ID:       "same-id",
			Command:  "just check",
			Advisory: true,
		}},
		ExecutedChecks: []state.ValidationCheckExecution{{
			CheckID: "same-id",
			Result:  state.ValidationCheckResultFailed,
		}},
	}

	if got := advisoryFailingCheckIDs(coverage); len(got) != 0 {
		t.Fatalf("declared check should override advisory derived collision, got %#v", got)
	}
}

func TestExecuteVerification_AdvisoryDerivedFailureDoesNotBlockAfterRepairBudget(t *testing.T) {
	repoRoot := t.TempDir()
	installFailingRuff(t)

	cfg := policy.DefaultConfig()
	cfg.Policy.MaxImplementationAttempts = 1

	stubToolSignals(t, ToolSignals{HasRuff: true})

	st := newVerificationTestState(t, "run-advisory-budget", "ver-advisory-budget", cfg, 1)
	st.repoRoot = repoRoot
	st.implementation = &state.ImplementationArtifact{
		ChangedFiles: []string{"tests/test_smoke.py"},
	}

	blocked, err := st.executeVerification(context.Background(), repoRoot)
	if err != nil {
		t.Fatalf("executeVerification: %v", err)
	}
	if blocked {
		t.Fatalf("expected advisory-only derived failure not to block after repair budget")
	}
	if st.currentPhase != state.TicketPhaseReview {
		t.Fatalf("expected advisory-only failure to continue to review after budget, got %q", st.currentPhase)
	}
	if len(st.repairCycles) != 0 {
		t.Fatalf("expected no extra advisory repair cycle after budget, got %d", len(st.repairCycles))
	}
	if st.blockReason != "" {
		t.Fatalf("expected no block reason for advisory-only failure, got %q", st.blockReason)
	}
	if st.verification == nil || !st.verification.Passed {
		t.Fatalf("expected advisory-only failure to keep verification artifact passed")
	}
}

func installFailingRuff(t *testing.T) {
	t.Helper()
	binDir := t.TempDir()
	ruffPath := filepath.Join(binDir, "ruff")
	if err := os.WriteFile(ruffPath, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write fake ruff: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func stubToolSignals(t *testing.T, signals ToolSignals) {
	t.Helper()
	originalToolSignals := toolSignalsProvider
	t.Cleanup(func() {
		toolSignalsProvider = originalToolSignals
	})
	toolSignalsProvider = func(string) ToolSignals {
		return signals
	}
}

func TestRunTicket_VerifyFailureLoopsToImplement(t *testing.T) {
	repoRoot := t.TempDir()
	cfg := policy.DefaultConfig()
	ticket := testTicket("ver-verify-loop")
	plan, claim := testPlanAndClaim(t, repoRoot, ticket, cfg, "run-verify-loop", "lease-verify-loop", []string{verifyToggleCommand()})

	started, finished := testRunTimes()
	adapter := runtimefake.New(
		[]runtime.WorkerResult{
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            claim.LeaseID,
				StartedAt:          started,
				FinishedAt:         finished,
				ResultArtifactPath: filepath.Join(repoRoot, "worker-1.json"),
			},
			{
				Status:             runtime.WorkerStatusDoneWithConcerns,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            claim.LeaseID,
				StartedAt:          finished.Add(3 * time.Second),
				FinishedAt:         finished.Add(4 * time.Second),
				ResultArtifactPath: filepath.Join(repoRoot, "worker-2.json"),
			},
		},
		[]runtime.ReviewResult{
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            claim.LeaseID,
				StartedAt:          finished.Add(5 * time.Second),
				FinishedAt:         finished.Add(6 * time.Second),
				ReviewStatus:       runtime.ReviewStatusPassed,
				Summary:            "clean",
				ResultArtifactPath: filepath.Join(repoRoot, "review.json"),
			},
		},
	)

	result, err := RunTicket(context.Background(), RunTicketRequest{
		RepoRoot: repoRoot,
		RunID:    "run-verify-loop",
		Ticket:   ticket,
		Plan:     plan,
		Claim:    claim,
		Adapter:  adapter,
		Config:   cfg,
	})
	if err != nil {
		t.Fatalf("RunTicket returned error: %v", err)
	}
	if result.Snapshot.CurrentPhase != state.TicketPhaseClosed {
		t.Fatalf("expected closed phase, got %q", result.Snapshot.CurrentPhase)
	}
	if result.Snapshot.ImplementationAttempts != 2 {
		t.Fatalf("expected 2 implementation attempts, got %d", result.Snapshot.ImplementationAttempts)
	}
	if result.Snapshot.VerificationAttempts != 2 {
		t.Fatalf("expected 2 verification attempts, got %d", result.Snapshot.VerificationAttempts)
	}
	if result.Snapshot.ReviewAttempts != 1 {
		t.Fatalf("expected 1 review attempt, got %d", result.Snapshot.ReviewAttempts)
	}
	if len(adapter.WorkerRequests()) != 2 {
		t.Fatalf("expected 2 worker requests, got %d", len(adapter.WorkerRequests()))
	}
	if len(adapter.ReviewRequests()) != 1 {
		t.Fatalf("expected 1 review request, got %d", len(adapter.ReviewRequests()))
	}
}

func TestRunTicket_RepairLimitBlocks(t *testing.T) {
	repoRoot := t.TempDir()
	cfg := policy.DefaultConfig()
	cfg.Policy.MaxRepairCycles = 1
	ticket := testTicket("ver-repair-limit")
	plan, claim := testPlanAndClaim(t, repoRoot, ticket, cfg, "run-repair-limit", "lease-repair-limit", []string{`true`})

	started, finished := testRunTimes()
	blockingFinding := runtime.ReviewFinding{
		ID:          "finding-1",
		Severity:    runtime.SeverityP2,
		Title:       "blocking issue",
		Body:        "blocking issue",
		File:        "internal/example.go",
		Line:        12,
		Disposition: runtime.ReviewDispositionOpen,
	}
	adapter := runtimefake.New(
		[]runtime.WorkerResult{
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            claim.LeaseID,
				StartedAt:          started,
				FinishedAt:         finished,
				ResultArtifactPath: filepath.Join(repoRoot, "worker-1.json"),
			},
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            claim.LeaseID,
				StartedAt:          finished.Add(3 * time.Second),
				FinishedAt:         finished.Add(4 * time.Second),
				ResultArtifactPath: filepath.Join(repoRoot, "worker-2.json"),
			},
		},
		[]runtime.ReviewResult{
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            claim.LeaseID,
				StartedAt:          finished.Add(5 * time.Second),
				FinishedAt:         finished.Add(6 * time.Second),
				ReviewStatus:       runtime.ReviewStatusFindings,
				Summary:            "needs repair",
				Findings:           []runtime.ReviewFinding{blockingFinding},
				ResultArtifactPath: filepath.Join(repoRoot, "review-1.json"),
			},
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            claim.LeaseID,
				StartedAt:          finished.Add(7 * time.Second),
				FinishedAt:         finished.Add(8 * time.Second),
				ReviewStatus:       runtime.ReviewStatusFindings,
				Summary:            "still blocked",
				Findings:           []runtime.ReviewFinding{blockingFinding},
				ResultArtifactPath: filepath.Join(repoRoot, "review-2.json"),
			},
		},
	)

	result, err := RunTicket(context.Background(), RunTicketRequest{
		RepoRoot: repoRoot,
		RunID:    "run-repair-limit",
		Ticket:   ticket,
		Plan:     plan,
		Claim:    claim,
		Adapter:  adapter,
		Config:   cfg,
	})
	if err != nil {
		t.Fatalf("RunTicket returned error: %v", err)
	}
	if result.Snapshot.CurrentPhase != state.TicketPhaseBlocked {
		t.Fatalf("expected blocked phase, got %q", result.Snapshot.CurrentPhase)
	}
	if !strings.Contains(result.Snapshot.BlockReason, "repair limit") {
		t.Fatalf("expected repair limit block reason, got %q", result.Snapshot.BlockReason)
	}
	if len(result.Snapshot.RepairCycles) != 2 {
		t.Fatalf("expected 2 repair cycles, got %d", len(result.Snapshot.RepairCycles))
	}

	durableClaimPath := filepath.Join(repoRoot, ".verk", "runs", "run-repair-limit", "claims", "claim-"+ticket.ID+".json")
	var durableClaim state.ClaimArtifact
	if err := state.LoadJSON(durableClaimPath, &durableClaim); err != nil {
		t.Fatalf("load durable claim: %v", err)
	}
	if durableClaim.State != "released" {
		t.Fatalf("expected released claim state, got %q", durableClaim.State)
	}
}

func TestRunTicket_RetryableRuntimeFailuresRetryBeforeBlocking(t *testing.T) {
	repoRoot := t.TempDir()
	cfg := policy.DefaultConfig()
	ticket := testTicket("ver-runtime-retry")
	plan, claim := testPlanAndClaim(t, repoRoot, ticket, cfg, "run-runtime-retry", "lease-runtime-retry", []string{`true`})

	started, finished := testRunTimes()
	adapter := runtimefake.New(
		[]runtime.WorkerResult{
			{
				Status:             runtime.WorkerStatusBlocked,
				RetryClass:         runtime.RetryClassRetryable,
				LeaseID:            claim.LeaseID,
				StartedAt:          started,
				FinishedAt:         finished,
				ResultArtifactPath: filepath.Join(repoRoot, "worker-1.json"),
			},
			{
				Status:             runtime.WorkerStatusBlocked,
				RetryClass:         runtime.RetryClassRetryable,
				LeaseID:            claim.LeaseID,
				StartedAt:          finished.Add(3 * time.Second),
				FinishedAt:         finished.Add(4 * time.Second),
				ResultArtifactPath: filepath.Join(repoRoot, "worker-2.json"),
			},
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            claim.LeaseID,
				StartedAt:          finished.Add(5 * time.Second),
				FinishedAt:         finished.Add(6 * time.Second),
				ResultArtifactPath: filepath.Join(repoRoot, "worker-3.json"),
			},
		},
		[]runtime.ReviewResult{
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            claim.LeaseID,
				StartedAt:          finished.Add(7 * time.Second),
				FinishedAt:         finished.Add(8 * time.Second),
				ReviewStatus:       runtime.ReviewStatusPassed,
				Summary:            "clean",
				ResultArtifactPath: filepath.Join(repoRoot, "review.json"),
			},
		},
	)

	result, err := RunTicket(context.Background(), RunTicketRequest{
		RepoRoot: repoRoot,
		RunID:    "run-runtime-retry",
		Ticket:   ticket,
		Plan:     plan,
		Claim:    claim,
		Adapter:  adapter,
		Config:   cfg,
	})
	if err != nil {
		t.Fatalf("RunTicket returned error: %v", err)
	}
	if result.Snapshot.CurrentPhase != state.TicketPhaseClosed {
		t.Fatalf("expected closed phase, got %q", result.Snapshot.CurrentPhase)
	}
	if len(adapter.WorkerRequests()) != 3 {
		t.Fatalf("expected 3 worker requests after retry budget, got %d", len(adapter.WorkerRequests()))
	}
}

func TestRunTicket_RenewsClaimDuringLongRunningWorker(t *testing.T) {
	repoRoot := t.TempDir()
	cfg := policy.DefaultConfig()
	ticket := testTicket("ver-claim-renewal")
	plan, claim := testPlanAndClaim(t, repoRoot, ticket, cfg, "run-claim-renewal", "lease-claim-renewal", []string{`true`})
	now := time.Now().UTC()
	claim.LeasedAt = now
	claim.ExpiresAt = now.Add(500 * time.Millisecond)
	durableClaimPath := seedClaimSnapshots(t, repoRoot, "run-claim-renewal", ticket.ID, claim)

	adapter := &sleepyRuntimeAdapter{
		workerDelay: 250 * time.Millisecond,
		workerResult: runtime.WorkerResult{
			Status:             runtime.WorkerStatusDone,
			RetryClass:         runtime.RetryClassTerminal,
			LeaseID:            claim.LeaseID,
			StartedAt:          testRunTime(),
			FinishedAt:         testRunTime().Add(time.Second),
			ResultArtifactPath: filepath.Join(repoRoot, "worker.json"),
		},
		reviewResult: runtime.ReviewResult{
			Status:             runtime.WorkerStatusDone,
			RetryClass:         runtime.RetryClassTerminal,
			LeaseID:            claim.LeaseID,
			StartedAt:          testRunTime().Add(2 * time.Second),
			FinishedAt:         testRunTime().Add(3 * time.Second),
			ReviewStatus:       runtime.ReviewStatusPassed,
			Summary:            "clean",
			ResultArtifactPath: filepath.Join(repoRoot, "review.json"),
		},
	}

	result, err := RunTicket(context.Background(), RunTicketRequest{
		RepoRoot: repoRoot,
		RunID:    "run-claim-renewal",
		Ticket:   ticket,
		Plan:     plan,
		Claim:    claim,
		Adapter:  adapter,
		Config:   cfg,
	})
	if err != nil {
		t.Fatalf("RunTicket returned error: %v", err)
	}
	if result.Path == "" {
		t.Fatal("expected run result path to be populated")
	}

	var durableClaim state.ClaimArtifact
	if err := state.LoadJSON(durableClaimPath, &durableClaim); err != nil {
		t.Fatalf("load durable claim: %v", err)
	}
	if !durableClaim.ExpiresAt.After(claim.ExpiresAt) {
		t.Fatalf("expected durable claim expiry to be renewed past %s, got %s", claim.ExpiresAt, durableClaim.ExpiresAt)
	}
}

func TestRunTicket_RenewsFromLiveClaimWhenRequestClaimIsStale(t *testing.T) {
	repoRoot := t.TempDir()
	cfg := policy.DefaultConfig()
	ticket := testTicket("ver-live-renewal")
	plan, requestClaim := testPlanAndClaim(t, repoRoot, ticket, cfg, "run-live-renewal", "lease-live-renewal", []string{`true`})

	now := time.Now().UTC()
	requestClaim.LeasedAt = now
	requestClaim.ExpiresAt = now.Add(3 * time.Second)

	liveClaim := requestClaim
	liveClaim.ExpiresAt = now.Add(900 * time.Millisecond)
	durableClaimPath := seedClaimSnapshots(t, repoRoot, "run-live-renewal", ticket.ID, liveClaim)

	adapter := &sleepyRuntimeAdapter{
		reviewDelay: 1300 * time.Millisecond,
		workerResult: runtime.WorkerResult{
			Status:             runtime.WorkerStatusDone,
			RetryClass:         runtime.RetryClassTerminal,
			LeaseID:            requestClaim.LeaseID,
			StartedAt:          testRunTime(),
			FinishedAt:         testRunTime().Add(time.Second),
			ResultArtifactPath: filepath.Join(repoRoot, "worker.json"),
		},
		reviewResult: runtime.ReviewResult{
			Status:             runtime.WorkerStatusDone,
			RetryClass:         runtime.RetryClassTerminal,
			LeaseID:            requestClaim.LeaseID,
			StartedAt:          testRunTime().Add(2 * time.Second),
			FinishedAt:         testRunTime().Add(3 * time.Second),
			ReviewStatus:       runtime.ReviewStatusPassed,
			Summary:            "clean",
			ResultArtifactPath: filepath.Join(repoRoot, "review.json"),
		},
	}

	result, err := RunTicket(context.Background(), RunTicketRequest{
		RepoRoot: repoRoot,
		RunID:    "run-live-renewal",
		Ticket:   ticket,
		Plan:     plan,
		Claim:    requestClaim,
		Adapter:  adapter,
		Config:   cfg,
	})
	if err != nil {
		t.Fatalf("RunTicket returned error: %v", err)
	}
	if result.Snapshot.CurrentPhase != state.TicketPhaseClosed {
		t.Fatalf("expected closed phase, got %q", result.Snapshot.CurrentPhase)
	}

	var durableClaim state.ClaimArtifact
	if err := state.LoadJSON(durableClaimPath, &durableClaim); err != nil {
		t.Fatalf("load durable claim: %v", err)
	}
	if !durableClaim.ExpiresAt.After(requestClaim.ExpiresAt) {
		t.Fatalf("expected live near-expiry claim to be renewed past stale request expiry %s, got %s",
			requestClaim.ExpiresAt.Format(time.RFC3339Nano), durableClaim.ExpiresAt.Format(time.RFC3339Nano))
	}
}

func TestRunTicket_RejectsStaleLeaseID(t *testing.T) {
	repoRoot := t.TempDir()
	cfg := policy.DefaultConfig()
	ticket := testTicket("ver-stale-lease")
	plan, claim := testPlanAndClaim(t, repoRoot, ticket, cfg, "run-stale-lease", "lease-current", []string{`true`})

	started, finished := testRunTimes()
	adapter := runtimefake.New(
		[]runtime.WorkerResult{
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            "lease-stale",
				StartedAt:          started,
				FinishedAt:         finished,
				ResultArtifactPath: filepath.Join(repoRoot, "worker.json"),
			},
		},
		nil,
	)

	_, err := RunTicket(context.Background(), RunTicketRequest{
		RepoRoot: repoRoot,
		RunID:    "run-stale-lease",
		Ticket:   ticket,
		Plan:     plan,
		Claim:    claim,
		Adapter:  adapter,
		Config:   cfg,
	})
	if err == nil {
		t.Fatal("expected stale lease result to fail")
	}
	if !strings.Contains(err.Error(), "lease fence mismatch") {
		t.Fatalf("expected lease fence mismatch, got %v", err)
	}

	if _, statErr := os.Stat(filepath.Join(repoRoot, ".verk", "runs", "run-stale-lease", "tickets", ticket.ID, "verification.json")); statErr == nil || !os.IsNotExist(statErr) {
		t.Fatalf("expected no verification artifact after stale lease failure, got %v", statErr)
	}
}

func TestRunTicket_ScopeViolationBlocksTicket(t *testing.T) {
	repoRoot := t.TempDir()

	// Initialize a real git repo so collectChangedFiles can detect changes.
	mustRunGit(t, repoRoot, "init")
	mustRunGit(t, repoRoot, "config", "user.email", "test@example.com")
	mustRunGit(t, repoRoot, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repoRoot, "tracked.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	mustRunGit(t, repoRoot, "add", "tracked.txt")
	mustRunGit(t, repoRoot, "commit", "-m", "base")

	headOut, err := gitOutput(repoRoot, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	baseCommit := strings.TrimSpace(headOut)

	// Create a file outside the owned scope to simulate a scope violation.
	outsideDir := filepath.Join(repoRoot, "outside")
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outsideDir, "violation.go"), []byte("package outside\n"), 0o644); err != nil {
		t.Fatalf("write violation file: %v", err)
	}
	mustRunGit(t, repoRoot, "add", "outside/violation.go")
	mustRunGit(t, repoRoot, "commit", "-m", "out-of-scope change")

	cfg := policy.DefaultConfig()
	ticket := tkmd.Ticket{
		ID:         "ver-scope-viol",
		Title:      "Ticket ver-scope-viol",
		OwnedPaths: []string{"internal/engine"},
		UnknownFrontmatter: map[string]any{
			"type": "task",
		},
	}
	plan, claim := testPlanAndClaim(t, repoRoot, ticket, cfg, "run-scope-viol", "lease-scope-viol", []string{`true`})

	started, finished := testRunTimes()
	adapter := runtimefake.New(
		[]runtime.WorkerResult{
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            claim.LeaseID,
				StartedAt:          started,
				FinishedAt:         finished,
				ResultArtifactPath: filepath.Join(repoRoot, "worker.json"),
			},
		},
		nil,
	)

	result, err := RunTicket(context.Background(), RunTicketRequest{
		RepoRoot:           repoRoot,
		RunID:              "run-scope-viol",
		BaseCommit:         baseCommit,
		Ticket:             ticket,
		Plan:               plan,
		Claim:              claim,
		Adapter:            adapter,
		Config:             cfg,
		EnforceSingleScope: true,
	})
	if err != nil {
		t.Fatalf("RunTicket returned error: %v", err)
	}
	if result.Snapshot.CurrentPhase != state.TicketPhaseBlocked {
		t.Fatalf("expected blocked phase, got %q", result.Snapshot.CurrentPhase)
	}
	if !strings.Contains(result.Snapshot.BlockReason, "scope violation") {
		t.Fatalf("expected scope violation block reason, got %q", result.Snapshot.BlockReason)
	}
}

func TestRunTicket_ScopeCheckBlocksWhenOwnedPathsEmpty(t *testing.T) {
	repoRoot := t.TempDir()

	// Initialize a real git repo with a file change outside any scope.
	mustRunGit(t, repoRoot, "init")
	mustRunGit(t, repoRoot, "config", "user.email", "test@example.com")
	mustRunGit(t, repoRoot, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repoRoot, "tracked.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	mustRunGit(t, repoRoot, "add", "tracked.txt")
	mustRunGit(t, repoRoot, "commit", "-m", "base")

	headOut, err := gitOutput(repoRoot, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	baseCommit := strings.TrimSpace(headOut)

	// Create a file that would be out of scope if owned_paths were set.
	if err := os.MkdirAll(filepath.Join(repoRoot, "anywhere"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "anywhere", "file.go"), []byte("package anywhere\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	mustRunGit(t, repoRoot, "add", "anywhere/file.go")
	mustRunGit(t, repoRoot, "commit", "-m", "change")

	cfg := policy.DefaultConfig()
	// No OwnedPaths set - G9 requires scope checks to default to deny/fail-closed.
	ticket := testTicket("ver-no-scope")
	plan, claim := testPlanAndClaim(t, repoRoot, ticket, cfg, "run-no-scope", "lease-no-scope", []string{`true`})

	started, finished := testRunTimes()
	adapter := runtimefake.New(
		[]runtime.WorkerResult{
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            claim.LeaseID,
				StartedAt:          started,
				FinishedAt:         finished,
				ResultArtifactPath: filepath.Join(repoRoot, "worker.json"),
			},
		},
		[]runtime.ReviewResult{
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            claim.LeaseID,
				StartedAt:          finished.Add(time.Second),
				FinishedAt:         finished.Add(2 * time.Second),
				ReviewStatus:       runtime.ReviewStatusPassed,
				Summary:            "clean",
				ResultArtifactPath: filepath.Join(repoRoot, "review.json"),
			},
		},
	)

	result, err := RunTicket(context.Background(), RunTicketRequest{
		RepoRoot:           repoRoot,
		RunID:              "run-no-scope",
		BaseCommit:         baseCommit,
		Ticket:             ticket,
		Plan:               plan,
		Claim:              claim,
		Adapter:            adapter,
		Config:             cfg,
		EnforceSingleScope: true,
	})
	// With no owned_paths, G9 requires scope checks to fail closed, so the ticket transitions to blocked.
	if err != nil {
		t.Fatalf("RunTicket returned error: %v", err)
	}
	if result.Snapshot.CurrentPhase != state.TicketPhaseBlocked {
		t.Fatalf("expected blocked phase, got %q", result.Snapshot.CurrentPhase)
	}
	if !strings.Contains(result.Snapshot.BlockReason, "single-ticket scope violation") {
		t.Fatalf("expected scope violation block reason, got %q", result.Snapshot.BlockReason)
	}
	if !strings.Contains(result.Snapshot.BlockReason, ticket.ID) {
		t.Fatalf("expected block reason to contain ticket ID %q, got %q", ticket.ID, result.Snapshot.BlockReason)
	}
}

func TestRunTicket_ScopeMissingThenReopensToProceed(t *testing.T) {
	repoRoot := t.TempDir()
	cfg := policy.DefaultConfig()

	runID := "run-scope-reopen"
	ticketID := "ver-scope-reopen"
	ticket := testTicket(ticketID)

	// First run: no OwnedPaths set, so single-ticket scope check should block.
	firstPlan, firstClaim := testPlanAndClaim(t, repoRoot, ticket, cfg, runID, "lease-scope-reopen-empty", []string{`true`})

	started, finished := testRunTimes()
	firstAdapter := runtimefake.New(
		[]runtime.WorkerResult{
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            firstClaim.LeaseID,
				StartedAt:          started,
				FinishedAt:         finished,
				ResultArtifactPath: filepath.Join(repoRoot, "worker-empty.json"),
			},
		},
		nil,
	)

	blockedResult, err := RunTicket(context.Background(), RunTicketRequest{
		RepoRoot:           repoRoot,
		RunID:              runID,
		Ticket:             ticket,
		Plan:               firstPlan,
		Claim:              firstClaim,
		Adapter:            firstAdapter,
		Config:             cfg,
		EnforceSingleScope: true,
	})
	if err != nil {
		t.Fatalf("first RunTicket returned error: %v", err)
	}
	if blockedResult.Snapshot.CurrentPhase != state.TicketPhaseBlocked {
		t.Fatalf("expected blocked phase on first run, got %q", blockedResult.Snapshot.CurrentPhase)
	}
	if !strings.Contains(blockedResult.Snapshot.BlockReason, "single-ticket scope violation") {
		t.Fatalf("expected scope violation block reason, got %q", blockedResult.Snapshot.BlockReason)
	}

	// Simulate reopen flow artifacts that would normally exist in an epic run.
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
	writeTicketRunFixture(t, repoRoot, runID, blockedResult.Snapshot)
	writePlanFixture(t, repoRoot, runID, firstPlan)
	writeTicketMarkdownFixture(t, repoRoot, tkmd.Ticket{
		ID:                 ticketID,
		Title:              ticket.Title,
		Status:             tkmd.StatusBlocked,
		OwnedPaths:         nil,
		UnknownFrontmatter: map[string]any{"type": "task"},
	})

	// Operator adds scope declarations and reopens the ticket.
	if err := ReopenTicket(context.Background(), ReopenRequest{
		RepoRoot: repoRoot,
		RunID:    runID,
		TicketID: ticketID,
		ToPhase:  state.TicketPhaseImplement,
	}); err != nil {
		t.Fatalf("ReopenTicket returned error: %v", err)
	}

	reopenedTicket, err := tkmd.LoadTicket(ticketMarkdownPath(repoRoot, ticketID))
	if err != nil {
		t.Fatalf("load reopened ticket: %v", err)
	}
	if reopenedTicket.Status != tkmd.StatusOpen {
		t.Fatalf("expected ticket status to become open after reopen, got %q", reopenedTicket.Status)
	}
	reopenedTicket.OwnedPaths = []string{"internal/engine"}
	if err := tkmd.SaveTicket(filepath.Join(repoRoot, ".tickets", ticketID+".md"), reopenedTicket); err != nil {
		t.Fatalf("save reopened ticket: %v", err)
	}

	reopenedPlan, reopenedClaim := testPlanAndClaim(
		t,
		repoRoot,
		reopenedTicket,
		cfg,
		runID,
		"lease-scope-reopen-scope",
		[]string{`true`},
	)

	started, finished = testRunTimes()
	reopenedAdapter := runtimefake.New(
		[]runtime.WorkerResult{
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            reopenedClaim.LeaseID,
				StartedAt:          started,
				FinishedAt:         finished,
				ResultArtifactPath: filepath.Join(repoRoot, "worker-scope.json"),
			},
		},
		[]runtime.ReviewResult{
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            reopenedClaim.LeaseID,
				StartedAt:          finished.Add(time.Second),
				FinishedAt:         finished.Add(2 * time.Second),
				ReviewStatus:       runtime.ReviewStatusPassed,
				Summary:            "clean",
				ResultArtifactPath: filepath.Join(repoRoot, "review-scope.json"),
			},
		},
	)

	reopenedResult, err := RunTicket(context.Background(), RunTicketRequest{
		RepoRoot:           repoRoot,
		RunID:              runID,
		Ticket:             reopenedTicket,
		Plan:               reopenedPlan,
		Claim:              reopenedClaim,
		Adapter:            reopenedAdapter,
		Config:             cfg,
		EnforceSingleScope: true,
	})
	if err != nil {
		t.Fatalf("second RunTicket returned error: %v", err)
	}
	if reopenedResult.Snapshot.CurrentPhase != state.TicketPhaseClosed {
		t.Fatalf("expected ticket to proceed to closed, got %q", reopenedResult.Snapshot.CurrentPhase)
	}
	if reopenedResult.Snapshot.BlockReason != "" {
		t.Fatalf("expected no block reason after scope is declared, got %q", reopenedResult.Snapshot.BlockReason)
	}
}

func TestRunTicket_WorkerBlockReasonRoundTrips(t *testing.T) {
	repoRoot := t.TempDir()
	cfg := policy.DefaultConfig()
	ticket := testTicket("ver-block-reason")
	plan, claim := testPlanAndClaim(t, repoRoot, ticket, cfg, "run-block-reason", "lease-block-reason", []string{`true`})

	started, finished := testRunTimes()
	adapter := runtimefake.New(
		[]runtime.WorkerResult{
			{
				Status:             runtime.WorkerStatusNeedsContext,
				CompletionCode:     "missing credentials",
				BlockReason:        "cannot proceed without AWS_SECRET_ACCESS_KEY",
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            claim.LeaseID,
				StartedAt:          started,
				FinishedAt:         finished,
				ResultArtifactPath: filepath.Join(repoRoot, "worker.json"),
			},
		},
		nil,
	)

	result, err := RunTicket(context.Background(), RunTicketRequest{
		RepoRoot: repoRoot,
		RunID:    "run-block-reason",
		Ticket:   ticket,
		Plan:     plan,
		Claim:    claim,
		Adapter:  adapter,
		Config:   cfg,
	})
	if err != nil {
		t.Fatalf("RunTicket returned error: %v", err)
	}
	if result.Snapshot.CurrentPhase != state.TicketPhaseBlocked {
		t.Fatalf("expected blocked phase, got %q", result.Snapshot.CurrentPhase)
	}

	// The worker's specific block_reason should flow through to the snapshot and implementation artifact.
	expectedReason := "cannot proceed without AWS_SECRET_ACCESS_KEY"
	if result.Snapshot.BlockReason != expectedReason {
		t.Fatalf("expected snapshot block reason %q, got %q", expectedReason, result.Snapshot.BlockReason)
	}
	if result.Snapshot.Implementation == nil {
		t.Fatalf("expected implementation artifact to be present")
	}
	if result.Snapshot.Implementation.BlockReason != expectedReason {
		t.Fatalf("expected implementation block reason %q, got %q", expectedReason, result.Snapshot.Implementation.BlockReason)
	}
}

func TestRunTicket_ConcernsRoundTripToArtifact(t *testing.T) {
	repoRoot := t.TempDir()
	cfg := policy.DefaultConfig()
	ticket := testTicket("ver-concerns")
	plan, claim := testPlanAndClaim(t, repoRoot, ticket, cfg, "run-concerns", "lease-concerns", []string{`true`})

	started, finished := testRunTimes()
	adapter := runtimefake.New(
		[]runtime.WorkerResult{
			{
				Status:             runtime.WorkerStatusDoneWithConcerns,
				CompletionCode:     "ok",
				Concerns:           []string{"minor style issue", "consider adding more tests"},
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            claim.LeaseID,
				StartedAt:          started,
				FinishedAt:         finished,
				ResultArtifactPath: filepath.Join(repoRoot, "worker.json"),
			},
		},
		[]runtime.ReviewResult{
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            claim.LeaseID,
				StartedAt:          finished.Add(time.Second),
				FinishedAt:         finished.Add(2 * time.Second),
				ReviewStatus:       runtime.ReviewStatusPassed,
				Summary:            "clean",
				ResultArtifactPath: filepath.Join(repoRoot, "review.json"),
			},
		},
	)

	result, err := RunTicket(context.Background(), RunTicketRequest{
		RepoRoot: repoRoot,
		RunID:    "run-concerns",
		Ticket:   ticket,
		Plan:     plan,
		Claim:    claim,
		Adapter:  adapter,
		Config:   cfg,
	})
	if err != nil {
		t.Fatalf("RunTicket returned error: %v", err)
	}
	if result.Snapshot.CurrentPhase != state.TicketPhaseClosed {
		t.Fatalf("expected closed phase, got %q", result.Snapshot.CurrentPhase)
	}
	if result.Snapshot.Implementation == nil {
		t.Fatalf("expected implementation artifact to be present")
	}
	concerns := result.Snapshot.Implementation.Concerns
	if len(concerns) != 2 {
		t.Fatalf("expected 2 concerns, got %d: %v", len(concerns), concerns)
	}
	if concerns[0] != "minor style issue" {
		t.Fatalf("expected first concern 'minor style issue', got %q", concerns[0])
	}
	if concerns[1] != "consider adding more tests" {
		t.Fatalf("expected second concern 'consider adding more tests', got %q", concerns[1])
	}
}

func testTicket(id string) tkmd.Ticket {
	return tkmd.Ticket{
		ID:    id,
		Title: "Ticket " + id,
		UnknownFrontmatter: map[string]any{
			"type": "task",
		},
	}
}

func testPlanAndClaim(t *testing.T, repoRoot string, ticket tkmd.Ticket, cfg policy.Config, runID, leaseID string, verificationCommands []string) (state.PlanArtifact, state.ClaimArtifact) {
	t.Helper()

	plan, err := BuildPlanArtifact(ticket, cfg)
	if err != nil {
		t.Fatalf("BuildPlanArtifact: %v", err)
	}
	plan.ValidationCommands = append([]string(nil), verificationCommands...)

	claim, err := tkmd.AcquireClaim(repoRoot, runID, ticket.ID, leaseID, 10*time.Minute, time.Now().UTC())
	if err != nil {
		t.Fatalf("AcquireClaim: %v", err)
	}
	return plan, claim
}

func seedClaimSnapshots(t *testing.T, repoRoot, runID, ticketID string, claim state.ClaimArtifact) string {
	t.Helper()
	if err := state.SaveJSONAtomic(liveClaimPath(repoRoot, ticketID), claim); err != nil {
		t.Fatalf("seed live claim: %v", err)
	}
	durableClaimPath := filepath.Join(repoRoot, ".verk", "runs", runID, "claims", "claim-"+ticketID+".json")
	if err := state.SaveJSONAtomic(durableClaimPath, claim); err != nil {
		t.Fatalf("seed durable claim: %v", err)
	}
	return durableClaimPath
}

type sleepyRuntimeAdapter struct {
	workerDelay  time.Duration
	reviewDelay  time.Duration
	workerResult runtime.WorkerResult
	reviewResult runtime.ReviewResult
}

func (a *sleepyRuntimeAdapter) RunWorker(ctx context.Context, req runtime.WorkerRequest) (runtime.WorkerResult, error) {
	select {
	case <-time.After(a.workerDelay):
	case <-ctx.Done():
		return runtime.WorkerResult{}, ctx.Err()
	}
	return a.workerResult, nil
}

func (a *sleepyRuntimeAdapter) RunReviewer(ctx context.Context, req runtime.ReviewRequest) (runtime.ReviewResult, error) {
	select {
	case <-time.After(a.reviewDelay):
	case <-ctx.Done():
		return runtime.ReviewResult{}, ctx.Err()
	}
	return a.reviewResult, nil
}

func TestRunTicket_ChangedFilesCaptured(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	cfg := policy.DefaultConfig()
	ticket := testTicket("ver-changed-files")
	plan, claim := testPlanAndClaim(t, repoRoot, ticket, cfg, "run-changed-files", "lease-changed-files", []string{`true`})

	// Simulate worker-created files: user files that should appear in ChangedFiles.
	for _, entry := range []struct{ dir, name, content string }{
		{"src", "app.go", "package app\n"},
		{"docs", "readme.md", "# Docs\n"},
	} {
		dir := filepath.Join(repoRoot, entry.dir)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, entry.name), []byte(entry.content), 0o644); err != nil {
			t.Fatalf("write %s/%s: %v", entry.dir, entry.name, err)
		}
	}

	started, finished := testRunTimes()
	adapter := runtimefake.New(
		[]runtime.WorkerResult{{
			Status:             runtime.WorkerStatusDone,
			RetryClass:         runtime.RetryClassTerminal,
			LeaseID:            claim.LeaseID,
			StartedAt:          started,
			FinishedAt:         finished,
			ResultArtifactPath: filepath.Join(repoRoot, "worker.json"),
		}},
		[]runtime.ReviewResult{{
			Status:             runtime.WorkerStatusDone,
			RetryClass:         runtime.RetryClassTerminal,
			LeaseID:            claim.LeaseID,
			StartedAt:          finished.Add(time.Second),
			FinishedAt:         finished.Add(2 * time.Second),
			ReviewStatus:       runtime.ReviewStatusPassed,
			Summary:            "clean",
			ResultArtifactPath: filepath.Join(repoRoot, "review.json"),
		}},
	)

	result, err := RunTicket(context.Background(), RunTicketRequest{
		RepoRoot:   repoRoot,
		RunID:      "run-changed-files",
		BaseCommit: baseCommit,
		Ticket:     ticket,
		Plan:       plan,
		Claim:      claim,
		Adapter:    adapter,
		Config:     cfg,
	})
	if err != nil {
		t.Fatalf("RunTicket error: %v", err)
	}
	if result.Snapshot.Implementation == nil {
		t.Fatal("expected implementation artifact, got nil")
	}

	changed := result.Snapshot.Implementation.ChangedFiles
	if changed == nil {
		t.Fatal("ChangedFiles must not be nil")
	}

	// User files must be present.
	want := map[string]bool{"src/app.go": false, "docs/readme.md": false}
	for _, f := range changed {
		if _, ok := want[f]; ok {
			want[f] = true
		}
		// Engine-owned files must be excluded.
		if strings.HasPrefix(f, ".verk/") || strings.HasPrefix(f, ".tickets/") {
			t.Errorf("engine-owned file %q must not appear in ChangedFiles", f)
		}
	}
	for f, found := range want {
		if !found {
			t.Errorf("expected %q in ChangedFiles, got %v", f, changed)
		}
	}
}

func TestRunTicket_ChangedFilesEmptyWhenNoUserChanges(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	cfg := policy.DefaultConfig()
	ticket := testTicket("ver-no-changes")
	plan, claim := testPlanAndClaim(t, repoRoot, ticket, cfg, "run-no-changes", "lease-no-changes", []string{`true`})

	started, finished := testRunTimes()
	adapter := runtimefake.New(
		[]runtime.WorkerResult{{
			Status:             runtime.WorkerStatusDone,
			RetryClass:         runtime.RetryClassTerminal,
			LeaseID:            claim.LeaseID,
			StartedAt:          started,
			FinishedAt:         finished,
			ResultArtifactPath: filepath.Join(repoRoot, "worker.json"),
		}},
		[]runtime.ReviewResult{{
			Status:             runtime.WorkerStatusDone,
			RetryClass:         runtime.RetryClassTerminal,
			LeaseID:            claim.LeaseID,
			StartedAt:          finished.Add(time.Second),
			FinishedAt:         finished.Add(2 * time.Second),
			ReviewStatus:       runtime.ReviewStatusPassed,
			Summary:            "clean",
			ResultArtifactPath: filepath.Join(repoRoot, "review.json"),
		}},
	)

	result, err := RunTicket(context.Background(), RunTicketRequest{
		RepoRoot:   repoRoot,
		RunID:      "run-no-changes",
		BaseCommit: baseCommit,
		Ticket:     ticket,
		Plan:       plan,
		Claim:      claim,
		Adapter:    adapter,
		Config:     cfg,
	})
	if err != nil {
		t.Fatalf("RunTicket error: %v", err)
	}
	if result.Snapshot.Implementation == nil {
		t.Fatal("expected implementation artifact, got nil")
	}

	changed := result.Snapshot.Implementation.ChangedFiles
	if changed == nil {
		t.Fatal("ChangedFiles must be empty slice, not nil")
	}
	if len(changed) != 0 {
		t.Fatalf("expected no changed files, got %v", changed)
	}
}

func verifyToggleCommand() string {
	return `count_file=.verk/verify-count; count=0; if [ -f "$count_file" ]; then count=$(cat "$count_file"); fi; count=$((count+1)); printf '%s' "$count" > "$count_file"; if [ "$count" -lt 2 ]; then exit 1; fi`
}

func testRunTime() time.Time {
	return time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC)
}

func testRunTimes() (time.Time, time.Time) {
	start := testRunTime()
	return start, start.Add(2 * time.Second)
}

func TestCollectDiff_ReturnsErrorOnInvalidRepo(t *testing.T) {
	_, err := collectDiff("/nonexistent/path/that/does/not/exist", "abc123")
	if err == nil {
		t.Fatal("expected error from collectDiff with invalid repo path")
	}
}

func TestCollectDiff_ReturnsErrorOnInvalidBaseCommit(t *testing.T) {
	repoRoot := t.TempDir()
	mustRunGit(t, repoRoot, "init")
	mustRunGit(t, repoRoot, "config", "user.email", "test@example.com")
	mustRunGit(t, repoRoot, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repoRoot, "file.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	mustRunGit(t, repoRoot, "add", "file.txt")
	mustRunGit(t, repoRoot, "commit", "-m", "initial")

	_, err := collectDiff(repoRoot, "nonexistent-commit-hash")
	if err == nil {
		t.Fatal("expected error from collectDiff with invalid base commit")
	}
}

func TestCollectChangedFiles_ReturnsErrorOnInvalidRepo(t *testing.T) {
	_, err := collectChangedFiles("/nonexistent/path/that/does/not/exist", "abc123")
	if err == nil {
		t.Fatal("expected error from collectChangedFiles with invalid repo path")
	}
}

func TestCollectChangedFiles_ReturnsErrorOnInvalidBaseCommit(t *testing.T) {
	repoRoot := t.TempDir()
	mustRunGit(t, repoRoot, "init")
	mustRunGit(t, repoRoot, "config", "user.email", "test@example.com")
	mustRunGit(t, repoRoot, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repoRoot, "file.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	mustRunGit(t, repoRoot, "add", "file.txt")
	mustRunGit(t, repoRoot, "commit", "-m", "initial")

	_, err := collectChangedFiles(repoRoot, "nonexistent-commit-hash")
	if err == nil {
		t.Fatal("expected error from collectChangedFiles with invalid base commit")
	}
}

func TestCurrentClaimRemainingTTL_FallsBackToRequestClaim(t *testing.T) {
	now := time.Now().UTC()
	st := &ticketRunState{
		req: RunTicketRequest{
			Claim: state.ClaimArtifact{
				LeasedAt:  now.Add(-10 * time.Minute),
				ExpiresAt: now.Add(20 * time.Minute),
			},
		},
	}

	ttl := st.claimTTL()
	if ttl != 30*time.Minute {
		t.Fatalf("expected claimTTL to be 30m (original full TTL), got %v", ttl)
	}

	remaining, known := st.currentClaimRemainingTTL()
	if !known {
		t.Fatal("expected request claim expiry to provide known remaining TTL")
	}
	// currentClaimRemainingTTL should be approximately 20m, not 30m.
	if remaining >= 30*time.Minute {
		t.Fatalf("expected current remaining TTL < 30m (should be ~20m), got %v", remaining)
	}
	if remaining < 15*time.Minute {
		t.Fatalf("expected current remaining TTL > 15m (should be ~20m), got %v", remaining)
	}
}

func TestCurrentClaimRemainingTTL_ZeroExpiresAt(t *testing.T) {
	st := &ticketRunState{
		req: RunTicketRequest{
			Claim: state.ClaimArtifact{},
		},
	}
	remaining, known := st.currentClaimRemainingTTL()
	if known {
		t.Fatalf("expected unknown remaining TTL for zero ExpiresAt, got %v", remaining)
	}
}

func TestNormalizeRunTicketConfig_SetsMaxRepairCyclesDefault(t *testing.T) {
	cfg := policy.Config{}
	// Zero-value config should have MaxRepairCycles == 0.
	if cfg.Policy.MaxRepairCycles != 0 {
		t.Fatalf("expected zero-value MaxRepairCycles, got %d", cfg.Policy.MaxRepairCycles)
	}

	normalized := normalizeRunTicketConfig(cfg)
	defaults := policy.DefaultConfig()
	if normalized.Policy.MaxRepairCycles != defaults.Policy.MaxRepairCycles {
		t.Fatalf("expected normalized MaxRepairCycles to be %d (default), got %d",
			defaults.Policy.MaxRepairCycles, normalized.Policy.MaxRepairCycles)
	}

	// Non-zero value should be preserved.
	cfg.Policy.MaxRepairCycles = 5
	normalized = normalizeRunTicketConfig(cfg)
	if normalized.Policy.MaxRepairCycles != 5 {
		t.Fatalf("expected MaxRepairCycles to stay 5, got %d", normalized.Policy.MaxRepairCycles)
	}
}

// TestRunTicket_NeedsContextBlocksWorkflow is a regression test for ver-dmnr:
// WorkerStatusNeedsContext must transition to TicketPhaseBlocked (not success).
// This guards against the engine advancing workflows that should pause for
// operator input.
func TestRunTicket_NeedsContextBlocksWorkflow(t *testing.T) {
	repoRoot := t.TempDir()
	cfg := policy.DefaultConfig()
	ticket := testTicket("ver-needs-ctx")
	plan, claim := testPlanAndClaim(t, repoRoot, ticket, cfg, "run-needs-ctx", "lease-needs-ctx", []string{`true`})

	started, finished := testRunTimes()
	adapter := runtimefake.New(
		[]runtime.WorkerResult{
			{
				Status:             runtime.WorkerStatusNeedsContext,
				CompletionCode:     "needs_more_context",
				BlockReason:        "acceptance criteria unclear",
				RetryClass:         runtime.RetryClassBlockedByOperatorInput,
				LeaseID:            claim.LeaseID,
				StartedAt:          started,
				FinishedAt:         finished,
				ResultArtifactPath: filepath.Join(repoRoot, "worker.json"),
			},
		},
		nil, // no review results — should never reach review
	)

	result, err := RunTicket(context.Background(), RunTicketRequest{
		RepoRoot: repoRoot,
		RunID:    "run-needs-ctx",
		Ticket:   ticket,
		Plan:     plan,
		Claim:    claim,
		Adapter:  adapter,
		Config:   cfg,
	})
	if err != nil {
		t.Fatalf("RunTicket returned error: %v", err)
	}

	// Must block, not advance to verify or closed.
	if result.Snapshot.CurrentPhase != state.TicketPhaseBlocked {
		t.Fatalf("expected blocked phase for needs_context, got %q", result.Snapshot.CurrentPhase)
	}
	if !strings.Contains(result.Snapshot.BlockReason, "acceptance criteria unclear") {
		t.Fatalf("expected block reason to contain worker's reason, got %q", result.Snapshot.BlockReason)
	}

	// Cross-ticket invariant (ver-dmnr × ver-m8d1): the claim must be released
	// when the workflow blocks via needs_context so that retries are not
	// permanently locked out.  needs_more_context normalization routes through
	// WorkerStatusNeedsContext → TicketPhaseBlocked, and the blocked terminal
	// path must release the live claim just like startup failures do.
	livePath := filepath.Join(repoRoot, ".tickets", ".claims", ticket.ID+".json")
	if _, err := os.Stat(livePath); err == nil {
		t.Fatalf("expected live claim to be released after needs_context block, but file still exists: %s", livePath)
	}
	durablePath := filepath.Join(repoRoot, ".verk", "runs", "run-needs-ctx", "claims", "claim-"+ticket.ID+".json")
	var durable state.ClaimArtifact
	if err := state.LoadJSON(durablePath, &durable); err != nil {
		t.Fatalf("load durable claim after needs_context block: %v", err)
	}
	if durable.State != "released" {
		t.Fatalf("expected durable claim state %q after needs_context block, got %q", "released", durable.State)
	}
}

// TestRunTicket_ReleasesClaimOnStartupFailure verifies that every startup/setup
// failure after claim acquisition releases the live claim so that retries are
// not blocked by a leaked claim (ver-m8d1).
func TestRunTicket_ReleasesClaimOnStartupFailure(t *testing.T) {
	assertClaimReleased := func(t *testing.T, repoRoot, runID, ticketID string) {
		t.Helper()
		// Live claim file should have been removed by release.
		livePath := filepath.Join(repoRoot, ".tickets", ".claims", ticketID+".json")
		if _, err := os.Stat(livePath); err == nil {
			t.Fatalf("expected live claim file to be removed, but it still exists: %s", livePath)
		}
		// Durable claim should be in released state.
		durablePath := filepath.Join(repoRoot, ".verk", "runs", runID, "claims", "claim-"+ticketID+".json")
		var durable state.ClaimArtifact
		if err := state.LoadJSON(durablePath, &durable); err != nil {
			t.Fatalf("load durable claim: %v", err)
		}
		if durable.State != "released" {
			t.Fatalf("expected durable claim state 'released', got %q", durable.State)
		}
	}

	t.Run("invalid_start_phase", func(t *testing.T) {
		repoRoot := t.TempDir()
		cfg := policy.DefaultConfig()
		ticket := testTicket("ver-phase-fail")
		plan, claim := testPlanAndClaim(t, repoRoot, ticket, cfg, "run-phase-fail", "lease-phase-fail", nil)
		plan.Phase = state.TicketPhaseReview // not a valid starting phase

		_, err := RunTicket(context.Background(), RunTicketRequest{
			RepoRoot: repoRoot,
			RunID:    "run-phase-fail",
			Ticket:   ticket,
			Plan:     plan,
			Claim:    claim,
			Adapter:  runtimefake.New(nil, nil),
			Config:   cfg,
		})
		if err == nil {
			t.Fatal("expected error for invalid start phase, got nil")
		}
		assertClaimReleased(t, repoRoot, "run-phase-fail", ticket.ID)
	})

	t.Run("artifact_write_failure", func(t *testing.T) {
		repoRoot := t.TempDir()
		cfg := policy.DefaultConfig()
		ticket := testTicket("ver-write-fail")
		plan, claim := testPlanAndClaim(t, repoRoot, ticket, cfg, "run-write-fail", "lease-write-fail", nil)

		// Make the ticket run directory unwritable so SaveJSONAtomic fails.
		runDir := filepath.Join(repoRoot, ".verk", "runs", "run-write-fail", "tickets", ticket.ID)
		if err := os.MkdirAll(runDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		// Create a non-directory file at plan.json's path to force a write error.
		planPath := filepath.Join(runDir, "plan.json")
		if err := os.WriteFile(planPath, []byte("{}"), 0o444); err != nil {
			t.Fatalf("write blocking file: %v", err)
		}
		if err := os.Chmod(runDir, 0o555); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(runDir, 0o755) })

		_, err := RunTicket(context.Background(), RunTicketRequest{
			RepoRoot: repoRoot,
			RunID:    "run-write-fail",
			Ticket:   ticket,
			Plan:     plan,
			Claim:    claim,
			Adapter:  runtimefake.New(nil, nil),
			Config:   cfg,
		})
		if err == nil {
			t.Fatal("expected error for artifact write failure, got nil")
		}
		assertClaimReleased(t, repoRoot, "run-write-fail", ticket.ID)
	})

	t.Run("early_engine_error_cancelled_context", func(t *testing.T) {
		repoRoot := t.TempDir()
		cfg := policy.DefaultConfig()
		ticket := testTicket("ver-ctx-cancel")
		plan, claim := testPlanAndClaim(t, repoRoot, ticket, cfg, "run-ctx-cancel", "lease-ctx-cancel", nil)

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // cancel immediately

		_, err := RunTicket(ctx, RunTicketRequest{
			RepoRoot: repoRoot,
			RunID:    "run-ctx-cancel",
			Ticket:   ticket,
			Plan:     plan,
			Claim:    claim,
			Adapter:  runtimefake.New(nil, nil),
			Config:   cfg,
		})
		if err == nil {
			t.Fatal("expected error for cancelled context, got nil")
		}
		assertClaimReleased(t, repoRoot, "run-ctx-cancel", ticket.ID)
	})

	t.Run("retry_not_blocked_after_transient_failure", func(t *testing.T) {
		repoRoot := t.TempDir()
		cfg := policy.DefaultConfig()
		ticket := testTicket("ver-retry")
		plan, claim := testPlanAndClaim(t, repoRoot, ticket, cfg, "run-retry", "lease-retry", nil)
		plan.Phase = state.TicketPhaseReview // invalid phase causes failure

		_, err := RunTicket(context.Background(), RunTicketRequest{
			RepoRoot: repoRoot,
			RunID:    "run-retry",
			Ticket:   ticket,
			Plan:     plan,
			Claim:    claim,
			Adapter:  runtimefake.New(nil, nil),
			Config:   cfg,
		})
		if err == nil {
			t.Fatal("expected error for invalid start phase, got nil")
		}
		assertClaimReleased(t, repoRoot, "run-retry", ticket.ID)

		// Verify we can re-acquire the claim after the transient failure.
		_, err = tkmd.AcquireClaim(repoRoot, "run-retry-2", ticket.ID, "lease-retry-2", 10*time.Minute, time.Now().UTC())
		if err != nil {
			t.Fatalf("expected claim re-acquisition after transient failure, got error: %v", err)
		}
	})
}

// TestRunTicket_RenewsResumedClaimBeforeExpiry verifies that a resumed claim
// whose LeasedAt was long ago but ExpiresAt is imminent gets renewed before it
// expires (ver-exae). The renewal cadence must use current remaining TTL, not
// claimTTL(), so a claim with a 30-minute total TTL acquired 29m55s ago
// schedules its first renewal within seconds instead of waiting 10 minutes.
func TestRunTicket_RenewsResumedClaimBeforeExpiry(t *testing.T) {
	repoRoot := t.TempDir()
	cfg := policy.DefaultConfig()
	ticket := testTicket("ver-resumed-renew")
	plan, claim := testPlanAndClaim(t, repoRoot, ticket, cfg, "run-resumed-renew", "lease-resumed-renew", []string{`true`})

	// Simulate a resumed claim: original TTL 30 minutes, leased almost 30
	// minutes ago, leaving only ~500ms until expiry.
	originalTTL := 30 * time.Minute
	claim.LeasedAt = time.Now().UTC().Add(-(originalTTL - 500*time.Millisecond))
	claim.ExpiresAt = time.Now().UTC().Add(500 * time.Millisecond)

	// Update the live and durable claim snapshots to reflect the simulated
	// resumed state so RenewClaim checks and updates the same near-expiry lease.
	liveClaimPath := filepath.Join(repoRoot, ".tickets", ".claims", ticket.ID+".json")
	if err := state.SaveJSONAtomic(liveClaimPath, claim); err != nil {
		t.Fatalf("seed live claim: %v", err)
	}
	durableClaimPath := filepath.Join(repoRoot, ".verk", "runs", "run-resumed-renew", "claims", "claim-"+ticket.ID+".json")
	if err := state.SaveJSONAtomic(durableClaimPath, claim); err != nil {
		t.Fatalf("seed durable claim: %v", err)
	}

	// Worker completes in 250ms — within the 500ms expiry window. With the old
	// cadence (claimTTL/3 ~= 10min) renewal would never fire. With the fix
	// (current remaining TTL/3 ~= 167ms) renewal fires well before expiry.
	adapter := &sleepyRuntimeAdapter{
		workerDelay: 250 * time.Millisecond,
		workerResult: runtime.WorkerResult{
			Status:             runtime.WorkerStatusDone,
			RetryClass:         runtime.RetryClassTerminal,
			LeaseID:            claim.LeaseID,
			StartedAt:          testRunTime(),
			FinishedAt:         testRunTime().Add(time.Second),
			ResultArtifactPath: filepath.Join(repoRoot, "worker.json"),
		},
		reviewResult: runtime.ReviewResult{
			Status:             runtime.WorkerStatusDone,
			RetryClass:         runtime.RetryClassTerminal,
			LeaseID:            claim.LeaseID,
			StartedAt:          testRunTime().Add(2 * time.Second),
			FinishedAt:         testRunTime().Add(3 * time.Second),
			ReviewStatus:       runtime.ReviewStatusPassed,
			Summary:            "clean",
			ResultArtifactPath: filepath.Join(repoRoot, "review.json"),
		},
	}

	result, err := RunTicket(context.Background(), RunTicketRequest{
		RepoRoot: repoRoot,
		RunID:    "run-resumed-renew",
		Ticket:   ticket,
		Plan:     plan,
		Claim:    claim,
		Adapter:  adapter,
		Config:   cfg,
	})
	if err != nil {
		t.Fatalf("RunTicket returned error: %v", err)
	}
	if result.Path == "" {
		t.Fatal("expected run result path to be populated")
	}

	// The durable claim must show a renewed ExpiresAt beyond the near-expiry
	// window we set — proving renewal fired before the claim expired.
	var durableClaim state.ClaimArtifact
	if err := state.LoadJSON(durableClaimPath, &durableClaim); err != nil {
		t.Fatalf("load durable claim: %v", err)
	}
	if !durableClaim.ExpiresAt.After(claim.ExpiresAt) {
		t.Fatalf("expected durable claim to be renewed past %s, got %s — resumed near-expiry claim must renew before expiry",
			claim.ExpiresAt.Format(time.RFC3339Nano), durableClaim.ExpiresAt.Format(time.RFC3339Nano))
	}
}

func TestTicketRunState_snapshotPreservesCreatedAt(t *testing.T) {
	st := &ticketRunState{
		req: RunTicketRequest{
			RunID:  "run-snapshot-test",
			Ticket: tkmd.Ticket{ID: "ver-snapshot-test"},
		},
	}

	snap1 := st.snapshot()
	time.Sleep(2 * time.Millisecond)
	snap2 := st.snapshot()

	if !snap1.CreatedAt.Equal(snap2.CreatedAt) {
		t.Errorf("CreatedAt changed between snapshots: %v -> %v", snap1.CreatedAt, snap2.CreatedAt)
	}
	if !snap2.UpdatedAt.After(snap1.UpdatedAt) {
		t.Errorf("expected UpdatedAt to advance between snapshots: first=%v second=%v", snap1.UpdatedAt, snap2.UpdatedAt)
	}
}

func TestTicketRunState_snapshotOutcome(t *testing.T) {
	cases := []struct {
		name  string
		phase state.TicketPhase
		want  state.TicketOutcome
	}{
		{name: "active", phase: state.TicketPhaseVerify, want: ""},
		{name: "closed", phase: state.TicketPhaseClosed, want: state.TicketOutcomeClosed},
		{name: "legacy_blocked", phase: state.TicketPhaseBlocked, want: state.TicketOutcomeBlocked},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := &ticketRunState{
				req: RunTicketRequest{
					RunID:  "run-snapshot-outcome",
					Ticket: tkmd.Ticket{ID: "ver-snapshot-outcome"},
				},
				currentPhase: tc.phase,
			}

			snap := st.snapshot()
			if snap.Outcome != tc.want {
				t.Fatalf("expected outcome %q for phase %q, got %q", tc.want, tc.phase, snap.Outcome)
			}
		})
	}
}

func TestTicketRunState_snapshotPreservesRestoredCreatedAt(t *testing.T) {
	repoRoot := t.TempDir()
	runID := "run-snapshot-restore"
	ticketID := "ver-snapshot-restore"
	fixedCreatedAt := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	fixture := TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{
			SchemaVersion: artifactSchemaVersion,
			RunID:         runID,
			CreatedAt:     fixedCreatedAt,
			UpdatedAt:     fixedCreatedAt,
		},
		TicketID:     ticketID,
		CurrentPhase: state.TicketPhaseImplement,
	}
	if err := state.SaveJSONAtomic(ticketSnapshotPath(repoRoot, runID, ticketID), fixture); err != nil {
		t.Fatalf("seed ticket snapshot: %v", err)
	}

	var loaded TicketRunSnapshot
	if err := loadTicketSnapshot(repoRoot, runID, ticketID, &loaded); err != nil {
		t.Fatalf("load persisted ticket snapshot: %v", err)
	}

	st := &ticketRunState{
		req: RunTicketRequest{
			RunID:  runID,
			Ticket: tkmd.Ticket{ID: ticketID},
		},
	}
	st.createdAt = loaded.CreatedAt

	snap := st.snapshot()

	if !snap.CreatedAt.Equal(fixedCreatedAt) {
		t.Errorf("expected CreatedAt %v, got %v", fixedCreatedAt, snap.CreatedAt)
	}
}

func TestRenderReviewInstructions_RequestsOwnerRiskAndValidationEvidence(t *testing.T) {
	plan := state.PlanArtifact{
		TicketID:                 "VER-42",
		Title:                    "Sample ticket",
		Description:              "Some description.",
		AcceptanceCriteria:       []string{"does the right thing"},
		EffectiveReviewThreshold: "P2",
	}

	out := renderReviewInstructions(plan, 1)

	for _, phrase := range []string{
		"brutally honest external review",
		"owning ticket",
		"severity",
		"`VER-42`",
		"concrete risk",
		"missing validation or test evidence",
		"auto-repaired",
	} {
		if !strings.Contains(out, phrase) {
			t.Fatalf("expected review instructions to contain %q:\n%s", phrase, out)
		}
	}
}

// TestRenderRepairInstructions_IncludesFindingAndContext verifies the repair
// worker prompt carries the specific review finding plus enough surrounding
// context (acceptance criteria, changed files, prior verification summary) for
// the worker to address the finding without rerunning the whole ticket blindly
// (ver-amsh AC3).
func TestRenderRepairInstructions_IncludesFindingAndContext(t *testing.T) {
	st := &ticketRunState{
		req: RunTicketRequest{
			Plan: state.PlanArtifact{
				TicketID:                 "ver-repair-ctx",
				Title:                    "repair context",
				Description:              "Original ticket description about docs.",
				AcceptanceCriteria:       []string{"docs read correctly"},
				TestCases:                []string{"docs do not contradict each other"},
				OwnedPaths:               []string{"docs"},
				EffectiveReviewThreshold: state.SeverityP2,
			},
		},
		repairCycles: []state.RepairCycleArtifact{{
			TicketID:          "ver-repair-ctx",
			Cycle:             1,
			TriggerFindingIDs: []string{"finding-42"},
			Status:            "repair_pending",
			Scope:             state.ValidationScopeTicket,
		}},
		review: &state.ReviewFindingsArtifact{
			TicketID: "ver-repair-ctx",
			Findings: []state.ReviewFinding{{
				ID:          "finding-42",
				Severity:    state.SeverityP2,
				Title:       "docs contradiction",
				Body:        "README claims X but CONTRIBUTING claims Y.",
				File:        "docs/README.md",
				Line:        12,
				Disposition: "open",
			}},
			EffectiveReviewThreshold: state.SeverityP2,
		},
		implementation: &state.ImplementationArtifact{
			TicketID:     "ver-repair-ctx",
			ChangedFiles: []string{"docs/README.md", "docs/CONTRIBUTING.md"},
		},
		verification: &state.VerificationArtifact{
			TicketID: "ver-repair-ctx",
			Attempt:  2,
			Passed:   true,
			Results: []state.VerificationResult{
				{Command: "markdownlint docs", Passed: true},
			},
		},
	}

	out := renderRepairInstructions(st)

	for _, phrase := range []string{
		"**Ticket ID:** ver-repair-ctx",
		"**Repair Cycle:** 1",
		"finding-42",
		"docs contradiction",
		"docs/README.md:12",
		"Original Ticket Description",
		"Original ticket description about docs.",
		"Acceptance Criteria",
		"docs read correctly",
		"Test Cases",
		"docs do not contradict each other",
		"Changed Files So Far",
		"docs/README.md",
		"docs/CONTRIBUTING.md",
		"Prior Verification",
		"Do not regress",
	} {
		if !strings.Contains(out, phrase) {
			t.Fatalf("expected repair instructions to contain %q:\n%s", phrase, out)
		}
	}
}

// TestRunTicket_ReviewFindingRepairedClosesTicket exercises ver-amsh AC1+AC2:
// a medium-severity review finding must trigger a repair cycle, and once the
// repair worker addresses it and the follow-up review passes, the ticket
// closes with a repair artifact that references the triggering finding id.
func TestRunTicket_ReviewFindingRepairedClosesTicket(t *testing.T) {
	repoRoot := t.TempDir()
	cfg := policy.DefaultConfig()
	ticket := testTicket("ver-review-repair")
	plan, claim := testPlanAndClaim(t, repoRoot, ticket, cfg, "run-review-repair", "lease-review-repair", []string{`true`})

	started, finished := testRunTimes()
	blockingFinding := runtime.ReviewFinding{
		ID:          "finding-77",
		Severity:    runtime.SeverityP2,
		Title:       "docs contradict each other",
		Body:        "README and CONTRIBUTING disagree about bootstrap command.",
		File:        "docs/README.md",
		Line:        22,
		Disposition: runtime.ReviewDispositionOpen,
	}
	adapter := runtimefake.New(
		[]runtime.WorkerResult{
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            claim.LeaseID,
				StartedAt:          started,
				FinishedAt:         finished,
				ResultArtifactPath: filepath.Join(repoRoot, "worker-1.json"),
			},
			// Repair worker run after the blocking finding lands.
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            claim.LeaseID,
				StartedAt:          finished.Add(5 * time.Second),
				FinishedAt:         finished.Add(6 * time.Second),
				ResultArtifactPath: filepath.Join(repoRoot, "worker-repair.json"),
			},
		},
		[]runtime.ReviewResult{
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            claim.LeaseID,
				StartedAt:          finished.Add(2 * time.Second),
				FinishedAt:         finished.Add(3 * time.Second),
				ReviewStatus:       runtime.ReviewStatusFindings,
				Summary:            "needs docs repair",
				Findings:           []runtime.ReviewFinding{blockingFinding},
				ResultArtifactPath: filepath.Join(repoRoot, "review-1.json"),
			},
			// After repair, review passes cleanly.
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            claim.LeaseID,
				StartedAt:          finished.Add(7 * time.Second),
				FinishedAt:         finished.Add(8 * time.Second),
				ReviewStatus:       runtime.ReviewStatusPassed,
				Summary:            "clean after repair",
				ResultArtifactPath: filepath.Join(repoRoot, "review-2.json"),
			},
		},
	)

	result, err := RunTicket(context.Background(), RunTicketRequest{
		RepoRoot: repoRoot,
		RunID:    "run-review-repair",
		Ticket:   ticket,
		Plan:     plan,
		Claim:    claim,
		Adapter:  adapter,
		Config:   cfg,
	})
	if err != nil {
		t.Fatalf("RunTicket returned error: %v", err)
	}
	if result.Snapshot.CurrentPhase != state.TicketPhaseClosed {
		t.Fatalf("expected closed phase after successful repair, got %q (block=%q)",
			result.Snapshot.CurrentPhase, result.Snapshot.BlockReason)
	}
	if len(result.Snapshot.RepairCycles) != 1 {
		t.Fatalf("expected 1 repair cycle, got %d", len(result.Snapshot.RepairCycles))
	}
	cycle := result.Snapshot.RepairCycles[0]
	if len(cycle.TriggerFindingIDs) != 1 || cycle.TriggerFindingIDs[0] != "finding-77" {
		t.Fatalf("expected repair cycle to reference finding-77, got %#v", cycle.TriggerFindingIDs)
	}
	if cycle.Status != "completed" {
		t.Fatalf("expected completed repair cycle after passing review, got %q", cycle.Status)
	}
	if result.Snapshot.ReviewAttempts != 2 {
		t.Fatalf("expected 2 review attempts (initial + post-repair), got %d", result.Snapshot.ReviewAttempts)
	}
	if len(adapter.WorkerRequests()) != 2 {
		t.Fatalf("expected 2 worker requests (implement + repair), got %d", len(adapter.WorkerRequests()))
	}
	// The repair worker's prompt must include the triggering finding id so
	// the worker can act on it without rerunning the whole ticket.
	repairPrompt := adapter.WorkerRequests()[1].Instructions
	if !strings.Contains(repairPrompt, "finding-77") {
		t.Fatalf("expected repair worker prompt to reference finding id, got:\n%s", repairPrompt)
	}
	if !strings.Contains(repairPrompt, "docs contradict each other") {
		t.Fatalf("expected repair worker prompt to include the finding title, got:\n%s", repairPrompt)
	}
}

// TestRunTicket_ExhaustedReviewRepairBlocksWithActionableReason exercises
// ver-amsh AC1+AC5: when a finding survives repair cycles past the policy
// budget, the ticket must transition to blocked with a reason that cites the
// canonical non-convergent prefix, the unresolved finding id, and a suggested
// next action (operator input) so the blocker is actionable.
func TestRunTicket_ExhaustedReviewRepairBlocksWithActionableReason(t *testing.T) {
	repoRoot := t.TempDir()
	cfg := policy.DefaultConfig()
	cfg.Policy.MaxRepairCycles = 1
	ticket := testTicket("ver-review-exhaust")
	plan, claim := testPlanAndClaim(t, repoRoot, ticket, cfg, "run-review-exhaust", "lease-review-exhaust", []string{`true`})

	started, finished := testRunTimes()
	persistent := runtime.ReviewFinding{
		ID:          "finding-stuck",
		Severity:    runtime.SeverityP1,
		Title:       "high severity cannot be repaired",
		Body:        "requires operator decision about schema migration.",
		File:        "internal/engine/example.go",
		Line:        10,
		Disposition: runtime.ReviewDispositionOpen,
	}
	adapter := runtimefake.New(
		[]runtime.WorkerResult{
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            claim.LeaseID,
				StartedAt:          started,
				FinishedAt:         finished,
				ResultArtifactPath: filepath.Join(repoRoot, "worker-1.json"),
			},
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            claim.LeaseID,
				StartedAt:          finished.Add(3 * time.Second),
				FinishedAt:         finished.Add(4 * time.Second),
				ResultArtifactPath: filepath.Join(repoRoot, "worker-2.json"),
			},
		},
		[]runtime.ReviewResult{
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            claim.LeaseID,
				StartedAt:          finished.Add(5 * time.Second),
				FinishedAt:         finished.Add(6 * time.Second),
				ReviewStatus:       runtime.ReviewStatusFindings,
				Summary:            "needs repair",
				Findings:           []runtime.ReviewFinding{persistent},
				ResultArtifactPath: filepath.Join(repoRoot, "review-1.json"),
			},
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            claim.LeaseID,
				StartedAt:          finished.Add(7 * time.Second),
				FinishedAt:         finished.Add(8 * time.Second),
				ReviewStatus:       runtime.ReviewStatusFindings,
				Summary:            "still blocked",
				Findings:           []runtime.ReviewFinding{persistent},
				ResultArtifactPath: filepath.Join(repoRoot, "review-2.json"),
			},
		},
	)

	result, err := RunTicket(context.Background(), RunTicketRequest{
		RepoRoot: repoRoot,
		RunID:    "run-review-exhaust",
		Ticket:   ticket,
		Plan:     plan,
		Claim:    claim,
		Adapter:  adapter,
		Config:   cfg,
	})
	if err != nil {
		t.Fatalf("RunTicket returned error: %v", err)
	}
	if result.Snapshot.CurrentPhase != state.TicketPhaseBlocked {
		t.Fatalf("expected blocked phase after repair budget exhausted, got %q", result.Snapshot.CurrentPhase)
	}
	for _, phrase := range []string{
		string(state.EscalationNonConvergentReview),
		"repair limit reached",
		"finding-stuck",
		"operator input required",
	} {
		if !strings.Contains(result.Snapshot.BlockReason, phrase) {
			t.Fatalf("expected block reason to contain %q, got %q", phrase, result.Snapshot.BlockReason)
		}
	}
	if len(result.Snapshot.RepairCycles) != 2 {
		t.Fatalf("expected 2 repair cycles, got %d", len(result.Snapshot.RepairCycles))
	}
	last := result.Snapshot.RepairCycles[len(result.Snapshot.RepairCycles)-1]
	if last.Status != "blocked" {
		t.Fatalf("expected last repair cycle to be blocked, got %q", last.Status)
	}
	if last.PolicyLimitReached == nil {
		t.Fatalf("expected PolicyLimitReached to be set on exhausted repair cycle")
	}
	if last.PolicyLimitReached.Name != "max_repair_cycles" {
		t.Fatalf("expected max_repair_cycles limit name, got %q", last.PolicyLimitReached.Name)
	}
	if len(last.TriggerFindingIDs) != 1 || last.TriggerFindingIDs[0] != "finding-stuck" {
		t.Fatalf("expected last cycle to reference finding-stuck, got %#v", last.TriggerFindingIDs)
	}
}

// TestRunTicket_LowSeverityFindingDoesNotBlock exercises ver-amsh AC1+AC4:
// a reviewer finding whose severity is below the configured threshold must
// not trigger a repair cycle and must not prevent closure. The finding still
// round-trips through the review artifact so it stays visible to operators.
func TestRunTicket_LowSeverityFindingDoesNotBlock(t *testing.T) {
	repoRoot := t.TempDir()
	cfg := policy.DefaultConfig() // default threshold is P2
	ticket := testTicket("ver-low-sev")
	plan, claim := testPlanAndClaim(t, repoRoot, ticket, cfg, "run-low-sev", "lease-low-sev", []string{`true`})

	started, finished := testRunTimes()
	lowFinding := runtime.ReviewFinding{
		ID:          "finding-nit",
		Severity:    runtime.SeverityP3,
		Title:       "minor style nit",
		Body:        "consider renaming `foo` to something more descriptive.",
		File:        "internal/example.go",
		Line:        5,
		Disposition: runtime.ReviewDispositionOpen,
	}
	adapter := runtimefake.New(
		[]runtime.WorkerResult{
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            claim.LeaseID,
				StartedAt:          started,
				FinishedAt:         finished,
				ResultArtifactPath: filepath.Join(repoRoot, "worker.json"),
			},
		},
		[]runtime.ReviewResult{
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            claim.LeaseID,
				StartedAt:          finished.Add(time.Second),
				FinishedAt:         finished.Add(2 * time.Second),
				ReviewStatus:       runtime.ReviewStatusPassed, // below-threshold → derived status is passed
				Summary:            "only style nits remain",
				Findings:           []runtime.ReviewFinding{lowFinding},
				ResultArtifactPath: filepath.Join(repoRoot, "review.json"),
			},
		},
	)

	result, err := RunTicket(context.Background(), RunTicketRequest{
		RepoRoot: repoRoot,
		RunID:    "run-low-sev",
		Ticket:   ticket,
		Plan:     plan,
		Claim:    claim,
		Adapter:  adapter,
		Config:   cfg,
	})
	if err != nil {
		t.Fatalf("RunTicket returned error: %v", err)
	}
	if result.Snapshot.CurrentPhase != state.TicketPhaseClosed {
		t.Fatalf("expected closed phase with below-threshold finding, got %q (block=%q)",
			result.Snapshot.CurrentPhase, result.Snapshot.BlockReason)
	}
	if len(result.Snapshot.RepairCycles) != 0 {
		t.Fatalf("expected no repair cycles for below-threshold finding, got %d", len(result.Snapshot.RepairCycles))
	}
	if result.Snapshot.Review == nil {
		t.Fatalf("expected review artifact on snapshot")
	}
	if len(result.Snapshot.Review.Findings) != 1 || result.Snapshot.Review.Findings[0].ID != "finding-nit" {
		t.Fatalf("expected finding-nit recorded on review artifact, got %#v", result.Snapshot.Review.Findings)
	}
	if !result.Snapshot.Review.Passed {
		t.Fatalf("expected review.Passed=true for below-threshold finding, got false")
	}
	if len(result.Snapshot.Review.BlockingFindings) != 0 {
		t.Fatalf("expected no blocking findings, got %#v", result.Snapshot.Review.BlockingFindings)
	}
}

// TestBuildReviewRepairBlockReason_IncludesFindingIDsAndNextAction guards the
// canonical shape of the review-repair exhaustion block reason. Operators rely
// on this string to know which findings stalled the ticket and what to do next
// (ver-amsh AC2+AC5).
func TestBuildReviewRepairBlockReason_IncludesFindingIDsAndNextAction(t *testing.T) {
	reason := buildReviewRepairBlockReason(3, []string{"finding-1", "finding-2"})
	for _, phrase := range []string{
		string(state.EscalationNonConvergentReview),
		"repair limit reached after 3 cycle(s)",
		"finding-1",
		"finding-2",
		"operator input required",
	} {
		if !strings.Contains(reason, phrase) {
			t.Fatalf("expected reason to contain %q, got %q", phrase, reason)
		}
	}
}

// TestBuildReviewRepairBlockReason_EmptyFindingIDs verifies the helper still
// emits an actionable suggestion when the engine could not attribute the
// exhaustion to specific finding ids (AC5).
func TestBuildReviewRepairBlockReason_EmptyFindingIDs(t *testing.T) {
	reason := buildReviewRepairBlockReason(2, nil)
	if !strings.Contains(reason, "operator input required") {
		t.Fatalf("expected operator-input next-action even without finding ids, got %q", reason)
	}
	if strings.Contains(reason, "unresolved findings:") {
		t.Fatalf("expected no unresolved-findings tail when list is empty, got %q", reason)
	}
}

// TestRunTicket_WorkerRequestIncludesRoleProfile covers ver-laq2 test case 6:
// the engine must forward the configured role profile fields (runtime, model,
// reasoning) to the adapter via WorkerRequest and ReviewRequest so that CLI
// adapters can pass them to their underlying tools. The implementation and
// review artifacts must also record the profile so runs are auditable.
func TestRunTicket_WorkerRequestIncludesRoleProfile(t *testing.T) {
	repoRoot := t.TempDir()
	cfg := policy.DefaultConfig()
	// DefaultConfig sets Worker={claude/sonnet/high}, Reviewer={claude/opus/xhigh}.
	ticket := testTicket("ver-role-profile")
	plan, claim := testPlanAndClaim(t, repoRoot, ticket, cfg, "run-role-profile", "lease-role-profile", []string{`true`})

	started, finished := testRunTimes()
	adapter := runtimefake.New(
		[]runtime.WorkerResult{
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            claim.LeaseID,
				StartedAt:          started,
				FinishedAt:         finished,
				ResultArtifactPath: filepath.Join(repoRoot, "worker.json"),
			},
		},
		[]runtime.ReviewResult{
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            claim.LeaseID,
				StartedAt:          finished.Add(time.Second),
				FinishedAt:         finished.Add(2 * time.Second),
				ReviewStatus:       runtime.ReviewStatusPassed,
				Summary:            "clean",
				ResultArtifactPath: filepath.Join(repoRoot, "review.json"),
			},
		},
	)

	result, err := RunTicket(context.Background(), RunTicketRequest{
		RepoRoot: repoRoot,
		RunID:    "run-role-profile",
		Ticket:   ticket,
		Plan:     plan,
		Claim:    claim,
		Adapter:  adapter,
		Config:   cfg,
	})
	if err != nil {
		t.Fatalf("RunTicket returned error: %v", err)
	}
	if result.Snapshot.CurrentPhase != state.TicketPhaseClosed {
		t.Fatalf("expected closed phase, got %q", result.Snapshot.CurrentPhase)
	}

	// Verify the worker request carried the configured worker profile.
	workerReqs := adapter.WorkerRequests()
	if len(workerReqs) != 1 {
		t.Fatalf("expected 1 worker request, got %d", len(workerReqs))
	}
	if got := workerReqs[0].Runtime; got != "claude" {
		t.Errorf("worker request: want Runtime %q, got %q", "claude", got)
	}
	if got := workerReqs[0].Model; got != "sonnet" {
		t.Errorf("worker request: want Model %q, got %q", "sonnet", got)
	}
	if got := workerReqs[0].Reasoning; got != "high" {
		t.Errorf("worker request: want Reasoning %q, got %q", "high", got)
	}
	if got := workerReqs[0].FallbackReason; got != "" {
		t.Errorf("worker request: expected empty FallbackReason on primary attempt, got %q", got)
	}

	// Verify the reviewer request carried the configured reviewer profile.
	reviewReqs := adapter.ReviewRequests()
	if len(reviewReqs) != 1 {
		t.Fatalf("expected 1 review request, got %d", len(reviewReqs))
	}
	if got := reviewReqs[0].Runtime; got != "claude" {
		t.Errorf("review request: want Runtime %q, got %q", "claude", got)
	}
	if got := reviewReqs[0].Model; got != "opus" {
		t.Errorf("review request: want Model %q, got %q", "opus", got)
	}
	if got := reviewReqs[0].Reasoning; got != "xhigh" {
		t.Errorf("review request: want Reasoning %q, got %q", "xhigh", got)
	}
	if got := reviewReqs[0].FallbackReason; got != "" {
		t.Errorf("review request: expected empty FallbackReason on primary attempt, got %q", got)
	}

	// Verify the implementation artifact records the worker profile.
	impl := result.Snapshot.Implementation
	if impl == nil {
		t.Fatal("expected implementation artifact, got nil")
	}
	if impl.Runtime != "claude" {
		t.Errorf("implementation artifact: want Runtime %q, got %q", "claude", impl.Runtime)
	}
	if impl.Model != "sonnet" {
		t.Errorf("implementation artifact: want Model %q, got %q", "sonnet", impl.Model)
	}
	if impl.Reasoning != "high" {
		t.Errorf("implementation artifact: want Reasoning %q, got %q", "high", impl.Reasoning)
	}
	if impl.FallbackReason != "" {
		t.Errorf("implementation artifact: expected empty FallbackReason, got %q", impl.FallbackReason)
	}

	// Verify the review artifact records the reviewer profile.
	rev := result.Snapshot.Review
	if rev == nil {
		t.Fatal("expected review artifact, got nil")
	}
	if rev.ReviewerRuntime != "claude" {
		t.Errorf("review artifact: want ReviewerRuntime %q, got %q", "claude", rev.ReviewerRuntime)
	}
	if rev.ReviewerModel != "opus" {
		t.Errorf("review artifact: want ReviewerModel %q, got %q", "opus", rev.ReviewerModel)
	}
	if rev.ReviewerReasoning != "xhigh" {
		t.Errorf("review artifact: want ReviewerReasoning %q, got %q", "xhigh", rev.ReviewerReasoning)
	}
	if rev.FallbackReason != "" {
		t.Errorf("review artifact: expected empty FallbackReason, got %q", rev.FallbackReason)
	}
}

// TestRunTicket_FallbackProfileUsedOnRetry covers ver-laq2 test case 9:
// when the primary worker profile returns a retryable failure the retry must
// switch to the configured WorkerFallback profile, and the implementation
// artifact must record the fallback model/reasoning/fallback_reason so the
// audit trail is accurate even when the primary profile was not the one that
// ultimately produced the result.
func TestRunTicket_FallbackProfileUsedOnRetry(t *testing.T) {
	repoRoot := t.TempDir()
	cfg := policy.DefaultConfig()
	cfg.Runtime.WorkerFallback = policy.RoleProfile{
		Runtime:   "claude",
		Model:     "haiku",
		Reasoning: "low",
	}
	ticket := testTicket("ver-fallback-retry")
	plan, claim := testPlanAndClaim(t, repoRoot, ticket, cfg, "run-fallback-retry", "lease-fallback-retry", []string{`true`})

	started, finished := testRunTimes()
	adapter := runtimefake.New(
		[]runtime.WorkerResult{
			// First attempt uses the primary profile and returns retryable.
			{
				Status:             runtime.WorkerStatusBlocked,
				RetryClass:         runtime.RetryClassRetryable,
				LeaseID:            claim.LeaseID,
				StartedAt:          started,
				FinishedAt:         finished,
				ResultArtifactPath: filepath.Join(repoRoot, "worker-1.json"),
			},
			// Second attempt (fallback) succeeds.
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            claim.LeaseID,
				StartedAt:          finished.Add(time.Second),
				FinishedAt:         finished.Add(2 * time.Second),
				ResultArtifactPath: filepath.Join(repoRoot, "worker-2.json"),
			},
		},
		[]runtime.ReviewResult{
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            claim.LeaseID,
				StartedAt:          finished.Add(3 * time.Second),
				FinishedAt:         finished.Add(4 * time.Second),
				ReviewStatus:       runtime.ReviewStatusPassed,
				Summary:            "clean",
				ResultArtifactPath: filepath.Join(repoRoot, "review.json"),
			},
		},
	)

	result, err := RunTicket(context.Background(), RunTicketRequest{
		RepoRoot: repoRoot,
		RunID:    "run-fallback-retry",
		Ticket:   ticket,
		Plan:     plan,
		Claim:    claim,
		Adapter:  adapter,
		Config:   cfg,
	})
	if err != nil {
		t.Fatalf("RunTicket returned error: %v", err)
	}
	if result.Snapshot.CurrentPhase != state.TicketPhaseClosed {
		t.Fatalf("expected closed phase, got %q", result.Snapshot.CurrentPhase)
	}

	workerReqs := adapter.WorkerRequests()
	if len(workerReqs) != 2 {
		t.Fatalf("expected 2 worker requests (primary + fallback retry), got %d", len(workerReqs))
	}

	// First request must use the primary (sonnet) profile with no fallback reason.
	if got := workerReqs[0].Model; got != "sonnet" {
		t.Errorf("attempt 1: want primary Model %q, got %q", "sonnet", got)
	}
	if got := workerReqs[0].FallbackReason; got != "" {
		t.Errorf("attempt 1: expected empty FallbackReason on primary attempt, got %q", got)
	}

	// Second request must use the fallback (haiku) profile with a non-empty fallback reason.
	if got := workerReqs[1].Model; got != "haiku" {
		t.Errorf("attempt 2: want fallback Model %q, got %q", "haiku", got)
	}
	if got := workerReqs[1].Reasoning; got != "low" {
		t.Errorf("attempt 2: want fallback Reasoning %q, got %q", "low", got)
	}
	if got := workerReqs[1].FallbackReason; got == "" {
		t.Error("attempt 2: expected non-empty FallbackReason on fallback attempt")
	}

	// The implementation artifact must reflect the fallback profile that
	// produced the successful result, not the primary profile.
	impl := result.Snapshot.Implementation
	if impl == nil {
		t.Fatal("expected implementation artifact, got nil")
	}
	if impl.Model != "haiku" {
		t.Errorf("implementation artifact: want fallback Model %q, got %q", "haiku", impl.Model)
	}
	if impl.Reasoning != "low" {
		t.Errorf("implementation artifact: want fallback Reasoning %q, got %q", "low", impl.Reasoning)
	}
	if impl.FallbackReason == "" {
		t.Error("implementation artifact: expected non-empty FallbackReason when fallback was used")
	}
}

// TestRunTicket_VerifyBudgetExhaustedBlocks verifies that when the
// MaxImplementationAttempts limit is reached after repeated verification
// failures, the ticket transitions to blocked with a clear reason that cites
// the non-convergent verification prefix and the number of attempts.
// This is an end-to-end regression test for AC5 (ticket ver-1qru): the
// ticket must block with a clear reason only after repair budget is exhausted.
func TestRunTicket_VerifyBudgetExhaustedBlocks(t *testing.T) {
	repoRoot := t.TempDir()
	cfg := policy.DefaultConfig()
	cfg.Policy.MaxImplementationAttempts = 2
	ticket := testTicket("ver-verify-budget")
	// Use a command that always fails so verification never passes.
	plan, claim := testPlanAndClaim(t, repoRoot, ticket, cfg, "run-verify-budget", "lease-verify-budget", []string{`false`})

	started, finished := testRunTimes()
	adapter := runtimefake.New(
		[]runtime.WorkerResult{
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            claim.LeaseID,
				StartedAt:          started,
				FinishedAt:         finished,
				ResultArtifactPath: filepath.Join(repoRoot, "worker-1.json"),
			},
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            claim.LeaseID,
				StartedAt:          finished.Add(3 * time.Second),
				FinishedAt:         finished.Add(4 * time.Second),
				ResultArtifactPath: filepath.Join(repoRoot, "worker-2.json"),
			},
		},
		nil, // verification never passes, no review reached
	)

	result, err := RunTicket(context.Background(), RunTicketRequest{
		RepoRoot: repoRoot,
		RunID:    "run-verify-budget",
		Ticket:   ticket,
		Plan:     plan,
		Claim:    claim,
		Adapter:  adapter,
		Config:   cfg,
	})
	if err != nil {
		t.Fatalf("RunTicket returned error: %v", err)
	}

	// AC5: the ticket must block, not close.
	if result.Snapshot.CurrentPhase != state.TicketPhaseBlocked {
		t.Fatalf("expected blocked phase after budget exhausted, got %q", result.Snapshot.CurrentPhase)
	}
	// AC5: block reason must be clear and cite the canonical non-convergent prefix.
	if !strings.Contains(result.Snapshot.BlockReason, string(state.EscalationNonConvergentVerification)) {
		t.Fatalf("expected non-convergent verification prefix in block reason, got %q", result.Snapshot.BlockReason)
	}
	if !strings.Contains(result.Snapshot.BlockReason, "2 attempt") {
		t.Fatalf("expected attempt count in block reason, got %q", result.Snapshot.BlockReason)
	}
	// AC7: repair limit must be persisted in the coverage artifact.
	if result.Snapshot.Verification == nil {
		t.Fatal("expected verification artifact to be present")
	}
	if result.Snapshot.Verification.ValidationCoverage == nil {
		t.Fatal("expected validation coverage to be present on verification artifact")
	}
	limit := result.Snapshot.Verification.ValidationCoverage.RepairLimit
	if limit == nil {
		t.Fatal("expected ValidationRepairLimit to be recorded on coverage after budget exhaustion")
	}
	if limit.Name != "max_implementation_attempts" {
		t.Fatalf("expected repair limit name max_implementation_attempts, got %q", limit.Name)
	}
	if limit.Limit != 2 || limit.Reached != 2 {
		t.Fatalf("expected limit=2 reached=2, got limit=%d reached=%d", limit.Limit, limit.Reached)
	}
	// AC6: existing behavior compatible — 2 worker requests, 0 review requests.
	if len(adapter.WorkerRequests()) != 2 {
		t.Fatalf("expected 2 worker requests, got %d", len(adapter.WorkerRequests()))
	}
	if len(adapter.ReviewRequests()) != 0 {
		t.Fatalf("expected 0 review requests (verify never passed), got %d", len(adapter.ReviewRequests()))
	}
}

// TestBuildImplementPhaseInstructions_FirstAttemptUnchanged verifies that the
// first attempt (no repair cycles) returns the same instructions as
// renderImplementInstructions so the existing behavior is preserved (AC6).
func TestBuildImplementPhaseInstructions_FirstAttemptUnchanged(t *testing.T) {
	plan := state.PlanArtifact{
		ArtifactMeta:             state.ArtifactMeta{RunID: "run-first"},
		TicketID:                 "ver-first",
		Title:                    "Test ticket",
		Description:              "Description here.",
		AcceptanceCriteria:       []string{"criterion one"},
		ValidationCommands:       []string{"go test ./..."},
		EffectiveReviewThreshold: state.SeverityP2,
	}
	st := &ticketRunState{
		req:          RunTicketRequest{Plan: plan},
		currentPhase: state.TicketPhaseImplement,
	}

	got := buildImplementPhaseInstructions(st, 1)
	want := renderImplementInstructions(plan, state.TicketPhaseImplement, 1)
	if got != want {
		t.Fatalf("expected first-attempt instructions to be identical to renderImplementInstructions output")
	}
}

// TestBuildImplementPhaseInstructions_RetryIncludesVerificationRepairContext
// verifies that when a retry is triggered by failing verification checks, the
// implement instructions include the prior verification summary and the
// specific failing check details. This ensures the worker gets focused repair
// context rather than a generic retry note (AC2, AC3 for ticket ver-1qru).
func TestBuildImplementPhaseInstructions_RetryIncludesVerificationRepairContext(t *testing.T) {
	plan := state.PlanArtifact{
		ArtifactMeta:             state.ArtifactMeta{RunID: "run-repair-ctx"},
		TicketID:                 "ver-repair-ctx",
		EffectiveReviewThreshold: state.SeverityP2,
	}

	const failingCheckID = "check-derived-ruff"
	const failingCmd = "ruff check src/app.py"

	verification := &state.VerificationArtifact{
		ArtifactMeta: state.ArtifactMeta{RunID: "run-repair-ctx"},
		TicketID:     "ver-repair-ctx",
		Attempt:      1,
		Passed:       false,
		Results: []state.VerificationResult{
			{Command: failingCmd, ExitCode: 1, Passed: false},
		},
		ValidationCoverage: &state.ValidationCoverageArtifact{
			ArtifactMeta: state.ArtifactMeta{RunID: "run-repair-ctx"},
			Scope:        state.ValidationScopeTicket,
			TicketID:     "ver-repair-ctx",
			DerivedChecks: []state.ValidationCheck{{
				ID:           failingCheckID,
				Scope:        state.ValidationScopeTicket,
				Source:       state.ValidationCheckSourceDerived,
				Command:      failingCmd,
				Reason:       "ruff check for changed Python files",
				MatchedFiles: []string{"src/app.py"},
				TicketID:     "ver-repair-ctx",
				Advisory:     true,
			}},
			ExecutedChecks: []state.ValidationCheckExecution{{
				CheckID:  failingCheckID,
				Result:   state.ValidationCheckResultFailed,
				ExitCode: 1,
				Attempt:  1,
			}},
		},
	}

	repairCycle := state.RepairCycleArtifact{
		TicketID:        "ver-repair-ctx",
		Cycle:           1,
		TriggerCheckIDs: []string{failingCheckID},
		Status:          "repair_pending",
		Scope:           state.ValidationScopeTicket,
	}

	st := &ticketRunState{
		req: RunTicketRequest{
			Plan: plan,
		},
		currentPhase: state.TicketPhaseImplement,
		verification: verification,
		repairCycles: []state.RepairCycleArtifact{repairCycle},
		implementation: &state.ImplementationArtifact{
			TicketID:     "ver-repair-ctx",
			ChangedFiles: []string{"src/app.py"},
		},
	}

	instructions := buildImplementPhaseInstructions(st, 2)

	// The retry instructions must mention the failing command.
	if !strings.Contains(instructions, failingCmd) {
		t.Fatalf("expected retry instructions to include failing command %q, got:\n%s", failingCmd, instructions)
	}
	// The instructions must reference the matched file that triggered the check.
	if !strings.Contains(instructions, "src/app.py") {
		t.Fatalf("expected retry instructions to include matched file, got:\n%s", instructions)
	}
	// The instructions must include a "Checks to Fix" or similar section.
	if !strings.Contains(instructions, "Checks to Fix") {
		t.Fatalf("expected 'Checks to Fix' section in retry instructions, got:\n%s", instructions)
	}
	// Prior verification summary must be present (shows which commands failed).
	if !strings.Contains(instructions, "Prior Verification") {
		t.Fatalf("expected 'Prior Verification' section in retry instructions, got:\n%s", instructions)
	}
	// Changed files section must be present.
	if !strings.Contains(instructions, "Changed Files") {
		t.Fatalf("expected 'Changed Files' section in retry instructions, got:\n%s", instructions)
	}
	// The base attempt number must still be present.
	if !strings.Contains(instructions, "Attempt:") {
		t.Fatalf("expected 'Attempt:' field in retry instructions, got:\n%s", instructions)
	}
}

// TestBuildImplementPhaseInstructions_RetryWithoutVerificationCyclesUnchanged
// verifies that a retry with NO verification repair cycles returns the same
// output as the base renderImplementInstructions. This covers the case where
// a retry is needed but not triggered by failing derived/declared checks
// (e.g. a worker crash retry before any verification ran).
func TestBuildImplementPhaseInstructions_RetryWithoutVerificationCyclesUnchanged(t *testing.T) {
	plan := state.PlanArtifact{
		ArtifactMeta:             state.ArtifactMeta{RunID: "run-no-cycles"},
		TicketID:                 "ver-no-cycles",
		EffectiveReviewThreshold: state.SeverityP2,
	}
	st := &ticketRunState{
		req:          RunTicketRequest{Plan: plan},
		currentPhase: state.TicketPhaseImplement,
		repairCycles: []state.RepairCycleArtifact{
			// Only review-triggered cycle (TriggerFindingIDs), no check IDs.
			{
				TicketID:          "ver-no-cycles",
				Cycle:             1,
				TriggerFindingIDs: []string{"finding-1"},
				Status:            "completed",
			},
		},
	}

	got := buildImplementPhaseInstructions(st, 2)
	want := renderImplementInstructions(plan, state.TicketPhaseImplement, 2)
	if got != want {
		t.Fatalf("expected retry with only review cycles to return base instructions unchanged")
	}
}
