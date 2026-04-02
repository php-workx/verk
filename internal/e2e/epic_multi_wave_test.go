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

func TestEpicMultipleWavesNoConflicts(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initRepo(t, repoRoot)
	cfg := policy.DefaultConfig()

	root := epicTicket("epic-root", []string{"internal", "docs"})
	saveTicket(t, repoRoot, root)
	saveTicket(t, repoRoot, epicChild("ticket-a", root.ID, "ready", []string{"internal/app"}))
	saveTicket(t, repoRoot, epicChild("ticket-b", root.ID, "ready", []string{"internal/app/api"}))
	saveTicket(t, repoRoot, epicChild("ticket-c", root.ID, "ready", []string{"docs"}))

	adapter := runtimefake.New(
		[]runtime.WorkerResult{
			workerDone("lease-run-epic-ticket-a", repoRoot, "worker-a.json", 0),
			workerDone("lease-run-epic-ticket-c", repoRoot, "worker-c.json", 4*time.Second),
			workerDone("lease-run-epic-ticket-b", repoRoot, "worker-b.json", 8*time.Second),
		},
		[]runtime.ReviewResult{
			reviewPassed("lease-run-epic-ticket-a", repoRoot, "review-a.json", 2*time.Second),
			reviewPassed("lease-run-epic-ticket-c", repoRoot, "review-c.json", 6*time.Second),
			reviewPassed("lease-run-epic-ticket-b", repoRoot, "review-b.json", 10*time.Second),
		},
	)

	result, err := engine.RunEpic(context.Background(), engine.RunEpicRequest{
		RepoRoot:     repoRoot,
		RunID:        "run-epic",
		RootTicketID: root.ID,
		BaseCommit:   baseCommit,
		Adapter:      adapter,
		Config:       cfg,
	})
	if err != nil {
		t.Fatalf("RunEpic returned error: %v", err)
	}
	if result.Run.Status != state.EpicRunStatusCompleted {
		t.Fatalf("expected completed epic, got %q", result.Run.Status)
	}
	if len(result.Waves) != 2 {
		t.Fatalf("expected 2 waves, got %d", len(result.Waves))
	}
}

func TestEpicConflictSerialization(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initRepo(t, repoRoot)
	cfg := policy.DefaultConfig()

	root := epicTicket("epic-conflict", []string{"internal"})
	saveTicket(t, repoRoot, root)
	saveTicket(t, repoRoot, epicChild("ticket-a", root.ID, "ready", []string{"internal/app"}))
	saveTicket(t, repoRoot, epicChild("ticket-b", root.ID, "ready", []string{"internal/app/api"}))

	adapter := runtimefake.New(
		[]runtime.WorkerResult{
			workerDone("lease-run-conflict-ticket-a", repoRoot, "worker-a.json", 0),
			workerDone("lease-run-conflict-ticket-b", repoRoot, "worker-b.json", 4*time.Second),
		},
		[]runtime.ReviewResult{
			reviewPassed("lease-run-conflict-ticket-a", repoRoot, "review-a.json", 2*time.Second),
			reviewPassed("lease-run-conflict-ticket-b", repoRoot, "review-b.json", 6*time.Second),
		},
	)

	result, err := engine.RunEpic(context.Background(), engine.RunEpicRequest{
		RepoRoot:     repoRoot,
		RunID:        "run-conflict",
		RootTicketID: root.ID,
		BaseCommit:   baseCommit,
		Adapter:      adapter,
		Config:       cfg,
	})
	if err != nil {
		t.Fatalf("RunEpic returned error: %v", err)
	}
	if len(result.Waves) != 2 {
		t.Fatalf("expected conflicting tickets to serialize into 2 waves, got %d", len(result.Waves))
	}
	if got := result.Waves[0].TicketIDs; len(got) != 1 || got[0] != "ticket-a" {
		t.Fatalf("unexpected first wave tickets: %#v", got)
	}
}

func workerDone(leaseID, repoRoot, artifact string, offset time.Duration) runtime.WorkerResult {
	return runtime.WorkerResult{
		Status:             runtime.WorkerStatusDone,
		RetryClass:         runtime.RetryClassTerminal,
		LeaseID:            leaseID,
		StartedAt:          testTime().Add(offset),
		FinishedAt:         testTime().Add(offset + time.Second),
		ResultArtifactPath: filepath.Join(repoRoot, artifact),
	}
}

func reviewPassed(leaseID, repoRoot, artifact string, offset time.Duration) runtime.ReviewResult {
	return runtime.ReviewResult{
		Status:             runtime.WorkerStatusDone,
		RetryClass:         runtime.RetryClassTerminal,
		LeaseID:            leaseID,
		StartedAt:          testTime().Add(offset),
		FinishedAt:         testTime().Add(offset + time.Second),
		ReviewStatus:       runtime.ReviewStatusPassed,
		Summary:            "clean",
		ResultArtifactPath: filepath.Join(repoRoot, artifact),
	}
}
