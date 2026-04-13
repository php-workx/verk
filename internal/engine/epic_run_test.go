package engine

import (
	"context"
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
	runtimefake "verk/internal/adapters/runtime/fake"
	"verk/internal/adapters/ticketstore/tkmd"
	"verk/internal/policy"
	"verk/internal/state"
)

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

func TestAcceptWaveRejectsScopeViolation(t *testing.T) {
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

	if _, err := AcceptWave(req); err == nil {
		t.Fatal("expected scope violation to fail acceptance")
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
	if err != nil {
		t.Fatalf("RunEpic returned error: %v", err)
	}

	// Both open and ready tickets should have been scheduled
	if adapter.maxConcurrent() < 2 {
		t.Fatalf("expected both open and ready tickets to run concurrently, max concurrent was %d", adapter.maxConcurrent())
	}

	// Epic should not be completed because blocked ticket remains
	if result.Run.Status == state.EpicRunStatusCompleted {
		t.Fatalf("expected epic to stay incomplete while blocked ticket remains")
	}
}

func TestRunEpicFailsOnScopeViolation(t *testing.T) {
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
				LeaseID:            "lease-run-scope-ticket-scope",
				StartedAt:          epicTestStart(),
				FinishedAt:         epicTestStart().Add(time.Second),
				ResultArtifactPath: filepath.Join(repoRoot, "worker.json"),
			},
		},
		[]runtime.ReviewResult{
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            "lease-run-scope-ticket-scope",
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
	if err != nil {
		t.Fatalf("RunEpic returned error: %v", err)
	}

	if _, statErr := os.Stat(touchOutsideScope); statErr != nil {
		t.Fatalf("expected scope violation fixture file to exist: %v", statErr)
	}
	if result.Run.Status != state.EpicRunStatusBlocked {
		t.Fatalf("expected epic to be blocked, got %q", result.Run.Status)
	}
	if len(result.Waves) == 0 || result.Waves[0].Status != state.WaveStatusFailed {
		t.Fatalf("expected failed wave, got %#v", result.Waves)
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
				LeaseID:            "lease-run-blocked-ticket-ready",
				StartedAt:          epicTestStart(),
				FinishedAt:         epicTestStart().Add(time.Second),
				ResultArtifactPath: filepath.Join(repoRoot, "worker.json"),
			},
		},
		[]runtime.ReviewResult{
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            "lease-run-blocked-ticket-ready",
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
	if err != nil {
		t.Fatalf("RunEpic returned error: %v", err)
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
					LeaseID:            "lease-run-runtime-ticket-codex",
					StartedAt:          epicTestStart(),
					FinishedAt:         epicTestStart().Add(time.Second),
					ResultArtifactPath: filepath.Join(repoRoot, "codex-worker.json"),
				},
				{
					Status:             runtime.WorkerStatusDone,
					RetryClass:         runtime.RetryClassTerminal,
					LeaseID:            "lease-run-runtime-ticket-codex",
					StartedAt:          epicTestStart().Add(4 * time.Second),
					FinishedAt:         epicTestStart().Add(5 * time.Second),
					ResultArtifactPath: filepath.Join(repoRoot, "codex-worker-2.json"),
				},
			},
			[]runtime.ReviewResult{
				{
					Status:             runtime.WorkerStatusDone,
					RetryClass:         runtime.RetryClassTerminal,
					LeaseID:            "lease-run-runtime-ticket-codex",
					StartedAt:          epicTestStart().Add(2 * time.Second),
					FinishedAt:         epicTestStart().Add(3 * time.Second),
					ReviewStatus:       runtime.ReviewStatusPassed,
					Summary:            "clean",
					ResultArtifactPath: filepath.Join(repoRoot, "codex-review.json"),
				},
				{
					Status:             runtime.WorkerStatusDone,
					RetryClass:         runtime.RetryClassTerminal,
					LeaseID:            "lease-run-runtime-ticket-codex",
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
					LeaseID:            "lease-run-runtime-ticket-claude",
					StartedAt:          epicTestStart().Add(8 * time.Second),
					FinishedAt:         epicTestStart().Add(9 * time.Second),
					ResultArtifactPath: filepath.Join(repoRoot, "claude-worker.json"),
				},
				{
					Status:             runtime.WorkerStatusDone,
					RetryClass:         runtime.RetryClassTerminal,
					LeaseID:            "lease-run-runtime-ticket-claude",
					StartedAt:          epicTestStart().Add(12 * time.Second),
					FinishedAt:         epicTestStart().Add(13 * time.Second),
					ResultArtifactPath: filepath.Join(repoRoot, "claude-worker-2.json"),
				},
			},
			[]runtime.ReviewResult{
				{
					Status:             runtime.WorkerStatusDone,
					RetryClass:         runtime.RetryClassTerminal,
					LeaseID:            "lease-run-runtime-ticket-claude",
					StartedAt:          epicTestStart().Add(10 * time.Second),
					FinishedAt:         epicTestStart().Add(11 * time.Second),
					ReviewStatus:       runtime.ReviewStatusPassed,
					Summary:            "clean",
					ResultArtifactPath: filepath.Join(repoRoot, "claude-review.json"),
				},
				{
					Status:             runtime.WorkerStatusDone,
					RetryClass:         runtime.RetryClassTerminal,
					LeaseID:            "lease-run-runtime-ticket-claude",
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
	if !reflect.DeepEqual(got, []string{"claude", "codex"}) {
		t.Fatalf("unexpected requested runtimes: %#v", requestedRuntimes)
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
				LeaseID:            "lease-run-baseline-ticket-baseline",
				StartedAt:          epicTestStart(),
				FinishedAt:         epicTestStart().Add(time.Second),
				ResultArtifactPath: filepath.Join(repoRoot, "worker-baseline.json"),
			},
		},
		[]runtime.ReviewResult{
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            "lease-run-baseline-ticket-baseline",
				StartedAt:          epicTestStart().Add(2 * time.Second),
				FinishedAt:         epicTestStart().Add(3 * time.Second),
				ReviewStatus:       runtime.ReviewStatusPassed,
				Summary:            "clean",
				ResultArtifactPath: filepath.Join(repoRoot, "review-baseline.json"),
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
