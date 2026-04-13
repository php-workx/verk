# Worker Isolation via Git Worktrees

## Context

When verk runs multiple tickets in parallel within a wave, all workers share the same git working tree. This causes two problems:

1. **Verification cross-contamination**: `just check` runs repo-wide, so Worker A's verification fails because of lint issues in Worker B's files. After 3 impl→verify loops, both tickets get blocked with `non_convergent_verification`.
2. **Changed file confusion**: `repo.ChangedFilesAgainst(baseCommit)` sees all workers' changes mixed together, making scope validation unreliable.

The fix: give each worker its own git worktree so it only sees and checks its own changes.

## Approach

Split the current `RepoRoot` into two paths:
- **`repoRoot`** (main worktree): Where `.verk/` and `.tickets/` live. Used for claims, artifacts, config, ticket store updates. Unchanged.
- **`worktreePath`** (per-ticket worktree): Where the worker edits files and runs verification. A separate git worktree checked out from the wave base commit.

Each wave ticket gets:
1. A worktree created at `<repoParent>/.verk-worktrees/<runID>/<ticketID>/` (sibling to repo root — git forbids nested worktrees)
2. Worker subprocess runs with CWD set to the worktree
3. Verification commands run in the worktree (only sees this worker's changes)
4. After the wave, changed files are collected per-worktree and file-copied back to the main tree
5. Worktrees are cleaned up

No worktrees are created for single-ticket runs (backward compatible).

## Implementation Steps

### Step 1: Git adapter worktree methods

**File**: `internal/adapters/repo/git/repo.go`

Add two methods:

```go
func (r *Repo) CreateWorktree(ctx context.Context, commitish, targetPath string) error
func (r *Repo) RemoveWorktree(targetPath string) error
```

- `CreateWorktree` runs `git worktree add --detach <targetPath> <commitish>` from the main worktree root
- `RemoveWorktree` runs `git worktree remove --force <targetPath>` then best-effort `os.RemoveAll`
- Worktrees are placed at `<repoParent>/.verk-worktrees/<runID>/<ticketID>/` (outside the main worktree, which git requires)

**Tests**: `internal/adapters/repo/git/repo_test.go` — create, verify checkout, make changes, collect diff, remove.

### Step 2: WorktreeManager

**New file**: `internal/engine/worktree.go`

```go
type WorktreeManager struct {
    mainRoot   string
    baseCommit string
    runID      string
    mu         sync.Mutex
    worktrees  map[string]string // ticketID -> worktreePath
}

func NewWorktreeManager(mainRoot, baseCommit, runID string) *WorktreeManager
func (m *WorktreeManager) CreateWorktree(ctx context.Context, ticketID string) (string, error)
func (m *WorktreeManager) WorktreePath(ticketID string) string
func (m *WorktreeManager) ChangedFiles(ticketID string) ([]string, error)
func (m *WorktreeManager) Diff(ticketID string) (string, error)
func (m *WorktreeManager) MergeToMain(ticketID string) error  // file-copy per changed file
func (m *WorktreeManager) CleanupAll() error
```

- `CreateWorktree` calls `git.CreateWorktree`, stores in the map
- `ChangedFiles` creates `git.New(worktreePath)` and calls `ChangedFilesAgainst(baseCommit)`, filtering engine-owned files
- `MergeToMain` copies each changed file from worktree to main tree (`os.MkdirAll` + `os.Link` or `io.Copy`), including untracked new files
- `CleanupAll` removes all worktrees, called in a `defer` from `RunEpic`

**Tests**: `internal/engine/worktree_test.go`

### Step 3: Fix runtime adapters to set cmd.Dir

**Files**:
- `internal/adapters/runtime/claude/adapter.go` — add `workDir` param to `defaultRunCommand` and `runStreamingCommand`; set `cmd.Dir = workDir` when non-empty
- `internal/adapters/runtime/codex/adapter.go` — already passes `--cwd`, also set `cmd.Dir` for process environment correctness
- `internal/adapters/runtime/types.go` — `WorkerRequest.WorktreePath` already exists, ensure it's populated

Both adapters receive `req.WorktreePath` and pass it as the subprocess working directory. When empty (single-ticket runs), the subprocess inherits the parent CWD as before.

**Tests**: Update adapter tests to verify `cmd.Dir` is set correctly.

### Step 4: Split repoRoot/worktreePath in ticket_run.go

**File**: `internal/engine/ticket_run.go`

Changes to `ticketRunState`:
```go
type ticketRunState struct {
    // ... existing fields ...
    repoRoot     string  // main worktree root (for .verk/, .tickets/, claims)
    worktreePath string  // per-ticket worktree (for git ops, worker CWD, verification CWD)
}
```

When `req.WorktreePath == ""`, `worktreePath = absRepoRoot` (backward compatible).

Places that switch from `absRepoRoot` to `st.worktreePath`:
- `WorkerRequest.WorktreePath` (line ~180, ~357) — already uses `absRepoRoot`, switch to `st.worktreePath`
- `collectChangedFiles(st.repoRoot, ...)` → `collectChangedFiles(st.worktreePath, ...)`
- `collectDiff(st.repoRoot, ...)` → `collectDiff(st.worktreePath, ...)`
- `runVerification(ctx, absRepoRoot)` → `runVerification(ctx, st.repoRoot, st.worktreePath)` — CWD in worktree, artifacts in main tree

Places that stay on `st.repoRoot` (main tree):
- `buildTicketRunPaths(absRepoRoot, ...)` — artifacts go to main tree
- `filepath.Join(absRepoRoot, ".tickets", ...)` — ticket store in main tree
- `tkmd.AcquireClaim`, `tkmd.ReleaseClaim`, `tkmd.RenewClaim` — claims in main tree

Add `WorktreePath` field to `RunTicketRequest`:
```go
type RunTicketRequest struct {
    RepoRoot             string
    WorktreePath         string // optional: isolated worktree for file operations
    // ... rest unchanged ...
}
```

### Step 5: Update verification runner for split paths

**File**: `internal/adapters/verify/command/runner.go`

Change `RunCommands` and `RunQualityCommands` signatures to accept a separate `workDir string` parameter:

```go
func RunCommands(ctx context.Context, repoRoot string, workDir string, commands []string, ...) (...)
func RunQualityCommands(ctx context.Context, repoRoot string, workDir string, qualityCfg []policy.QualityCommand, ...) (...)
```

- `repoRoot`: used for artifact directory creation (`.verk/verification/`)
- `workDir`: used for `cmd.Dir` (where commands execute). Falls back to `repoRoot` when empty.

### Step 6: Update wave execution in epic_run.go

**File**: `internal/engine/epic_run.go`

The goroutine-based wave execution changes from:

```
for each ticket: go executeEpicTicket(req, cfg, wave, ticketID)
wg.Wait()
changedFiles = repo.ChangedFilesAgainst(baseCommit)  // sees ALL workers' changes mixed
```

To:

```
wm := NewWorktreeManager(req.RepoRoot, baseCommit, req.RunID)
defer wm.CleanupAll()

for each ticket: wm.CreateWorktree(ctx, ticketID)
for each ticket: go executeEpicTicket(req, cfg, wave, ticketID, wm.WorktreePath(ticketID))
wg.Wait()

// Collect per-ticket changed files and merge back
allChangedFiles := []string{}
for _, ticketID := range wave.TicketIDs {
    files, _ := wm.ChangedFiles(ticketID)
    allChangedFiles = append(allChangedFiles, files...)
    wm.MergeToMain(ticketID)
}
allChangedFiles = uniqueSorted(allChangedFiles)
```

`executeEpicTicket` gets an additional `worktreePath string` parameter, which it passes to `RunTicketRequest.WorktreePath`.

The baseline changed files calculation (`repo.ChangedFilesAgainst(baseCommit)` before the wave) stays on the main tree — it's checking for pre-existing dirty files.

### Step 7: CLI backward compatibility

**File**: `internal/cli/run.go`

The `doRunTicket` function creates `RunTicketRequest` without `WorktreePath`. This is fine — when `WorktreePath` is empty, all code falls back to using `RepoRoot` as before. No changes needed for single-ticket runs.

## Verification

1. **Unit tests**: Each step above has test files listed
2. **Integration test**: Run `verk run` with a 2-ticket epic and verify:
   - Each worker runs in its own worktree
   - Verification in one worker doesn't see the other's files
   - Changed files are correctly collected and merged back
   - Worktrees are cleaned up after the wave
3. **Regression test**: Run existing `epic_run_test.go` tests to verify backward compatibility (single-ticket runs use no worktree)
4. **Manual test**: Create an epic with 2 tickets, introduce a deliberate lint error in one, verify the other still passes verification

## Critical Files

| File | Change |
|------|--------|
| `internal/adapters/repo/git/repo.go` | Add `CreateWorktree`, `RemoveWorktree` |
| `internal/engine/worktree.go` | New: WorktreeManager |
| `internal/engine/epic_run.go` | Wave flow: create worktrees, per-ticket collection, merge-back, cleanup |
| `internal/engine/ticket_run.go` | Split `repoRoot`/`worktreePath` in state, update operations |
| `internal/adapters/runtime/claude/adapter.go` | Set `cmd.Dir` from WorktreePath |
| `internal/adapters/runtime/codex/adapter.go` | Set `cmd.Dir` from WorktreePath |
| `internal/adapters/verify/command/runner.go` | Accept separate `workDir` parameter |