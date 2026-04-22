# Ticket State Machine Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Make ticket execution use explicit, user-facing state-machine outcomes so failures, decision points, and true blockers are not all collapsed into `blocked`.

**Architecture:** Keep `TicketPhase` as the execution cursor (`implement`, `verify`, `review`, `repair`, `closeout`) and add a separate terminal/interruption `TicketOutcome` for what happened when automation stopped. Store the outcome in `ticket-run.json`, derive legacy behavior from it during migration, and only later change scheduler/CLI decisions to use outcomes directly.

**Tech Stack:** Go state artifacts in `internal/state`, engine orchestration in `internal/engine`, CLI rendering in `internal/cli`, JSON artifacts under `.verk/runs`.

---

## Background

The current model has two overloaded concepts:

- `ticket status` in markdown (`open`, `ready`, `in_progress`, `blocked`, `closed`)
- `ticket phase` in run artifacts (`intake`, `implement`, `verify`, `review`, `repair`, `closeout`, `closed`, `blocked`)

The `blocked` phase currently covers several different situations:

- safe automatic retry exists
- repair budget was exhausted and the operator should choose what to do
- the ticket has a true external blocker
- run artifacts are missing or unsafe to trust
- the epic is waiting on dependencies

That makes CLI guidance confusing. It also led to invalid reopen suggestions when ticket-store status said `blocked` but the current run had no per-ticket snapshot.

The new model should reserve true `blocked` for cases that cannot progress until external conditions change. Most automation failures should be `failed_retryable` first, then `needs_decision` after budgets or repeated failures are exhausted.

## Target State Model

### Ticket Store Status

Markdown ticket status remains coarse and scheduler-friendly:

- `ready`: claimable work
- `in_progress`: actively owned by a run
- `blocked`: cannot be scheduled until external conditions change
- `closed`: complete

During migration, `open` remains accepted as an alias/default for unscheduled work.

### Execution Phase

`TicketPhase` remains the fine-grained execution cursor:

- `intake`
- `implement`
- `verify`
- `review`
- `repair`
- `closeout`
- `closed`
- `blocked` (legacy only during migration)

Long term, `blocked` should stop being an execution phase. It remains readable for old artifacts.

### Outcome

Add `TicketOutcome`:

- `closed`: ticket met closeout requirements
- `failed_retryable`: automation failed, but Verk has enough reliable state to retry automatically
- `needs_decision`: automation stopped because the next step requires an operator choice
- `blocked`: no useful retry exists until an external condition changes
- `cancelled`: operator interrupted the run

Empty outcome means “still running or legacy artifact with no outcome.”

## Behavioral Rules

1. Formatting, linting, tests, verification failures, and review findings are not true blockers on first failure. They enter repair or `failed_retryable`.
2. Exhausted repair budgets should become `needs_decision`, not `blocked`, unless the reason is a true external prerequisite.
3. Scope violations and dirty overlapping worktrees should become `needs_decision`: retrying may be possible, but the operator must choose.
4. Missing credentials, missing tools, claim divergence, malformed artifacts, impossible dependencies, and contradictory tickets are true `blocked`.
5. Ticket-store status alone must never imply automatic retry. Automatic retry requires a trusted run snapshot with a retryable outcome or legal legacy phase.
6. Interactive CLI should ask for decisions when outcome is `needs_decision`.
7. Non-interactive CLI should print exact commands and exit non-zero for `needs_decision`.

---

## Task 1: Add Outcome Types And Snapshot Field

**Files:**
- Modify: `internal/state/types.go`
- Modify: `internal/engine/ticket_run.go`
- Test: `internal/engine/ticket_run_test.go`

**Step 1: Write the failing test**

Add a test that runs a normal ticket and asserts the persisted `ticket-run.json` includes `outcome: "closed"` when `CurrentPhase` is `closed`.

Also add a direct snapshot test for active phases:

```go
func TestTicketRunSnapshotOutcome_ActivePhaseOmitsOutcome(t *testing.T) {
    st := &ticketRunState{
        req: RunTicketRequest{
            RunID: "run-outcome",
            Ticket: tkmd.Ticket{ID: "ver-outcome"},
        },
        currentPhase: state.TicketPhaseVerify,
    }
    snap := st.snapshot()
    if snap.Outcome != "" {
        t.Fatalf("expected empty outcome for active phase, got %q", snap.Outcome)
    }
}
```

**Step 2: Run the focused test**

Run:

```bash
go test ./internal/engine -run 'TestRunTicket_HappyPath|TestTicketRunSnapshotOutcome' -count=1
```

Expected: fail because `Outcome` does not exist.

**Step 3: Add the minimal implementation**

Add:

```go
type TicketOutcome string

const (
    TicketOutcomeClosed          TicketOutcome = "closed"
    TicketOutcomeFailedRetryable TicketOutcome = "failed_retryable"
    TicketOutcomeNeedsDecision   TicketOutcome = "needs_decision"
    TicketOutcomeBlocked         TicketOutcome = "blocked"
    TicketOutcomeCancelled       TicketOutcome = "cancelled"
)
```

Add `Outcome state.TicketOutcome "json:\"outcome,omitempty\""` to `TicketRunSnapshot`.

Set outcome in `ticketRunState.snapshot()` using a small helper:

```go
func ticketOutcomeForPhase(phase state.TicketPhase) state.TicketOutcome {
    switch phase {
    case state.TicketPhaseClosed:
        return state.TicketOutcomeClosed
    case state.TicketPhaseBlocked:
        return state.TicketOutcomeBlocked
    default:
        return ""
    }
}
```

This is deliberately conservative: it makes artifacts outcome-aware without changing scheduler behavior yet.

**Step 4: Run tests**

Run:

```bash
go test ./internal/engine -run 'TestRunTicket_HappyPath|TestTicketRunSnapshotOutcome' -count=1
go test ./internal/engine
```

Expected: pass.

**Step 5: Commit**

```bash
git add internal/state/types.go internal/engine/ticket_run.go internal/engine/ticket_run_test.go
git commit -m "feat(engine): persist ticket run outcomes"
```

---

## Task 2: Derive Retry Guidance From Outcome

**Files:**
- Modify: `internal/engine/epic_run.go`
- Modify: `internal/engine/reopen.go`
- Test: `internal/engine/epic_run_test.go`
- Test: `internal/engine/reopen_test.go`

**Step 1: Write failing tests**

Add tests for these cases:

- snapshot `Outcome=failed_retryable`, `CurrentPhase=verify` offers retry to `verify` or `implement` according to the selected policy.
- snapshot `Outcome=needs_decision` is listed but does not auto-generate a retry command.
- snapshot `Outcome=blocked` is listed as blocked and does not auto-generate a retry command unless the operator explicitly chooses a legal reopen target.
- old snapshot with `CurrentPhase=blocked` and empty outcome keeps legacy retry behavior for backward compatibility.

**Step 2: Implement outcome-aware retry target helper**

Introduce:

```go
func DefaultReopenTargetForSnapshot(snapshot TicketRunSnapshot) (state.TicketPhase, bool)
```

Rules:

- `failed_retryable`: reopen to the best safe phase from the snapshot. Initial policy can use `implement` for simplicity.
- `needs_decision`: not automatically retryable.
- `blocked`: not automatically retryable.
- empty outcome: fall back to `DefaultReopenTargetForPhase`.

**Step 3: Wire blocked-ticket collection**

Update `collectBlockedTickets` so trusted snapshots drive retry guidance. Ticket-store status alone may list a ticket as not closed, but cannot produce a retry command.

**Step 4: Run tests**

Run:

```bash
go test ./internal/engine -run 'TestCollectBlockedTickets|TestReopen' -count=1
go test ./internal/engine
```

**Step 5: Commit**

```bash
git add internal/engine/epic_run.go internal/engine/epic_run_test.go internal/engine/reopen.go internal/engine/reopen_test.go
git commit -m "feat(engine): derive retry guidance from ticket outcomes"
```

---

## Task 3: Classify Common Stop Reasons

**Files:**
- Modify: `internal/engine/ticket_run.go`
- Modify: `internal/engine/wave_scheduler.go`
- Test: `internal/engine/ticket_run_test.go`
- Test: `internal/engine/wave_scheduler_test.go`

**Step 1: Add classification tests**

Cover:

- verification failure with repair budget remaining becomes repair, not terminal
- verification failure after budget exhaustion becomes `needs_decision`
- review findings after repair budget exhaustion become `needs_decision`
- worker `needs_context` becomes `blocked`
- missing/expired claim or claim divergence becomes `blocked`
- scope violation becomes `needs_decision`
- operator cancellation becomes `cancelled`

**Step 2: Add outcome classifier**

Introduce an internal helper:

```go
func classifyTicketStop(reason string, phase state.TicketPhase, kind stopKind) state.TicketOutcome
```

Keep it deterministic. Do not ask an LLM to classify stop reasons.

**Step 3: Set outcome at block sites**

Every transition to legacy `TicketPhaseBlocked` must set an explicit outcome.

**Step 4: Run tests**

Run:

```bash
go test ./internal/engine -run 'TestRunTicket_.*Block|TestRunTicket_.*Decision|TestBuildBlockedTicketSummary' -count=1
go test ./internal/engine
```

**Step 5: Commit**

```bash
git add internal/engine/ticket_run.go internal/engine/ticket_run_test.go internal/engine/wave_scheduler.go internal/engine/wave_scheduler_test.go
git commit -m "feat(engine): classify ticket stop outcomes"
```

---

## Task 4: Add Operator Decision Rendering

**Files:**
- Modify: `internal/cli/run.go`
- Modify: `internal/cli/reopen.go`
- Test: `internal/cli/run_blocked_test.go`
- Test: `internal/cli/reopen_test.go`

**Step 1: Write CLI rendering tests**

For a `needs_decision` ticket, assert output includes:

- the ticket id
- concise reason
- available actions
- exact commands for non-interactive mode

Example commands:

```text
verk reopen <run-id> <ticket-id> --to implement
verk reopen <run-id> <ticket-id> --to repair
verk block <ticket-id> --reason "..."
```

**Step 2: Implement rendering**

Add separate sections:

- `Retryable tickets`
- `Tickets needing decision`
- `Blocked tickets`

Avoid calling everything “blocked.”

**Step 3: Run tests**

Run:

```bash
go test ./internal/cli -run 'TestRun.*Blocked|TestReopen' -count=1
go test ./internal/cli
```

**Step 4: Commit**

```bash
git add internal/cli/run.go internal/cli/reopen.go internal/cli/run_blocked_test.go internal/cli/reopen_test.go
git commit -m "feat(cli): render ticket decisions separately from blockers"
```

---

## Task 5: Add Interactive Decisions

**Files:**
- Modify: `internal/cli/run.go`
- Modify: `internal/engine/reopen.go`
- Test: `internal/cli/run_blocked_test.go`

**Step 1: Add interaction seam**

Introduce a small prompt interface so tests can inject answers without a real terminal:

```go
type decisionPrompter interface {
    ChooseTicketDecision(ticket engine.BlockedTicket) (ticketDecision, error)
}
```

**Step 2: Implement choices**

Supported interactive choices:

- retry from implement
- retry from repair
- leave as needs decision
- mark blocked
- stop

Do not auto-edit tickets in this task.

**Step 3: Add tests**

Cover one accepted retry and one “leave as decision” case.

**Step 4: Run tests**

Run:

```bash
go test ./internal/cli -run 'TestRun.*Decision' -count=1
go test ./internal/cli
```

**Step 5: Commit**

```bash
git add internal/cli/run.go internal/cli/run_blocked_test.go internal/engine/reopen.go
git commit -m "feat(cli): ask operators for ticket decisions"
```

---

## Task 6: Update Docs And Migration Notes

**Files:**
- Modify: `docs/plans/INDEX.md`
- Modify: `docs/plans/2026-04-22-ticket-state-machine.md`
- Modify or create: `docs/ticket-state-machine.md`

**Step 1: Write user-facing docs**

Explain:

- ready vs in-progress vs failed retryable vs needs decision vs blocked
- what `verk run` does in interactive mode
- what non-interactive mode prints
- how old `blocked` artifacts are interpreted

**Step 2: Run docs checks**

Run:

```bash
just docs-check
```

If no docs target exists, run:

```bash
just pre-commit
```

**Step 3: Commit**

```bash
git add docs/plans/INDEX.md docs/plans/2026-04-22-ticket-state-machine.md docs/ticket-state-machine.md
git commit -m "docs: describe ticket state machine outcomes"
```

---

## Rollout Guardrails

- Preserve backward compatibility for existing `ticket-run.json` artifacts.
- Do not remove `TicketPhaseBlocked` until all old artifacts and CLI paths are migrated.
- Do not let ticket-store status alone create reopen commands.
- Keep `blocked` as last resort in user-facing output.
- Each task must pass `go test ./internal/engine` or `go test ./internal/cli` as appropriate.
- Run `just pre-commit` before merging.

## Open Questions

- Should `failed_retryable` reopen to the failed phase or always to `implement`?
- Should repeated identical `failed_retryable` outcomes auto-promote to `needs_decision` after a run-level threshold?
- Should `needs_decision` be stored in ticket markdown status, or only in run artifacts?
- Do we need a separate `waiting_on_deps` outcome, or is that only an epic scheduling condition?
