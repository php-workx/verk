# RPI Discovery Summary: `specs/initial_v1.md`

Date: 2026-04-02
Phase: Discovery
Verdict: WARN

## Scope

Assess the current spec for implementation readiness and identify where it needs:

- more research
- more backing
- more implementation direction

## Overall assessment

The spec is coherent at the product-intent level and already captures several strong invariants:

- engine-owned durable state
- fresh-context workers and reviewers
- deterministic ticket and epic loops
- direct markdown ticket storage

The main gap is not product vision. The main gap is operational precision. Several high-risk surfaces are named but not yet specified tightly enough to build without making product-shaping assumptions in code.

## Areas needing more research

1. `tk` compatibility details
   - The spec requires direct read/write of `.tickets/` markdown while preserving compatibility, but it does not define the exact frontmatter/body conventions, edge cases, or compatibility test corpus.

2. Claim and lease semantics
   - Exclusive claims with lease renewal are required, but lease TTLs, renewal ownership rules, recovery after crash, and split-brain prevention rules are not defined.

3. Runtime adapter capability differences
   - Codex and Claude are named as v1 runtimes, but the spec does not establish the minimal common denominator versus runtime-specific optional features.

4. Conflict detection accuracy
   - Scheduling depends on non-overlapping scope, but the spec does not define whether `owned_paths` is authoritative, advisory, glob-based, directory-based, or reconciled against actual changed files.

5. Review severity normalization
   - Closeout blocks on unresolved `P1`/`P2` equivalent findings, but the mapping from reviewer output to normalized severities is unspecified.

6. Deterministic verification environment
   - Verification commands are central to closure, but there is no decision yet on execution sandboxing, timeout policy, environment variables, or reproducibility expectations.

7. Ralph provenance and boundaries
   - The spec references a Hybrid Ralph contract as a hard invariant, but its exact source behavior and non-negotiable boundaries are not captured in a way an implementer can test against.

## Areas needing more backing

1. Standalone Go repo choice
   - The spec mandates a new standalone Go project, but does not justify this against alternatives such as embedding in an existing orchestrator or using a thinner runtime-specific system first.

2. One mandatory independent review in v1
   - This is plausible, but the cost/latency tradeoff is not backed by expected ticket volume, runtime cost, or failure-reduction evidence.

3. P1/P2 closeout threshold
   - Blocking on unresolved `P1`/`P2` findings is reasonable, but the spec needs backing for why that threshold is the right default and how it maps across reviewer types.

4. Wave scheduling as a first-class primitive
   - The spec asserts wave scheduling as core behavior, but does not justify expected throughput gains versus a simpler serialized execution model for v1.

5. Direct `tk`-compatible storage
   - The decision avoids a shell dependency, but the spec does not yet back the compatibility risk, maintenance cost, or migration story with examples.

6. `pkg/verk` as future embedding surface
   - Future Fabrikk/Fabric embedding is mentioned, but the required API stability and consumer needs are not described.

## Areas needing more implementation direction

1. State machine definition
   - The ticket phases are named, but the allowed transitions, entry/exit conditions, and persisted status enums are not fully enumerated.

2. Artifact schemas
   - The filesystem layout is defined, but the JSON schemas for `run.json`, `wave-<n>.json`, `plan.json`, `verification.json`, and related artifacts are missing.

3. Intake output contract
   - `intake` is described conceptually, but not as a concrete normalized data structure with required fields and validation rules.

4. Implementer result handling
   - The fixed worker vocabulary is listed, but the exact engine action for `done_with_concerns` and `needs_context` is still ambiguous.

5. Wave acceptance algorithm
   - The outer loop includes “run wave acceptance,” but no acceptance criteria are defined for a wave versus an individual ticket.

6. Reopen and ratchet semantics
   - Completed waves are “never re-executed unless explicitly reopened,” but reopen triggers, authority, and effects on ticket state are not specified.

7. Repair depth policy
   - The spec says v1 must define a default maximum repair depth, but does not define the number or escalation path.

8. Runtime adapter interfaces
   - The adapter responsibilities are listed, but request/response payloads, cancellation behavior, and artifact handoff are not.

9. `status` and `doctor` command behavior
   - CLI command names are defined, but expected outputs, machine-readable modes, and failure diagnostics are not.

10. Config model
   - The spec references configured concurrency and policy, but does not define a config file, precedence rules, or defaults source.

11. Evidence model
   - Closeout requires each acceptance criterion to map to explicit evidence, but the structure of that evidence map is not defined.

12. Verification failure and runtime failure routing
   - The retry classes are named, but attempt limits, backoff, and operator escalation rules are still open.

## Highest-priority follow-up questions

1. What exact `.tickets/` markdown format must round-trip without loss?
2. What is the canonical claim/lease model, including TTL, renewal, and stale-claim recovery?
3. What exact JSON schema does each persisted artifact use?
4. How are scope conflicts computed before a wave and detected after execution?
5. What normalized reviewer finding schema maps runtime-specific outputs to severity and closure decisions?
6. What deterministic behavior should the engine take for `done_with_concerns` and `needs_context`?
7. What is wave acceptance, and how does it differ from per-ticket closeout?
8. What config surface exists in v1, and where do defaults live?

## Recommended next step

Do not start implementation from this spec alone. First produce a tighter design addendum covering:

- artifact schemas
- ticket markdown schema and compatibility rules
- claim/lease protocol
- runtime adapter contracts
- conflict-detection rules
- evidence and review-finding schemas
- config and retry defaults
