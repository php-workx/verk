package e2e

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
	"verk/internal/adapters/runtime"
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

	adapter := newRoutingAdapter(
		map[string]runtime.WorkerResult{
			"ticket-a": workerDone(repoRoot, "worker-a.json", 0),
			"ticket-b": workerDone(repoRoot, "worker-b.json", 8*time.Second),
			"ticket-c": workerDone(repoRoot, "worker-c.json", 4*time.Second),
		},
		map[string]runtime.ReviewResult{
			"ticket-a": reviewPassed(repoRoot, "review-a.json", 2*time.Second),
			"ticket-b": reviewPassed(repoRoot, "review-b.json", 10*time.Second),
			"ticket-c": reviewPassed(repoRoot, "review-c.json", 6*time.Second),
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

	adapter := newRoutingAdapter(
		map[string]runtime.WorkerResult{
			"ticket-a": workerDone(repoRoot, "worker-a.json", 0),
			"ticket-b": workerDone(repoRoot, "worker-b.json", 4*time.Second),
		},
		map[string]runtime.ReviewResult{
			"ticket-a": reviewPassed(repoRoot, "review-a.json", 2*time.Second),
			"ticket-b": reviewPassed(repoRoot, "review-b.json", 6*time.Second),
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

func workerDone(repoRoot, artifact string, offset time.Duration) runtime.WorkerResult {
	return runtime.WorkerResult{
		Status:             runtime.WorkerStatusDone,
		RetryClass:         runtime.RetryClassTerminal,
		StartedAt:          testTime().Add(offset),
		FinishedAt:         testTime().Add(offset + time.Second),
		ResultArtifactPath: filepath.Join(repoRoot, artifact),
	}
}

func reviewPassed(repoRoot, artifact string, offset time.Duration) runtime.ReviewResult {
	return runtime.ReviewResult{
		Status:             runtime.WorkerStatusDone,
		RetryClass:         runtime.RetryClassTerminal,
		StartedAt:          testTime().Add(offset),
		FinishedAt:         testTime().Add(offset + time.Second),
		ReviewStatus:       runtime.ReviewStatusPassed,
		Summary:            "clean",
		ResultArtifactPath: filepath.Join(repoRoot, artifact),
	}
}

type routingAdapter struct {
	mu sync.Mutex

	workerResults map[string]runtime.WorkerResult
	reviewResults map[string]runtime.ReviewResult
}

func newRoutingAdapter(workerResults map[string]runtime.WorkerResult, reviewResults map[string]runtime.ReviewResult) *routingAdapter {
	return &routingAdapter{
		workerResults: workerResults,
		reviewResults: reviewResults,
	}
}

func (a *routingAdapter) RunWorker(ctx context.Context, req runtime.WorkerRequest) (runtime.WorkerResult, error) {
	if err := ctx.Err(); err != nil {
		return runtime.WorkerResult{}, err
	}
	a.mu.Lock()
	result, ok := a.workerResults[req.TicketID]
	a.mu.Unlock()
	if !ok {
		return runtime.WorkerResult{}, fmt.Errorf("missing worker result for ticket %q", req.TicketID)
	}
	result.LeaseID = req.LeaseID
	if result.StartedAt.IsZero() {
		result.StartedAt = testTime()
	}
	if result.FinishedAt.IsZero() {
		result.FinishedAt = result.StartedAt.Add(time.Second)
	}
	return result, nil
}

func (a *routingAdapter) RunReviewer(ctx context.Context, req runtime.ReviewRequest) (runtime.ReviewResult, error) {
	if err := ctx.Err(); err != nil {
		return runtime.ReviewResult{}, err
	}
	a.mu.Lock()
	result, ok := a.reviewResults[req.TicketID]
	a.mu.Unlock()
	if !ok {
		return runtime.ReviewResult{}, fmt.Errorf("missing review result for ticket %q", req.TicketID)
	}
	result.LeaseID = req.LeaseID
	if result.StartedAt.IsZero() {
		result.StartedAt = testTime().Add(2 * time.Second)
	}
	if result.FinishedAt.IsZero() {
		result.FinishedAt = result.StartedAt.Add(time.Second)
	}
	return result, nil
}
