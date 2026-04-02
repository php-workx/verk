package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
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
		ClaimsReleased:       true,
		PersistenceSucceeded: true,
	}

	if _, err := AcceptWave(req); err == nil {
		t.Fatal("expected scope violation to fail acceptance")
	}
}

func TestRunEpicUsesReadyPredicate(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	cfg := policy.DefaultConfig()

	epic := epicTicket("epic-ready")
	mustSaveTicket(t, repoRoot, epic)

	ready := epicChildTicket("ticket-ready", epic.ID, tkmd.StatusReady, nil, []string{"internal/app"})
	mustSaveTicket(t, repoRoot, ready)

	ignored := epicChildTicket("ticket-open", epic.ID, tkmd.StatusOpen, nil, []string{"docs"})
	mustSaveTicket(t, repoRoot, ignored)

	adapter := runtimefake.New(
		[]runtime.WorkerResult{
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            "lease-run-ready-ticket-ready",
				StartedAt:          epicTestStart(),
				FinishedAt:         epicTestStart().Add(time.Second),
				ResultArtifactPath: filepath.Join(repoRoot, "worker.json"),
			},
		},
		[]runtime.ReviewResult{
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            "lease-run-ready-ticket-ready",
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
		RunID:        "run-ready",
		RootTicketID: epic.ID,
		BaseCommit:   baseCommit,
		Adapter:      adapter,
		Config:       cfg,
	})
	if err != nil {
		t.Fatalf("RunEpic returned error: %v", err)
	}

	if len(adapter.WorkerRequests()) != 1 {
		t.Fatalf("expected only the ready ticket to run, got %d worker requests", len(adapter.WorkerRequests()))
	}
	if len(adapter.ReviewRequests()) != 1 {
		t.Fatalf("expected one review request, got %d", len(adapter.ReviewRequests()))
	}
	if result.Run.Status == state.EpicRunStatusCompleted {
		t.Fatalf("expected epic to stay incomplete while open ticket remains")
	}

	loadedOpen, err := tkmd.LoadTicket(filepath.Join(repoRoot, ".tickets", ignored.ID+".md"))
	if err != nil {
		t.Fatalf("load open ticket: %v", err)
	}
	if loadedOpen.Status != tkmd.StatusOpen {
		t.Fatalf("expected open ticket to remain open, got %q", loadedOpen.Status)
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

func epicChildTicket(id, parent string, status tkmd.Status, deps []string, owned []string) tkmd.Ticket {
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
