# Worker Isolation via Git Worktrees

## Context

When `verk run` processes multiple tickets in parallel within a wave, all workers
currently share the same git working tree. That causes two user-visible bugs:

1. **Verification cross-contamination**: repo-wide checks can fail for Worker A
   because Worker B left broken files in the same workspace.
2. **Changed-file confusion**: `ChangedFilesAgainst(baseCommit)` sees all worker
   changes mixed together, so scope validation and review context become
   unreliable.

The fix is to execute every epic wave ticket in its own git worktree. Each worker
sees its own file changes, verification runs against only that worker's checkout,
and the engine merges only accepted ticket changes back into the main workspace.

## Architecture

The engine splits the repository into two concepts:

- **`repoRoot`**: the user's main worktree. Engine state lives here:
  `.verk/`, `.tickets/`, claims, run artifacts, verification logs, and the final
  merged result of accepted wave tickets.
- **`worktreePath`**: a per-ticket git worktree. Worker subprocesses, reviewer
  subprocesses, git diff collection, and verification commands run here.

For epics, `RunEpic` always uses worktrees, even for one-ticket waves. Direct
single-ticket CLI runs intentionally do not use worktrees; they continue to edit
the main tree directly for interactive use.

## Semantics & Invariants

## Worktree cache configuration

Set `VERK_WORKTREE_ROOT` to force where worktree caches are created.
If unset, the tool resolves cache location from `$XDG_CACHE_HOME`, then
`$HOME`, and finally `os.TempDir()`.

### 1) Main tree cleanliness precondition

Decision: before each wave, the main tree must match the current internal wave base
for all non-engine-owned files (outside `.verk/`, `.tickets/`, `.git/`), and any
violation fails the wave immediately with an explicit operator-facing error.
Rationale: clean baselines prevent hidden local edits from being merged or
silently ignored by isolated workers.

### 2) `.verk/` visibility from worker CWD

Decision: after creating each worktree, `WorktreeManager` must create
`<worktree>/.verk -> <mainRoot>/.verk`, and all workers/reviewers continue
using workspace-relative `.verk/...` paths.
Rationale: this is simpler than rewriting every call site to absolute paths and
keeps run artifacts and context visible in the shared engine state.

### 3) `.tickets/` handling

Decision: `.tickets/` stays managed via `<mainRoot>/.tickets` using
`st.repoRoot`; workers/reviewers may read ticket files, but must never write
state there.
Rationale: ticket metadata is committed project state and must be updated only in
run-owned main-tree flow.

### 4) Intra-wave file conflict policy

Decision: if multiple tickets in one wave touch the same file path, the wave must
fail with a structured conflict object containing `{path, ticketIDs}` entries.
Rationale: two concurrent winners for one path is ambiguous and must be surfaced
rather than resolved by hidden precedence.

### 5) Merge-back case enumeration

Decision: merge-back is handled via an explicit case matrix for
`modified / added / deleted / renamed / type-change / mode-only / symlink` and
never by informal file-copy prose.
Rationale: explicit case coverage prevents silent behavior drift and gives a
reviewable merge contract.

| File status | Merge-back behavior |
| --- | --- |
| modified | Rewrite content at same path, preserving executable mode |
| added | Create path and content in main tree with mode |
| deleted | Remove path in main tree |
| renamed | Delete source path and create destination path |
| type-change | Remove old path and create target type |
| mode-only | Update file mode bits only |
| symlink | Replace with canonical symlink target |

### 6) Crash cleanup

Decision: on `RunEpic` startup, run stale reconciliation that executes
`git worktree prune` and cleans cache dir entries for run IDs without active
claim/heartbeat.
Rationale: this prevents worktree leaks and stale index state after abnormal exits.

### 7) Single-ticket vs wave semantics

Decision: inside `RunEpic`, even one-ticket waves use worktrees; direct
`doRunTicket` CLI never uses worktrees and continues main-tree operation.
Rationale: epic semantics stay deterministic while direct single-ticket execution
retains interactive behavior.

### 8) Worktree path placement

Decision: worktrees are created under cache roots resolved from
`$VERK_WORKTREE_ROOT` then `$XDG_CACHE_HOME/verk/worktrees` then
`$HOME/.cache/verk/worktrees` then `os.TempDir()/verk/worktrees`.

`ResolveWorktreeRoot(mainRoot)` returns an absolute path shaped as:

```text
<cacheRoot>/<repoHash>
```

where `<repoHash>` is `sha256(absPath(mainRoot))[:12]`.
Per-ticket worktrees are created under:

```text
<workRoot>/<runID>/<ticketID>/
```

Rationale: this keeps ephemeral workspaces out of source roots and under
explicit cleanup jurisdiction.

`$VERK_WORKTREE_ROOT` is documented as a hard override for operators that want
explicit control over where worktree caches are placed.

### 9) Cumulative wave base

Decision: maintain a hidden integration ref `refs/verk/runs/<runID>/base` backed
by an integration worktree under the run cache directory; successful wave
changes merge there first, are internally committed, and become the base for the
next wave.
Rationale: later waves must see earlier accepted output without mutating the user
branch or relying on uncommitted working tree state.

### 10) Reviewer isolation

Decision: reviewer execution is submitted with `WorktreePath`, and reviewer
subprocesses must run in the same ticket worktree as worker execution.
Rationale: reviewers must assess the exact candidate state that will drive merge
policy.

### 11) Failed-ticket merge policy

Decision: failed, blocked, cancelled, and needs-decision outcomes never merge to
the main tree, while diffs and concise failure summaries are always retained.
Closed sibling tickets remain merge-eligible: the wave integration gate freezes
and verifies only closed ticket refs, advances the hidden integration base for
that accepted subset, applies the integrated delta to the main tree, and then
leaves the epic blocked on the failed tickets.
Rationale: keeps the workspace clean while preserving evidence for triage and
operator review.

### 12) Merge-back atomicity

Decision: preflight all intended merges before mutating main state; on any late
error, block the wave and emit explicit partial-merge recovery guidance.
Rationale: partial merge states are not valid run outcomes.

### 13) Resume semantics

Decision: resumed epics must execute with the same worktree-isolated flow as new
`RunEpic` invocations; shared-workspace baseline subtraction is not allowed as a
resume-only path.
Rationale: continuity must preserve the same integrity assumptions in every run
mode.

### 14) Sub-epic semantics

Decision: sub-epics must either use the same isolation machinery or be
explicitly rejected.
Rationale: silent shared-workspace execution in nested flows violates the isolation
contract.

Current implementation choice: explicitly reject nested sub-epics during
worktree-isolated epic runs with an operator-facing error until recursive hidden
base / integration handling exists for descendant waves.

### 15) Cancellation and claims

Decision: claims, heartbeats, and cancellation remain anchored in
`<repoRoot>/.tickets`, and lost claims/timeouts/cancelled tickets are treated as
non-accepted with preserved artifacts but no merge.
Rationale: ticket lifecycle is authoritative in centralized engine state.

### 16) Worker commit policy

Decision: workers may make local commits as optional artifacts, but may not push,
rebase, amend shared refs, move branches, or modify the user's main tree;
reviewers never commit.
Rationale: local commits help debugging but cannot become run truth without
orchestrator validation.

### 17) Internal ticket refs

Decision: accepted tickets are materialized in run-owned refs such as
`refs/verk/runs/<runID>/tickets/<ticketID>`, using worker commit when valid,
otherwise creating a canonical commit from worktree diff.
Rationale: official run refs are owned by Verk and stable even when worker commit
usage varies.

### 18) Wave integration gate

Decision: the orchestrator merges only accepted internal ticket refs into hidden
integration base, performs wave-level checks there, commits and advances the
hidden base, then applies the integrated result to main; conflicts block by
default with no hidden priority.
Rationale: this gate is the run's integrity boundary and prevents
last-writer-wins behavior.

## Self-Review against required semantics

The section above is the definitive contract and is rechecked explicitly:

1. Main-tree baseline is validated before each wave.
2. Worker worktrees symlink `.verk` to main-root `.verk`.
3. `.tickets/` is managed only via main-root ticket state.
4. Intra-wave conflicts emit `{path, ticketIDs}` listings.
5. Merge-back is driven by the required status matrix.
6. Startup reconciliation prunes stale worktrees and cache orphans.
7. Every `RunEpic` wave, including single-ticket, uses worktrees.
8. Worktree placement resolves cache-root policy first.
9. Wave base advances through hidden integration refs.
10. Reviewers execute in ticket worktrees.
11. Non-accepted outcomes never merge but keep diff artifacts.
12. Merge is atomic after full preflight.
13. Resume flows use the same isolation path.
14. Sub-epics cannot silently skip isolation.
15. Lost claims or cancellations block merge acceptance.
16. Worker commits stay optional local artifacts.
17. Accepted tickets become internal run refs.
18. Integration gate runs checks before applying to main.

## Implementation Steps

### Step 1: Git Adapter Worktree Methods

**File:** `internal/adapters/repo/git/repo.go`

Add:

```go
func (r *Repo) CreateWorktree(
  ctx context.Context,
  commitish, targetPath string,
) error
func (r *Repo) RemoveWorktree(targetPath string) error
```

`CreateWorktree` runs `git worktree add --detach <targetPath> <commitish>` from
the main worktree root. `RemoveWorktree` force-removes the worktree and prunes
stale git registry entries.

### Step 2: Worktree Root And Manager

**File:** `internal/engine/worktree.go`

Implement cache-root resolution, `WorktreeManager`, per-ticket worktree creation,
`.verk` symlink setup, changed-file collection, diff capture, cleanup, conflict
detection, and merge-back.

`WorktreeManager` should not infer user-visible policy. It provides primitives;
the epic runner decides which ticket outcomes are accepted and eligible to merge.

### Step 3: Wave Base Materialization

**File:** `internal/engine/worktree.go`

Add an integration-base helper that maintains the hidden run ref used as the base
for the next wave. It must:

- create and clean up an integration worktree,
- create or adopt internal ticket refs from accepted ticket worktrees,
- merge accepted ticket refs into the integration worktree,
- create an internal commit without moving the user's branch,
- update the hidden ref,
- expose the current base commit/ref for ticket worktree creation.

This step must happen before rewriting `RunEpic`; otherwise later waves can be
created from stale source.

### Step 4: Runtime Adapter CWD

**Files:**

- `internal/adapters/runtime/types.go`
- `internal/adapters/runtime/claude/adapter.go`
- `internal/adapters/runtime/codex/adapter.go`

Both worker and reviewer requests carry `WorktreePath`. Runtime adapters launch
the subprocess in that worktree, or document and test why an adapter-specific CWD
flag is equivalent.

### Step 5: Verification Runner Split

**File:** `internal/adapters/verify/command/runner.go`

Change verification execution to accept both:

- `repoRoot`: artifact root for `.verk/verification`
- `workDir`: command CWD for isolated checks

When `workDir` is empty, fall back to `repoRoot`.

### Step 6: Ticket Runner Split

**File:** `internal/engine/ticket_run.go`

Add `RunTicketRequest.WorktreePath` and `ticketRunState.worktreePath`.

Use `repoRoot` for engine state: tickets, claims, artifacts, run JSON, and
verification logs. Use `worktreePath` for worker/reviewer CWD, git diff
collection, changed-file collection, and verification command execution.

### Step 7: Epic Wave Flow

**Files:**

- `internal/engine/epic_run.go`
- `internal/engine/resume.go`

Replace shared-workspace baseline subtraction with per-ticket worktrees:

1. Reconcile stale worktrees once at epic startup.
2. Assert the main tree is safe for an isolated wave.
3. Create worktrees from the current integration base.
4. Run workers, verification, repair, and review in those worktrees.
5. Persist diffs for every non-empty ticket result.
6. Freeze accepted ticket results as internal ticket refs.
7. Detect intra-wave conflicts before integration.
8. Merge only accepted ticket refs into the integration base.
9. Run wave-level checks on the integrated result.
10. Commit the integrated result and advance the hidden integration ref for the
    next wave.
11. Apply the integrated wave delta to main.
12. Clean up worktrees on completion or failure.

The same flow applies to resumed epics. Sub-epics must not bypass it.

### Step 8: Direct CLI Compatibility

**File:** `internal/cli/run.go`

Direct single-ticket runs leave `RunTicketRequest.WorktreePath` empty. The ticket
runner fallback maps an empty worktree path to `repoRoot`, preserving existing
interactive behavior.

## Verification

1. Unit tests for git worktree creation/removal.
2. Unit tests for cache-root resolution and stale reconciliation.
3. Unit tests for engine-owned path filtering.
4. Unit tests for merge-back cases: modified, added, deleted, renamed,
   type change, mode-only, symlink, nested paths, and engine-owned rejection.
5. Unit tests for hidden integration base materialization across at least two
   waves.
6. Runtime adapter tests proving worker and reviewer commands use the worktree.
7. Verification runner tests proving artifacts land in `repoRoot` while commands
   run in `workDir`.
8. Unit tests proving worker commits are allowed but only orchestrator-created
   internal ticket refs feed wave integration.
9. Integration test: two tickets in one wave, one leaves a lint error, the other
   still verifies and merges.
10. Resume test: a blocked/resumed epic continues with isolated worktrees.
11. Sub-epic test or explicit unsupported-path test.

## Critical Files

- `internal/adapters/repo/git/repo.go`: add worktree create/remove primitives
- `internal/engine/worktree.go`: add cache-root and worktree manager
- `internal/engine/epic_run.go`: run waves from isolated worktrees
- `internal/engine/resume.go`: preserve isolated flow on resume
- `internal/engine/ticket_run.go`:
  split state between main tree and worktree
- `internal/adapters/runtime/types.go`: carry reviewer worktree path
- `internal/adapters/runtime/claude/adapter.go`:
  run worker/reviewer in worktree
- `internal/adapters/runtime/codex/adapter.go`:
  enforce worktree CWD usage
- `internal/adapters/verify/command/runner.go`:
  split artifact root from command dir
- `internal/cli/run.go`: keep direct single-ticket main-tree behavior
