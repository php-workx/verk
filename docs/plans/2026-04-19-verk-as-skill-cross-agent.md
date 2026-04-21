# verk as Skill — v1: Claude Code Foundation

## Metadata

- Date: 2026-04-19
- Scope: expose verk's inner-loop primitives as installable skills invokable from inside Claude Code
- Host scope: **Claude Code only** in v1. Other hosts are deferred to [verk-skill-host-portability](2026-04-19-verk-skill-host-portability.md).
- Out of scope: browser automation (permanent); multi-judge council + vakt bridge (sibling plan boundary); cross-runtime sub-agents (host portability plan).
- Sibling plan: [impl-verify-improvements](2026-04-19-impl-verify-improvements.md) covers engine-mode improvements.
- Status: planned (v2 after the brutal self-review of 2026-04-19)

## Summary

Add a second surface to verk — **verk-as-skill** — invokable from inside Claude Code during a live session. Engine-mode (`verk run …`) stays the primary interface; skill-mode is an in-session convenience layer that preserves verk's correctness invariants.

Three-tier model:

```
  Claude Code session (orchestrator, stays visible)
        │
        │ invokes /verk-* skill
        ▼
  verk daemon (HTTP, bearer token, session-bound; owns state + gates)
        │
        │ (a) deterministic phases: daemon executes directly
        │ (b) LLM phases: orchestrator dispatches sub-agents via Agent tool
        ▼
  Sub-agent workers (ephemeral, fresh context, write output to daemon-controlled paths)
```

Six tracks (S4 Host Adapters extracted to the follow-up plan):

| # | Track |
|---|-------|
| S1 | Daemon mode (HTTP + state file + session binding + startup lock + version-restart) |
| S2 | Skill catalog (7 skills) |
| S3 | Template + codegen from Go types |
| S5 | Sub-agent dispatch layer (Claude Code only) |
| S7 | Setup + team mode (Claude Code only) |
| S8 | Safety integration (`owned_paths` auto-freeze) |

Order: **S1 → (S2 + S3 parallel) → S5 → (S7 + S8 parallel)**.

## Context and Boundaries

### What stays unchanged

- The `verk` binary, CLI, and engine-mode orchestration.
- Contracts in `docs/plans/done/initial_v1.md`: state machine, artifacts, gates, derived pass/fail, lease semantics, scope enforcement.
- The `.verk/runs/<run-id>/` artifact layout. Skill-mode writes the same artifacts via the same engine code paths.

### What this plan does not address

- **Host portability.** Claude Code only. Codex, OpenCode, OpenClaw, Cursor, Kiro, Factory Droid are in the follow-up plan.
- **Browser automation.** Permanent out of scope for verk.
- **Cross-runtime sub-agents.** E.g., Claude Code orchestrator spawning a Codex sub-agent. Deferred to the host-portability plan.
- **Remote / shared daemon.** Localhost only.
- **Windows.** v1 is Unix-like (macOS + Linux). Windows is a separate track against existing `runlock_windows.go` infrastructure.

## Trust Boundary (read first)

The plan's most important non-obvious property: **in-session sub-agent integrity cannot be fully proven**, because by design the orchestrator (Claude Code session) dispatches sub-agents via the host's Agent tool. A drifting or hallucinating orchestrator can in principle fabricate sub-agent work.

verk does not pretend otherwise. The threat model is realistic about it, and defense is layered:

| Layer | Mechanism | What it prevents |
|-------|-----------|------------------|
| 1. **Phase split** | Deterministic phases (`verify`, `closeout`, gate evaluation, scope reconciliation) execute **inside the daemon**. No orchestrator involvement. No LLM. | Orchestrator cannot fabricate gate outcomes or verification results. |
| 2. **Daemon-controlled output paths** | Each sub-agent lease carries `output_path = .verk/runs/<run-id>/sub-agent-outputs/<lease-id>.json`. Sub-agent writes JSON there via its Write tool. Daemon reads from disk. | Makes the "happy path" unambiguous and auditable. Does not prevent orchestrator-side fabrication. |
| 3. **Sub-agent transcript capture** | Orchestrator posts the Agent tool's full return transcript, not just the summary. Daemon validates: transcript contains the Write tool call to the expected path; timestamps are consistent. | Raises the bar on fabrication. A forged transcript is detectable if it lacks the expected tool-call sequence. |
| 4. **Content-addressed artifacts** | Sub-agent output references `changed_files[]` with per-file `sha256`. Daemon re-hashes and verifies. | Catches accidental drift (sub-agent reported wrong file). Does not prevent deliberate fabrication. |
| 5. **Strict mode (opt-in)** | `policy.daemon.strict_mode = true` → daemon dispatches sub-agents as subprocess runtime adapters (engine-mode path), not via the orchestrator's Agent tool. | Full isolation. Orchestrator cannot fabricate because it's not in the path. Cost: subprocess startup per phase; reduced in-session visibility. |

**Strict mode is the escape hatch for any operator who distrusts in-session sub-agent integrity.** It ships with v1. Non-strict mode is the default because its UX is better and the mitigations above are adequate for the common threat model ("the orchestrator agent is Claude, not an adversary").

The plan's self-review retains an open finding about residual fabrication risk (RR-1) rather than claiming it's solved.

---

## S1 — Daemon Mode

### Goal

Long-lived daemon per workspace, reachable via HTTP on localhost with bearer auth. Session-bound so multiple concurrent invocations can't step on each other.

### State file

`.verk/daemon.json`, atomic write (tmp + rename), mode `0600`:

```json
{
  "pid": 12345,
  "port": 34567,
  "token": "uuid-v4",
  "started_at": "...",
  "binary_version": "<git-sha>",
  "schema_version": "1",
  "supported_prompt_schemas": ["v1"]
}
```

### Startup lock (R-H3)

Daemon startup is serialized via `flock` on `.verk/.daemon-startup.lock`. Second process blocks until first completes, then reads the fresh state file. Prevents port/state races when two skill invocations arrive simultaneously.

### HTTP surface

All endpoints require `Authorization: Bearer <token>` except `/health` and `/version`.

| Endpoint | Purpose |
|----------|---------|
| `GET /health` | Liveness probe |
| `GET /version` | Binary + schema versions |
| `GET /capabilities` | Supported prompt schemas, strict-mode availability, concurrency limits |
| `GET /status` | Active runs, phases, last failed gate |
| `GET /runs` | List in-flight runs |
| `POST /orchestrate` | Start a skill-mode run; returns `{run_id, orchestrator_session_id}` |
| `POST /heartbeat` | Orchestrator liveness signal |
| `POST /next-step` | Given state, return next action + prompt + `output_path` + `lease_id` |
| `POST /intent-result` | Sub-agent posts intent echo (path + lease) |
| `POST /implement-result` | Sub-agent posts implementation summary (path + lease + transcript) |
| `POST /verify` | Daemon runs verification commands itself; returns per-command results |
| `POST /review-result` | Sub-agent posts reviewer findings |
| `POST /repair-result` | Sub-agent posts repair cycle output |
| `POST /closeout` | Daemon runs typed closeout gates; returns per-gate result |
| `POST /cancel-run` | Orchestrator signals user abort; daemon cleans leases |
| `POST /resume` | Orchestrator attaches to an orphaned run (requires `--claim`) |

All state-mutating endpoints accept `Idempotency-Key` header; daemon dedupes: same key + same body = cached response; same key + different body = 409.

### Session binding (R-C3)

`/orchestrate` issues an `orchestrator_session_id` (UUID). All subsequent endpoints for that run must carry it. Only the owning session drives the run. `/heartbeat` extends the session lease (default 2 min). Missed heartbeats → run transitions to `orphaned`. Resumable via `/resume --claim <run-id>`.

Multiple concurrent runs on the same daemon is allowed; each has its own session. Per-run mutex protects state writes. Global mutex protects `.tickets/.claims/`. `policy.daemon.max_concurrent_runs` defaults 4.

### Crash recovery (R-H2)

- Lease records (in `.verk/runs/<run-id>/leases/<lease-id>.json`) include the rendered prompt and expected output schema. Daemon can replay leases after restart without in-memory state.
- Sub-agent output paths are deterministic (daemon-controlled); daemon scans for pending outputs on startup and reconciles against lease state.
- In-flight subprocess children: daemon sets `PR_SET_PDEATHSIG` (Linux) / kqueue (macOS) so children die with the daemon. No orphans.
- On restart: daemon reads all `runs/<run-id>/leases/` with `state = active`; for each, checks output_path existence; if present, parses + transitions; if absent and lease expired, marks failed.

### Lifecycle

- Auto-start from skill preamble. Startup polled up to `policy.daemon.startup_timeout_seconds` (default 30; generous for first-run).
- Idle shutdown after `policy.daemon.idle_timeout_minutes` (default 30).
- Version mismatch between binary and running daemon → graceful restart. Daemon refuses shutdown while any endpoint is mid-request; completes in-flight work then exits.

### Mode interaction (R-H1)

One mode per repo at a time:

- If daemon is running, engine-mode `verk run` refuses with `daemon_active` error pointing at `verk daemon stop` or `/verk-run-ticket`.
- If an engine-mode run holds a ticket lease, daemon `/orchestrate` on the same ticket returns 409 with `active_mode: "engine"`.
- `verk status` surfaces active mode and how to switch.

### Security

- Binds `127.0.0.1` only.
- Token mode `0600`, never logged.
- Port random in `10000-60000`, retry on collision.
- No cross-workspace daemon reuse; state file under `.verk/`.

### Observability (R-M5)

- Structured JSON logs to `.verk/daemon.log`, leveled (`debug|info|warn|error`), rotated at 10MB or daily.
- Per-run trace at `.verk/runs/<run-id>/daemon-trace.jsonl` with endpoint, phase, duration, tokens (where applicable), lease, outcome.
- `verk status <run-id> --trace` surfaces the sequence.
- Daemon-level metrics (memory, goroutine count, uptime, per-endpoint latency) available via optional `/metrics` endpoint (`policy.daemon.metrics_enabled`, default false).

### Files

- `internal/daemon/` (new): `server.go`, `state.go`, `startup_lock.go`, `session.go`, `lifecycle.go`, `auth.go`, `crash_recovery.go`, `logs.go`.
- `cmd/verk/daemon.go` — new subcommand `verk daemon start|stop|status|logs`.

### Tests

- Unit: state file atomic write; concurrent readers never see partial.
- Unit: startup lock serializes two concurrent `verk daemon start` invocations.
- Unit: token mismatch returns 401; session-id mismatch returns 403.
- Unit: `Idempotency-Key` dedupes matching requests; different body returns 409.
- Unit: heartbeat timeout marks run `orphaned`; `/resume --claim` recovers it.
- Integration: kill -9 daemon mid-request; restart; in-flight lease replayed from disk; no orphaned children.
- Integration: version mismatch triggers graceful restart; in-flight work completes.
- Integration: engine-mode run + concurrent daemon start → correct conflict messaging.

---

## S2 — Skill Catalog

Seven skills. Bodies are thin: preamble ensures daemon is live and shows state; instructions tell the orchestrator to call the daemon and dispatch sub-agents.

| Skill | Purpose |
|-------|---------|
| `/verk-run-ticket <ticket-id>` | Full inner loop via sub-agents: intake → intent → implement → verify → review → repair → closeout |
| `/verk-verify [--files=<glob>]` | Run verification commands against current worktree (daemon executes) |
| `/verk-review <diff-range>` | Dispatch reviewer sub-agent against a diff; record findings |
| `/verk-repair <finding-ids>` | Dispatch repair sub-agent for named findings; enforce `resolution_evidence` |
| `/verk-closeout <ticket-id>` | Run typed closeout gates (daemon executes); report failed gate |
| `/verk-status [<run-id>]` | Current run, active tickets, pending findings, last failed gate |
| `/verk-doctor` | Environment check: daemon, ticket store, claims, git, runtime adapters |

### Skill body shape

Three sections per skill:

1. **Frontmatter** (Claude Code format): `name`, `description`, `allowed-tools`, `triggers`.
2. **Preamble** (short bash): ensures daemon is live, fetches current state, fails fast with clear error if prereqs missing.
3. **Body** (prose): tells the orchestrator the interaction loop with the daemon. The daemon returns prompts; the orchestrator dispatches sub-agents and posts results.

The skill body does not embed worker prompts. Prompts are served by `/next-step` so they can evolve without re-releasing the skill pack.

### Prompt-schema versioning (R-H4)

- Daemon advertises `supported_prompt_schemas` on `/capabilities`.
- Skill declares `required_prompt_schema` in frontmatter.
- `/orchestrate` picks the highest schema both support; if none, rejects with `incompatible_skill` and message pointing at `verk-upgrade`.
- Skill pack upgrade check (preamble) fails loudly if daemon's minimum supported schema exceeds the skill's declared.

### Files

- `skills/verk-run-ticket/SKILL.md.tmpl` (and six more, one per skill).
- `skills/_shared/preamble.sh` — shared preamble (daemon auto-start, capability check, state report).
- `skills/_shared/orchestration-loop.md` — shared body snippet for the dispatch loop (included by the full-loop skills).

### Tests

- Unit: each skill template renders to valid Claude Code SKILL.md (frontmatter parses; no unbound placeholders).
- Integration: skill preamble auto-starts daemon on first call; second call reuses.
- E2E (Claude Code host): `/verk-run-ticket` on a seeded fixture ticket completes all phases; output artifact matches engine-mode output byte-for-byte (per MVP success criterion).

---

## S3 — Template + Codegen from Go Types

### Goal

Skill docs' command tables, phase names, severity enum, disposition values, completion codes, failed-gate names, policy schema, and daemon endpoints all live in Go source. Generate skill-pack docs from those sources so docs cannot drift.

### Placeholders

| Placeholder | Source |
|-------------|--------|
| `{{TICKET_PHASES}}` | `state.TicketPhase` enum + allowed transitions |
| `{{COMPLETION_CODES}}` | `runtime.CompletionCode` |
| `{{SEVERITY_ENUM}}` | `state.Severity` + bucket mapping |
| `{{DISPOSITION_VALUES}}` | `state.Disposition` |
| `{{FAILED_GATES}}` | closeout gate constants |
| `{{POLICY_SCHEMA}}` | `policy/config.go` struct tags |
| `{{DAEMON_ENDPOINTS}}` | daemon route registration |
| `{{PROMPT_SCHEMA_VERSIONS}}` | daemon's supported schemas |

### CI gate

`verk-gen-skills --check` compares generated output to committed files. Non-zero exit on drift. Runs on every PR touching `internal/state/`, `internal/daemon/`, `internal/policy/`, or `skills/`.

### Files

- `cmd/verk-gen-skills/main.go` (new).
- `internal/skills/gen/` (new): `ast.go`, `placeholders.go`, `render.go`.
- `.github/workflows/skill-docs-fresh.yml` (new).
- `justfile`: `gen-skills`, `check-skills`.

### Tests

- Unit: each placeholder renders for a known input.
- Integration: drift detection catches a deliberately-introduced mismatch.

---

## S5 — Sub-Agent Dispatch (Claude Code)

### Goal

Centralize the "spawn a fresh-context sub-agent, receive structured output via daemon-controlled path" semantics, specific to Claude Code's Agent tool.

### Dispatch contract

Each `/next-step` response carries:

```json
{
  "phase": "intent|implement|review|repair",
  "lease_id": "uuid-v4",
  "output_path": ".verk/runs/<run-id>/sub-agent-outputs/<lease-id>.json",
  "expected_schema": "v1.intent-result",
  "prompt": "… full rendered prompt including the output-path instruction …",
  "sub_agent_tool": "Agent",
  "timeout_seconds": 600
}
```

The orchestrator's instruction (from skill body): invoke the `Agent` tool with the given prompt. The prompt ends with "write your JSON output to `<output_path>` and return `DONE` as your final message." When Agent tool returns, orchestrator posts `{ lease_id, transcript }` to the phase endpoint.

### Daemon validation of posted results

On each post, daemon verifies:

1. Session ID matches the owner of the run.
2. Lease ID is active, unexpired, belongs to this run.
3. Output file exists at the expected path.
4. Output JSON parses against `expected_schema`.
5. Transcript contains a `Write` tool call with the output path (defense-in-depth per trust-boundary layer 3).
6. For phases with artifacts: `sha256` per `changed_files[]` matches what's on disk; each file is under the ticket's `owned_paths`.
7. Lease is consumed on success. Subsequent posts with the same lease return 409.

Any validation failure returns a typed error. The daemon never "partially accepts" — either all checks pass and the phase advances, or it rejects and the run stays put.

### Deterministic phases (no sub-agent)

- **`/verify`**: daemon executes declared + constraint verification commands itself via `internal/adapters/verify/command`. Orchestrator polls `/verify-status` (or blocks synchronously for short commands). No sub-agent dispatch. Orchestrator cannot fabricate the result.
- **`/closeout`**: daemon runs typed gate evaluation from `internal/engine/closeout.go`. No sub-agent.

### Strict mode (R-C1 mitigation layer 5)

`policy.daemon.strict_mode = true`: daemon dispatches LLM phases via subprocess runtime adapters (engine-mode path), not via orchestrator's Agent tool. Orchestrator polls `/phase-status?lease_id=...`. Fresh-context preserved; full isolation. Default off for v1; surfaced prominently in `verk-doctor` and README.

### Files

- `internal/daemon/dispatch/` (new): `lease.go`, `prompts/` (per-phase rendering), `validate.go`, `strict_mode.go`.
- `internal/daemon/dispatch/prompts/intent.go`, `implement.go`, `review.go`, `repair.go`.
- `internal/daemon/dispatch/validate_test.go` — fabrication-attempt test fixtures.

### Tests

- Unit: prompt rendering deterministic given same ticket + phase + bundle.
- Unit: each validation rule fails deterministically (missing file, wrong hash, expired lease, wrong session, transcript without Write call, schema mismatch).
- Unit: idempotent lease consumption (second post of same lease → 409).
- Integration: strict mode dispatches via subprocess; orchestrator receives progress; end-to-end result matches non-strict for fixture ticket.
- Adversarial: seeded "malicious orchestrator" test fixture posts fabricated results. All fabrication attempts rejected by validation layers; at least one attempt recorded in audit log.

---

## S7 — Setup + Team Mode

### Goal

One command installs skills + daemon for Claude Code. Optional team mode commits installation to the repo so teammates get verk automatically.

### Commands

| Command | Effect |
|---------|--------|
| `./setup` | Install skills to `~/.claude/skills/verk-*/`. Prompts on each. |
| `./setup --team` | Install to repo-local `.claude/skills/verk-*/`; commit. |
| `./setup --no-prefix` | Skills install as `/verk-run-ticket`. |
| `./setup --prefix` | Install as namespaced `/agentops-verk-run-ticket`. |
| `./setup --dry-run` | Print plan; no writes. |
| `verk-uninstall` | Remove skills + daemon state. |

### Binary distribution (R-C4)

Skills depend on `verk` being callable. Decision for v1:

- **`./setup` requires `verk` on PATH.** Fails fast with install instructions if missing.
- The skill pack does not bundle the binary. Rationale: verk is already a Go binary that users install separately (via `go install`, `brew`, or release binary); bundling creates version-drift problems. Preamble calls `verk` via PATH lookup.
- Future v2 may bundle per-platform binaries alongside the skill pack; flagged in follow-up plan.

### Team mode contract (R-M6 — softened)

- `./setup --team` commits `.claude/skills/verk-*/` and an opt-in line to `CLAUDE.md`.
- **Team mode advertises verk; it does not enforce via pre-push.** Enforcement at git-push is fragile (bypass via `--no-verify`, heuristic author detection) and creates friction for teammates who haven't installed verk. Strong enforcement belongs at code review, not locally.
- Teammates running Claude Code discover the skills automatically if the project-local install is present. `verk-doctor` detects team mode and warns if `verk` binary is missing.

### Auto-upgrade

Preamble calls `verk update-check --silent --throttled-1h`. If newer version available, prints a single line: `verk 0.6.0 available — run 'verk-upgrade'`. Never auto-applies. User-sovereignty preserved.

### Files

- `setup` — shell script wrapper calling `cmd/verk-setup`.
- `cmd/verk-setup/main.go` (new).
- `cmd/verk-uninstall/main.go` (new).

### Tests

- Unit: each subcommand renders correct install plan for a synthetic Claude Code install.
- Integration: end-to-end install → invoke skill → uninstall, against a scratch dir.

---

## S8 — Safety Integration (`owned_paths` Auto-Freeze)

### Goal

When a skill-mode run is active, sub-agents receive a hard edit guard instructing them to stay within the ticket's `owned_paths`. Borrows gstack's `/freeze` surface pattern; reuses verk's existing scope enforcement.

### Mechanism

- Daemon writes `.verk/runs/<run-id>/freeze.json` at orchestration start: `{run_id, ticket_id, allowed_paths, active, started_at}`.
- Sub-agent prompts include a `## Hard Edit Guard` section listing allowed paths with the instruction: "any edit outside this set must stop immediately and return `status: blocked` with reason `scope_escape_attempt`."
- Post-implementation, engine reconciles actual changed files against `allowed_paths` (existing behavior from `docs/plans/done/initial_v1.md`). Auto-freeze catches attempts at prompt time; scope reconciliation catches what slipped through.

### Not a new enforcement layer

This surfaces verk's existing `owned_paths` contract in the sub-agent prompt. Same rule, two surfaces: prompt-time guidance + post-implementation reconciliation.

### Files

- `internal/daemon/dispatch/prompts/safety.go` — render the guard block.
- `internal/state/types.go` — add `FreezeArtifact`.

### Tests

- Unit: prompt includes all `owned_paths`.
- E2E: sub-agent that attempts to edit outside returns `scope_escape_attempt`; engine blocks; audit event recorded.

---

## Token Strategy (skill-mode)

Mirrors the sibling plan's structure. Skill-mode has an additional cost layer: the orchestrator agent incurs tokens for dispatching and reporting on top of the sub-agents.

### Prevention

- Skill body is short (<100 lines) so orchestrator doesn't re-read a large instruction set each phase.
- Prompt-schema version mismatch rejects early (before any LLM call).

### Efficiency

- **Prompt caching.** Skill bodies and the rendered static prefix (persona + standards bundle) are cache-friendly. Daemon marks the prefix with `cache_control: ephemeral` on Claude Code dispatch.
- Sub-agent prompts use model tiers per sibling plan's `policy.runtime.model_tier_by_task` (intent → cheap, implement → standard, review → cheap, repair → standard).

### Skips

- Single-ticket wave skips wave review (inherited from sibling plan).
- Deterministic phases don't call LLMs at all.

### Reporting

- Per-phase `tokens_in` / `tokens_out` recorded in the sub-agent output and `daemon-trace.jsonl`.
- `verk status <run-id>` surfaces cumulative spend per run.
- Soft-warn threshold `policy.cost.soft_warn_tokens_per_skill_run` (default 200k) prints a warning but does not block.

### Never auto-abort

Same rule as sibling plan. Only `verk cancel <run-id>` or orchestrator-initiated `/cancel-run` ends a run early.

---

## MVP Success Criterion

From inside a Claude Code session on a clean fixture workspace:

1. User invokes `/verk-run-ticket <fixture-ticket-id>` (a seeded ticket in `.tickets/` with acceptance criteria + `owned_paths` touching 1-2 files).
2. The run completes through all phases (intake → intent → implement → verify → review → closeout) without operator intervention.
3. The resulting `.verk/runs/<run-id>/` artifact tree is **byte-identical** to what `verk run ticket <fixture-ticket-id>` produces in engine-mode against the same fixture, except for: `run_id`, timestamps, `reviewer_runtime`, and `daemon-trace.jsonl` (which is skill-mode-only).
4. The daemon-trace shows: at least one sub-agent dispatch per LLM phase; all validation layers passed; no fabrication-attempt warnings.

Byte-identical artifacts (modulo the exceptions) prove skill-mode is a legitimate alternate surface of the same engine, not a parallel implementation with different semantics.

---

## Rollout (Claude Code only)

Single-host rollout keeps this narrow:

1. **Alpha.** Internal self-dogfood only. `policy.daemon.enabled: false` by default; operator opts in explicitly.
2. **Beta.** `policy.daemon.enabled: false` still default. Documented in `docs/skills/BETA.md`. Operator opt-in per repo.
3. **GA.** Default flips to `true`. README advertises the skill surface. Still requires `./setup` to install skills.

Each stage has a soak gate:

| Stage | Gate |
|-------|------|
| Alpha → Beta | MVP success criterion holds for 10 consecutive fixture runs; zero fabrication-attempt warnings; no crash-recovery failures. |
| Beta → GA | 20 real ticket runs (not fixtures) across at least 2 operators; daemon uptime > 99% over 2 weeks of use; no open P0/P1 findings from adversarial testing. |

Kill switch at every stage: `verk daemon stop --force`; `./setup --uninstall`; `policy.daemon.enabled: false`.

---

## Self-Review (Residual Open Items)

This section documents **known-residual** issues that the current plan does not fully solve. This is deliberate: some trade-offs are intrinsic to the "skill-mode invokes sub-agents via the host" architecture and cannot be eliminated without a different architecture.

| ID | Severity | Residual issue | Plan's current stance |
|----|----------|----------------|-----------------------|
| RR-1 | high (known) | In-session sub-agent integrity cannot be fully proven. A sufficiently motivated orchestrator drift can fabricate sub-agent work despite all five defense layers. | Documented in Trust Boundary. Strict mode is the escape for operators who need higher assurance. |
| RR-2 | medium | Deterministic phases run inside the daemon process; long-running verification commands block the daemon's event loop unless carefully async-dispatched. | Implementation must use goroutines + per-command context cancellation. Called out in S1 tests. Not a conceptual gap, but a real-work item. |
| RR-3 | medium | Binary distribution via PATH means skill pack install can succeed with a stale `verk` binary on PATH. | `verk-doctor` and preamble both check binary version against skill-pack schema requirement; fail loudly. User may still ignore warnings. Acceptable trade-off for v1. |
| RR-4 | medium | Sub-agent transcript forgery is possible at layer 3 of the trust boundary. Detection relies on structural consistency checks that a sophisticated forger can satisfy. | Defense-in-depth; strict mode for full mitigation. |
| RR-5 | low | Concurrent skill-mode runs within one daemon share process memory. A crash in one run can affect others. | Per-run mutex + panic recovery per handler. Not fully isolated. Acceptable at `max_concurrent_runs: 4`. |
| RR-6 | low | `Idempotency-Key` dedupe window is bounded (in-memory for daemon lifetime). A long-delayed retry after daemon restart is not dedupe-protected. | Acceptable; the alternative (persistent idempotency store) adds complexity for a rare case. |
| RR-7 | low | `./setup` requires `verk` on PATH and fails if missing. Users who install skills first, binary second, hit a confusing error. | Preamble emits a specific install-instruction error. v2 may bundle binaries. |

### Findings from the adversarial self-review that **are** addressed in this revision

| Originally-flagged | Addressed by |
|---|---|
| Sub-agent output integrity insufficient | Trust Boundary section + 5-layer defense + strict mode + content-addressed artifacts + transcript validation |
| Orchestrator-daemon session binding missing | `orchestrator_session_id` + heartbeat + orphaned run + `/resume --claim` |
| Binary distribution unspecified | S7 explicit: PATH lookup required, fail-loudly preamble, v2 bundling deferred |
| Engine/skill coexistence undesigned | Mode Interaction section under S1 |
| Crash recovery partial | S1 crash-recovery sub-section: lease replay, subprocess reaping, output-path scan on restart |
| Startup race | `.verk/.daemon-startup.lock` via `flock` |
| Prompt versioning contract missing | `supported_prompt_schemas` + `required_prompt_schema` handshake |
| Missing HTTP endpoints (cancel, list, resume, heartbeat, capabilities) | Endpoint table expanded |
| Concurrency model undefined | Per-run + global mutex; `max_concurrent_runs`; panic recovery |
| Token Strategy absent | New Token Strategy section, skill-mode specific |
| Observability underspecified | `.verk/daemon.log`, trace schema, rotation, optional metrics |
| Team-mode fragile pre-push | Softened to advisory; advertising only; doctor warns |
| Idempotency / retries | `Idempotency-Key` on all state-mutating endpoints |
| MVP success criterion missing | Explicit byte-identical-artifacts criterion |

---

## Open Questions

1. **Claim reconciliation across modes.** Engine-mode and skill-mode share `.tickets/.claims/`. The Mode Interaction section rejects overlap at `/orchestrate`, but a race between the two is still possible (engine acquires lease just as orchestrator calls `/orchestrate`). Ticket claim acquisition logic in `internal/adapters/ticketstore/tkmd` handles this today; confirm skill-mode code path actually routes through it.

2. **Prompt cache partition key.** One key per run is conservative (cache hits only within a run). One key per repo is aggressive (cache hits across runs but crosses trust boundaries — a run-specific prompt leaks into another run's cache). v1 uses per-run; revisit if cache-hit rate underperforms.

3. **Strict mode default.** v1 defaults non-strict (orchestrator-dispatched sub-agents). If RR-1 / RR-4 prove practically concerning, flip default to strict in a v2 and make the current behavior opt-in. Decision point after Beta soak.

4. **Daemon log verbosity default.** `info` is reasonable but may miss diagnostic signal; `debug` floods disk. Settle during Alpha self-dogfood.

## Out of Scope

- **Host portability** — Codex, OpenCode, OpenClaw, Cursor, Kiro, Factory Droid. See [verk-skill-host-portability](2026-04-19-verk-skill-host-portability.md).
- **Cross-runtime sub-agents** (Claude orchestrator → Codex sub-agent). In host portability plan.
- **Windows.** Requires separate cross-platform adaptation using existing `runlock_windows.go` patterns.
- **Browser automation.** Permanent.
- **Remote / shared daemon.**
- **Multi-judge council, vakt bridge, flywheel, specialist personas beyond worker + reviewer.** Sibling plan boundaries.

## Acceptance Criteria (Plan-Done)

1. Alpha stage reached on Claude Code; MVP success criterion met for 10 consecutive fixture runs.
2. All residual open items (RR-1 through RR-7) documented in `docs/reviews/` with operator guidance.
3. Adversarial self-dogfood (operator deliberately attempts fabrication) finds no unmitigated path; findings either block a phase (pass) or get escalated to RR-list (document).
4. Strict mode works end-to-end for at least one LLM phase.
5. Engine-mode byte-identity test passes.
6. `verk-doctor` surfaces every health check described in this plan with actionable guidance.
7. `daemon-trace.jsonl` schema committed under `schemas/`.
8. No effort estimates on this plan — time-to-implement is governed by the gate criteria, not a date.
