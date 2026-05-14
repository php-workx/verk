package engine

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// mustWriteFile writes content to path, creating parent directories as needed.
func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestReviewDelta_ExcludesPreExistingDirtyFiles verifies that a file that was
// already dirty (modified but not committed) before the worker started is not
// included in the review delta when the worker does not touch it.
func TestReviewDelta_ExcludesPreExistingDirtyFiles(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)

	mustWriteFile(t, filepath.Join(repoRoot, "preexisting.txt"), "dirty before worker\n")
	baseline, err := captureReviewBaseline(repoRoot, baseCommit)
	if err != nil {
		t.Fatalf("captureReviewBaseline: %v", err)
	}

	mustWriteFile(t, filepath.Join(repoRoot, "worker.txt"), "created by worker\n")
	delta, err := collectReviewDelta(repoRoot, baseCommit, baseline)
	if err != nil {
		t.Fatalf("collectReviewDelta: %v", err)
	}

	if slices.Contains(delta.ChangedFiles, "preexisting.txt") {
		t.Fatalf("pre-existing dirty file leaked into review delta: %v", delta.ChangedFiles)
	}
	if !slices.Contains(delta.ChangedFiles, "worker.txt") {
		t.Fatalf("worker-created file missing from review delta: %v", delta.ChangedFiles)
	}
}

// TestReviewDelta_IncludesCleanTrackedFileChangedByWorker verifies that a
// clean tracked file that the worker modifies after baseline is included.
func TestReviewDelta_IncludesCleanTrackedFileChangedByWorker(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)

	// tracked.txt exists and is clean at baseline
	baseline, err := captureReviewBaseline(repoRoot, baseCommit)
	if err != nil {
		t.Fatalf("captureReviewBaseline: %v", err)
	}

	// worker modifies the tracked file
	mustWriteFile(t, filepath.Join(repoRoot, "tracked.txt"), "modified by worker\n")
	delta, err := collectReviewDelta(repoRoot, baseCommit, baseline)
	if err != nil {
		t.Fatalf("collectReviewDelta: %v", err)
	}

	if !slices.Contains(delta.ChangedFiles, "tracked.txt") {
		t.Fatalf("worker-modified tracked file missing from review delta: %v", delta.ChangedFiles)
	}
}

// TestReviewDelta_IncludesNewUntrackedFileCreatedByWorker verifies that a
// brand-new untracked file created by the worker after baseline is included.
func TestReviewDelta_IncludesNewUntrackedFileCreatedByWorker(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)

	baseline, err := captureReviewBaseline(repoRoot, baseCommit)
	if err != nil {
		t.Fatalf("captureReviewBaseline: %v", err)
	}

	mustWriteFile(t, filepath.Join(repoRoot, "newfile.go"), "package main\n")
	delta, err := collectReviewDelta(repoRoot, baseCommit, baseline)
	if err != nil {
		t.Fatalf("collectReviewDelta: %v", err)
	}

	if !slices.Contains(delta.ChangedFiles, "newfile.go") {
		t.Fatalf("worker-created untracked file missing from review delta: %v", delta.ChangedFiles)
	}
}

// TestReviewDelta_IncludesPreExistingDirtyFileChangedAgainByWorker verifies
// that a file that was dirty before the worker started AND was further modified
// by the worker is included in the review delta.
func TestReviewDelta_IncludesPreExistingDirtyFileChangedAgainByWorker(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)

	mustWriteFile(t, filepath.Join(repoRoot, "preexisting.txt"), "dirty before worker\n")
	baseline, err := captureReviewBaseline(repoRoot, baseCommit)
	if err != nil {
		t.Fatalf("captureReviewBaseline: %v", err)
	}

	// worker further modifies the same file
	mustWriteFile(t, filepath.Join(repoRoot, "preexisting.txt"), "modified again by worker\n")
	delta, err := collectReviewDelta(repoRoot, baseCommit, baseline)
	if err != nil {
		t.Fatalf("collectReviewDelta: %v", err)
	}

	if !slices.Contains(delta.ChangedFiles, "preexisting.txt") {
		t.Fatalf("pre-existing dirty file modified by worker missing from review delta: %v", delta.ChangedFiles)
	}
}

// TestReviewDelta_IncludesPreExistingUntrackedFileChangedByWorker verifies
// that a pre-existing untracked file that the worker modifies is included.
func TestReviewDelta_IncludesPreExistingUntrackedFileChangedByWorker(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)

	mustWriteFile(t, filepath.Join(repoRoot, "untracked.txt"), "original untracked content\n")
	baseline, err := captureReviewBaseline(repoRoot, baseCommit)
	if err != nil {
		t.Fatalf("captureReviewBaseline: %v", err)
	}

	mustWriteFile(t, filepath.Join(repoRoot, "untracked.txt"), "modified by worker\n")
	delta, err := collectReviewDelta(repoRoot, baseCommit, baseline)
	if err != nil {
		t.Fatalf("collectReviewDelta: %v", err)
	}

	if !slices.Contains(delta.ChangedFiles, "untracked.txt") {
		t.Fatalf("pre-existing untracked file modified by worker missing from review delta: %v", delta.ChangedFiles)
	}
}

// TestReviewDelta_FiltersEngineOwnedFiles verifies that engine-owned paths
// (under .verk/ and .tickets/) are excluded from the review delta.
func TestReviewDelta_FiltersEngineOwnedFiles(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)

	baseline, err := captureReviewBaseline(repoRoot, baseCommit)
	if err != nil {
		t.Fatalf("captureReviewBaseline: %v", err)
	}

	// worker creates a legitimate file and engine-owned files
	mustWriteFile(t, filepath.Join(repoRoot, "worker.txt"), "worker output\n")
	mustWriteFile(t, filepath.Join(repoRoot, ".verk", "state.json"), "{}\n")
	mustWriteFile(t, filepath.Join(repoRoot, ".tickets", "ticket-1.md"), "# ticket\n")

	delta, err := collectReviewDelta(repoRoot, baseCommit, baseline)
	if err != nil {
		t.Fatalf("collectReviewDelta: %v", err)
	}

	for _, f := range delta.ChangedFiles {
		if isEngineOwned(f) {
			t.Fatalf("engine-owned file leaked into review delta: %s (full list: %v)", f, delta.ChangedFiles)
		}
	}
	if !slices.Contains(delta.ChangedFiles, "worker.txt") {
		t.Fatalf("worker file missing from review delta: %v", delta.ChangedFiles)
	}
}
