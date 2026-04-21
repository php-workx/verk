# verk Skill Host Portability

## Metadata

- Date: 2026-04-19
- Scope: extend verk's skill surface from Claude Code (v1) to additional agent CLIs: Codex, OpenCode, OpenClaw, Cursor, Kiro, Factory Droid.
- Depends on: [verk-as-skill-cross-agent (v1)](2026-04-19-verk-as-skill-cross-agent.md) shipped and GA on Claude Code.
- Out of scope: browser automation (permanent); multi-judge council, vakt bridge, flywheel (sibling-plan boundary).
- Status: blocked on v1 GA + host capability audit

## Summary

v1 established verk's skill surface on Claude Code. This plan extends it across 6+ additional agent CLIs, with two critical constraints:

1. **Each host's sub-agent primitive must be audited before implementation.** Many host capabilities in the original draft were assumed; they must be verified against official documentation before building adapters.
2. **Degraded mode is a first-class target**, not a fallback. Some hosts will not have fresh-context sub-agent primitives at all. Those hosts must still ship a working `verk` skill surface — via degraded mode (daemon-dispatched subprocess workers).

Six tracks, gated in order:

| # | Track |
|---|-------|
| H0 | Host capability audit (required before any adapter implementation) |
| H1 | Adapter architecture: config schema + transformer library |
| H2 | Per-host adapter implementation (phased: Codex first) |
| H3 | Degraded mode (upgrade from v1's skeleton) |
| H4 | Cross-runtime sub-agents (e.g., Claude orchestrator → Codex sub-agent) |
| H5 | Auto-detect setup (multi-host install) |

**H0 is a hard gate.** If the audit reveals a host lacks a usable fresh-context primitive AND subprocess dispatch is not viable, that host is dropped from scope until the upstream provides one.

## Context

### What v1 established

- Daemon architecture with HTTP surface, session binding, crash recovery, trust-boundary model.
- Skill catalog (7 skills) with template-based generation from Go types.
- Claude Code sub-agent dispatch via `Agent` tool with daemon-controlled output paths.
- Strict mode (daemon dispatches LLM phases via subprocess) as a correctness-maximizing escape hatch.
- `./setup`, team mode, `verk-doctor` for single-host install.

### What this plan adds

- Per-host adapter configs and post-generation transformers.
- A formal capability taxonomy (what does each host actually support?).
- First-class degraded mode for hosts without fresh-context sub-agents.
- Cross-runtime dispatch as an optional capability.
- Auto-detect multi-host install.

### What this plan does **not** change

- Daemon architecture. Unchanged.
- State machine, artifact contracts, gates. Unchanged.
- Trust boundary and defense layers from v1. Same layers apply to every host; per-host details differ only in which layer is strongest.

---

## H0 — Host Capability Audit

### Goal

Before writing any adapter, produce a capability attestation per host. Implementation is blocked until this exists.

### Audit questions per host

For each candidate host, document:

| Question | Why it matters |
|----------|----------------|
| Official docs URL for skill/plugin/command system | Source of truth for all other answers |
| Sub-agent primitive name (if any) | Determines whether in-session dispatch is possible |
| Does the primitive provide **fresh context**? | Ralph invariant. If no, host is degraded-mode-only. |
| Does the primitive allow **write-to-file output**? | Needed for daemon-controlled output paths (Trust Boundary layer 2) |
| Does the primitive return a **transcript** of sub-agent tool use? | Enables Trust Boundary layer 3 |
| Does the primitive support **parallel** sub-agents? | Future wave-scope reviewer parallelism |
| Does the host have a tool-use allowlist / frontmatter equivalent? | Adapter must respect it |
| Does the host have its own `AskUserQuestion` or equivalent? | Affects interactive-prompt transforms |
| How does the host install skills? (path, manifest, registration) | Setup-script target |
| Can the host run a bash preamble? | Our preamble assumes this |
| Can the host call an HTTP endpoint from within a skill? | Required for daemon communication |

### Candidate hosts

Hosts to audit for Phase 1:

- Codex CLI
- OpenCode
- OpenClaw
- Cursor
- Kiro
- Factory Droid

Each gets one section in the audit document.

### Artifact

Single file: `docs/reviews/2026-04-XX-host-capability-audit.md` with one table per host plus overall findings:

- Hosts with full capability → adapter Phase 1.
- Hosts with partial capability (e.g., no transcript, no write-to-file) → adapter with reduced Trust Boundary depth; document which layers are missing.
- Hosts with no usable sub-agent primitive → degraded mode only; no in-session sub-agent.
- Hosts that cannot call HTTP from a skill → **dropped** (can't reach the daemon).
- Hosts with fundamental incompatibility → dropped with documented reason.

### Decision gate

No adapter code is written until the audit is committed and reviewed. Adapters for "partial capability" hosts require explicit sign-off on which Trust Boundary layers are reduced.

### Files

- `docs/reviews/2026-04-XX-host-capability-audit.md` — committed artifact.
- `hosts/_audit-template.yaml` — starter template for audit entries.

---

## H1 — Adapter Architecture

### Goal

Per-host configuration + post-generation transformers. Adapters are data + code, not prose.

### Config schema (`hosts/<host>.yaml`)

```yaml
name: codex
display: "OpenAI Codex CLI"
install_root: "~/.codex/skills/verk-%s/"
skill_manifest:
  format: "codex_frontmatter_v1"
  required_fields: [name, description, allowed_tools, triggers]
  frontmatter_delimiter: "---"
http_capability:
  can_curl_from_skill: true
  preamble_shell: bash
sub_agent:
  primitive: "<as documented in audit>"
  fresh_context: true | false | unknown
  supports_output_path: true | false
  supports_transcript: true | false
  supports_parallel: true | false
trust_boundary_layers:
  - layer_1_phase_split       # always available
  - layer_2_output_path       # if supports_output_path
  - layer_3_transcript        # if supports_transcript
  - layer_4_content_addressed # always
  - layer_5_strict_mode       # always (subprocess fallback)
fallback_mode: "in_session" | "degraded_only"
tool_mappings:
  # Claude Code name → host name
  AskUserQuestion: "<host equivalent or 'prose'>"
  Agent: "<sub-agent primitive name>"
transformers:
  - <named transformer functions>
```

### Transformer library

Post-generation Go functions that mutate Claude Code's canonical SKILL.md into host-specific output:

- `frontmatter_rewrite` — substitute frontmatter schema per host conventions.
- `tool_mapping_replace` — rename tool references (`Agent` → host's primitive name).
- `askuserquestion_to_prose` — for hosts without structured AskUserQuestion.
- `bash_preamble_adapt` — adjust preamble syntax for hosts with different shell conventions.
- `http_call_lower` — if a host expresses HTTP differently (e.g., structured fetch rather than `curl`), rewrite the preamble.
- `degraded_mode_inject` — for hosts with `fallback_mode: degraded_only`, replace in-session dispatch instructions with degraded-mode instructions.

Transformers are pure Go functions with unit tests and before/after fixtures.

### Files

- `hosts/*.yaml` — one per host.
- `internal/skills/transform/` — transformer library.
- `internal/skills/transform/*_test.go` — each transformer tested against fixture pairs.

---

## H2 — Per-Host Adapter Implementation

### Phased order

Adapters roll out in phases based on audit confidence:

**Phase 2a — Full-capability hosts (pending audit)**

Likely candidates: Codex, OpenCode. Same Trust Boundary depth as Claude Code v1.

**Phase 2b — Partial-capability hosts (pending audit)**

Hosts with sub-agent primitive but missing one or more of: transcript, output-path, parallel. Adapters ship with explicit documentation of which layers are reduced and a warning in `verk-doctor`:

> Host `<name>`: sub-agent transcript not available. Trust Boundary layer 3 not active. Consider `policy.daemon.strict_mode = true` for higher assurance.

**Phase 2c — Degraded-mode-only hosts (pending audit)**

Hosts without a usable sub-agent primitive. `./setup --host <name>` installs skills that always invoke degraded mode. Full functionality, different dispatch path. See H3.

### Per-adapter acceptance

For each adapter:

1. Audit entry committed.
2. Config in `hosts/<host>.yaml`.
3. Transformers produce valid SKILL.md for the host's skill format (verified by host-specific load test if test infra exists, by frontmatter-lint if not).
4. Fixture run: from inside the host, `/verk-run-ticket <fixture>` completes the MVP success criterion (byte-identical artifacts modulo host name).
5. `verk-doctor` reports health correctly on that host.

### Rollout stages per host

Same three-stage rollout as v1 Claude Code rollout, per host: alpha (self-dogfood), beta (opt-in), GA.

A host does not reach GA until:
- 10 consecutive fixture runs pass the MVP criterion.
- `verk-doctor` has no open warnings.
- Trust Boundary layer status is documented accurately.

---

## H3 — Degraded Mode (first-class)

### Goal

v1's plan sketched degraded mode as a fallback for hosts lacking sub-agent primitives. This plan promotes it to a first-class path: fully documented, tested, and recommended for hosts where it's the only option.

### Mechanism

When skill is invoked on a `fallback_mode: degraded_only` host:

1. Orchestrator (host agent) calls `/orchestrate` as usual.
2. `/next-step` returns `dispatch_mode: "daemon_subprocess"` instead of `"in_session_subagent"`.
3. Orchestrator's only job: call `POST /dispatch-engine-worker { lease_id, phase }`, then poll `GET /phase-status?lease_id=...` until terminal.
4. Daemon spawns a runtime subprocess (`claude-code`, `codex`, or whichever adapter is configured in `policy.runtime`) with the rendered prompt. Subprocess writes output to daemon-controlled path. Daemon validates as usual.
5. Orchestrator reports results to user as each phase completes.

### Why this is better than in-session on some hosts

For hosts where in-session sub-agents lack fresh context (Trust Boundary layer 1 violated) or lack output-path guarantees (layer 2 missing), degraded mode achieves **stronger** trust guarantees than in-session dispatch on the same host: the daemon spawns the subprocess, controls its environment, and reads its output — orchestrator cannot fabricate.

This is the Trust Boundary layer 5 (strict mode) from v1, applied by default on hosts that need it.

### Cost

- Each LLM phase costs one subprocess startup (~1-3 seconds).
- Less in-session visibility: the user sees progress via polled `/phase-status` reports, not as live tool calls in their session.
- Orchestrator's token spend is minimal (just polling + reporting).

### Files

- `internal/daemon/dispatch/subprocess.go` — elaborate on v1's skeleton.
- `internal/daemon/dispatch/subprocess_test.go` — subprocess lifecycle, timeout, signal handling.

### Tests

- Integration: seeded degraded-mode fixture run completes end-to-end.
- Chaos: daemon killed mid-subprocess → child reaps; orphan recovery works.
- Unit: polling interval, timeout, cancellation.

---

## H4 — Cross-Runtime Sub-Agents

### Goal

Orchestrator in host A spawns a sub-agent in host B — e.g., Claude Code orchestrator spawns a Codex sub-agent for a review phase. Enables cross-model review without requiring both runtimes from the same agent session.

### This is a separate sub-protocol

v1 treated this as a line-item; adversarial review correctly identified it's actually a full sub-protocol. This track fully specifies it.

### Mechanism

When `policy.skill.cross_runtime_review = true` AND the configured review runtime ≠ orchestrator's host:

1. `/next-step` for the review phase returns `dispatch_mode: "cross_runtime"` with `target_runtime: "codex"`.
2. Orchestrator is instructed: "invoke `codex exec --prompt-file <X> --output-file <Y>` as a subprocess." This is a degraded-like path but explicitly cross-runtime.
3. Target runtime's CLI must support non-interactive, prompt-file, output-file mode. Audited per host.
4. Daemon validates the output file like any other sub-agent output.

### Prerequisites

- Target runtime CLI installed on user's machine.
- Target runtime credentials available in environment.
- Target runtime's non-interactive-mode capability verified in audit.

### Doctor check

`verk-doctor` validates all configured cross-runtime targets: CLI present, credentials available, non-interactive mode works. Emits specific install/login instructions per missing component.

### Files

- `internal/daemon/dispatch/cross_runtime.go`.
- `internal/daemon/dispatch/cross_runtime_test.go` — fixture test per supported target.
- `hosts/<host>.yaml` gains `non_interactive_exec:` capability block.

### Tests

- Integration: Claude Code orchestrator + Codex review on fixture ticket; output validated.
- Unit: missing credentials → doctor flags.
- Unit: target CLI not on PATH → doctor flags.

---

## H5 — Auto-Detect Setup (multi-host)

### Goal

`./setup` detects installed hosts and offers per-host install. Direct evolution of v1's single-host setup.

### Commands

| Command | Effect |
|---------|--------|
| `./setup` | Detect installed hosts; prompt per host for install; install for accepted hosts |
| `./setup --host <name>` | Install for one named host |
| `./setup --host=all` | Install for all detected hosts, no prompts |
| `./setup --dry-run` | Print per-host install plan; no writes |
| `./setup --team` | Install to repo-local; commit |

### Detection

Walk known host install roots with presence marker checks (not mere directory existence):

| Host | Marker checked |
|------|----------------|
| Claude Code | `~/.claude/settings.json` present |
| Codex | `~/.codex/config.toml` present |
| OpenCode | `~/.config/opencode/config.json` present |
| Cursor | `~/.cursor/` with version file |
| (etc., per audit) | |

### Rejection

Hosts present but unsupported (e.g., Cursor version below required minimum) are listed with an actionable error and skipped.

### Files

- `cmd/verk-setup/detect_multi.go` — multi-host detection.
- `cmd/verk-setup/install_per_host.go` — per-host install orchestration.

### Tests

- Unit: detection with a synthetic root containing multiple hosts at known paths.
- Unit: version-below-minimum rejection.
- Integration: `./setup --dry-run` on a realistic synthetic install shows correct plan.

---

## Rollout

### Per-host cadence

Each host goes through alpha → beta → GA independently, gated on its own audit + Phase 2 acceptance criteria.

A host is never promoted to GA before Claude Code GA. Claude Code v1 is the reference; other hosts are compared to it.

### Rollout prioritization

Order (post-audit, subject to audit findings):

1. **Codex** — highest priority; already a first-class runtime for verk workers, audit is mostly verifying what's already used in engine-mode.
2. **OpenCode + OpenClaw** — YC/Anthropic ecosystem; high ROI for users.
3. **Cursor** — large user base but capability uncertain; likely degraded mode only.
4. **Kiro + Factory Droid** — smaller ecosystems; ship if audit clears.

Hosts that fail audit (no HTTP from skill, no sub-agent, no non-interactive mode) are documented as unsupported with reasons.

### Documentation

`docs/skills/HOST_COMPAT.md` maintained with a matrix:

| Host | Status | Trust Boundary layers active | Degraded mode | Cross-runtime source/target | Notes |
|------|--------|------------------------------|---------------|----------------------------|-------|

Updated on each host promotion or demotion.

---

## Self-Review

Targeted on this plan specifically. Shorter than v1's because most cross-cutting concerns are already handled by v1's architecture.

| ID | Severity | Finding | Stance |
|----|----------|---------|--------|
| HR-1 | high | Without H0 audit, Phase 2 adapter work cannot begin. This plan's entire critical path starts with a research task. | **Accepted.** Stating this explicitly is the point. Audit must complete before adapter code. |
| HR-2 | medium | Transformer library may accumulate one-off hacks per host. Could become a mess. | Transformers are Go functions with tests; naming convention (`<domain>_<action>`); PR review flags unnamed/ad-hoc transforms. |
| HR-3 | medium | Cross-runtime sub-agents require target CLI installed + credentialed + non-interactive-capable. Significant install-friction delta per user. | `verk-doctor` is verbose and specific; cross-runtime is opt-in per policy; never auto-enabled. |
| HR-4 | medium | Host capability may change upstream (e.g., Codex adds fresh-context sub-agents). Audit goes stale. | Re-audit on each host's major version bump; `verk-doctor` includes a version-pin check against audit document. |
| HR-5 | low | Degraded mode cost (subprocess per phase) may be unacceptable for latency-sensitive workflows. | Acceptable trade-off for correctness on those hosts. Operators aware via `HOST_COMPAT.md`. |
| HR-6 | low | The "one host drops to degraded mode" story is different per host, producing a matrix of per-host quirks that may confuse users. | `HOST_COMPAT.md` is the single source of truth; `verk-doctor --host <name>` prints the host's full capability report. |

### Open items acknowledged

| ID | Residual |
|----|----------|
| HR-R1 | Audit findings may invalidate the host list. Some candidates may be dropped mid-plan. This is a feature of the gate, not a bug. |
| HR-R2 | Per-host test infrastructure varies widely. Some hosts have no programmatic way to validate "skill loads"; for those, best-effort frontmatter-lint only. Document limitations. |
| HR-R3 | Cross-runtime non-interactive-exec protocols are per-host. One host's pattern likely doesn't translate to another. Expect N separate implementations. |

---

## Open Questions

1. **Should cross-runtime (H4) ship before or after Phase 2 adapters for the involved hosts?** H4 depends on both hosts being adapter-capable (for the orchestrator side) but also requires the target host's non-interactive-exec capability. Natural order: Phase 2 for both hosts → then H4 between them. Revisit if a specific host pair has strong demand.
2. **Should degraded mode be the default for partial-capability hosts, or should operators opt in?** Leaning default degraded, with in-session as an opt-in override — "stronger guarantees are safer default."
3. **Auto-detect precedence when multiple hosts are present.** `./setup` prompts per host today; should there be a "install for all that support full mode, degraded only on request" shortcut?
4. **Versioning of the audit document.** Audit findings should be pinned to host versions. When a host releases a new major version, we need to re-audit. How is this tracked? Proposed: `hosts/<host>.yaml` carries `audited_version_range` and `verk-doctor` warns if user's installed version exceeds the range.

## Out of Scope

- Windows. Separate track against existing `runlock_windows.go` infrastructure; applies to daemon portability, not host portability.
- Browser automation.
- Remote or shared daemons.
- Multi-judge council, vakt bridge, flywheel, specialist personas beyond worker + reviewer (v1 and sibling plan boundaries).
- Host CLI bundling — verk does not ship other hosts' binaries. Users install hosts independently.

## Acceptance Criteria (Plan-Done)

1. H0 audit committed; covers all 6 candidate hosts; each has a capability table.
2. At least 2 Phase 2a hosts (full-capability) at GA, meeting byte-identity criterion against Claude Code v1 reference.
3. At least 1 Phase 2c host (degraded-only) at GA, demonstrating that degraded mode is a legitimate full-functionality path.
4. `HOST_COMPAT.md` current and accurate.
5. `verk-doctor` reports per-host capability truthfully.
6. Cross-runtime (H4) demonstrated end-to-end for at least one host pair, OR explicitly deferred with documented reason.
7. Auto-detect `./setup` handles the full supported host set without manual intervention.
8. No host is promoted to GA without its capability audit entry being reviewed and signed off.
