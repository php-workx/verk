# Per-Worker Review Diffs Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Make ticket reviewers inspect the delta introduced by the current worker attempt instead of the entire dirty worktree diff against the run base commit.

**Architecture:** Capture a review baseline immediately before each worker attempt, compute the files and diff that changed after that attempt, and pass only that scoped review input to the reviewer. Keep broader wave and epic checks separate so they can still validate the whole run state.

**Tech Stack:** Go, `git` CLI via `internal/adapters/repo/git`, existing fake runtime adapters, `go test`, `just format-check`, `just lint-check`, `just build-check`.

---

## Current Behavior

The current ticket review path builds reviewer input with `collectDiff(absRepoRoot, req.BaseCommit)` in `internal/engine/ticket_run.go`. That calls `Repo.DiffAgainst(baseCommit)`, which runs:

```go
git diff <baseCommit> --
```

This means the reviewer receives all tracked worktree changes relative to the run base commit, including unrelated dirty files and other worker changes. Separately, `ChangedFilesAgainst` includes untracked files, while `DiffAgainst` does not include untracked file contents. That creates an inconsistent review surface: scope checks can see untracked files, but reviewers cannot review their contents.

## Target Behavior

Ticket reviewers should receive:

- Files changed by the current worker attempt.
- Diff content for those files only.
- Untracked worker-created file contents.
- Explicit metadata saying which files are under review.

Ticket reviewers should not receive:

- Unrelated dirty files present before the worker started.
- Files changed by other tickets in the same wave unless the current worker also touched them.
- Engine-owned files such as `.verk/` or `.tickets/`.

Wave and epic gates should still be allowed to inspect aggregate wave/run changes. This plan changes per-ticket review input, not closure gate semantics.

## Important Edge Cases

- Clean tracked file changed by worker: include normal `git diff <base> -- <file>`.
- New untracked file created by worker: include synthetic new-file diff with file contents.
- File dirty before worker and untouched by worker: exclude from reviewer diff.
- File dirty before worker and changed again by worker: include it as a current-attempt change.
- File dirty before worker and reverted by worker: include it because the worker changed worktree state.
- Worker deletes a tracked file: include deletion diff.
- Worker deletes an untracked pre-existing file: include a deletion-style synthetic diff or a clear deletion marker.
- Worker creates binary or very large untracked file: include a bounded marker rather than dumping opaque or huge bytes.
- Repair worker attempt: reviewer should inspect the repair attempt delta, not the original implementation delta.

## Design Decisions

- Use a per-attempt baseline, not only `ImplementationArtifact.ChangedFiles`. `ChangedFiles` is currently computed against the run base commit and can include unrelated dirty state.
- Keep baseline capture in the engine layer, because "worker attempt" is an engine concept.
- Put path-scoped git diff primitives in the git adapter, because command construction and path normalization belong there.
- Store review-scoped changed files on `runtime.ReviewRequest` so prompts and adapter artifacts can be audited without re-parsing the diff.
- Keep the first implementation focused on correctness and bounded output. Do not introduce a full diff library dependency unless tests show the synthetic diff path is too fragile.

---

### Task 1: Add Path-Scoped Git Diff Support

**Files:**
- Modify: `internal/adapters/repo/git/repo.go`
- Modify: `internal/adapters/repo/git/repo_test.go`

**Step 1: Write failing tests**

Add tests that prove the git adapter can build a diff for selected files only and include untracked selected files.

```go
func TestDiffAgainstFiles_ExcludesUnselectedTrackedFiles(t *testing.T) {
	repo, root, baseCommit := newTestRepo(t)

	mustWriteFile(t, filepath.Join(root, "tracked.txt"), "selected update\n")
	mustWriteFile(t, filepath.Join(root, "other.txt"), "other\n")
	runGit(t, root, "add", "other.txt")
	runGit(t, root, "commit", "-m", "add other")
	mustWriteFile(t, filepath.Join(root, "tracked.txt"), "selected update\n")
	mustWriteFile(t, filepath.Join(root, "other.txt"), "unrelated update\n")

	diff, err := repo.DiffAgainstFiles(baseCommit, []string{"tracked.txt"})
	if err != nil {
		t.Fatalf("DiffAgainstFiles: %v", err)
	}
	if !strings.Contains(diff, "tracked.txt") {
		t.Fatalf("expected selected file in diff:\n%s", diff)
	}
	if strings.Contains(diff, "other.txt") {
		t.Fatalf("did not expect unselected file in diff:\n%s", diff)
	}
}
```

```go
func TestDiffAgainstFiles_IncludesUntrackedFileContent(t *testing.T) {
	repo, root, baseCommit := newTestRepo(t)
	mustWriteFile(t, filepath.Join(root, "new.txt"), "first\nsecond\n")

	diff, err := repo.DiffAgainstFiles(baseCommit, []string{"new.txt"})
	if err != nil {
		t.Fatalf("DiffAgainstFiles: %v", err)
	}
	for _, want := range []string{
		"diff --git a/new.txt b/new.txt",
		"new file mode",
		"+++ b/new.txt",
		"+first",
		"+second",
	} {
		if !strings.Contains(diff, want) {
			t.Fatalf("expected %q in diff:\n%s", want, diff)
		}
	}
}
```

Also add a repo-escape test:

```go
func TestDiffAgainstFiles_RejectsRepoEscape(t *testing.T) {
	repo, _, baseCommit := newTestRepo(t)
	if _, err := repo.DiffAgainstFiles(baseCommit, []string{"../escape.txt"}); err == nil {
		t.Fatal("expected repo escape path to be rejected")
	}
}
```

**Step 2: Run tests and verify failure**

Run:

```bash
go test ./internal/adapters/repo/git -run 'TestDiffAgainstFiles' -count=1
```

Expected: fails because `DiffAgainstFiles` does not exist.

**Step 3: Implement `DiffAgainstFiles`**

Add:

```go
func (r *Repo) DiffAgainstFiles(baseCommit string, files []string) (string, error)
```

Implementation rules:

- Validate repo and base commit exactly like `DiffAgainst`.
- Normalize and deduplicate `files`.
- Filter empty paths.
- Reject paths that escape the repo or contain NUL.
- Return `""` for an empty file list.
- Run tracked diff with:

```go
git diff <baseCommit> -- <files...>
```

- Append synthetic diffs for selected untracked files because plain `git diff` omits them.
- Sort file paths for stable output.

Suggested helper shape:

```go
func (r *Repo) DiffAgainstFiles(baseCommit string, files []string) (string, error) {
	if r == nil {
		return "", fmt.Errorf("nil repo")
	}
	baseCommit = strings.TrimSpace(baseCommit)
	if baseCommit == "" {
		return "", fmt.Errorf("base commit is required")
	}
	if err := r.verifyCommit(baseCommit); err != nil {
		return "", err
	}

	normalized, err := r.normalizeDiffPaths(files)
	if err != nil {
		return "", err
	}
	if len(normalized) == 0 {
		return "", nil
	}

	args := append([]string{"diff", baseCommit, "--"}, normalized...)
	trackedDiff, err := gitOutput(r.root, args...)
	if err != nil {
		return "", err
	}

	untrackedDiff, err := r.syntheticUntrackedDiff(normalized)
	if err != nil {
		return "", err
	}

	return joinDiffParts(trackedDiff, untrackedDiff), nil
}
```

The synthetic untracked diff should:

- Use labels `a/<path>` and `b/<path>`.
- Prefix every content line with `+`.
- Preserve a final line marker if the file has no trailing newline.
- Limit output for large files, for example after 256 KiB or 4,000 lines, with a clear truncation marker.
- Detect binary content with a simple NUL-byte scan and emit a marker instead of raw content.

**Step 4: Run focused tests**

Run:

```bash
go test ./internal/adapters/repo/git -run 'TestDiffAgainstFiles|TestChangedFilesAgainstBaseline' -count=1
```

Expected: pass.

**Step 5: Commit**

Commit only git adapter changes:

```bash
git add internal/adapters/repo/git/repo.go internal/adapters/repo/git/repo_test.go
git commit -m "fix: add scoped git diffs for review"
```

---

### Task 2: Add Review Request Changed-File Metadata

**Files:**
- Modify: `internal/adapters/runtime/types.go`
- Modify: `internal/adapters/runtime/prompt.go`
- Modify: `internal/adapters/runtime/prompt_test.go`

**Step 1: Write failing prompt test**

Add:

```go
func TestBuildReviewPrompt_IncludesFilesUnderReview(t *testing.T) {
	prompt := BuildReviewPrompt(ReviewRequest{
		TicketID:                 "VER-001",
		LeaseID:                  "lease-1",
		EffectiveReviewThreshold: "P2",
		ChangedFiles:             []string{"src/app.go", "docs/readme.md"},
		Diff:                     "diff --git a/src/app.go b/src/app.go\n",
	})

	for _, want := range []string{
		"### Files Under Review",
		"- src/app.go",
		"- docs/readme.md",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected %q in prompt:\n%s", want, prompt)
		}
	}
}
```

**Step 2: Run test and verify failure**

Run:

```bash
go test ./internal/adapters/runtime -run TestBuildReviewPrompt_IncludesFilesUnderReview -count=1
```

Expected: fails because `ReviewRequest.ChangedFiles` does not exist or prompt omits it.

**Step 3: Add request field**

Add to `ReviewRequest`:

```go
ChangedFiles []string `json:"changed_files,omitempty"`
```

Place it next to `Diff` so review input metadata stays grouped.

**Step 4: Render changed files in review prompt**

In `BuildReviewPrompt`, before the diff block:

```go
if len(req.ChangedFiles) > 0 {
	b.WriteString("\n### Files Under Review\n\n")
	for _, file := range req.ChangedFiles {
		fmt.Fprintf(&b, "- %s\n", file)
	}
}
```

Do not change the reviewer system prompt yet. The prompt should remain focused on reviewing the provided diff. The new section makes the scoped input explicit.

**Step 5: Run focused tests**

Run:

```bash
go test ./internal/adapters/runtime -run 'TestBuildReviewPrompt' -count=1
```

Expected: pass.

**Step 6: Commit**

Commit only runtime request and prompt changes:

```bash
git add internal/adapters/runtime/types.go internal/adapters/runtime/prompt.go internal/adapters/runtime/prompt_test.go
git commit -m "fix: show scoped review files in reviewer prompts"
```

---

### Task 3: Add Per-Attempt Review Baseline Helpers

**Files:**
- Create: `internal/engine/review_delta.go`
- Create: `internal/engine/review_delta_test.go`

**Step 1: Write failing helper tests**

Create tests for pure helper behavior before wiring it into `RunTicket`.

Test cases:

- Existing dirty files are excluded when untouched by the worker.
- A clean tracked file changed after baseline is included.
- A new untracked file created after baseline is included.
- A pre-existing dirty file changed again after baseline is included.
- A pre-existing untracked file changed after baseline is included.
- Engine-owned files are filtered out.

Example:

```go
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
```

**Step 2: Run tests and verify failure**

Run:

```bash
go test ./internal/engine -run 'TestReviewDelta' -count=1
```

Expected: fails because helpers do not exist.

**Step 3: Implement baseline types**

Create:

```go
type reviewBaseline struct {
	BaseCommit          string
	PreExistingChanged []string
	Files              map[string]reviewFileSnapshot
}

type reviewFileSnapshot struct {
	Path   string
	Exists bool
	Bytes  []byte
}

type reviewDelta struct {
	ChangedFiles []string
	Diff         string
}
```

**Step 4: Implement baseline capture**

`captureReviewBaseline(repoRoot, baseCommit string) (reviewBaseline, error)` should:

- Call `collectChangedFiles(repoRoot, baseCommit)`.
- Filter engine-owned files.
- Read bytes for each existing path.
- Record missing paths as `Exists: false`.
- Sort paths for deterministic behavior.

This captures the dirty state already present when the worker starts.

**Step 5: Implement delta collection**

`collectReviewDelta(repoRoot, baseCommit string, baseline reviewBaseline) (reviewDelta, error)` should:

- Call `collectChangedFiles(repoRoot, baseCommit)` after the worker.
- Filter engine-owned files.
- Include files that are now changed but were not pre-existing dirty files.
- For every pre-existing dirty file, compare current existence and bytes to the captured snapshot:
  - If unchanged, exclude.
  - If changed, include.
  - If deleted, include.
  - If reverted clean, include because the worker changed it.
- Sort and deduplicate changed files.
- Build a diff for those files with the new git adapter method from Task 1.

Important limitation to document in code comments: in a shared worktree, if another concurrent worker modifies the same pre-existing dirty file during this attempt, the engine cannot attribute that with perfect certainty. Existing scope validation should prevent overlapping ownership; this helper isolates normal pre-existing dirty state and disjoint parallel work.

**Step 6: Run focused helper tests**

Run:

```bash
go test ./internal/engine -run 'TestReviewDelta' -count=1
```

Expected: pass.

**Step 7: Commit**

Commit only the new helper and tests:

```bash
git add internal/engine/review_delta.go internal/engine/review_delta_test.go
git commit -m "fix: capture per-attempt review deltas"
```

---

### Task 4: Wire Review Delta into Ticket Review

**Files:**
- Modify: `internal/engine/ticket_run.go`
- Modify: `internal/engine/ticket_run_test.go`

**Step 1: Write failing ticket-run tests**

Add a custom test adapter in `ticket_run_test.go` that mutates files during `RunWorker`. Do not pre-create worker files before `RunTicket`, because that simulates dirty baseline rather than worker output.

Suggested adapter shape:

```go
type mutatingRuntimeAdapter struct {
	*runtimefake.Adapter
	onWorker func()
}

func (a *mutatingRuntimeAdapter) RunWorker(ctx context.Context, req runtime.WorkerRequest) (runtime.WorkerResult, error) {
	if a.onWorker != nil {
		a.onWorker()
	}
	return a.Adapter.RunWorker(ctx, req)
}
```

Add tests:

```go
func TestRunTicket_ReviewDiffExcludesPreExistingDirtyFiles(t *testing.T)
func TestRunTicket_ReviewDiffIncludesUntrackedWorkerFile(t *testing.T)
func TestRunTicket_ReviewChangedFilesMatchesWorkerDelta(t *testing.T)
```

The first test should:

- Create a tracked dirty file and untracked dirty file before `RunTicket`.
- During `RunWorker`, change a different tracked file.
- Assert `adapter.ReviewRequests()[0].Diff` contains worker file content.
- Assert it does not contain pre-existing dirty file names or contents.
- Assert `adapter.ReviewRequests()[0].ChangedFiles` contains only the worker file.

The untracked test should:

- During `RunWorker`, create `docs/new-worker-file.md`.
- Assert the review diff contains `new file mode`, `+++ b/docs/new-worker-file.md`, and content lines.

The changed-files test should:

- Assert review request changed files are filtered and sorted.
- Assert `.verk/` and `.tickets/` paths do not appear.

**Step 2: Run tests and verify failure**

Run:

```bash
go test ./internal/engine -run 'TestRunTicket_ReviewDiff|TestRunTicket_ReviewChangedFiles' -count=1
```

Expected: fails because `RunTicket` still uses `collectDiff(absRepoRoot, req.BaseCommit)`.

**Step 3: Capture baseline before each worker attempt**

In `RunTicket`, before invoking the worker for implementation and repair phases, call:

```go
reviewBaseline, err := captureReviewBaseline(absRepoRoot, req.BaseCommit)
if err != nil {
	return RunTicketResult{}, fmt.Errorf("capture review baseline: %w", err)
}
st.reviewBaseline = reviewBaseline
```

Add a field to `ticketRunState`:

```go
reviewBaseline *reviewBaseline
```

Use a pointer or zero-value-aware struct so resume paths remain safe.

**Step 4: Use review delta in review phase**

Replace:

```go
diffForReview, err := collectDiff(absRepoRoot, req.BaseCommit)
```

with:

```go
delta, err := collectReviewDelta(absRepoRoot, req.BaseCommit, st.currentReviewBaseline())
if err != nil {
	return RunTicketResult{}, fmt.Errorf("collect review delta: %w", err)
}
```

Then pass:

```go
ChangedFiles: delta.ChangedFiles,
Diff:         delta.Diff,
Standards:   runtime.BuildReviewStandards(runtime.DetectLanguages(delta.Diff)),
```

Fallback rule: if no baseline exists because a run is resumed directly into review from older artifacts, use a conservative compatibility baseline that treats no files as pre-existing dirty. This preserves current behavior for old runs rather than crashing.

**Step 5: Keep implementation artifact changed files behavior separate**

Do not replace `st.implementation.ChangedFiles` yet unless tests prove it is needed. That field currently represents changed files against the run base commit and is used by closeout and validation. The reviewer-scoped changed files should live on `ReviewRequest.ChangedFiles`.

If scope validation still blocks on pre-existing dirty files after the review fix, create a separate follow-up plan for per-ticket scope semantics. Do not blend that into this change.

**Step 6: Run focused ticket tests**

Run:

```bash
go test ./internal/engine -run 'TestRunTicket_ReviewDiff|TestRunTicket_ReviewChangedFiles|TestRunTicket_ChangedFilesCaptured|TestRunTicket_ReviewFindingRepairedClosesTicket' -count=1
```

Expected: pass.

**Step 7: Commit**

Commit only engine wiring and ticket-run tests:

```bash
git add internal/engine/ticket_run.go internal/engine/ticket_run_test.go
git commit -m "fix: review only current worker attempt diffs"
```

---

### Task 5: Cover Repair Attempt Semantics

**Files:**
- Modify: `internal/engine/ticket_run_test.go`
- Modify: `internal/engine/ticket_run.go` if Task 4 did not naturally cover repair

**Step 1: Write repair-specific failing test**

Extend or add a test:

```go
func TestRunTicket_RepairReviewDiffOnlyIncludesRepairDelta(t *testing.T)
```

Scenario:

- Initial worker changes `src/app.go`.
- Initial reviewer returns a blocking finding.
- Repair worker changes `src/app_test.go`.
- Second reviewer request should contain `src/app_test.go`.
- Second reviewer request should not contain the original `src/app.go` diff unless the repair worker touched it.

Use a mutating adapter that changes different files on first and second worker calls.

**Step 2: Run test and verify failure if not already passing**

Run:

```bash
go test ./internal/engine -run TestRunTicket_RepairReviewDiffOnlyIncludesRepairDelta -count=1
```

Expected: fail if the review baseline is only captured for the first implementation phase.

**Step 3: Adjust worker-attempt baseline placement**

Ensure baseline capture happens immediately before every worker invocation, including:

- Initial implementation.
- Repair worker.
- Any fallback runtime retry that actually invokes a worker again.

Do not capture baseline once per ticket. Capture once per worker attempt.

**Step 4: Run focused tests**

Run:

```bash
go test ./internal/engine -run 'TestRunTicket_RepairReviewDiffOnlyIncludesRepairDelta|TestRunTicket_ReviewFindingRepairedClosesTicket' -count=1
```

Expected: pass.

**Step 5: Commit**

Commit repair-specific changes:

```bash
git add internal/engine/ticket_run.go internal/engine/ticket_run_test.go
git commit -m "fix: scope repair reviews to repair attempts"
```

---

### Task 6: Add Adapter Artifact Coverage

**Files:**
- Modify: `internal/adapters/runtime/claude/adapter_test.go`
- Modify: `internal/adapters/runtime/codex/adapter_test.go`

**Step 1: Write artifact tests**

Both runtime adapters persist `ReviewRequest` in their JSON artifacts. Add or update tests to assert the serialized request contains `changed_files`.

Example assertion pattern:

```go
if !strings.Contains(string(artifactBytes), `"changed_files":["src/app.go"]`) {
	t.Fatalf("expected changed_files in review artifact:\n%s", artifactBytes)
}
```

Prefer decoding JSON into the existing artifact struct if the test already does that.

**Step 2: Run focused adapter tests**

Run:

```bash
go test ./internal/adapters/runtime/claude ./internal/adapters/runtime/codex -run RunReviewer -count=1
```

Expected: pass.

**Step 3: Commit**

Commit adapter test changes:

```bash
git add internal/adapters/runtime/claude/adapter_test.go internal/adapters/runtime/codex/adapter_test.go
git commit -m "test: assert review changed files are persisted"
```

---

### Task 7: End-to-End Regression Guard

**Files:**
- Modify: `internal/engine/ticket_run_test.go`

**Step 1: Add one high-signal integration-style test**

Add:

```go
func TestRunTicket_ReviewerDoesNotReceiveUnrelatedDirtyWorktree(t *testing.T)
```

Scenario:

- Repo has dirty tracked file `unrelated/tracked.txt` before `RunTicket`.
- Repo has untracked file `unrelated/untracked.txt` before `RunTicket`.
- Worker creates `owned/worker.md`.
- Review request receives only `owned/worker.md`.
- Review prompt built from that request does not contain unrelated paths.

This is the user-facing bug guard. Keep it focused and easy to understand.

**Step 2: Run the regression test**

Run:

```bash
go test ./internal/engine -run TestRunTicket_ReviewerDoesNotReceiveUnrelatedDirtyWorktree -count=1
```

Expected: pass.

**Step 3: Run broader engine test subset**

Run:

```bash
go test ./internal/engine -run 'TestRunTicket|TestReviewDelta' -count=1
```

Expected: pass.

**Step 4: Commit**

Commit the regression guard:

```bash
git add internal/engine/ticket_run_test.go
git commit -m "test: guard reviewer diff against unrelated dirty files"
```

---

### Task 8: Full Verification

**Files:**
- No code changes expected.

**Step 1: Format**

Run:

```bash
just format
```

Expected: Go files are formatted. Review the diff afterward so formatting does not sweep unrelated files into the commit.

**Step 2: Check formatting**

Run:

```bash
just format-check
```

Expected: pass.

**Step 3: Run focused package tests**

Run:

```bash
go test ./internal/adapters/repo/git ./internal/adapters/runtime ./internal/adapters/runtime/claude ./internal/adapters/runtime/codex ./internal/engine -count=1
```

Expected: pass.

**Step 4: Run lint check**

Run:

```bash
just lint-check
```

Expected: pass for files changed by this plan. If lint fails on unrelated dirty worker files, record exact unrelated paths and do not mix those fixes into this change.

**Step 5: Run build check**

Run:

```bash
just build-check
```

Expected: pass.

**Step 6: Check whitespace**

Run:

```bash
git diff --check
```

Expected: no whitespace errors in changed files.

**Step 7: Final commit if formatting changed files**

If `just format` changed files after earlier commits, commit only related formatting changes:

```bash
git add <related files>
git commit -m "style: format scoped review diff changes"
```

---

## Acceptance Criteria

- Review requests for tickets include only the current worker attempt's changed files and diff.
- Review requests include untracked worker-created file content in the diff.
- Pre-existing dirty worktree files do not leak into per-ticket reviewer prompts.
- Repair reviews are scoped to the repair worker attempt.
- Runtime review prompts show a `Files Under Review` section.
- Adapter artifacts persist `changed_files` for auditability.
- Existing wave and epic closure gates keep aggregate changed-file behavior.
- Focused tests pass for git adapter, runtime prompt, runtime adapters, and engine ticket runs.

## Follow-Up Work Not Included

- Per-ticket scope validation currently uses implementation changed files against the run base commit. If it still blocks on unrelated pre-existing dirty files, handle that as a separate scope-validation change.
- Parallel workers that modify the exact same path remain inherently ambiguous in a shared worktree. The existing owned-path and scope checks should prevent this; stronger isolation would require per-ticket worktrees or merge orchestration.
- Reviewer token budgeting can be improved after this change by measuring diff size before and after scoping.

