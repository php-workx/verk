# Ticket Quality Gate

Verk runs a deterministic ticket quality gate before dispatching any
worker. The gate evaluates each ticket against a fixed taxonomy of
lint rules and blocks the run if any finding is at or above the
configured severity threshold (default P2).

## Commands

```bash
verk inspect ticket <ticket-id>             # lint a single ticket
verk inspect ticket <ticket-id> --fix       # apply safe repairs
verk inspect epic <ticket-id>               # lint an epic + children
verk inspect epic <ticket-id> --fix         # apply safe repairs across the epic
```

`verk run epic <id>` and `verk run ticket <id>` run the gate automatically
before the first worker is dispatched.

## Finding Codes

| Code | Default Severity | Meaning |
| --- | --- | --- |
| missing_acceptance_criteria | P1 | Ticket has no criteria, test cases, or validation commands. |
| ambiguous_acceptance_criterion | P2 | Criterion uses vague wording without an observable. |
| compound_acceptance_criterion | P3 | Multiple independently verifiable requirements packed into one line. |
| missing_validation_commands | P2 | No declared command and no obvious derived check. |
| missing_owned_paths | P1 | Implementation ticket has no scope. |
| owned_path_missing | P2 | Owned path does not exist and is not clearly new. |
| dependency_missing | P1 | Dependency id does not exist. |
| dependency_blocked_or_closed_mismatch | P2 | Dependency/status relationship inconsistent. |
| missing_public_contract_scenario | P1 | CLI/API ticket lacks black-box command scenario. |
| missing_negative_case | P2 | Validation/error-handling lacks failure-path expectation. |
| docs_descope_risk | P1 | Docs appear to remove planned behavior without a plan update. |
| integration_gap | P1 | Multi-surface epic missing integration ticket. |
| plan_traceability_gap | P2 | Ticket references plan but does not link the requirement. |
| reviewer_instruction_gap | P3 | Risky ticket lacks reviewer guidance. |

## Safe Auto-Repair

`verk inspect ... --fix` and (when policy.ticket_quality.auto_fix_safe is true)
`verk run` may apply these repairs:

- set an epic's owned_paths to the sorted union of child owned_paths,
  only when every child has non-empty owned_paths
- record planner-required findings as ticket_quality_notes frontmatter
  so future runs surface the open issue

Unsafe repairs are never applied silently. The gate never invents
acceptance criteria, public CLI scenarios, or test expectations; it
never splits compound criteria or rewrites docs body text.

## Resolving a Blocked Gate

1. Run `verk inspect epic <id>` or `verk inspect ticket <id>`.
2. Read the findings list. The artifact lives at
   `.verk/runs/<run-id>/ticket-quality.json` for full detail.
3. Apply safe repairs with `--fix`. Operator-required findings
   (acceptance criteria, public contract scenarios) need a human edit.
4. Re-run.

## Learning Loop

Lessons captured from past escaped defects can be promoted into
additional planner-review context. That mechanism is intentionally
separate; see the memory learning loop plan.
