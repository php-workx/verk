# Plans Index

This index is the quick map for planned, active, and reference design work. It
is intentionally an index, not a second source of truth: update the owning plan
or ticket first, then update this file.

## Status Legend

- Active: has open tickets or is currently being implemented.
- Planned: design exists, but no active implementation was found.
- Blocked: design exists, but another plan or capability must land first.
- Reference: baseline or support spec that other plans build on.
- Gap: repeatedly identified need, but no dedicated plan was found.

## Reference Specs

| Area | Status | Document | Related tickets | Notes |
| --- | --- | --- | --- | --- |
| Core engine v1 | Reference | [done/initial_v1.md](done/initial_v1.md) | historical baseline | Deterministic engine, phase state machine, artifacts, claims, policy contract, and scope enforcement. |
| Validation coverage artifacts | Reference / Active | [validation-coverage.md](validation-coverage.md) | `ver-vyag`, `ver-rcgh`, `ver-y29o`, `ver-1qru`, `ver-ssp3` | Durable record for declared, derived, executed, skipped, repaired, and blocking checks. |
| Worker isolation | Active | [worker-isolation.md](worker-isolation.md) | `ver-wi0p`, `ver-wi01` through `ver-wi18` | Per-ticket git worktrees for parallel worker isolation, merge-back, verification CWD split, and cleanup. |

## Feature Tracks

| Feature area | Status | Primary document | Related tickets | Scope |
| --- | --- | --- | --- | --- |
| Repair-oriented run gates | Active | [2026-04-19-verk-run-repair-oriented-gates.md](2026-04-19-verk-run-repair-oriented-gates.md) | `ver-vyag`, `ver-rcgh`, `ver-laq2`, `ver-y29o`, `ver-1qru`, `ver-tidw`, `ver-amsh`, `ver-ssp3`, `ver-bks9`, `ver-mbvz`, `ver-aw4j` | Ticket, wave, and epic closeout should prefer repair over early blocking and should surface actionable blocker reasons. |
| Implementation and verification loop improvements | Planned / Reference | [2026-04-19-impl-verify-improvements.md](2026-04-19-impl-verify-improvements.md) | overlaps `ver-vyag` | Broader roadmap for the impl -> verify -> review -> repair loop, including intent echo, standards, validators, and reviewer gates. |
| Per-worker review diffs | Active / Planned | [2026-04-20-per-worker-review-diffs.md](2026-04-20-per-worker-review-diffs.md) | no dedicated epic found in this index pass | Reviewers should inspect the current worker attempt's delta instead of the whole dirty worktree. |
| Recursive sub-epic execution | Active | no dedicated plan found | `ver-vmgr` and children | Make sub-epics resumable, retryable, artifact-backed, depth-limited by scheduler policy, and safe when descendants block. |
| High-severity review findings | Active | no dedicated plan found | `ver-wgxh` and children | Remediate claim safety, lease handling, runtime status normalization, verification environment behavior, and epic acceptance propagation. |
| Benchmarking | Active / Planned | [2026-04-19-benchmark-adoption-and-creation.md](2026-04-19-benchmark-adoption-and-creation.md) | `ver-g9p2` and children | Public/private benchmark strategy, reproducibility, verifier integrity, flake taxonomy, cost accounting, and suite governance. |
| Verk as skill | Planned | [2026-04-19-verk-as-skill-cross-agent.md](2026-04-19-verk-as-skill-cross-agent.md) | no active epic found in this index pass | Claude Code skill-mode foundation for verk primitives and artifact-compatible execution. |
| Skill host portability | Blocked | [2026-04-19-verk-skill-host-portability.md](2026-04-19-verk-skill-host-portability.md) | no active epic found in this index pass | Extend skill-mode support beyond Claude Code after the v1 skill surface is available. |
| Ticket quality pre-run gate | Planned | [2026-04-21-ticket-quality-gate.md](2026-04-21-ticket-quality-gate.md) | no active epic found in this index pass | Needed before `verk run`: deterministic ticket lint, planner-role review, traceability checks, and safe auto-repair for underspecified tickets. |
| Memory learning loop | Planned | [2026-04-21-memory-learning-loop.md](2026-04-21-memory-learning-loop.md) | no active epic found in this index pass | Repo-local escaped-defect memory, human-reviewed lesson promotion, and advisory feedback into ticket quality review. |
| Anti-rationalization catalog | Planned | [Rationalizations.md](Rationalizations.md) | none | Detailed spec for P3 (impl-verify-improvements). Full catalog of 91 named rationalizations + verk-specific additions, with injection point mapping per worker phase. |
| Agent profiles | Planned | [2026-04-21-agent-profiles.md](2026-04-21-agent-profiles.md) | none | Role-based worker profiles (security-engineer, contract-engineer, frontend-engineer, backend-engineer). Project-agnostic detection, `profile` frontmatter field, pre-run validation, rationalization injection per profile, and prompt placement. Full implementation of P3. |

## Priority And Parallelism

This section is intentionally coarse. Revisit it when a major plan lands or a
new escaped defect changes the risk profile.

### Current Priority Order

1. Stabilize active execution correctness: close `ver-wgxh` high-severity
   findings and `ver-vmgr` recursive sub-epic execution issues first.
2. Finish the core repair-oriented run gates in
   [2026-04-19-verk-run-repair-oriented-gates.md](2026-04-19-verk-run-repair-oriented-gates.md),
   especially derived verification, repair routing, visible blocker reasons,
   and epic closure behavior.
3. Land reviewer-scope correctness: prioritize
   [2026-04-20-per-worker-review-diffs.md](2026-04-20-per-worker-review-diffs.md),
   then [worker-isolation.md](worker-isolation.md).
4. Implement the deterministic and planner-reviewed ticket quality gate from
   [2026-04-21-ticket-quality-gate.md](2026-04-21-ticket-quality-gate.md).
5. Add the advisory memory and learning loop from
   [2026-04-21-memory-learning-loop.md](2026-04-21-memory-learning-loop.md).
6. Revisit benchmarking, skill packaging, and host portability after the core
   run and quality gates are stable.

### Parallel Work Guidance

Safe parallel tracks:

- `ver-wgxh` high-severity remediation.
- `ver-vmgr` recursive sub-epic execution.
- Per-worker review diffs.
- Deterministic ticket-quality evaluator and inspection CLI, before wiring it
  into `verk run`.
- Memory loop storage and CLI skeleton, while keeping it advisory until ticket
  quality finding codes are stable.

Coordinate carefully or serialize when work touches shared execution files:

- `internal/engine/ticket_run.go`
- `internal/engine/epic_run.go`
- `internal/state/types.go`
- `internal/adapters/runtime/prompt.go`
- `internal/policy/config.go`
- `internal/cli/run.go`

Benchmarking, verk-as-skill, and skill host portability are useful tracks, but
they should not outrank core run correctness and quality gates.

## Review Materials

| Purpose | Document |
| --- | --- |
| Sub-agent persona reviews for the v1 engine plan | [../reviews/2026-04-02-subagent-persona-reviews.md](../reviews/2026-04-02-subagent-persona-reviews.md) |
| Distributed systems workflow-engine review prompt | [../review-prompts/2026-04-02-distributed-systems-workflow-engine-reviewer.md](../review-prompts/2026-04-02-distributed-systems-workflow-engine-reviewer.md) |
| Senior Go systems and storage-contract review prompt | [../review-prompts/2026-04-02-senior-go-systems-implementer-storage-contract-reviewer.md](../review-prompts/2026-04-02-senior-go-systems-implementer-storage-contract-reviewer.md) |
| Rigid QA adversarial verification review prompt | [../review-prompts/2026-04-02-rigid-qa-adversarial-verification-reviewer.md](../review-prompts/2026-04-02-rigid-qa-adversarial-verification-reviewer.md) |
| Adversarial harness engineer review prompt | [../review-prompts/2026-04-19-adversarial-harness-engineer-review-prompt.md](../review-prompts/2026-04-19-adversarial-harness-engineer-review-prompt.md) |
| Benchmark scientist review prompt | [../review-prompts/2026-04-19-benchmark-scientist-review-prompt.md](../review-prompts/2026-04-19-benchmark-scientist-review-prompt.md) |

## Known Planning Gaps

- Plan/ticket index automation: this file is manual today. A future command
  should derive most of it from `docs/plans/`, `.tickets/`, and ticket links.
- Dedicated plans for `ver-vmgr` and `ver-wgxh`: both are active epics with
  meaningful scope, but this pass did not find standalone plan documents.

## Maintenance Rules

- Add new feature plans here when they are created.
- Prefer linking an existing plan or ticket over creating a duplicate plan.
- Keep status coarse; this file should not mirror every child ticket state.
- Move review prompts and completed historical specs out of the active-plan list
  instead of mixing them with implementation plans.
