# Memory Learning Loop

Verk captures lessons from escaped defects (issues that slipped past the
ticket quality gate, reviewer, or validation) and lets operators promote
those lessons into reusable advisory rules. The loop is intentionally
human-reviewed: raw failed-run data never changes blocking behavior.

## What Counts as an Escaped Defect

An escaped defect is any issue you discover after a worker has already
been dispatched (or after a ticket has closed) that an earlier gate
should have caught. Examples:

- a CLI ticket missed a black-box command scenario and a worker
  shipped the wrong contract
- a docs ticket silently removed planned behavior because the gate
  didn't flag the descope
- reviewer accepted a ticket whose acceptance criterion was too vague
  to verify

## Recording a Lesson

```bash
verk learn escaped \
  --run <run-id> \
  --summary "..." \
  --missed-by reviewer,ticket_acceptance \
  --source-tickets ver-1234,ver-5678 \
  --recommended-rule "CLI tickets must include black-box command scenarios"
```

The lesson is appended to `.verk/memory/escaped-defects.jsonl` with
status `proposed`. Lessons are durable — they survive across sessions.

## Listing and Inspecting

```bash
verk learn list             # concise table
verk learn list --json      # structured output
verk learn show <lesson-id>
verk learn show <lesson-id> --json
```

## Promotion

```bash
verk learn promote <lesson-id> \
  --target ticket-quality-rule \
  --rule-id <rule-id>
```

Promotion:

- requires an explicit `--rule-id`
- marks the lesson status as `promoted` (append-only)
- appends a `PromotionEntry` to `.verk/memory/promoted-rules.jsonl`
- prints the exact rule id

Promotion does NOT edit ticket-quality code automatically. Promoted
rules are advisory: they appear as context in planner-role review
prompts and as P3 advisory findings, but never block a run by
themselves. Blocking behavior changes require a code change to the
ticket quality gate.

## Why Promotion Is Human-Reviewed

Auto-promoting raw failure signal into blocking gates would cause:

- false positives from noisy or one-off incidents
- silent expansion of blocking behavior outside operator awareness
- harder-to-debug gate failures with no provenance

A human-reviewed step preserves the operator's ability to weigh signal
strength, see exactly which lessons turn into rules, and reject ones
that are too narrow to generalize.

## Relationship To The Ticket Quality Gate

The ticket quality gate (see [docs/ticket-quality-gate.md](./ticket-quality-gate.md))
runs deterministic lint rules. Promoted memory rules add advisory
context to the planner-review prompt. Together they form a feedback
loop:

1. Gate misses a defect → operator records a lesson with `verk learn escaped`.
2. After review, operator promotes the lesson with `verk learn promote`.
3. Future planner-review prompts see the lesson as context.
4. If the lesson becomes a hard rule, a code change adds it to the
   deterministic finding taxonomy.

## Storage Layout

```text
.verk/memory/escaped-defects.jsonl
.verk/memory/promoted-rules.jsonl
```

Both are append-only JSONL. Status updates write a new record with the
same `id` and updated status; readers deduplicate using last-record-wins.

Whether `.verk/memory/` is committed is a per-project choice. Operators
who want shared lessons should commit it; operators who want
machine-local lessons should add it to `.gitignore`.

## Committing Memory

The `.verk/memory/` directory holds append-only JSONL files. The tradeoff:

| Choice | Commit to repo | Add to `.gitignore` |
|---|---|---|
| **Shared lessons** | Lessons are visible to all team members and survive machine changes. | — |
| **Per-machine lessons** | — | Lessons stay local; no risk of leaking sensitive incident context. |
| **Hybrid** | Commit only `promoted-rules.jsonl` (curated). Ignore `escaped-defects.jsonl` (raw). | — |

### Sample `.gitignore` entries

**Ignore everything in memory (per-machine):**
```gitignore
# verk memory — machine-local only
.verk/memory/
```

**Commit promoted rules, ignore raw defects:**
```gitignore
# verk memory — keep curated rules, ignore raw escaped defects
.verk/memory/escaped-defects.jsonl
```

**Commit everything (shared team lessons):**
```gitignore
# verk memory — intentionally committed; remove this block to share lessons
# (nothing to ignore)
```

> Tip: because both files are append-only JSONL, merge conflicts are
> rare — each line is a self-contained JSON object. `git` will surface
> a conflict only if two branches appended records in the exact same
> byte offset, which is unlikely in practice.
