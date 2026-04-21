# verk Implementation + Verification Loop Improvements

## Metadata

- Date: 2026-04-19
- Scope: lightweight correctness + completeness tightening for verk's impl→verify→review→repair→closeout slice
- Out of scope: deep semantic review (owned by `vakt`); cross-agent skill delivery (owned by [verk-as-skill-cross-agent](2026-04-19-verk-as-skill-cross-agent.md))
- Status: planned

## Summary

Seven changes to the verk impl/verify loop. The theme is **cheap, mechanical, compounding** — verk stays lightweight; heavy code review remains `vakt`'s job (not wired yet).

| # | Plan | Layer | Blocks | Effort |
|---|------|-------|--------|--------|
| P1 | Intent echo before dispatch | worker plumbing | — | S |
| P2 | Standards injection into implementer | worker plumbing | — | XS |
| P3 | Anti-rationalization in worker prompts | worker plumbing | P1 | XS |
| P4 | Finding-level resolution evidence | state schema | P7 | M |
| P5 | Wave-level reviewer | review scope | P4 | M |
| P6 | Epic-level reviewer (plan + acceptance) | review scope | P4, P5 | M |
| P7 | Compiled-constraint promotion | learning layer | P4 | L |

Three cross-cutting tracks govern how all seven land:

- **Token Strategy** — prevention, efficiency, safe skips, model selection, reporting. No mid-loop aborts.
- **Rollout** — each P behind a policy flag; reviewers launch in shadow mode before enforcement.
- **Observability** — per-P and per-run metrics that make success measurable.

Recommended order: **P2 → P3 → P1 → P4 → P5 → P6 → P7**. Weeks 1 = discipline; week 2 = scoped reviewers; week 3 = constraint promotion. Revised MVP cut at the end.

## Context and Boundaries

### What verk does (per `docs/plans/done/initial_v1.md`)

Deterministic ticket execution. Workers are ephemeral, the engine is the only orchestrator. Reviews are one fresh-context independent reviewer per ticket. Closeout is gate-typed with derived pass/fail. Waves serialize by declared `owned_paths` conflict.

### What `vakt` does

Deep multi-pass AI code review (explorer/verifier/judge pipeline, minutes to an hour). Produces findings against a base commit, optionally verified against a spec.

### Where this plan draws the line

- verk's reviewer adapters (`internal/adapters/runtime/{claude,codex}`) already normalize findings to the canonical `P0–P4` severity + `open/resolved/waived` disposition model. Use them.
- Cross-file reasoning, contradiction detection, adversarial review happen **in-process** at wave and epic scope, one lightweight reviewer call each, using the same adapters. No external service. No explorer/judge pipeline.
- No bridge to `vakt` in this plan. A future plan may add one.
- Everything proposed here is either a state-schema change, a new `Gate`, a new adapter prompt mode, or a new review scope. No new runtime types.

### Design invariants preserved

Every proposed change respects v1's existing contracts:

- Derived pass/fail is always recomputed by the engine. Self-reported `passed` is never trusted. *(New review scopes follow the same rule.)*
- Every new artifact carries `schema_version`, `run_id`, object identifier, `created_at`, `updated_at`.
- Transition commit order is unchanged: sidecar → claim → wave → `run.json`.
- Worker isolation via per-ticket git worktrees is unchanged. Wave and epic reviewers run on the **union diff against the wave/epic base commit**, not against worker worktrees.

### Engine-Verified Baseline (Confirmed 2026-04-19)

Three properties verified by reading the engine directly, correcting earlier plan drafts that over- or under-claimed:

1. **`criterion_id` is already engine-generated.** `internal/engine/intake.go:99-113` produces deterministic IDs of form `criterion-<NN>-<sha1-8>` at intake time, one per non-empty `acceptance_criteria` entry. Empty strings are silently dropped. **P4 does not add this** — P4's actual contribution is `ResolutionEvidence` for findings marked `resolved`. Any earlier framing of P4 as "stable criterion_id" was imprecise; the closure-contract rationale (cross-run evidence binds by id, not text) still motivates P4, but the id itself was in place before this plan.
2. **Ticket-frontmatter `model` is a no-op.** `internal/engine/intake.go:50-52` explicitly excludes the ticket's `model` field from the plan snapshot — only `runtime` is copied through. Persona selection (A1), cross-runtime reviewer selection (A2), and model-tier selection (Token Strategy) all route on `runtime` + policy, never on ticket-level `model`. Any ticket carrying a `model` frontmatter field is cargo-culted; the engine ignores it. A1 and A2 reviewers must confirm no code path they touch reads `ticket.Model`.
3. **Zero-criteria tickets close trivially.** The `criteria_evidence` closeout gate loops over `plan.Criteria` and requires evidence for each. On a zero-criteria ticket the loop is vacuous and the gate passes with no evidence required. This is a latent hole in the closure contract. **A4 fixes it.**

---

## Token Strategy

Hard per-call caps with mid-flight aborts create worse failure modes than the overspending they try to prevent (half-done tickets, skipped reviews, orphaned findings). Replace that model with five layers that act **before** or **beside** token spend, never in the middle of it.

### Layer 1 — Prevention at intake and scheduling

Catch oversized work before any tokens are spent.

- Intake rejects tickets whose `owned_paths` file-count or aggregate source size exceeds a configurable threshold. Reason: `oversized_scope`. Operator resolves by splitting.
- Wave scheduling rejects waves whose union diff (estimated from base commit + touched paths) would exceed reviewer context capacity for the configured model tier. Reason: `oversized_wave`. Operator resolves by reducing wave concurrency or splitting tickets.
- Epic intake rejects epics whose child-ticket count exceeds a configurable max. Reason: `oversized_epic`.
- Thresholds live under `policy.scheduling.oversized_scope`:
  ```yaml
  oversized_scope:
    max_ticket_paths: 20
    max_ticket_source_bytes: 200000
    max_wave_diff_bytes_estimate: 400000
    max_epic_children: 30
  ```
- All rejections are **pre-dispatch**. No LLM calls made. Clean operator loop.

### Layer 2 — Per-call efficiency

Every call that verk makes should use tokens meaningfully.

- **Compressed summaries.** Wave and epic reviewers receive per-ticket summaries (criteria, plan, status) not raw ticket markdown.
- **Diff summaries.** Reviewer inputs are "changed-files list + top-N hunks by touched-tickets count" not full diff. Full diff available by reference (`verk diff <ticket-id>`) for adapters that can fetch on demand.
- **Targeted prompts.** Each review scope has a tight instruction set listing *only* what it should find. Wave reviewer is told four categories and nothing else (cross-ticket contradictions, integration surface drift, incomplete fanout, orphaned references). Epic reviewer is told its own four.
- **Prompt caching.** See [Prompt Caching](#prompt-caching) under Cross-Cutting. Largest cost lever.

### Layer 3 — Safe-seam skips

Skip scopes that add no signal. Decisions made at scheduling time, never mid-loop.

- Single-ticket wave → skip wave review (P5).
- Single-ticket epic → skip all epic reviews (P6).
- Epic with fewer than `policy.epic_review.plan_min_tickets` children → skip epic-plan review (P6 plan-time only).
- Single-ticket wave with `len(owned_paths) ≤ 2` → skip wave review even if wave has size (per SR-12 revision).
- Repair-only waves (wave composed entirely of reopened tickets with no scope changes) → reuse prior wave review unless `repolicy.wave_review.rerun_on_repair_waves = true`.

These are tokens **not** spent, not tokens cut short.

### Layer 4 — Adaptive model selection

Right-size the model for the task. Encoded in config:

```yaml
policy.runtime.model_tier_by_task:
  intent: cheap             # P1: short structured JSON, low risk
  implement: standard       # worker must reason about code
  review_ticket: standard   # single-ticket semantic review
  review_wave: cheap        # narrowly scoped, 4-category prompt
  review_epic: standard     # broader surface, worth the cost
  repair: standard          # must reason about findings + diff
  constraint_check: none    # P7 constraints are deterministic, no LLM
```

Model tiers (`cheap`, `standard`, `strong`) map to concrete model IDs per-adapter in `.verk/config.yaml`. Adapters already support runtime preference; this extends it per task-type.

A wave review whose diff exceeds a configurable threshold upgrades from `cheap` to `standard` automatically (`policy.review_wave.upgrade_at_diff_bytes`). Recorded in artifact as `model_upgraded: true` with reason.

### Layer 5 — Report, never auto-abort

Once a call is dispatched, it runs to completion or fails with its own retry logic.

- Every artifact records `tokens_in`, `tokens_out`, `model`, `cache_hit_ratio`.
- `verk status --json` emits per-run and per-ticket cumulative spend.
- Soft-warn thresholds (`policy.cost.soft_warn_tokens_per_ticket`, default 100k) surface in `verk status` without blocking.
- Hard run cancellation is explicit: `verk cancel <run-id>`. No implicit mid-run termination.

### Truncation is reported, not fatal

Replaces the earlier SR-3 hard-fail rule.

- Reviewer inputs that exceed the model's context budget truncate via priority policy: (1) files changed by ≥2 tickets, (2) public API surface, (3) doc files, (4) tests, (5) everything else.
- Artifact records `full_diff_bytes`, `included_diff_bytes`, `truncation_severity ∈ {none, moderate, significant}`, `omitted_files[]`.
- `significant` = files changed by multiple tickets were dropped. Surfaces as a warning in `verk status` and an audit event in `run.json`. Review still runs.
- Operator decides whether to accept a review flagged `significant`. Default policy `policy.review_wave.accept_truncation_severity ∈ {"moderate", "significant"}`; default `moderate` means `significant` truncation blocks wave acceptance with reason `review_input_truncated_significantly`.
- No mid-review abort.

### Principle

> Prevent oversized inputs. Compress what we send. Skip what adds no signal. Match model to task. Report spend; never abort mid-flight.

---

## Observability

The plan introduces seven changes; none of them matter without a way to tell whether the loop got healthier. Each P emits a defined set of metrics, and the engine rolls them up to a `verk metrics` surface.

### Per-P metrics

| P | Metrics | Source |
|---|---------|--------|
| P1 Intent echo | `intent.attempts_per_ticket` (distribution), `intent.accept_rate`, `intent.block_reason_counts{missing_criteria, superset_paths, empty_test_plan}` | `intent-<attempt>.json` |
| P2 Standards | `prompt.implementer_bytes` (pre/post distribution, one-time measurement during rollout) | Adapter logs |
| P3 Anti-rationalization | `implementer.completion_code_distribution{done, done_with_concerns, needs_context, blocked}` — compared before/after rollout | `implementation.json` |
| P4 Resolution evidence | `closeout.gate_review_failure_rate`, `closeout.scope_violation_from_resolution_evidence` | `closeout.json` |
| P5 Wave reviewer | `wave_review.findings_per_wave`, `wave_review.target_assignment.{exact, prefix, longest, seam}`, `wave_review.retry_rate`, `wave_review.truncation_severity_distribution` | `wave-review.json` |
| P6 Epic reviewer | Same shape as P5 at epic scope; plus `epic_plan_review.operator_block_rate` | `epic-review-*.json` |
| P7 Constraints | `constraints.candidates_count`, `constraints.active_count`, `constraints.disabled_count`, `constraints.catch_rate` (constraint-fails-verify vs equivalent-post-review finding), `constraints.time_to_first_trigger` (per constraint) | `.verk/constraints/index.json` + verification artifacts |

### Global metrics (system health)

Rolled up across all runs on this repo:

- `tokens_per_ticket` — mean, p95. Trends up = prompts bloating; trends down = caching/efficiency winning.
- `llm_calls_per_ticket` — mean, p95. Target: ≤ 4 for normal tickets (intent + implement + ticket-review + optional repair).
- `repair_cycles_per_ticket` — distribution. Healthy loop = most tickets finish with 0 repair cycles.
- `time_dispatch_to_closeout` — distribution. The ultimate cycle-time metric.
- `review_finding_rate_by_scope{ticket, wave, epic_plan, epic_acceptance}` — trending down for ticket-scope + steady-or-down at wave/epic = constraints compounding working.

### Surface

- `verk status <run-id>` — shows per-run metrics including cost.
- `verk metrics` (new, low-priority) — aggregated across runs in the repo. Reads `.verk/runs/*/run.json` + `.verk/constraints/index.json`. JSON output option.
- `verk doctor` — emits warnings when metrics cross configured thresholds (e.g., repair rate > 40%, disabled-constraint rate > 15%).

### Rollup storage

Per-run metrics live in `run.json.metrics_snapshot`. Cross-run aggregation is computed on demand; no separate store. Prevents metric schema drift and keeps `.verk/` self-describing.

---

## Rollout and Feature Flags

Seven changes landing at once will confound debugging. Each P lands behind its own policy flag, defaulted **off** for new installs. Reviewers (P5, P6) have a three-mode flag with a **shadow phase** as default.

### Flag model

- Mechanical/schema changes (P1, P2, P3, P4, P7): boolean `policy.<feature>.enabled`. Default `false`. Operator flips to `true` when ready.
- Review scopes (P5, P6): tri-mode flag:
  ```yaml
  policy.wave_review.mode: disabled | shadow | enforce
  policy.epic_review.plan_mode: disabled | shadow | enforce
  policy.epic_review.acceptance_mode: disabled | shadow | enforce
  ```
  - **`disabled`**: no reviewer call, no artifact.
  - **`shadow`**: reviewer runs, artifact written, findings recorded, target-ticket assignment computed — **but findings do not reopen tickets or fail acceptance**. Used to collect data before enforcing.
  - **`enforce`**: full behavior per P5/P6 spec.
- Defaults for new installs:
  - P1/P2/P3/P4/P7: `enabled: false`.
  - P5/P6: `shadow`.

### Recommended rollout sequence

Per-repo, once implementations merge:

| Stage | Flip | Soak | Gate |
|-------|------|------|------|
| 0 | Measure baseline (current main, before any P enables). Capture global metrics for 5 epic runs. | — | — |
| 1 | P2 `enabled: true`, P3 `enabled: true` (prompt-only) | 5 epic runs | Implementer prompt size within budget (G4). |
| 2 | P4 `enabled: true` + migration | 5 epic runs | No legacy-flagged findings from new runs. |
| 3 | P1 `enabled: true` | 5 epic runs | Intent block rate < 10% after 3 runs. |
| 4 | P5 `shadow → enforce` | 10 epic runs | Shadow-phase findings triaged as real by operator ≥ 60% of the time. |
| 5 | P6 acceptance `shadow → enforce` | 10 epic runs | Same signal test. |
| 6 | P6 plan `shadow → enforce` | 10 epic runs | Plan-time false-positive rate acceptable. |
| 7 | P7 `enabled: true` with seeded constraints (see SR-S2) | 20 epic runs | Catch rate > 0, disabled rate < 15%. |

Each gate that fails rolls back the flag and records findings in `docs/reviews/`. Next stage does not start until the gate passes.

### Kill switch

Any P can be flipped to `disabled`/`false` with no state migration required. All P artifacts are additive; turning a P off stops new writes, old artifacts remain and are ignored by gates.

---

## P1 — Intent Echo Before Dispatch

### Goal

Before the implementer writes code, it returns a structured echo (understood criteria, planned file touches, test plan). verk rejects and re-dispatches if the echo fails mechanical alignment checks.

### Scope

Pure correctness/completeness pre-flight. No semantic judgment — verk compares `criterion_id` sets, path subsets, and presence of a test plan.

### State changes

New artifact `tickets/<ticket-id>/intent-<attempt>.json`:

```
{
  "schema_version": "1",
  "run_id": "...",
  "ticket_id": "...",
  "attempt": 1,
  "runtime": "claude|codex",
  "model": "haiku-tier-id",
  "lease_id": "...",
  "echoed_criterion_ids": ["..."],
  "planned_owned_paths": ["..."],
  "test_plan_summary": "...",
  "status": "passed|failed",
  "failure_reasons": ["missing_criteria", "superset_paths", ...],
  "tokens_in": 0,
  "tokens_out": 0,
  "cache_hit_ratio": 0.0,
  "created_at": "...",
  "updated_at": "..."
}
```

Intent sits *before* `implementation.json` in the ticket artifact order. Implementation retry count increments only on verification failure, not on intent failure — intent has its own retry budget.

### Post-implementation alignment check (per SR-1)

After implementation completes, verk compares `implementation.changed_files` against `intent.planned_owned_paths`:

- Files changed outside planned set → scope violation (same rule as declared `owned_paths` violation; already a failing check at verify).
- Files planned but not changed → warn only (worker may have correctly decided a file didn't need editing). Recorded in `implementation.json.planned_not_changed[]`.

Prevents the "echo correct, implement different" loophole.

### Adapter contract

`ImplementerAdapter` gains `CollectIntent(ctx, req) (IntentResult, error)`. Both `claude` and `codex` adapters implement it as a short prompt mode that returns JSON. The `fake` adapter implements a deterministic version keyed off `RunTicketRequest` for tests.

### Gate rules

Enforced by engine, not adapter:

1. Every `criterion_id` from `plan.acceptance_criteria` must appear in `echoed_criterion_ids`. Missing → reject.
2. `planned_owned_paths ⊆ plan.owned_paths`. Superset → reject. (Empty is allowed: the worker may plan no file touches if the ticket is docs/no-op.)
3. `test_plan_summary` non-empty → proceed. Empty → reject.

### Retry + block policy

- Default intent retries: 2 (configurable under `policy.intent.max_attempts`).
- Third failed intent → ticket transitions to `blocked` with reason `intent_non_convergent`.
- Intent failures are **not** counted against `max_implementation_attempts`. Separate budget.

### Model selection

Intent echo uses `model_tier_by_task.intent = cheap`. Structured short output on haiku-tier is production-ready today.

### Files

- `internal/adapters/runtime/types.go` — `IntentRequest`, `IntentResult`.
- `internal/adapters/runtime/{claude,codex,fake}/adapter.go` — `CollectIntent`.
- `internal/engine/intent.go` (new) — gate evaluation + retry loop.
- `internal/engine/ticket_run.go` — call `runIntent` before `runImplement`; call `alignImplementationWithIntent` after `runImplement`.
- `internal/state/types.go` — `IntentArtifact`.
- `internal/state/store.go` — atomic writer.
- `internal/policy/config.go` — `policy.intent.{enabled, max_attempts}`, defaults `{false, 2}`.

### Tests

- Unit: each gate rule rejects deterministically (missing id, superset path, empty test plan).
- Unit: third-retry block with reason `intent_non_convergent`; implementation attempt counter untouched.
- Unit: intent disabled via config skips phase entirely, no artifact written.
- Unit: `fake` adapter intent is deterministic given the same request.
- Unit: post-implementation alignment rejects changed files outside planned paths (SR-1 coverage).
- Unit: planned-but-unchanged files warn, don't block.
- E2E: ticket with intentionally missing criterion → blocked after retries; artifacts present.
- Resume: crash mid-intent resumes from latest `intent-<attempt>.json` without redoing passed attempts.

### Risks and rollback

- One extra LLM call per attempt. Mitigation: `policy.intent.enabled` off-switch; short prompt on cheap model.
- False rejections on valid plans. Mitigation: rejection reason is typed and surfaced in `run.json` audit; operator can `verk reopen … --to implement --skip-intent` for a single attempt.

### Estimated effort

2–3 days.

---

## P2 — Standards Injection Into Implementer Context

### Goal

Ensure `internal/adapters/runtime/standards/{universal,go,cross_platform}.md` reach the **implementer** in every attempt, not only the reviewer.

### Current state investigation (pre-implementation)

Step 1 of P2 is explicit: grep `standards` references in `internal/adapters/runtime/{claude,codex}/adapter.go` and write findings to `docs/reviews/2026-04-XX-standards-injection-audit.md` before writing code. If standards already reach the implementer, P2 collapses to bundle-selection logic only. (Per SR-10.)

### Design

Add `StandardsBundle` to runtime request contract:

```go
type StandardsFile struct {
    Name      string
    Body      string
    AppliesTo []string // "implementer" | "reviewer"
}

type StandardsBundle struct {
    Files []StandardsFile
}
```

Bundle assembly helper (`internal/adapters/runtime/standards/bundle.go`):

- `universal.md` — always, applies to both.
- `go.md` — included when `plan.owned_paths` matches `*.go`. Applies to both.
- `cross_platform.md` — included when ticket touches filenames with platform suffixes or files under `runlock*`. Applies to both.
- `anti_rationalization.md` (from P3) — always, implementer only.

Implementer prompts prepend only files with `"implementer" ∈ AppliesTo`. Reviewer prompts prepend only `"reviewer" ∈ AppliesTo`.

### Worker Prompt Budget Guard (G4)

Before enabling P2 by default:

1. Measure baseline implementer prompt size for a representative sample of tickets (docs-only, go-only, mixed, refactor).
2. Measure post-P2+P3 prompt size.
3. Commit a budget check: standards + anti-rationalization + ticket plan + diff summary must stay under 20% of the worker's context window for the selected model tier. If exceeded:
   - Trim standards bodies (identify rarely-relevant sections).
   - Split into always-injected core + on-demand detail referenced by section heading.
   - Re-measure.
4. Record the measurement in `docs/reviews/2026-04-XX-standards-injection-audit.md` alongside the investigation results.
5. `policy.standards.max_bundle_bytes` sets the hard limit; bundle assembly that would exceed it fails fast with a terminal engine error.

### Files

- `internal/adapters/runtime/standards/bundle.go` (new).
- `internal/adapters/runtime/standards/bundle_test.go` (new) — selection logic.
- `internal/adapters/runtime/{claude,codex}/adapter.go` — wire bundle into `Implement` and `Review` prompt templates.
- `docs/reviews/2026-04-XX-standards-injection-audit.md` (new, one-time artifact).

### Tests

- Unit: bundle selection for `.go` ticket includes universal + go.
- Unit: docs-only ticket (no `.go` paths) includes only universal.
- Unit: reviewer bundle excludes `anti_rationalization.md` even when implementer bundle includes it.
- Unit: over-budget bundle returns a terminal error; no partial prompt sent.
- Snapshot: rendered implementer prompt for one representative ticket matches expected skeleton.

### Risks

- Prompt size growth. Mitigation: budget guard above; `AppliesTo` filtering keeps reviewer prompts tight.

### Estimated effort

4 hours + audit.

---

## P3 — Anti-Rationalization In Worker Prompts

### Goal

Preempt the worker's temptation to report `done_with_concerns` when the honest completion code is `blocked` or `needs_context`.

### Design

New file `internal/adapters/runtime/standards/anti_rationalization.md`. Treated as a fourth always-injected bundle entry by P2's bundle mechanism, `AppliesTo = ["implementer"]`.

Content is a rationalization table. Draft:

| Thought | Correct action |
|---------|----------------|
| "Close enough, I'll return `done_with_concerns`" | If the concern blocks an acceptance criterion, return `blocked` citing the `criterion_id`. |
| "I can't find the related code, I'll implement from scratch" | Return `needs_context` naming the specific symbol, file, or test that's missing. |
| "Tests would fail but the impl is right, I'll skip writing tests" | Tests are the verification evidence. No tests = no closeout. If a test is infeasible, return `blocked` with reason. |
| "The `owned_paths` list is too narrow, I'll edit outside it" | Scope violations fail the wave. Return `needs_context` citing the needed path. |
| "The acceptance criterion is ambiguous, I'll interpret it" | Return `needs_context` with the `criterion_id` and the specific ambiguity. |
| "My repair resolved the finding, I'll mark it `resolved` and move on" | Populate `resolution_evidence` with the diff range and the test that covers it. Without both, closeout will reject. (See P4.) |
| "Verification failed but it's a flaky test, I'll report `done`" | Flaky tests are a real finding. Return `done_with_concerns` naming the flakiness *and* still attempt a stable fix. |

### Wire-in

Ships through P2's bundle mechanism; zero extra plumbing if P2 lands first.

### Files

- `internal/adapters/runtime/standards/anti_rationalization.md` (new).
- `internal/adapters/runtime/standards/bundle.go` — add file to always-injected set for implementer.

### Tests

- Prompt-rendering test: every implementer prompt contains the rationalization heading.
- No runtime-behavior test. This is guidance, not an enforced gate. Efficacy measurement tracked as a follow-up under G8.

### Risks

Minimal. Purely additive prompt content. Budget impact covered under P2's prompt-budget guard.

### Estimated effort

2 hours (including table refinement).

---

## P4 — Finding-Level Resolution Evidence

### Goal

When a repair cycle marks a review finding `resolved`, the disposition change must carry evidence (diff range + test reference). Closes the "worker acknowledges finding, ships a different fix, reviewer closes without noticing" failure mode.

### State changes

Extend `state.ReviewFinding`:

```go
type ReviewFinding struct {
    // existing fields ...
    ResolutionEvidence *ResolutionEvidence `json:"resolution_evidence,omitempty"`
}

type ResolutionEvidence struct {
    DiffRanges     []DiffRange         `json:"diff_ranges"`     // required, non-empty
    TestReferences []TestReference     `json:"test_references"` // required, non-empty, normalized
    RepairCycleID  string              `json:"repair_cycle_id"` // links to cycles/repair-<n>.json
    ResolvedAt     time.Time           `json:"resolved_at"`
    Legacy         bool                `json:"legacy,omitempty"` // set only by migration
}

type DiffRange struct {
    File      string `json:"file"`
    StartLine int    `json:"start_line"`
    EndLine   int    `json:"end_line"`
}

// TestReference is normalized per SR-2. Free-form strings are rejected.
type TestReference struct {
    Kind    string `json:"kind"` // "test_function" | "file_line"
    Package string `json:"package,omitempty"` // for test_function
    Name    string `json:"name,omitempty"`    // for test_function
    File    string `json:"file,omitempty"`    // for file_line
    Line    int    `json:"line,omitempty"`    // for file_line
}
```

Bump `review-findings.json` `schema_version` to `"2"`.

### Gate rules

Extend `internal/engine/closeout.go` `validateReviewFinding`:

1. `disposition = "resolved"` → require non-nil `ResolutionEvidence`.
2. `ResolutionEvidence.DiffRanges` must be non-empty.
3. `ResolutionEvidence.TestReferences` must be non-empty and every entry must parse into a valid `TestReference`. Free-form strings are rejected. (Per SR-2.)
4. Every `DiffRange.File` must lie within the ticket's `owned_paths`. A diff outside scope is a scope violation (separate error, not a closeout pass).
5. `RepairCycleID` must reference an existing `cycles/repair-<n>.json` file.
6. Engine must resolve each `TestReference` to a concrete test or assertion:
   - `test_function` → `{Package}.{Name}` must exist in the worktree.
   - `file_line` → `{File}:{Line}` must exist and fall within the diff range set of some test file.
   - Resolution failure is a blocking gate error with `failed_gate: "review"` and reason `test_reference_unresolved`.
7. `Legacy = true` grandfathered (migration path only).

### Migration

One-time migration on first engine startup after upgrade:

- Walk all `.verk/runs/*/tickets/*/review-findings.json` with `schema_version < 2`.
- For every finding with `disposition = "resolved"` lacking `resolution_evidence`, synthesize `{ Legacy: true, ResolvedAt: artifact.updated_at }` with empty ranges/tests.
- Bump file `schema_version` to `"2"`.
- Migration runs atomically per file (write-temp + rename). Failure aborts and rolls back that file only.
- Migration acquires an exclusive lock on `.verk/migrations.lock` (separate from per-run locks) per SR-8. Migration refuses to run if any `run.lock` is held. First migration attempt blocks new runs until complete.

Future runs never emit `Legacy = true`. Any finding with `Legacy = true` from a **new** run is a blocking engine bug.

### Repair worker contract update

Repair workers receive the existing finding set and are instructed (via prompt) to populate `resolution_evidence` for every finding they mark `resolved`. Reviewer prompts check that evidence presence matches disposition.

### Files

- `internal/state/types.go` — `ResolutionEvidence`, `DiffRange`, `TestReference`.
- `internal/state/store.go` — schema migration entrypoint; `.verk/migrations.lock` handling.
- `internal/engine/closeout.go` — extend `validateReviewFinding`; add `TestReference` resolver.
- `internal/adapters/runtime/types.go` — reviewer output schema.
- `internal/adapters/runtime/{claude,codex}/adapter.go` — prompt update (repair + review).
- `scripts/migrate-review-findings-v2.go` or one-shot migration in `cmd/verk/main.go` startup path.

### Tests

- Unit: `resolved` + empty evidence fails closeout with gate `review`.
- Unit: `resolved` + free-form `TestReference` string fails with `test_reference_malformed`.
- Unit: `resolved` + unresolvable `TestReference` fails with `test_reference_unresolved`.
- Unit: `resolved` + diff outside `owned_paths` fails with scope error (not closeout gate).
- Unit: `resolved` + valid evidence passes.
- Unit: `Legacy = true` grandfathered.
- Unit: migration idempotent (re-running doesn't corrupt v2 files).
- Unit: migration refuses to run under held run.lock.
- Unit: `RepairCycleID` pointing at missing file fails.
- E2E: ticket through 1 repair cycle with full evidence closes; strip evidence → blocks.
- Migration: seeded v1 file with 3 resolved findings migrates cleanly to v2 with `Legacy = true` on all.

### Risks

- Existing runs fail closeout after upgrade if not migrated. Mitigation: migration runs before engine accepts any new run.
- Reviewer output normalization must preserve evidence exactly. Mitigation: adapter tests with golden inputs.

### Estimated effort

2 days.

---

## P5 — Wave-Level Reviewer

### Goal

After all tickets in a wave complete verification and ticket-level review but before wave acceptance, run **one fresh-context reviewer** over the wave's union diff to catch:

- Cross-ticket contradictions (one ticket says X, another says not-X).
- Integration surface drift (shared types/APIs modified inconsistently).
- Incomplete fanout (ticket A added X but ticket B that depends on X wasn't updated).
- Orphaned references (renamed symbol left dangling).

### Scope discipline

- **One reviewer call** per wave. Same adapter (`runtime.ReviewerAdapter`), new prompt mode (`review_scope = "wave"`).
- Prompt is **targeted**: instructed to find only the four categories above, not repeat per-ticket review.
- Reviewer reads the union diff vs `wave_base_commit`, plus per-ticket plan summaries (criteria + owned_paths) and per-ticket review statuses.
- **No explorer/judge pipeline.** That's `vakt`'s job.
- Uses `model_tier_by_task.review_wave = cheap`; auto-upgrades to `standard` when diff exceeds `policy.review_wave.upgrade_at_diff_bytes`.

### State changes

New artifact `waves/wave-<n>/wave-review.json`:

```
{
  "schema_version": "1",
  "run_id": "...",
  "wave_id": "...",
  "ordinal": 0,
  "reviewer_runtime": "claude|codex",
  "model": "...",
  "model_upgraded": false,
  "review_scope": "wave",
  "base_commit": "...",
  "scope_union": ["..."],
  "ticket_summaries": [
    { "ticket_id": "...", "criterion_ids": [...], "review_passed": true, ... }
  ],
  "findings": [
    {
      "target_ticket_ids": ["ver-abc1"],    // multi-target per SR-4
      "target_rationale": "exact_match",     // "exact_match" | "prefix_match" | "longest_prefix" | "seam"
      // ...all standard ReviewFinding fields...
    }
  ],
  "blocking_findings": ["..."],
  "passed": true,
  "effective_review_threshold": "P2",
  "mode": "shadow|enforce",
  "full_diff_bytes": 0,
  "included_diff_bytes": 0,
  "truncation_severity": "none|moderate|significant",
  "omitted_files": [],
  "tokens_in": 0,
  "tokens_out": 0,
  "cache_hit_ratio": 0.0,
  "started_at": "...",
  "finished_at": "..."
}
```

`passed` is **derived**, same rule as `review-findings.json`.

### Target-ticket assignment rules (per SR-4, multi-target)

Engine recomputes `target_ticket_ids` for each finding to prevent reviewer errors:

1. Exact match of `file` in one ticket's `owned_paths` → that ticket.
2. `file` under directory prefix in one ticket's `owned_paths` → that ticket.
3. Multiple tickets match → the ticket with the most specific (longest-prefix) match **plus any other ticket whose `owned_paths` overlap the finding's file** go in `target_ticket_ids`.
4. Findings that touch the seam between tickets (e.g., contradictions across two tickets' `file` fields) must have `target_ticket_ids` = all affected tickets.
5. No ticket matches → `target_ticket_ids = ["__wave_seam__"]`. These findings escalate to **operator block**, not repair. Recorded in `run.json` audit events.

Reviewer-supplied assignments are retained for debugging but never trusted for routing.

Every finding with concrete `target_ticket_ids` reopens **all** named tickets to repair with a `shared_with` list in each repair context so repair workers know they share responsibility.

### New phase in the epic outer loop

Insert between existing phases:

```
... wave executes ... (unchanged)
  → all tickets reach `closeout` or `blocked`
  → NEW: wave_review                ← P5
  → NEW: wave_acceptance_gate       ← already exists, now depends on wave_review
  → wave `accepted` | `failed` | `failed_reopened`
```

Skip conditions (evaluated before dispatch):

- Wave contains 1 ticket AND `len(owned_paths) ≤ 2` → skip (per SR-12 revision).
- Wave is a repair-only rerun and `rerun_on_repair_waves = false` → reuse prior wave review.
- `policy.wave_review.mode = disabled` → skip.

### Gate rules

Any wave-review finding with `disposition = "open"` AND severity ≥ `effective_review_threshold` (default P2) AND `policy.wave_review.mode = enforce`:

- `target_ticket_ids` resolves to concrete tickets → all of them reopen to `repair` with `input_review_artifact` pointing at `wave-review.json` and `trigger_finding_ids` set. Wave status becomes `failed_reopened`.
- `target_ticket_ids = ["__wave_seam__"]` → wave status becomes `failed`, epic status becomes `blocked` with reason `wave_seam_review_unassigned`. Requires operator intervention.

In `shadow` mode, findings and assignments are written but no reopens trigger. Used during rollout (see [Rollout](#rollout-and-feature-flags)).

### Repair cycle extensions

`cycles/repair-<n>.json` gains:

- `review_scope: "ticket" | "wave" | "epic_acceptance"` — source of the triggering findings.
- `shared_with: [ticket_ids]` — other tickets reopened for the same review artifact.

Repair workers' prompts reference `review_scope` so they frame their task correctly.

### Retry policy

- Wave review is retried at most once if the reviewer adapter returns `retryable`.
- `terminal` reviewer failure at wave level → wave fails with `wave_review_runtime_failure`, operator must `verk reopen … --to review --wave <wave-id>`.
- Ticket-repair retries from wave-review findings count against the ticket's existing `max_repair_cycles` budget.

### Truncation

Follows [Token Strategy § Truncation](#truncation-is-reported-not-fatal). `significant` truncation surfaces a warning; default `accept_truncation_severity = moderate` means wave acceptance blocks if `significant`.

### Files

- `internal/adapters/runtime/types.go` — `ReviewRequest.Scope = "ticket" | "wave" | "epic_plan" | "epic_acceptance"`.
- `internal/adapters/runtime/{claude,codex,fake}/adapter.go` — wave-scoped reviewer prompt.
- `internal/engine/wave_review.go` (new) — orchestration + target-ticket assignment.
- `internal/engine/epic_run.go` — insert `wave_review` step before `wave_acceptance`.
- `internal/engine/wave_verify.go` — unchanged.
- `internal/state/types.go` — `WaveReviewArtifact`, updated `RepairCycleArtifact`.
- `internal/policy/config.go` — `policy.wave_review.{mode, threshold, retry_on_retryable, skip_single_ticket_waves, accept_truncation_severity, upgrade_at_diff_bytes, rerun_on_repair_waves}`.

### Tests

- Unit: single-ticket, narrow-scope waves skip wave_review (SR-12).
- Unit: target-ticket assignment rules (exact match, prefix match, longest prefix, multi-target, no match).
- Unit: `__wave_seam__` finding blocks epic with correct reason.
- Unit: derived `passed` recomputed from findings, contradicting reviewer `passed` blocks with artifact error.
- Unit: P2 open finding fails wave in `enforce` mode; same finding in `shadow` mode is recorded but does not reopen.
- Unit: wave_review finding counts against same `max_repair_cycles` budget as ticket review.
- Unit: multi-target finding reopens all named tickets with correct `shared_with`.
- Unit: `significant` truncation with default policy fails acceptance; `accept_truncation_severity = significant` allows it with warning.
- Unit: model upgrade triggers above `upgrade_at_diff_bytes`.
- E2E: 2-ticket wave with contrived contradiction → wave review catches, Ticket B reopens to repair, next wave passes.
- E2E: 2-ticket wave with seam gap → `__wave_seam__` finding, epic blocks, operator unblocks by adding new ticket.
- Resume: crash mid-wave-review resumes cleanly; in-progress reviewer output is discarded.

### Risks

- Reviewer hallucinates `target_ticket_ids` assignments. Mitigation: engine recomputes from `file` field + `owned_paths`; reviewer's assignment is advisory only.
- Findings with no `file` field. Mitigation: require `file` on every finding at adapter normalization; findings without `file` are a terminal adapter error.
- Cost: one more LLM call per wave. Mitigation: skip single-ticket-narrow-scope waves; `cheap` model tier default; prompt caching for system prompt + standards.

### Estimated effort

3 days.

---

## P6 — Epic-Level Reviewer (Plan + Acceptance)

### Goal

Run a fresh-context reviewer at epic scope at two points:

1. **Plan time**: before the first wave dispatches. Reviews the epic plan (root ticket + child tickets) adversarially. Catches structural issues that cheap mechanical validators can't (e.g., "this ticket set won't actually deliver the epic's stated outcome"). Findings update tickets or block the epic.
2. **Acceptance time**: after the final wave completes. Reviews the epic's total delta vs epic base commit. Catches end-to-end consistency gaps: docs/changelog/CLI output/error messages, stale references, missing tests at integration seams.

Both uses share the same adapter and a similar artifact shape. The plan-time pass cannot be purely mechanical because the questions it answers require semantic judgment. It stays lightweight: **one reviewer call per invocation**, targeted prompt.

### Declared-category forcing function (per SR-5)

Plan-time reviewer must emit one finding per category it considered, with `disposition = "resolved"` if no issue found. Empty findings array at plan scope is a terminal adapter error. This prevents "reviewer silently failed" from looking like "reviewer passed the plan".

Categories (plan-time):
- `epic_outcome_coverage` — does the ticket set satisfy the epic's stated outcome?
- `missing_ticket` — any artifact (doc, config, test) that should have its own ticket?
- `dependency_correctness` — are declared `deps` acyclic and complete?
- `owned_paths_coverage` — does the union of `owned_paths` cover all files the epic must touch?

Categories (acceptance-time):
- `epic_outcome_evidence` — does the delta provably satisfy the epic's outcome?
- `end_to_end_consistency` — docs/CLI/errors coherent?
- `stale_references` — dangling mentions of removed things?
- `integration_test_coverage` — tests exist at integration seams?

### State changes

New artifacts:

- `runs/<run-id>/epic-review-plan.json` — plan-time review.
- `runs/<run-id>/epic-review-acceptance.json` — acceptance-time review.

Schema mirrors `WaveReviewArtifact`, with:

- `review_scope = "epic_plan" | "epic_acceptance"`.
- `category_coverage: map[string]{considered: bool, finding_ids: []}` — per declared-category-forcing contract.
- Plan-time: `ticket_summaries` list every child ticket's plan (criteria + owned_paths + test plan).
- Acceptance-time: `ticket_summaries` include closed review statuses + `criteria_evidence` summary.

### Epic phase insertions

```
epic_run begins
  → NEW: epic_review_plan              ← P6 plan-time
  → (if any P0/P1/P2 plan findings → block, escalate)
  → wave scheduling + execution        ← existing, now optionally includes P5 wave reviews
  → (repeat waves until all tickets closed or blocked)
  → NEW: epic_review_acceptance        ← P6 acceptance-time
  → (if any P0/P1/P2 acceptance findings → reopen affected tickets in a new wave)
  → epic_acceptance_gate                ← existing
```

### Plan-time gate rules

Target assignment per P5, plus:

- `target_ticket_ids = ["__epic_plan__"]` → epic status becomes `blocked` with reason `epic_plan_review_gap`. Operator must edit ticket definitions and run `verk reopen <run-id> --to epic_plan --mark-findings-addressed <finding-ids>` (per SR-13). Engine validates the named findings are marked resolved in `epic-review-plan.json` before unblocking.
- Per-ticket plan findings attach to `plan.pending_plan_findings[]`. Intake on that ticket in the next wave must address them (via acceptance_criteria updates) before progressing.
- Plan-time timeout (> `policy.epic_review.plan_timeout_minutes`, default 5) → epic blocks with reason `epic_plan_review_timeout`; not treated as pass.

Plan findings do **not** reopen tickets that haven't yet been dispatched.

### Acceptance-time gate rules

Same as P5 wave review, applied at epic scope:

- Finding with open disposition + severity ≥ threshold → `target_ticket_ids` reopen to repair, new wave scheduled.
- Seam finding with no owner → `__epic_seam__` → epic blocks for operator.
- All findings resolved → epic proceeds to acceptance gate (existing).

### Cost controls

- Plan-time review is **on by default only for epics with ≥ `policy.epic_review.plan_min_tickets` children** (default 3). Single-ticket epics skip plan review.
- Acceptance-time review is **on by default for all multi-ticket epic runs**; single-ticket epics skip.
- Both have tri-mode flags (`disabled | shadow | enforce`); both default `shadow` for new installs.
- Both disabled per-run via `verk run epic … --skip-epic-review-plan` / `--skip-epic-review-acceptance`.
- Model tier: plan-time uses `standard` (broader surface), acceptance-time uses `standard` (higher stakes).

### Files

- `internal/engine/epic_review.go` (new).
- `internal/engine/epic_run.go` — insert `epic_review_plan` at start and `epic_review_acceptance` before `epic_acceptance_gate`.
- `internal/state/types.go` — `EpicReviewArtifact`, `EpicReviewScope`, `PlanFindingReference`, `CategoryCoverage`.
- `internal/state/types.go` — `PlanArtifact.PendingPlanFindings`.
- `internal/engine/intake.go` — respect `PendingPlanFindings` when preparing `plan.json`.
- `internal/adapters/runtime/{claude,codex,fake}/adapter.go` — epic-scoped reviewer prompt modes.
- `internal/policy/config.go` — `policy.epic_review.{plan_mode, acceptance_mode, plan_min_tickets, plan_timeout_minutes, threshold, accept_truncation_severity}`.

### Tests

- Unit: plan-time review triggers only for ≥ threshold ticket count.
- Unit: `__epic_plan__` finding blocks epic; audit event written.
- Unit: per-ticket plan finding attaches to `plan.pending_plan_findings[]`; intake flags it before implement.
- Unit: acceptance-time finding with known `target_ticket_ids` reopens tickets and schedules new wave.
- Unit: plan-time timeout routes to operator block, not pass.
- Unit: empty findings at plan scope is a terminal adapter error (SR-5 forcing function).
- Unit: category coverage missing a category fails plan review validation.
- Unit: operator reopen with `--mark-findings-addressed` validates the named findings are resolved before unblocking.
- E2E: 3-ticket epic where the plan omits a required doc ticket → plan review catches (`missing_ticket` category), seam finding, operator adds ticket, epic proceeds.
- E2E: epic acceptance catches stale doc reference → reopen → new wave → close.
- Resume: crash after plan review but before first wave → resume reads `epic-review-plan.json`, skips re-run.

### Risks

- Plan-time review could block on false positives. Mitigation: operator override; default `shadow` mode for long initial rollout; tri-mode flag supports per-repo tuning.
- Epic-level acceptance review expensive if the epic is huge. Mitigation: review runs against a **summary diff** (file list + per-file hunks, capped per Token Strategy); over-budget epics fall back to scope-restricted review (docs + changelog + public APIs only).
- Adapter normalization consistency between wave and epic scopes. Mitigation: shared `ReviewRequest` shape from P5.

### Estimated effort

3 days.

---

## P7 — Compiled-Constraint Promotion

### Goal

Repeating findings become cheap mechanical checks run at verk's verify phase. The same class of bug is never "discovered" by the reviewer twice — it's caught before review by a command or regex.

### Why verk, explicitly

Findings are produced by review (any scope: ticket, wave, epic). Promotion turns a pattern into a deterministic check — a shell command or regex, **not** an LLM call. That belongs at verify, not review. This is the unique compounding play for verk's slice.

### State changes

New store `.verk/constraints/index.json`:

```
{
  "schema_version": "1",
  "constraints": [
    {
      "id": "cst-...",
      "created_at": "...",
      "finding_signature": {
        "title_regex": "...",
        "file_glob": "...",
        "severity_bucket": "P0-P1|P2|P3-P4"
      },
      "promoted_from": [
        { "run_id": "...", "ticket_id": "...", "finding_id": "..." }
      ],
      "promoted_from_count": 3,
      "check": {
        "type": "command" | "grep",
        "spec": { /* see below */ },
        "timeout_ms": 30000,
        "stdout_paths": ["..."]
      },
      "active": true,
      "promotable_only": true,
      "activation_threshold": 3,
      "last_triggered_at": "...",
      "last_triggered_ticket_id": "...",
      "disabled_at": null,
      "disabled_reason": null
    }
  ]
}
```

Candidate files live under human-readable paths (per SR-6):

`.verk/constraints/candidates/<severity_bucket>-<first-word-of-title>-<file-glob-hash8>.jsonl`

Each line records one finding occurrence. The internal `signature` hash is the key in `index.json` but never in the filesystem.

### Check types (v1)

- `command`: shell command run from repo root, expected exit code 0 = pass. Fields: `command`, `args`, `cwd_mode: "repo_root" | "worktree"`, `env_passthrough`, `base_used: "wave_base_commit" | "head"` (per SR-7).
- `grep`: regex applied to git diff. Fields: `pattern`, `file_glob`, `must_not_match: bool` (default `true`), `base_used: "wave_base_commit" | "head"` (per SR-7).
- `ast` deferred to v2.

For wave-level tickets, `base_used = "wave_base_commit"` is the default; for single-ticket runs, `head` (repo HEAD before changes). Recorded per-constraint-execution in `verification.json.per_command_results[].constraint_execution`.

### Signature derivation

On every ticket closeout, for each review finding (ticket, wave, or epic scope), compute:

```
signature = sha256(
  normalize(finding.title_prefix_first_6_words) ||
  file_glob(finding.file) ||
  severity_bucket(finding.severity)    // {P0-P1, P2, P3-P4}
)
```

`file_glob` maps `internal/engine/epic_run.go` → `internal/engine/*.go`. Full glob reduction rules go in `internal/engine/constraints/signature.go` with a dedicated test suite.

### Promotability gate (per SR-S3)

Reviewer findings carry a `promotable: boolean` tag:

- `true` for objective rules ("missing error check", "unsafe path construction").
- `false` for judgment calls ("confusing variable name", "could be clearer").
- Default `true`; reviewer prompts instruct when to mark `false`.
- Candidates files only accept `promotable = true` findings. Non-promotable findings still appear in review artifacts for the operator.

### Promotion flow

1. On every closeout (success OR block), walk review findings (ticket + wave + epic scopes for that ticket's run).
2. For each finding with `promotable = true`: append `{run_id, ticket_id, finding_id, occurred_at}` to the human-keyed candidate file.
3. After append, count **distinct `ticket_id`** entries in the candidate file.
4. When distinct count ≥ `activation_threshold` (default 3): emit a promotion-ready marker `candidates/<signature>.ready` and log a notice on `verk status`.
5. **Promotion is never automatic in v1.** Operator runs `verk constraints promote --candidate <signature> --spec <check-spec.yaml>`. Operator-authored spec is the only way to turn a signature into an active check.
6. `verk constraints show <candidate-signature>` emits a starter YAML template reconstructed from the finding title and file glob (per SR-14). Template-only, not LLM-generated.

### Seed constraints (per SR-S2)

At P7 merge time, hand-write a small seed set of constraints drawn from `internal/adapters/runtime/standards/universal.md`:

- `safety-check-fail-closed` — grep for `return nil` patterns in scope-check functions.
- `aggregate-field-hardcoded` — grep for `Current*`, `Effective*`, `Aggregate*` fields with literal defaults.

Seed constraints exercise every code path (load, execute, report) end-to-end before relying on organic promotion.

### Execution

At verify phase: all `active` constraints append to the ticket's `validation_commands` set. Results merge into `verification.json` per-command results. Gate rules unchanged — any constraint command exit non-zero fails the ticket to `implement`.

Per-constraint `stdout_paths` contribute to `required_artifacts`.

Per-constraint `timeout_ms` (default 30s); total budget `policy.constraints.max_runtime_total_ms` (default 120s). Over-budget skips remaining constraints and flags in `verification.json.constraints_over_budget`.

### Constraint failure attribution

When a constraint fails verification, the error names the constraint: `constraint cst-abc123 failed: <reason>`. `verification.json` per-command result carries `constraint_id`. Repair workers see "this constraint triggered" and can read `verk constraints show <id>` for the promoted-from history.

### CLI

- `verk constraints list [--active | --candidate | --disabled]`
- `verk constraints show <id-or-candidate-signature>` — full history, promoted_from, check spec, starter template for candidates.
- `verk constraints promote --candidate <signature> --spec <path>` — requires spec file. Validates spec. Writes to `index.json` atomically.
- `verk constraints disable <id> --reason <text>` — flips `active: false`, records `disabled_at`/`disabled_reason`.
- `verk constraints history <id>` — tickets that triggered it.
- `verk constraints gc` — proposes stale-constraint disablement (operator-confirmed only, not auto-applied).

### Disable and decay

- Constraints can be disabled at any time. Disabled constraints stay in `index.json` for audit.
- Auto-decay (v1 default off): if a constraint hasn't triggered in `policy.constraints.stale_days` (default 90), `verk constraints gc` proposes disablement.

### Files

- `internal/engine/constraints/{signature,store,promote,execute}.go` (new package).
- `internal/engine/constraints/signature_test.go` — signature stability + collision tests.
- `internal/engine/verify.go` or `internal/engine/ticket_run.go` — run constraints alongside declared verification commands.
- `internal/engine/closeout.go` — post-closeout candidate append hook (filters by `promotable`).
- `internal/cli/constraints.go` (new) — subcommand.
- `internal/policy/config.go` — `policy.constraints.{enabled, activation_threshold, stale_days, max_runtime_total_ms}`.

### Tests

- Unit: signature computation stable for equivalent findings; distinct for different severity buckets.
- Unit: candidate accumulation counts distinct tickets, not distinct findings.
- Unit: `promotable = false` findings do not enter candidates.
- Unit: promotion requires operator spec; auto-promotion path is absent.
- Unit: `command` and `grep` check types plug into verification correctly.
- Unit: `base_used` correctly selected for wave vs single-ticket runs.
- Unit: constraint failure surfaces `constraint_id` in `verification.json` per-command result.
- Unit: disable → constraint skipped at verify; active=false persists across runs.
- Unit: constraint timeout respects `timeout_ms`; budget exhaustion skips remaining constraints and flags.
- Unit: seed constraints load and execute on a fresh install.
- E2E: seed 3 tickets with same-signature findings across 3 runs → candidate ready marker appears with human-readable filename; operator promotes with a `grep` spec; next ticket introducing the pattern fails verify.
- E2E: disabled constraint → verify passes; re-enable → next verify catches.

### Risks

- **False promotions become permanent friction.** Mitigation: operator-authored spec gate; `disable` CLI one command; disabled-rate metric watched.
- **Signature collisions.** Mitigation: signature includes severity bucket + file glob; observable via `verk constraints candidates`; in v1 collisions accepted as "might catch two bug classes at once" and promoted carefully.
- **Constraint runtime cost accumulates.** Mitigation: `timeout_ms` per constraint + `max_runtime_total_ms` per verify; over-budget skips remaining.
- **Stale constraints.** Mitigation: `verk constraints gc` proposer, not auto-disable.

### Estimated effort

5–6 days.

---

## Adoptions from gstack (Generic)

These are cross-cutting borrows from gstack (`github.com/garrytan/gstack`) that apply to verk's impl+verify loop regardless of delivery form. They are smaller than P1–P7 and share infrastructure with the sibling plan's skill-mode work. Ship alongside the main tracks.

Scope rule for this section: **only adoptions that operate on verk's engine-mode workers and reviewers**. Delivery-form concerns (daemon, host adapters, skill templates) live in [verk-as-skill-cross-agent](2026-04-19-verk-as-skill-cross-agent.md).

### A1 — Systematized Role Personas for Workers and Reviewers

**Goal.** Today verk has three reviewer personas in `docs/review-prompts/` (distributed-systems workflow, rigid-QA adversarial, senior-Go systems). The implementer side has no corresponding persona files — worker prompts are assembled ad-hoc from the ticket and the standards bundle. Systematize the persona concept for **both** roles, consistent format, versioned, loadable at prompt time.

**Shape.**
- New directory `internal/adapters/runtime/personas/` with files:
  - `implementer-generic.md` — default for any worker.
  - `implementer-go-systems.md` — Go-specific systems engineer.
  - `reviewer-generic.md` — default ticket-scope reviewer.
  - `reviewer-adversarial-qa.md` — existing rigid-QA prompt reshaped.
  - `reviewer-distributed-systems.md` — existing prompt reshaped.
  - `reviewer-storage-contract.md` — existing prompt reshaped.
- Each file has a consistent footer: `Red Flags`, `Verification`, `Return Schema`. Borrowed from agent-skills' skill structure.
- Frontmatter declares `role: implementer|reviewer`, `applies_to: []` (e.g., `["go", "systems"]`), `version`.

**Selection.**
Engine picks one implementer persona + one reviewer persona per dispatch based on:
1. Ticket frontmatter `persona_hint` (operator override).
2. `owned_paths` file extension matching (`*.go` → go-systems personas).
3. Default fallback (`implementer-generic`, `reviewer-generic`).

Policy: `policy.personas.{default_implementer, default_reviewer, selection_rules}`.

**Injection.**
Persona file prepends the standards bundle in the worker/reviewer prompt. Order: persona → standards → anti-rationalization (implementer only, per P3) → task-specific instructions.

**Applies to.**
- Ticket-scope implementer workers (P1, existing implement path).
- Ticket-scope reviewer (existing review path).
- Wave-scope reviewer (P5) — gets `reviewer-wave.md` instead of `reviewer-generic`.
- Epic-scope reviewers (P6) — gets `reviewer-epic-plan.md` or `reviewer-epic-acceptance.md`.
- Repair workers — gets `implementer-repair.md` (variant of implementer that knows it's repairing findings, not implementing new).

**Files.**
- `internal/adapters/runtime/personas/*.md` — persona content.
- `internal/adapters/runtime/personas/select.go` — selection logic.
- `internal/adapters/runtime/personas/select_test.go`.
- `internal/adapters/runtime/{claude,codex}/adapter.go` — wire persona into prompt assembly (before standards bundle).

**Tests.**
- Unit: persona selection for a `.go` ticket returns go-systems personas.
- Unit: operator override via `persona_hint` wins over selection rules.
- Unit: missing persona file returns a terminal error (no silent fallback to empty).
- Snapshot: rendered implementer prompt includes persona header, standards, anti-rationalization, task instructions, in that order.

**Risks.**
- Persona files drift from engine reality. Mitigation: persona selection logic has a `require_role_return_schema` check — each persona must declare its `Return Schema` section matching the engine's expected output contract. Generator audits this at build time.

**Estimated effort.** 1.5 days.

### A2 — Cross-Runtime Reviewer by Default

**Goal.** When both Codex and Claude adapters are available, default reviewer runtime ≠ implementer runtime. Cheapest possible "two different models looked at this" without a multi-judge council (which remains excluded).

**Why this is not a council.**
- One implementer call, one reviewer call — same count as today.
- No deliberation, no parallel judges, no convergence logic.
- Only difference: the engine picks the reviewer's runtime to be the *other* one, when both are configured.

**Design.**
- New policy field `policy.runtime.prefer_cross_model_review: bool` (default `true`).
- Implementer adapter selection is explicit (ticket frontmatter or `policy.runtime.default_runtime`).
- Reviewer adapter selection: if `prefer_cross_model_review = true` AND implementer's runtime is known AND another adapter is configured, pick that other adapter. Otherwise fall back to `policy.runtime.default_runtime`.
- Records `runtime_pair: {implementer, reviewer}` in `review-findings.json` for audit.

**Applies to.**
- Ticket-scope reviewer (existing).
- Wave-scope reviewer (P5).
- Epic-scope reviewers (P6).
- Repair-triggered re-review (if repair cycles trigger a fresh review, the runtime pair is preserved for comparability).

**Edge cases.**
- Only one adapter configured → preference ignored, use what's available.
- Configured alternate adapter missing credentials → warning + fall back + record `cross_model_fallback_reason`.
- Ticket frontmatter explicitly requests a specific reviewer runtime → override wins.

**Files.**
- `internal/policy/config.go` — new field.
- `internal/engine/runtime_select.go` (new) — pairing logic.
- `internal/engine/runtime_select_test.go`.
- `internal/state/types.go` — add `RuntimePair` to review artifacts.

**Tests.**
- Unit: Claude implementer + both adapters available → Codex reviewer selected.
- Unit: single-adapter repo → same runtime used; `cross_model_fallback_reason = "only_one_adapter"` recorded.
- Unit: ticket override wins over preference.
- Unit: missing credentials on alternate → fall back + warning.
- E2E: seeded ticket runs implementer on A, reviewer on B; artifact records `{implementer: A, reviewer: B}`.

**Risks.**
- Cross-model disagreements may increase repair rate (different models find different things). Mitigation: monitor repair rate as part of observability; if regression > 20%, revisit default. In practice disagreement is signal, not noise.
- Adapter cost differences. Mitigation: `model_tier_by_task` (from Token Strategy) already lets operators pick cheap models per task; cross-runtime composes cleanly with it.

**Estimated effort.** 1 day.

### A3 — Constraint-Context Surfacing in Worker Prompts

**Goal.** P7 turns recurring findings into mechanical constraints that catch regressions at verify time. Extend that: when an implementer or repair worker dispatches on a ticket whose `owned_paths` match any active constraint's `file_glob`, surface the constraint in the worker prompt. Prevents the bug *before* it's introduced, not after.

**Why this is an enhancement to P7, not a separate primitive.**
- Reuses `.verk/constraints/index.json` from P7 — no new store.
- Same signature/file-glob matching P7 already computes.
- Pure prompt enrichment — no new gates.

**Design.**
- At implement dispatch: engine looks up active constraints whose `file_glob` intersects with the ticket's `owned_paths`.
- Up to `policy.constraints.prompt_surface_max` (default 5) matching constraints are rendered into a "Known Patterns to Avoid" section of the implementer prompt.
- Each surfaced constraint shows: human-readable title, the finding signature, a one-line reminder ("this has been flagged 4 times across 4 tickets").
- At repair dispatch: same logic, plus any constraint that was triggered in the triggering verification run is surfaced with the specific failure output.

**Prompt fragment shape:**
```
## Known Patterns to Avoid

Constraints active for files you're touching:

- **cst-abc12**: Scope-check returning nil on empty input  — flagged 4× across tickets
- **cst-def34**: Aggregate field defaulted to most common value — flagged 3× across tickets

If your implementation would trigger any of these, stop and reconsider. They
have been caught as real bugs in prior work.
```

**Applies to.**
- Implementer dispatch (P1 intent + implement).
- Repair dispatch.
- NOT reviewers — reviewers should discover findings independently; surfacing constraints at review time would bias them toward confirming known patterns only.

**Integration with A1.**
Constraint-context block slots into the persona-driven prompt between standards and task-specific instructions. Zero extra plumbing after A1 lands.

**Cost controls.**
- Hard cap on number of surfaced constraints per prompt (`prompt_surface_max`).
- Disabled entirely if `policy.constraints.enabled = false`.
- Disabled per-ticket via ticket frontmatter `skip_constraint_surfacing: true` (useful for refactors that intentionally touch constraint-matched code).

**Files.**
- `internal/engine/constraints/surface.go` (new) — match + render.
- `internal/engine/constraints/surface_test.go`.
- `internal/engine/ticket_run.go` — call `surface.RenderForTicket(ticket)` and append to prompt context.
- `internal/policy/config.go` — `policy.constraints.prompt_surface_max`.

**Tests.**
- Unit: ticket with `owned_paths` matching 2 constraints renders both in prompt.
- Unit: `prompt_surface_max = 1` caps to one; most-triggered wins.
- Unit: constraint surface disabled via ticket frontmatter → no block rendered.
- Unit: repair dispatch after a constraint failure surfaces the triggering constraint with failure output.
- Unit: empty index → no block (not an empty heading).
- E2E: seed a constraint, create a ticket with matching `owned_paths`, verify implementer prompt contains the block.

**Risks.**
- Too many surfaced constraints overwhelm the prompt. Mitigation: hard cap + rank by trigger count.
- Constraints surfaced in prompt may bias implementer toward those patterns *specifically* rather than general quality. Mitigation: block is appended, not prepended; task instructions still lead.

**Estimated effort.** 1.5 days.

### A4 — Intake: Minimum-Criterion Gate

**Goal.** Close a latent hole: today's engine lets a zero-criteria ticket pass the `criteria_evidence` closeout gate trivially (empty loop, no evidence required). Reject such tickets at intake with a deterministic, typed error, with two explicit exception paths for genuinely criterion-less work.

**Verified failure mode.** `internal/engine/closeout.go` `validateCriteriaEvidence` iterates over `plan.Criteria`; if that slice is empty, every downstream check is vacuous. `internal/engine/intake.go:99-113` constructs the slice by dropping empty strings from `acceptance_criteria`. A ticket with frontmatter `acceptance_criteria: []`, or with only whitespace entries, currently passes intake and could close with no evidence whatsoever.

**Design.**
- New intake validation after `buildPlanCriteria`: if `len(plan.Criteria) == 0`, fail intake with reason `empty_acceptance_criteria` unless an explicit exception is declared.
- Two explicit exceptions, each recorded in `plan.json` as `no_criteria_reason`:
  - Frontmatter `type: docs` — pure-documentation tickets where acceptance is "docs render and CI passes". Criteria-evidence gate replaced by declared-checks gate + verification gate.
  - Frontmatter `no_criteria_by_design: true` — operator explicitly attests no criterion is appropriate (rare; audit-visible; surfaces in `verk status`).
- Exceptions bypass the `criteria_evidence` gate but **do not** bypass `verification`, `review`, or `declared_checks`.
- Policy flag `policy.intake.empty_criteria_mode ∈ {warn, reject}` (default `reject` on new installs; `warn` available for repos with legacy criterion-less tickets during migration).

**Files.**
- `internal/engine/intake.go` — add check after `buildPlanCriteria`; read exception flags from frontmatter.
- `internal/engine/intake_test.go` — rejection + exception cases.
- `internal/state/types.go` — add `NoCriteriaReason` field to `PlanArtifact`.
- `internal/engine/closeout.go` — when `NoCriteriaReason` is set, mark `criteria_evidence` gate as `skipped` (not `passed`) with the reason, so it's visible in audit.
- `internal/policy/config.go` — `policy.intake.empty_criteria_mode`.

**Tests.**
- Unit: zero-criteria ticket → intake returns `empty_acceptance_criteria`.
- Unit: all-whitespace criteria → same error (empties dropped, slice empty).
- Unit: `type: docs` with zero criteria → allowed, plan records `no_criteria_reason: "docs"`; closeout marks `criteria_evidence` as `skipped`.
- Unit: `no_criteria_by_design: true` → allowed, plan records `no_criteria_reason: "by_design"`.
- Unit: `empty_criteria_mode: warn` → plan records warning, no rejection.
- Unit: existing non-zero-criteria tickets → unaffected.
- Integration: docs-type ticket closes without evidence on any criterion; non-docs ticket with zero criteria is blocked at intake.

**Migration.**
- Pre-merge audit: `grep -L "acceptance_criteria:" .tickets/*.md` to locate legacy criterion-less tickets.
- Each audited ticket: add criteria, OR add `type: docs`, OR add `no_criteria_by_design: true` with a comment explaining why.
- During the migration window, operators can set `empty_criteria_mode: warn` to avoid blocking on legacy tickets.

**Risks.**
- Over-rejecting legitimate tickets on day one. Mitigated by the warn-mode escape hatch and the migration audit.
- Exception flags get cargo-culted and suppress real criteria work. Mitigated by `verk status` surfacing every run whose plan has a `no_criteria_reason`; review-friendly for operators.

**Estimated effort.** 0.5 days.

### Adoptions summary

| ID | Name | Ships with | Effort |
|----|------|-----------|--------|
| A1 | Systematized role personas | Alongside P1, P5, P6 | 1.5d |
| A2 | Cross-runtime reviewer default | Alongside P5 | 1d |
| A3 | Constraint-context in prompts | After P7 | 1.5d |
| A4 | Intake minimum-criterion gate | Before P1 | 0.5d |

Total: ~4.5 days. Fits inside the existing Week 2–3 budget as incremental additions to the P-items they pair with.

### Adoptions explicitly rejected

The gstack comparison surfaced several patterns that do **not** belong in verk:

- **Browser daemon.** Covered in the sibling plan as explicit out-of-scope.
- **Preamble per skill (~100 lines of bash).** verk's engine already owns state; a preamble would duplicate functionality.
- **Role specialists beyond worker + reviewer** (CEO, designer, SRE, etc.). Outside verk's engineering-execution boundary.
- **Telemetry.** verk stays local; no opt-in dashboard.
- **Taste memory / proactive skill suggestions.** UX-layer concerns; live in the agent, not in verk.
- **Auto-upgrade checks at runtime.** Operator-controlled via existing release process.

---

## Cross-Cutting

### Shared Reviewer Request Shape

P5 and P6 share `ReviewRequest.Scope ∈ {"ticket", "wave", "epic_plan", "epic_acceptance"}`. One `ReviewerAdapter` interface:

```go
type ReviewerAdapter interface {
    Review(ctx context.Context, req ReviewRequest) (ReviewResult, error)
}

type ReviewRequest struct {
    Scope              ReviewScope
    Mode               ReviewMode   // shadow | enforce (metadata for prompt + artifact only)
    BaseCommit         string
    ChangedFiles       []string
    DiffSummary        string       // capped at token budget
    TicketSummaries    []TicketSummary
    EffectiveThreshold Severity
    StandardsBundle    StandardsBundle
    ModelTier          ModelTier
    Timeout            time.Duration
    CacheContext       CacheContext // see Prompt Caching below
}
```

Every scope uses the same normalization rules:

- Severity normalized to `P0–P4` before returning to engine.
- Disposition restricted to `open | resolved | waived`.
- Findings carry `promotable: boolean` (per SR-S3).
- Findings carry `target_ticket_ids: []string` (multi-target per SR-4).
- `passed` recomputed by engine from findings; adapter-supplied `passed` is advisory.
- Missing `file` on any finding is a terminal adapter error.
- Plan-scope findings must cover every declared category (SR-5 forcing function).

No new runtime types. One adapter, four prompt modes.

### Prompt Caching

The single largest token-cost lever. Standards bundles are static; wave/epic system prompts are fixed per scope. That's the textbook cache-prefix case.

Structure every call as:

```
[ CACHEABLE PREFIX ]
  System prompt (reviewer persona + scope instructions + category list)
  StandardsBundle (filtered to applies_to=this role)
  Review-scope fixed framing
[ CACHE-VARYING TAIL ]
  Ticket/wave/epic summaries
  Diff summary
  Prior findings (for repair workers)
```

Implementation rules:

- `claude` adapter: mark the prefix with `cache_control: { type: "ephemeral" }` on the appropriate content block. Verify compatibility with the Codex CLI equivalent; if none, codex adapter documents "no caching" and records `cache_hit_ratio: 0`.
- `codex` adapter: use whatever caching primitive the CLI exposes at call time; if none, record zero-hit and proceed.
- The `CacheContext` field on `ReviewRequest` names a stable cache partition key so that multiple calls in the same session benefit (e.g., all wave reviews in run `run-42` share a partition).
- First call of a session pre-loads standards bundle as a cache warm-up. Not a separate network call — the first real call pays full cost; subsequent calls hit cache.

Metrics:

- Every call records `cache_hit_ratio` (reported by adapter). Goal: > 0.5 on second-and-later calls.
- `verk status --json` rolls up `cache_hit_ratio` per run.

Estimated savings: Anthropic prompt caching publishes up to ~90% cost reduction on cached prefixes. For verk, where standards bundles are reinjected on every worker call and every reviewer call, realistic steady-state savings are 40–60% of total token spend. This is the measurable success criterion for G5.

### Config Precedence (G7 note)

Every policy field introduced by P1–P7 follows verk's existing precedence order (per `docs/plans/done/initial_v1.md § Config contract`):

1. CLI flags
2. `.verk/config.yaml`
3. engine defaults

Schema validation enforced at `verk doctor` (must pass before any run) and engine startup (terminal error on malformed config). Full schema for each new section lives under `docs/plans/done/initial_v1.md` schema-addendum section added in the same PR as each P.

### Forward-compatible schema versioning (per SR-15)

Global rule added to `docs/plans/done/initial_v1.md § State mutation model`:

- Any artifact with `schema_version` greater than the engine's known max for its type is a **terminal load error** with message `engine too old for artifact version X; upgrade engine or use a compatible binary`. Never silent downgrade. Never guess-parse.
- `verk doctor` surfaces this as a blocking error (exit 2) with the offending artifact path.

---

## Self-Review (adversarial)

Applying verk's own `docs/review-prompts/2026-04-02-rigid-qa-adversarial-verification-reviewer.md` persona to this plan.

### Findings against the plan itself

| ID | Severity | Finding | Status |
|----|----------|---------|--------|
| SR-1 | **high** | Intent echo has a loophole: worker can echo correctly then implement different. | **Addressed.** P1 now includes post-implementation alignment check comparing `implementation.changed_files` vs `intent.planned_owned_paths`. |
| SR-2 | **high** | `TestReferences` free-form string format would pass trivially. | **Addressed.** P4 normalizes `TestReference` to `{test_function, file_line}` variants; engine resolves each to a concrete test; unresolvable → blocking gate error. |
| SR-3 | **high, revised** | Original SR-3 proposed hard-fail on truncation; this creates bad mid-loop failure modes. | **Replaced.** See [Token Strategy § Truncation is reported, not fatal](#truncation-is-reported-not-fatal). Reviewer inputs truncate via priority policy, run to completion, and record `truncation_severity`. Operator policy decides whether `significant` truncation blocks acceptance. No mid-review abort. |
| SR-4 | **medium** | P5 target assignment assumed single `target_ticket_id`; seam findings between tickets lose a target. | **Addressed.** Multi-target `target_ticket_ids: []string` plus `shared_with` list in repair context. |
| SR-5 | **medium** | P6 plan-time silent failure looks identical to pass. | **Addressed.** Declared-category forcing function — plan-time reviewer must emit one finding per category, `disposition = "resolved"` when no issue. Empty findings = terminal adapter error. Plan-time timeout routes to operator block. |
| SR-6 | **medium** | P7 candidate signatures not human-readable. | **Addressed.** Candidate filesystem paths use `<severity>-<first-word>-<file-glob-hash8>` keys. Signature hash kept internal. |
| SR-7 | **medium** | P7 `grep` base commit was unspecified. | **Addressed.** `base_used ∈ {wave_base_commit, head}` per constraint + per-execution. Recorded in verification artifact. |
| SR-8 | **medium** | P4 migration ordering vs in-flight runs unclear. | **Addressed.** Migration acquires `.verk/migrations.lock`; refuses to run if any `run.lock` is held; blocks new runs while running. |
| SR-9 | **medium** | Per-ticket LLM call budget expanded without aggregation. | **Addressed.** See [Token Strategy](#token-strategy). Prevention at intake, compression per call, safe-seam skips, adaptive models, and prompt caching together keep steady-state spend near the 2-calls baseline for most tickets. Soft-warn thresholds surface outliers. |
| SR-10 | **low** | P2 assumed current standards-injection scope. | **Addressed.** P2 Step 1 is an explicit grep + audit writeup to `docs/reviews/`. |
| SR-11 | **low** | P3 efficacy untested. | Tracked under **G8** (plan-done definition) as a follow-up measurement ticket. |
| SR-12 | **low** | P5 skip rule too narrow. | **Addressed.** Skip rule is "1 ticket **and** `len(owned_paths) ≤ 2`". |
| SR-13 | **low** | P6 operator-reopen flow after `__epic_plan__` unspecified. | **Addressed.** `verk reopen <run-id> --to epic_plan --mark-findings-addressed <finding-ids>`; engine validates addressed findings resolved in artifact. |
| SR-14 | **low** | P7 operator has nothing to author a spec from. | **Addressed.** `verk constraints show <candidate>` emits template-only starter YAML reconstructed from title + file glob. No LLM synthesis. |
| SR-15 | **low** | Schema forward-compat unspecified. | **Addressed.** See [Cross-Cutting § Forward-compatible schema versioning](#forward-compatible-schema-versioning-per-sr-15). |

### Spirit-of-the-plan findings

| ID | Severity | Finding | Status |
|----|----------|---------|--------|
| SR-S1 | **high** | Call-count expansion from 1 reviewer to 4 review scopes is a significant surface increase. | **Addressed via Rollout.** Three-mode flag (`disabled | shadow | enforce`) defaults to `shadow` on review scopes; flip-one-at-a-time rollout sequence with explicit soak gates. Revised MVP at the end still applies. |
| SR-S2 | **medium** | P7 compounding wedge is slow to observe; seed with hand-written constraints. | **Addressed.** P7 includes a seed-constraint set drawn from `standards/universal.md` to exercise the pipeline end-to-end at merge time. |
| SR-S3 | **medium** | Not all findings are equally promotable. | **Addressed.** Findings carry `promotable: boolean` (reviewer-assigned, default true for objective rules). Candidates file only accepts `promotable = true`. |
| SR-S4 | **low** | Future `vakt` bridge overlap with P5/P6 findings. | Tracked in [Out of Scope](#out-of-scope-deferred). Future bridge plan must define dedup rules. |

### Revised MVP cut

Given SR-S1 and the Rollout plan: ship **P3 + P4 + P5 + P7** first (drop P6 to a follow-up). This keeps verk's call budget roughly flat at 2 calls/ticket + 1 call/wave in `shadow` then `enforce`, delivers the compounding payoff, and defers epic-scope reviews until wave-scope proves its worth.

---

## Lower-Priority Must-Haves (G6–G8)

These are **in-plan and non-optional** but lower priority than P1–P7. They can land in a follow-up PR against the same plan doc once main implementation is underway, provided they ship before the final flag flip to `enforce` on all P's.

### G6 — Failure-injection Testing

**Goal.** Exercise crash-resume on every new artifact and every new adapter mode.

**Scenarios to cover:**

- Reviewer timeout mid-run (wave, epic_plan, epic_acceptance).
- Adapter returns malformed JSON for `IntentResult`, `ReviewResult`, `EpicReviewResult`.
- Disk full during atomic write of `intent-*.json`, `wave-review.json`, `epic-review-*.json`, `constraints/index.json`.
- Truncated diff fed to reviewer due to binary file or submodule boundary.
- Migration crash mid-file (P4): confirm rollback and no partial schema_version upgrades.
- `run.json` transition commit crash after sidecar write but before commit (intent phase specifically).
- Claim lease expiry during intent retries.
- Reviewer returns finding with `target_ticket_ids` referencing a ticket not in the wave: adapter-level terminal error.
- Cache-context mismatch (adapter reports cache hit when prefix changed): detect and invalidate.
- Simultaneous migration attempt from two processes: migrations.lock contention.

**Test shape:** every scenario has a deterministic fake adapter that simulates the failure; resume must produce the same artifacts that a crash-free run would produce or fail loudly with a typed error. No silent recovery.

**Ships with:** each P's primary PR adds the scenarios relevant to its artifacts. Global scenarios land in a `chaos_test.go` covering cross-P interactions.

**Effort:** ~3 days spread across all PRs.

### G7 — Config Precedence & Schema

**Goal.** Make new policy fields load under the same precedence as v1 and validate at the earliest possible moment.

**Work items:**

- For every P, add a schema section to `docs/plans/done/initial_v1.md` (or a linked addendum document) documenting: field name, type, default, allowed values, precedence position.
- Extend `internal/policy/config.go` JSON/YAML tags + validation for every new field.
- `verk doctor` gains validation for the full policy tree; invalid config is exit code 2 with a typed error listing offending fields.
- Engine startup refuses to run with invalid config.
- Precedence test: for each field, prove CLI flag > config file > default by observing effective value.

**Effort:** ~1 day.

### G8 — Plan-Done Definition

**Goal.** Measurable criteria for "this plan shipped successfully".

**Acceptance criteria for the plan as a whole:**

1. P1–P7 each have `policy.<feature>` flag and default **off** for new installs.
2. P1–P7 each pass unit + E2E test suites in CI.
3. P2 prompt budget measurement published in `docs/reviews/` with pre/post numbers under 20% context share.
4. P4 migration soaked for ≥5 real runs with zero legacy-flagged findings from new artifacts.
5. P5 and P6 collect ≥10 epic-runs of `shadow`-mode data on the verk repo itself; findings triaged; false-positive rate < 30%.
6. All flags flipped to `enforce` (or `true` for mechanical P's) one at a time with 5-epic soak per flip.
7. Observability metrics show: `repair_cycles_per_ticket` distribution stable or improved vs baseline; `llm_calls_per_ticket` p95 ≤ 5; `tokens_per_ticket` p95 ≤ baseline × 1.3 (accepting some increase for the new review scopes, cushioned by caching).
8. P7 has at least one organically-promoted constraint (not just seed) catching at least one ticket at verify.
9. G6 scenarios all pass.
10. G7 validation live in `verk doctor`.
11. One real epic run completes end-to-end with every P enforced and zero operator intervention.

**Effort:** criteria 1–4, 6, 10 are captured during each P's PR; criteria 5, 7–9, 11 are the final integration gate (~1 week of soak).

---

## Open Questions

1. **Where does the wave/epic reviewer token budget actually cap out in practice?** Needs a measurement pass during Rollout Stage 4 before finalizing truncation policy defaults.
2. **Should intent echo artifacts be counted in resume?** If intent-1 passes but intent-2 is in progress at crash, should resume restart intent-2 or accept intent-1's echo? Spec says re-dispatch on unexpired lease; confirm this survives intent-pass-but-implement-fail.
3. **Should `__wave_seam__` / `__epic_plan__` / `__epic_seam__` be the same sentinel?** Naming consistency vs clarity-of-intent.
4. **Does P7 `grep` check run on staged-only diff or full worktree diff?** In wave context a worker's worktree may have uncommitted changes. Choose before implementation; default `head` for single-ticket, `wave_base_commit` for wave-ticket is current plan but staged-only vs full-worktree within that is undecided.
5. **If P5 wave review reopens a ticket, should the ticket's `cycles/repair-<n>.json` carry `review_scope: "wave"` (proposed here) or is a new artifact type more honest?** Current repair schema assumes ticket-scope trigger.
6. **Model tier fallback when configured `cheap` model is unavailable?** Policy `policy.runtime.tier_fallback_order` or hard-fail? Lean toward hard-fail to keep behavior deterministic.
7. **Prompt cache partition key reuse across runs?** Same standards bundle across runs of the same repo could share a partition, but that crosses run boundaries. Current plan: per-run partition only. Revisit if cache hit rate underperforms.

## Out of Scope (Deferred)

- Bridge to `vakt` for deep cross-file review. Future plan; not required for the loop-tightening in this document. When built, must define finding dedup rules vs P5/P6.
- AST-based check type in P7.
- Auto-synthesis of constraint specs from LLM output. Kept human-authored in v1.
- Parallel/debating reviewers (multi-judge councils). Light + targeted is the stated v1 discipline.
- Constraint-to-finding back-promotion (e.g., if a constraint triggers, should it auto-create a review finding record?). v1 treats constraints and findings as distinct streams.
- Cost telemetry beyond `last_triggered_at` and per-artifact tokens. Richer dashboards are a follow-up.
- Efficacy measurement of P3 anti-rationalization (eval harness). Tracked under G8 as a follow-up ticket.

## Total Estimated Effort

- **Week 1 (discipline):** P2 (4h + audit) + P3 (2h) + P1 (3d) + P4 (2d) ≈ 5.5 days.
- **Week 2 (scoped reviewers):** P5 (3d) + P6 (3d) ≈ 6 days.
- **Week 3 (constraints):** P7 ≈ 5–6 days.
- **Lower-priority (G6–G8):** ~1 week of additional work, some folded into P1–P7 PRs.

**Total: ~3.5 weeks of focused work** for full rollout including shadow-mode soak.

Applying SR-S1's revised MVP: **P3 + P4 + P5 + P7** ≈ 10–11 days plus soak, with P1 + P2 + P6 as a follow-up track.
