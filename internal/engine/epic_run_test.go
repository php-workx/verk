package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
	"verk/internal/adapters/runtime"
	"verk/internal/adapters/ticketstore/tkmd"
	"verk/internal/policy"
	"verk/internal/state"

	runtimefake "verk/internal/adapters/runtime/fake"
)

// reflectingAdapter wraps a fake.Adapter and reflects the request LeaseID
// into the response, avoiding the need to predict dynamic lease IDs.
// It bypasses the inner adapter validation by returning results directly
// with the LeaseID set from the request.
type reflectingAdapter struct {
	inner         *runtimefake.Adapter
	workerIndex   int
	reviewIndex   int
	workerResults []runtime.WorkerResult
	reviewResults []runtime.ReviewResult
	mu            sync.Mutex
	workerReqs    []runtime.WorkerRequest
	reviewReqs    []runtime.ReviewRequest
}

func (a *reflectingAdapter) RunWorker(ctx context.Context, req runtime.WorkerRequest) (runtime.WorkerResult, error) {
	if err := ctx.Err(); err != nil {
		return runtime.WorkerResult{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.workerReqs = append(a.workerReqs, req)
	if a.workerIndex >= len(a.workerResults) {
		return runtime.WorkerResult{}, fmt.Errorf("reflectingAdapter: no more scripted worker results (index %d)", a.workerIndex)
	}
	result := a.workerResults[a.workerIndex]
	result.LeaseID = req.LeaseID
	a.workerIndex++
	return result, nil
}

func (a *reflectingAdapter) RunReviewer(ctx context.Context, req runtime.ReviewRequest) (runtime.ReviewResult, error) {
	if err := ctx.Err(); err != nil {
		return runtime.ReviewResult{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reviewReqs = append(a.reviewReqs, req)
	if a.reviewIndex >= len(a.reviewResults) {
		return runtime.ReviewResult{}, fmt.Errorf("reflectingAdapter: no more scripted review results (index %d)", a.reviewIndex)
	}
	result := a.reviewResults[a.reviewIndex]
	result.LeaseID = req.LeaseID
	a.reviewIndex++
	return result, nil
}

func (a *reflectingAdapter) WorkerRequests() []runtime.WorkerRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]runtime.WorkerRequest(nil), a.workerReqs...)
}

func (a *reflectingAdapter) ReviewRequests() []runtime.ReviewRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]runtime.ReviewRequest(nil), a.reviewReqs...)
}

func newReflectingAdapter(numTickets int) *reflectingAdapter {
	start := epicTestStart()
	workerResults := make([]runtime.WorkerResult, numTickets)
	// +1 review result: the extra slot is reserved for the epic closure gate
	// reviewer that runs after all child tickets close. Without it the gate
	// tries to invoke the reviewer but runs out of scripted results.
	reviewResults := make([]runtime.ReviewResult, numTickets+1)
	for i := 0; i < numTickets; i++ {
		workerResults[i] = runtime.WorkerResult{
			Status:             runtime.WorkerStatusDone,
			RetryClass:         runtime.RetryClassTerminal,
			LeaseID:            "placeholder", // will be overwritten by reflectingAdapter
			StartedAt:          start.Add(time.Duration(i) * time.Second),
			FinishedAt:         start.Add(time.Duration(i) * time.Second).Add(time.Second),
			ResultArtifactPath: "artifact.json",
		}
		reviewResults[i] = runtime.ReviewResult{
			Status:             runtime.WorkerStatusDone,
			RetryClass:         runtime.RetryClassTerminal,
			LeaseID:            "placeholder", // will be overwritten by reflectingAdapter
			StartedAt:          start.Add(time.Duration(i) * time.Second).Add(2 * time.Second),
			FinishedAt:         start.Add(time.Duration(i) * time.Second).Add(3 * time.Second),
			ReviewStatus:       runtime.ReviewStatusPassed,
			Summary:            "clean",
			ResultArtifactPath: "review.json",
		}
	}
	// Epic closure gate reviewer result (index numTickets).
	epicIdx := numTickets
	reviewResults[epicIdx] = runtime.ReviewResult{
		Status:             runtime.WorkerStatusDone,
		RetryClass:         runtime.RetryClassTerminal,
		LeaseID:            "placeholder", // will be overwritten by reflectingAdapter
		StartedAt:          start.Add(time.Duration(epicIdx+1) * time.Second).Add(2 * time.Second),
		FinishedAt:         start.Add(time.Duration(epicIdx+1) * time.Second).Add(3 * time.Second),
		ReviewStatus:       runtime.ReviewStatusPassed,
		Summary:            "epic gate: no blocking findings",
		ResultArtifactPath: "epic-review.json",
	}
	return &reflectingAdapter{
		inner:         runtimefake.New(workerResults, reviewResults),
		workerResults: workerResults,
		reviewResults: reviewResults,
	}
}

func TestBuildWaveSerializesConflictingOwnedPaths(t *testing.T) {
	ready := []tkmd.Ticket{
		{
			ID:     "ticket-a",
			Title:  "A",
			Status: tkmd.StatusReady,
			OwnedPaths: []string{
				"internal/app",
			},
		},
		{
			ID:     "ticket-b",
			Title:  "B",
			Status: tkmd.StatusReady,
			OwnedPaths: []string{
				"internal/app/api",
			},
		},
		{
			ID:     "ticket-c",
			Title:  "C",
			Status: tkmd.StatusReady,
			OwnedPaths: []string{
				"docs",
			},
		},
	}

	wave, err := BuildWave(ready, 2)
	if err != nil {
		t.Fatalf("BuildWave returned error: %v", err)
	}

	want := []string{"ticket-a", "ticket-c"}
	if !reflect.DeepEqual(wave.TicketIDs, want) {
		t.Fatalf("expected wave tickets %v, got %v", want, wave.TicketIDs)
	}
	if wave.Status != state.WaveStatusPlanned {
		t.Fatalf("expected planned wave, got %q", wave.Status)
	}
	if !reflect.DeepEqual(wave.PlannedScope, []string{"docs", "internal/app"}) {
		t.Fatalf("unexpected planned scope: %#v", wave.PlannedScope)
	}
}

func TestAcceptWave_ScopeViolationIsFatal(t *testing.T) {
	wave := state.WaveArtifact{
		WaveID: "wave-1",
		Status: state.WaveStatusRunning,
		TicketIDs: []string{
			"ticket-a",
		},
		PlannedScope: []string{
			"internal/app",
		},
	}

	req := WaveAcceptanceRequest{
		Wave:                 wave,
		TicketPhases:         []state.TicketPhase{state.TicketPhaseClosed},
		ChangedFiles:         []string{"internal/other.go"},
		TicketScopes:         map[string][]string{"ticket-a": {"internal/app"}},
		ClaimsReleased:       true,
		PersistenceSucceeded: true,
	}

	// Scope violations must fail-closed (G9: scope checks fail closed).
	result, err := AcceptWave(req)
	if err == nil {
		t.Fatal("expected error for scope violation, got nil")
	}
	if result.Status != state.WaveStatusFailed {
		t.Fatalf("expected failed status for scope violation, got %q", result.Status)
	}
}

func TestCollectBlockedTicketsDoesNotOfferRetryForSnapshotlessBlockedTicket(t *testing.T) {
	repoRoot := t.TempDir()
	child := tkmd.Ticket{
		ID:     "ticket-blocked",
		Title:  "Blocked ticket",
		Status: tkmd.StatusBlocked,
	}

	blocked := collectBlockedTickets(repoRoot, "run-without-snapshot", []tkmd.Ticket{child})
	if len(blocked) != 1 {
		t.Fatalf("expected one blocked ticket, got %d", len(blocked))
	}
	if blocked[0].Phase != state.TicketPhaseIntake {
		t.Fatalf("expected derived intake phase without snapshot, got %q", blocked[0].Phase)
	}
	if blocked[0].RetryPhase != "" {
		t.Fatalf("expected no retry phase without a blocked run snapshot, got %q", blocked[0].RetryPhase)
	}
}

func TestCollectBlockedTicketsOffersRetryForBlockedSnapshot(t *testing.T) {
	repoRoot := t.TempDir()
	runID := "run-with-blocked-snapshot"
	child := tkmd.Ticket{
		ID:     "ticket-blocked",
		Title:  "Blocked ticket",
		Status: tkmd.StatusBlocked,
	}
	writeTicketRunFixture(t, repoRoot, runID, TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: artifactSchemaVersion, RunID: runID},
		TicketID:     child.ID,
		CurrentPhase: state.TicketPhaseBlocked,
		BlockReason:  "review failed",
	})

	blocked := collectBlockedTickets(repoRoot, runID, []tkmd.Ticket{child})
	if len(blocked) != 1 {
		t.Fatalf("expected one blocked ticket, got %d", len(blocked))
	}
	if blocked[0].Phase != state.TicketPhaseBlocked {
		t.Fatalf("expected blocked phase, got %q", blocked[0].Phase)
	}
	if blocked[0].RetryPhase != state.TicketPhaseImplement {
		t.Fatalf("expected implement retry phase, got %q", blocked[0].RetryPhase)
	}
}

func TestRunEpicSchedulesOpenAndReadyTickets(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	cfg := policy.DefaultConfig()

	epic := epicTicket("epic-ready")
	mustSaveTicket(t, repoRoot, epic)

	ready := epicChildTicket("ticket-ready", epic.ID, tkmd.StatusReady, nil, []string{"internal/app"})
	mustSaveTicket(t, repoRoot, ready)

	// Open tickets with resolved deps should also be scheduled (tk creates tickets as open)
	open := epicChildTicket("ticket-open", epic.ID, tkmd.StatusOpen, nil, []string{"docs"})
	mustSaveTicket(t, repoRoot, open)

	// Blocked tickets should NOT be scheduled
	blocked := epicChildTicket("ticket-blocked", epic.ID, tkmd.StatusBlocked, nil, []string{"config"})
	mustSaveTicket(t, repoRoot, blocked)

	// Use the blocking adapter which handles any ticket order (uses req.LeaseID)
	adapter := newBlockingEpicAdapter(t)
	go func() {
		// Collect started ticket IDs from the channel and relay them.
		// Do NOT call t.Fatalf from this goroutine — Go 1.22+ panics on that.
		ids := make([]string, 0, 2)
		timeout := time.NewTimer(10 * time.Second)
		defer timeout.Stop()
		for len(ids) < 2 {
			select {
			case id := <-adapter.started:
				ids = append(ids, id)
			case <-timeout.C:
				close(adapter.release)
				return
			}
		}
		close(adapter.release)
	}()

	result, err := RunEpic(context.Background(), RunEpicRequest{
		RepoRoot:     repoRoot,
		RunID:        "run-ready",
		RootTicketID: epic.ID,
		BaseCommit:   baseCommit,
		Adapter:      adapter,
		Config:       cfg,
	})

	// Both open and ready tickets should have been scheduled
	if adapter.maxConcurrent() < 2 {
		t.Fatalf("expected both open and ready tickets to run concurrently, max concurrent was %d", adapter.maxConcurrent())
	}

	// Epic should not be completed because blocked ticket remains, and
	// the non-completed persisted state must be reflected as a non-nil error.
	if err == nil {
		t.Fatal("expected non-nil error when epic has blocked children, got nil")
	}
	if !errors.Is(err, ErrEpicBlocked) {
		t.Fatalf("expected ErrEpicBlocked, got: %v", err)
	}
	if result.Run.Status == state.EpicRunStatusCompleted {
		t.Fatalf("expected epic to stay incomplete while blocked ticket remains")
	}
}

func TestRunEpicScopeViolationBlocksWave(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	cfg := policy.DefaultConfig()

	epic := epicTicket("epic-scope")
	mustSaveTicket(t, repoRoot, epic)

	child := epicChildTicket("ticket-scope", epic.ID, tkmd.StatusReady, nil, []string{"internal/app"})
	mustSaveTicket(t, repoRoot, child)

	adapter := runtimefake.New(
		[]runtime.WorkerResult{
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            "lease-run-scope-ticket-scope-wave-1",
				StartedAt:          epicTestStart(),
				FinishedAt:         epicTestStart().Add(time.Second),
				ResultArtifactPath: filepath.Join(repoRoot, "worker.json"),
			},
		},
		[]runtime.ReviewResult{
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            "lease-run-scope-ticket-scope-wave-1",
				StartedAt:          epicTestStart().Add(2 * time.Second),
				FinishedAt:         epicTestStart().Add(3 * time.Second),
				ReviewStatus:       runtime.ReviewStatusPassed,
				Summary:            "clean",
				ResultArtifactPath: filepath.Join(repoRoot, "review.json"),
			},
		},
	)

	touchOutsideScope := filepath.Join(repoRoot, "out-of-scope.txt")
	childVerification := []string{
		"printf 'violation\\n' > out-of-scope.txt",
	}

	result, err := RunEpic(context.Background(), RunEpicRequest{
		RepoRoot:             repoRoot,
		RunID:                "run-scope",
		RootTicketID:         epic.ID,
		BaseCommit:           baseCommit,
		Adapter:              adapter,
		Config:               cfg,
		VerificationByTicket: map[string][]string{child.ID: childVerification},
	})
	// Scope violation must surface as a non-nil error so callers are not misled.
	if err == nil {
		t.Fatal("expected non-nil error for scope violation, got nil")
	}

	if _, statErr := os.Stat(touchOutsideScope); statErr != nil {
		t.Fatalf("expected scope violation fixture file to exist: %v", statErr)
	}
	// Scope violation must fail closed: the wave should fail and the epic should
	// not complete (G9: scope checks fail closed).
	if len(result.Waves) == 0 {
		t.Fatal("expected at least one wave")
	}
	if result.Waves[0].Status != state.WaveStatusFailed {
		t.Fatalf("expected failed wave for scope violation, got %q", result.Waves[0].Status)
	}
	if result.Run.Status != state.EpicRunStatusBlocked {
		t.Fatalf("expected blocked epic status after scope violation, got %q", result.Run.Status)
	}
}

func TestRunEpicBlockedTicketPreventsFalseCompletion(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	cfg := policy.DefaultConfig()

	epic := epicTicket("epic-blocked")
	mustSaveTicket(t, repoRoot, epic)

	ready := epicChildTicket("ticket-ready", epic.ID, tkmd.StatusReady, nil, []string{"internal/app"})
	mustSaveTicket(t, repoRoot, ready)

	blocked := epicChildTicket("ticket-blocked", epic.ID, tkmd.StatusBlocked, nil, []string{"docs"})
	mustSaveTicket(t, repoRoot, blocked)

	adapter := runtimefake.New(
		[]runtime.WorkerResult{
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            "lease-run-blocked-ticket-ready-wave-1",
				StartedAt:          epicTestStart(),
				FinishedAt:         epicTestStart().Add(time.Second),
				ResultArtifactPath: filepath.Join(repoRoot, "worker.json"),
			},
		},
		[]runtime.ReviewResult{
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            "lease-run-blocked-ticket-ready-wave-1",
				StartedAt:          epicTestStart().Add(2 * time.Second),
				FinishedAt:         epicTestStart().Add(3 * time.Second),
				ReviewStatus:       runtime.ReviewStatusPassed,
				Summary:            "clean",
				ResultArtifactPath: filepath.Join(repoRoot, "review.json"),
			},
		},
	)

	result, err := RunEpic(context.Background(), RunEpicRequest{
		RepoRoot:     repoRoot,
		RunID:        "run-blocked",
		RootTicketID: epic.ID,
		BaseCommit:   baseCommit,
		Adapter:      adapter,
		Config:       cfg,
	})

	// Blocked children must surface as a non-nil error so CLI/API callers
	// are not misled into treating a blocked epic as successful.
	if err == nil {
		t.Fatal("expected non-nil error when epic is blocked, got nil")
	}
	if !errors.Is(err, ErrEpicBlocked) {
		t.Fatalf("expected ErrEpicBlocked, got: %v", err)
	}

	if result.Run.Status == state.EpicRunStatusCompleted {
		t.Fatal("expected blocked child to prevent false completion")
	}
	if result.Run.Status != state.EpicRunStatusBlocked {
		t.Fatalf("expected epic to be blocked, got %q", result.Run.Status)
	}
}

func TestRunEpicDispatchesSameWaveTicketsConcurrently(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	cfg := policy.DefaultConfig()

	root := epicTicket("epic-concurrency")
	mustSaveTicket(t, repoRoot, root)
	mustSaveTicket(t, repoRoot, epicChildTicket("ticket-a", root.ID, tkmd.StatusReady, nil, []string{"internal/app"}))
	mustSaveTicket(t, repoRoot, epicChildTicket("ticket-b", root.ID, tkmd.StatusReady, nil, []string{"docs"}))

	adapter := newBlockingEpicAdapter(t)
	done := make(chan struct {
		result RunEpicResult
		err    error
	}, 1)
	go func() {
		result, err := RunEpic(context.Background(), RunEpicRequest{
			RepoRoot:     repoRoot,
			RunID:        "run-concurrency",
			RootTicketID: root.ID,
			BaseCommit:   baseCommit,
			Adapter:      adapter,
			Config:       cfg,
		})
		done <- struct {
			result RunEpicResult
			err    error
		}{result: result, err: err}
	}()

	waitForStarted(t, adapter.started, 2)
	if got := adapter.maxConcurrent(); got < 2 {
		t.Fatalf("expected concurrent same-wave ticket execution, max active=%d", got)
	}
	close(adapter.release)

	select {
	case outcome := <-done:
		if outcome.err != nil {
			t.Fatalf("RunEpic returned error: %v", outcome.err)
		}
		if outcome.result.Run.Status != state.EpicRunStatusCompleted {
			t.Fatalf("expected completed epic, got %q", outcome.result.Run.Status)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for concurrent epic run to finish")
	}
}

func TestRunEpicSelectsRuntimePerTicket(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	cfg := policy.DefaultConfig()

	root := epicTicket("epic-runtime")
	mustSaveTicket(t, repoRoot, root)
	codexTicket := epicChildTicket("ticket-codex", root.ID, tkmd.StatusReady, nil, []string{"internal/app"})
	codexTicket.Runtime = "codex"
	mustSaveTicket(t, repoRoot, codexTicket)
	claudeTicket := epicChildTicket("ticket-claude", root.ID, tkmd.StatusReady, nil, []string{"docs"})
	claudeTicket.Runtime = "claude"
	mustSaveTicket(t, repoRoot, claudeTicket)

	var mu sync.Mutex
	requestedRuntimes := make([]string, 0, 2)
	adapters := map[string]*runtimefake.Adapter{
		"codex": runtimefake.New(
			[]runtime.WorkerResult{
				{
					Status:             runtime.WorkerStatusDone,
					RetryClass:         runtime.RetryClassTerminal,
					LeaseID:            "lease-run-runtime-ticket-codex-wave-1",
					StartedAt:          epicTestStart(),
					FinishedAt:         epicTestStart().Add(time.Second),
					ResultArtifactPath: filepath.Join(repoRoot, "codex-worker.json"),
				},
				{
					Status:             runtime.WorkerStatusDone,
					RetryClass:         runtime.RetryClassTerminal,
					LeaseID:            "lease-run-runtime-ticket-codex-wave-1",
					StartedAt:          epicTestStart().Add(4 * time.Second),
					FinishedAt:         epicTestStart().Add(5 * time.Second),
					ResultArtifactPath: filepath.Join(repoRoot, "codex-worker-2.json"),
				},
			},
			[]runtime.ReviewResult{
				{
					Status:             runtime.WorkerStatusDone,
					RetryClass:         runtime.RetryClassTerminal,
					LeaseID:            "lease-run-runtime-ticket-codex-wave-1",
					StartedAt:          epicTestStart().Add(2 * time.Second),
					FinishedAt:         epicTestStart().Add(3 * time.Second),
					ReviewStatus:       runtime.ReviewStatusPassed,
					Summary:            "clean",
					ResultArtifactPath: filepath.Join(repoRoot, "codex-review.json"),
				},
				{
					Status:             runtime.WorkerStatusDone,
					RetryClass:         runtime.RetryClassTerminal,
					LeaseID:            "lease-run-runtime-ticket-codex-wave-1",
					StartedAt:          epicTestStart().Add(6 * time.Second),
					FinishedAt:         epicTestStart().Add(7 * time.Second),
					ReviewStatus:       runtime.ReviewStatusPassed,
					Summary:            "clean",
					ResultArtifactPath: filepath.Join(repoRoot, "codex-review-2.json"),
				},
			},
		),
		"claude": runtimefake.New(
			[]runtime.WorkerResult{
				{
					Status:             runtime.WorkerStatusDone,
					RetryClass:         runtime.RetryClassTerminal,
					LeaseID:            "lease-run-runtime-ticket-claude-wave-1",
					StartedAt:          epicTestStart().Add(8 * time.Second),
					FinishedAt:         epicTestStart().Add(9 * time.Second),
					ResultArtifactPath: filepath.Join(repoRoot, "claude-worker.json"),
				},
				{
					Status:             runtime.WorkerStatusDone,
					RetryClass:         runtime.RetryClassTerminal,
					LeaseID:            "lease-run-runtime-ticket-claude-wave-1",
					StartedAt:          epicTestStart().Add(12 * time.Second),
					FinishedAt:         epicTestStart().Add(13 * time.Second),
					ResultArtifactPath: filepath.Join(repoRoot, "claude-worker-2.json"),
				},
			},
			[]runtime.ReviewResult{
				{
					Status:             runtime.WorkerStatusDone,
					RetryClass:         runtime.RetryClassTerminal,
					LeaseID:            "lease-run-runtime-ticket-claude-wave-1",
					StartedAt:          epicTestStart().Add(10 * time.Second),
					FinishedAt:         epicTestStart().Add(11 * time.Second),
					ReviewStatus:       runtime.ReviewStatusPassed,
					Summary:            "clean",
					ResultArtifactPath: filepath.Join(repoRoot, "claude-review.json"),
				},
				{
					Status:             runtime.WorkerStatusDone,
					RetryClass:         runtime.RetryClassTerminal,
					LeaseID:            "lease-run-runtime-ticket-claude-wave-1",
					StartedAt:          epicTestStart().Add(14 * time.Second),
					FinishedAt:         epicTestStart().Add(15 * time.Second),
					ReviewStatus:       runtime.ReviewStatusPassed,
					Summary:            "clean",
					ResultArtifactPath: filepath.Join(repoRoot, "claude-review-2.json"),
				},
			},
		),
	}

	result, err := RunEpic(context.Background(), RunEpicRequest{
		RepoRoot:     repoRoot,
		RunID:        "run-runtime",
		RootTicketID: root.ID,
		BaseCommit:   baseCommit,
		AdapterFactory: func(ticketPreference string) (runtime.Adapter, error) {
			mu.Lock()
			requestedRuntimes = append(requestedRuntimes, ticketPreference)
			mu.Unlock()
			adapter, ok := adapters[ticketPreference]
			if !ok {
				return nil, fmt.Errorf("unexpected runtime preference %q", ticketPreference)
			}
			return adapter, nil
		},
		Config: cfg,
	})
	if err != nil {
		t.Fatalf("RunEpic returned error: %v", err)
	}
	if result.Run.Status != state.EpicRunStatusCompleted {
		t.Fatalf("expected completed epic, got %q", result.Run.Status)
	}

	mu.Lock()
	got := append([]string(nil), requestedRuntimes...)
	mu.Unlock()
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"claude", "claude", "codex"}) {
		t.Fatalf("unexpected requested runtimes: %#v", requestedRuntimes)
	}
}

// TestRunEpicFailsOnScopeViolation is a focused regression test for the bug
// where RunEpic persisted a blocked run state but returned nil, allowing callers
// to treat acceptance failures as success.  When AcceptWave rejects a wave
// (here: scope violation) and waveFailed is false (no worker errors), RunEpic
// must return a non-nil error that matches the persisted blocked state.
func TestRunEpicFailsOnScopeViolation(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	cfg := policy.DefaultConfig()

	epic := epicTicket("epic-failscope")
	mustSaveTicket(t, repoRoot, epic)

	// Ticket with a narrow scope so that writing an unrelated file triggers a violation.
	child := epicChildTicket("ticket-failscope", epic.ID, tkmd.StatusReady, nil, []string{"internal/app"})
	mustSaveTicket(t, repoRoot, child)

	adapter := runtimefake.New(
		[]runtime.WorkerResult{
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            "lease-run-failscope-ticket-failscope-wave-1",
				StartedAt:          epicTestStart(),
				FinishedAt:         epicTestStart().Add(time.Second),
				ResultArtifactPath: filepath.Join(repoRoot, "worker-failscope.json"),
			},
		},
		[]runtime.ReviewResult{
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            "lease-run-failscope-ticket-failscope-wave-1",
				StartedAt:          epicTestStart().Add(2 * time.Second),
				FinishedAt:         epicTestStart().Add(3 * time.Second),
				ReviewStatus:       runtime.ReviewStatusPassed,
				Summary:            "clean",
				ResultArtifactPath: filepath.Join(repoRoot, "review-failscope.json"),
			},
		},
	)

	// The worker succeeds (no outcome.err), but the verification step touches a
	// file outside the ticket's declared scope.  AcceptWave will detect this
	// and return a non-nil acceptErr while waveFailed remains false — the exact
	// condition that caused the original nil-return bug.
	scopeViolationCmd := []string{"printf 'violation\\n' > failscope-out-of-scope.txt"}

	result, err := RunEpic(context.Background(), RunEpicRequest{
		RepoRoot:             repoRoot,
		RunID:                "run-failscope",
		RootTicketID:         epic.ID,
		BaseCommit:           baseCommit,
		Adapter:              adapter,
		Config:               cfg,
		VerificationByTicket: map[string][]string{child.ID: scopeViolationCmd},
	})

	// Core regression assertion: RunEpic must not return nil when the persisted
	// run state is blocked.
	if err == nil {
		t.Fatal("RunEpic returned nil error despite AcceptWave blocking the run; " +
			"persisted blocked state diverges from returned nil — regression of ver-z6em")
	}

	// The persisted state and the returned state must agree.
	if result.Run.Status != state.EpicRunStatusBlocked {
		t.Fatalf("expected persisted run status %q, got %q", state.EpicRunStatusBlocked, result.Run.Status)
	}
}

func TestRunEpicIgnoresBaselineDirtyFilesInScopeChecks(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	cfg := policy.DefaultConfig()

	baselinePath := filepath.Join(repoRoot, "baseline-dirty.txt")
	if err := os.WriteFile(baselinePath, []byte("dirty baseline\n"), 0o644); err != nil {
		t.Fatalf("write baseline file: %v", err)
	}

	root := epicTicket("epic-baseline")
	mustSaveTicket(t, repoRoot, root)
	mustSaveTicket(t, repoRoot, epicChildTicket("ticket-baseline", root.ID, tkmd.StatusReady, nil, []string{"internal/app"}))

	adapter := runtimefake.New(
		[]runtime.WorkerResult{
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            "lease-run-baseline-ticket-baseline-wave-1",
				StartedAt:          epicTestStart(),
				FinishedAt:         epicTestStart().Add(time.Second),
				ResultArtifactPath: filepath.Join(repoRoot, "worker-baseline.json"),
			},
		},
		[]runtime.ReviewResult{
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            "lease-run-baseline-ticket-baseline-wave-1",
				StartedAt:          epicTestStart().Add(2 * time.Second),
				FinishedAt:         epicTestStart().Add(3 * time.Second),
				ReviewStatus:       runtime.ReviewStatusPassed,
				Summary:            "clean",
				ResultArtifactPath: filepath.Join(repoRoot, "review-baseline.json"),
			},
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            "epic-review-epic-baseline-1",
				StartedAt:          epicTestStart().Add(4 * time.Second),
				FinishedAt:         epicTestStart().Add(5 * time.Second),
				ReviewStatus:       runtime.ReviewStatusPassed,
				Summary:            "epic gate clean",
				ResultArtifactPath: filepath.Join(repoRoot, "review-baseline-epic.json"),
			},
		},
	)

	result, err := RunEpic(context.Background(), RunEpicRequest{
		RepoRoot:     repoRoot,
		RunID:        "run-baseline",
		RootTicketID: root.ID,
		BaseCommit:   baseCommit,
		Adapter:      adapter,
		Config:       cfg,
	})
	if err != nil {
		t.Fatalf("RunEpic returned error: %v", err)
	}
	if result.Run.Status != state.EpicRunStatusCompleted {
		t.Fatalf("expected completed epic despite dirty baseline, got %q", result.Run.Status)
	}
	if len(result.Waves) != 1 {
		t.Fatalf("expected one wave, got %d", len(result.Waves))
	}
	raw, ok := result.Waves[0].Acceptance["baseline_changed_files"].([]string)
	if !ok {
		t.Fatalf("expected baseline_changed_files acceptance entry, got %#v", result.Waves[0].Acceptance["baseline_changed_files"])
	}
	if !reflect.DeepEqual(raw, []string{"baseline-dirty.txt"}) {
		t.Fatalf("expected baseline file to be recorded, got %#v", raw)
	}
}

func initEpicRepo(t *testing.T, root string) string {
	t.Helper()

	mustRunGit(t, root, "init")
	mustRunGit(t, root, "config", "user.email", "test@example.com")
	mustRunGit(t, root, "config", "user.name", "Test User")

	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	mustRunGit(t, root, "add", "tracked.txt")
	mustRunGit(t, root, "commit", "-m", "base")

	head, err := gitOutput(root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}
	return strings.TrimSpace(head)
}

func mustSaveTicket(t *testing.T, repoRoot string, ticket tkmd.Ticket) {
	t.Helper()

	if err := tkmd.SaveTicket(filepath.Join(repoRoot, ".tickets", ticket.ID+".md"), ticket); err != nil {
		t.Fatalf("SaveTicket(%s): %v", ticket.ID, err)
	}
}

func epicTicket(id string) tkmd.Ticket {
	return tkmd.Ticket{
		ID:     id,
		Title:  "Epic " + id,
		Status: tkmd.StatusReady,
		OwnedPaths: []string{
			"internal/app",
			"docs",
		},
		UnknownFrontmatter: map[string]any{
			"type": "epic",
		},
	}
}

func epicChildTicket(id, parent string, status tkmd.Status, deps, owned []string) tkmd.Ticket {
	return tkmd.Ticket{
		ID:         id,
		Title:      "Child " + id,
		Status:     status,
		Deps:       deps,
		OwnedPaths: append([]string(nil), owned...),
		UnknownFrontmatter: map[string]any{
			"parent": parent,
			"type":   "task",
		},
	}
}

func epicTestStart() time.Time {
	return time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC)
}

type blockingEpicAdapter struct {
	mu sync.Mutex

	started chan string
	release chan struct{}

	activeWorkers int
	maxActive     int
}

func newBlockingEpicAdapter(t *testing.T) *blockingEpicAdapter {
	t.Helper()
	return &blockingEpicAdapter{
		started: make(chan string, 4),
		release: make(chan struct{}),
	}
}

func (a *blockingEpicAdapter) RunWorker(ctx context.Context, req runtime.WorkerRequest) (runtime.WorkerResult, error) {
	a.mu.Lock()
	a.activeWorkers++
	if a.activeWorkers > a.maxActive {
		a.maxActive = a.activeWorkers
	}
	a.mu.Unlock()

	select {
	case a.started <- req.TicketID:
	case <-ctx.Done():
		return runtime.WorkerResult{}, ctx.Err()
	}

	select {
	case <-a.release:
	case <-ctx.Done():
		return runtime.WorkerResult{}, ctx.Err()
	}

	a.mu.Lock()
	a.activeWorkers--
	a.mu.Unlock()

	start := epicTestStart()
	return runtime.WorkerResult{
		Status:             runtime.WorkerStatusDone,
		RetryClass:         runtime.RetryClassTerminal,
		LeaseID:            req.LeaseID,
		StartedAt:          start,
		FinishedAt:         start.Add(time.Second),
		ResultArtifactPath: filepath.Join("artifacts", req.TicketID+".json"),
	}, nil
}

func (a *blockingEpicAdapter) RunReviewer(ctx context.Context, req runtime.ReviewRequest) (runtime.ReviewResult, error) {
	if err := ctx.Err(); err != nil {
		return runtime.ReviewResult{}, err
	}
	start := epicTestStart().Add(2 * time.Second)
	return runtime.ReviewResult{
		Status:             runtime.WorkerStatusDone,
		RetryClass:         runtime.RetryClassTerminal,
		LeaseID:            req.LeaseID,
		StartedAt:          start,
		FinishedAt:         start.Add(time.Second),
		ReviewStatus:       runtime.ReviewStatusPassed,
		Summary:            "clean",
		ResultArtifactPath: filepath.Join("artifacts", req.TicketID+"-review.json"),
	}, nil
}

func (a *blockingEpicAdapter) maxConcurrent() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.maxActive
}

func waitForStarted(t *testing.T, started <-chan string, want int) []string {
	t.Helper()
	got := make([]string, 0, want)
	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()
	for len(got) < want {
		select {
		case ticketID := <-started:
			got = append(got, ticketID)
		case <-timeout.C:
			t.Fatalf("timed out waiting for %d started tickets, got %v", want, got)
		}
	}
	return got
}

func mustRunGit(t *testing.T, dir string, args ...string) {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
	}
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func TestRunEpicConcurrentLockContention(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	cfg := policy.DefaultConfig()

	epic := epicTicket("epic-lock")
	mustSaveTicket(t, repoRoot, epic)
	child := epicChildTicket("ticket-lock", epic.ID, tkmd.StatusReady, nil, []string{"internal/app"})
	mustSaveTicket(t, repoRoot, child)

	// Use a slow adapter so the first RunEpic holds the lock long enough
	// for the second to attempt acquisition.
	slowAdapter := newBlockingEpicAdapter(t)

	runID := "run-lock-contention"

	// Pre-acquire the lock to simulate a running process.
	lock, err := AcquireRunLock(repoRoot, runID)
	if err != nil {
		t.Fatalf("pre-acquire lock: %v", err)
	}

	// Second RunEpic with same run ID should fail immediately.
	_, err = RunEpic(context.Background(), RunEpicRequest{
		RepoRoot:     repoRoot,
		RunID:        runID,
		RootTicketID: epic.ID,
		BaseCommit:   baseCommit,
		Adapter:      slowAdapter,
		Config:       cfg,
	})
	if err == nil {
		t.Fatal("expected concurrent RunEpic to fail with lock error")
	}
	if !strings.Contains(err.Error(), "already being executed") {
		t.Fatalf("expected lock contention error, got: %v", err)
	}

	_ = lock.Release()
}

func writeClaimJSON(t *testing.T, path string, claim state.ClaimArtifact) {
	t.Helper()
	data, err := json.Marshal(claim)
	if err != nil {
		t.Fatalf("marshal claim: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write claim file: %v", err)
	}
}

func TestWaveClaimsReleased(t *testing.T) {
	t.Run("missing claim file returns false nil", func(t *testing.T) {
		dir := t.TempDir()
		got, err := waveClaimsReleased(dir, "run-1", []string{"ticket-a"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got {
			t.Fatal("expected false for missing claim file")
		}
	})

	t.Run("present but unreleased returns false nil", func(t *testing.T) {
		dir := t.TempDir()
		claimsDir := filepath.Join(dir, ".verk", "runs", "run-1", "claims")
		if err := os.MkdirAll(claimsDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		writeClaimJSON(t, filepath.Join(claimsDir, "claim-ticket-a.json"), state.ClaimArtifact{State: "active"})
		got, err := waveClaimsReleased(dir, "run-1", []string{"ticket-a"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got {
			t.Fatal("expected false for unreleased claim")
		}
	})

	t.Run("present and released returns true nil", func(t *testing.T) {
		dir := t.TempDir()
		claimsDir := filepath.Join(dir, ".verk", "runs", "run-1", "claims")
		if err := os.MkdirAll(claimsDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		writeClaimJSON(t, filepath.Join(claimsDir, "claim-ticket-a.json"), state.ClaimArtifact{State: "released"})
		got, err := waveClaimsReleased(dir, "run-1", []string{"ticket-a"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !got {
			t.Fatal("expected true for released claim")
		}
	})

	t.Run("malformed JSON returns false with error", func(t *testing.T) {
		dir := t.TempDir()
		claimsDir := filepath.Join(dir, ".verk", "runs", "run-1", "claims")
		if err := os.MkdirAll(claimsDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(claimsDir, "claim-ticket-a.json"), []byte("not json{{{"), 0o644); err != nil {
			t.Fatalf("write bad claim: %v", err)
		}
		got, err := waveClaimsReleased(dir, "run-1", []string{"ticket-a"})
		if err == nil {
			t.Fatal("expected error for malformed JSON")
		}
		if got {
			t.Fatal("expected false when error occurs")
		}
	})
}

func TestRunEpicRecursesIntoSubTickets(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	cfg := policy.DefaultConfig()
	cfg.Scheduler.MaxDepth = 3

	// Epic -> ticket-1 (has children) -> sub-1, sub-2
	// Epic -> ticket-2 (leaf)
	epic := epicTicket("epic-sub")
	mustSaveTicket(t, repoRoot, epic)

	ticket1 := epicChildTicket("ticket-1", epic.ID, tkmd.StatusOpen, nil, []string{"internal/app"})
	mustSaveTicket(t, repoRoot, ticket1)

	ticket2 := epicChildTicket("ticket-2", epic.ID, tkmd.StatusOpen, nil, []string{"docs"})
	mustSaveTicket(t, repoRoot, ticket2)

	sub1 := epicChildTicket("sub-1", ticket1.ID, tkmd.StatusOpen, nil, []string{"internal/app/sub1"})
	mustSaveTicket(t, repoRoot, sub1)

	sub2 := epicChildTicket("sub-2", ticket1.ID, tkmd.StatusOpen, nil, []string{"internal/app/sub2"})
	mustSaveTicket(t, repoRoot, sub2)

	adapter := newReflectingAdapter(4)

	result, err := RunEpic(context.Background(), RunEpicRequest{
		RepoRoot:     repoRoot,
		RunID:        "run-sub",
		RootTicketID: epic.ID,
		BaseCommit:   baseCommit,
		Adapter:      adapter,
		Config:       cfg,
	})

	// All tickets including sub-tickets should have been dispatched
	workerReqs := adapter.WorkerRequests()
	startedSet := make(map[string]bool)
	for _, req := range workerReqs {
		startedSet[req.TicketID] = true
	}
	for _, id := range []string{"sub-1", "sub-2", "ticket-1", "ticket-2"} {
		if !startedSet[id] {
			t.Errorf("expected ticket %q to be started, got started: %v", id, keys(startedSet))
		}
	}

	// Epic should complete since all tickets pass
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Run.Status != state.EpicRunStatusCompleted {
		t.Fatalf("expected completed epic, got status %s", result.Run.Status)
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestRunEpicRespectsMaxDepth(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	cfg := policy.DefaultConfig()
	cfg.Scheduler.MaxDepth = 1 // Only 1 level: epic's direct children

	// Epic -> ticket-1 -> sub-1
	// With MaxDepth=1, sub-1 should NOT be run as a sub-epic;
	// ticket-1 runs as a flat ticket (no recursion into its children).
	epic := epicTicket("epic-depth")
	mustSaveTicket(t, repoRoot, epic)

	ticket1 := epicChildTicket("ticket-1", epic.ID, tkmd.StatusOpen, nil, []string{"internal/app"})
	mustSaveTicket(t, repoRoot, ticket1)

	sub1 := epicChildTicket("sub-1", ticket1.ID, tkmd.StatusOpen, nil, []string{"internal/app/sub"})
	mustSaveTicket(t, repoRoot, sub1)

	// Only 1 worker + 1 review needed: just ticket-1 (sub-1 is skipped by MaxDepth=1)
	adapter := newReflectingAdapter(1)

	_, err := RunEpic(context.Background(), RunEpicRequest{
		RepoRoot:     repoRoot,
		RunID:        "run-depth",
		RootTicketID: epic.ID,
		BaseCommit:   baseCommit,
		Adapter:      adapter,
		Config:       cfg,
	})

	// sub-1 should NOT have been started because MaxDepth=1 prevents recursion
	workerReqs := adapter.WorkerRequests()
	for _, req := range workerReqs {
		if req.TicketID == "sub-1" {
			t.Error("sub-1 should not have been started with MaxDepth=1")
		}
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunSubEpicBasic(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	cfg := policy.DefaultConfig()
	cfg.Scheduler.MaxDepth = 3

	// ticket-1 has children: sub-1, sub-2
	ticket1 := epicChildTicket("ticket-1", "epic-parent", tkmd.StatusOpen, nil, []string{"internal/app"})
	mustSaveTicket(t, repoRoot, ticket1)

	sub1 := epicChildTicket("sub-1", ticket1.ID, tkmd.StatusOpen, nil, []string{"internal/app/sub1"})
	mustSaveTicket(t, repoRoot, sub1)

	sub2 := epicChildTicket("sub-2", ticket1.ID, tkmd.StatusOpen, nil, []string{"internal/app/sub2"})
	mustSaveTicket(t, repoRoot, sub2)

	adapter := newReflectingAdapter(2)

	wave := state.WaveArtifact{
		WaveID:         "wave-1",
		Ordinal:        1,
		Status:         state.WaveStatusRunning,
		WaveBaseCommit: baseCommit,
		StartedAt:      epicTestStart(),
	}

	req := RunEpicRequest{
		RepoRoot:     repoRoot,
		RunID:        "run-sub-epic",
		RootTicketID: "epic-parent",
		BaseCommit:   baseCommit,
		Adapter:      adapter,
		Config:       cfg,
	}

	outcome := runSubEpic(context.Background(), req, cfg, ticket1.ID, wave, 1, nil, nil)

	t.Logf("outcome: ticketID=%s phase=%s err=%v", outcome.ticketID, outcome.phase, outcome.err)
	if outcome.err != nil {
		t.Fatalf("runSubEpic failed: %v", outcome.err)
	}
	if outcome.phase != state.TicketPhaseClosed {
		t.Fatalf("expected closed, got %s", outcome.phase)
	}

	// Both sub-tickets should have been dispatched
	workerReqs := adapter.WorkerRequests()
	startedSet := make(map[string]bool)
	for _, req := range workerReqs {
		startedSet[req.TicketID] = true
	}
	for _, id := range []string{"sub-1", "sub-2"} {
		if !startedSet[id] {
			t.Errorf("expected ticket %q to be started, got %v", id, keys(startedSet))
		}
	}
}

// TestRunEpicSubWaveVerificationFailureBlocks verifies that when wave-level
// quality commands fail for a sub-wave, the parent epic blocks instead of
// silently accepting nested work. This guards against sub-epic behavior being
// weaker than top-level waves (no verification gate).
//
// We use a counter-based quality command that passes the first time (so the
// single sub-ticket's ticket-level verification succeeds) and fails on all
// subsequent calls (so the wave-level verification for the sub-wave fails).
func TestRunEpicSubWaveVerificationFailureBlocks(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	cfg := policy.DefaultConfig()
	cfg.Scheduler.MaxDepth = 3
	// Disable wave repair so verification failure is immediate and deterministic.
	cfg.Policy.MaxWaveRepairCycles = 0

	counterFile := filepath.Join(repoRoot, ".verk", "verify-counter")
	// Use /bin/sh: pass on first invocation, fail on any later invocation.
	// Sub-wave verification is the 2nd call after the grandchild's
	// ticket-level verification succeeds.
	script := fmt.Sprintf(
		"COUNT=$(cat %s 2>/dev/null || echo 0); NEXT=$((COUNT+1)); printf %%s $NEXT > %s; if [ \"$COUNT\" -ge 1 ]; then exit 1; fi",
		counterFile, counterFile,
	)
	cfg.Verification.QualityCommands = []policy.QualityCommand{{Path: ".", Run: []string{script}}}

	epic := epicTicket("epic-subwave-verify-fail")
	mustSaveTicket(t, repoRoot, epic)

	parentTask := epicChildTicket("parent-verify-fail", epic.ID, tkmd.StatusOpen, nil, []string{"internal/app"})
	mustSaveTicket(t, repoRoot, parentTask)

	grandchild := epicChildTicket("grandchild-verify-fail", parentTask.ID, tkmd.StatusOpen, nil, []string{"internal/app/sub"})
	mustSaveTicket(t, repoRoot, grandchild)

	adapter := newReflectingAdapter(2)

	runID := "run-subwave-verify-fail"
	result, err := RunEpic(context.Background(), RunEpicRequest{
		RepoRoot:     repoRoot,
		RunID:        runID,
		RootTicketID: epic.ID,
		BaseCommit:   baseCommit,
		Adapter:      adapter,
		Config:       cfg,
	})
	if err == nil {
		t.Fatal("expected error when sub-wave verification fails, got nil")
	}

	if result.Run.Status != state.EpicRunStatusBlocked {
		t.Fatalf("expected blocked run after sub-wave verification failure, got %q", result.Run.Status)
	}

	subWaveID := fmt.Sprintf("sub-%s-wave-1", parentTask.ID)
	var subWave state.WaveArtifact
	if err := state.LoadJSON(waveArtifactPath(repoRoot, runID, subWaveID), &subWave); err != nil {
		t.Fatalf("load sub-wave artifact: %v", err)
	}
	if got, ok := subWave.Acceptance["wave_verification_passed"].(bool); !ok || got {
		t.Fatalf("expected sub-wave verification_passed=false, acceptance=%v", subWave.Acceptance)
	}
}

// TestRunSubEpicClaimCheckErrorBlocksParent verifies that if the sub-wave's
// claim-release check returns an error (e.g., corrupted claim artifact), the
// sub-epic returns a blocked outcome with the underlying error rather than
// silently accepting the sub-wave.
func TestRunSubEpicClaimCheckErrorBlocksParent(t *testing.T) {
	repoRoot := t.TempDir()
	runID := "run-subwave-claim-err"

	// Seed an unparseable claim artifact in the claims directory so
	// waveClaimsReleased returns an error on load. This mirrors what a
	// corrupted on-disk claim would look like between sub-ticket completion
	// and the parent-level acceptance gate.
	claimDir := filepath.Join(repoRoot, ".verk", "runs", runID, "claims")
	if err := os.MkdirAll(claimDir, 0o755); err != nil {
		t.Fatalf("mkdir claims: %v", err)
	}
	ticketID := "grandchild-claim-check"
	if err := os.WriteFile(filepath.Join(claimDir, "claim-"+ticketID+".json"), []byte("not-json"), 0o644); err != nil {
		t.Fatalf("seed corrupt claim: %v", err)
	}

	// Directly exercise the helper to confirm a corrupt claim surfaces as an
	// error (not (false, nil)), which is what the sub-epic gate relies on to
	// block the parent ticket.
	ok, err := waveClaimsReleased(repoRoot, runID, []string{ticketID})
	if err == nil {
		t.Fatal("expected error from waveClaimsReleased with corrupt claim, got nil")
	}
	if ok {
		t.Fatalf("expected claimsReleased=false on error, got %v", ok)
	}
}

// TestRunEpicBlockedGrandchildTerminatesRun is a regression test for the bug
// where a blocked descendant left the parent sub-epic ticket in open/ready state,
// causing the outer RunEpic loop to reschedule the same parent repeatedly and
// create accepted waves forever without making progress.
//
// Topology: epic -> parent-task (has children) -> grandchild
// The grandchild's worker returns needs_context, which becomes TicketPhaseBlocked.
// The fix: executeEpicTicket persists blocked state on the parent ticket so the
// outer loop cannot reschedule it.
func TestRunEpicBlockedGrandchildTerminatesRun(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	cfg := policy.DefaultConfig()
	cfg.Scheduler.MaxDepth = 3

	epic := epicTicket("epic-gc-blocked")
	mustSaveTicket(t, repoRoot, epic)

	// parent-task has children, so it triggers the sub-epic path in executeEpicTicket.
	parentTask := epicChildTicket("parent-task", epic.ID, tkmd.StatusOpen, nil, []string{"internal/app"})
	mustSaveTicket(t, repoRoot, parentTask)

	// grandchild is a leaf; its worker will return needs_context (blocked).
	grandchild := epicChildTicket("grandchild", parentTask.ID, tkmd.StatusOpen, nil, []string{"internal/app/sub"})
	mustSaveTicket(t, repoRoot, grandchild)

	// Script the grandchild worker to return needs_context. The reflectingAdapter
	// overrides LeaseID from the request, so no pre-computed lease ID is needed.
	adapter := &reflectingAdapter{
		workerResults: []runtime.WorkerResult{
			{
				Status:             runtime.WorkerStatusNeedsContext,
				RetryClass:         runtime.RetryClassBlockedByOperatorInput,
				LeaseID:            "placeholder", // overwritten by reflectingAdapter.RunWorker
				StartedAt:          epicTestStart(),
				FinishedAt:         epicTestStart().Add(time.Second),
				ResultArtifactPath: "artifact.json",
			},
		},
		reviewResults: []runtime.ReviewResult{}, // no review: grandchild blocked at implement
	}

	result, err := RunEpic(context.Background(), RunEpicRequest{
		RepoRoot:     repoRoot,
		RunID:        "run-gc-blocked",
		RootTicketID: epic.ID,
		BaseCommit:   baseCommit,
		Adapter:      adapter,
		Config:       cfg,
	})

	// RunEpic must terminate with ErrEpicBlocked, not loop forever.
	if err == nil {
		t.Fatal("expected ErrEpicBlocked, got nil error — regression: blocked grandchild did not stop the run")
	}
	if !errors.Is(err, ErrEpicBlocked) {
		t.Fatalf("expected ErrEpicBlocked, got: %v", err)
	}

	// The parent sub-epic ticket must be persisted as blocked so it cannot be
	// rescheduled in a future wave for the same run.
	parentTicket, loadErr := loadEpicTicket(repoRoot, parentTask.ID)
	if loadErr != nil {
		t.Fatalf("load parent-task ticket: %v", loadErr)
	}
	if parentTicket.Status != tkmd.StatusBlocked {
		t.Fatalf("expected parent-task to be blocked in ticket store, got %q — "+
			"parent will be rescheduled until it is explicitly marked blocked", parentTicket.Status)
	}

	// Only one wave must have been created: parent-task must not be rescheduled
	// after its grandchild blocked. Repeated accepted waves for the same parent
	// indicate the looping regression is still present.
	if len(result.Waves) != 1 {
		t.Fatalf("expected exactly 1 wave (parent not rescheduled after grandchild blocked), "+
			"got %d waves — regression: same blocked parent accepted into multiple waves", len(result.Waves))
	}
}

// TestRunEpicContextCancellation verifies that cancelling the context passed to
// RunEpic causes the run to terminate promptly rather than hanging until workers
// finish normally. This mirrors the behaviour triggered by Ctrl-C / SIGINT at
// the CLI layer (which calls signal.NotifyContext → cancel).
func TestRunEpicContextCancellation(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	cfg := policy.DefaultConfig()

	epic := epicTicket("epic-ctx-cancel")
	mustSaveTicket(t, repoRoot, epic)
	child := epicChildTicket("ticket-ctx-cancel", epic.ID, tkmd.StatusReady, nil, []string{"internal/app"})
	mustSaveTicket(t, repoRoot, child)

	// blockingEpicAdapter blocks inside RunWorker until the context is cancelled
	// or adapter.release is closed, so it faithfully simulates a long-running
	// worker process (e.g. a Claude worker waiting for MCP calls to complete).
	adapter := newBlockingEpicAdapter(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type epicResult struct {
		r   RunEpicResult
		err error
	}
	done := make(chan epicResult, 1)
	go func() {
		r, err := RunEpic(ctx, RunEpicRequest{
			RepoRoot:     repoRoot,
			RunID:        "run-ctx-cancel",
			RootTicketID: epic.ID,
			BaseCommit:   baseCommit,
			Adapter:      adapter,
			Config:       cfg,
		})
		done <- epicResult{r, err}
	}()

	// Wait until the blocking worker has actually started before cancelling so
	// the test exercises the in-flight cancellation path, not a pre-start check.
	waitForStarted(t, adapter.started, 1)

	// Cancel the context — RunWorker should unblock immediately and RunEpic
	// should propagate the cancellation and return.
	cancel()

	// RunEpic must return within a short deadline. A hang here indicates that
	// context cancellation does not propagate to in-flight workers.
	select {
	case res := <-done:
		if res.err == nil {
			t.Fatal("expected error after context cancellation, got nil")
		}
		t.Logf("RunEpic returned on cancellation (expected): %v", res.err)
	case <-time.After(10 * time.Second):
		t.Fatal("RunEpic did not return after context cancellation — workers may be ignoring ctx.Done()")
	}
}

// TestRunEpicLinkedSiblingsNotEachOthersChildren is an engine-level regression
// test for the bug where peer tk links between siblings were treated as child
// edges by recursive epic execution. When sib-a links to sib-b and sib-b links
// to sib-a, neither should be discovered as a child of the other. The parent
// epic must complete in a single wave with both siblings dispatched as flat
// (non-sub-epic) tickets.
func TestRunEpicLinkedSiblingsNotEachOthersChildren(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	cfg := policy.DefaultConfig()
	cfg.Scheduler.MaxDepth = 3

	epic := epicTicket("epic-linked-sibs")
	mustSaveTicket(t, repoRoot, epic)

	// Two siblings that cross-link each other via the tk links field.
	// Links are stored in UnknownFrontmatter since Ticket has no native Links field.
	sibA := tkmd.Ticket{
		ID:         "sib-linked-a",
		Title:      "Sibling A",
		Status:     tkmd.StatusOpen,
		OwnedPaths: []string{"internal/app/sib-a"},
		UnknownFrontmatter: map[string]any{
			"parent": epic.ID,
			"type":   "task",
			"links":  []any{"sib-linked-b"},
		},
	}
	mustSaveTicket(t, repoRoot, sibA)

	sibB := tkmd.Ticket{
		ID:         "sib-linked-b",
		Title:      "Sibling B",
		Status:     tkmd.StatusOpen,
		OwnedPaths: []string{"internal/app/sib-b"},
		UnknownFrontmatter: map[string]any{
			"parent": epic.ID,
			"type":   "task",
			"links":  []any{"sib-linked-a"},
		},
	}
	mustSaveTicket(t, repoRoot, sibB)

	// Two workers: one for each sibling. No review results needed to keep the
	// test simple — both return WorkerStatusDone so the epic completes.
	adapter := newReflectingAdapter(2)

	result, err := RunEpic(context.Background(), RunEpicRequest{
		RepoRoot:     repoRoot,
		RunID:        "run-linked-sibs",
		RootTicketID: epic.ID,
		BaseCommit:   baseCommit,
		Adapter:      adapter,
		Config:       cfg,
	})
	if err != nil {
		t.Fatalf("RunEpic failed: %v — regression: linked siblings caused repeated waves or cycle", err)
	}
	if result.Run.Status != state.EpicRunStatusCompleted {
		t.Fatalf("expected epic to complete, got status %q", result.Run.Status)
	}

	// Both siblings must have been dispatched exactly once.
	workerReqs := adapter.WorkerRequests()
	dispatched := make(map[string]int)
	for _, req := range workerReqs {
		dispatched[req.TicketID]++
	}
	for _, id := range []string{"sib-linked-a", "sib-linked-b"} {
		if dispatched[id] != 1 {
			t.Errorf("expected %s dispatched exactly once, got %d — linked sibling treated as sub-epic child", id, dispatched[id])
		}
	}

	// Exactly one wave: no repeated waves from sibling-as-child recursion.
	if len(result.Waves) != 1 {
		t.Fatalf("expected exactly 1 wave, got %d — regression: linked siblings caused repeated wave execution", len(result.Waves))
	}
}

// workerTicketIDs extracts the TicketID from each WorkerRequest for readable assertions.
func workerTicketIDs(reqs []runtime.WorkerRequest) []string {
	ids := make([]string, len(reqs))
	for i, req := range reqs {
		ids[i] = req.TicketID
	}
	return ids
}

// TestResumeRun_InProgressDescendantIsResumed verifies that a descendant ticket
// which was in_progress when the prior run crashed is reset to ready and re-executed
// by the resume path. The sub-wave artifact persisted by runSubEpic lets
// loadRunArtifacts find the wave; the descendant's presence in run.TicketIDs
// ensures the reset loop resets it before the wave loop re-enters the sub-epic.
func TestResumeRun_InProgressDescendantIsResumed(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	runID := "run-desc-inprog"

	// Hierarchy: epic -> parent-task -> grandchild
	epic := epicTicket("epic-desc-inprog")
	mustSaveTicket(t, repoRoot, epic)

	parentTask := epicChildTicket("parent-task-inprog", epic.ID, tkmd.StatusInProgress, nil, []string{"internal/app"})
	mustSaveTicket(t, repoRoot, parentTask)

	grandchild := epicChildTicket("grandchild-inprog", parentTask.ID, tkmd.StatusInProgress, nil, []string{"internal/app/sub"})
	mustSaveTicket(t, repoRoot, grandchild)

	// Simulate crash mid-sub-wave: grandchild added to TicketIDs by registerSubWave,
	// sub-wave persisted to WaveIDs, but the top-level wave-1 not yet accepted.
	subWaveID := fmt.Sprintf("sub-%s-wave-1", parentTask.ID)
	writeOpRunFixture(t, repoRoot, runID, state.RunArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		Mode:         "epic",
		RootTicketID: epic.ID,
		Status:       state.EpicRunStatusRunning,
		CurrentPhase: state.TicketPhaseImplement,
		BaseCommit:   baseCommit,
		TicketIDs:    []string{parentTask.ID, grandchild.ID},
		WaveIDs:      []string{subWaveID},
		ResumeCursor: map[string]any{"wave_ordinal": 0},
	})
	writeTicketRunFixture(t, repoRoot, runID, TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:     parentTask.ID,
		CurrentPhase: state.TicketPhaseImplement,
	})
	writeTicketRunFixture(t, repoRoot, runID, TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:     grandchild.ID,
		CurrentPhase: state.TicketPhaseImplement,
	})
	writePlanFixture(t, repoRoot, runID, state.PlanArtifact{
		ArtifactMeta:             state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:                 parentTask.ID,
		EffectiveReviewThreshold: state.SeverityP2,
	})
	writePlanFixture(t, repoRoot, runID, state.PlanArtifact{
		ArtifactMeta:             state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:                 grandchild.ID,
		EffectiveReviewThreshold: state.SeverityP2,
	})
	// Sub-wave artifact persisted by runSubEpic before crash.
	writeWaveFixture(t, repoRoot, runID, state.WaveArtifact{
		WaveID:         subWaveID,
		ParentTicketID: parentTask.ID,
		Status:         state.WaveStatusRunning,
		TicketIDs:      []string{grandchild.ID},
	})

	// Adapter returns needs_context for grandchild so execution terminates
	// predictably without requiring full review/verification machinery.
	adapter := &reflectingAdapter{
		workerResults: []runtime.WorkerResult{
			{
				Status:             runtime.WorkerStatusNeedsContext,
				RetryClass:         runtime.RetryClassBlockedByOperatorInput,
				LeaseID:            "placeholder",
				StartedAt:          epicTestStart(),
				FinishedAt:         epicTestStart().Add(time.Second),
				ResultArtifactPath: "artifact.json",
			},
		},
		reviewResults: []runtime.ReviewResult{},
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

	// Grandchild must have been dispatched: the resume path must have re-entered
	// the sub-epic loop for parent-task and scheduled the in-progress grandchild.
	reqs := adapter.WorkerRequests()
	dispatched := false
	for _, req := range reqs {
		if req.TicketID == grandchild.ID {
			dispatched = true
			break
		}
	}
	if !dispatched {
		t.Errorf("expected in-progress grandchild %q to be dispatched on resume, got dispatched: %v",
			grandchild.ID, workerTicketIDs(reqs))
	}

	// Run ends blocked because grandchild returned needs_context.
	if report.Run.Status != state.EpicRunStatusBlocked {
		t.Errorf("expected blocked run after grandchild needs_context, got %q", report.Run.Status)
	}
}

// TestResumeRun_BlockedDescendantIsResumed verifies that a descendant ticket
// that was blocked when the prior run stopped is reset to ready and re-executed
// by the resume path. Blocked is not a terminal phase for resume purposes — the
// engine treats it as "needs another attempt" and resets the ticket to open.
func TestResumeRun_BlockedDescendantIsResumed(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	runID := "run-desc-blocked"

	// Hierarchy: epic -> parent-task -> grandchild (blocked)
	epic := epicTicket("epic-desc-blocked")
	mustSaveTicket(t, repoRoot, epic)

	parentTask := epicChildTicket("parent-task-blocked", epic.ID, tkmd.StatusBlocked, nil, []string{"internal/app"})
	mustSaveTicket(t, repoRoot, parentTask)

	grandchild := epicChildTicket("grandchild-blocked", parentTask.ID, tkmd.StatusBlocked, nil, []string{"internal/app/sub"})
	mustSaveTicket(t, repoRoot, grandchild)

	subWaveID := fmt.Sprintf("sub-%s-wave-1", parentTask.ID)
	writeOpRunFixture(t, repoRoot, runID, state.RunArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		Mode:         "epic",
		RootTicketID: epic.ID,
		Status:       state.EpicRunStatusBlocked,
		CurrentPhase: state.TicketPhaseBlocked,
		BaseCommit:   baseCommit,
		TicketIDs:    []string{parentTask.ID, grandchild.ID},
		WaveIDs:      []string{subWaveID},
		ResumeCursor: map[string]any{"wave_ordinal": 0},
	})
	writeTicketRunFixture(t, repoRoot, runID, TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:     parentTask.ID,
		CurrentPhase: state.TicketPhaseBlocked,
		BlockReason:  "sub-epic blocked: grandchild needs_context",
	})
	writeTicketRunFixture(t, repoRoot, runID, TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:     grandchild.ID,
		CurrentPhase: state.TicketPhaseBlocked,
		BlockReason:  "needs_context",
	})
	writePlanFixture(t, repoRoot, runID, state.PlanArtifact{
		ArtifactMeta:             state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:                 parentTask.ID,
		EffectiveReviewThreshold: state.SeverityP2,
	})
	writePlanFixture(t, repoRoot, runID, state.PlanArtifact{
		ArtifactMeta:             state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:                 grandchild.ID,
		EffectiveReviewThreshold: state.SeverityP2,
	})
	writeWaveFixture(t, repoRoot, runID, state.WaveArtifact{
		WaveID:         subWaveID,
		ParentTicketID: parentTask.ID,
		Status:         state.WaveStatusFailed,
		TicketIDs:      []string{grandchild.ID},
	})

	// On resume, grandchild is reset to open and gets another chance.
	// The adapter blocks it again (needs_context) to keep the test deterministic.
	adapter := &reflectingAdapter{
		workerResults: []runtime.WorkerResult{
			{
				Status:             runtime.WorkerStatusNeedsContext,
				RetryClass:         runtime.RetryClassBlockedByOperatorInput,
				LeaseID:            "placeholder",
				StartedAt:          epicTestStart(),
				FinishedAt:         epicTestStart().Add(time.Second),
				ResultArtifactPath: "artifact.json",
			},
		},
		reviewResults: []runtime.ReviewResult{},
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

	// Blocked grandchild must be re-executed on resume.
	reqs := adapter.WorkerRequests()
	dispatched := false
	for _, req := range reqs {
		if req.TicketID == grandchild.ID {
			dispatched = true
			break
		}
	}
	if !dispatched {
		t.Errorf("expected blocked grandchild %q to be dispatched on resume, got dispatched: %v",
			grandchild.ID, workerTicketIDs(reqs))
	}

	// Epic ends blocked because grandchild is still stuck on needs_context.
	if report.Run.Status != state.EpicRunStatusBlocked {
		t.Errorf("expected blocked run after grandchild still needs_context, got %q", report.Run.Status)
	}
}

func TestResumeRun_PendingVerificationSubWaveIsCompleted(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	runID := "run-subwave-pending-verification"

	epic := epicTicket("epic-subwave-pending")
	mustSaveTicket(t, repoRoot, epic)

	parentTask := epicChildTicket("parent-subwave-pending", epic.ID, tkmd.StatusClosed, nil, []string{"internal/app"})
	mustSaveTicket(t, repoRoot, parentTask)

	grandchild := epicChildTicket("grandchild-subwave-pending", parentTask.ID, tkmd.StatusClosed, nil, []string{"internal/app/sub"})
	mustSaveTicket(t, repoRoot, grandchild)

	subWaveID := fmt.Sprintf("sub-%s-wave-1", parentTask.ID)
	writeOpRunFixture(t, repoRoot, runID, state.RunArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		Mode:         "epic",
		RootTicketID: epic.ID,
		Status:       state.EpicRunStatusRunning,
		CurrentPhase: state.TicketPhaseImplement,
		BaseCommit:   baseCommit,
		TicketIDs:    []string{parentTask.ID, grandchild.ID},
		WaveIDs:      []string{subWaveID},
		ResumeCursor: map[string]any{
			"wave_ordinal":              0,
			"pending_wave_verification": subWaveID,
		},
	})
	writeTicketRunFixture(t, repoRoot, runID, TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:     parentTask.ID,
		CurrentPhase: state.TicketPhaseClosed,
		Closeout:     &state.CloseoutArtifact{Closable: true},
	})
	writeTicketRunFixture(t, repoRoot, runID, TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:     grandchild.ID,
		CurrentPhase: state.TicketPhaseClosed,
		Closeout:     &state.CloseoutArtifact{Closable: true},
	})
	writePlanFixture(t, repoRoot, runID, state.PlanArtifact{
		ArtifactMeta:             state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:                 parentTask.ID,
		EffectiveReviewThreshold: state.SeverityP2,
	})
	writePlanFixture(t, repoRoot, runID, state.PlanArtifact{
		ArtifactMeta:             state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:                 grandchild.ID,
		EffectiveReviewThreshold: state.SeverityP2,
	})
	writeWaveFixture(t, repoRoot, runID, state.WaveArtifact{
		ArtifactMeta:   state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		WaveID:         subWaveID,
		ParentTicketID: parentTask.ID,
		Status:         state.WaveStatusAccepted,
		TicketIDs:      []string{grandchild.ID},
		ActualScope:    []string{"internal/app/sub"},
		Acceptance:     map[string]any{},
		WaveBaseCommit: baseCommit,
		StartedAt:      epicTestStart(),
		FinishedAt:     epicTestStart().Add(time.Second),
	})

	cfg := policy.DefaultConfig()
	cfg.Verification.QualityCommands = []policy.QualityCommand{{Path: ".", Run: []string{"true"}}}

	report, err := ResumeRun(context.Background(), ResumeRequest{
		RepoRoot: repoRoot,
		RunID:    runID,
		Adapter:  newReflectingAdapter(0),
		Config:   cfg,
	})
	if err != nil {
		t.Fatalf("ResumeRun returned error: %v", err)
	}

	if _, ok := pendingWaveVerificationID(report.Run.ResumeCursor); ok {
		t.Fatalf("expected pending sub-wave verification marker to be cleared, cursor=%v", report.Run.ResumeCursor)
	}
	if report.Run.Status != state.EpicRunStatusCompleted {
		t.Fatalf("expected run to complete after pending sub-wave verification, got %q", report.Run.Status)
	}

	var subWave state.WaveArtifact
	if err := state.LoadJSON(waveArtifactPath(repoRoot, runID, subWaveID), &subWave); err != nil {
		t.Fatalf("load sub-wave artifact: %v", err)
	}
	if subWave.ParentTicketID != parentTask.ID {
		t.Fatalf("expected sub-wave parent %q, got %q", parentTask.ID, subWave.ParentTicketID)
	}
	if got, ok := subWave.Acceptance["wave_verification_passed"].(bool); !ok || !got {
		t.Fatalf("expected sub-wave verification to pass, acceptance=%v", subWave.Acceptance)
	}
}
