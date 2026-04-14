# verk v1: Deterministic Engineering Execution Engine

## Summary

`verk` is a standalone Go project that executes engineering tickets and epics through a deterministic state machine. It is not a skill-first workflow runner. The core product is an engine plus adapters, with CLIs and skills as thin wrappers.

v1 combines four proven ideas:

- **Hybrid Ralph execution contract**: fresh worker context every attempt or wave, scheduler-heavy engine, disk-backed continuity, atomic work passes, and hard backpressure before advancement.
- **Ticket ratchet**: every ticket runs through `intake -> implement -> verify -> review -> repair -> closeout` until it is closure-ready or blocked.
- **Epic wave scheduler**: ready tickets are grouped into non-conflicting waves and run in parallel up to a configured concurrency limit.
- **Direct `tk`-compatible ticket storage**: `verk` reads and writes markdown tickets directly through a Go store adapter, with no shell dependency on the `tk` CLI.

v1 is engineering-only, supports Codex and Claude runtimes, uses Git plus deterministic command verification, and requires one fresh-context independent review before ticket closure.

## Core Architecture

### Product shape

Build `verk` as a new standalone Go repo with:

- `cmd/verk`
  - CLI entrypoint
- `internal/engine`
  - ticket loop orchestration
  - epic wave orchestration
  - transition guards
  - closure gate
- `internal/state`
  - run, wave, and ticket artifact types
  - state transition validation
  - atomic file persistence helpers
- `internal/policy`
  - retry policy
  - review severity thresholds
  - closure rules
  - conflict and concurrency policy
- `internal/adapters/ticketstore/tkmd`
  - direct `tk`-compatible markdown+frontmatter store
  - dependency graph queries
  - claim/lease support
- `internal/adapters/runtime/codex`
- `internal/adapters/runtime/claude`
  - worker launch
  - fresh-context reviewer launch
  - normalized task status/output
- `internal/adapters/repo/git`
  - status, diff, changed files, branch/base, file conflict checks
- `internal/adapters/verify/command`
  - deterministic command execution and result capture
- `pkg/verk`
  - exported engine API for later Fabrikk/Fabric embedding

### Naming and filesystem layout

Use `verk` consistently in binary, package, and artifact naming.

Persist all execution state under:

- `.verk/runs/<run-id>/run.json`
- `.verk/runs/<run-id>/waves/wave-<n>.json`
- `.verk/runs/<run-id>/tickets/<ticket-id>/plan.json`
- `.verk/runs/<run-id>/tickets/<ticket-id>/implementation.json`
- `.verk/runs/<run-id>/tickets/<ticket-id>/verification.json`
- `.verk/runs/<run-id>/tickets/<ticket-id>/review-findings.json`
- `.verk/runs/<run-id>/tickets/<ticket-id>/closeout.json`
- `.verk/runs/<run-id>/tickets/<ticket-id>/cycles/repair-<n>.json`
- `.verk/runs/<run-id>/claims/claim-<ticket-id>.json`

State must be resumable from these artifacts alone. Worker chat/session memory must never be required for recovery.

The `.tickets/.claims/` store is the live coordination surface for lock acquisition. The `.verk/runs/<run-id>/claims/` directory is the durable run-local claim snapshot required for resume, auditability, and status reconstruction. If the two diverge, the engine must treat `.tickets/.claims/` as live truth for acquisition and `.verk` claim snapshots as the durable run record.

### Hybrid Ralph contract

`verk` must treat Ralph as a hard execution invariant:

- every implementation or review worker is fresh-context and ephemeral
- the engine is the only long-lived orchestrator
- workers perform one scoped unit of work and terminate
- the engine persists state, chooses next actions, and reconciles outputs
- advancing the loop requires deterministic gates, not worker self-assertion

`verk` is intentionally **not** a pure Ralph shell loop. It adds first-class wave scheduling, claims, deterministic closure gates, and structured repair cycles on top of the Ralph core.

## Execution Model and Interfaces

### Ticket inner loop

Every ticket runs these phases:

1. `intake`
2. `implement`
3. `verify`
4. `review`
5. `repair`
6. `closeout`

Required behavior:

- `intake` normalizes ticket metadata into an execution plan:
  - acceptance criteria
  - explicit test cases
  - validation commands
  - likely file scope
  - review threshold
- `implement` launches a fresh worker against the ticket scope
- `verify` runs deterministic checks from ticket and policy
- `review` launches one fresh-context independent reviewer in v1
- `repair` is entered only when review returns blocking findings
- `closeout` succeeds only when all closure rules pass

Implementer result vocabulary is fixed:

- `done`
- `done_with_concerns`
- `needs_context`
- `blocked`

The engine must react deterministically to these values. Workers do not decide whether the ticket is complete.

Canonical ticket phase enum:

- `intake`
- `implement`
- `verify`
- `review`
- `repair`
- `closeout`
- `closed`
- `blocked`

Allowed transitions:

- `intake -> implement`
- `implement -> verify`
- `implement -> blocked`
- `verify -> review`
- `verify -> implement`
- `verify -> blocked`
- `review -> closeout`
- `review -> repair`
- `review -> blocked`
- `repair -> verify`
- `repair -> blocked`
- `closeout -> closed`
- `closeout -> repair`
- `closeout -> blocked`

Forbidden transitions:

- any transition from `closed`
- `blocked -> *` without explicit operator reopen
- direct `implement -> closeout`
- direct `verify -> closeout`

Closeout failure routing:

- unresolved blocking review findings discovered at closeout:
  - `closeout -> repair`
- missing evidence, missing required artifacts, failed declared checks, or malformed closeout data:
  - `closeout -> blocked`
- `repair` is therefore reserved for review-driven remediation only

Deterministic engine handling for implementer results:

- `done`:
  - persist implementation result
  - transition to `verify`
- `done_with_concerns`:
  - persist implementation result plus normalized concerns
  - transition to `verify`
- `needs_context`:
  - transition to `blocked`
  - persist structured reason `needs_context`
- `blocked`:
  - transition to `blocked`
  - persist runtime-provided structured block reason

### Epic outer loop

An epic run is a wave scheduler over child tickets:

1. query ready, unclaimed child tickets
2. exclude blocked or conflict-overlapping tickets
3. build a wave of non-overlapping tickets
4. dispatch tickets in parallel up to configured concurrency
5. require each ticket to complete its full inner loop
6. run wave acceptance
7. checkpoint wave state
8. repeat until all children are closed or the epic is blocked

Waves must be computed from actual dependency and conflict data, not from ad hoc prompt decisions.

Canonical epic run statuses:

- `running`
- `waiting_on_leases`
- `blocked`
- `completed`

Canonical wave statuses:

- `planned`
- `running`
- `accepted`
- `failed`
- `failed_reopened`

### Ticket store contract

Use `.tickets/` markdown files with YAML frontmatter as the canonical work graph. `verk` must read and update them directly.

v1 ticket fields must support at least:

- `id`
- `title`
- `status`
- `deps`
- `priority`
- `acceptance_criteria`
- `test_cases`
- `validation_commands`
- `owned_paths`
- `review_threshold`
- optional runtime/model preferences

Canonical v1 ticket-store status enum:

- `open`
- `ready`
- `in_progress`
- `blocked`
- `closed`

Ticket readiness rules:

- a ticket is `ready` only when:
  - all `deps` are `closed`
  - ticket-store `status` is `ready`
  - the ticket has no active live claim owned by another run
- a ticket is not schedulable when `status` is `open`, `in_progress`, `blocked`, or `closed`
- epic wave selection must use this readiness predicate, not ad hoc interpretation

Status mapping rules:

- ticket-store `status` is the canonical scheduling status for `.tickets/`
- engine phase is the canonical execution status under `.verk`
- the engine must update ticket-store `status` on phase transitions that change schedulability:
  - claim acquisition -> `in_progress`
  - terminal block -> `blocked`
  - successful closeout -> `closed`
  - reopen of blocked ticket -> `ready` unless operator explicitly reopens into immediate execution

Unknown frontmatter and body content must round-trip unchanged.

v1 ticket markdown rules:

- markdown body must round-trip exactly
- unknown frontmatter must survive read/write
- frontmatter key order must be stable on write
- absent optional fields must be omitted, not written as null
- `owned_paths` is required for epic scheduling
- `owned_paths` accepts exact file paths or directory prefixes only in v1
- glob patterns in `owned_paths` are invalid and must fail intake deterministically
- no alternate scope field is valid in v1; `owned_paths` is the only scheduling scope input

The store must support exclusive claims with lease renewal so parallel workers cannot race the same ticket.

Claim records live under:

- `.tickets/.claims/<ticket-id>.json`
- `.verk/runs/<run-id>/claims/claim-<ticket-id>.json`

Claim fields:

- `ticket_id`
- `owner_run_id`
- `owner_wave_id`
- `lease_id`
- `leased_at`
- `expires_at`
- `released_at`
- `release_reason`
- `state`

Claim identifier safety:

- `ticket_id` and `run_id` must be validated before any path construction
- identifiers containing path separators, dot-dot sequences (`..`), or absolute-path prefixes must be rejected
- validation must apply at every claim entry point (acquire, renew, release, reconcile), not just one helper

Lease semantics:

- default lease TTL is 10 minutes
- fresh claims schedule renewal at one-third of the total lease TTL
- resumed claims schedule their first renewal based on remaining time (`expires_at - now`), not the original full TTL, so short-remaining claims renew before expiry
- renewal is valid only from the current `lease_id`
- acquisition uses atomic create
- renewal uses atomic compare-and-replace
- expired claims become acquirable only after rereading current disk state
- lease loss during execution is a blocking failure for the ticket
- claim `state` must be one of:
  - `active`
  - `released`
- released claims are tombstoned, not silently deleted, in the durable `.verk` snapshot
- live `.tickets/.claims/` entries for released claims may be deleted only after durable snapshot update completes
- every worker, reviewer, and verifier result must carry the current `lease_id`
- the engine must reject any result whose `lease_id` does not match the current live claim and durable snapshot
- expired claims must not be reacquired until the engine records the previous execution as failed, blocked, or abandoned

Crash and resume semantics:

- `resume` must scan claims owned by the run
- unexpired claims must be resumed
- expired claims must be treated as stale candidates and may be reacquired only after the engine records the previous execution as failed, blocked, or abandoned
- malformed claim files are terminal store errors
- every live claim mutation must also update the corresponding durable claim snapshot under `.verk/runs/<run-id>/claims/`

Claim release operation:

- claim release must occur on:
  - successful closeout
  - terminal block
  - wave cleanup after failure
  - startup or setup failure before engine ownership is established (prevents tickets from being blocked until lease expiry after transient startup errors)
- release must:
  - persist `released_at`, `release_reason`, and `state=released` to the durable claim snapshot
  - remove or tombstone the live claim record only after the durable snapshot update succeeds

Claim reconciliation procedure:

- for each ticket, `resume` and `status` must compare:
  - live claim record under `.tickets/.claims/`
  - durable claim snapshot under `.verk/runs/<run-id>/claims/`
  - persisted `lease_id` references in ticket and run artifacts
- reconciliation cases:
  - live present, durable present, same `lease_id`:
    - treat as valid active claim
  - live present, durable missing:
    - rebuild durable snapshot from live claim only if `owner_run_id` matches the run and the ticket is not terminal
  - durable present, live missing, durable state `released`:
    - treat as released claim
  - durable present, live missing, durable state `active`:
    - treat as stale live-claim loss and block the run for operator review unless the ticket is terminal
  - live present and durable present with mismatched `owner_run_id` or `lease_id`:
    - treat as claim divergence and block the run with a deterministic corruption/conflict reason

### Runtime adapter contract

Both Codex and Claude adapters must normalize to one engine shape:

- launch worker
- launch fresh reviewer
- wait for completion
- return normalized:
  - status
  - stdout/stderr or result artifact
  - structured completion code
  - retryable vs terminal failure signal

The engine must not assume equal runtime capabilities. Capability differences must be explicit.

Shared normalized runtime result fields:

- `status`
- `completion_code`
- `retry_class`
- `stdout_path`
- `stderr_path`
- `result_artifact_path`
- `started_at`
- `finished_at`
- `lease_id`

Implementer adapter output enum:

- `done`
- `done_with_concerns`
- `needs_context`
- `blocked`

Completion code normalization rules:

- adapters must normalize raw runtime output to the canonical enum before returning to the engine
- hyphenated and underscored variants of the same code must both map to the same canonical value (e.g., `needs-more-context` and `needs_more_context` must both normalize to `needs_context`)
- the engine must never receive a raw un-normalized completion code; any unknown value after normalization is a terminal adapter error
- Codex and Claude adapters must apply the same normalization logic to produce identical canonical outputs for identical raw inputs

Reviewer adapter output fields:

- `review_status`: `passed` or `findings`
- `summary`
- `findings`

Reviewer artifact validity rules:

- `review_status` is derived from normalized findings, not trusted as self-asserted truth
- `passed` in persisted review artifacts must be recomputed by the engine from:
  - finding severities
  - dispositions
  - effective review threshold
- any contradiction between derived pass/fail and runtime-reported pass/fail is a blocking artifact error

Runtime retry classes:

- `retryable`
- `terminal`
- `blocked_by_operator_input`

Reviewer outputs must normalize to one severity enum:

- `P0`
- `P1`
- `P2`
- `P3`
- `P4`

Each normalized finding must include:

- `id`
- `severity`
- `title`
- `body`
- `file`
- `line`
- `disposition`
- `disposition` must be one of:
  - `open`
  - `resolved`
  - `waived`

Waiver rules:

- `waived` is valid only with waiver metadata:
  - `waived_by`
  - `waived_at`
  - `waiver_reason`
  - optional `waiver_expires_at`
- any waived finding at or above the effective review threshold must include waiver metadata and be referenced in `closeout.json`
- missing waiver metadata is a blocking closeout failure

### Closure gate

A ticket is closable only if all are true:

- every acceptance criterion is mapped to explicit evidence
- all required verification commands passed
- required artifacts exist
- independent fresh review completed
- no open review findings remain at or above the configured threshold
- any declared doc/config/schema checks also passed

Default v1 policy:

- block closeout on any unresolved `P0`, `P1`, or `P2` equivalent finding

If closeout fails, the engine must say exactly which gate failed and which artifact or finding blocked closure.

v1 severity policy:

- unresolved `P0`, `P1`, or `P2` findings block closeout
- unresolved `P3` and `P4` findings do not block closeout by default

Evidence model:

- every acceptance criterion must have a stable `criterion_id`
- closeout must persist `criteria_evidence[]`
- each evidence entry must contain:
  - `criterion_id`
  - `criterion_text`
  - `evidence_type`
  - `source`
  - `summary`
  - `run_id`
  - `ticket_id`
  - `attempt`
  - exact artifact path or immutable artifact hash
- accepted `evidence_type` values in v1:
  - `verification`
  - `artifact`
  - `diff`
  - `review`
- any acceptance criterion without at least one evidence entry fails closeout
- evidence from a different `run_id` must not satisfy closeout for the current run

Determinism note:

- verification commands themselves may depend on external systems when explicitly configured
- in v1, determinism means the engine's state transitions, retry behavior, gating, and persisted outcomes are deterministic given the recorded verification and review artifacts
- v1 does not guarantee that verification commands are reproducible across machines or time unless the operator configures them to be

### CLI surface

v1 CLI commands:

- `verk run ticket <ticket-id>`
- `verk run epic <ticket-id>`
- `verk reopen <run-id> <ticket-id> --to <phase>`
- `verk resume <run-id>`
- `verk status <run-id>`
- `verk doctor`

`resume` must continue from durable run state, not restart from scratch. `status` must be derivable entirely from run artifacts and active claims.

CLI output contract:

- `verk status <run-id>` human output must include:
  - run status
  - current wave if any
  - per-ticket phase and status
  - active claims
  - last failed gate if any
- `verk status <run-id> --json` must emit machine-readable run state plus computed claim snapshot
- `verk doctor` must check:
  - repo root detection
  - ticket store accessibility
  - claim directory health
  - JSON artifact parseability
  - configured runtime availability
  - git worktree visibility
- `verk doctor` exit codes:
  - `0`: healthy
  - `1`: recoverable warnings
  - `2`: blocking configuration or store failure
- `verk reopen <run-id> <ticket-id> --to <phase>` must:
  - validate the requested reopen target against reopen rules
  - append an audit event to `run.json`
  - update affected ticket, wave, and run state atomically under the transition commit rules

## Deterministic State, Concurrency, and Failure Rules

### State mutation model

All persisted run state is engine-owned. Workers must never mutate run artifacts directly.

Rules:

- single-writer model for run state
- atomic write-via-temp-and-rename for persisted artifacts
- corruption detection for malformed JSON artifacts
- claim acquisition and release are serialized through the ticket store adapter
- resumed runs must detect stale claims and expired leases deterministically

Transition commit rules:

- each engine transition must write sidecar artifacts before advancing the durable pointer in `run.json`
- `run.json` is the transition commit marker for run-level resume
- commit order for ticket transitions:
  1. write or update ticket-local artifact
  2. write or update claim snapshot if claim state changed
  3. write or update wave artifact if wave state changed
  4. write final `run.json` update with new `current_phase`, cursor, and audit event
- if a crash occurs before `run.json` is updated, resume must treat sidecar changes as incomplete transition attempt data
- if a crash occurs after `run.json` is updated, resume must treat the transition as committed and repair any missing derived summaries from committed sidecars only

All run artifacts are JSON and must carry:

- `schema_version`
- `run_id`
- object-local identifier such as `ticket_id` or `wave_id`
- `created_at`
- `updated_at`

Minimum required persisted artifacts:

- `run.json`
  - `mode`
  - `root_ticket_id`
  - `status`
  - `current_phase`
  - `policy`
  - `config`
  - `wave_ids`
  - `ticket_ids`
  - `base_branch`
  - `base_commit`
  - `resume_cursor`
  - `audit_events`
- `waves/wave-<n>.json`
  - `ordinal`
  - `status`
  - `ticket_ids`
  - `planned_scope`
  - `actual_scope`
  - `acceptance`
  - `wave_base_commit`
  - `started_at`
  - `finished_at`
- `tickets/<ticket-id>/plan.json`
  - `phase`
  - `acceptance_criteria`
  - `test_cases`
  - `validation_commands`
  - `owned_paths`
  - `review_threshold`
  - `effective_review_threshold`
  - `runtime_preference`
- `tickets/<ticket-id>/implementation.json`
  - `attempt`
  - `runtime`
  - `status`
  - `completion_code`
  - `retry_class`
  - `concerns`
  - `failure_reason`
  - `block_reason`
  - `changed_files`
  - `artifacts`
  - `lease_id`
  - `started_at`
  - `finished_at`
- `tickets/<ticket-id>/verification.json`
  - `attempt`
  - `commands`
  - `results`
  - `passed`
  - `repo_root`
  - `started_at`
  - `finished_at`
- `tickets/<ticket-id>/review-findings.json`
  - `attempt`
  - `reviewer_runtime`
  - `summary`
  - `findings`
  - `blocking_findings`
  - `passed`
  - `effective_review_threshold`
- `passed` in `review-findings.json` is derived, not authoritative
- `tickets/<ticket-id>/closeout.json`
  - `criteria_evidence`
  - `required_artifacts`
  - `gate_results`
  - `closable`
  - `failed_gate`
- `tickets/<ticket-id>/cycles/repair-<n>.json`
  - `cycle`
  - `trigger_finding_ids`
  - `input_review_artifact`
  - `repair_notes`
  - `verification_artifact`
  - `review_artifact`
  - `status`
  - `started_at`
  - `finished_at`
- `claims/claim-<ticket-id>.json`
  - `ticket_id`
  - `owner_run_id`
  - `owner_wave_id`
  - `lease_id`
  - `leased_at`
  - `expires_at`
  - `released_at`
  - `release_reason`
  - `state`
  - `last_seen_live_claim_path`

Typed closeout gate reporting:

- `failed_gate` must be one of:
  - `criteria_evidence`
  - `verification`
  - `required_artifacts`
  - `review`
  - `declared_checks`
  - `artifact_integrity`
- `gate_results` must be a typed map keyed by gate name
- each gate result must contain:
  - `status`: `passed` or `failed`
  - `reason`
  - `artifact_paths`
  - `finding_ids`

### Concurrency policy

Parallelism is allowed only when two tickets do not overlap in claimed scope.

v1 scheduling rules:

- no two tickets in the same wave may claim overlapping file ownership
- claimed scope violations discovered after execution are blocking failures
- wave concurrency is bounded by config
- fresh workers are created per wave; no worker persists across waves
- completed waves are ratcheted and never re-executed unless explicitly reopened

Conflict detection rules:

- pre-wave scheduling uses declared `owned_paths` as authoritative scope
- all scope comparison is against normalized repo-relative clean paths
- file-to-file overlap conflicts on exact match
- file-to-directory overlap conflicts when the file is under the directory prefix
- directory-to-directory overlap conflicts on shared prefix
- empty `owned_paths` is invalid for epic execution and must fail intake

Canonical path normalization rules:

- normalize all candidate paths to repo-relative slash-separated clean paths
- reject paths that escape the repo root after cleaning
- remove trailing slashes except for internal normalization before prefix comparison
- directory prefix matching must respect path-segment boundaries
- symlinks must be resolved against the repo worktree before comparison
- case sensitivity must follow the underlying filesystem, but persisted normalized paths must preserve canonical repo spelling

Post-execution reconciliation:

- actual changed files must be collected from git
- any changed file outside declared `owned_paths` is a scope violation
- a scope violation fails the ticket and fails wave acceptance

Git baseline rules:

- every run must record a `base_branch` and `base_commit`
- every wave must record a `wave_base_commit`
- actual changed files for scope reconciliation must be computed against the recorded wave base, not the ambient worktree alone
- v1 default requires a clean worktree before `verk run ticket` or `verk run epic`
- if dirty-worktree operation is later allowed by policy, the pre-existing dirty set must be recorded and excluded from ticket attribution

Wave acceptance rules:

- a wave is accepted only if every ticket in the wave reaches `closed`
- any ticket ending `blocked` makes the wave fail
- any scope violation makes the wave fail
- all wave claims must be released before acceptance
- wave artifact persistence must succeed before acceptance is recorded

### Retry and repair policy

Verification failure:

- return to `implement` with failure evidence

Blocking review findings:

- create a repair cycle and return to `repair -> verify -> review`

`verk` must persist repair iteration count. v1 must define a default maximum repair depth. When the limit is exceeded, the ticket transitions to `blocked` with a structured non-convergence reason.

Runtime failures must be classified as:

- retryable
- terminal
- blocked-by-operator-input

The engine must never silently swallow worker, verifier, or reviewer failures.

Default retry limits in v1:

- implementation retries after verification failure: 3 attempts total
- repair cycles after blocking review findings: 2 cycles total
- retryable runtime failures: 2 retries before `blocked`

Repair-cycle persistence rules:

- each entry into `repair` creates the next `cycles/repair-<n>.json`
- each repair artifact must identify the triggering review findings
- resume must reconstruct in-progress repair state from the latest repair artifact plus current ticket phase
- missing or malformed repair artifacts during resume are blocking run-state corruption errors

Escalation behavior:

- repeated verification failure beyond limit -> `blocked` with reason `non_convergent_verification`
- repeated blocking review findings beyond limit -> `blocked` with reason `non_convergent_review`
- `blocked-by-operator-input` -> immediate `blocked`

Operator reopen rules:

- reopening is explicit and must be recorded in `run.json`
- allowed reopen targets:
  - `blocked -> implement`
  - `blocked -> repair`
  - `closed -> repair` only for operator-directed defect follow-up in the same run
- reopening a closed ticket also reopens its accepted wave and marks that wave `failed_reopened`
- reopening a blocked ticket in an epic must also:
  - move the containing epic from `blocked` to `running`
  - mark the prior wave `failed_reopened` if the ticket had already been assigned to a wave
  - require the reopened ticket to be rescheduled in a new wave, never silently injected back into a completed wave

### Verification contract

Deterministic verification rules:

- verification commands run from repo root
- commands run with a clean environment plus explicit allowlisted passthrough variables
- default timeout per command is 15 minutes
- non-zero exit or timeout is a failed verification result
- verification emits artifacts only; it never mutates run state directly
- `verification.json` pass/fail must be derived from recorded per-command results, not trusted as self-asserted truth

Per-command verification result fields:

- `command`
- `cwd`
- `exit_code`
- `timed_out`
- `duration_ms`
- `stdout_path`
- `stderr_path`
- `started_at`
- `finished_at`

v1 environment passthrough allowlist:

- `PATH`
- `HOME`
- `CI`
- explicitly configured runtime auth variables

### Config contract

v1 config file:

- `.verk/config.yaml`

Config sections:

- `scheduler`
- `policy`
- `runtime`
- `verification`
- `logging`

Minimum v1 config schema:

- `scheduler`
  - `max_concurrency`
- `policy`
  - `review_threshold`
  - `max_implementation_attempts`
  - `max_repair_cycles`
  - `allow_dirty_worktree`
- `runtime`
  - `default_runtime`
  - `worker_timeout_minutes`
  - `reviewer_timeout_minutes`
  - `auth_env_vars`
- `verification`
  - `default_timeout_minutes`
  - `env_passthrough`
- `logging`
  - `level`
  - `artifact_retention`

Default values:

- `scheduler.max_concurrency: 4`
- `policy.review_threshold: P2`
- `policy.max_implementation_attempts: 3`
- `policy.max_repair_cycles: 2`
- `verification.default_timeout_minutes: 15`

Precedence order:

1. CLI flags
2. `.verk/config.yaml`
3. engine defaults

Threshold precedence and ordering:

- severity ordering is total and fixed:
  - `P0` > `P1` > `P2` > `P3` > `P4`
- effective review threshold precedence is:
  1. CLI override if supported for the run
  2. ticket `review_threshold`
  3. `.verk/config.yaml` `policy.review_threshold`
  4. engine default
- the effective threshold must be persisted in `plan.json` and used for review and closeout derivation

## Test Plan

### Core state machine

Add tests that prove:

- invalid state transitions are rejected
- verify failure loops back to implement
- blocking review findings loop through repair
- closeout fails when any acceptance criterion lacks evidence
- repair depth limits block instead of looping forever
- resume continues from persisted state without redoing completed phases
- closeout routes to `repair` only for unresolved blocking review findings
- closeout routes to `blocked` for non-review gate failures

### Ticket store and claims

Add tests that prove:

- markdown tickets remain `tk`-compatible after read/write
- unknown frontmatter survives round trips
- body text survives metadata updates
- claim acquisition is exclusive
- lease renewal extends claims correctly
- stale claims are detected and recoverable after simulated crash
- malformed claim files fail deterministically
- lease loss during execution blocks the ticket
- durable `.verk` claim snapshots are updated on claim mutation
- claim release updates both live and durable claim state deterministically
- claim reconciliation handles live-only, durable-only, and mismatched `lease_id` cases
- stale worker results with outdated `lease_id` are rejected deterministically

### Wave scheduler

Add tests that prove:

- only ready tickets enter waves
- file-conflicting tickets are not co-scheduled
- non-conflicting tickets are co-scheduled up to concurrency limit
- completed waves checkpoint cleanly
- blocked tickets prevent false epic completion
- changed files outside declared `owned_paths` trigger scope violations
- empty `owned_paths` fails epic intake deterministically
- dirty worktrees are rejected or excluded according to recorded policy and baseline

### Runtime and review orchestration

Add tests that prove:

- Codex and Claude adapters normalize to the same result shape
- fresh review runs in isolated context
- worker status vocabulary is handled deterministically
- retryable and terminal failures take different engine paths
- implementer adapter outputs are restricted to the canonical enum
- reviewer findings normalize `P0` through `P4` plus valid dispositions only
- unresolved `P0` findings block closeout
- contradictory reviewer artifacts with `passed: true` and blocking findings fail deterministically
- waived blocking findings without waiver metadata fail closeout

### End-to-end

Add end-to-end tests for:

- single ticket happy path
- single ticket with review findings and one repair cycle
- single ticket that fails verification then recovers
- epic with multiple waves and no conflicts
- epic with conflict serialization
- crash/resume from a partially completed run
- closeout blocked by unresolved review findings
- reopen of a blocked ticket resumes from the specified target phase
- reopen of a closed ticket marks its accepted wave `failed_reopened`
- in-progress repair cycles resume from persisted repair artifacts
- late worker results with stale `lease_id` are rejected after claim reacquisition
- copied closeout evidence from a previous run does not satisfy current closeout
- live/durable claim divergence blocks resume until reconciled by defined rules

## Assumptions and Defaults

- Language: Go
- Repo shape: new standalone project
- Canonical work graph: `.tickets/` markdown via direct store adapter
- v1 runtimes: Codex and Claude
- Review model in v1: one mandatory fresh-context independent review
- Product scope in v1: engineering execution only, not general workflow automation
- Ralph mode: Hybrid Ralph, not pure shell-loop Ralph
- Skills and CLIs are wrappers over the engine, not the source of orchestration truth
- Later Fabrikk/Fabric integration must consume `pkg/verk`, not fork engine logic
