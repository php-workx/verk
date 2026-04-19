# Verk Run Repair-Oriented Gates Implementation Plan

## Metadata

- Date: 2026-04-19
- Owner: Ronny Unger
- Epic: `ver-vyag`
- Status: planned

## Summary

`verk run` should make ticket, wave, and epic closure more robust without adding
unnecessary friction. The desired behavior is repair-oriented:

1. Inspect the actual implementation state.
2. Derive focused validation checks from changed files and ticket scope.
3. Run declared and derived checks.
4. Route fixable failures to repair workers.
5. Run rigorous reviews before closeout.
6. Block only when `verk` cannot infer a safe solution or repair attempts are
   exhausted.

This plan creates a new epic with detailed child tickets that can be executed by
`verk` itself:

```bash
verk run epic ver-vyag
```

## Problem Context

Recent review findings showed three different failure modes:

- A worker added a Python smoke test that passed its focused pytest command, but
  the broader `pre-push` gate failed on Ruff SIM117.
- A docs worker updated some local self-hosting text but left contradictory
  "Docker Compose only" wording in the same doc area.
- Scanner wording changed from Gitleaks to Betterleaks locally and TruffleHog in
  CI, but stale wording remained in adjacent docs that were outside the named
  ticket files.

The diagnosis was mixed:

- Worker quality gap: a worker missed a contradiction in its direct docs scope.
- Ticket planning gap: the ticket did not name every stale-doc location.
- Epic review gap: final broad validation and cross-ticket review were not
  strict enough before closing the parent.

The implementation must improve all three layers.

## Principles

### Helpful Before Strict

The tool should try to help first. Missing validation metadata is not a reason to
fail immediately when `verk` can derive a reasonable check or route repair work.

### Repair Before Block

If a validation check or review finding is fixable, `verk` should repair it or
delegate it to a repair worker. It should block only when repair is exhausted,
unsafe without user context, or impossible to map to a concrete action.

### Visible Blockers

Blocked or skipped tickets must stay visible with an explanation. A wave summary
such as `3/4 closed` is not enough if the fourth ticket is blocked.

### Bounded Loops

Ticket, wave, and epic repair loops must be bounded by policy settings. The
system must not retry forever.

### Review-Proof Work

Worker and reviewer prompts should set the expectation that the result must
withstand a rigid and brutally honest external review.

## Existing Extension Points

The implementation should reuse existing architecture:

- `internal/adapters/runtime/prompt.go` builds worker and reviewer prompts.
- `internal/engine/ticket_run.go` owns ticket implementation, verification,
  review, repair, and closeout flow.
- `internal/engine/closeout.go` validates closeout evidence.
- `internal/engine/wave_verify.go` already runs wave-level quality commands and
  wave repair cycles.
- `internal/engine/epic_run.go` owns wave scheduling and epic lifecycle.
- `internal/state/types.go` stores artifacts and should carry durable validation
  coverage.
- `internal/tui/plain.go` and `internal/tui/run_model.go` render progress.
- `internal/policy/config.go` already contains repair-cycle limits and quality
  commands.

## Ticket Plan

| Order | Ticket | Priority | Purpose |
| --- | --- | --- | --- |
| 1 | `ver-rcgh` | P2 | Add durable validation coverage artifacts. |
| 2 | `ver-laq2` | P2 | Configure role-based model and reasoning defaults. |
| 3 | `ver-y29o` | P2 | Derive focused checks from changed files and scope. |
| 4 | `ver-1qru` | P1 | Run derived checks with repair retries. |
| 5 | `ver-tidw` | P2 | Strengthen worker and reviewer prompts. |
| 6 | `ver-amsh` | P1 | Repair review findings before closeout. |
| 7 | `ver-ssp3` | P2 | Add wave-level validation coverage and repair routing. |
| 8 | `ver-bks9` | P1 | Add rigorous epic-level review and closure gate. |
| 9 | `ver-mbvz` | P2 | Expose gate and repair status in run output. |
| 10 | `ver-aw4j` | P2 | Add end-to-end coverage and docs. |

## Dependency Graph

```text
ver-rcgh
  -> ver-y29o
    -> ver-1qru
      -> ver-amsh
      -> ver-ssp3
  -> ver-tidw
    -> ver-amsh

ver-laq2
  -> ver-aw4j

ver-amsh + ver-ssp3
  -> ver-bks9
  -> ver-mbvz

ver-bks9 + ver-mbvz + ver-laq2
  -> ver-aw4j
```

## Detailed Implementation Design

### 1. Validation Coverage Artifacts

Add durable state for validation coverage. The data model should explain what
was required, what was derived, what ran, what was skipped, what failed, what was
repaired, and what remains blocked.

Expected concepts:

- `ValidationScope`: ticket, wave, epic.
- `ValidationCheck`: id, source, command, reason, matched files, severity.
- `ValidationCheckSource`: declared, derived, quality, reviewer, operator.
- `ValidationCheckResult`: pending, passed, failed, skipped, repaired.
- `ValidationCoverageArtifact`: checks, results, skipped reasons, blockers,
  repair artifact references.

Compatibility matters. Older artifacts must still load with empty defaults.

### 2. Role-Based Model Defaults

Configure model and reasoning selection by role in policy config rather than in
ticket frontmatter. The default worker profile is `claude`/`sonnet`/`high`; the
default reviewer profile is `claude`/`opus`/`xhigh`. Record the profile actually
used for each worker or reviewer attempt, including fallback reason when a
non-primary profile is used, so run and benchmark results remain auditable
without preventing retry or fallback to another model.

Ticket `model` frontmatter must not affect execution. If retained for backward
compatibility, it should be documented as ignored or deprecated.

### 3. Derived Checks

Add a conservative derivation layer. It should consume changed files, declared
checks, owned paths, and repository tooling signals.

Suggested derivations:

- Go files: package-level `go test`.
- Python files: focused pytest plus Ruff when Ruff tooling exists.
- Markdown files: markdownlint when available and stale-wording searches for
  docs tickets.
- YAML files: YAML parse or lint when a local tool exists.
- Shell files: shellcheck when available.

Derived checks should be focused and cheap. Missing optional tooling should be
recorded as skipped rather than failing the run by default.

### 4. Ticket Closeout

Ticket closeout should run declared and derived checks before closing. Failing
checks should trigger repair while budget remains. Closeout should block only
when repair fails, repair is unsafe without user input, or the system cannot map
the problem to a safe action.

The closeout artifact should include validation coverage so the operator and
resume logic can understand the state later.

### 5. Prompt Pressure

Worker prompts should include a short instruction to inspect the existing
implementation before editing and continue from the actual state.

Worker and reviewer prompts should state that the goal is a robust
implementation or fix that can withstand a rigid and brutally honest external
review.

Epic reviewer prompts must include:

```text
Take a careful look at the task items we created, then conduct a rigorous review
of the current implementation. Find any gaps, incomplete implementations, and
missing tests so that we are confident that our implementation and fixes will
withstand a brutally honest external review.
```

### 6. Review Finding Repair

Review findings should not be report-only. Findings at or above the configured
threshold must be repaired, explicitly waived with a reason, or converted to a
clear blocker. Low-effort low-severity findings should also be repaired when the
system can do so safely.

Repair artifacts should reference the triggering review finding ids.

### 7. Wave Gates

Wave verification should derive checks from the union of wave-changed files.
Wave repair should run before scheduling the next wave when a merged-state issue
is fixable.

Blocked or skipped tickets should remain visible in the wave summary, including
the reason and whether user input is required.

### 8. Epic Closure Gate

Before closing the parent epic, `verk` should gather child tickets, acceptance
criteria, changed files, validation coverage, reviews, blockers, and repair
history. It should run broad configured gates, derive epic-scoped checks, and ask
an epic reviewer to look for gaps, incomplete implementation, and missing tests.

Epic-level findings should be mapped to an existing child when possible. Fixable
findings should be repaired or routed to follow-up work. The epic should block
only when repair or routing cannot safely resolve the issue.

### 9. Output and Interaction

Plain and TUI output should show:

- planned/running/passed/failed/skipped validation checks
- repair cycle starts and outcomes
- blocked tickets with reason
- whether user input is required
- retry or resume instructions for non-interactive runs

Interactive runs may ask whether to unblock and retry when user context is
needed. Non-interactive runs must never hang waiting for input.

### 10. End-to-End Coverage and Docs

Add deterministic integration coverage for:

- Ruff-only failure caught after pytest passed.
- Stale docs wording outside the worker's named file.
- Wave-level merged-state repair.
- Epic-level review finding before closure.
- Non-interactive blocker output.

Update docs to explain repair-oriented gates, validation coverage, wave repair,
epic review, and retry/resume behavior.

## Validation Strategy

Each ticket includes its own validation commands. The final epic should run at
least:

```bash
go test ./internal/engine/... ./internal/adapters/runtime/... \
  ./internal/tui/... ./internal/cli/...
go test ./internal/e2e/...
```

If repository-wide quality commands are configured, the epic closure gate should
run them before closing.

## Risks

### Token Growth

More review and repair context can increase token usage. Mitigate by passing
focused diffs, check outputs, and child summaries instead of full artifacts when
possible.

### Slow Runs

Derived checks can slow down large epics. Mitigate with package-level and
file-level checks first, and reserve broad gates for wave or epic closure.

### Noisy False Positives

Derived checks and stale-doc searches can overreach. Mitigate by recording
advisory checks separately and requiring repair/block only for configured
required checks or reviewer-confirmed findings.

### Endless Repair Loops

Repair loops can repeat if the engine does not persist state. Mitigate with
existing policy limits and durable repair-cycle artifacts tied to check ids or
finding ids.

## Definition of Done

The epic is done when:

1. All child tickets are closed.
2. Ticket closeout records validation coverage and repairs failed checks.
3. Review findings are repaired or explicitly blocked before closure.
4. Wave gates derive and repair merged-state issues.
5. Epic closure runs broad gates and rigorous review.
6. Blocked/skipped tickets are visible with reasons.
7. E2E tests cover the motivating failure modes.
8. Docs explain the behavior for operators.
