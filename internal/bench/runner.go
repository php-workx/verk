package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// RunOptions controls a benchmark run.
type RunOptions struct {
	RepoRoot   string
	OutDir     string // where artifacts land (default: <repoRoot>/.verk/bench/runs/<run-id>)
	SuiteName  string
	Provider   Provider
	Matrix     Matrix
	AllowDirty bool
	Budgets    RunBudgets
	Clock      func() time.Time // injectable for tests
	Executor   Executor         // injectable for tests; defaults to DefaultExecutor(provider)
}

// RunBudgets bounds resource consumption.
type RunBudgets struct {
	MaxTasks       int           // 0 = unbounded
	PerTaskTimeout time.Duration // 0 = unbounded
}

// RunSummary is returned by Run and extends RunResult with the runtime output directory.
// RunResult (defined in report.go) carries the persisted fields; OutDir is the
// ephemeral filesystem path that was written during this execution.
type RunSummary struct {
	RunResult
	OutDir string `json:"out_dir"`
}

// Executor is the function that actually runs a task against a profile.
// The default executor uses the provider's scoring function (if available)
// against the prepared workspace; tests inject custom executors.
type Executor func(ctx context.Context, task Task, profile MatrixProfile, workDir string) TaskResult

// Scorer is an optional interface providers may implement.
type Scorer interface {
	Score(task Task, workspaceDir string) Score
}

// WorkspacePreparer is an optional interface providers may implement.
type WorkspacePreparer interface {
	PrepareWorkspace(task Task, dir string) error
}

// DefaultExecutor returns an Executor that uses provider scoring if available,
// otherwise returns an unsolved result with FailureOther.
func DefaultExecutor(provider Provider) Executor {
	return func(ctx context.Context, task Task, profile MatrixProfile, workDir string) TaskResult {
		start := time.Now()
		scorer, ok := provider.(Scorer)
		if !ok {
			return TaskResult{
				TaskID:     task.ID,
				ProfileID:  profile.ID,
				Status:     TaskStatusUnsolved,
				Failure:    FailureOther,
				DurationMS: time.Since(start).Milliseconds(),
				Notes:      "provider does not implement Scorer; skipped",
			}
		}
		score := scorer.Score(task, workDir)
		status := TaskStatusUnsolved
		if score.Solved {
			status = TaskStatusSolved
		}
		return TaskResult{
			TaskID:     task.ID,
			ProfileID:  profile.ID,
			Status:     status,
			Score:      score,
			DurationMS: time.Since(start).Milliseconds(),
		}
	}
}

// clock returns the effective clock function (defaults to time.Now).
func (opts RunOptions) clock() func() time.Time {
	if opts.Clock != nil {
		return opts.Clock
	}
	return time.Now
}

// Run executes the suite. It locks the task manifest, executes each
// (task, profile) pair, writes per-task results atomically, writes a
// checkpoint after each task, and writes the complete marker only after
// all reports are persisted.
func Run(ctx context.Context, opts RunOptions) (RunSummary, error) {
	now := opts.clock()

	// 1. Validate matrix.
	if err := ValidateMatrix(opts.Matrix); err != nil {
		return RunSummary{}, err
	}

	// 2. Dirty worktree check.
	dirtyLabels, err := checkDirtyWorktree(opts.RepoRoot, opts.AllowDirty)
	if err != nil {
		return RunSummary{}, err
	}

	// 3. Generate run ID.
	runID := fmt.Sprintf("bench-%d", now().UnixNano())

	// 4. Resolve outDir.
	outDir := opts.OutDir
	if outDir == "" {
		outDir = filepath.Join(opts.RepoRoot, ".verk", "bench", "runs", runID)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return RunSummary{}, fmt.Errorf("bench: create outDir %q: %w", outDir, err)
	}

	startedAt := now()

	// 5. Lock manifest.
	tasks, err := opts.Provider.LoadTasks(opts.SuiteName)
	if err != nil {
		return RunSummary{}, fmt.Errorf("bench: load tasks: %w", err)
	}

	taskIDs := make([]string, len(tasks))
	for i, t := range tasks {
		taskIDs[i] = t.ID
	}

	manifest := LockedTaskManifest{
		Suite:    opts.SuiteName,
		LockedAt: now(),
		TaskIDs:  taskIDs,
	}
	if len(dirtyLabels) > 0 {
		manifest.SourceRef = "dirty:" + strings.Join(dirtyLabels, ",")
	}

	if err := writeJSONAtomic(filepath.Join(outDir, "manifest.json"), manifest); err != nil {
		return RunSummary{}, fmt.Errorf("bench: write manifest: %w", err)
	}

	// 6. Snapshot resolved profiles.
	snapshots := make([]ResolvedProfileSnapshot, len(opts.Matrix.Profiles))
	for i, p := range opts.Matrix.Profiles {
		snapshots[i] = ResolvedProfileSnapshot{
			MatrixProfile: p,
			ResolvedAt:    now(),
		}
	}
	if err := writeJSONAtomic(filepath.Join(outDir, "profiles.json"), snapshots); err != nil {
		return RunSummary{}, fmt.Errorf("bench: write profiles: %w", err)
	}

	// Resolve executor.
	executor := opts.Executor
	if executor == nil {
		executor = DefaultExecutor(opts.Provider)
	}

	// 7. Per-task loop.
	pairings := ExpandPairings(opts.Matrix)
	results := make([]TaskResult, 0, len(tasks)*len(pairings))
	completedTaskKeys := make([]string, 0, len(tasks)*len(pairings))

	resultsFile, err := os.OpenFile(filepath.Join(outDir, "results.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return RunSummary{}, fmt.Errorf("bench: create results.jsonl: %w", err)
	}
	defer resultsFile.Close()

	maxTasks := opts.Budgets.MaxTasks
	taskCount := 0

	for _, task := range tasks {
		for _, pair := range pairings {
			if maxTasks > 0 && taskCount >= maxTasks {
				break
			}

			workerProfile := pair[0]
			result, skip, ferr := runOnePair(ctx, opts, executor, outDir, task, workerProfile)
			if ferr != nil {
				return RunSummary{}, ferr
			}

			results = append(results, result)
			if skip {
				continue
			}

			completedTaskKeys = append(completedTaskKeys, task.ID+"/"+workerProfile.ID)

			// Atomic append to results.jsonl.
			line, merr := json.Marshal(result)
			if merr != nil {
				return RunSummary{}, fmt.Errorf("bench: marshal result for task %q: %w", task.ID, merr)
			}
			if _, werr := resultsFile.Write(append(line, '\n')); werr != nil {
				return RunSummary{}, fmt.Errorf("bench: write result for task %q: %w", task.ID, werr)
			}

			// Write checkpoint after each task.
			checkpoint := RunCheckpoint{
				RunID:          runID,
				UpdatedAt:      now(),
				SuiteName:      opts.SuiteName,
				Matrix:         opts.Matrix,
				LockedManifest: manifest,
				CompletedTasks: completedTaskKeys,
			}
			if err := writeJSONAtomic(filepath.Join(outDir, "checkpoint.json"), checkpoint); err != nil {
				return RunSummary{}, fmt.Errorf("bench: write checkpoint: %w", err)
			}

			taskCount++
		}
		if maxTasks > 0 && taskCount >= maxTasks {
			break
		}
	}

	endedAt := now()

	// 8. Dump RunResult (report.go shape) and write complete marker.
	// RunResult uses Mode, Profiles, Labels, EndedAt, StartedAt — no Matrix/OutDir/CompletedAt.
	runResult := RunResult{
		RunID:     runID,
		SuiteName: opts.SuiteName,
		Mode:      opts.Matrix.Mode,
		Profiles:  snapshots,
		Manifest:  manifest,
		Results:   results,
		Labels:    dirtyLabels,
		StartedAt: startedAt,
		EndedAt:   endedAt,
	}

	if err := writeJSONAtomic(filepath.Join(outDir, "run.json"), runResult); err != nil {
		return RunSummary{}, fmt.Errorf("bench: write run.json: %w", err)
	}

	complete := CompleteMarker{
		RunID:       runID,
		CompletedAt: endedAt,
		ReportPaths: []string{filepath.Join(outDir, "run.json")},
	}
	if err := writeJSONAtomic(filepath.Join(outDir, "complete.json"), complete); err != nil {
		return RunSummary{}, fmt.Errorf("bench: write complete.json: %w", err)
	}

	return RunSummary{RunResult: runResult, OutDir: outDir}, nil
}

// runOnePair executes a single (task, profile) pair. It returns the TaskResult,
// a skip boolean (true when the workspace setup failed and the result is a
// "blocked" sentinel that should be recorded but not checkpointed), and any
// hard error that should abort the run.
func runOnePair(ctx context.Context, opts RunOptions, executor Executor, outDir string, task Task, workerProfile MatrixProfile) (TaskResult, bool, error) {
	workDir := filepath.Join(outDir, "tasks", task.ID, workerProfile.ID, "work")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return TaskResult{}, false, fmt.Errorf("bench: create work dir for task %q profile %q: %w", task.ID, workerProfile.ID, err)
	}

	// PrepareWorkspace if provider supports it.
	if preparer, ok := opts.Provider.(WorkspacePreparer); ok {
		if err := preparer.PrepareWorkspace(task, workDir); err != nil {
			blocked := TaskResult{
				TaskID:    task.ID,
				ProfileID: workerProfile.ID,
				Status:    TaskStatusBlocked,
				Failure:   FailureSetup,
				Notes:     fmt.Sprintf("prepare workspace: %v", err),
			}
			return blocked, true, nil // skip checkpointing for setup failures
		}
	}

	// Apply per-task timeout.
	taskCtx := ctx
	var cancel context.CancelFunc
	if opts.Budgets.PerTaskTimeout > 0 {
		taskCtx, cancel = context.WithTimeout(ctx, opts.Budgets.PerTaskTimeout)
	}

	result := executor(taskCtx, task, workerProfile, workDir)
	if cancel != nil {
		cancel()
	}

	// Classify timeout.
	if taskCtx.Err() == context.DeadlineExceeded && result.Status != TaskStatusSolved {
		result.Status = TaskStatusUnsolved
		result.Failure = FailureSetup
		result.Notes = "task timed out"
	}

	return result, false, nil
}

// LoadCheckpoint reads the latest run checkpoint from outDir (if any).
func LoadCheckpoint(outDir string) (RunCheckpoint, bool, error) {
	data, err := os.ReadFile(filepath.Join(outDir, "checkpoint.json"))
	if os.IsNotExist(err) {
		return RunCheckpoint{}, false, nil
	}
	if err != nil {
		return RunCheckpoint{}, false, fmt.Errorf("bench: read checkpoint: %w", err)
	}
	var cp RunCheckpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return RunCheckpoint{}, false, fmt.Errorf("bench: decode checkpoint: %w", err)
	}
	return cp, true, nil
}

// Resume continues a previously checkpointed run.
// It reads the checkpoint from outDir and skips already-completed task/profile pairs.
func Resume(ctx context.Context, outDir string, opts RunOptions) (RunSummary, error) {
	cp, ok, err := LoadCheckpoint(outDir)
	if err != nil {
		return RunSummary{}, err
	}
	if !ok {
		// No checkpoint — start fresh.
		opts.OutDir = outDir
		return Run(ctx, opts)
	}

	// Rebuild the set of completed keys.
	completed := make(map[string]bool, len(cp.CompletedTasks))
	for _, key := range cp.CompletedTasks {
		completed[key] = true
	}

	// Override opts with checkpoint state.
	opts.OutDir = outDir
	opts.SuiteName = cp.SuiteName
	opts.Matrix = cp.Matrix

	// Save the original run ID from the checkpoint.
	resumeRunID := cp.RunID

	// Delegate to a wrapped executor that skips already-completed pairs.
	innerExec := opts.Executor
	if innerExec == nil {
		innerExec = DefaultExecutor(opts.Provider)
	}
	opts.Executor = func(ctx context.Context, task Task, profile MatrixProfile, workDir string) TaskResult {
		key := task.ID + "/" + profile.ID
		if completed[key] {
			return TaskResult{
				TaskID:    task.ID,
				ProfileID: profile.ID,
				Status:    TaskStatusCancelled,
				Notes:     "skipped: already completed in prior run",
			}
		}
		return innerExec(ctx, task, profile, workDir)
	}

	result, err := Run(ctx, opts)
	if err != nil {
		return RunSummary{}, err
	}

	// Restore original run ID from checkpoint.
	if resumeRunID != "" {
		result.RunID = resumeRunID
	}
	return result, nil
}

// checkDirtyWorktree enforces or flags dirty worktree state.
// Returns labels to attach to the manifest (non-empty when dirty+allowed).
// Returns an error when dirty and not allowed.
func checkDirtyWorktree(repoRoot string, allowDirty bool) ([]string, error) {
	cmd := exec.Command("git", "-C", repoRoot, "status", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		// git not available or not a git repo — treat as clean to avoid blocking non-git runs.
		return nil, nil //nolint:nilerr // git unavailable or not a repo — treat as clean
	}
	if len(strings.TrimSpace(string(out))) == 0 {
		return nil, nil
	}
	if !allowDirty {
		return nil, fmt.Errorf("bench: worktree is dirty; commit or stash changes, or pass --allow-dirty")
	}
	return []string{"non-comparable"}, nil
}

// writeJSONAtomic marshals v to JSON and writes it to path via a temp file
// to ensure atomic replacement.
func writeJSONAtomic(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %T: %w", v, err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("rename temp to %q: %w", path, err)
	}
	return nil
}
