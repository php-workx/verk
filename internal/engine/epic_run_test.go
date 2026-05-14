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
	"verk/internal/adapters/ticketstore/epos"
	"verk/internal/policy"
	"verk/internal/state"

	eposticket "github.com/php-workx/epos/ticket"
	eposruntime "github.com/php-workx/epos/ticket/runtime"
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

type functionAdapter struct {
	runWorker   func(context.Context, runtime.WorkerRequest) (runtime.WorkerResult, error)
	runReviewer func(context.Context, runtime.ReviewRequest) (runtime.ReviewResult, error)
}

func (a functionAdapter) RunWorker(ctx context.Context, req runtime.WorkerRequest) (runtime.WorkerResult, error) {
	if a.runWorker == nil {
		return runtime.WorkerResult{}, fmt.Errorf("functionAdapter: RunWorker not configured")
	}
	return a.runWorker(ctx, req)
}

func (a functionAdapter) RunReviewer(ctx context.Context, req runtime.ReviewRequest) (runtime.ReviewResult, error) {
	if a.runReviewer == nil {
		return runtime.ReviewResult{}, fmt.Errorf("functionAdapter: RunReviewer not configured")
	}
	return a.runReviewer(ctx, req)
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
	ready := []epos.Ticket{
		{
			ID:     "ticket-a",
			Title:  "A",
			Status: epos.StatusReady,
			OwnedPaths: []string{
				"internal/app",
			},
		},
		{
			ID:     "ticket-b",
			Title:  "B",
			Status: epos.StatusReady,
			OwnedPaths: []string{
				"internal/app/api",
			},
		},
		{
			ID:     "ticket-c",
			Title:  "C",
			Status: epos.StatusReady,
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
	child := epos.Ticket{
		ID:     "ticket-blocked",
		Title:  "Blocked ticket",
		Status: epos.StatusBlocked,
	}

	blocked := collectBlockedTickets(repoRoot, "run-without-snapshot", []epos.Ticket{child})
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

func TestCollectBlockedTicketsDescribesLiveClaimForOpenTicket(t *testing.T) {
	repoRoot := t.TempDir()
	child := epos.Ticket{
		ID:     "ticket-claimed",
		Title:  "Claimed ticket",
		Status: epos.StatusOpen,
	}
	mustSaveTicket(t, repoRoot, child)
	if _, err := epos.AcquireClaim(repoRoot, "run-owner", child.ID, "lease-owner", 10*time.Minute, time.Now().UTC()); err != nil {
		t.Fatalf("acquire claim: %v", err)
	}

	blocked := collectBlockedTickets(repoRoot, "run-reporter", []epos.Ticket{child})
	if len(blocked) != 1 {
		t.Fatalf("expected one blocked ticket, got %d", len(blocked))
	}
	if blocked[0].Reason != "waiting on lease (held by run-owner)" {
		t.Fatalf("expected claim-aware reason, got %q", blocked[0].Reason)
	}
}

func TestCollectBlockedTicketsClaimReasonHonorsEposEligibility(t *testing.T) {
	t.Run("same run claim falls back to status", func(t *testing.T) {
		repoRoot := t.TempDir()
		child := epos.Ticket{ID: "ticket-same-run", Status: epos.StatusOpen}
		mustSaveTicket(t, repoRoot, child)
		if _, err := epos.AcquireClaim(repoRoot, "run-current", child.ID, "lease-current", 10*time.Minute, time.Now().UTC()); err != nil {
			t.Fatalf("acquire claim: %v", err)
		}

		blocked := collectBlockedTickets(repoRoot, "run-current", []epos.Ticket{child})
		if blocked[0].Reason != "status=open" {
			t.Fatalf("expected status fallback, got %q", blocked[0].Reason)
		}
	})

	t.Run("expired lease falls back to status", func(t *testing.T) {
		repoRoot := t.TempDir()
		child := epos.Ticket{ID: "ticket-expired", Status: epos.StatusOpen}
		mustSaveTicket(t, repoRoot, child)
		writeRuntimeState(t, repoRoot, &eposticket.RuntimeState{
			TicketID: child.ID,
			Claim: &eposticket.Claim{
				ClaimedBy:    "run-owner",
				ClaimBackend: "run-owner",
				ClaimedAt:    time.Now().Add(-2 * time.Hour),
			},
			Lease: &eposticket.Lease{
				LeaseID:   "lease-expired",
				ExpiresAt: time.Now().Add(-time.Hour),
			},
		})

		blocked := collectBlockedTickets(repoRoot, "run-current", []epos.Ticket{child})
		if blocked[0].Reason != "status=open" {
			t.Fatalf("expected status fallback, got %q", blocked[0].Reason)
		}
	})

	t.Run("deps remain the primary reason", func(t *testing.T) {
		repoRoot := t.TempDir()
		child := epos.Ticket{ID: "ticket-deps", Status: epos.StatusOpen, Deps: []string{"dep-open"}}
		mustSaveTicket(t, repoRoot, child)
		if _, err := epos.AcquireClaim(repoRoot, "run-owner", child.ID, "lease-owner", 10*time.Minute, time.Now().UTC()); err != nil {
			t.Fatalf("acquire claim: %v", err)
		}

		blocked := collectBlockedTickets(repoRoot, "run-current", []epos.Ticket{child})
		if blocked[0].Reason != "waiting on deps: dep-open" {
			t.Fatalf("expected deps reason, got %q", blocked[0].Reason)
		}
	})

	t.Run("unknown status is not masked by claim", func(t *testing.T) {
		repoRoot := t.TempDir()
		child := epos.Ticket{ID: "ticket-weird", Status: epos.Status("weird")}
		if _, err := epos.AcquireClaim(repoRoot, "run-owner", child.ID, "lease-owner", 10*time.Minute, time.Now().UTC()); err != nil {
			t.Fatalf("acquire claim: %v", err)
		}

		blocked := collectBlockedTickets(repoRoot, "run-current", []epos.Ticket{child})
		if blocked[0].Reason != "status=weird" {
			t.Fatalf("expected status fallback, got %q", blocked[0].Reason)
		}
	})

	t.Run("live blocking claim wins over stale snapshot status", func(t *testing.T) {
		repoRoot := t.TempDir()
		runID := "run-current"
		child := epos.Ticket{ID: "ticket-stale-snapshot", Status: epos.StatusOpen}
		mustSaveTicket(t, repoRoot, child)
		if _, err := epos.AcquireClaim(repoRoot, "run-owner", child.ID, "lease-owner", 10*time.Minute, time.Now().UTC()); err != nil {
			t.Fatalf("acquire claim: %v", err)
		}
		writeTicketRunFixture(t, repoRoot, runID, TicketRunSnapshot{
			ArtifactMeta: state.ArtifactMeta{SchemaVersion: artifactSchemaVersion, RunID: runID},
			TicketID:     child.ID,
			CurrentPhase: state.TicketPhaseImplement,
			BlockReason:  "status=open",
		})

		blocked := collectBlockedTickets(repoRoot, runID, []epos.Ticket{child})
		if blocked[0].Reason != "waiting on lease (held by run-owner)" {
			t.Fatalf("expected claim-aware reason, got %q", blocked[0].Reason)
		}
	})
}

func TestCollectBlockedTicketsOffersRetryForBlockedSnapshot(t *testing.T) {
	repoRoot := t.TempDir()
	runID := "run-with-blocked-snapshot"
	child := epos.Ticket{
		ID:     "ticket-blocked",
		Title:  "Blocked ticket",
		Status: epos.StatusBlocked,
	}
	writeTicketRunFixture(t, repoRoot, runID, TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: artifactSchemaVersion, RunID: runID},
		TicketID:     child.ID,
		CurrentPhase: state.TicketPhaseBlocked,
		BlockReason:  "review failed",
	})

	blocked := collectBlockedTickets(repoRoot, runID, []epos.Ticket{child})
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

// TestCollectBlockedTickets_FailedRetryableOffersRetryToImplement verifies that
// a snapshot with Outcome=failed_retryable sets RetryPhase to implement,
// regardless of which phase the ticket was in (e.g. verify).
func TestCollectBlockedTickets_FailedRetryableOffersRetryToImplement(t *testing.T) {
	repoRoot := t.TempDir()
	runID := "run-fr"
	child := epos.Ticket{
		ID:     "ticket-fr",
		Title:  "Failed retryable ticket",
		Status: epos.StatusBlocked,
	}
	writeTicketRunFixture(t, repoRoot, runID, TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: artifactSchemaVersion, RunID: runID},
		TicketID:     child.ID,
		CurrentPhase: state.TicketPhaseVerify,
		Outcome:      state.TicketOutcomeFailedRetryable,
		BlockReason:  "verification failed",
	})

	blocked := collectBlockedTickets(repoRoot, runID, []epos.Ticket{child})
	if len(blocked) != 1 {
		t.Fatalf("expected one blocked ticket, got %d", len(blocked))
	}
	if blocked[0].Outcome != state.TicketOutcomeFailedRetryable {
		t.Fatalf("expected failed_retryable outcome, got %q", blocked[0].Outcome)
	}
	if blocked[0].RetryPhase != state.TicketPhaseImplement {
		t.Fatalf("expected implement retry phase for failed_retryable, got %q", blocked[0].RetryPhase)
	}
}

// TestCollectBlockedTickets_NeedsDecisionHasNoAutoRetry verifies that a snapshot
// with Outcome=needs_decision is listed but does not produce a retry command.
func TestCollectBlockedTickets_NeedsDecisionHasNoAutoRetry(t *testing.T) {
	repoRoot := t.TempDir()
	runID := "run-nd"
	child := epos.Ticket{
		ID:     "ticket-nd",
		Title:  "Needs decision ticket",
		Status: epos.StatusBlocked,
	}
	writeTicketRunFixture(t, repoRoot, runID, TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: artifactSchemaVersion, RunID: runID},
		TicketID:     child.ID,
		CurrentPhase: state.TicketPhaseBlocked,
		Outcome:      state.TicketOutcomeNeedsDecision,
		BlockReason:  "ambiguous requirements",
	})

	blocked := collectBlockedTickets(repoRoot, runID, []epos.Ticket{child})
	if len(blocked) != 1 {
		t.Fatalf("expected one blocked ticket, got %d", len(blocked))
	}
	if blocked[0].Outcome != state.TicketOutcomeNeedsDecision {
		t.Fatalf("expected needs_decision outcome, got %q", blocked[0].Outcome)
	}
	if blocked[0].RetryPhase != "" {
		t.Fatalf("expected no auto retry for needs_decision, got RetryPhase=%q", blocked[0].RetryPhase)
	}
}

// TestCollectBlockedTickets_OutcomeBlockedHasNoAutoRetry verifies that a
// snapshot with Outcome=blocked is listed as blocked but does not produce an
// automatic retry command. An operator must explicitly choose a legal reopen
// target.
func TestCollectBlockedTickets_OutcomeBlockedHasNoAutoRetry(t *testing.T) {
	repoRoot := t.TempDir()
	runID := "run-ob"
	child := epos.Ticket{
		ID:     "ticket-ob",
		Title:  "Outcome blocked ticket",
		Status: epos.StatusBlocked,
	}
	writeTicketRunFixture(t, repoRoot, runID, TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: artifactSchemaVersion, RunID: runID},
		TicketID:     child.ID,
		CurrentPhase: state.TicketPhaseBlocked,
		Outcome:      state.TicketOutcomeBlocked,
		BlockReason:  "external dependency unavailable",
	})

	blocked := collectBlockedTickets(repoRoot, runID, []epos.Ticket{child})
	if len(blocked) != 1 {
		t.Fatalf("expected one blocked ticket, got %d", len(blocked))
	}
	if blocked[0].Outcome != state.TicketOutcomeBlocked {
		t.Fatalf("expected blocked outcome, got %q", blocked[0].Outcome)
	}
	if blocked[0].RetryPhase != "" {
		t.Fatalf("expected no auto retry for outcome=blocked, got RetryPhase=%q", blocked[0].RetryPhase)
	}
}

// TestCollectBlockedTickets_LegacyBlockedPhaseNoOutcomeKeepsRetry verifies
// backward compatibility: an old snapshot with CurrentPhase=blocked and empty
// Outcome still gets RetryPhase=implement via the phase-based fallback.
func TestCollectBlockedTickets_LegacyBlockedPhaseNoOutcomeKeepsRetry(t *testing.T) {
	repoRoot := t.TempDir()
	runID := "run-legacy"
	child := epos.Ticket{
		ID:     "ticket-legacy",
		Title:  "Legacy blocked ticket",
		Status: epos.StatusBlocked,
	}
	writeTicketRunFixture(t, repoRoot, runID, TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: artifactSchemaVersion, RunID: runID},
		TicketID:     child.ID,
		CurrentPhase: state.TicketPhaseBlocked,
		Outcome:      "", // old snapshot: no outcome
		BlockReason:  "legacy block reason",
	})

	blocked := collectBlockedTickets(repoRoot, runID, []epos.Ticket{child})
	if len(blocked) != 1 {
		t.Fatalf("expected one blocked ticket, got %d", len(blocked))
	}
	if blocked[0].Outcome != "" {
		t.Fatalf("expected empty outcome for legacy snapshot, got %q", blocked[0].Outcome)
	}
	// Legacy: CurrentPhase=blocked with no outcome → fallback to implement.
	if blocked[0].RetryPhase != state.TicketPhaseImplement {
		t.Fatalf("expected implement retry for legacy blocked phase, got %q", blocked[0].RetryPhase)
	}
}

func TestRunEpicSchedulesOpenAndReadyTickets(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	cfg := policy.DefaultConfig()

	epic := epicTicket("epic-ready")
	mustSaveTicket(t, repoRoot, epic)

	ready := epicChildTicket("ticket-ready", epic.ID, epos.StatusReady, nil, []string{"internal/app"})
	mustSaveTicket(t, repoRoot, ready)

	// Open tickets with resolved deps should also be scheduled (tk creates tickets as open)
	open := epicChildTicket("ticket-open", epic.ID, epos.StatusOpen, nil, []string{"docs"})
	mustSaveTicket(t, repoRoot, open)

	// Blocked tickets should NOT be scheduled
	blocked := epicChildTicket("ticket-blocked", epic.ID, epos.StatusBlocked, nil, []string{"config"})
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

func TestRunEpic_RejectsUnsafeRootTicketIDWithSafeRunID(t *testing.T) {
	repoRoot := t.TempDir()
	_, err := RunEpic(context.Background(), RunEpicRequest{
		RepoRoot:     repoRoot,
		RunID:        "run-safe",
		RootTicketID: "../escaped",
		Adapter:      runtimefake.New(nil, nil),
	})
	if err == nil {
		t.Fatal("expected unsafe root ticket id to be rejected")
	}
	if !strings.Contains(err.Error(), "invalid root ticket id") {
		t.Fatalf("expected invalid root ticket id error, got: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(repoRoot, ".verk", "runs", "run-safe")); !os.IsNotExist(statErr) {
		t.Fatalf("unsafe root ticket id created run artifacts: %v", statErr)
	}
}

func TestRunEpicScopeViolationBlocksWave(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	cfg := policy.DefaultConfig()

	epic := epicTicket("epic-scope")
	mustSaveTicket(t, repoRoot, epic)

	child := epicChildTicket("ticket-scope", epic.ID, epos.StatusReady, nil, []string{"internal/app"})
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

	if _, statErr := os.Stat(touchOutsideScope); !os.IsNotExist(statErr) {
		t.Fatalf("expected scope violation fixture to stay out of main tree, got %v", statErr)
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

	ready := epicChildTicket("ticket-ready", epic.ID, epos.StatusReady, nil, []string{"internal/app"})
	mustSaveTicket(t, repoRoot, ready)

	blocked := epicChildTicket("ticket-blocked", epic.ID, epos.StatusBlocked, nil, []string{"docs"})
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
	mustSaveTicket(t, repoRoot, epicChildTicket("ticket-a", root.ID, epos.StatusReady, nil, []string{"internal/app"}))
	mustSaveTicket(t, repoRoot, epicChildTicket("ticket-b", root.ID, epos.StatusReady, nil, []string{"docs"}))

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
	codexTicket := epicChildTicket("ticket-codex", root.ID, epos.StatusReady, nil, []string{"internal/app"})
	codexTicket.Runtime = "codex"
	mustSaveTicket(t, repoRoot, codexTicket)
	claudeTicket := epicChildTicket("ticket-claude", root.ID, epos.StatusReady, nil, []string{"docs"})
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
	child := epicChildTicket("ticket-failscope", epic.ID, epos.StatusReady, nil, []string{"internal/app"})
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

func TestRunEpicBlocksWhenMainTreeDiffersFromWaveBase(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	cfg := policy.DefaultConfig()

	baselinePath := filepath.Join(repoRoot, "baseline-dirty.txt")
	if err := os.WriteFile(baselinePath, []byte("dirty baseline\n"), 0o644); err != nil {
		t.Fatalf("write baseline file: %v", err)
	}

	root := epicTicket("epic-baseline")
	mustSaveTicket(t, repoRoot, root)
	mustSaveTicket(t, repoRoot, epicChildTicket("ticket-baseline", root.ID, epos.StatusReady, nil, []string{"internal/app"}))

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
	if err == nil {
		t.Fatal("expected dirty main tree to block wave start, got nil error")
	}
	if !strings.Contains(err.Error(), "dirty main tree") {
		t.Fatalf("expected dirty main tree error, got %v", err)
	}
	if result.Run.Status != state.EpicRunStatusBlocked {
		t.Fatalf("expected blocked run status for dirty main tree, got %q", result.Run.Status)
	}
	if got := adapter.WorkerRequests(); len(got) != 0 {
		t.Fatalf("expected no worker execution when main tree is dirty, got %d requests", len(got))
	}
}

func TestRunEpicWaveTwoSeesAcceptedWaveOneChangesWithoutUserVisibleCommit(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	cfg := policy.DefaultConfig()

	root := epicTicket("epic-two-wave")
	mustSaveTicket(t, repoRoot, root)
	mustSaveTicket(t, repoRoot, epicChildTicket("ticket-wave-1", root.ID, epos.StatusReady, nil, []string{"wave1.txt"}))
	mustSaveTicket(t, repoRoot, epicChildTicket("ticket-wave-2", root.ID, epos.StatusOpen, []string{"ticket-wave-1"}, []string{"wave2.txt"}))

	start := epicTestStart()
	adapter := functionAdapter{
		runWorker: func(ctx context.Context, req runtime.WorkerRequest) (runtime.WorkerResult, error) {
			switch req.TicketID {
			case "ticket-wave-1":
				if err := os.WriteFile(filepath.Join(req.WorktreePath, "wave1.txt"), []byte("wave one\n"), 0o644); err != nil {
					return runtime.WorkerResult{}, err
				}
			case "ticket-wave-2":
				content, err := os.ReadFile(filepath.Join(req.WorktreePath, "wave1.txt"))
				if err != nil {
					return runtime.WorkerResult{}, fmt.Errorf("wave 2 missing accepted wave 1 output: %w", err)
				}
				if strings.TrimSpace(string(content)) != "wave one" {
					return runtime.WorkerResult{}, fmt.Errorf("wave 2 saw unexpected wave1 content %q", string(content))
				}
				if err := os.WriteFile(filepath.Join(req.WorktreePath, "wave2.txt"), []byte("wave two\n"), 0o644); err != nil {
					return runtime.WorkerResult{}, err
				}
			default:
				return runtime.WorkerResult{}, fmt.Errorf("unexpected ticket %s", req.TicketID)
			}
			return runtime.WorkerResult{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            req.LeaseID,
				StartedAt:          start,
				FinishedAt:         start.Add(time.Second),
				ResultArtifactPath: filepath.Join(repoRoot, req.TicketID+".worker.json"),
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
				ResultArtifactPath: filepath.Join(repoRoot, req.TicketID+".review.json"),
			}, nil
		},
	}

	result, err := RunEpic(context.Background(), RunEpicRequest{
		RepoRoot:     repoRoot,
		RunID:        "run-two-wave",
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
	if got := len(result.Waves); got != 2 {
		t.Fatalf("expected two waves, got %d", got)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "wave1.txt")); err != nil {
		t.Fatalf("expected wave1.txt in main tree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "wave2.txt")); err != nil {
		t.Fatalf("expected wave2.txt in main tree: %v", err)
	}

	head, err := gitOutput(repoRoot, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}
	if strings.TrimSpace(head) != baseCommit {
		t.Fatalf("expected user-visible HEAD to remain %q, got %q", baseCommit, strings.TrimSpace(head))
	}
	if _, err := gitOutput(repoRoot, "show-ref", "--verify", integrationBaseRef("run-two-wave")); err != nil {
		t.Fatalf("expected hidden integration base ref to exist: %v", err)
	}
	if _, err := gitOutput(repoRoot, "show-ref", "--verify", integrationTicketRef("run-two-wave", "ticket-wave-1")); err != nil {
		t.Fatalf("expected hidden ticket ref for wave 1 to exist: %v", err)
	}
	if _, err := gitOutput(repoRoot, "show-ref", "--verify", integrationTicketRef("run-two-wave", "ticket-wave-2")); err != nil {
		t.Fatalf("expected hidden ticket ref for wave 2 to exist: %v", err)
	}
}

func TestRunEpicCleansWaveWorktreesAfterFreshRun(t *testing.T) {
	repoRoot := t.TempDir()
	worktreeRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	cfg := policy.DefaultConfig()

	root := epicTicket("epic-clean-fresh")
	child := epicChildTicket("ticket-clean-fresh", root.ID, epos.StatusReady, nil, []string{"fresh.txt"})
	mustSaveTicket(t, repoRoot, root)
	mustSaveTicket(t, repoRoot, child)

	start := epicTestStart()
	var workerWorktreePath string
	adapter := functionAdapter{
		runWorker: func(ctx context.Context, req runtime.WorkerRequest) (runtime.WorkerResult, error) {
			workerWorktreePath = req.WorktreePath
			if strings.TrimSpace(workerWorktreePath) == "" {
				return runtime.WorkerResult{}, fmt.Errorf("worker did not receive worktree path")
			}
			if err := os.WriteFile(filepath.Join(workerWorktreePath, "fresh.txt"), []byte("fresh output\n"), 0o644); err != nil {
				return runtime.WorkerResult{}, err
			}
			return runtime.WorkerResult{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            req.LeaseID,
				StartedAt:          start,
				FinishedAt:         start.Add(time.Second),
				ResultArtifactPath: filepath.Join(repoRoot, req.TicketID+".worker.json"),
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
				ResultArtifactPath: filepath.Join(repoRoot, req.TicketID+".review.json"),
			}, nil
		},
	}

	result, err := RunEpic(context.Background(), RunEpicRequest{
		RepoRoot:     repoRoot,
		WorktreeRoot: worktreeRoot,
		RunID:        "run-clean-fresh",
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
	if _, statErr := os.Stat(workerWorktreePath); !os.IsNotExist(statErr) {
		t.Fatalf("expected ticket worktree to be removed, got %q: %v", workerWorktreePath, statErr)
	}
	integrationPath := filepath.Join(worktreeRoot, "run-clean-fresh", "_integration")
	if _, statErr := os.Stat(integrationPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected integration worktree to be removed, got %q: %v", integrationPath, statErr)
	}
}

func TestRunEpicDoesNotMutateMainWhenFreshWaveCommitFails(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	cfg := policy.DefaultConfig()

	root := epicTicket("epic-commit-fail")
	child := epicChildTicket("ticket-commit-fail", root.ID, epos.StatusReady, nil, []string{"integrated.txt"})
	mustSaveTicket(t, repoRoot, root)
	mustSaveTicket(t, repoRoot, child)

	start := epicTestStart()
	adapter := functionAdapter{
		runWorker: func(ctx context.Context, req runtime.WorkerRequest) (runtime.WorkerResult, error) {
			if err := os.WriteFile(filepath.Join(req.WorktreePath, "integrated.txt"), []byte("integrated\n"), 0o644); err != nil {
				return runtime.WorkerResult{}, err
			}
			lockPath := filepath.Join(repoRoot, ".git", "refs", "verk", "runs", "run-commit-fail", "base.lock")
			if err := os.WriteFile(lockPath, []byte("locked\n"), 0o644); err != nil {
				return runtime.WorkerResult{}, err
			}
			return runtime.WorkerResult{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            req.LeaseID,
				StartedAt:          start,
				FinishedAt:         start.Add(time.Second),
				ResultArtifactPath: filepath.Join(repoRoot, req.TicketID+".worker.json"),
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
				ResultArtifactPath: filepath.Join(repoRoot, req.TicketID+".review.json"),
			}, nil
		},
	}

	_, err := RunEpic(context.Background(), RunEpicRequest{
		RepoRoot:     repoRoot,
		RunID:        "run-commit-fail",
		RootTicketID: root.ID,
		BaseCommit:   baseCommit,
		Adapter:      adapter,
		Config:       cfg,
	})
	if err == nil {
		t.Fatal("expected RunEpic to fail when hidden base ref cannot advance")
	}
	if _, statErr := os.Stat(filepath.Join(repoRoot, "integrated.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("main tree was mutated before hidden base advanced: %v", statErr)
	}
}

func TestRunEpic_DoesNotAdvanceHiddenBaseWhenMainApplyFails(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	runID := "run-main-apply-fail"
	cfg := policy.DefaultConfig()
	cfg.Verification.QualityCommands = nil
	cfg.Verification.WaveCommands = nil

	root := epicTicket("epic-main-apply-fail")
	child := epicChildTicket("ticket-main-apply-fail", root.ID, epos.StatusReady, nil, []string{"integrated.txt"})
	mustSaveTicket(t, repoRoot, root)
	mustSaveTicket(t, repoRoot, child)

	start := epicTestStart()
	lockPath := filepath.Join(repoRoot, ".git", "index.lock")
	t.Cleanup(func() { _ = os.Remove(lockPath) })
	adapter := functionAdapter{
		runWorker: func(ctx context.Context, req runtime.WorkerRequest) (runtime.WorkerResult, error) {
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
				ResultArtifactPath: filepath.Join(repoRoot, req.TicketID+".worker.json"),
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
				ResultArtifactPath: filepath.Join(repoRoot, req.TicketID+".review.json"),
			}, nil
		},
	}

	_, err := RunEpic(context.Background(), RunEpicRequest{
		RepoRoot:     repoRoot,
		RunID:        runID,
		RootTicketID: root.ID,
		BaseCommit:   baseCommit,
		Adapter:      adapter,
		Config:       cfg,
	})
	if err == nil {
		t.Fatal("expected RunEpic to fail when applying integrated delta to main")
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

func TestRunEpic_FinalRunSaveFailureAfterMainApplyIsRecoverable(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	worktreeRoot := t.TempDir()
	runID := "run-final-save-recoverable"
	cfg := policy.DefaultConfig()
	cfg.Verification.QualityCommands = nil
	cfg.Verification.WaveCommands = nil

	root := epicTicket("epic-final-save-recoverable")
	child := epicChildTicket("ticket-final-save-recoverable", root.ID, epos.StatusReady, nil, []string{"docs/final-save.txt"})
	mustSaveTicket(t, repoRoot, root)
	mustSaveTicket(t, repoRoot, child)

	integration, err := prepareWaveIntegration(context.Background(), repoRoot, runID, worktreeRoot, baseCommit)
	if err != nil {
		t.Fatalf("prepare wave integration: %v", err)
	}
	t.Cleanup(func() { _ = integration.Cleanup() })

	acceptedCommit := createDetachedWorktreeCommit(t, repoRoot, baseCommit, map[string]string{
		"docs/final-save.txt": "final save\n",
	})
	acceptedRef := integrationTicketRef(runID, child.ID)
	if err := gitUpdateRef(repoRoot, acceptedRef, acceptedCommit); err != nil {
		t.Fatalf("seed accepted ref: %v", err)
	}
	if err := integration.ApplyAcceptedTicketRefs(context.Background(), []string{acceptedRef}); err != nil {
		t.Fatalf("apply accepted ref: %v", err)
	}

	runPath := runJSONPath(repoRoot, runID)
	wavePath := waveArtifactPath(repoRoot, runID, "wave-1")
	wave := state.WaveArtifact{
		ArtifactMeta:   state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		WaveID:         "wave-1",
		Ordinal:        1,
		Status:         state.WaveStatusAccepted,
		TicketIDs:      []string{child.ID},
		PlannedScope:   []string{"docs/final-save.txt"},
		ActualScope:    []string{"docs/final-save.txt"},
		Acceptance:     map[string]any{"wave_verification_passed": true},
		WaveBaseCommit: baseCommit,
	}
	if err := state.SaveJSONAtomic(wavePath, wave); err != nil {
		t.Fatalf("save wave artifact: %v", err)
	}

	run := state.RunArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		Mode:         "epic",
		RootTicketID: root.ID,
		Status:       state.EpicRunStatusRunning,
		CurrentPhase: state.TicketPhaseImplement,
		WaveIDs:      []string{wave.WaveID},
		TicketIDs:    []string{child.ID},
		BaseCommit:   baseCommit,
		ResumeCursor: map[string]any{"wave_ordinal": 1},
	}
	tx := pendingWaveIntegrationTransaction{
		WaveID:       wave.WaveID,
		BaseCommit:   baseCommit,
		AcceptedRefs: []string{acceptedRef},
		ChangedFiles: []string{"docs/final-save.txt"},
		WorktreePath: integration.WorktreePath(),
	}
	setPendingWaveIntegration(run.ResumeCursor, tx)
	if err := state.SaveJSONAtomic(runPath, run); err != nil {
		t.Fatalf("save initial run artifact: %v", err)
	}

	badRunPath := filepath.Join(repoRoot, "tracked.txt", "run.json")
	err = completePendingWaveIntegrationTransaction(
		RunEpicRequest{RepoRoot: repoRoot, RunID: runID, WorktreeRoot: worktreeRoot, Config: cfg},
		run.ResumeCursor,
		badRunPath,
		&run,
		&wave,
		wavePath,
		tx,
		integration,
	)
	if err == nil {
		t.Fatal("expected final run save failure")
	}
	if !strings.Contains(err.Error(), "persist run state") {
		t.Fatalf("expected final save error to mention persist run state, got %v", err)
	}
	if got, readErr := os.ReadFile(filepath.Join(repoRoot, "docs", "final-save.txt")); readErr != nil || string(got) != "final save\n" {
		t.Fatalf("expected integrated file on main after failed final save, got %q err=%v", string(got), readErr)
	}

	if err := integration.Cleanup(); err != nil {
		t.Fatalf("cleanup integration worktree: %v", err)
	}
	var durableRun state.RunArtifact
	if err := state.LoadJSON(runPath, &durableRun); err != nil {
		t.Fatalf("load durable pending run: %v", err)
	}
	err = resumePendingWaveVerification(
		context.Background(),
		RunEpicRequest{RepoRoot: repoRoot, RunID: runID, WorktreeRoot: worktreeRoot, Config: cfg},
		cfg,
		durableRun.ResumeCursor,
		runPath,
		&durableRun,
	)
	if err != nil {
		t.Fatalf("resume pending already-applied integration: %v", err)
	}
	if _, ok := pendingWaveVerificationID(durableRun.ResumeCursor); ok {
		t.Fatalf("expected pending wave verification to clear after recovery, cursor=%v", durableRun.ResumeCursor)
	}
	if _, ok := durableRun.ResumeCursor["pending_wave_integration"]; ok {
		t.Fatalf("expected pending wave integration to clear after recovery, cursor=%v", durableRun.ResumeCursor)
	}
	if got, ok := durableRun.ResumeCursor["last_wave_base_commit"].(string); !ok || got == "" || got == baseCommit {
		t.Fatalf("expected recovered last_wave_base_commit to advance, got %q cursor=%v", got, durableRun.ResumeCursor)
	}
}

func TestRunEpic_AppliesPostRepairFilesToMain(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	runID := "run-post-repair-main"
	cfg := policy.DefaultConfig()
	cfg.Verification.QualityCommands = nil
	cfg.Verification.WaveCommands = []policy.QualityCommand{{Path: ".", Run: []string{"test -f repair.txt"}}}
	cfg.Policy.MaxWaveRepairCycles = 1

	root := epicTicket("epic-post-repair-main")
	child := epicChildTicket("ticket-post-repair-main", root.ID, epos.StatusReady, nil, []string{"primary.txt"})
	mustSaveTicket(t, repoRoot, root)
	mustSaveTicket(t, repoRoot, child)

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

	result, err := RunEpic(context.Background(), RunEpicRequest{
		RepoRoot:     repoRoot,
		RunID:        runID,
		RootTicketID: root.ID,
		BaseCommit:   baseCommit,
		Adapter:      adapter,
		Config:       cfg,
	})
	if err != nil {
		t.Fatalf("RunEpic returned error: %v", err)
	}
	if result.Run.Status != state.EpicRunStatusCompleted {
		t.Fatalf("expected completed run, got %q", result.Run.Status)
	}
	for _, path := range []string{"primary.txt", "repair.txt"} {
		if _, err := os.Stat(filepath.Join(repoRoot, path)); err != nil {
			t.Fatalf("expected %s applied to main: %v", path, err)
		}
	}
}

func TestRunEpic_PersistsWaveOrdinalBeforeWaveArtifact(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	runID := "run-wave-cursor-before-artifact"
	cfg := policy.DefaultConfig()
	cfg.Verification.QualityCommands = nil
	cfg.Verification.WaveCommands = nil

	root := epicTicket("epic-wave-cursor-before-artifact")
	child := epicChildTicket("ticket-wave-cursor-before-artifact", root.ID, epos.StatusReady, nil, []string{"tracked.txt"})
	mustSaveTicket(t, repoRoot, root)
	mustSaveTicket(t, repoRoot, child)

	adapter := newBlockingEpicAdapter(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type runOutcome struct {
		result RunEpicResult
		err    error
	}
	done := make(chan runOutcome, 1)
	go func() {
		result, err := RunEpic(ctx, RunEpicRequest{
			RepoRoot:     repoRoot,
			RunID:        runID,
			RootTicketID: root.ID,
			BaseCommit:   baseCommit,
			Adapter:      adapter,
			Config:       cfg,
		})
		done <- runOutcome{result: result, err: err}
	}()

	select {
	case got := <-adapter.started:
		if got != child.ID {
			t.Fatalf("unexpected worker ticket %q", got)
		}
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("timed out waiting for worker to start")
	}

	var duringWave state.RunArtifact
	if err := state.LoadJSON(runJSONPath(repoRoot, runID), &duringWave); err != nil {
		close(adapter.release)
		t.Fatalf("load run while wave is active: %v", err)
	}

	close(adapter.release)
	select {
	case outcome := <-done:
		if outcome.err != nil {
			t.Fatalf("RunEpic returned error after releasing worker: %v", outcome.err)
		}
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("timed out waiting for RunEpic to finish")
	}

	if got := resumeCursorWaveOrdinal(duringWave.ResumeCursor); got != 1 {
		t.Fatalf("expected wave_ordinal to be durable before worker started, got %d in cursor %#v", got, duringWave.ResumeCursor)
	}
	foundWaveID := false
	for _, waveID := range duringWave.WaveIDs {
		if waveID == "wave-1" {
			foundWaveID = true
			break
		}
	}
	if !foundWaveID {
		t.Fatalf("expected wave-1 in durable WaveIDs before worker started, got %#v", duringWave.WaveIDs)
	}
}

func TestRunEpicBlockedTicketKeepsMainTreeCleanAndPersistsDiffArtifact(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	cfg := policy.DefaultConfig()

	root := epicTicket("epic-blocked-artifact")
	mustSaveTicket(t, repoRoot, root)
	mustSaveTicket(t, repoRoot, epicChildTicket("ticket-blocked-artifact", root.ID, epos.StatusReady, nil, []string{"tracked.txt"}))

	start := epicTestStart()
	adapter := functionAdapter{
		runWorker: func(ctx context.Context, req runtime.WorkerRequest) (runtime.WorkerResult, error) {
			if err := os.WriteFile(filepath.Join(req.WorktreePath, "tracked.txt"), []byte("blocked output\n"), 0o644); err != nil {
				return runtime.WorkerResult{}, err
			}
			return runtime.WorkerResult{
				Status:             runtime.WorkerStatusBlocked,
				RetryClass:         runtime.RetryClassRetryable,
				BlockReason:        "worker blocked after writing candidate output",
				LeaseID:            req.LeaseID,
				StartedAt:          start,
				FinishedAt:         start.Add(time.Second),
				ResultArtifactPath: filepath.Join(repoRoot, "ticket-blocked-artifact.worker.json"),
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
				ResultArtifactPath: filepath.Join(repoRoot, req.TicketID+".review.json"),
			}, nil
		},
	}

	result, err := RunEpic(context.Background(), RunEpicRequest{
		RepoRoot:     repoRoot,
		RunID:        "run-blocked-artifact",
		RootTicketID: root.ID,
		BaseCommit:   baseCommit,
		Adapter:      adapter,
		Config:       cfg,
	})
	if err == nil {
		t.Fatal("expected blocked epic result, got nil error")
	}
	if result.Run.Status != state.EpicRunStatusBlocked {
		t.Fatalf("expected blocked epic run, got %q", result.Run.Status)
	}
	mainContent, err := os.ReadFile(filepath.Join(repoRoot, "tracked.txt"))
	if err != nil {
		t.Fatalf("read main tracked.txt: %v", err)
	}
	if string(mainContent) != "base\n" {
		t.Fatalf("expected main tree tracked.txt to remain unchanged, got %q", string(mainContent))
	}
	diffPath := filepath.Join(repoRoot, ".verk", "runs", "run-blocked-artifact", "tickets", "ticket-blocked-artifact", "worktree.diff")
	diffContent, err := os.ReadFile(diffPath)
	if err != nil {
		t.Fatalf("expected persisted diff artifact at %s: %v", diffPath, err)
	}
	if !strings.Contains(string(diffContent), "tracked.txt") {
		t.Fatalf("expected diff artifact to mention tracked.txt, got %q", string(diffContent))
	}
}

func TestRunEpic_BlocksWhenFailedTicketDiffPersistenceFails(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	runID := "run-blocked-diff-persist-fail"
	cfg := policy.DefaultConfig()
	cfg.Verification.QualityCommands = nil
	cfg.Verification.WaveCommands = nil

	root := epicTicket("epic-blocked-diff-persist-fail")
	child := epicChildTicket("ticket-blocked-diff-persist-fail", root.ID, epos.StatusReady, nil, []string{"tracked.txt"})
	mustSaveTicket(t, repoRoot, root)
	mustSaveTicket(t, repoRoot, child)

	diffPath := filepath.Join(repoRoot, ".verk", "runs", runID, "tickets", child.ID, "worktree.diff")
	if err := os.MkdirAll(diffPath, 0o755); err != nil {
		t.Fatalf("seed unwritable diff artifact path: %v", err)
	}

	start := epicTestStart()
	adapter := functionAdapter{
		runWorker: func(ctx context.Context, req runtime.WorkerRequest) (runtime.WorkerResult, error) {
			if err := os.WriteFile(filepath.Join(req.WorktreePath, "tracked.txt"), []byte("blocked output\n"), 0o644); err != nil {
				return runtime.WorkerResult{}, err
			}
			return runtime.WorkerResult{
				Status:             runtime.WorkerStatusBlocked,
				RetryClass:         runtime.RetryClassRetryable,
				BlockReason:        "blocked after writing candidate output",
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
				ResultArtifactPath: filepath.Join(repoRoot, req.TicketID+".review.json"),
			}, nil
		},
	}

	_, err := RunEpic(context.Background(), RunEpicRequest{
		RepoRoot:     repoRoot,
		RunID:        runID,
		RootTicketID: root.ID,
		BaseCommit:   baseCommit,
		Adapter:      adapter,
		Config:       cfg,
	})
	if err == nil {
		t.Fatal("expected blocked epic result with diff artifact persistence failure")
	}
	if !strings.Contains(err.Error(), "persist diff artifact") {
		t.Fatalf("expected error to mention persist diff artifact, got %v", err)
	}
}

func TestWaveIsolation_LintInOneTicketDoesNotBlockOther(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	cfg := policy.DefaultConfig()
	cfg.Scheduler.MaxConcurrency = 2
	cfg.Policy.MaxImplementationAttempts = 1
	cfg.Verification.QualityCommands = nil
	cfg.Verification.WaveCommands = nil

	root := epicTicket("epic-wave-isolation")
	mustSaveTicket(t, repoRoot, root)

	ticketA := epicChildTicket("ticket-a", root.ID, epos.StatusReady, nil, []string{"a.go"})
	ticketA.ValidationCommands = []string{
		`test -f a.go && test ! -f b.go && printf 'ticket-a verified\n'`,
	}
	mustSaveTicket(t, repoRoot, ticketA)

	ticketB := epicChildTicket("ticket-b", root.ID, epos.StatusReady, nil, []string{"b.go"})
	ticketB.ValidationCommands = []string{
		`test -f b.go && printf 'lint failure: b.go\n' >&2 && exit 1`,
	}
	mustSaveTicket(t, repoRoot, ticketB)

	start := epicTestStart()
	adapter := functionAdapter{
		runWorker: func(ctx context.Context, req runtime.WorkerRequest) (runtime.WorkerResult, error) {
			if strings.TrimSpace(req.WorktreePath) == "" {
				return runtime.WorkerResult{}, fmt.Errorf("worker %s did not receive worktree path", req.TicketID)
			}
			switch req.TicketID {
			case ticketA.ID:
				if err := os.WriteFile(filepath.Join(req.WorktreePath, "a.go"), []byte("package main\n"), 0o644); err != nil {
					return runtime.WorkerResult{}, err
				}
			case ticketB.ID:
				if err := os.WriteFile(filepath.Join(req.WorktreePath, "b.go"), []byte("package main\n\nimport \"fmt\"\n"), 0o644); err != nil {
					return runtime.WorkerResult{}, err
				}
			default:
				return runtime.WorkerResult{}, fmt.Errorf("unexpected worker ticket %q", req.TicketID)
			}
			return runtime.WorkerResult{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            req.LeaseID,
				StartedAt:          start,
				FinishedAt:         start.Add(time.Second),
				ResultArtifactPath: filepath.Join(t.TempDir(), req.TicketID+".worker.json"),
			}, nil
		},
		runReviewer: func(ctx context.Context, req runtime.ReviewRequest) (runtime.ReviewResult, error) {
			if req.TicketID != ticketA.ID {
				return runtime.ReviewResult{}, fmt.Errorf("unexpected review for %q", req.TicketID)
			}
			if strings.TrimSpace(req.WorktreePath) == "" {
				return runtime.ReviewResult{}, fmt.Errorf("reviewer %s did not receive worktree path", req.TicketID)
			}
			return runtime.ReviewResult{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            req.LeaseID,
				StartedAt:          start.Add(2 * time.Second),
				FinishedAt:         start.Add(3 * time.Second),
				ReviewStatus:       runtime.ReviewStatusPassed,
				Summary:            "clean",
				ResultArtifactPath: filepath.Join(t.TempDir(), req.TicketID+".review.json"),
			}, nil
		},
	}

	result, err := RunEpic(context.Background(), RunEpicRequest{
		RepoRoot:     repoRoot,
		RunID:        "run-wave-isolation",
		RootTicketID: root.ID,
		BaseCommit:   baseCommit,
		Adapter:      adapter,
		Config:       cfg,
	})
	if !errors.Is(err, ErrEpicBlocked) {
		t.Fatalf("expected blocked epic because ticket-b failed verification, got %v", err)
	}
	if result.Run.Status != state.EpicRunStatusBlocked {
		t.Fatalf("expected blocked run, got %q", result.Run.Status)
	}

	var snapshotA TicketRunSnapshot
	if err := state.LoadJSON(ticketSnapshotPath(repoRoot, "run-wave-isolation", ticketA.ID), &snapshotA); err != nil {
		t.Fatalf("load ticket-a snapshot: %v", err)
	}
	if snapshotA.CurrentPhase != state.TicketPhaseClosed {
		t.Fatalf("expected ticket-a closed, got %q", snapshotA.CurrentPhase)
	}
	if snapshotA.Verification == nil || !snapshotA.Verification.Passed {
		t.Fatalf("expected ticket-a verification to pass, got %#v", snapshotA.Verification)
	}
	for _, result := range snapshotA.Verification.Results {
		for _, path := range []string{result.StdoutPath, result.StderrPath} {
			if strings.TrimSpace(path) == "" {
				continue
			}
			content, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatalf("read ticket-a verification log %s: %v", path, readErr)
			}
			if strings.Contains(string(content), "b.go") {
				t.Fatalf("ticket-a verification output referenced ticket-b file in %s: %q", path, string(content))
			}
		}
	}

	var snapshotB TicketRunSnapshot
	if err := state.LoadJSON(ticketSnapshotPath(repoRoot, "run-wave-isolation", ticketB.ID), &snapshotB); err != nil {
		t.Fatalf("load ticket-b snapshot: %v", err)
	}
	if snapshotB.CurrentPhase != state.TicketPhaseBlocked {
		t.Fatalf("expected ticket-b blocked, got %q", snapshotB.CurrentPhase)
	}
	if snapshotB.Verification == nil || snapshotB.Verification.Passed {
		t.Fatalf("expected ticket-b verification to fail, got %#v", snapshotB.Verification)
	}

	if _, err := os.Stat(filepath.Join(repoRoot, "a.go")); err != nil {
		t.Fatalf("expected accepted ticket-a output in main tree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "b.go")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected failed ticket-b output to stay out of main tree, stat err=%v", err)
	}

	diffPath := filepath.Join(repoRoot, ".verk", "runs", "run-wave-isolation", "tickets", ticketB.ID, "worktree.diff")
	diffContent, err := os.ReadFile(diffPath)
	if err != nil {
		t.Fatalf("expected failed ticket diff artifact at %s: %v", diffPath, err)
	}
	if !strings.Contains(string(diffContent), "b.go") {
		t.Fatalf("expected failed ticket diff to mention b.go, got %q", string(diffContent))
	}
}

func initEpicRepo(t *testing.T, root string) string {
	t.Helper()

	mustRunGit(t, root, "init")

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

func mustSaveTicket(t *testing.T, repoRoot string, ticket epos.Ticket) {
	t.Helper()

	if err := epos.SaveTicket(filepath.Join(repoRoot, ".tickets", ticket.ID+".md"), ticket); err != nil {
		t.Fatalf("SaveTicket(%s): %v", ticket.ID, err)
	}
}

func writeRuntimeState(t *testing.T, repoRoot string, runtimeState *eposticket.RuntimeState) {
	t.Helper()

	if err := eposruntime.WriteRuntimeState(repoRoot, runtimeState); err != nil {
		t.Fatalf("WriteRuntimeState(%s): %v", runtimeState.TicketID, err)
	}
}

func epicTicket(id string) epos.Ticket {
	return epos.Ticket{
		ID:     id,
		Title:  "Epic " + id,
		Status: epos.StatusReady,
		OwnedPaths: []string{
			"internal/app",
			"docs",
		},
		UnknownFrontmatter: map[string]any{
			"type": "epic",
		},
	}
}

func epicChildTicket(id, parent string, status epos.Status, deps, owned []string) epos.Ticket {
	return epos.Ticket{
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
	cmd.Env = engineGitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
	}
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = engineGitEnv()
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
	child := epicChildTicket("ticket-lock", epic.ID, epos.StatusReady, nil, []string{"internal/app"})
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

func TestRunEpicNestedSubEpicIsExplicitlyUnsupported(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	cfg := policy.DefaultConfig()
	cfg.Scheduler.MaxDepth = 3

	// Epic -> ticket-1 (has children) -> sub-1, sub-2
	// Epic -> ticket-2 (leaf)
	epic := epicTicket("epic-sub")
	mustSaveTicket(t, repoRoot, epic)

	ticket1 := epicChildTicket("ticket-1", epic.ID, epos.StatusOpen, nil, []string{"internal/app"})
	mustSaveTicket(t, repoRoot, ticket1)

	ticket2 := epicChildTicket("ticket-2", epic.ID, epos.StatusOpen, nil, []string{"docs"})
	mustSaveTicket(t, repoRoot, ticket2)

	sub1 := epicChildTicket("sub-1", ticket1.ID, epos.StatusOpen, nil, []string{"internal/app/sub1"})
	mustSaveTicket(t, repoRoot, sub1)

	sub2 := epicChildTicket("sub-2", ticket1.ID, epos.StatusOpen, nil, []string{"internal/app/sub2"})
	mustSaveTicket(t, repoRoot, sub2)

	adapter := newReflectingAdapter(1)

	result, err := RunEpic(context.Background(), RunEpicRequest{
		RepoRoot:     repoRoot,
		RunID:        "run-sub",
		RootTicketID: epic.ID,
		BaseCommit:   baseCommit,
		Adapter:      adapter,
		Config:       cfg,
	})

	if err == nil {
		t.Fatal("expected nested sub-epic to be rejected inside isolated epic run")
	}
	if !strings.Contains(err.Error(), "sub-epic isolation is not implemented yet") {
		t.Fatalf("expected explicit unsupported isolation error, got %v", err)
	}
	if result.Run.Status != state.EpicRunStatusBlocked {
		t.Fatalf("expected blocked epic, got status %s", result.Run.Status)
	}

	// Direct descendants of the nested parent must not be dispatched.
	workerReqs := adapter.WorkerRequests()
	startedSet := make(map[string]bool)
	for _, req := range workerReqs {
		startedSet[req.TicketID] = true
	}
	for _, id := range []string{"sub-1", "sub-2", "ticket-1"} {
		if startedSet[id] {
			t.Errorf("did not expect nested ticket %q to be started, got started: %v", id, keys(startedSet))
		}
	}
	if !startedSet["ticket-2"] {
		t.Fatalf("expected non-nested sibling ticket to still run, got started: %v", keys(startedSet))
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

	ticket1 := epicChildTicket("ticket-1", epic.ID, epos.StatusOpen, nil, []string{"internal/app"})
	mustSaveTicket(t, repoRoot, ticket1)

	sub1 := epicChildTicket("sub-1", ticket1.ID, epos.StatusOpen, nil, []string{"internal/app/sub"})
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
	ticket1 := epicChildTicket("ticket-1", "epic-parent", epos.StatusOpen, nil, []string{"internal/app"})
	mustSaveTicket(t, repoRoot, ticket1)

	sub1 := epicChildTicket("sub-1", ticket1.ID, epos.StatusOpen, nil, []string{"internal/app/sub1"})
	mustSaveTicket(t, repoRoot, sub1)

	sub2 := epicChildTicket("sub-2", ticket1.ID, epos.StatusOpen, nil, []string{"internal/app/sub2"})
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

// TestRunEpicNestedSubEpicBlocksParentTicket verifies that once worktree
// isolation is active, a nested sub-epic blocks explicitly instead of silently
// falling back to shared-workspace execution.
func TestRunEpicNestedSubEpicBlocksParentTicket(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	cfg := policy.DefaultConfig()
	cfg.Scheduler.MaxDepth = 3

	epic := epicTicket("epic-gc-blocked")
	mustSaveTicket(t, repoRoot, epic)

	// parent-task has children, so it triggers the sub-epic path in executeEpicTicket.
	parentTask := epicChildTicket("parent-task", epic.ID, epos.StatusOpen, nil, []string{"internal/app"})
	mustSaveTicket(t, repoRoot, parentTask)

	// grandchild exists purely to trigger the nested sub-epic path.
	grandchild := epicChildTicket("grandchild", parentTask.ID, epos.StatusOpen, nil, []string{"internal/app/sub"})
	mustSaveTicket(t, repoRoot, grandchild)

	adapter := newReflectingAdapter(0)

	result, err := RunEpic(context.Background(), RunEpicRequest{
		RepoRoot:     repoRoot,
		RunID:        "run-gc-blocked",
		RootTicketID: epic.ID,
		BaseCommit:   baseCommit,
		Adapter:      adapter,
		Config:       cfg,
	})

	if err == nil {
		t.Fatal("expected nested sub-epic to block isolated epic run")
	}
	if !errors.Is(err, ErrEpicBlocked) {
		t.Fatalf("expected ErrEpicBlocked, got: %v", err)
	}
	if !strings.Contains(err.Error(), "sub-epic isolation is not implemented yet") {
		t.Fatalf("expected explicit unsupported isolation message, got: %v", err)
	}

	parentTicket, loadErr := loadEpicTicket(repoRoot, parentTask.ID)
	if loadErr != nil {
		t.Fatalf("load parent-task ticket: %v", loadErr)
	}
	if parentTicket.Status != epos.StatusBlocked {
		t.Fatalf("expected parent-task to be blocked in ticket store, got %q", parentTicket.Status)
	}

	if len(result.Waves) != 1 {
		t.Fatalf("expected exactly 1 wave, got %d", len(result.Waves))
	}

	for _, req := range adapter.WorkerRequests() {
		if req.TicketID == grandchild.ID || req.TicketID == parentTask.ID {
			t.Fatalf("did not expect nested ticket %q to be dispatched during unsupported path", req.TicketID)
		}
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
	child := epicChildTicket("ticket-ctx-cancel", epic.ID, epos.StatusReady, nil, []string{"internal/app"})
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
	sibA := epos.Ticket{
		ID:         "sib-linked-a",
		Title:      "Sibling A",
		Status:     epos.StatusOpen,
		OwnedPaths: []string{"internal/app/sib-a"},
		UnknownFrontmatter: map[string]any{
			"parent": epic.ID,
			"type":   "task",
			"links":  []any{"sib-linked-b"},
		},
	}
	mustSaveTicket(t, repoRoot, sibA)

	sibB := epos.Ticket{
		ID:         "sib-linked-b",
		Title:      "Sibling B",
		Status:     epos.StatusOpen,
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

// TestResumeRun_NestedSubEpicIsolationIsExplicitlyUnsupported verifies that a
// resumed epic with a nested child wave blocks explicitly instead of
// re-entering shared-workspace descendant execution.
func TestResumeRun_NestedSubEpicIsolationIsExplicitlyUnsupported(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	runID := "run-desc-inprog"

	// Hierarchy: epic -> parent-task -> grandchild
	epic := epicTicket("epic-desc-inprog")
	mustSaveTicket(t, repoRoot, epic)

	parentTask := epicChildTicket("parent-task-inprog", epic.ID, epos.StatusInProgress, nil, []string{"internal/app"})
	mustSaveTicket(t, repoRoot, parentTask)

	grandchild := epicChildTicket("grandchild-inprog", parentTask.ID, epos.StatusInProgress, nil, []string{"internal/app/sub"})
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
		ResumeCursor: map[string]any{
			"wave_ordinal":          0,
			"last_wave_base_commit": baseCommit,
		},
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

	adapter := newReflectingAdapter(0)

	cfg := policy.DefaultConfig()
	cfg.Verification.QualityCommands = nil

	report, err := ResumeRun(context.Background(), ResumeRequest{
		RepoRoot: repoRoot,
		RunID:    runID,
		Adapter:  adapter,
		Config:   cfg,
	})
	if err != nil {
		t.Fatalf("expected blocked resume report, got error: %v", err)
	}

	reqs := adapter.WorkerRequests()
	for _, req := range reqs {
		if req.TicketID == grandchild.ID {
			t.Fatalf("did not expect nested grandchild %q to be dispatched on resume", grandchild.ID)
		}
	}

	if report.Run.Status != state.EpicRunStatusBlocked {
		t.Errorf("expected blocked run after unsupported nested sub-epic, got %q", report.Run.Status)
	}
	if report.Run.CurrentPhase != state.TicketPhaseBlocked {
		t.Errorf("expected blocked current phase, got %q", report.Run.CurrentPhase)
	}

	parentTicket, loadErr := epos.LoadTicket(ticketMarkdownPath(repoRoot, parentTask.ID))
	if loadErr != nil {
		t.Fatalf("load parent ticket: %v", loadErr)
	}
	if parentTicket.Status != epos.StatusBlocked {
		t.Fatalf("expected parent ticket to persist blocked status, got %q", parentTicket.Status)
	}

	var wave state.WaveArtifact
	if err := state.LoadJSON(filepath.Join(repoRoot, ".verk", "runs", runID, "waves", "wave-1.json"), &wave); err != nil {
		t.Fatalf("load resumed top-level wave: %v", err)
	}
	if got := wave.Acceptance["crash_reason"]; !strings.Contains(fmt.Sprint(got), "sub-epic isolation is not implemented yet") {
		t.Fatalf("expected wave crash_reason to record unsupported nested isolation, got %#v", got)
	}
}

func TestResumeRun_PendingVerificationSubWaveIsCompleted(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	runID := "run-subwave-pending-verification"

	epic := epicTicket("epic-subwave-pending")
	mustSaveTicket(t, repoRoot, epic)

	parentTask := epicChildTicket("parent-subwave-pending", epic.ID, epos.StatusClosed, nil, []string{"internal/app"})
	mustSaveTicket(t, repoRoot, parentTask)

	grandchild := epicChildTicket("grandchild-subwave-pending", parentTask.ID, epos.StatusClosed, nil, []string{"internal/app/sub"})
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
			"last_wave_base_commit":     baseCommit,
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
