# RPI Phase 2 Summary: `specs/initial_v1.md`

Date: 2026-04-02
Phase: Implementation
Mode: document
Status: DONE

## Changes Applied

Updated `specs/initial_v1.md` to close the remaining discovery gaps:

- added canonical v1 ticket-store status enum and readiness rules
- defined status mapping between `.tickets/` scheduling state and `.verk` execution state
- defined claim release operation, release metadata, and terminal claim state
- tightened stale claim reacquisition semantics
- added `lease_id` fencing to normalized runtime results
- added missing artifact schema fields:
  - `base_commit`
  - `wave_base_commit`
  - `effective_review_threshold`
  - `audit_events`
  - implementation failure/block fields
- added typed closeout gate reporting
- tightened verification derivation rules and per-command result fields
- added explicit `verk reopen <run-id> <ticket-id> --to <phase>` CLI contract

## Outcome

The implementation phase for this document-mode RPI run was direct spec refinement. No code implementation was attempted in this phase.
