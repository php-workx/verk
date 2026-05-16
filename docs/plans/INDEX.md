# Plans Index

This index is the quick map for planned, active, and reference design work. It
is intentionally an index, not a second source of truth: update the owning plan
or ticket first, then update this file.

## Status Legend

- Active: has open tickets or is currently being implemented.
- Implemented: feature track has landed; document is retained as the durable
  design/reference.
- Planned: design exists, but no active implementation was found.
- Blocked: design exists, but another plan or capability must land first.
- Reference: baseline or support spec that other plans build on.
- Gap: repeatedly identified need, but no dedicated plan was found.

## Reference Specs

| Area | Status | Document | Related tickets | Notes |
| --- | --- | --- | --- | --- |
| Core engine v1 | Reference | [done/initial_v1.md](done/initial_v1.md) | historical baseline | Deterministic engine, phase state machine, artifacts, claims, policy contract, and scope enforcement. |
| Validation coverage artifacts | Reference / Implemented | [validation-coverage.md](validation-coverage.md) | `ver-vyag`, `ver-rcgh`, `ver-y29o`, `ver-1qru`, `ver-ssp3` | Durable record for declared, derived, executed, skipped, repaired, and blocking checks. |
| Worker isolation | Reference / Implemented | [worker-isolation.md](worker-isolation.md) | `ver-wi0p`, `ver-wi01` through `ver-wi22` | Per-ticket git worktrees for parallel worker isolation, merge-back, verification CWD split, and cleanup. |

## Feature Tracks

| Feature area | Status | Primary document | Related tickets | Scope |
| --- | --- | --- | --- | --- |
| Repair-oriented run gates | Implemented / Reference | [2026-04-19-verk-run-repair-oriented-gates.md](2026-04-19-verk-run-repair-oriented-gates.md) | `ver-vyag`, `ver-rcgh`, `ver-laq2`, `ver-y29o`, `ver-1qru`, `ver-tidw`, `ver-amsh`, `ver-ssp3`, `ver-bks9`, `ver-mbvz`, `ver-aw4j` | Ticket, wave, and epic closeout prefer repair over early blocking and surface actionable blocker reasons. |
| Implementation and verification loop improvements | Active / Partially implemented | [2026-04-19-impl-verify-improvements.md](2026-04-19-impl-verify-improvements.md) | overlaps `ver-vyag` | Broader roadmap for the impl -> verify -> review -> repair loop. Standards, profiles, resolution evidence, and epic review pieces exist; intent and compiled-constraint work still need a dedicated completion pass. |
| Per-worker review diffs | Implemented / Reference | [2026-04-20-per-worker-review-diffs.md](2026-04-20-per-worker-review-diffs.md) | no dedicated epic found in this index pass | Reviewers inspect the current worker attempt's delta instead of the whole dirty worktree. |
| Recursive sub-epic execution | Implemented / No standalone plan | no dedicated plan found | `ver-vmgr` and children | Closed remediation track for recursive sub-epic review findings; a future standalone plan is only needed for new recursive execution scope. |
| High-severity review findings | Implemented / No standalone plan | no dedicated plan found | `ver-wgxh` and children | Closed remediation track for claim safety, lease handling, runtime status normalization, verification environment behavior, and epic acceptance propagation. |
| Benchmarking | Active / Planned | [2026-04-19-benchmark-adoption-and-creation.md](2026-04-19-benchmark-adoption-and-creation.md) | `ver-g9p2` and children | Public/private benchmark strategy, reproducibility, verifier integrity, flake taxonomy, cost accounting, and suite governance. |
| Verk as skill | Planned | [2026-04-19-verk-as-skill-cross-agent.md](2026-04-19-verk-as-skill-cross-agent.md) | no active epic found in this index pass | Claude Code skill-mode foundation for verk primitives and artifact-compatible execution. |
| Skill host portability | Blocked | [2026-04-19-verk-skill-host-portability.md](2026-04-19-verk-skill-host-portability.md) | no active epic found in this index pass | Extend skill-mode support beyond Claude Code after the v1 skill surface is available. |
| Ticket quality pre-run gate | Implemented | [2026-04-21-ticket-quality-gate.md](2026-04-21-ticket-quality-gate.md) | no active epic found in this index pass | Deterministic ticket lint, planner-role review, traceability checks, and safe auto-repair for underspecified tickets. User-facing reference: [../ticket-quality-gate.md](../ticket-quality-gate.md). |
| Ticket state machine outcomes | Implemented | [2026-04-22-ticket-state-machine.md](2026-04-22-ticket-state-machine.md) | no dedicated epic yet | Separate retryable failures, operator decisions, and true blockers so `blocked` becomes a last-resort state instead of a generic stop reason. User-facing reference: [../ticket-state-machine.md](../ticket-state-machine.md). |
| Memory learning loop | Implemented | [2026-04-21-memory-learning-loop.md](2026-04-21-memory-learning-loop.md) | no active epic found in this index pass | Repo-local escaped-defect memory, human-reviewed lesson promotion, and advisory feedback into ticket quality review. User-facing reference: [../memory-learning-loop.md](../memory-learning-loop.md). |
| Anti-rationalization catalog | Planned | [Rationalizations.md](Rationalizations.md) | none | Detailed spec for P3 (impl-verify-improvements). Full catalog of 91 named rationalizations + verk-specific additions, with injection point mapping per worker phase. |
| Agent profiles | Implemented / Reference | [2026-04-21-agent-profiles.md](2026-04-21-agent-profiles.md) | none | Role-based worker profiles (security-engineer, contract-engineer, frontend-engineer, backend-engineer). Project-agnostic detection, `profile` frontmatter field, pre-run validation, rationalization injection per profile, and prompt placement. Full implementation of P3. |

## Priority And Parallelism

This section is intentionally coarse. Revisit it when a major plan lands or a
new escaped defect changes the risk profile.

### Current Priority Order

1. Close the benchmark implementation track from
   [2026-04-19-benchmark-adoption-and-creation.md](2026-04-19-benchmark-adoption-and-creation.md),
   especially reproducibility, manifest/profile freezing, verifier integrity,
   flake accounting, cache/workspace isolation, and cost provenance.
2. Finish the remaining active pieces of
   [2026-04-19-impl-verify-improvements.md](2026-04-19-impl-verify-improvements.md),
   especially intent echo policy wiring/resume behavior and any compiled-constraint
   promotion work not covered by the implemented profile and review-gate pieces.
3. Implement the Claude Code foundation in
   [2026-04-19-verk-as-skill-cross-agent.md](2026-04-19-verk-as-skill-cross-agent.md),
   starting with the localhost HTTP daemon.
4. Keep [2026-04-19-verk-skill-host-portability.md](2026-04-19-verk-skill-host-portability.md)
   blocked until the v1 skill surface is real enough to audit against other
   host capabilities.

### Parallel Work Guidance

Safe parallel tracks:

- Independent benchmark provider/reporting subtasks that do not share writer
  files.
- Intent echo adapter/prompt work, provided engine phase wiring is serialized
  with active `ticket_run.go` changes.
- Daemon state-file/startup-lock/auth work for the skill-mode plan, before
  touching shared engine orchestration.
- Documentation and report rendering updates for implemented tracks.

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
- Dedicated standalone plans for the now-closed `ver-vmgr` and `ver-wgxh`
  remediation tracks were never created. Leave them as no-standalone-plan
  history unless new recursive sub-epic or high-severity remediation scope
  opens again.

## Maintenance Rules

- Add new feature plans here when they are created.
- Prefer linking an existing plan or ticket over creating a duplicate plan.
- Keep status coarse; this file should not mirror every child ticket state.
- Move review prompts and completed historical specs out of the active-plan list
  instead of mixing them with implementation plans.
