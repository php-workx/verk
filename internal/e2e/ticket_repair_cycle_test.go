package e2e

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"verk/internal/adapters/runtime"
	runtimefake "verk/internal/adapters/runtime/fake"
	"verk/internal/engine"
	"verk/internal/policy"
	"verk/internal/state"
)

func TestTicketRepairCycle(t *testing.T) {
	repoRoot := t.TempDir()
	_ = initRepo(t, repoRoot)
	cfg := policy.DefaultConfig()
	ticket := taskTicket("ticket-repair", "", []string{"internal/app"})
	saveTicket(t, repoRoot, ticket)
	plan, claim := testPlanAndClaim(t, repoRoot, cfg, "run-repair", ticket, "lease-repair", []string{"true"})

	blockingFinding := runtime.ReviewFinding{
		ID:          "finding-1",
		Severity:    runtime.SeverityP2,
		Title:       "blocking",
		Body:        "blocking",
		File:        "internal/app/file.go",
		Line:        12,
		Disposition: runtime.ReviewDispositionOpen,
	}
	adapter := runtimefake.New(
		[]runtime.WorkerResult{
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            claim.LeaseID,
				StartedAt:          testTime(),
				FinishedAt:         testTime().Add(time.Second),
				ResultArtifactPath: filepath.Join(repoRoot, "worker-1.json"),
			},
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            claim.LeaseID,
				StartedAt:          testTime().Add(4 * time.Second),
				FinishedAt:         testTime().Add(5 * time.Second),
				ResultArtifactPath: filepath.Join(repoRoot, "worker-2.json"),
			},
		},
		[]runtime.ReviewResult{
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            claim.LeaseID,
				StartedAt:          testTime().Add(2 * time.Second),
				FinishedAt:         testTime().Add(3 * time.Second),
				ReviewStatus:       runtime.ReviewStatusFindings,
				Summary:            "needs repair",
				Findings:           []runtime.ReviewFinding{blockingFinding},
				ResultArtifactPath: filepath.Join(repoRoot, "review-1.json"),
			},
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            claim.LeaseID,
				StartedAt:          testTime().Add(6 * time.Second),
				FinishedAt:         testTime().Add(7 * time.Second),
				ReviewStatus:       runtime.ReviewStatusPassed,
				Summary:            "clean",
				ResultArtifactPath: filepath.Join(repoRoot, "review-2.json"),
			},
		},
	)

	result, err := engine.RunTicket(context.Background(), engine.RunTicketRequest{
		RepoRoot: repoRoot,
		RunID:    "run-repair",
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
		t.Fatalf("expected closed snapshot, got %q", result.Snapshot.CurrentPhase)
	}
	if len(result.Snapshot.RepairCycles) != 1 {
		t.Fatalf("expected 1 repair cycle, got %d", len(result.Snapshot.RepairCycles))
	}
}

func TestWaivedBlockingFindingWithoutMetadataFails(t *testing.T) {
	repoRoot := t.TempDir()
	_ = initRepo(t, repoRoot)
	cfg := policy.DefaultConfig()
	ticket := taskTicket("ticket-waiver", "", []string{"internal/app"})
	saveTicket(t, repoRoot, ticket)
	plan, claim := testPlanAndClaim(t, repoRoot, cfg, "run-waiver", ticket, "lease-waiver", []string{"true"})

	adapter := runtimefake.New(
		[]runtime.WorkerResult{{
			Status:             runtime.WorkerStatusDone,
			RetryClass:         runtime.RetryClassTerminal,
			LeaseID:            claim.LeaseID,
			StartedAt:          testTime(),
			FinishedAt:         testTime().Add(time.Second),
			ResultArtifactPath: filepath.Join(repoRoot, "worker.json"),
		}},
		[]runtime.ReviewResult{{
			Status:       runtime.WorkerStatusDone,
			RetryClass:   runtime.RetryClassTerminal,
			LeaseID:      claim.LeaseID,
			StartedAt:    testTime().Add(2 * time.Second),
			FinishedAt:   testTime().Add(3 * time.Second),
			ReviewStatus: runtime.ReviewStatusPassed,
			Summary:      "waived",
			Findings: []runtime.ReviewFinding{{
				ID:          "finding-1",
				Severity:    runtime.SeverityP2,
				Title:       "blocking",
				Body:        "blocking",
				File:        "internal/app/file.go",
				Line:        12,
				Disposition: runtime.ReviewDispositionWaived,
			}},
			ResultArtifactPath: filepath.Join(repoRoot, "review.json"),
		}},
	)

	if _, err := engine.RunTicket(context.Background(), engine.RunTicketRequest{
		RepoRoot: repoRoot,
		RunID:    "run-waiver",
		Ticket:   ticket,
		Plan:     plan,
		Claim:    claim,
		Adapter:  adapter,
		Config:   cfg,
	}); err == nil {
		t.Fatal("expected invalid waived finding metadata to fail")
	}
}
