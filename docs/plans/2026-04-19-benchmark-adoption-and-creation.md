# Benchmark Adoption and Creation Specification

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans
> to implement this plan task-by-task.

**Goal:** Define how `verk` should benchmark coding agents, runtime adapters,
and its own orchestration layer in a repeatable, cost-aware way.

**Architecture:** Use public coding benchmarks to exercise the full `verk`
pipeline with external comparability, and a private `verk-native` suite for
orchestration behavior that public benchmarks do not cover. Normalize all tasks
through a common benchmark runner that records deterministic results,
per-attempt model profiles, token usage, cost, repair loops, reviews, and
artifacts.

**Tech Stack:** Go, `tk`, `verk` artifacts, Docker or OCI containers,
SWE-bench-compatible datasets, Aider Polyglot tasks, Markdown/JSON/CSV reports.

---

## Background

We want to compare how `verk` behaves with different code agents and models:

- Claude
- Codex
- Gemini
- opencode
- future local or hosted agents

The goal is not one monolithic leaderboard. `verk` has several responsibilities,
and each needs a different benchmark style:

1. Coding ability: does the agent solve real software tasks?
2. Orchestration quality: does `verk` improve outcomes with planning, waves,
   review, repair, resume, and closeout gates?
3. Runtime reliability: do adapters survive terminal/process edge cases?

Public coding benchmarks should run through the full `verk` pipeline by default
because we want to benchmark the tool we are building, not isolated fragments of
it. A private `verk-native` suite still matters because public benchmarks do not
cover many `verk`-specific behaviors such as blocked ticket visibility, repair
routing, model fallback, and artifact completeness.

Full-pipeline benchmarking and model comparison are related but not identical.
When `full-verk` mode is used, the headline result is a system result: worker,
reviewer, repair policy, wave gates, fallback policy, and validation behavior
all contribute to the outcome. Reports must not collapse that into a raw
worker/model leaderboard unless the matrix controls reviewer and repair effects.

This spec adopts a layered benchmark strategy.

## Source Benchmarks

### SWE-Bench and SWE-Bench Multilingual

SWE-bench evaluates repository-scale issue resolution by giving an agent a
GitHub issue and repository snapshot, then scoring the generated patch using
tests. The original SWE-bench paper emphasizes real repositories, large
codebases, and execution-based evaluation.

SWE-bench Multilingual is the better first public coding target for `verk`
because it covers more of the languages `verk` is likely to orchestrate. It has
300 curated tasks from 42 repositories across 9 languages: C, C++, Go, Java,
JavaScript, TypeScript, PHP, Ruby, and Rust.

Adoption role:

- Primary public coding benchmark.
- Good for comparing agent/model coding ability.
- Good for testing multi-language repository workflows.
- Less direct coverage of `verk`-specific repair/review behavior.

### Aider Polyglot

Aider Polyglot is based on difficult Exercism coding exercises across C++, Go,
Java, JavaScript, Python, and Rust. It is less representative of repository
maintenance than SWE-bench-style tasks, but it is cheaper and faster.

Adoption role:

- Fast coding benchmark for iteration.
- Good smoke test for model/editing capability.
- Not sufficient as the main `verk` benchmark because tasks are smaller and
  less representative of multi-step repository work.
- Smoke results are regression/sanity signals only, not externally defensible
  model rankings.

### SWE-Bench Pro

SWE-bench Pro appears better suited to long-horizon, contamination-resistant
software engineering evaluation, but it is heavier and may have access or cost
constraints.

Adoption role:

- Later-stage benchmark.
- Use after the runner works on smaller public and private suites.
- Good for periodic frontier comparisons, not day-to-day regression testing.

## Benchmark Layers

### Layer 1: Public Coding Through Full Verk

Purpose:

- Compare full `verk` runs on externally meaningful coding tasks.
- Avoid overfitting to `verk` internals.
- Measure whether the complete pipeline, including review/repair/gates, solves
  coding tasks better than raw agent execution.

Suites:

- `swe-multilingual-smoke`: 10-20 hand-picked tasks.
- `swe-multilingual-go-rust-ts`: focused subset for languages we care about.
- `swe-multilingual-full`: all 300 tasks.
- `aider-polyglot-smoke`: small, cheap multi-language smoke subset.
- `aider-polyglot-full`: full Aider Polyglot suite if cost permits.

Primary score:

- Task resolved by official test harness.

Claim limits:

- Smoke suites are regression/sanity checks only.
- Public model/provider comparisons require a frozen representative task set, a
  documented sampling method, a minimum task count, and paired uncertainty
  reporting before the report can make broad claims.
- `full-verk` reports are system-level results unless the matrix fixes the
  reviewer profile or reports a full worker x reviewer factorial design.

Secondary metrics:

- patch applies
- fail-to-pass tests passed
- pass-to-pass tests preserved
- wall-clock time
- token usage
- dollar cost
- attempts
- repair cycles
- review findings
- final diff size

### Layer 2: Runtime and Terminal Reliability

Purpose:

- Test whether `verk` can drive different agents through their CLI runtimes.
- Catch adapter regressions and terminal/process problems.
- Validate cancellation, output parsing, environment behavior, and timeout
  handling.

This layer is intentionally deferred. We should not adopt Terminal-Bench in the
first benchmark version. Runtime reliability should initially be covered by
`verk-native` tasks and adapter-specific tests.

Primary score:

- adapter/runtime behavior passes deterministic `verk-native` or adapter tests

Secondary metrics:

- adapter startup failures
- command timeout frequency
- process tree cleanup
- stdout/stderr capture correctness
- invalid protocol block rate
- retry classification correctness
- cancellation latency

### Layer 3: Verk-Native Orchestration

Purpose:

- Test what public benchmarks do not cover well:
  - wave scheduling
  - child ticket planning
  - review pressure
  - repair routing
  - blocked ticket visibility
  - resume behavior
  - model fallback
  - artifact completeness
  - cost-aware context passing

This suite is private and versioned in the repository. It should use small
fixture repositories and deterministic fake services where possible.

The private suite must be split into:

- a development/regression set that can be used during implementation
- a locked holdout set that is not used for routine tuning

Tasks sourced from real `verk` failures are valuable, but they can overfit if
the same examples guide implementation and judge readiness. Holdout tasks should
be run only at defined validation points, and private canary tasks should be
rotated or refreshed after major model or orchestration changes.

Suites:

- `verk-native-smoke`
- `verk-native-review-repair`
- `verk-native-wave-resume`
- `verk-native-model-fallback`
- `verk-native-full`

Primary score:

- Deterministic acceptance checks pass and all expected `verk` artifacts are
  present.

Secondary metrics:

- issue solved
- ticket closed only after required evidence
- review findings repaired or blocked
- wave state correct
- resume cursor correct
- blocker reason visible
- model fallback recorded
- no endless loop
- token/cost budget respected

## Non-Goals

- Do not create a single vanity leaderboard that hides task class differences.
- Do not adopt Terminal-Bench in the initial implementation.
- Do not rely on model-judge scoring for primary pass/fail.
- Do not require every local development run to execute expensive public
  benchmarks.
- Do not force retries to reuse a failed model just because an earlier attempt
  recorded that model in artifacts.
- Do not expose private benchmark tasks as public model-training data.

## Resolved Design Decisions

The first version should resolve enough choices that implementation can start
without reopening the whole design.

### Task Materialization

`verk bench` should create real `tk` tickets in an isolated benchmark workspace
by default. That exercises the same intake, artifact, ticket, review, repair,
resume, and closeout paths as production `verk run`.

In-memory tasks are allowed only for unit tests of provider parsing, matrix
expansion, scoring, and report rendering. They should not be the default
execution path for benchmark runs because they would bypass important `verk`
integration behavior.

### Benchmark Modes

The runner should make benchmark intent explicit.

`capability` mode:

- worker-only by default
- no reviewer by default
- no repair by default
- model fallback disabled by default
- measures raw coding ability under a fixed profile as an optional ablation
- not the default for public benchmark adoption

`full-verk` mode:

- reviewer enabled
- repair enabled
- wave/epic gates enabled
- model fallback allowed if configured
- measures whether `verk` orchestration improves final outcomes
- primary mode for `verk-native` suites and public coding benchmarks

`runtime` mode:

- focused on adapter and terminal behavior
- fallback disabled unless the task explicitly tests fallback
- classifies runtime failures separately from task failures
- deferred until we explicitly adopt runtime-only benchmarks

Reports must always include the mode, because solve rates from these modes are
not directly comparable.

Mode-specific claim rules:

- `capability` mode may produce worker/model comparisons only when task
  manifests and worker profiles are fixed.
- `full-verk` mode produces end-to-end `verk` system results by default.
- `full-verk` worker leaderboards are valid only when reviewer profile, repair
  policy, fallback policy, and task manifest are controlled.
- `runtime` mode results must not be mixed with coding solve-rate results.

### Public Benchmark Review Policy

For public coding benchmarks, the default run should use `full-verk` mode. The
point is to evaluate the complete tool: planning, worker execution, review,
repair, validation, artifacts, and closeout behavior.

`capability` mode remains useful as an optional ablation. It answers whether the
full pipeline improves or hurts solve rate, cost, and latency relative to a raw
worker-only run. It should not be the headline number for `verk`.

When a report compares workers or models, reviewer variation must be controlled:

- Use a fixed reviewer profile for simple worker leaderboards.
- Or run a full worker x reviewer factorial matrix and report interaction
  effects explicitly.
- Do not collapse `all-pairs` results into a worker-only ranking unless the
  report labels the aggregation as exploratory and shows reviewer effects.

### Model Fallback Policy

Fallback is disabled by default in strict profile comparisons. If a profile is
unavailable, the task should be marked as a profile availability failure for
that matrix cell.

Fallback is allowed in production-like `full-verk` runs and in dedicated
fallback tests. When fallback occurs, the task result must record both the
failed attempt profile and the fallback profile actually used.

Benchmark runs freeze the resolved fallback chain before execution starts. A
resume may use fallback profiles from that frozen run snapshot. If live config
has changed since the snapshot, the runner must either keep using the snapshot,
refuse to resume, or require an explicit override that marks the run
non-comparable.

## Core Concepts

### Benchmark Suite

A suite is a named collection of tasks plus a runner policy.

Example:

```yaml
id: swe-multilingual-go-rust-ts
source: swe-bench-multilingual
version: "2026-04-19"
task_filter:
  languages: [go, rust, typescript]
  limit: 30
policy:
  max_attempts: 1
  allow_repair: false
  allow_review: false
```

### Benchmark Task

A task is the normalized unit of work. Public benchmark tasks and `verk-native`
tasks should be adapted into this shape.

Required fields:

- task id
- suite id
- source benchmark
- source version
- repository or fixture path
- base commit or image digest
- prompt or issue text
- setup commands
- validation commands
- expected scoring method
- timeout
- allowed network policy
- allowed files or workspace scope

### Agent Profile

An agent profile describes how `verk` invokes an agent for a role.

Fields:

- profile id
- runtime: `claude`, `codex`, `gemini`, `opencode`
- model
- reasoning level
- role: worker, reviewer, or both
- max turns
- timeout
- fallback profiles
- environment requirements

This must align with `ver-laq2`: model and reasoning are config-owned, not
ticket-owned.

### Attempt Profile

Every worker or reviewer attempt records what was actually used:

- runtime
- model
- reasoning
- role
- fallback reason, if any
- start/end time
- token usage
- cost
- retry class

Attempt metadata records what happened. It is not by itself an execution lock
for normal `verk run` behavior. Benchmark runs add a stricter layer: before the
first attempt, the runner snapshots the resolved worker, reviewer, and fallback
profile chain for the matrix cell. Resume and retry use that benchmark snapshot
unless an explicit drift override marks the run non-comparable.

### Benchmark Run

A benchmark run is one execution of one suite under one matrix.

Fields:

- benchmark run id
- git commit of `verk`
- dirty-worktree state
- comparability state: comparable, exploratory, or non-comparable
- suite id/version
- matrix id
- locked task manifest id and task ids
- resolved agent profile snapshot
- resolved fallback chain snapshot
- start/end time
- total cost
- total tokens
- environment metadata
- result summary
- run-complete marker

Comparable benchmark runs require a clean worktree by default. Dirty runs are
allowed only with an explicit exploratory override, and they cannot become
baselines for headline comparisons.

### Task Result

A task result records one task under one matrix cell.

Fields:

- task id
- agent profile ids
- solved: true/false
- deterministic score details
- validation command results
- patch artifact
- run artifact path
- attempts
- reviews
- repair cycles
- blockers
- final status
- verifier flaky status, if applicable
- token/cost usage
- wall-clock duration

## Benchmark Matrix

The matrix should support runtime/model comparisons without changing tickets.

Example:

```yaml
id: core-agent-matrix
workers:
  - id: claude-sonnet-high
    runtime: claude
    model: sonnet
    reasoning: high
  - id: codex-frontier-high
    runtime: codex
    model: gpt-5.4
    reasoning: high
  - id: gemini-pro
    runtime: gemini
    model: gemini-pro
    reasoning: high
  - id: opencode-qwen
    runtime: opencode
    model: qwen-coder
    reasoning: high
reviewers:
  - id: claude-opus-xhigh
    runtime: claude
    model: opus
    reasoning: xhigh
  - id: codex-frontier-xhigh
    runtime: codex
    model: gpt-5.4
    reasoning: xhigh
pairing:
  mode: all-pairs
```

Simple worker/model leaderboards should prefer a fixed reviewer profile. The
`all-pairs` mode is useful for research and robustness analysis, but reports
must expose reviewer effects and worker x reviewer interactions instead of
silently folding every pair into one ranking.

Matrix controls:

- max total cost
- max task cost
- max wall-clock time
- concurrency
- retries per transient failure
- repair cycles
- review threshold
- randomization seed
- benchmark mode: `full-verk`, `capability`, or deferred `runtime`
- fallback policy: disabled, enabled, or task-only
- comparison design: fixed reviewer, full factorial, or exploratory
- task manifest lock: required for comparable runs

## Provider Adoption Criteria

A benchmark provider is eligible for adoption only when it defines:

- how tasks are discovered and versioned
- how repositories, images, or fixtures are pinned
- how setup commands run
- how the verifier runs
- how official pass/fail is computed
- how task timeouts work
- what network access is required
- what local tools are required
- how large caches are stored outside `.verk/bench`
- how mutable solution workspaces are separated from immutable verifier
  workspaces or pinned containers
- how shared caches are namespaced, locked, or mounted read-only
- how candidate patches are filtered before verification
- what license or usage constraints apply
- how provider-specific failures map into `verk` failure taxonomy

Providers that cannot supply deterministic scoring may still be useful for
exploration, but they should not be used for regression gates.

## Scoring Model

### Primary Scoring Rules

Public coding suites:

- pass/fail according to official benchmark tests
- no model judge for primary score
- no credit for explanations without passing tests

Verk-native:

- pass/fail according to deterministic acceptance checks
- artifact assertions are part of the score
- hidden or missing blockers count as failures even if code tests pass

### Secondary Metrics

Record these for every suite:

- solved
- patch applies
- tests passed
- wall-clock time
- worker attempts
- reviewer attempts
- repair attempts
- wave count
- resumed: true/false
- blocked: true/false
- unresolved blocker reason
- input tokens
- cached input tokens
- output tokens
- total tokens
- estimated dollar cost
- final diff files changed
- final diff lines added/removed
- validation commands run
- validation commands skipped
- flake retries

### Failure Taxonomy

Every failed task should be classified into one primary failure class:

- `task_failed`: agent produced a patch, but official tests failed.
- `patch_failed`: patch could not be applied cleanly.
- `setup_failed`: repository or benchmark setup failed before the agent ran.
- `verifier_failed`: the verifier crashed or produced invalid output.
- `adapter_failed`: runtime adapter failed to invoke or parse the agent.
- `agent_unavailable`: selected model/runtime was unavailable.
- `budget_exceeded`: task exceeded configured cost or time budget.
- `policy_blocked`: `verk` blocked due to safety, scope, or user-needed context.
- `harness_failed`: benchmark runner bug or missing artifact.

Reports should separate `task_failed` from harness and adapter failures. A model
should not be penalized as a coding failure when the benchmark harness or
runtime adapter broke before it had a fair attempt.

Verifier flakiness is a first-class result status or durable substatus, not a
generic verifier failure. Reports should surface `verifier_flaky` separately
from solved, failed, setup, patch, adapter, and harness failures.

### Flake Policy

Flaky tasks can distort small benchmark suites. The runner should support a
bounded verifier retry policy that is separate from agent retry policy.

Rules:

- Never rerun the agent automatically just because a verifier is flaky.
- Rerun only the verifier when the failure matches a configured flake pattern.
- Record every verifier retry.
- Mark the task `verifier_flaky` when a retry changes the result.
- Report raw solve rate and flake-adjusted solve rate side by side.
- Exclude unresolved flaky tasks from adjusted headline solve-rate deltas only
  when the report explicitly labels the adjustment.
- Keep flake allowlists versioned with the suite.
- Define a maximum flake-exclusion percentage for any result used in headline
  regression claims.

### Regression Thresholds

Each suite can define regression rules.

Example:

```yaml
regression:
  min_solve_rate_delta: -0.03
  max_cost_delta: 0.20
  max_duration_delta: 0.25
  fail_on_new_adapter_error: true
  fail_on_missing_artifact: true
```

For small suites, avoid overinterpreting solve-rate deltas. Prefer task-level
regression reports.

### Baseline Comparison Rules

Benchmark comparisons should be task-paired. Compare the candidate and baseline
only on task ids present in both runs unless the report explicitly says it is a
suite composition change.

Headline comparisons require identical locked task manifests. If the baseline
and candidate do not cover the same predeclared task set, the comparison is
exploratory and must not produce headline solve-rate deltas.

Comparable baseline/candidate runs also require compatible resolved profile
snapshots, fallback policy, benchmark mode, suite version, clean-worktree state,
and run-complete markers. Runs with dirty-worktree overrides, skipped matrix
cells, changed task manifests, or out-of-snapshot fallback are non-comparable
unless the report explicitly labels them as exploratory.

Required comparison views:

- all tasks
- tasks solved by baseline but failed by candidate
- tasks failed by baseline but solved by candidate
- tasks with cost regression over threshold
- tasks with duration regression over threshold
- tasks with new adapter/runtime failures
- tasks with changed failure class

For small samples, prefer a task-level table over claims such as "model A is
better." For larger samples, reports may include confidence intervals, but they
must not hide task-level regressions.

## Verk-Native Task Catalog

The private suite should include tasks that came from real `verk` failures.

### Task Class: Narrow Test Misses Lint

Purpose:

- Catch cases where a worker passes the ticket's focused test but fails a
  derived lint check.

Expected behavior:

- `verk` derives the missing lint check.
- repair worker receives focused failure output.
- ticket closes only after lint passes.

### Task Class: Stale Docs Outside Named File

Purpose:

- Catch ticket-scope gaps where named docs are updated but related docs remain
  stale.

Expected behavior:

- wave or epic gate catches stale wording.
- finding is mapped to the owning ticket when possible.
- repair or blocker is recorded.

### Task Class: Review Finding Repair

Purpose:

- Ensure reviewer findings trigger repair rather than report-only failure.

Expected behavior:

- reviewer returns a medium/high finding.
- repair worker receives the finding and relevant diff.
- review reruns or finding is marked resolved.

### Task Class: Wave-Level Merged Failure

Purpose:

- Catch failures caused by multiple tickets landing together.

Expected behavior:

- wave validation runs after merge.
- wave repair runs before next wave.
- wave artifact records validation coverage.

### Task Class: Blocked Ticket Visibility

Purpose:

- Prevent hidden "3/4 closed" wave summaries.

Expected behavior:

- blocked/skipped ticket appears with reason.
- interactive mode can ask for retry/unblock when safe.
- non-interactive mode exits with instructions.

### Task Class: Resume During Pending Verification

Purpose:

- Ensure interrupted runs resume at the correct phase.

Expected behavior:

- pending verification is persisted.
- resumed run continues verification/repair.
- completed tickets are not re-executed.

### Task Class: Model Fallback

Purpose:

- Verify model fallback behavior after runtime/service failure.

Expected behavior:

- failed attempt records runtime/model/reasoning.
- fallback reason is recorded.
- retry uses configured fallback profile.
- final result remains auditable.

### Task Class: Token Efficiency

Purpose:

- Detect benchmark regressions in context size and cost.

Expected behavior:

- repair/review prompts use focused diffs and summaries.
- full artifacts are not passed unless necessary.
- cost deltas are visible in reports.

## CLI Specification

### List Suites

```bash
verk bench list
```

Output:

- suite id
- source benchmark
- task count
- estimated time
- estimated cost
- installed/available status

### Run Suite

```bash
verk bench run <suite-id> --matrix bench/matrices/core.yaml
```

Options:

- `--limit N`
- `--task <id>`
- `--language <name>`
- `--runtime <runtime>`
- `--worker-profile <profile>`
- `--reviewer-profile <profile>`
- `--max-cost <amount>`
- `--max-duration <duration>`
- `--concurrency N`
- `--seed <seed>`
- `--resume <bench-run-id>`
- `--no-review`
- `--no-repair`
- `--output json|markdown`
- `--allow-dirty`
- `--exploratory`

`--allow-dirty` must imply exploratory/non-comparable output unless a future
suite-specific policy proves that the dirty state cannot affect the benchmark.
Comparable runs should fail fast when the source worktree is dirty.

### Compare Runs

```bash
verk bench compare <baseline-run-id> <candidate-run-id>
```

Comparison output:

- solve-rate delta
- cost delta
- time delta
- token delta
- new failures
- fixed failures
- task-level diff table
- adapter/runtime failure table

### Report

```bash
verk bench report <bench-run-id> --format markdown
```

Report formats:

- Markdown for human review.
- JSON for automation.
- CSV for spreadsheets.

## Artifact Layout

Recommended layout:

```text
.verk/bench/
  suites/
    registry.json
  runs/
    bench-<timestamp>-<slug>/
      bench_run.json
      matrix.json
      locked_task_manifest.json
      resolved_profiles.json
      environment.json
      checkpoints.json
      complete.json
      tasks/
        <task-id>/
          task.json
          result.json
          state.json
          usage.json
          verk-run/
          patch.diff
          verifier/
            verifier_input.json
            verifier_result.json
          logs/
      reports/
        summary.md
        summary.json
        summary.csv
```

Do not store large third-party repositories directly in `.verk/bench`. Store
cache pointers, image digests, or external checkout metadata instead.

Artifact durability rules:

- JSON artifacts are written with atomic file replacement.
- Each task has a durable state/checkpoint file that records the last committed
  phase.
- `complete.json` is written only after all task results and reports are
  persisted.
- `compare` refuses runs without `complete.json` unless explicitly asked to
  inspect partial runs.
- Resume starts from the last committed checkpoint and ignores or repairs
  incomplete partial writes.

Workspace and cache metadata should record:

- mutable solution workspace path
- immutable verifier workspace or container digest
- cache namespace
- cache mode: read-only, locked mutable, or disabled
- cache hit/miss or reuse metadata when available

## Provider Interface

The runner should use provider adapters.

```go
type BenchmarkProvider interface {
    ID() string
    ListSuites(ctx context.Context) ([]SuiteInfo, error)
    PrepareTask(ctx context.Context, task TaskSpec) (PreparedTask, error)
    ScoreTask(ctx context.Context, result TaskExecution) (Score, error)
    CleanupTask(ctx context.Context, task PreparedTask) error
}
```

Provider implementations must make the verification boundary explicit. The
agent receives only the mutable solution workspace. The verifier receives a
fresh checkout, pinned container, or otherwise immutable workspace plus the
filtered candidate patch. The runner must never patch hidden tests, scoring
scripts, benchmark metadata, or the verifier tree in place.

Initial providers:

- `verk-native`
- `aider-polyglot`
- `swe-bench-multilingual`

Later providers:

- `swe-bench-pro`
- `terminal-bench`
- custom private repository suites

## Execution Flow

1. Load suite registry.
2. Load matrix.
3. Check clean-worktree policy; fail comparable runs on dirty state unless an
   explicit exploratory override is provided.
4. Resolve and lock the task manifest.
5. Resolve and snapshot worker, reviewer, and fallback profile chains.
6. Check budget and environment prerequisites.
7. Prepare each task in an isolated mutable solution workspace.
8. Prepare a separate immutable verifier workspace or pinned verifier
   container.
9. Convert task into one or more `tk` tickets.
10. Run `verk` using the selected worker/reviewer profile snapshot.
11. Collect `verk` artifacts.
12. Filter the final patch to allowed repository paths.
13. Apply the filtered patch only to a fresh verification checkout, never to the
    verifier metadata or hidden-test tree.
14. Run official verifier or deterministic checks.
15. Record result artifacts with atomic writes and committed phase checkpoints.
16. Generate reports.
17. Write `complete.json`.
18. Compare against baseline when requested.

## Integration With Existing Epics

This benchmark work depends on role-based model config from `ver-laq2`.

It should also consume results from the repair-oriented run epic `ver-vyag`,
especially:

- validation coverage artifacts
- review finding repair artifacts
- wave validation artifacts
- blocked ticket visibility
- per-attempt runtime/model/reasoning metadata

The benchmark runner should not duplicate `verk run` logic. It should prepare
tasks, invoke `verk`, and score outputs.

## Benchmark Adoption Phases

### Phase 1: Design and Metadata

Deliverables:

- benchmark registry format
- matrix format
- result artifact schema
- provider interface
- report format

Validation:

- unit tests for parsing registry, matrix, and result artifacts
- markdown spec review

### Phase 2: Verk-Native Smoke Suite

Deliverables:

- 5-8 deterministic private tasks
- fake adapter support where possible
- local-only runner

Validation:

- `verk bench run verk-native-smoke`
- deterministic pass/fail output
- report generation

### Phase 3: Agent Matrix Smoke

Deliverables:

- runtime profile matrix for Claude, Codex, Gemini, and opencode
- skip unavailable runtime profiles with clear reasons
- per-attempt profile recording

Validation:

- one small task runs across all available runtimes
- unavailable runtimes are reported, not silently ignored

### Phase 4: Public Coding Benchmark Adapter

Deliverables:

- SWE-bench Multilingual subset provider
- Aider Polyglot smoke provider
- official verifier integration where practical

Validation:

- run a small subset under one profile
- compare two profiles
- produce Markdown/JSON/CSV reports

### Phase 5: Public Coding Expansion

Deliverables:

- broader SWE-bench Multilingual subset
- optional SWE-bench Pro spike if access and cost allow
- more `verk-native` orchestration tasks based on real failures

Validation:

- compare at least two full `verk` profiles on the same public coding subset
- reports separate solve-rate changes from cost and duration changes

### Phase 6: CI and Baselines

Deliverables:

- checked-in baseline reports
- nightly smoke benchmark
- weekly public subset benchmark
- manual full benchmark command

Validation:

- CI detects benchmark harness regressions
- reports show cost/time/solve-rate deltas

## MVP Implementation Breakdown

The first implementation should be small enough to land safely. Suggested
ticket-sized tasks:

### Task 1: Benchmark Artifact Types

Files:

- Create `internal/bench/types.go`
- Test `internal/bench/types_test.go`

Implement:

- suite metadata structs
- matrix structs
- locked task manifest structs
- resolved profile snapshot structs
- task result structs
- usage record structs
- failure taxonomy constants
- `verifier_flaky` status or durable substatus
- run checkpoint and complete-marker structs
- usage confidence and pricing-version fields
- JSON round-trip tests

Validation:

```bash
go test ./internal/bench
```

### Task 2: Matrix Parser

Files:

- Create `internal/bench/matrix.go`
- Test `internal/bench/matrix_test.go`

Implement:

- parse matrix YAML/JSON
- validate unique profile ids
- validate benchmark mode
- expand worker/reviewer pairings
- support fixed-reviewer and full-factorial comparison designs
- reject or mark exploratory all-pairs worker leaderboard requests
- enforce fallback policy defaults by mode
- resolve and freeze fallback chains for benchmark runs

Validation:

```bash
go test ./internal/bench
```

### Task 3: Provider Interface and Registry

Files:

- Create `internal/bench/provider.go`
- Create `internal/bench/registry.go`
- Test `internal/bench/registry_test.go`

Implement:

- provider interface
- in-process provider registry
- suite listing
- provider capability metadata
- provider verifier isolation metadata
- provider cache policy metadata

Validation:

```bash
go test ./internal/bench
```

### Task 4: Verk-Native Smoke Provider

Files:

- Create `internal/bench/providers/verknative`
- Create fixture data under a small benchmark fixture directory
- Test provider loading and scoring

Implement:

- 2-3 deterministic initial tasks
- classify tasks as development/regression or holdout
- task preparation into isolated workspaces
- separate mutable solution workspace from immutable verifier workspace
- deterministic scoring without live AI calls

Validation:

```bash
go test ./internal/bench/... ./internal/engine/...
```

### Task 5: CLI Skeleton

Files:

- Modify `internal/cli`
- Add CLI tests

Implement:

- `verk bench list`
- `verk bench run <suite>`
- `verk bench report <run-id>`
- `verk bench compare <baseline> <candidate>`
- JSON and Markdown output stubs
- `--allow-dirty` and exploratory/non-comparable report labeling
- refusal paths for comparing incomplete or non-comparable runs

Validation:

```bash
go test ./internal/cli/... ./internal/bench/...
```

### Task 6: Run Orchestration

Files:

- Create `internal/bench/runner.go`
- Test `internal/bench/runner_test.go`

Implement:

- load suite
- load matrix
- prepare tasks
- fail comparable runs on dirty worktrees by default
- lock task manifests before execution
- snapshot resolved profiles and fallback chains
- create isolated `tk` tickets
- allocate separate per-run/per-task solution and verifier workspaces
- enforce cache namespaces and locking/read-only cache modes
- invoke `verk run`
- collect artifacts
- classify failures
- write task and run checkpoints atomically
- write final run-complete marker only after reports are persisted
- resume from committed checkpoints
- enforce budgets

Validation:

```bash
go test ./internal/bench/... ./internal/engine/...
```

### Task 7: Report and Compare

Files:

- Create `internal/bench/report.go`
- Create `internal/bench/compare.go`
- Test report and comparison output

Implement:

- Markdown report
- JSON report
- CSV report
- task-paired baseline comparison
- exact vs estimated usage labels
- exact-only and estimated/derived cost comparison sections
- raw and flake-adjusted solve rates
- task manifest and profile snapshot compatibility checks
- mode-specific claim labels: system, capability, runtime, exploratory
- reviewer-effect reporting for full worker x reviewer matrices

Validation:

```bash
go test ./internal/bench/...
```

### Task 8: Suite Governance and Holdout Policy

Files:

- Create or modify `internal/bench/suites`
- Test suite metadata validation

Implement:

- smoke suite metadata marks results as regression/sanity only
- public comparison suites require documented sampling metadata
- minimum task-count and uncertainty-reporting requirements for external claims
- `verk-native` split into development/regression and holdout sets
- holdout suites protected from routine tuning workflows

Validation:

```bash
go test ./internal/bench/...
```

### Task 9: Public Provider Spike

Files:

- Add one public provider behind an experimental flag

Implement:

- prefer Aider Polyglot smoke first because it is smaller
- document missing official harness pieces if any
- do not gate CI on public provider in the first version

Validation:

```bash
go test ./internal/bench/...
verk bench list
```

## Data and Contamination Rules

Public benchmarks may be contaminated in model training. Treat public scores as
comparability signals, not absolute truth.

Rules:

- Pin benchmark versions.
- Pin task ids.
- Pin repository commits and image digests.
- Lock the task manifest before execution.
- Snapshot the resolved worker, reviewer, and fallback profiles before
  execution.
- Store run metadata and `verk` commit.
- Require a clean worktree for comparable runs.
- Mark dirty override runs as exploratory, non-baselineable, and
  non-comparable.
- Keep private `verk-native` tasks out of public training channels.
- Keep locked holdout tasks out of routine implementation prompts and tuning
  loops.
- Rotate or add private canary tasks after major model releases.
- Avoid putting hidden expected fixes in prompts or ticket descriptions.

## Security and Isolation

Benchmark tasks should run in isolated workspaces.

Default policy:

- no secrets in benchmark environment
- no network unless task requires it
- bounded CPU/memory/time
- cleanup after each task
- captured stdout/stderr
- process group cancellation
- explicit allowlist for environment variables
- deterministic per-run and per-task workspace roots
- no shared mutable solution or verifier directories between matrix cells

Public benchmark providers that require Docker or network access must declare
that requirement in suite metadata.

### Workspace and Cache Isolation

The runner must treat solution workspaces, verifier workspaces, and caches as
separate resources.

Rules:

- Each benchmark run and task gets a deterministic isolated workspace root.
- Matrix cells cannot share mutable solution or verifier directories.
- Shared caches are read-only by default.
- Mutable shared caches require advisory locking with clear timeout behavior.
- Cache keys include suite, provider, runtime, model, benchmark mode, and
  relevant config dimensions.
- Stale lock recovery must be deterministic and visible in logs.
- Reports record cache mode and cache reuse metadata when available.

### Anti-Cheating and Scope Rules

Benchmark tasks must protect the verifier and benchmark metadata from agent
edits.

Rules:

- The agent works in a mutable solution workspace.
- The verifier runs from an immutable verifier workspace or pinned container.
- The runner never applies agent changes directly to the verifier tree.
- Verification applies the filtered candidate patch only to a fresh verification
  checkout or provider-approved mutable copy.
- Benchmark metadata, expected outputs, hidden tests, and scoring scripts are
  read-only to the agent.
- Final patches are filtered to allowed repository paths.
- Patches that modify benchmark harness files, hidden tests, or task metadata
  are scored as `policy_blocked` or `patch_failed`.
- Network access defaults to off unless the suite declares it.
- Secrets and host credentials are never exposed to benchmark tasks.

For public benchmarks, preserve the official benchmark's own anti-cheating
rules and add `verk` scope checks on top.

## Cost Controls

Cost must be a first-class benchmark result.

Controls:

- max total run cost
- max task cost
- max profile cost
- concurrency limit
- stop-on-budget-exceeded
- dry-run estimate where possible
- cached-input token tracking
- repair/review budget limits

Reports should show:

- cost per solved task
- cost per attempted task
- tokens per solved task
- cached token ratio
- output token ratio
- retries and repair cycles that drove cost
- accounting quality: exact, derived, estimated, or unavailable
- pricing table version used for estimates
- whether headline cost deltas are exact-only or mixed-quality

### Usage and Cost Accounting

Different runtimes expose usage data differently. The benchmark runner should
store usage with provenance and confidence.

Usage record fields:

- runtime
- model
- reasoning
- role
- prompt/input tokens
- cached input tokens
- output tokens
- total tokens
- tool-call count, when available
- reported cost, when available
- estimated cost, when reported cost is unavailable
- pricing table version
- usage source: runtime API, CLI JSON, parsed log, or estimate
- confidence: exact, derived, estimated, or unavailable

Reports must distinguish exact usage from estimates. Cost comparisons should not
mix exact and estimated data without labeling it.

Headline cost tables should be exact-only when possible. Estimated and derived
costs should appear in separate sections or be clearly marked as
non-comparable to exact cost. Unavailable usage is reported as unavailable,
never as zero.

## Reporting Requirements

Every report should include:

- benchmark run id
- `verk` git commit
- dirty/clean state and comparability state
- benchmark suite/version
- matrix id
- locked task manifest id
- resolved profile snapshot id
- benchmark mode and valid claim scope
- environment summary
- total tasks
- solved tasks
- solve rate
- raw solve rate
- flake-adjusted solve rate
- total cost
- total tokens
- total duration
- per-profile table
- per-task table
- new failures compared to baseline
- fixed failures compared to baseline
- adapter/runtime failure summary
- artifact completeness summary
- verifier-flaky summary
- cache mode and cache reuse summary
- exact/estimated/unavailable usage summary

Example table:

| Profile | Solved | Cost | Duration | Tokens | Repairs | Adapter Failures |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| claude-sonnet-high + claude-opus-xhigh | 8/10 | $12.40 | 42m | 1.2M | 3 | 0 |
| codex-high + codex-xhigh | 7/10 | $9.80 | 39m | 900k | 2 | 1 |

## Acceptance Criteria

The benchmark system is acceptable when:

1. It can run at least one `verk-native` smoke suite deterministically.
2. It can compare two runtime/model profiles on the same locked task manifest.
3. It records per-attempt runtime/model/reasoning and fallback reasons.
4. It can produce Markdown, JSON, and CSV reports.
5. It separates coding failures from adapter/runtime failures.
6. It tracks token usage and cost.
7. It supports at least one public coding benchmark provider.
8. It runs public coding benchmarks through `full-verk` mode by default.
9. It can compare a candidate run against a baseline.
10. It has clear budget and isolation controls.
11. It classifies failures into task, patch, setup, verifier, adapter, agent
    availability, budget, policy, or harness failures.
12. It protects benchmark metadata and verifiers from agent edits.
13. It labels exact, derived, estimated, and unavailable usage data separately.
14. It supports task-paired baseline comparisons.
15. It separates `full-verk` system results from controlled worker/model
    comparisons.
16. It prevents worker leaderboards unless reviewer and task-manifest controls
    are satisfied or the report is explicitly exploratory.
17. It freezes task manifests and resolved profile/fallback snapshots for
    comparable runs.
18. It rejects dirty worktrees for comparable runs and marks dirty overrides as
    non-baselineable.
19. It writes atomic checkpoints and a run-complete marker before allowing
    headline comparisons.
20. It reports `verifier_flaky` separately and shows raw plus flake-adjusted
    solve rates.
21. It isolates solution workspaces, verifier workspaces, and shared caches.
22. It distinguishes smoke, development/regression, and holdout suite results.

## Readiness Checklist

Before implementation starts:

- Role-based model config from `ver-laq2` is either implemented or the benchmark
  runner has a temporary profile injection path.
- The first `verk-native-smoke` tasks are selected and small enough for local
  execution.
- The first benchmark mode is chosen. Recommended: `full-verk` for
  `verk-native-smoke`.
- CI cost and runtime budgets are agreed.
- Public provider adoption order is agreed.
- The implementation is split into tickets no larger than the MVP tasks above.
- Benchmark claim rules are accepted: smoke is regression-only, full-verk is a
  system benchmark by default, and controlled worker/model comparisons require
  fixed reviewer or factorial design.
- Dirty-worktree, cache, verifier-isolation, and flake policies are accepted
  before any baseline is published.

## Open Questions

1. How much private `verk-native` data should be checked into the repo versus
   stored outside the repository?
2. Should reports include model-judge qualitative summaries if deterministic
   scoring is already available?
3. Which public provider should be implemented first if Aider Polyglot has
   integration friction: SWE-bench Multilingual smoke or another small coding
   suite?
4. Should benchmark runs be resumable independently of normal `verk run` resume,
   or is normal run resume sufficient for the first version?

## Recommendation

Build in this order:

1. `verk-native-smoke`
2. role profile matrix and per-attempt metadata
3. report and compare commands
4. Aider Polyglot smoke provider
5. SWE-bench Multilingual subset provider
6. expanded public coding benchmark support
7. deferred runtime-only benchmark support if it becomes useful later

This gives fast feedback first, then coding comparability, then terminal/runtime
coverage only when we explicitly decide to invest in it. The near-term priority
is full-pipeline coding outcomes, not terminal-only agent skill.

## References

- [SWE-bench Multilingual](https://www.swebench.com/multilingual)
- [Aider Polyglot announcement](https://aider.chat/2024/12/21/polyglot.html)
- [SWE-bench paper](https://juanmirod.github.io/public/papers/swe-bench_2310.06770v3.pdf)
