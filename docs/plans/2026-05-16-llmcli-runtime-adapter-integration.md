# Fabrikk llmcli Runtime Adapter Integration

> **Status: Planned / High priority.** This is the current priority lane for
> tool stability and coding-agent reliability.

> **Related tickets:** `fi-8ken` and children.

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to
> implement this plan task-by-task.

## Goal

Migrate Verk's Claude and Codex runtime execution path onto the Fabrikk
`github.com/php-workx/fabrikk/llmcli` and `llmclient` packages while preserving
Verk's existing runtime API, artifacts, retry semantics, and worktree
isolation.

This plan intentionally targets the coding-agent tool path before returning to
benchmark implementation. Benchmarks remain useful, but stable and observable
runtime execution is the higher-priority foundation.

## Source Material

- Fabrikk package: `/Users/runger/workspaces/fabrikk/llmcli`
- Fabrikk client API: `/Users/runger/workspaces/fabrikk/llmclient`
- Fabrikk migration note:
  `/Users/runger/workspaces/fabrikk/docs/plans/2026-04-25-verk-llmcli-migration.md`
- Current Verk adapter packages:
  `internal/adapters/runtime/{claude,codex}`

## Why This Matters

Verk currently owns separate subprocess handling for Claude and Codex. That
duplicates process setup, timeout handling, raw output capture, availability
checks, and event normalization. Fabrikk's `llmcli` package gives Verk a shared
backend abstraction for coding agents, including readiness checks, working
directory options, environment overlays, timeout options, raw capture, Codex
JSONL mode, and event streams.

The migration should reduce adapter drift without weakening Verk-specific
product semantics. Verk still owns ticket prompts, artifact schema, result
normalization, retry classification, and closeout behavior.

## Non-Negotiable Compatibility

- Keep user-facing runtime names as `claude` and `codex`.
- Map Verk runtime `codex` to Fabrikk backend `codex-exec`; do not expose
  `codex-exec` in user-facing config, status, or doctor output.
- Preserve `claude.New`, `claude.NewWithCommand`, `codex.New`, and
  `codex.NewWithCommand`.
- Preserve worker, reviewer, and intent execution through
  `runtime.Adapter`.
- Preserve `runtime.BuildIsolatedProcessEnv`; never bypass it with ad-hoc
  environment construction.
- Preserve stdout, stderr, worker result, review result, token usage, activity
  stats, and raw Codex JSONL artifacts.
- Preserve Codex quota, rate-limit, and missing-context classification.
- Preserve timeout and process-tree cancellation behavior or document any
  fidelity gap before landing.
- Add only additive artifact diagnostics, such as `backend_name` or
  `fidelity`.
- Do not remove local subprocess helpers until bridge parity tests cover the
  replacement behavior.

## Proposed Architecture

Add a shared bridge package under:

```text
internal/adapters/runtime/llmclibridge
```

The package should hide Fabrikk-specific backend names from the rest of Verk.
The Claude and Codex packages become thin wrappers that preserve their existing
public constructors and call the bridge for worker, reviewer, and intent modes.

Bridge responsibilities:

- resolve backend name from Verk runtime name
- resolve custom command paths, including bare names via `exec.LookPath`
- construct `llmclient.Context` from Verk prompts
- apply `llmclient.WithWorkingDirectory`
- apply `llmclient.WithEnvironment` using `runtime.BuildIsolatedProcessEnv`
- apply `llmclient.WithTimeout`
- apply `llmclient.WithRawCapture`
- apply `llmclient.WithCodexJSONL(true)` for Codex
- apply `llmclient.WithReasoningEffort` only when supported and required
- collect streamed events into the local result shape used by adapters
- write artifacts in the same places and formats as today

Event collection rules:

- Raw capture stdout/stderr remains the artifact source of truth.
- Append `EventTextDelta` content to the normalized text buffer.
- If no deltas were seen, use terminal text event content.
- If no text events were seen, fall back to text blocks in `EventDone.Message`.
- Do not append both deltas and done-message text.
- On `EventError`, synthesize a non-zero exit code and append the error message
  to captured stderr.
- Treat cancelled done events as non-zero and let existing Verk retry
  classification decide the final retry class.
- Forward tool-call start/end events through `OnProgress` when available.

## Task Breakdown

### P1. Pin Fabrikk and Confirm Module Compatibility

- Add `github.com/php-workx/fabrikk` to Verk's module graph.
- Use a local `replace` only while developing.
- Before merge, pin a real Fabrikk tag or pseudo-version.
- Confirm the Go versions are compatible between Verk and Fabrikk.
- Acceptance: `go test ./...` resolves the dependency without a local-only
  assumption.

Implementation note:

- Pinned Fabrikk at `v0.1.0`, which matches the local Fabrikk checkout commit
  `d766ca3f90755c11305143a233a45eb291bf396b`.
- Raised Verk's `go` directive to `1.26.3`, matching Fabrikk's module
  directive and the installed toolchain.

### P1. Add the llmclibridge Package

- Implement backend selection and Verk-to-Fabrikk runtime mapping.
- Implement custom command resolution.
- Implement request option assembly for worker, reviewer, and intent calls.
- Implement fake-backend tests for event collection, raw capture, required
  options, and error mapping.
- Acceptance: bridge tests do not spawn real Claude or Codex CLIs.

Implementation note:

- Added `internal/adapters/runtime/llmclibridge` with fake-backend tests for
  runtime mapping, `exec.LookPath` command resolution, request context/options,
  raw capture, text de-duplication, text fallbacks, error/cancel exit mapping,
  and tool-call progress callbacks.

### P1. Migrate the Claude Adapter

- Rewire `internal/adapters/runtime/claude` through the bridge.
- Keep public constructors and `CheckAvailability`.
- Preserve worker, reviewer, and intent prompt behavior.
- Preserve artifact paths and progress behavior.
- Acceptance: existing Claude adapter tests pass with focused bridge-backed
  coverage.

### P1. Migrate the Codex Adapter

- Rewire `internal/adapters/runtime/codex` through the bridge using backend
  `codex-exec`.
- Pass Codex reasoning effort and JSONL options.
- Preserve token/activity telemetry from raw JSONL capture.
- Preserve quota, rate-limit, and missing-context classification.
- Acceptance: existing Codex adapter tests pass with JSONL and telemetry
  coverage.

### P2. Remove Duplicated Subprocess Helpers After Parity

- Delete local process-group and run-command helpers only after Claude and
  Codex parity tests are green.
- Keep platform-specific cancellation coverage if Fabrikk does not fully cover
  it.
- Acceptance: no behavior covered only by the deleted helpers is lost.

Implementation note:

- Removed the unused Claude-local process-group helper and its obsolete
  helper-only tests after Claude and Codex adapters were routed through
  Fabrikk `llmcli` via `llmclibridge`; Verk no longer owns local
  Claude/Codex subprocess or process-group helpers.
- Verk bridge and adapter tests retain bridge-level coverage for timeout and
  cancelled event normalization, raw capture plumbing, and runtime artifact
  behavior. They do not duplicate subprocess process-tree cancellation tests.
- Process-tree cancellation fidelity is owned by Fabrikk `llmcli` subprocess
  supervisor tests for the pinned Fabrikk version, including
  `llmcli/subprocess_test.go` coverage such as
  `TestSupervisor_ContextCancelTerminatesProcess`.
- Removal condition and guardrail: if Verk changes the pinned Fabrikk version
  or switches backend supervision mode, verify Fabrikk subprocess
  process-tree cancellation tests still exist and pass, or add a Verk smoke
  test before relying on the new execution path.

### P2. Update Doctor and Runtime Diagnostics

- Keep CLI runtime selection and doctor output user-facing as `claude` and
  `codex`.
- Surface missing binary, missing auth, unsupported option, and readiness
  failures with actionable details.
- Add backend/fidelity diagnostics to artifacts or status only where useful.
- Acceptance: doctor and CLI tests cover the stable user-facing names.

### P2. Document Operational Fallbacks

- Document when to use direct Go package integration versus the `llmcli` stdio
  binary.
- Document how to identify whether failures are Verk prompt/normalization
  failures or backend readiness failures.
- Keep persistent backends such as `codex-appserver` out of scope until
  `codex-exec` parity is complete.

## Verification

Focused tests:

```bash
go test ./internal/adapters/runtime/llmclibridge
go test ./internal/adapters/runtime/claude
go test ./internal/adapters/runtime/codex
go test ./internal/cli ./internal/engine -run 'Test.*Runtime|Test.*Doctor|Test.*Intent'
```

Full validation before merge:

```bash
go test ./...
just pre-commit
just pre-push
```

Manual smoke tests:

- Run one small ticket with Claude.
- Run one small ticket with Codex.
- Confirm worker, reviewer, and intent artifacts are still written.
- Confirm stdout/stderr artifact paths exist.
- Confirm worktree-local commands execute inside the assigned worktree.

## Open Decisions

- Direct Go package integration is the default for Verk; the `llmcli` stdio
  binary remains a fallback for non-Go consumers unless implementation uncovers
  a better reason to use it.
- Persistent backends are postponed until subprocess backend parity is proven.
- If Fabrikk lacks a required cancellation or artifact-fidelity hook, either
  add it in Fabrikk first or keep the smallest possible Verk-side shim with a
  documented removal condition.
