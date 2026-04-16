---
id: pre-mortem-2026-04-02-verk-v1-implementation
type: pre-mortem
date: 2026-04-02
source: "[[.agents/plans/2026-04-02-verk-v1-implementation.md]]"
---

# Pre-Mortem: verk v1 implementation

## Council Verdict: WARN

| Judge | Verdict | Key Finding |
|-------|---------|-------------|
| Missing-Requirements | WARN | The plan still does not include a concrete issue for implementing real Codex and Claude adapters beyond scaffolding and contracts. |
| Feasibility | WARN | Some later-wave work still depends on behavior that is only specified at interface level, especially runtime integration and manual verification setup. |
| Scope | PASS | The wave structure is disciplined, and the new `tk` hierarchy mirrors it cleanly without obvious scope creep. |
| Spec-Completeness | WARN | The plan is largely executable, but a few acceptance/conformance checks remain too weak for the highest-risk areas. |

## Shared Findings

- The `tk` hierarchy is structurally correct and improves execution tracking, but it does not resolve the main implementation-risk gaps in the plan itself.
- The plan is still missing one explicit issue for real runtime adapter implementation. It creates `internal/adapters/runtime/codex/adapter.go` and `internal/adapters/runtime/claude/adapter.go` as scaffolds in the file inventory, but no issue owns completing those adapters to the level required by the spec.
- The acceptance criteria for runtime integration are still too weak relative to the risk. The current conformance checks verify that type definitions and engine functions exist, but they do not prove Codex/Claude adapters can launch workers, launch reviewers, and return normalized fenced results.
- The manual verification flow remains underspecified for a greenfield repo. The plan includes `go run ./cmd/verk doctor` and `go run ./cmd/verk status test-run --json`, but it does not define the minimum fixture setup needed for these commands to succeed or fail in a controlled, reproducible way.
- The cross-wave registry is useful and the `tk` wave epics now mirror it, but the plan should still state the execution rule explicitly: later waves must branch from the latest merged SHA of the previous wave.

## Concerns Raised

- **Missing concrete runtime implementation issue**
  - Why it matters: The spec promises v1 support for Codex and Claude runtimes. The plan currently covers shared runtime contracts and fake adapters, but not the real adapter behavior needed for production execution.
  - Recommendation: Add a dedicated issue after Issue 5 and before engine execution that implements both real runtime adapters, with acceptance criteria covering worker launch, reviewer launch, normalized result mapping, retry classification, and lease ID propagation. Mirror that issue into the `tk` hierarchy as its own ticket rather than burying it under another engine task.

- **Conformance checks are weakest where risk is highest**
  - Why it matters: The plan is strongest on file inventory and structure, but runtime integration, claim fencing, and closeout derivation depend on behavior, not file existence.
  - Recommendation: Add command- or test-based conformance checks for:
    - real runtime adapter behavior
    - lease-fence rejection
    - cross-run evidence rejection
    - reopen command side effects on wave/run artifacts
  - Recommendation detail: the `tk` tickets should reference these stronger checks in their acceptance text or notes so execution tracking matches the plan’s real verification burden.

- **Manual verification section needs tighter reproducibility**
  - Why it matters: The current manual verification block creates minimal config, but not the ticket/run artifacts needed to exercise core flows meaningfully.
  - Recommendation: Extend the manual verification section with one full ticket fixture and one resume fixture under `testdata/manual/`, then define the expected output for `doctor`, `status --json`, and one failed closeout case.

- **Wave handoff language should be made more operational**
  - Why it matters: The plan already identifies cross-wave shared files, and the `tk` wave epics now reflect the sequencing, but the execution rule is still implicit rather than mandatory.
  - Recommendation: Add one sentence under `Execution Order` or `Cross-Wave Shared Files`: “Every new wave must branch from the latest merged SHA of the previous wave; no later wave may execute from the original repo baseline.”

## Recommendation

Address the warnings before implementation. The `tk` execution structure is good, but the plan is still not quite strong enough to start `/crank` without risking a false start around runtime integration and verification strength.

## Decision Gate

- [ ] PROCEED - Council passed, ready to implement
- [x] ADDRESS - Fix concerns before implementing
- [ ] RETHINK - Fundamental issues, needs redesign
