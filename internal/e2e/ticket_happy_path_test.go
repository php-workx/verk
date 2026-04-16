package e2e

import (
	"context"
	"path/filepath"
	"testing"
	"time"
	"verk/internal/adapters/runtime"
	"verk/internal/engine"
	"verk/internal/policy"
	"verk/internal/state"

	runtimefake "verk/internal/adapters/runtime/fake"
)

func TestTicketHappyPath(t *testing.T) {
	repoRoot := t.TempDir()
	_ = initRepo(t, repoRoot)
	cfg := policy.DefaultConfig()
	ticket := taskTicket("ticket-happy", "", []string{"internal/app"})
	saveTicket(t, repoRoot, ticket)
	plan, claim := testPlanAndClaim(t, repoRoot, cfg, "run-happy", ticket, "lease-happy", []string{"true"})

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
			Status:             runtime.WorkerStatusDone,
			RetryClass:         runtime.RetryClassTerminal,
			LeaseID:            claim.LeaseID,
			StartedAt:          testTime().Add(2 * time.Second),
			FinishedAt:         testTime().Add(3 * time.Second),
			ReviewStatus:       runtime.ReviewStatusPassed,
			Summary:            "clean",
			ResultArtifactPath: filepath.Join(repoRoot, "review.json"),
		}},
	)

	result, err := engine.RunTicket(context.Background(), engine.RunTicketRequest{
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
		t.Fatalf("expected closed snapshot, got %q", result.Snapshot.CurrentPhase)
	}
	if result.Snapshot.Closeout == nil || !result.Snapshot.Closeout.Closable {
		t.Fatalf("expected closable closeout, got %#v", result.Snapshot.Closeout)
	}
}
