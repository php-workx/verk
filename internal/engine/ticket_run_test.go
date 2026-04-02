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
				RetryClass:         runtime.RetryClassRetryable,
				LeaseID:            claim.LeaseID,
				StartedAt:          started,
				FinishedAt:         finished,
				ResultArtifactPath: filepath.Join(repoRoot, "worker-1.json"),
			},
			{
				Status:             runtime.WorkerStatusDoneWithConcerns,
				RetryClass:         runtime.RetryClassRetryable,
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
				RetryClass:         runtime.RetryClassRetryable,
				LeaseID:            claim.LeaseID,
				StartedAt:          started,
				FinishedAt:         finished,
				ResultArtifactPath: filepath.Join(repoRoot, "worker-1.json"),
			},
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassRetryable,
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
				RetryClass:         runtime.RetryClassRetryable,
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

	claim, err := tkmd.AcquireClaim(repoRoot, runID, ticket.ID, leaseID, 30*time.Minute, testRunTime())
	if err != nil {
		t.Fatalf("AcquireClaim: %v", err)
	}
	return plan, claim
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
