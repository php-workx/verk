# Ticket State Machine Outcomes

This document explains how verk classifies ticket execution states, what each
outcome means, and how the CLI surfaces these states to operators.

## State Overview

Ticket state is tracked at two levels:

- **Ticket store status** (in `.tickets/<id>.md` frontmatter): coarse,
  scheduler-facing state used to determine what work can be claimed.
- **Run outcome** (in `.verk/runs/<run-id>/tickets/<id>/ticket-run.json`):
  fine-grained per-run result set when automation stops or completes.

### Ticket Store Status

| Status | Meaning |
| --- | --- |
| `ready` | Claimable; no active run owns this ticket. |
| `in_progress` | Actively owned by a running ticket run. |
| `blocked` | Cannot be scheduled until an external condition changes. |
| `closed` | Implementation and verification complete; no further work needed. |

`open` is accepted as a legacy alias for unscheduled work.

### Run Outcome

The `outcome` field in `ticket-run.json` is the precise reason a ticket stopped:

| Outcome | Meaning |
| --- | --- |
| `closed` | Ticket met all closeout requirements. |
| `failed_retryable` | Automation failed, but verk has enough reliable state to retry automatically. |
| `needs_decision` | Automation stopped because the next step requires an operator choice. |
| `blocked` | No useful retry exists until an external condition changes. |
| `cancelled` | Operator interrupted the run. |

An empty `outcome` field means the ticket is still running or the artifact
predates the outcome field (legacy). See [Backward Compatibility](#backward-compatibility)
for how legacy artifacts are handled.

## When Each Outcome Is Set

| Stop reason | Outcome |
| --- | --- |
| Ticket completed closeout phase | `closed` |
| Repair or implementation budget exhausted | `needs_decision` |
| Scope violation (files changed outside owned scope) | `needs_decision` |
| Worker signals it needs additional context | `blocked` |
| Claim renewal lost (lease expired) | `blocked` |
| Missing credentials, missing tools, impossible dependencies | `blocked` |
| Operator interrupted (Ctrl-C / SIGTERM) | `cancelled` |
| Any unclassified stop with no budget or context signal | `blocked` |

Formatting, linting, test, verification, and review failures are not terminal
on first failure. They enter the repair phase or become `failed_retryable`
while budget remains. Only after budgets are exhausted does a ticket escalate
to `needs_decision`.

## What `verk run` Does

### Non-Interactive Mode (CI / piped output)

When stdout or stdin is not a terminal, `verk run epic <id>` prints a
structured summary and exits non-zero. Tickets are grouped into three sections:

```
Retryable tickets:
  <ticket-id>: <reason>
  To retry:  verk reopen <run-id> <ticket-id> --to <phase>

Tickets needing decision:
  <ticket-id>: <reason>
  To retry:  verk reopen <run-id> <ticket-id> --to implement
  To retry:  verk reopen <run-id> <ticket-id> --to repair
  To block:  verk block <ticket-id> --reason "..."

Blocked tickets:
  <ticket-id>: <reason>

  verk run
```

**Retryable tickets** have `outcome=failed_retryable`. Verk printed the exact
reopen command; run it and then `verk run` to resume.

**Tickets needing decision** have `outcome=needs_decision`. You must choose
whether to retry from an earlier phase or mark the ticket explicitly blocked.
Verk will not retry these automatically.

**Blocked tickets** have `outcome=blocked`. They cannot progress until an
external condition changes (missing credentials, unresolvable dependency, etc.).

If no tickets can be reopened automatically:

```
Retry: no tickets can be reopened automatically
  resolve blockers or dependencies, then run verk run
```

### Interactive Mode (terminal attached)

When both stdin and stdout are attached to a terminal, `verk run epic <id>`
prints the same structured summary and then prompts the operator ticket by
ticket.

**Step 1: Needs-decision tickets.**

For each `needs_decision` ticket the operator sees a menu:

```
Decision for <ticket-id>: <reason>
  [1] retry from implement
  [2] retry from repair
  [3] leave as needs decision
  [4] mark blocked
  [s] stop
  Choice:
```

A single keypress selects the action. Pressing `s`, `Ctrl+C`, or `Ctrl+X`
stops the interaction (no tickets are reopened). Any unrecognised key repeats
the prompt.

**Step 2: Retryable tickets.**

After the decision prompts, the operator is asked, one ticket at a time,
whether to reopen each retryable ticket:

```
Select blocked tickets to reopen and retry (default=no):
  Reopen <ticket-id>? [y/N]
```

Press `y` to include the ticket, `n` or Enter to skip, `Ctrl+C` to cancel the
whole selection.

**Step 3: Automatic resume.**

If any tickets were selected, verk reopens them and immediately resumes the run
in-place. A successful resume exits 0.

## Backward Compatibility

Legacy `ticket-run.json` artifacts may have no `outcome` field (empty string).
When verk reads such a snapshot, it derives an outcome from the `current_phase`
field using `ticketOutcomeForPhase`:

| Legacy `current_phase` | Derived outcome |
| --- | --- |
| `closed` | `closed` |
| `blocked` | `blocked` |
| any active phase | `""` (still running) |

This ensures old artifacts behave identically to how they did before the
outcome field was introduced. In particular, a legacy `blocked` snapshot is
treated as `outcome=blocked` and is not automatically retried unless the
operator explicitly supplies a `--to` phase via `verk reopen`.

`DefaultReopenTargetForSnapshot` drives automatic retry target selection. For
an empty outcome it falls back to `DefaultReopenTargetForPhase`, which uses
phase-based rules that predate this feature. For explicit outcomes it uses the
outcome rules described in the table above.

## Rollout Notes

- `TicketPhaseBlocked` remains readable in existing artifacts and is not
  removed. New stop sites set `currentOutcome` on `ticketRunState` directly so
  the snapshot carries a precise outcome alongside the legacy phase.
- Ticket store status (`blocked` in markdown) is never sufficient on its own to
  produce an automatic retry command. A trusted run snapshot with a retryable
  outcome is always required.
