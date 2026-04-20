package e2e

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"
	"verk/internal/adapters/runtime"
	"verk/internal/adapters/ticketstore/tkmd"
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
			"ticket-a":  reviewPassed(repoRoot, "review-a.json", 2*time.Second),
			"ticket-b":  reviewPassed(repoRoot, "review-b.json", 10*time.Second),
			"ticket-c":  reviewPassed(repoRoot, "review-c.json", 6*time.Second),
			"epic-root": reviewPassed(repoRoot, "review-epic.json", 12*time.Second),
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
			"ticket-a":      reviewPassed(repoRoot, "review-a.json", 2*time.Second),
			"ticket-b":      reviewPassed(repoRoot, "review-b.json", 6*time.Second),
			"epic-conflict": reviewPassed(repoRoot, "review-epic.json", 8*time.Second),
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

func TestEpicThreeLevelHierarchy(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initRepo(t, repoRoot)
	cfg := policy.DefaultConfig()
	cfg.Scheduler.MaxDepth = 3

	// 3-level hierarchy: epic -> ticket-1 -> sub-1
	epic := epicTicket("epic-3level", []string{"internal", "docs"})
	saveTicket(t, repoRoot, epic)

	ticket1 := epicChild("ticket-1", epic.ID, tkmd.StatusOpen, []string{"internal/app"})
	saveTicket(t, repoRoot, ticket1)

	ticket2 := epicChild("ticket-2", epic.ID, tkmd.StatusOpen, []string{"docs"})
	saveTicket(t, repoRoot, ticket2)

	// Sub-ticket of ticket-1 (level 3)
	sub1 := epicChild("sub-1", ticket1.ID, tkmd.StatusOpen, []string{"internal/app/sub"})
	saveTicket(t, repoRoot, sub1)

	// Create a reflecting adapter for the 3 tickets
	adapter := newE2EReflectingAdapter(3)

	result, err := engine.RunEpic(context.Background(), engine.RunEpicRequest{
		RepoRoot:     repoRoot,
		RunID:        "run-3level",
		RootTicketID: epic.ID,
		BaseCommit:   baseCommit,
		Adapter:      adapter,
		Config:       cfg,
	})
	if err != nil {
		t.Fatalf("RunEpic returned error: %v", err)
	}
	if result.Run.Status != state.EpicRunStatusCompleted {
		t.Fatalf("expected completed epic, got status %s", result.Run.Status)
	}

	// All 3 tickets (sub-1, ticket-1, ticket-2) should have been dispatched
	workerReqs := adapter.WorkerRequests()
	startedSet := make(map[string]bool)
	for _, req := range workerReqs {
		startedSet[req.TicketID] = true
	}
	for _, id := range []string{"sub-1", "ticket-1", "ticket-2"} {
		if !startedSet[id] {
			t.Errorf("expected ticket %q to be started, got %v", id, sortedKeys(startedSet))
		}
	}
}

// e2eReflectingAdapter is a reflecting adapter for e2e tests.
type e2eReflectingAdapter struct {
	workerIndex   int
	reviewIndex   int
	workerResults []runtime.WorkerResult
	reviewResults []runtime.ReviewResult
	mu            sync.Mutex
	workerReqs    []runtime.WorkerRequest
	reviewReqs    []runtime.ReviewRequest
}

func (a *e2eReflectingAdapter) RunWorker(ctx context.Context, req runtime.WorkerRequest) (runtime.WorkerResult, error) {
	if err := ctx.Err(); err != nil {
		return runtime.WorkerResult{}, err
	}
	a.mu.Lock()
	a.workerReqs = append(a.workerReqs, req)
	if a.workerIndex >= len(a.workerResults) {
		a.mu.Unlock()
		return runtime.WorkerResult{}, fmt.Errorf("e2eReflectingAdapter: no more worker results (index %d)", a.workerIndex)
	}
	result := a.workerResults[a.workerIndex]
	result.LeaseID = req.LeaseID
	a.workerIndex++
	a.mu.Unlock()
	return result, nil
}

func (a *e2eReflectingAdapter) RunReviewer(ctx context.Context, req runtime.ReviewRequest) (runtime.ReviewResult, error) {
	if err := ctx.Err(); err != nil {
		return runtime.ReviewResult{}, err
	}
	a.mu.Lock()
	a.reviewReqs = append(a.reviewReqs, req)
	if a.reviewIndex >= len(a.reviewResults) {
		a.mu.Unlock()
		return runtime.ReviewResult{}, fmt.Errorf("e2eReflectingAdapter: no more review results (index %d)", a.reviewIndex)
	}
	result := a.reviewResults[a.reviewIndex]
	result.LeaseID = req.LeaseID
	a.reviewIndex++
	a.mu.Unlock()
	return result, nil
}

func (a *e2eReflectingAdapter) WorkerRequests() []runtime.WorkerRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]runtime.WorkerRequest(nil), a.workerReqs...)
}

func (a *e2eReflectingAdapter) ReviewRequests() []runtime.ReviewRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]runtime.ReviewRequest(nil), a.reviewReqs...)
}

func newE2EReflectingAdapter(numTickets int) *e2eReflectingAdapter {
	start := testTime()
	workerResults := make([]runtime.WorkerResult, numTickets)
	reviewResults := make([]runtime.ReviewResult, numTickets+1)
	for i := 0; i < numTickets; i++ {
		workerResults[i] = runtime.WorkerResult{
			Status:             runtime.WorkerStatusDone,
			RetryClass:         runtime.RetryClassTerminal,
			LeaseID:            "placeholder",
			StartedAt:          start.Add(time.Duration(i) * time.Second),
			FinishedAt:         start.Add(time.Duration(i) * time.Second).Add(time.Second),
			ResultArtifactPath: "artifact.json",
		}
		reviewResults[i] = runtime.ReviewResult{
			Status:             runtime.WorkerStatusDone,
			RetryClass:         runtime.RetryClassTerminal,
			LeaseID:            "placeholder",
			StartedAt:          start.Add(time.Duration(i) * time.Second).Add(2 * time.Second),
			FinishedAt:         start.Add(time.Duration(i) * time.Second).Add(3 * time.Second),
			ReviewStatus:       runtime.ReviewStatusPassed,
			Summary:            "clean",
			ResultArtifactPath: "review.json",
		}
	}
	epicIdx := numTickets
	reviewResults[epicIdx] = runtime.ReviewResult{
		Status:             runtime.WorkerStatusDone,
		RetryClass:         runtime.RetryClassTerminal,
		LeaseID:            "placeholder",
		StartedAt:          start.Add(time.Duration(epicIdx) * time.Second).Add(2 * time.Second),
		FinishedAt:         start.Add(time.Duration(epicIdx) * time.Second).Add(3 * time.Second),
		ReviewStatus:       runtime.ReviewStatusPassed,
		Summary:            "epic gate clean",
		ResultArtifactPath: "epic-review.json",
	}
	return &e2eReflectingAdapter{
		workerResults: workerResults,
		reviewResults: reviewResults,
	}
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
