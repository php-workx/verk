package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"verk/internal/adapters/runtime"
	runtimefake "verk/internal/adapters/runtime/fake"
	"verk/internal/adapters/ticketstore/tkmd"
	"verk/internal/policy"
	"verk/internal/state"
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
	if result.Snapshot.Closeout == nil || !result.Snapshot.Closeout.Closable {
		t.Fatalf("expected closable closeout, got %#v", result.Snapshot.Closeout)
	}
	if result.Snapshot.BlockReason != "" {
		t.Fatalf("expected no block reason, got %q", result.Snapshot.BlockReason)
	}

	snapshotPath := filepath.Join(repoRoot, ".verk", "runs", "run-happy", "tickets", ticket.ID, "ticket-run.json")
	if _, err := os.Stat(snapshotPath); err != nil {
		t.Fatalf("expected snapshot file to exist: %v", err)
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
	claim.ExpiresAt = claim.LeasedAt.Add(500 * time.Millisecond)

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

	durableClaimPath := filepath.Join(repoRoot, ".verk", "runs", "run-claim-renewal", "claims", "claim-"+ticket.ID+".json")
	var durableClaim state.ClaimArtifact
	if err := state.LoadJSON(durableClaimPath, &durableClaim); err != nil {
		t.Fatalf("load durable claim: %v", err)
	}
	if !durableClaim.ExpiresAt.After(claim.ExpiresAt) {
		t.Fatalf("expected durable claim expiry to be renewed past %s, got %s", claim.ExpiresAt, durableClaim.ExpiresAt)
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

	claim, err := tkmd.AcquireClaim(repoRoot, runID, ticket.ID, leaseID, 10*time.Minute, testRunTime())
	if err != nil {
		t.Fatalf("AcquireClaim: %v", err)
	}
	return plan, claim
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

func TestRemainingTTL_ComputesFromNow(t *testing.T) {
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

	remaining := st.remainingTTL()
	// remainingTTL should be approximately 20m, not 30m
	if remaining >= 30*time.Minute {
		t.Fatalf("expected remainingTTL < 30m (should be ~20m), got %v", remaining)
	}
	if remaining < 15*time.Minute {
		t.Fatalf("expected remainingTTL > 15m (should be ~20m), got %v", remaining)
	}
}

func TestRemainingTTL_ZeroExpiresAt(t *testing.T) {
	st := &ticketRunState{
		req: RunTicketRequest{
			Claim: state.ClaimArtifact{},
		},
	}
	if st.remainingTTL() != 0 {
		t.Fatalf("expected 0 remainingTTL for zero ExpiresAt, got %v", st.remainingTTL())
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
// expires (ver-exae). The renewal cadence must use remainingTTL(), not
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
	// cadence (claimTTL/3 ≈ 10min) renewal would never fire. With the fix
	// (remainingTTL/3 ≈ 167ms) renewal fires well before expiry.
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

func TestTicketRunState_snapshotPreservesRestoredCreatedAt(t *testing.T) {
	fixedCreatedAt := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)

	st := &ticketRunState{
		req: RunTicketRequest{
			RunID:  "run-snapshot-restore",
			Ticket: tkmd.Ticket{ID: "ver-snapshot-restore"},
		},
		createdAt: fixedCreatedAt,
	}

	snap := st.snapshot()

	if !snap.CreatedAt.Equal(fixedCreatedAt) {
		t.Errorf("expected CreatedAt %v, got %v", fixedCreatedAt, snap.CreatedAt)
	}
}
