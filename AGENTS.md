# AGENTS.md

`AGENTS.md` is the durable repo instruction file. Do not put session memory,
ticket transcripts, or scratch notes here. Use `.agents/local-context.md` for
ephemeral local context instead.

## What This Repo Is

`verk` is an execution engine for ticketed implementation work. It operates on
file-backed tasks, runs deterministic local gates, and supports longer-running
execution patterns such as worktree-backed ticket isolation.

## Hard Rules

- Keep this file stable. Never inline `<claude-mem-context>` or other session
  logs here.
- Treat ticket files and runtime state as product surfaces, not casual scratch
  files.
- Use the repo’s `just` targets instead of ad hoc command bundles.
- If a task touches worktree behavior, preserve the cache-root semantics
  described in `README.md`.
- Do not make `clean`-style changes that would destroy active `.verk/runs/`
  state unless the task is explicitly about cleanup or lifecycle handling.

## Common Commands

```bash
just pre-commit
just pre-push
just check
just test
just test-race
just format
just build
```

Useful focused runs:

```bash
go test -count=1 ./...
go test -race -count=1 ./...
```

## Common Workflows

### 1. Engine changes

1. Run at least `just pre-commit`.
2. If the change affects orchestration, isolation, or cleanup semantics, run
   `just test-race` too.
3. Keep runtime-state and ticket-state assumptions explicit.

### 2. Worktree / isolation changes

1. Check `README.md` for the current `VERK_WORKTREE_ROOT` contract.
2. Validate both normal tests and race-detector coverage.
3. Be conservative about cleanup paths and cache-root resolution.

### 3. Ticket or runtime-state changes

1. Treat file formats and on-disk layout as compatibility-sensitive.
2. Preserve stable behavior deliberately; do not mix scratch metadata into
   tracked durable artifacts.

## Repo Facts

- Ephemeral operator context belongs in `.agents/local-context.md`.
- Worktree cache location can be overridden by `VERK_WORKTREE_ROOT`.
- If `VERK_WORKTREE_ROOT` is unset, cache resolution falls back through XDG
  cache, `~/.cache`, then the OS temp dir.

## References

- `README.md` — worktree cache and repo-root runtime notes
- `justfile` — supported local quality gates
