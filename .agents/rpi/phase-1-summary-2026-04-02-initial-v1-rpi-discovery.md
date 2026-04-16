# RPI Phase 1 Summary: `specs/initial_v1.md`

Date: 2026-04-02
Phase: Discovery
Mode: document
Verdict: WARN

## Research

Reviewed the current `specs/initial_v1.md` plus the prior three persona-based review outputs in `docs/reviews/2026-04-02-subagent-persona-reviews.md`.

The spec had already incorporated most reviewer findings, but discovery found a few remaining implementation-readiness gaps:

- ticket-store `status` existed without a canonical readiness contract
- claim release semantics were still implicit
- durable/live claim reconciliation was improved but still needed an explicit release operation and tighter stale-claim wording
- `base_commit`, `wave_base_commit`, and `effective_review_threshold` were referenced by policy text but not yet fully reflected in artifact schemas

## Plan

Apply a narrow hardening pass to the spec itself:

- define canonical ticket-store statuses and readiness mapping
- define claim release operation and terminal claim state behavior
- tighten stale claim reacquisition wording
- add missing artifact fields for baseline commits, effective threshold, and audit events
- preserve the current product direction and avoid introducing sidecar design docs

## Pre-mortem

Main anticipated failure mode:

- implementers diverge on scheduling, claim cleanup, and schema completeness despite the spec appearing “done”

Mitigation applied in Phase 2:

- convert implicit behaviors into explicit normative rules in the spec

Pre-mortem verdict:

- `WARN`

Reason:

- the document was close to implementation-ready but still had enough remaining ambiguity to justify one more spec-hardening pass before calling it ready
