# Ticket Quality Gate Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add a pre-run ticket quality gate that prevents `verk run` from dispatching workers against underspecified, ambiguous, or incomplete tickets.

**Architecture:** Run deterministic ticket lint first, then an optional planner-role LLM review for semantic completeness. Persist a durable quality artifact, apply only safe ticket auto-repairs, and block before worker dispatch when unresolved findings would make the run unreliable.

**Tech Stack:** Go, Cobra CLI, existing `tkmd` ticket store, existing runtime reviewer adapters, JSON artifacts under `.verk/runs/<run-id>/`, existing policy config and tests with fake runtime adapters.

---

## Problem

Recent full-epic execution showed that workers and reviewers can pass the wrong
contract when tickets are too local or vague. Downstream repair gates help after
implementation exists, but they do not solve the upstream failure mode:

- workers optimize for ticket acceptance rather than the source plan
- vague acceptance criteria become the new truth
- public CLI/API contracts miss black-box scenarios
- docs can normalize an incomplete implementation
- epic closure review catches gaps late, after time and tokens are spent

The quality gate must reject or repair weak tickets before the first worker
starts.

## Non-Goals

- Do not build a plan decomposition engine.
- Do not generate child tickets from prose plans.
- Do not implement the learning loop here. Capture a placeholder artifact shape,
  but leave feedback mining and learned rule evolution to a separate plan.
- Do not make worker tickets run heavyweight global checks. This gate validates
  ticket quality, not code quality.
- Do not silently rewrite ambiguous requirements. Auto-repair must be limited to
  safe, mechanical edits.

## Desired User Experience

Manual inspection:

```bash
verk inspect ticket ver-1234
verk inspect ticket ver-1234 --fix
verk inspect epic ver-vyag
verk inspect epic ver-vyag --planner-review --fix
```

Run integration:

```bash
verk run epic ver-vyag
```

Before dispatching the first wave, `verk run epic` runs the quality gate for the
root epic and all runnable child tickets. If blocking findings remain, it exits
before any worker starts and prints:

- the ticket ids with blocking findings
- concise reasons
- which findings were auto-repaired
- exact retry command
- artifact path for full details

Safe auto-repair:

- Manual `verk inspect ... --fix` applies safe repairs.
- `verk run` may apply safe deterministic repairs only when
  `policy.ticket_quality.auto_fix_safe: true`.
- LLM-generated text suggestions are never applied silently; they are written as
  proposed patches/findings unless `--fix` is explicit and the finding is marked
  safe.

## Quality Finding Taxonomy

Use stable finding codes so artifacts, CLI output, tests, and future learning
loop data can aggregate them.

| Code | Severity | Meaning |
| --- | --- | --- |
| `missing_acceptance_criteria` | P1 | Ticket has no non-empty criteria or an epic has no measurable closure criteria. |
| `ambiguous_acceptance_criterion` | P2 | Criterion uses vague wording without expected observable behavior. |
| `compound_acceptance_criterion` | P3 | Criterion packs multiple independently verifiable requirements into one line. |
| `missing_validation_commands` | P2 | Ticket has no declared command and no obvious derived check path. |
| `missing_owned_paths` | P1 | Non-epic implementation ticket has no scope, or epic scope is empty. |
| `owned_path_missing` | P2 | An owned path does not exist and is not clearly a new file path. |
| `dependency_missing` | P1 | A dependency id does not exist. |
| `dependency_blocked_or_closed_mismatch` | P2 | Dependency/status relationship is inconsistent enough to risk scheduling surprises. |
| `missing_public_contract_scenario` | P1 | CLI/API/user-facing ticket lacks black-box expected command/API behavior. |
| `missing_negative_case` | P2 | Validation/error-handling ticket lacks at least one failure-path expectation. |
| `docs_descope_risk` | P1 | Ticket/docs wording appears to remove or deny planned behavior without an explicit plan update. |
| `integration_gap` | P1 | Epic child set lacks an integration/traceability ticket for a multi-surface feature. |
| `plan_traceability_gap` | P2 | Ticket references a plan/spec but does not quote or link the exact requirement it owns. |
| `reviewer_instruction_gap` | P3 | Ticket has unusual risk but no reviewer guidance or validation expectation. |

## Artifact Model

Add a durable artifact so the run state can explain why no workers were
dispatched.

Create `internal/state/ticket_quality.go`:

```go
package state

type TicketQualityStatus string

const (
	TicketQualityPassed  TicketQualityStatus = "passed"
	TicketQualityRepaired TicketQualityStatus = "repaired"
	TicketQualityBlocked TicketQualityStatus = "blocked"
)

type TicketQualityFinding struct {
	ID              string   `json:"id"`
	TicketID        string   `json:"ticket_id"`
	Code            string   `json:"code"`
	Severity        Severity `json:"severity"`
	Title           string   `json:"title"`
	Body            string   `json:"body"`
	Evidence        []string `json:"evidence,omitempty"`
	Repairable      bool     `json:"repairable"`
	AutoRepairable  bool     `json:"auto_repairable"`
	RequiresPlanner bool     `json:"requires_planner,omitempty"`
	Disposition     string   `json:"disposition"`
}

type TicketQualityRepair struct {
	FindingID string `json:"finding_id"`
	TicketID  string `json:"ticket_id"`
	Kind      string `json:"kind"`
	Summary   string `json:"summary"`
	Applied   bool   `json:"applied"`
}

type TicketQualityArtifact struct {
	ArtifactMeta
	Scope        string                 `json:"scope"`
	RootTicketID string                 `json:"root_ticket_id,omitempty"`
	TicketIDs    []string               `json:"ticket_ids"`
	Status       TicketQualityStatus    `json:"status"`
	Findings     []TicketQualityFinding `json:"findings"`
	Repairs      []TicketQualityRepair  `json:"repairs,omitempty"`
	Blocked      bool                   `json:"blocked"`
	BlockReason  string                 `json:"block_reason,omitempty"`
}
```

Artifact paths:

```text
.verk/runs/<run-id>/ticket-quality.json
.verk/runs/<run-id>/ticket-quality-planner-review.json
```

## Policy Config

Extend `policy.Config` with:

```yaml
policy:
  ticket_quality:
    enabled: true
    planner_review: true
    auto_fix_safe: false
    block_threshold: P2
    require_public_contract_scenarios: true
    require_epic_integration_ticket: true
```

Defaults:

- `enabled: true`
- `planner_review: true` for epics, `false` for single tickets unless requested
- `auto_fix_safe: false`
- `block_threshold: P2`
- public contract and epic integration checks enabled

## Task 1: Add Ticket Quality State Types

**Files:**

- Create: `internal/state/ticket_quality.go`
- Test: `internal/state/ticket_quality_test.go`
- Modify: `docs/plans/validation-coverage.md`

**Step 1: Write failing JSON compatibility tests**

Add tests that marshal and unmarshal:

- a passed artifact with no findings
- a blocked artifact with one finding
- a repaired artifact with one applied repair

Run:

```bash
go test ./internal/state -run 'TestTicketQualityArtifact' -count=1
```

Expected: FAIL because the types do not exist.

**Step 2: Implement state types**

Add the structs from the artifact model. Reuse `state.Severity` and
`ArtifactMeta`.

**Step 3: Verify**

Run:

```bash
go test ./internal/state -run 'TestTicketQualityArtifact' -count=1
```

Expected: PASS.

**Step 4: Commit**

```bash
git add internal/state/ticket_quality.go internal/state/ticket_quality_test.go docs/plans/validation-coverage.md
git commit -m "feat: add ticket quality artifact types"
```

## Task 2: Add Deterministic Ticket Linter

**Files:**

- Create: `internal/engine/ticket_quality.go`
- Test: `internal/engine/ticket_quality_test.go`
- Reuse: `internal/adapters/ticketstore/tkmd/types.go`
- Reuse: `internal/adapters/ticketstore/tkmd/store.go`

**Step 1: Write failing unit tests**

Cover:

- missing acceptance criteria blocks
- vague criterion warns or blocks at threshold
- missing owned paths blocks implementation tickets
- missing dependency blocks
- public CLI ticket without command scenario blocks
- docs de-scope wording creates a planner-required finding
- multi-surface epic without an integration ticket blocks

Use `tkmd.Ticket` directly. Do not require filesystem setup for the first pass.

Run:

```bash
go test ./internal/engine -run 'TestTicketQuality' -count=1
```

Expected: FAIL because `EvaluateTicketQuality` does not exist.

**Step 2: Implement pure evaluator**

Add:

```go
type TicketQualityInput struct {
	RootTicket tkmd.Ticket
	Tickets    []tkmd.Ticket
	ExistingPaths map[string]bool
	Config    policy.Config
}

func EvaluateTicketQuality(input TicketQualityInput) state.TicketQualityArtifact
```

Rules:

- Generate stable finding ids from `ticket_id + code + normalized evidence`.
- Treat criteria containing only vague phrases such as `works`, `done`,
  `handled`, `state`, `support`, `properly`, `ready`, `complete` as ambiguous
  unless accompanied by concrete command, exit code, output, field, file, or
  behavior.
- Detect public contract tickets by title/body/owned paths containing CLI/API
  signals: `cmd/`, `internal/cli`, `subcommand`, `flag`, `exit code`, `stdout`,
  `stderr`, `API`, `HTTP`, `endpoint`.
- Public contract tickets need at least one criterion or test case mentioning a
  concrete invocation, flag, status/exit code, or output.
- Detect docs de-scope risk when docs tickets include phrases like
  `not supported`, `no --`, `does not support`, `only supports`, `remove support`
  and no body text references an explicit plan update.
- Detect multi-surface epics by child owned paths spanning two or more of:
  CLI, config, runtime, docs, tests, engine. Require a child with title/body
  containing `integration`, `traceability`, `e2e`, or `end-to-end`.

**Step 3: Verify**

Run:

```bash
go test ./internal/engine -run 'TestTicketQuality' -count=1
```

Expected: PASS.

**Step 4: Commit**

```bash
git add internal/engine/ticket_quality.go internal/engine/ticket_quality_test.go
git commit -m "feat: add deterministic ticket quality lint"
```

## Task 3: Add Safe Auto-Repair

**Files:**

- Modify: `internal/engine/ticket_quality.go`
- Test: `internal/engine/ticket_quality_test.go`
- Modify: `internal/adapters/ticketstore/tkmd/store.go` only if save behavior needs preserving unknown frontmatter order

**Safe repairs allowed:**

- trim empty criteria/test cases/validation commands
- add missing `owned_paths` to an epic from the union of child owned paths
- add a `test_cases` placeholder only when a concrete validation command already exists
- add a `ticket_quality_notes` frontmatter entry with planner-required findings

**Unsafe repairs not allowed:**

- invent expected behavior
- invent public CLI command semantics
- split compound criteria into new criteria without operator approval
- rewrite docs to include or remove feature claims

**Step 1: Write failing repair tests**

Test:

- epic gets union owned paths only when all children have non-empty scopes
- missing public scenario produces suggested text but no applied repair
- ambiguous criterion is not rewritten

Run:

```bash
go test ./internal/engine -run 'TestTicketQualityRepair' -count=1
```

Expected: FAIL.

**Step 2: Implement repair projection**

Add:

```go
type TicketQualityRepairPlan struct {
	Tickets map[string]tkmd.Ticket
	Repairs []state.TicketQualityRepair
}

func BuildTicketQualityRepairPlan(input TicketQualityInput, artifact state.TicketQualityArtifact) TicketQualityRepairPlan
```

Keep this pure. Filesystem writes happen in the CLI/run integration layer.

**Step 3: Verify**

Run:

```bash
go test ./internal/engine -run 'TestTicketQualityRepair' -count=1
```

Expected: PASS.

**Step 4: Commit**

```bash
git add internal/engine/ticket_quality.go internal/engine/ticket_quality_test.go
git commit -m "feat: plan safe ticket quality repairs"
```

## Task 4: Add CLI Inspection Commands

**Files:**

- Modify: `internal/cli/root.go`
- Create: `internal/cli/inspect.go`
- Test: `internal/cli/inspect_test.go`

**Commands:**

```text
verk inspect ticket <ticket-id> [--fix] [--planner-review]
verk inspect epic <ticket-id> [--fix] [--planner-review]
```

**Step 1: Write failing CLI tests**

Use a temp repo with `.tickets/`.

Assert:

- `verk inspect ticket bad-ticket` exits non-zero and prints finding codes
- `verk inspect epic root` includes child findings
- `--fix` applies only safe repairs
- no worker/runtime adapter is invoked by deterministic inspection

Run:

```bash
go test ./internal/cli -run 'TestInspect' -count=1
```

Expected: FAIL.

**Step 2: Implement command loading**

Use `tkmd.LoadTicket` and `tkmd.ListAllChildren` for epic mode. Build
`TicketQualityInput`, call `EvaluateTicketQuality`, optionally apply the repair
plan with `tkmd.SaveTicket`, and print a concise table:

```text
ticket quality: blocked

ver-1234 P1 missing_public_contract_scenario
  CLI/API ticket needs a black-box command scenario with expected exit/output.

Artifact: .verk/runs/<inspect-run-id>/ticket-quality.json
```

For standalone `verk inspect`, writing an artifact is useful but should not
create a full run directory unless the CLI already has a helper for ad-hoc
artifact dirs. If no helper exists, print only and leave run artifacts for
`verk run` integration.

**Step 3: Verify**

Run:

```bash
go test ./internal/cli -run 'TestInspect' -count=1
```

Expected: PASS.

**Step 4: Commit**

```bash
git add internal/cli/root.go internal/cli/inspect.go internal/cli/inspect_test.go
git commit -m "feat: add ticket quality inspect command"
```

## Task 5: Add Planner-Role Review Prompt

**Files:**

- Modify: `internal/adapters/runtime/types.go`
- Modify: `internal/adapters/runtime/prompt.go`
- Test: `internal/adapters/runtime/prompt_test.go`
- Modify: `internal/adapters/runtime/codex/adapter.go`
- Modify: `internal/adapters/runtime/claude/adapter.go`
- Test: `internal/adapters/runtime/codex/adapter_test.go`
- Test: `internal/adapters/runtime/claude/adapter_test.go`

**Step 1: Write failing prompt tests**

Assert that planner review prompt includes:

- root ticket and child ticket summaries
- deterministic findings from the first pass
- instruction that this is not implementation review
- instruction to find missing traceability, black-box scenarios, docs de-scope,
  integration gaps, and ambiguous criteria
- JSON-only response schema using the same finding taxonomy

Run:

```bash
go test ./internal/adapters/runtime -run 'TestBuildPlannerReviewPrompt' -count=1
```

Expected: FAIL.

**Step 2: Add request/result types**

Prefer new planner-specific types over overloading code review:

```go
type PlannerReviewRequest struct {
	RootTicketID string
	Tickets []TicketSummary
	DeterministicFindings []state.TicketQualityFinding
	EffectiveReviewThreshold Severity
	LeaseID string
}

type PlannerReviewResult struct {
	Status string
	Summary string
	Findings []RawTicketQualityFinding
}
```

If this creates too much adapter duplication, implement planner review by
building a normal `ReviewRequest` with a planner prompt, but keep planner
artifact parsing separate so code-review findings do not get mixed with ticket
quality findings.

**Step 3: Implement planner prompt**

Add `PlannerSystemPrompt` and `BuildPlannerReviewPrompt`.

The prompt must state:

```text
You are reviewing ticket quality before implementation. Do not review code.
Your job is to decide whether these tickets are specific, complete, and
verifiable enough for workers to deliver the whole epic.
```

**Step 4: Verify**

Run:

```bash
go test ./internal/adapters/runtime -run 'TestBuildPlannerReviewPrompt' -count=1
go test ./internal/adapters/runtime/codex ./internal/adapters/runtime/claude -run 'Test.*Planner' -count=1
```

Expected: PASS.

**Step 5: Commit**

```bash
git add internal/adapters/runtime/types.go internal/adapters/runtime/prompt.go internal/adapters/runtime/prompt_test.go internal/adapters/runtime/codex/adapter.go internal/adapters/runtime/codex/adapter_test.go internal/adapters/runtime/claude/adapter.go internal/adapters/runtime/claude/adapter_test.go
git commit -m "feat: add planner ticket quality review prompt"
```

## Task 6: Integrate Gate Into `verk run epic`

**Files:**

- Modify: `internal/engine/epic_run.go`
- Create: `internal/engine/ticket_quality_gate.go`
- Test: `internal/engine/epic_run_test.go`
- Test: `internal/engine/ticket_quality_gate_test.go`
- Modify: `internal/engine/ops_support.go`

**Step 1: Write failing integration tests**

Add tests:

- `TestRunEpic_BlocksBeforeFirstWaveWhenTicketQualityFails`
- `TestRunEpic_AppliesSafeTicketQualityRepairsBeforeDispatchWhenEnabled`
- `TestRunEpic_PersistsTicketQualityArtifact`
- `TestRunEpic_DoesNotRunPlannerReviewWhenPolicyDisabled`

Use a fake adapter and assert no worker calls occur when the gate blocks.

Run:

```bash
go test ./internal/engine -run 'TestRunEpic_.*TicketQuality|TestTicketQualityGate' -count=1
```

Expected: FAIL.

**Step 2: Implement gate function**

Add:

```go
func runTicketQualityGate(ctx context.Context, req RunEpicRequest, cfg policy.Config, root tkmd.Ticket, children []tkmd.Ticket) (state.TicketQualityArtifact, error)
```

Behavior:

- deterministic evaluation always runs when enabled
- planner review runs for epics when enabled
- safe repairs are applied only when policy allows
- artifact is persisted before any worker dispatch
- blocking findings at or above threshold return a typed blocked error

**Step 3: Insert before wave scheduling**

In `RunEpic`, after loading root/children and before creating the first wave,
call `runTicketQualityGate`.

Do not run this gate repeatedly on resume if a passed artifact for the same
ticket set already exists. Use ticket ids and a simple content hash if available;
otherwise rerun on resume until a hash is added in a later task.

**Step 4: Verify**

Run:

```bash
go test ./internal/engine -run 'TestRunEpic_.*TicketQuality|TestTicketQualityGate' -count=1
```

Expected: PASS.

**Step 5: Commit**

```bash
git add internal/engine/epic_run.go internal/engine/ticket_quality_gate.go internal/engine/ticket_quality_gate_test.go internal/engine/epic_run_test.go internal/engine/ops_support.go
git commit -m "feat: gate epic runs on ticket quality"
```

## Task 7: Integrate Gate Into Single-Ticket Runs

**Files:**

- Modify: `internal/engine/ticket_run.go`
- Test: `internal/engine/ticket_run_test.go`

**Step 1: Write failing tests**

Add:

- `TestRunTicket_BlocksBeforeWorkerWhenTicketQualityFails`
- `TestRunTicket_PersistsTicketQualityArtifact`
- `TestRunTicket_AllowsPlannerReviewOptIn`

Run:

```bash
go test ./internal/engine -run 'TestRunTicket_.*TicketQuality' -count=1
```

Expected: FAIL.

**Step 2: Implement single-ticket quality gate**

Run deterministic evaluation before implementation. Planner review is off by
default for single tickets unless policy or CLI requests it.

**Step 3: Verify**

Run:

```bash
go test ./internal/engine -run 'TestRunTicket_.*TicketQuality' -count=1
```

Expected: PASS.

**Step 4: Commit**

```bash
git add internal/engine/ticket_run.go internal/engine/ticket_run_test.go
git commit -m "feat: gate ticket runs on ticket quality"
```

## Task 8: Add Traceability Matrix Artifact

**Files:**

- Modify: `internal/state/ticket_quality.go`
- Modify: `internal/engine/ticket_quality.go`
- Test: `internal/engine/ticket_quality_test.go`
- Modify: `docs/plans/validation-coverage.md`

**Step 1: Write failing tests**

For an epic with plan-linked child tickets, assert the artifact includes:

- source reference
- owning ticket id
- acceptance criterion ids/text
- validation command/test case references
- public contract scenario marker

Run:

```bash
go test ./internal/engine -run 'TestTicketQualityTraceability' -count=1
```

Expected: FAIL.

**Step 2: Implement traceability projection**

Add:

```go
type TicketQualityTrace struct {
	SourceRef string `json:"source_ref,omitempty"`
	TicketID string `json:"ticket_id"`
	Criterion string `json:"criterion"`
	ValidationRefs []string `json:"validation_refs,omitempty"`
	PublicContract bool `json:"public_contract,omitempty"`
}
```

Populate this from ticket body links, criteria, test cases, validation commands,
and public-contract detection.

**Step 3: Verify**

Run:

```bash
go test ./internal/engine -run 'TestTicketQualityTraceability' -count=1
```

Expected: PASS.

**Step 4: Commit**

```bash
git add internal/state/ticket_quality.go internal/engine/ticket_quality.go internal/engine/ticket_quality_test.go docs/plans/validation-coverage.md
git commit -m "feat: record ticket quality traceability"
```

## Task 9: Surface Quality Gate Status In CLI/TUI

**Files:**

- Modify: `internal/cli/run.go`
- Modify: `internal/tui/plain.go`
- Modify: `internal/tui/run_model.go`
- Test: `internal/cli/run_test.go`
- Test: `internal/tui/plain_test.go`

**Step 1: Write failing output tests**

Assert blocked output includes:

```text
Ticket quality gate blocked before worker dispatch
ver-1234 P1 missing_public_contract_scenario
Artifact: .verk/runs/<run-id>/ticket-quality.json
Retry: verk inspect epic <id> --fix
```

Run:

```bash
go test ./internal/cli ./internal/tui -run 'Test.*TicketQuality' -count=1
```

Expected: FAIL.

**Step 2: Implement progress events and rendering**

Add progress event types or reuse existing detail events with stable detail
strings. Prefer explicit event types if the TUI model needs structured status.

**Step 3: Verify**

Run:

```bash
go test ./internal/cli ./internal/tui -run 'Test.*TicketQuality' -count=1
```

Expected: PASS.

**Step 4: Commit**

```bash
git add internal/cli/run.go internal/cli/run_test.go internal/tui/plain.go internal/tui/plain_test.go internal/tui/run_model.go
git commit -m "feat: display ticket quality gate failures"
```

## Task 10: Add E2E Coverage And Docs

**Files:**

- Create: `internal/e2e/ticket_quality_gate_test.go`
- Modify: `docs/plans/INDEX.md`
- Create or modify: `docs/ticket-quality-gate.md`
- Modify: `README.md` if run behavior is documented there

**Step 1: Write e2e tests**

Scenarios:

- CLI ticket missing black-box scenario blocks before worker dispatch.
- Epic touching CLI, config, and docs without integration ticket blocks.
- `verk inspect epic --fix` applies safe repairs and reports unresolved findings.
- Planner review finding blocks before dispatch.

Run:

```bash
go test ./internal/e2e -run 'TestTicketQualityGate' -count=1
```

Expected: FAIL before implementation, PASS after prior tasks.

**Step 2: Document behavior**

Document:

- command usage
- config fields
- finding taxonomy
- auto-repair boundaries
- how to resolve a blocked gate
- why the learning loop is intentionally separate

**Step 3: Full focused verification**

Run:

```bash
go test ./internal/state ./internal/engine ./internal/cli ./internal/tui ./internal/adapters/runtime/... ./internal/e2e/... -count=1
just format-check
just lint-check
```

Expected: PASS.

**Step 4: Commit**

```bash
git add internal/e2e/ticket_quality_gate_test.go docs/plans/INDEX.md docs/ticket-quality-gate.md README.md
git commit -m "docs: document ticket quality gate"
```

## Rollout Strategy

1. Land deterministic lint and `verk inspect` first.
2. Gate `verk run epic` with deterministic checks only.
3. Add planner review behind config.
4. Turn planner review on by default for epics after tests prove non-interactive
   behavior is clean.
5. Keep single-ticket planner review opt-in until latency and cost are measured.

## Acceptance Criteria

- `verk inspect ticket` and `verk inspect epic` report deterministic quality
  findings without invoking workers.
- `verk inspect --fix` applies only safe ticket repairs.
- `verk run epic` blocks before worker dispatch when ticket quality findings at
  or above threshold remain unresolved.
- Planner-role review can add semantic findings for traceability, black-box
  contracts, docs de-scope, and integration gaps.
- Ticket quality artifacts persist under the run directory.
- Public CLI/API tickets require black-box contract scenarios.
- Multi-surface epics require an integration or traceability ticket.
- Learning-loop data mining is left to a separate plan.

## Open Questions

- Should `planner_review` default to true for all epics immediately, or should
  it start opt-in for one release?
- Should auto-fix safe repairs be default-off forever, or become default-on for
  `verk run` after the behavior is stable?
- Should ticket quality findings use `P0` for impossible-to-run tickets, or keep
  all pre-run blockers at `P1` and below?
- Should traceability source references require explicit `plan_refs` frontmatter,
  or should body links be enough for v1?
