package bench

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- test helpers ---

// minimalMatrix returns a worker-only matrix valid for testing.
// Uses fixed-reviewer design (valid with worker-only) with a single profile
// that pairs with itself (fixed-reviewer with one profile).
func minimalMatrix() Matrix {
	return Matrix{
		Mode:             ModeWorkerOnly,
		ComparisonDesign: designFixedReviewer,
		Profiles: []MatrixProfile{
			{
				ID:     "p1",
				Worker: ModelRef{Runtime: "test", Model: "test-model"},
			},
		},
	}
}

// fakeProvider is a minimal Provider for runner tests.
type fakeProvider struct {
	name  string
	tasks []Task
	mu    sync.Mutex
	calls []string // record of LoadTasks calls
}

func (f *fakeProvider) Name() string { return f.name }
func (f *fakeProvider) Suites() []SuiteMeta {
	return []SuiteMeta{{Name: "fake-suite", Provider: f.name, TaskCount: len(f.tasks)}}
}

func (f *fakeProvider) LoadTasks(_ string) ([]Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "LoadTasks")
	return f.tasks, nil
}

func (f *fakeProvider) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		SupportedModes: []BenchmarkMode{ModeWorkerOnly, ModeFullVerk},
		CachePolicy:    "locked",
	}
}

// noopExecutor returns a solved result immediately.
func noopExecutor(_ context.Context, task Task, profile MatrixProfile, _ string) TaskResult {
	return TaskResult{
		TaskID:    task.ID,
		ProfileID: profile.ID,
		Status:    TaskStatusSolved,
		Score:     Score{Solved: true},
	}
}

// makeTempGitRepo creates a temporary directory that is a real git repo.
func makeTempGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "Test")
	run("commit", "--allow-empty", "-m", "init")
	return dir
}

// makeDirtyGitRepo creates a temp git repo with an untracked dirty file.
func makeDirtyGitRepo(t *testing.T) string {
	t.Helper()
	dir := makeTempGitRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// --- tests ---

func TestRun_LocksManifestBeforeExecution(t *testing.T) {
	outDir := t.TempDir()
	repoRoot := makeTempGitRepo(t)

	tasks := []Task{
		{ID: "task-1", Suite: "fake-suite"},
		{ID: "task-2", Suite: "fake-suite"},
	}
	provider := &fakeProvider{name: "fake", tasks: tasks}

	manifestChecked := false
	executor := func(ctx context.Context, task Task, profile MatrixProfile, workDir string) TaskResult {
		if !manifestChecked {
			// On the very first execution call, assert manifest.json already exists.
			manifestChecked = true
			manifestPath := filepath.Join(outDir, "manifest.json")
			if _, err := os.Stat(manifestPath); err != nil {
				t.Errorf("manifest.json does not exist before first task execution: %v", err)
			}
		}
		return noopExecutor(ctx, task, profile, workDir)
	}

	_, err := Run(context.Background(), RunOptions{
		RepoRoot:  repoRoot,
		OutDir:    outDir,
		SuiteName: "fake-suite",
		Provider:  provider,
		Matrix:    minimalMatrix(),
		Executor:  executor,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !manifestChecked {
		t.Error("executor was never called — cannot verify manifest-before-execution invariant")
	}

	// Also verify both task IDs are in the manifest.
	data, err := os.ReadFile(filepath.Join(outDir, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest.json: %v", err)
	}
	for _, id := range []string{"task-1", "task-2"} {
		if !strings.Contains(string(data), id) {
			t.Errorf("manifest.json missing task ID %q", id)
		}
	}
}

func TestRun_WritesCheckpointAfterEachTask(t *testing.T) {
	outDir := t.TempDir()
	repoRoot := makeTempGitRepo(t)

	tasks := []Task{
		{ID: "task-a", Suite: "fake-suite"},
		{ID: "task-b", Suite: "fake-suite"},
	}
	provider := &fakeProvider{name: "fake", tasks: tasks}

	_, err := Run(context.Background(), RunOptions{
		RepoRoot:  repoRoot,
		OutDir:    outDir,
		SuiteName: "fake-suite",
		Provider:  provider,
		Matrix:    minimalMatrix(),
		Executor:  noopExecutor,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	checkpointPath := filepath.Join(outDir, "checkpoint.json")
	data, err := os.ReadFile(checkpointPath)
	if err != nil {
		t.Fatalf("checkpoint.json not found: %v", err)
	}

	// Both tasks should be in completed_tasks.
	for _, id := range []string{"task-a", "task-b"} {
		if !strings.Contains(string(data), id) {
			t.Errorf("checkpoint.json missing task ID %q; content:\n%s", id, data)
		}
	}
}

func TestRun_WritesCompleteMarkerLast(t *testing.T) {
	outDir := t.TempDir()
	repoRoot := makeTempGitRepo(t)

	tasks := []Task{
		{ID: "t1", Suite: "fake-suite"},
	}
	provider := &fakeProvider{name: "fake", tasks: tasks}

	_, err := Run(context.Background(), RunOptions{
		RepoRoot:  repoRoot,
		OutDir:    outDir,
		SuiteName: "fake-suite",
		Provider:  provider,
		Matrix:    minimalMatrix(),
		Executor:  noopExecutor,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	completePath := filepath.Join(outDir, "complete.json")
	resultsPath := filepath.Join(outDir, "results.jsonl")

	completeInfo, err := os.Stat(completePath)
	if err != nil {
		t.Fatalf("complete.json not found: %v", err)
	}
	resultsInfo, err := os.Stat(resultsPath)
	if err != nil {
		t.Fatalf("results.jsonl not found: %v", err)
	}

	// complete.json must not be older than results.jsonl.
	// Use ModTime comparison; on fast systems both may be equal (same second),
	// so we only assert complete >= results.
	if completeInfo.ModTime().Before(resultsInfo.ModTime()) {
		t.Errorf("complete.json (%v) is older than results.jsonl (%v); complete marker must be written last",
			completeInfo.ModTime(), resultsInfo.ModTime())
	}
}

func TestRun_RejectsDirtyWorktreeWhenNotAllowed(t *testing.T) {
	outDir := t.TempDir()
	repoRoot := makeDirtyGitRepo(t)

	provider := &fakeProvider{name: "fake", tasks: []Task{{ID: "t1", Suite: "fake-suite"}}}

	_, err := Run(context.Background(), RunOptions{
		RepoRoot:   repoRoot,
		OutDir:     outDir,
		SuiteName:  "fake-suite",
		Provider:   provider,
		Matrix:     minimalMatrix(),
		AllowDirty: false,
		Executor:   noopExecutor,
	})
	if err == nil {
		t.Fatal("expected error for dirty worktree, got nil")
	}
	if !strings.Contains(err.Error(), "dirty") {
		t.Errorf("expected error to mention 'dirty', got: %v", err)
	}
}

func TestRun_AllowsDirtyWorktreeWhenFlagSet(t *testing.T) {
	outDir := t.TempDir()
	repoRoot := makeDirtyGitRepo(t)

	provider := &fakeProvider{name: "fake", tasks: []Task{{ID: "t1", Suite: "fake-suite"}}}

	result, err := Run(context.Background(), RunOptions{
		RepoRoot:   repoRoot,
		OutDir:     outDir,
		SuiteName:  "fake-suite",
		Provider:   provider,
		Matrix:     minimalMatrix(),
		AllowDirty: true,
		Executor:   noopExecutor,
	})
	if err != nil {
		t.Fatalf("expected no error with --allow-dirty, got: %v", err)
	}
	if result.RunID == "" {
		t.Error("expected non-empty RunID")
	}

	// Manifest should note that results are non-comparable.
	data, err := os.ReadFile(filepath.Join(outDir, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest.json: %v", err)
	}
	if !strings.Contains(string(data), "non-comparable") {
		t.Errorf("manifest.json should mark non-comparable for dirty run; got:\n%s", data)
	}
}

func TestRun_PerTaskTimeoutTriggersFailure(t *testing.T) {
	outDir := t.TempDir()
	repoRoot := makeTempGitRepo(t)

	tasks := []Task{{ID: "slow-task", Suite: "fake-suite"}}
	provider := &fakeProvider{name: "fake", tasks: tasks}

	blockingExecutor := func(ctx context.Context, task Task, profile MatrixProfile, workDir string) TaskResult {
		// Block until context is cancelled.
		<-ctx.Done()
		return TaskResult{
			TaskID:    task.ID,
			ProfileID: profile.ID,
			Status:    TaskStatusUnsolved,
			Failure:   FailureSetup,
			Notes:     "timed out",
		}
	}

	_, err := Run(context.Background(), RunOptions{
		RepoRoot:  repoRoot,
		OutDir:    outDir,
		SuiteName: "fake-suite",
		Provider:  provider,
		Matrix:    minimalMatrix(),
		Budgets: RunBudgets{
			PerTaskTimeout: 50 * time.Millisecond,
		},
		Executor: blockingExecutor,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// results.jsonl should contain the timed-out task.
	data, err := os.ReadFile(filepath.Join(outDir, "results.jsonl"))
	if err != nil {
		t.Fatalf("results.jsonl not found: %v", err)
	}
	if !strings.Contains(string(data), "slow-task") {
		t.Errorf("results.jsonl should contain slow-task; got:\n%s", data)
	}
}

func TestRun_RejectsInvalidMatrix(t *testing.T) {
	outDir := t.TempDir()
	repoRoot := makeTempGitRepo(t)

	provider := &fakeProvider{name: "fake", tasks: []Task{{ID: "t1", Suite: "fake-suite"}}}

	badMatrix := Matrix{
		Mode:             "invalid-mode",
		ComparisonDesign: designExploratory,
		Profiles: []MatrixProfile{
			{ID: "p1", Worker: ModelRef{Runtime: "x", Model: "y"}},
		},
	}

	_, err := Run(context.Background(), RunOptions{
		RepoRoot:  repoRoot,
		OutDir:    outDir,
		SuiteName: "fake-suite",
		Provider:  provider,
		Matrix:    badMatrix,
	})
	if err == nil {
		t.Fatal("expected error for invalid matrix, got nil")
	}
	if !strings.Contains(err.Error(), "invalid mode") {
		t.Errorf("expected 'invalid mode' in error, got: %v", err)
	}
}

func TestRun_MaxTasksBudget(t *testing.T) {
	outDir := t.TempDir()
	repoRoot := makeTempGitRepo(t)

	tasks := make([]Task, 5)
	for i := range tasks {
		tasks[i] = Task{ID: fmt.Sprintf("task-%d", i), Suite: "fake-suite"}
	}
	provider := &fakeProvider{name: "fake", tasks: tasks}

	var mu sync.Mutex
	executed := 0
	countingExecutor := func(ctx context.Context, task Task, profile MatrixProfile, workDir string) TaskResult {
		mu.Lock()
		executed++
		mu.Unlock()
		return noopExecutor(ctx, task, profile, workDir)
	}

	_, err := Run(context.Background(), RunOptions{
		RepoRoot:  repoRoot,
		OutDir:    outDir,
		SuiteName: "fake-suite",
		Provider:  provider,
		Matrix:    minimalMatrix(),
		Budgets:   RunBudgets{MaxTasks: 2},
		Executor:  countingExecutor,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if executed != 2 {
		t.Errorf("expected exactly 2 tasks executed (MaxTasks=2), got %d", executed)
	}
}

func TestRun_DefaultExecutorUnsolved_NoScorer(t *testing.T) {
	// A provider that does NOT implement Scorer should yield FailureOther.
	provider := &fakeProvider{name: "fake", tasks: nil}
	exec := DefaultExecutor(provider)
	task := Task{ID: "t1"}
	profile := MatrixProfile{ID: "p1"}
	result := exec(context.Background(), task, profile, t.TempDir())
	if result.Status != TaskStatusUnsolved {
		t.Errorf("expected unsolved, got %q", result.Status)
	}
	if result.Failure != FailureOther {
		t.Errorf("expected FailureOther, got %q", result.Failure)
	}
}

func TestLoadCheckpoint_MissingDir(t *testing.T) {
	_, found, err := LoadCheckpoint(t.TempDir())
	if err != nil {
		t.Fatalf("LoadCheckpoint: unexpected error: %v", err)
	}
	if found {
		t.Error("expected found=false for empty dir")
	}
}

func TestCellID_NoReviewerUsesNoneSuffix(t *testing.T) {
	p := MatrixProfile{
		ID:     "worker-a",
		Worker: ModelRef{Runtime: "claude", Model: "claude-3-5-sonnet"},
		// Reviewer deliberately left zero-value.
	}
	got := cellID(p)
	if got != "worker-a__none" {
		t.Errorf("cellID (no reviewer): got %q, want %q", got, "worker-a__none")
	}
}

func TestCellID_WithReviewerUsesModel(t *testing.T) {
	p := MatrixProfile{
		ID:       "worker-a",
		Worker:   ModelRef{Runtime: "claude", Model: "claude-3-5-sonnet"},
		Reviewer: ModelRef{Runtime: "claude", Model: "claude-3-opus"},
	}
	got := cellID(p)
	if got != "worker-a__claude-3-opus" {
		t.Errorf("cellID (with reviewer): got %q, want %q", got, "worker-a__claude-3-opus")
	}
}

func TestRun_WorkspacePathUsesCellID(t *testing.T) {
	outDir := t.TempDir()
	repoRoot := makeTempGitRepo(t)

	tasks := []Task{{ID: "task-ws", Suite: "fake-suite"}}
	provider := &fakeProvider{name: "fake", tasks: tasks}

	var capturedWorkDir string
	executor := func(ctx context.Context, task Task, profile MatrixProfile, workDir string) TaskResult {
		capturedWorkDir = workDir
		return noopExecutor(ctx, task, profile, workDir)
	}

	matrix := minimalMatrix() // worker-only, profile ID="p1", no reviewer
	_, err := Run(context.Background(), RunOptions{
		RepoRoot:  repoRoot,
		OutDir:    outDir,
		SuiteName: "fake-suite",
		Provider:  provider,
		Matrix:    matrix,
		Executor:  executor,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Workspace should use cellID format: <profile-id>__none for worker-only.
	expectedSuffix := filepath.Join("tasks", "task-ws", "p1__none", "work")
	if !strings.HasSuffix(capturedWorkDir, expectedSuffix) {
		t.Errorf("workspace path %q does not end with %q", capturedWorkDir, expectedSuffix)
	}
}
