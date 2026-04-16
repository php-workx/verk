# verk v1 Sub-Agent Persona Reviews

Review target: `specs/initial_v1.md`

Each section below is the output of a fresh-context sub-agent using only the technical plan and that persona's own prompt.

## Distributed-Systems / Workflow-Engine Reviewer

### No authoritative reconciliation rule for live vs durable claims
- Severity: critical
- Why it matters: Resume and status can reconstruct a ticket from the wrong owner or lose it entirely after a crash. The spec lets live `.tickets/.claims/` outrank durable snapshots for acquisition, but also requires status to be derived from run artifacts and active claims, so two implementers can legitimately make different recovery decisions when the files diverge.
- Evidence from the plan: “If the two diverge, the engine must treat `.tickets/.claims/` as live truth for acquisition and `.verk` claim snapshots as the durable run record.” Also: “resume must scan claims owned by the run” and “status must be derivable entirely from run artifacts and active claims.”
- Recommended change: Define one explicit reconciliation procedure for resume and status computation: compare live claim, durable claim, and `lease_id`; block the run on owner mismatch; and specify exactly when a durable snapshot may be rebuilt from a live claim versus treated as corruption.

### Lease expiry and reacquisition are not fenced against late worker results
- Severity: critical
- Why it matters: A ticket can keep running after its lease expires, another run can reacquire the claim, and both can produce outputs. The plan says expired claims may be reacquired and lease loss is a blocking failure, but it never says how to reject late results from the old worker, so duplicate execution can be accepted nondeterministically.
- Evidence from the plan: “default lease TTL is 10 minutes,” “active tickets renew their lease every 3 minutes,” “expired claims become acquirable only after rereading current disk state,” “lease loss during execution is a blocking failure for the ticket,” and “expired claims must be treated as stale and reacquired if available.”
- Recommended change: Add a lease-fence invariant: every worker/reviewer/verifier result must carry the current `lease_id`, and the engine must reject all results once the claim snapshot changes or the lease expires. Stale claims should not be reacquired until the prior execution is explicitly marked failed and quiescent.

### Multi-artifact state updates have no transactional commit protocol
- Severity: high
- Why it matters: State transitions span multiple files, but the plan only promises atomic writes per file. A crash between updating `run.json`, ticket phase artifacts, and wave artifacts can leave durable state internally contradictory, and the resume rules do not define which file is authoritative.
- Evidence from the plan: “single-writer model for run state,” “atomic write-via-temp-and-rename for persisted artifacts,” and the minimum artifact set across `run.json`, `waves/`, `tickets/`, and `claims/` with no journal or commit marker.
- Recommended change: Specify a per-transition commit order and a source-of-truth map, such as artifact-first plus a final commit marker or pointer update. Resume must read only that committed source and treat partially written sidecars as incomplete transition attempts.

### Reopen behavior is incomplete for wave and epic state
- Severity: medium
- Why it matters: The plan defines reopen targets for tickets, but only one reopen case updates wave state. Reopening a blocked ticket can leave the containing wave and epic in an ambiguous state, and two implementers can disagree on whether the old wave is revived, left failed, or replaced.
- Evidence from the plan: Allowed reopen targets are “`blocked -> implement`,” “`blocked -> repair`,” and “`closed -> repair`,” but the only explicit wave rule is “reopening a closed ticket also reopens its accepted wave and marks that wave `failed_reopened`.”
- Recommended change: Define exact wave and run transitions for every reopen target, including whether the ticket re-enters its prior wave, creates a new wave, or forces the epic back to `running`/`blocked`. Persist that decision in run and wave artifacts so resume cannot infer it differently.

### Review-threshold precedence is underspecified
- Severity: medium
- Why it matters: Closeout behavior can differ depending on whether a ticket-level `review_threshold` overrides the global policy default. One implementation may close a ticket with unresolved P3 findings while another may treat the policy default as authoritative, producing inconsistent closure decisions for the same ticket.
- Evidence from the plan: Ticket fields include `review_threshold`; config defaults include `policy.review_threshold: P2`; closeout says “no open review findings remain at or above the configured threshold.”
- Recommended change: State the precedence explicitly and persist the effective threshold in the ticket’s plan/closeout artifacts, for example: ticket field overrides policy, absent ticket field inherits policy, CLI flags override both.

### Scope reconciliation can misattribute dirty-worktree changes
- Severity: medium
- Why it matters: Post-execution scope checks rely on `git`-derived changed files, but the plan never requires a clean worktree or a pinned diff base before a run starts. Pre-existing local changes can be blamed on the ticket, or real scope violations can be masked, depending on which `git` view the implementation uses.
- Evidence from the plan: “actual changed files must be collected from git,” the git adapter includes “status, diff, changed files, branch/base, file conflict checks,” and `verk doctor` only checks “git worktree visibility,” not cleanliness or baseline capture.
- Recommended change: Require a clean worktree at run start or record an explicit base commit for every run/wave and compute changed files strictly against that base. Reject runs started on dirty worktrees unless the dirty set is recorded and excluded from ticket attribution.

## Senior Go Systems Implementer / Storage-Contract Reviewer

### Missing claim release lifecycle and terminal claim state
- Severity: high
- Why it matters: The lease model defines acquisition and renewal, but not release. That leaves resume and wave-acceptance behavior ambiguous when a claim should be dropped, deleted, or tombstoned. Different implementations will diverge on whether a released claim is still resumable, still visible in status, or immediately reacquirable.
- Evidence from the plan: Claim semantics only define `ticket_id`, `owner_run_id`, `owner_wave_id`, `lease_id`, `leased_at`, `expires_at`, acquisition, and renewal; wave acceptance also requires that all wave claims be released before acceptance is recorded.
- Recommended change: Define an explicit release operation and terminal claim state. If claims are tombstoned instead of deleted, add `released_at` and `release_reason`, and specify how live `.tickets/.claims/` files and durable `.verk` snapshots are updated on release.

### Ticket `status` is underspecified and not mapped to engine phases
- Severity: high
- Why it matters: The engine needs a deterministic readiness predicate for epic scheduling and resume, but the plan never defines the valid ticket-store status values or how they relate to internal phases. That will produce inconsistent implementations of `ready`, `blocked`, and `unclaimed` ticket selection.
- Evidence from the plan: The ticket frontmatter includes `status`; epic execution queries “ready, unclaimed child tickets” and wave rules say only ready tickets enter waves, but no status enum or readiness predicate is defined.
- Recommended change: Define the allowed ticket-store statuses, the exact readiness predicate, and the mapping between ticket markdown `status` and internal engine phase transitions. Make one of them canonical and state when the other is derived.

### Implementation artifact cannot persist required failure reasons
- Severity: high
- Why it matters: The plan requires structured reasons for `needs_context` and `blocked`, but `implementation.json` has no dedicated fields for them. That pushes engineers to overload `concerns` or `artifacts`, which breaks resume logic and makes failure classification inconsistent.
- Evidence from the plan: Implementer handling explicitly says to persist a structured reason for `needs_context` and runtime-provided structured block reason for `blocked`; the required `implementation.json` fields are only `attempt`, `runtime`, `status`, `completion_code`, `concerns`, `changed_files`, `artifacts`, `started_at`, and `finished_at`.
- Recommended change: Add explicit fields such as `failure_reason` / `block_reason` and, if needed, `retry_class` or `termination_class`. Require them whenever implementation does not end in a clean success state.

### Review threshold and severity policy lack a single precedence contract
- Severity: medium
- Why it matters: Closure gating depends on review severity thresholds, but the plan defines both ticket-level and config-level thresholds without stating which one wins or how `P0` through `P4` compare formally. That will create divergent closeout behavior across teams.
- Evidence from the plan: Ticket metadata includes `review_threshold`; the closure gate blocks on unresolved `P0`, `P1`, or `P2` equivalent findings; config defaults also set `policy.review_threshold: P2`.
- Recommended change: Define the ordering of `P0` to `P4`, the threshold comparison rule, and the precedence between ticket metadata, config, and engine defaults in one place.

### Path normalization and scope-overlap rules are incomplete
- Severity: medium
- Why it matters: Epic scheduling and scope violation checks depend on `owned_paths`, but the plan does not define a canonical normalization algorithm. Without it, implementations will disagree on trailing slashes, `.`/`..`, symlinks, and platform case sensitivity, causing false conflicts or missed overlaps.
- Evidence from the plan: `owned_paths` is limited to file paths or directory prefixes; pre-wave scheduling uses normalized repo-relative clean paths and exact/shared-prefix overlap rules.
- Recommended change: Specify one canonical normalization algorithm and state exactly how symlinks, separators, case, and directory boundaries are handled before overlap checks.

### Closeout failure reporting is not typed enough for audit/debugging
- Severity: medium
- Why it matters: The plan requires closeout to identify the exact failed gate and the artifact or finding that blocked closure, but the closeout artifact schema only names a generic `failed_gate` and an undefined `gate_results` blob. That is not enough for stable persistence or reliable status reconstruction.
- Evidence from the plan: The closure gate requires the engine to say exactly which gate failed and which artifact or finding blocked closure; `closeout.json` only lists `criteria_evidence`, `required_artifacts`, `gate_results`, `closable`, and `failed_gate`.
- Recommended change: Make `gate_results` a typed structure with per-gate status, blocked artifact references, blocked finding IDs, and a human-readable reason field.

### Config schema is incomplete beyond a few defaults
- Severity: medium
- Why it matters: `pkg/verk` and the CLI need a stable YAML contract, but the plan only names the top-level sections and a few defaults. `runtime` and `logging` are not specified at all, so engineers will invent incompatible config layouts.
- Evidence from the plan: The config contract lists only `scheduler`, `policy`, `runtime`, `verification`, and `logging` sections; the only concrete values are a handful of defaults and the env allowlist mentions “explicitly configured runtime auth variables” without a schema for them.
- Recommended change: Publish an explicit YAML schema for every section, including runtime selection, auth env vars, worker/reviewer settings, verification defaults, and logging fields.

## Rigid QA / Adversarial Verification Reviewer

### No executable reopen path for blocked or closed tickets
- Severity: high
- Why it matters: The plan defines reopen semantics, but there is no CLI or API path to invoke them. That leaves operators with manual `run.json` edits, which is not auditable and can bypass transition validation or wave state updates.
- Evidence from the plan: “Operator reopen rules: reopening is explicit and must be recorded in `run.json`” and “CLI surface: v1 CLI commands: `verk run ticket <ticket-id>`, `verk run epic <ticket-id>`, `verk resume <run-id>`, `verk status <run-id>`, `verk doctor`.”
- Recommended change: Add an explicit `verk reopen <run-id> <ticket-id> --to <phase>` command or equivalent engine API, require transition validation and audit logging, and add tests for reopening blocked and closed tickets plus wave re-failure on reopen.

### Closeout evidence can be fabricated or reused across runs
- Severity: critical
- Why it matters: `criteria_evidence[]` has no provenance strong enough to prove the evidence came from the current ticket execution. A stale note, copied summary, or evidence from another run can satisfy the schema and let a ticket close without proving current work.
- Evidence from the plan: “closeout must persist `criteria_evidence[]`” and “each evidence entry must contain: `criterion_id`, `criterion_text`, `evidence_type`, `source`, `summary`.”
- Recommended change: Require each evidence entry to carry immutable provenance such as `run_id`, `ticket_id`, `attempt`, and an artifact hash or exact artifact path, and reject evidence reused from a different run. Add a test that copied evidence from a previous run cannot satisfy closeout.

### Review pass/fail is self-asserted instead of derived from findings
- Severity: critical
- Why it matters: The plan persists both `findings` and `passed`, but never says the engine must recompute `passed` from normalized findings. A malformed or dishonest reviewer artifact can set `passed: true` while still containing unresolved P0/P1/P2 findings, producing a false-green closeout.
- Evidence from the plan: `review-findings.json` includes `findings`, `blocking_findings`, and `passed`; the closure policy blocks on unresolved `P0`, `P1`, or `P2` findings.
- Recommended change: Make `passed` a derived field, validate it against the normalized findings and disposition rules, and reject any contradictory review artifact. Add a test where `passed: true` plus an open P1 must still fail closeout.

### Verification pass/fail is not pinned to command-level execution data
- Severity: critical
- Why it matters: `verification.json` can be populated with a summary and `passed` flag, but the plan does not require the per-command exit code, timeout status, cwd, or stdout/stderr artifact references needed to audit or recompute the result. That allows a failed command to be summarized as passing.
- Evidence from the plan: `verification.json` includes `commands`, `results`, and `passed`; verification rules say non-zero exit or timeout is a failed verification result.
- Recommended change: Persist each command’s exact invocation, exit code, timeout flag, cwd, and stdout/stderr artifact paths, and require `passed` to be derived from those recorded results. Add a test that a non-zero verification command cannot be recorded as passing.

### Claim recovery is ambiguous when live and durable claim state diverge
- Severity: high
- Why it matters: The plan makes live `.tickets/.claims/` authoritative for acquisition and `.verk/runs/<run-id>/claims/` the durable run record, but it does not define deterministic reconciliation for mismatches. That can produce duplicate execution, lost work, or unrecoverable runs after crashes.
- Evidence from the plan: The plan says live claims are truth for acquisition and durable snapshots are the durable run record, and separately says `resume` must scan claims owned by the run.
- Recommended change: Specify exact reconciliation behavior for live-present/durable-missing, durable-present/live-missing, and divergent lease IDs, then add resume tests for each case.

### Waived review findings can bypass closeout without any waiver policy
- Severity: high
- Why it matters: The plan allows `waived` dispositions, but does not require approver identity, justification, expiry, or a separate audit record. A blocking finding can be waived informally and the ticket can still close, which weakens the closeout gate.
- Evidence from the plan: `disposition` may be `open`, `resolved`, or `waived`; closure blocks only on unresolved findings at or above the threshold.
- Recommended change: Require waiver authorization metadata for any waived finding at or above the closure threshold, include the waiver in closeout evidence, and add a test that a waived P1 without an approved waiver record does not permit closure.
