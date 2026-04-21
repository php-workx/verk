# Memory Learning Loop Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Capture escaped defects from failed or blocked verk runs and turn them into reviewable, reusable ticket-quality knowledge.

**Architecture:** Add a small repo-local memory store for run lessons, a command to record and review lessons, and a promotion path from approved lessons into ticket-quality lint rules or planner-review guidance. Keep the loop human-reviewed first; do not auto-change quality gates from raw failures.

**Tech Stack:** Go, Cobra CLI, existing `.verk/runs/<run-id>/` artifacts, markdown or JSONL memory files under `.verk/memory/`, existing ticket quality gate plan.

---

## Problem

`verk` can learn from repeated failures only if the signal survives the current
session. Right now, escaped defects are discussed in chat, maybe fixed once, and
then lost. The ticket quality gate should eventually benefit from these
failures, but it should not ingest unreviewed noise automatically.

This plan adds a lightweight memory loop:

1. Capture what escaped.
2. Classify why existing gates missed it.
3. Store the lesson durably.
4. Review and promote useful lessons into ticket-quality rules or prompts.

## Non-Goals

- Do not build a general knowledge base.
- Do not auto-generate implementation tickets.
- Do not let raw failed-run data automatically change blocking behavior.
- Do not require remote services or external vector stores.
- Do not duplicate the ticket quality gate implementation plan.

## Memory Model

Store lessons in repo-local files:

```text
.verk/memory/escaped-defects.jsonl
.verk/memory/promoted-rules.jsonl
.verk/memory/README.md
```

Each escaped-defect entry:

```json
{
  "id": "learn-20260421-001",
  "created_at": "2026-04-21T12:00:00Z",
  "source_run_id": "run-ver-vyag-...",
  "source_ticket_ids": ["ver-1234"],
  "summary": "CLI flag was required by plan but not exposed by parser.",
  "missed_by": ["ticket_acceptance", "reviewer", "validation"],
  "recommended_rule": "CLI tickets must include black-box command scenarios for required flags.",
  "candidate_quality_codes": ["missing_public_contract_scenario"],
  "status": "proposed"
}
```

Promotion entry:

```json
{
  "lesson_id": "learn-20260421-001",
  "promoted_at": "2026-04-21T12:30:00Z",
  "target": "ticket_quality_rule",
  "rule_id": "public-cli-required-flags",
  "summary": "Require black-box command scenarios for planned public flags."
}
```

## CLI Surface

```bash
verk learn escaped --run <run-id> --summary "..." --missed-by ticket_acceptance,reviewer
verk learn list
verk learn show <lesson-id>
verk learn promote <lesson-id> --target ticket-quality-rule
```

Optional later:

```bash
verk learn suggest --run <run-id>
```

`suggest` can inspect artifacts and draft a lesson, but should not promote it.

## Task 1: Add Memory Store Types

**Files:**

- Create: `internal/memory/types.go`
- Create: `internal/memory/store.go`
- Test: `internal/memory/store_test.go`

Implement append-only JSONL storage for escaped defects and promoted rules.

Tests:

```bash
go test ./internal/memory -count=1
```

Acceptance:

- appends valid lessons
- rejects missing summary
- lists lessons in insertion order
- writes under `.verk/memory/`

## Task 2: Add `verk learn` Commands

**Files:**

- Modify: `internal/cli/root.go`
- Create: `internal/cli/learn.go`
- Test: `internal/cli/learn_test.go`

Implement:

- `verk learn escaped`
- `verk learn list`
- `verk learn show`

Keep `promote` stubbed behind a clear “not implemented” error until Task 3.

Tests:

```bash
go test ./internal/cli -run 'TestLearn' -count=1
```

Acceptance:

- users can record a lesson manually
- list output is concise
- show output includes source run, tickets, missed-by, and recommendation

## Task 3: Add Human-Reviewed Promotion

**Files:**

- Modify: `internal/memory/store.go`
- Modify: `internal/cli/learn.go`
- Test: `internal/memory/store_test.go`
- Test: `internal/cli/learn_test.go`

Implement:

```bash
verk learn promote <lesson-id> --target ticket-quality-rule --rule-id <id>
```

Promotion should:

- require an explicit `--rule-id`
- mark lesson status as `promoted`
- append to `promoted-rules.jsonl`
- print the exact target rule id

It should not edit ticket quality code automatically.

Tests:

```bash
go test ./internal/memory ./internal/cli -run 'Test.*Promote|TestLearn' -count=1
```

## Task 4: Feed Promoted Rules Into Ticket Quality Review Context

**Files:**

- Modify: `internal/engine/ticket_quality.go`
- Modify: `internal/adapters/runtime/prompt.go`
- Test: `internal/engine/ticket_quality_test.go`
- Test: `internal/adapters/runtime/prompt_test.go`

Use promoted rules as advisory context:

- deterministic rules can emit advisory findings first
- planner-review prompt includes promoted rules as “lessons from prior escaped defects”
- blocking behavior requires a code change to the ticket quality gate, not just
  a promoted-memory entry

Tests:

```bash
go test ./internal/engine ./internal/adapters/runtime -run 'Test.*PromotedRule|Test.*Planner' -count=1
```

## Task 5: Add Docs And Index Entry

**Files:**

- Create: `docs/memory-learning-loop.md`
- Modify: `docs/plans/INDEX.md`

Document:

- what counts as an escaped defect
- how to record a lesson
- how promotion works
- why promotion is human-reviewed
- how this relates to the ticket quality gate

Verification:

```bash
just format-check
just lint-check
go test ./internal/memory ./internal/cli ./internal/engine ./internal/adapters/runtime -count=1
```

## Acceptance Criteria

- Escaped-defect lessons can be recorded durably in `.verk/memory/`.
- Lessons can be listed and inspected from the CLI.
- Lessons can be promoted only through an explicit command.
- Promotion does not silently change blocking ticket-quality behavior.
- Promoted rules can be included as context for future ticket quality review.
- Documentation explains the loop and its safety boundaries.

## Open Questions

- Should `.verk/memory/` be committed by default, or should projects choose?
- Should lessons reference artifact paths directly or only run/ticket ids?
- Should `verk learn suggest --run` be added in v1, or wait until enough manual
  lessons exist to design it well?
