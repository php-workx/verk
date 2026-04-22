# Validation Coverage Artifacts

## Purpose

`verk run` must answer durable questions after any run:

- Which checks were required by the ticket?
- Which checks were derived from the actual changed files?
- Which checks ran, passed, failed, or were skipped?
- Which failed checks were repaired automatically?
- Which unresolved issue is blocking closure?

The types in `internal/state/validation_coverage.go` plus backward-compatible
field extensions on existing artifacts in `internal/state/types.go` carry that
information across ticket, wave, and epic scopes.

This document is the durable schema reference for:

- [`ver-rcgh`](../docs/plans/2026-04-19-verk-run-repair-oriented-gates.md) —
  introduces these artifacts.
- `ver-y29o` / `ver-1qru` / `ver-ssp3` / `ver-bks9` — consume them.
- `ver-mbvz` / `ver-aw4j` — render them.

## Scope Reuse

A single schema is reused across scopes via `ValidationScope`:

- `ticket` — per-ticket verification + repair.
- `wave` — post-merge wave verification + repair.
- `epic` — epic-level closure gates and broad reviews.

`ValidationCoverageArtifact.TicketID`, `.WaveID`, `.EpicID`, and
`.ChildTicketIDs` identify the owning entity. Consumers should switch on
`Scope` rather than on the specific id field.

## Core Types

| Type | Purpose |
| --- | --- |
| `ValidationCheck` | Stable identity of a check: id, source, command, reason, matched files, severity, advisory flag. |
| `ValidationCheckSource` | `declared` \| `derived` \| `quality` \| `reviewer` \| `operator` — explains why the check exists. |
| `ValidationCheckResult` | `pending` \| `passed` \| `failed` \| `skipped` \| `repaired` — outcome of an execution. |
| `ValidationCheckExecution` | A single run of a check. Multiple executions can exist per check (e.g. initial failure + repair pass). |
| `ValidationCheckSkip` | Records that a check was intentionally not executed (e.g. optional tooling missing). |
| `ValidationBlocker` | Either a check id or review finding id that prevents closure. `RequiresOperator` signals human input is needed. |
| `ValidationRepairRef` | Links a check or finding to a repair cycle artifact. |
| `ValidationRepairLimit` | Records which policy-bounded loop limit stopped further repair. |
| `ValidationCoverageArtifact` | Top-level artifact aggregating the above for a scope. |

## Backward Compatibility

Every new field uses `json:",omitempty"` and every new struct field is
optional:

- `VerificationArtifact.ValidationCoverage` — nil for legacy artifacts.
- `WaveArtifact.ValidationCoverage` — nil for legacy artifacts.
- `CloseoutArtifact.ValidationCoverage` / `UnresolvedCheckID` / `BlockReason`
  — empty / nil for legacy artifacts.
- `RepairCycleArtifact.Scope` / `WaveID` / `EpicID` / `TriggerCheckIDs` /
  `PolicyLimitReached` — empty / nil for legacy artifacts.

Tests in `internal/state/validation_coverage_test.go` round-trip
hand-written legacy JSON payloads to prove older artifacts still load.

## Append-Only Behavior

Executions are append-only: when a failed check is re-run after a repair,
add a new `ValidationCheckExecution` with
`Result = ValidationCheckResultRepaired` rather than overwriting the prior
failure. `ValidationCoverageArtifact.LatestExecution(checkID)` returns the
most recent entry based on `FinishedAt`.

## Blocked Closure

Advisory checks are coverage signals first. A failed advisory check may create a
best-effort repair cycle reference while ticket implementation attempts remain,
but it must not create an unresolved blocker by itself. Required checks and
reviewer/policy-promoted findings are the cases that make a coverage artifact
non-closable.

When a scope cannot close:

1. Set `Closable = false` on the `ValidationCoverageArtifact`.
2. Add a `ValidationBlocker` to `UnresolvedBlockers` with `CheckID` or
   `FindingID` populated and a human-readable `Reason`.
3. If the engine hit a repair-loop policy limit, populate `RepairLimit`
   with the configured name/limit/reached values.
4. Set `BlockReason` to the primary explanation string.
5. For closeout specifically, also set `CloseoutArtifact.UnresolvedCheckID`
   (or the review finding id on `failed_gate = review`) plus
   `CloseoutArtifact.BlockReason`.

Operators and resume logic can then walk the artifacts without parsing log
text.

## Engine Helper

`engine.BuildTicketValidationCoverage(plan, verification, review, cycles)`
projects a ticket-scoped artifact from already-existing plan, verification,
review, and repair cycle artifacts. It never mutates its inputs, and any
argument may be nil. This is the seam later tickets (`ver-y29o`,
`ver-1qru`, `ver-amsh`) use to populate `ValidationCoverage` fields on
`VerificationArtifact` and `CloseoutArtifact` without rewriting the
existing gate logic.
