---
id: plan-2026-04-02-verk-v1-implementation
type: plan
date: 2026-04-02
source: "[[.agents/rpi/phase-1-summary-2026-04-02-initial-v1-rpi-discovery.md]]"
---

# Plan: Implement verk v1 from `specs/initial_v1.md`

## Context

`verk` is currently a spec-first repository with no Go implementation. The spec in `specs/initial_v1.md` has been hardened through an RPI pass and three adversarial persona reviews, and the current risk is no longer architectural ambiguity but faithful execution of the spec.

The implementation plan should therefore start from durable contracts, not from CLI polish. The main success condition for v1 is a deterministic engine with file-backed state, direct markdown ticket storage, claim/lease safety, normalized runtime adapters, and epic wave execution that can be resumed from artifacts alone.

## Files to Modify

| File | Change |
|------|--------|
| `go.mod` | **NEW** — initialize Go module |
| `cmd/verk/main.go` | **NEW** — CLI entrypoint |
| `cmd/verk/root.go` | **NEW** — root command wiring |
| `cmd/verk/run_ticket.go` | **NEW** — `verk run ticket` command |
| `cmd/verk/run_epic.go` | **NEW** — `verk run epic` command |
| `cmd/verk/reopen.go` | **NEW** — `verk reopen` command |
| `cmd/verk/resume.go` | **NEW** — `verk resume` command |
| `cmd/verk/status.go` | **NEW** — `verk status` command |
| `cmd/verk/doctor.go` | **NEW** — `verk doctor` command |
| `internal/state/types.go` | **NEW** — core enums and artifact structs |
| `internal/state/transitions.go` | **NEW** — state transition validation |
| `internal/state/store.go` | **NEW** — atomic JSON persistence |
| `internal/state/types_test.go` | **NEW** — transition tests |
| `internal/state/store_test.go` | **NEW** — persistence tests |
| `internal/policy/config.go` | **NEW** — config structs and load logic |
| `internal/policy/defaults.go` | **NEW** — engine defaults |
| `internal/policy/config_test.go` | **NEW** — config tests |
| `internal/adapters/ticketstore/tkmd/types.go` | **NEW** — ticket and claim data types |
| `internal/adapters/ticketstore/tkmd/store.go` | **NEW** — ticket read/write and graph queries |
| `internal/adapters/ticketstore/tkmd/claims.go` | **NEW** — claim acquire/renew/release/reconcile |
| `internal/adapters/ticketstore/tkmd/store_test.go` | **NEW** — markdown adapter tests |
| `internal/adapters/ticketstore/tkmd/claims_test.go` | **NEW** — claim lifecycle tests |
| `internal/adapters/runtime/types.go` | **NEW** — shared runtime contracts |
| `internal/adapters/runtime/fake/fake.go` | **NEW** — fake adapter for engine tests |
| `internal/adapters/runtime/types_test.go` | **NEW** — runtime contract tests |
| `internal/adapters/runtime/codex/adapter.go` | **NEW** — real Codex runtime adapter |
| `internal/adapters/runtime/codex/adapter_test.go` | **NEW** — Codex adapter tests |
| `internal/adapters/runtime/claude/adapter.go` | **NEW** — real Claude runtime adapter |
| `internal/adapters/runtime/claude/adapter_test.go` | **NEW** — Claude adapter tests |
| `internal/adapters/repo/git/repo.go` | **NEW** — repo root, diff baseline, changed-files logic |
| `internal/adapters/repo/git/repo_test.go` | **NEW** — git adapter tests |
| `internal/adapters/verify/command/runner.go` | **NEW** — deterministic verification runner |
| `internal/adapters/verify/command/runner_test.go` | **NEW** — command verification tests |
| `internal/engine/intake.go` | **NEW** — intake normalization |
| `internal/engine/closeout.go` | **NEW** — gate evaluation and evidence derivation |
| `internal/engine/ticket_run.go` | **NEW** — ticket loop |
| `internal/engine/epic_run.go` | **NEW** — epic orchestration |
| `internal/engine/wave_scheduler.go` | **NEW** — dependency and scope wave builder |
| `internal/engine/reopen.go` | **NEW** — reopen execution |
| `internal/engine/resume.go` | **NEW** — durable resume |
| `internal/engine/status.go` | **NEW** — status derivation |
| `internal/engine/doctor.go` | **NEW** — operational diagnostics |
| `internal/engine/*_test.go` | **NEW** — engine unit tests |
| `internal/e2e/ticket_happy_path_test.go` | **NEW** — end-to-end happy path |
| `internal/e2e/ticket_repair_cycle_test.go` | **NEW** — repair-cycle e2e |
| `internal/e2e/epic_multi_wave_test.go` | **NEW** — epic/wave e2e |
| `internal/e2e/resume_claim_recovery_test.go` | **NEW** — crash/recovery e2e |
| `testdata/manual/config.yaml` | **NEW** — reproducible manual verification config |
| `testdata/manual/tickets/*.md` | **NEW** — manual ticket fixtures |
| `testdata/manual/runs/test-run/*` | **NEW** — healthy status/resume fixture |
| `testdata/manual/runs/failed-closeout-run/*` | **NEW** — failed closeout fixture |
| `testdata/tickets/*.md` | **NEW** — sample ticket fixtures |
| `testdata/runs/*` | **NEW** — artifact fixtures for resume and corruption tests |

## Boundaries

**Always:** Follow `specs/initial_v1.md` as the implementation source of truth; keep all run state engine-owned; use file-backed deterministic artifacts; preserve markdown round-tripping; reject ambiguous or corrupt state deterministically; keep worker/runtime dependencies behind adapter interfaces; require mechanical verification for every issue.

**Ask First:** Final Go module path if a remote repository path is required; any desire to swap CLI libraries; any request to support dirty-worktree execution in v1 despite the current default.

**Never:** Introduce shell dependence on `tk` CLI; allow workers to mutate run artifacts directly; skip claim/lease safety; implement alternative scope fields beyond `owned_paths`; weaken closure rules to get green tests.

## Baseline Audit

| Metric | Command | Result |
|--------|---------|--------|
| Markdown planning/spec/review artifacts currently in repo | `find . -maxdepth 3 -type f | sed 's#^./##' | rg '^specs/.*\\.md$|^docs/.*\\.md$|^\\.agents/.*\\.md$' | wc -l` | `9` |
| Current spec/review/RPI files directly relevant to this plan | `find . -maxdepth 4 -type f | sed 's#^./##' | rg '^specs/initial_v1\\.md$|^docs/reviews/|^docs/review-prompts/|^\\.agents/rpi/' | wc -l` | `10` |
| Existing Go source/module files | `find . -maxdepth 3 -type f | sed 's#^./##' | rg '\\.(go|mod)$' | wc -l` | `0` |
| Existing research files under `.agents/research/` | `ls -la .agents/research/ 2>/dev/null | head -10` | `none` |
| Beads CLI availability | `command -v bd >/dev/null 2>&1 && echo BD_AVAILABLE || echo BD_MISSING` | `BD_MISSING` |

## File-Conflict Matrix

| File | Issues |
|------|--------|
| `internal/state/types.go` | Issue 1 |
| `internal/state/store.go` | Issue 1 |
| `internal/policy/config.go` | Issue 2 |
| `internal/adapters/ticketstore/tkmd/store.go` | Issue 3 |
| `internal/adapters/ticketstore/tkmd/claims.go` | Issue 4 |
| `internal/adapters/runtime/types.go` | Issue 5 |
| `internal/adapters/runtime/codex/adapter.go` | Issue 6 |
| `internal/adapters/runtime/claude/adapter.go` | Issue 6 |
| `internal/adapters/repo/git/repo.go` | Issue 7 |
| `internal/adapters/verify/command/runner.go` | Issue 8 |
| `internal/engine/intake.go` | Issue 9 |
| `internal/engine/closeout.go` | Issue 9 |
| `internal/engine/ticket_run.go` | Issue 10 |
| `internal/engine/wave_scheduler.go` | Issue 11 |
| `internal/engine/epic_run.go` | Issue 11 |
| `internal/engine/reopen.go` | Issue 12 |
| `internal/engine/resume.go` | Issue 12 |
| `internal/engine/status.go` | Issue 12 |
| `cmd/verk/*.go` | Issue 13 |
| `internal/e2e/*.go` | Issue 14 |

No same-wave shared-file conflicts are planned. Engine work is deliberately serialized after contracts and adapters.

## Cross-Wave Shared Files

| File | Wave 1 Issues | Wave 2+ Issues | Mitigation |
|------|---------------|----------------|------------|
| `internal/state/types.go` | Issue 1 | Issue 8, Issue 9, Issue 10, Issue 11 | Engine waves depend on state/contracts wave |
| `internal/policy/config.go` | Issue 2 | Issue 7, Issue 8, Issue 12 | Persist effective config before engine/CLI wiring |
| `internal/adapters/runtime/types.go` | Issue 5 | Issue 6, Issue 10, Issue 11 | Real runtime adapters and engine execution depend on normalized contracts |
| `internal/adapters/runtime/codex/adapter.go` | Issue 6 | Issue 10, Issue 12, Issue 13 | Engine and operator surfaces must branch from post-Issue-6 runtime behavior |
| `internal/adapters/runtime/claude/adapter.go` | Issue 6 | Issue 10, Issue 12, Issue 13 | Engine and operator surfaces must branch from post-Issue-6 runtime behavior |
| `internal/adapters/repo/git/repo.go` | Issue 7 | Issue 11, Issue 12 | Scheduler/resume must branch from post-Issue-7 state |
| `internal/adapters/verify/command/runner.go` | Issue 8 | Issue 9, Issue 10, Issue 14 | Engine/e2e use the finalized verification contract |

Execution rule: every new wave must branch from the latest merged SHA of the previous wave; no later wave may execute from the original repo baseline once a prior wave has landed.

## Implementation

### 1. Repo and Contract Foundation

Design brief:
This issue creates the non-negotiable contract layer for the whole system. The purpose is to encode the spec’s core enums, artifact types, transition rules, and atomic persistence so later engine work does not invent its own state semantics. Success is defined by typed structs, transition validators, and persistence helpers that all later layers reuse.

In `go.mod`:

- Initialize the module for local development. If no remote path is known yet, bootstrap with `module verk` and keep all imports repo-local.

In `internal/state/types.go`:

- Add `type TicketPhase string`, `type EpicRunStatus string`, `type WaveStatus string`, `type RetryClass string`, `type Severity string`.
- Add constants matching the spec:
  - `TicketPhaseIntake`, `TicketPhaseImplement`, `TicketPhaseVerify`, `TicketPhaseReview`, `TicketPhaseRepair`, `TicketPhaseCloseout`, `TicketPhaseClosed`, `TicketPhaseBlocked`
  - `WaveStatusPlanned`, `WaveStatusRunning`, `WaveStatusAccepted`, `WaveStatusFailed`, `WaveStatusFailedReopened`
- Add artifact structs:
  - `RunArtifact`
  - `WaveArtifact`
  - `PlanArtifact`
  - `ImplementationArtifact`
  - `VerificationArtifact`
  - `ReviewFindingsArtifact`
  - `CloseoutArtifact`
  - `RepairCycleArtifact`
  - `ClaimArtifact`

```go
type ClaimArtifact struct {
	SchemaVersion       int       `json:"schema_version"`
	RunID               string    `json:"run_id"`
	TicketID            string    `json:"ticket_id"`
	OwnerRunID          string    `json:"owner_run_id"`
	OwnerWaveID         string    `json:"owner_wave_id,omitempty"`
	LeaseID             string    `json:"lease_id"`
	LeasedAt            time.Time `json:"leased_at"`
	ExpiresAt           time.Time `json:"expires_at"`
	ReleasedAt          time.Time `json:"released_at,omitempty"`
	ReleaseReason       string    `json:"release_reason,omitempty"`
	State               string    `json:"state"`
	LastSeenLiveClaimPath string  `json:"last_seen_live_claim_path,omitempty"`
}
```

In `internal/state/transitions.go`:

- Add `func ValidateTicketTransition(from, to TicketPhase) error`
- Add `func EffectiveReviewThreshold(cli *Severity, ticket *Severity, cfg Severity) Severity`

In `internal/state/store.go`:

- Add `func SaveJSONAtomic(path string, v any) error`
- Add `func LoadJSON(path string, v any) error`
- Add `func WriteTransitionCommit(...) error` to enforce the commit order described in the spec.

**Tests**

**`internal/state/types_test.go`** — add:
- `TestValidateTicketTransition_AllowsSpecifiedTransitions`
- `TestValidateTicketTransition_RejectsForbiddenTransitions`
- `TestEffectiveReviewThreshold_Precedence`

**`internal/state/store_test.go`** — add:
- `TestSaveJSONAtomic_ReplacesAtomically`
- `TestLoadJSON_MalformedFails`
- `TestWriteTransitionCommit_CrashBeforeRunJSONLeavesUncommittedState`

### 2. Config and Policy

Design brief:
This issue defines the runtime and policy knobs that every other layer consumes. The purpose is to avoid hidden defaults scattered across the engine and adapters. Success is a typed config schema that matches the spec exactly and can be persisted in `run.json`.

In `internal/policy/config.go`:

- Add `type SchedulerConfig`, `PolicyConfig`, `RuntimeConfig`, `VerificationConfig`, `LoggingConfig`, `Config`.
- Add `func LoadConfig(repoRoot string) (Config, error)`.

```go
type PolicyConfig struct {
	ReviewThreshold           state.Severity `yaml:"review_threshold"`
	MaxImplementationAttempts int            `yaml:"max_implementation_attempts"`
	MaxRepairCycles           int            `yaml:"max_repair_cycles"`
	AllowDirtyWorktree        bool           `yaml:"allow_dirty_worktree"`
}
```

In `internal/policy/defaults.go`:

- Add `func DefaultConfig() Config`

**Tests**

**`internal/policy/config_test.go`** — add:
- `TestDefaultConfig_MatchesSpec`
- `TestLoadConfig_OverridesDefaults`
- `TestThresholdPrecedence_CLIWins`

### 3. Ticket Markdown Store

Design brief:
This issue creates the canonical `.tickets/` adapter. The purpose is lossless read/write of ticket markdown plus query support for epic scheduling. Success is round-tripping ticket body/frontmatter and deterministic readiness evaluation.

In `internal/adapters/ticketstore/tkmd/types.go`:

- Add `type Ticket struct` with `ID`, `Title`, `Status`, `Deps`, `Priority`, `AcceptanceCriteria`, `TestCases`, `ValidationCommands`, `OwnedPaths`, `ReviewThreshold`, `Runtime`, `Model`, `Body`, `UnknownFrontmatter`.

In `internal/adapters/ticketstore/tkmd/store.go`:

- Add:
  - `func LoadTicket(path string) (Ticket, error)`
  - `func SaveTicket(path string, t Ticket) error`
  - `func ListReadyChildren(epicID string, claims []ClaimArtifact) ([]Ticket, error)`
  - `func ValidateTicketSchedulingFields(t Ticket) error`

Key rules to implement:
- exact markdown body preservation
- stable frontmatter ordering
- reject `owned_paths` globs
- use ticket-store `status` as the scheduling status

**Tests**

**`internal/adapters/ticketstore/tkmd/store_test.go`** — add:
- `TestLoadSaveTicket_RoundTripsUnknownFrontmatter`
- `TestLoadSaveTicket_PreservesBodyExactly`
- `TestValidateTicketSchedulingFields_RejectsGlobOwnedPaths`
- `TestListReadyChildren_UsesCanonicalReadinessPredicate`

### 4. Claim and Lease Management

Design brief:
This issue makes parallel execution safe. The purpose is deterministic claim acquire, renew, release, fence, and reconcile logic shared by epic execution and resume. Success is a claim layer that rejects stale results, blocks on divergence, and persists both live and durable claim views correctly.

In `internal/adapters/ticketstore/tkmd/claims.go`:

- Add:
  - `func AcquireClaim(...) (state.ClaimArtifact, error)`
  - `func RenewClaim(...) (state.ClaimArtifact, error)`
  - `func ReleaseClaim(...) error`
  - `func ReconcileClaim(live *state.ClaimArtifact, durable *state.ClaimArtifact, runID string, terminal bool) (state.ClaimArtifact, error)`
  - `func ValidateLeaseFence(expected, actual string) error`

**Tests**

**`internal/adapters/ticketstore/tkmd/claims_test.go`** — add:
- `TestAcquireClaim_Exclusive`
- `TestRenewClaim_RejectsStaleLeaseID`
- `TestReleaseClaim_PersistsReleasedStateThenRemovesLiveClaim`
- `TestReconcileClaim_LiveOnlyRebuildsDurableForSameRun`
- `TestReconcileClaim_MismatchedLeaseBlocks`
- `TestValidateLeaseFence_RejectsLateResult`

### 5. Runtime Contracts and Fake Adapter

Design brief:
This issue defines the shared runtime seam before any real runtime integration. The purpose is to prevent Codex and Claude adapters from drifting into incompatible result shapes. Success is a common request/result API and a fake runtime that powers deterministic engine tests.

In `internal/adapters/runtime/types.go`:

- Add:
  - `type WorkerRequest`
  - `type WorkerResult`
  - `type ReviewResult`
  - `type Adapter interface`

```go
type WorkerResult struct {
	Status            string           `json:"status"`
	CompletionCode    string           `json:"completion_code"`
	RetryClass        state.RetryClass `json:"retry_class"`
	StdoutPath        string           `json:"stdout_path,omitempty"`
	StderrPath        string           `json:"stderr_path,omitempty"`
	ResultArtifactPath string          `json:"result_artifact_path,omitempty"`
	LeaseID           string           `json:"lease_id"`
	StartedAt         time.Time        `json:"started_at"`
	FinishedAt        time.Time        `json:"finished_at"`
}
```

In `internal/adapters/runtime/fake/fake.go`:

- Add fake implementations returning scripted worker/reviewer results.

**Tests**

**`internal/adapters/runtime/types_test.go`** — add:
- `TestWorkerResult_OnlyAllowsCanonicalImplementerStatuses`
- `TestReviewResult_NormalizesFindings`
- `TestReviewArtifact_DerivedPassFailMustMatchFindings`

### 6. Real Codex and Claude Runtime Adapters

Design brief:
This issue turns the shared runtime seam into real execution behavior. The purpose is to support Codex and Claude in v1 without letting adapter-specific behavior leak into the engine. Success is two concrete adapters that can launch worker and reviewer runs, propagate lease fencing, normalize results and findings, classify retries, and expose availability checks for operator surfaces.

In `internal/adapters/runtime/codex/adapter.go`:

- Add:
  - `type Adapter struct`
  - `func (a Adapter) RunWorker(ctx context.Context, req WorkerRequest) (WorkerResult, error)`
  - `func (a Adapter) RunReviewer(ctx context.Context, req WorkerRequest) (ReviewResult, error)`
  - `func (a Adapter) CheckAvailability(ctx context.Context) error`

In `internal/adapters/runtime/claude/adapter.go`:

- Add the same adapter surface for Claude.

Key rules to implement:
- include `lease_id` in worker and reviewer invocations and require it in normalized results
- capture stdout, stderr, and result artifact paths
- map runtime-specific outcomes to canonical implementer statuses and `RetryClass`
- normalize reviewer findings so engine-derived pass/fail can be recomputed deterministically
- treat availability checks as explicit adapter behavior used by `doctor`, not as incidental shell probing

**Tests**

**`internal/adapters/runtime/codex/adapter_test.go`** — add:
- `TestCodexAdapter_RunWorkerNormalizesCanonicalResult`
- `TestCodexAdapter_PropagatesLeaseID`

**`internal/adapters/runtime/claude/adapter_test.go`** — add:
- `TestClaudeAdapter_RunReviewerNormalizesFindings`
- `TestClaudeAdapter_ClassifiesRetryableVsBlockingFailures`

### 7. Git Baseline and Scope Adapter

Design brief:
This issue builds the git-based scope and baseline logic used by scheduler, closeout, and resume. The purpose is to turn the spec’s conflict and dirty-worktree rules into a deterministic adapter. Success is baseline-aware changed-file computation and canonical path normalization.

In `internal/adapters/repo/git/repo.go`:

- Add:
  - `func RepoRoot() (string, error)`
  - `func HeadCommit() (string, error)`
  - `func ChangedFilesAgainst(base string) ([]string, error)`
  - `func NormalizeOwnedPath(repoRoot, path string) (string, error)`
  - `func PathsOverlap(a, b string) bool`
  - `func EnsureCleanWorktree() error`

**Tests**

**`internal/adapters/repo/git/repo_test.go`** — add:
- `TestNormalizeOwnedPath_RejectsRepoEscape`
- `TestPathsOverlap_RespectsSegmentBoundaries`
- `TestEnsureCleanWorktree_FailsOnDirtyRepo`

### 8. Deterministic Verification Runner

Design brief:
This issue makes verification auditable. The purpose is to convert command execution into durable per-command evidence that the engine can derive pass/fail from. Success is deterministic artifact capture for each command plus a derived verification verdict.

In `internal/adapters/verify/command/runner.go`:

- Add:
  - `type CommandResult`
  - `func RunCommands(ctx context.Context, repoRoot string, cmds []string, cfg policy.VerificationConfig) ([]CommandResult, error)`
  - `func DeriveVerificationPassed(results []CommandResult) bool`

**Tests**

**`internal/adapters/verify/command/runner_test.go`** — add:
- `TestRunCommands_CapturesExitCodeAndArtifacts`
- `TestRunCommands_TimeoutMarksTimedOut`
- `TestDeriveVerificationPassed_FailsOnNonZeroExit`

### 9. Intake and Closeout

Design brief:
This issue turns the spec’s planning and closure rules into code. The purpose is to normalize ticket metadata into an executable plan and to ensure closeout is derived from artifacts and thresholds, not assertions. Success is deterministic intake and a closeout gate that can explain exactly why a ticket cannot close.

In `internal/engine/intake.go`:

- Add `func BuildPlanArtifact(t tkmd.Ticket, cfg policy.Config) (state.PlanArtifact, error)`

In `internal/engine/closeout.go`:

- Add:
  - `func DeriveGateResults(...) (map[string]GateResult, error)`
  - `func BuildCloseoutArtifact(...) (state.CloseoutArtifact, error)`
  - `func ReviewFindingBlocks(f Finding, threshold state.Severity) bool`

**Tests**

**`internal/engine/intake_test.go`** — add:
- `TestBuildPlanArtifact_PersistsEffectiveReviewThreshold`
- `TestBuildPlanArtifact_RejectsMissingOwnedPathsForEpic`

**`internal/engine/closeout_test.go`** — add:
- `TestBuildCloseoutArtifact_FailsOnCrossRunEvidence`
- `TestBuildCloseoutArtifact_FailsOnMissingWaiverMetadata`
- `TestBuildCloseoutArtifact_GateResultsAreTyped`

### 10. Ticket Execution Loop

Design brief:
This issue implements the inner ratchet. The purpose is to make one ticket move deterministically through implement, verify, review, repair, and closeout while persisting every state transition. Success is a resumable ticket loop that respects lease fencing, repair limits, and derived closure logic.

In `internal/engine/ticket_run.go`:

- Add:
  - `func RunTicket(ctx context.Context, req RunTicketRequest) error`
  - `func handleImplementResult(...)`
  - `func handleVerificationFailure(...)`
  - `func handleReviewOutcome(...)`

Key reuse:
- state transition validators from `internal/state/transitions.go`
- verification derivation from `internal/adapters/verify/command/runner.go`

**Tests**

**`internal/engine/ticket_run_test.go`** — add:
- `TestRunTicket_HappyPath`
- `TestRunTicket_VerifyFailureLoopsToImplement`
- `TestRunTicket_RepairLimitBlocks`
- `TestRunTicket_RejectsLateResultWithStaleLeaseID`

### 11. Wave Scheduler and Epic Loop

Design brief:
This issue implements the outer execution model. The purpose is to build waves from real dependency and scope data, then execute tickets safely in parallel without same-wave conflicts. Success is an epic loop that produces accepted or failed waves with durable checkpoints.

In `internal/engine/wave_scheduler.go`:

- Add:
  - `func BuildWave(ready []tkmd.Ticket, maxConcurrency int) ([]tkmd.Ticket, error)`
  - `func CheckScopeViolation(changed, owned []string) error`

In `internal/engine/epic_run.go`:

- Add:
  - `func RunEpic(ctx context.Context, req RunEpicRequest) error`
  - `func AcceptWave(...) error`

**Tests**

**`internal/engine/epic_run_test.go`** — add:
- `TestBuildWave_SerializesConflictingOwnedPaths`
- `TestRunEpic_UsesReadyPredicate`
- `TestAcceptWave_FailsOnScopeViolation`
- `TestRunEpic_BlockedTicketPreventsFalseCompletion`

### 12. Resume, Reopen, Status, Doctor

Design brief:
This issue covers operational control surfaces. The purpose is to make failed and interrupted runs inspectable and restartable without manual file edits. Success is deterministic resume, explicit reopen, and diagnostics strong enough to support operators.

In `internal/engine/reopen.go`:

- Add `func ReopenTicket(ctx context.Context, req ReopenRequest) error`

In `internal/engine/resume.go`:

- Add `func ResumeRun(ctx context.Context, runID string) error`

In `internal/engine/status.go`:

- Add `func DeriveStatus(runID string) (StatusReport, error)`

In `internal/engine/doctor.go`:

- Add `func RunDoctor(repoRoot string) (DoctorReport, int, error)`

**Tests**

**`internal/engine/resume_test.go`** — add:
- `TestResumeRun_BlocksOnClaimDivergence`
- `TestResumeRun_RepairsCommittedTransitionAfterCrash`

**`internal/engine/reopen_test.go`** — add:
- `TestReopenTicket_BlockedToImplement`
- `TestReopenTicket_ClosedRepairMarksWaveFailedReopened`

**`internal/engine/status_test.go`** — add:
- `TestDeriveStatus_UsesRunArtifactsAndClaimsOnly`

### 13. CLI Surface

Design brief:
This issue exposes the engine without duplicating orchestration logic. The purpose is to provide the exact v1 commands promised by the spec and to keep command handlers thin. Success is a CLI that passes through to engine APIs and emits both human and JSON status surfaces.

In `cmd/verk/root.go` and peers:

- Add commands:
  - `run ticket`
  - `run epic`
  - `reopen`
  - `resume`
  - `status`
  - `doctor`

**Tests**

**`cmd/verk/main_test.go`** — add:
- `TestStatusJSON_EmitsMachineReadableReport`
- `TestDoctor_ExitCodes`
- `TestReopen_ValidatesTargetPhase`

### 14. End-to-End Validation

Design brief:
This issue proves the system matches the spec under realistic flows. The purpose is to verify the contract across all layers, especially the high-risk cases called out in the external reviews. Success is an end-to-end suite that exercises happy path, repair path, crash/recovery, claim divergence, and evidence integrity.

**`internal/e2e/ticket_happy_path_test.go`** — add:
- `TestTicketHappyPath`

**`internal/e2e/ticket_repair_cycle_test.go`** — add:
- `TestTicketRepairCycle`
- `TestWaivedBlockingFindingWithoutMetadataFails`

**`internal/e2e/epic_multi_wave_test.go`** — add:
- `TestEpicMultipleWavesNoConflicts`
- `TestEpicConflictSerialization`

**`internal/e2e/resume_claim_recovery_test.go`** — add:
- `TestResumeBlocksOnLiveDurableClaimDivergence`
- `TestLateWorkerResultRejectedAfterReacquisition`
- `TestCopiedEvidenceFromPreviousRunFailsCloseout`

## Tests

**New test files**
- `internal/state/types_test.go`
- `internal/state/store_test.go`
- `internal/policy/config_test.go`
- `internal/adapters/ticketstore/tkmd/store_test.go`
- `internal/adapters/ticketstore/tkmd/claims_test.go`
- `internal/adapters/runtime/types_test.go`
- `internal/adapters/runtime/codex/adapter_test.go`
- `internal/adapters/runtime/claude/adapter_test.go`
- `internal/adapters/repo/git/repo_test.go`
- `internal/adapters/verify/command/runner_test.go`
- `internal/engine/intake_test.go`
- `internal/engine/closeout_test.go`
- `internal/engine/ticket_run_test.go`
- `internal/engine/epic_run_test.go`
- `internal/engine/reopen_test.go`
- `internal/engine/resume_test.go`
- `internal/engine/status_test.go`
- `cmd/verk/main_test.go`
- `internal/e2e/ticket_happy_path_test.go`
- `internal/e2e/ticket_repair_cycle_test.go`
- `internal/e2e/epic_multi_wave_test.go`
- `internal/e2e/resume_claim_recovery_test.go`

## Conformance Checks

| Issue | Check Type | Check |
|-------|-----------|-------|
| Issue 1 | `files_exist` | `["go.mod","internal/state/types.go","internal/state/store.go"]` |
| Issue 1 | `content_check` | `{file: "internal/state/transitions.go", pattern: "func ValidateTicketTransition"}` |
| Issue 2 | `content_check` | `{file: "internal/policy/config.go", pattern: "type Config struct"}` |
| Issue 3 | `content_check` | `{file: "internal/adapters/ticketstore/tkmd/store.go", pattern: "func LoadTicket"}` |
| Issue 4 | `tests` | `go test ./internal/adapters/ticketstore/tkmd -run 'TestAcquireClaim|TestRenewClaim|TestReleaseClaim|TestReconcileClaim|TestValidateLeaseFence' -v` |
| Issue 5 | `content_check` | `{file: "internal/adapters/runtime/types.go", pattern: "type WorkerResult struct"}` |
| Issue 6 | `tests` | `go test ./internal/adapters/runtime/... -run 'TestCodexAdapter|TestClaudeAdapter' -v` |
| Issue 7 | `content_check` | `{file: "internal/adapters/repo/git/repo.go", pattern: "func ChangedFilesAgainst"}` |
| Issue 8 | `tests` | `go test ./internal/adapters/verify/command -run 'TestRunCommands|TestDeriveVerificationPassed' -v` |
| Issue 9 | `tests` | `go test ./internal/engine -run 'TestBuildPlanArtifact|TestBuildCloseoutArtifact|TestReviewFindingBlocks' -v` |
| Issue 10 | `tests` | `go test ./internal/engine -run 'TestRunTicket|TestRunTicket_RejectsLateResultWithStaleLeaseID' -v` |
| Issue 11 | `tests` | `go test ./internal/engine -run 'TestBuildWave|TestRunEpic|TestAcceptWave' -v` |
| Issue 12 | `tests` | `go test ./internal/engine -run 'TestResumeRun|TestReopenTicket|TestDeriveStatus' -v` |
| Issue 13 | `tests` | `go test ./cmd/verk -run 'TestStatusJSON|TestDoctor|TestReopen' -v` |
| Issue 14 | `tests` | `go test ./internal/e2e -run 'Test.*' -v` |

## Verification

1. **State and policy layer**
```bash
go test ./internal/state ./internal/policy -v
```

2. **Adapter layer**
```bash
go test ./internal/adapters/... -v
```

3. **Engine layer**
```bash
go test ./internal/engine -v
```

4. **CLI layer**
```bash
go test ./cmd/verk -v
```

5. **Full build and suite**
```bash
go build ./...
go test ./... -timeout 180s
```

6. **Manual simulation**
```bash
# Load reproducible fixtures
mkdir -p .tickets .verk/runs
cp testdata/manual/config.yaml .verk/config.yaml
cp testdata/manual/tickets/*.md .tickets/
cp -R testdata/manual/runs/test-run .verk/runs/
cp -R testdata/manual/runs/failed-closeout-run .verk/runs/

# Doctor should succeed and report repo/ticket/runtime checks explicitly
go run ./cmd/verk doctor

# Healthy run fixture should emit machine-readable status with effective threshold and no claim divergence
go run ./cmd/verk status test-run --json

# Failed closeout fixture should expose failed gate results and the blocking reason
go run ./cmd/verk status failed-closeout-run --json
```

Expected manual outcomes:

- `doctor` exits `0` and reports repo root, ticket-store readability, and availability status for configured runtimes.
- `status test-run --json` includes `effective_review_threshold`, ticket phase summaries, and `claim_divergence: false`.
- `status failed-closeout-run --json` includes a failed closeout or blocked status with typed `gate_results` explaining the rejection.

## Issues

### Issue 1: Initialize repo contracts and atomic state layer
**Dependencies:** None
**Acceptance:** Core artifact structs, transition validators, and atomic JSON persistence exist and are covered by unit tests.
**Description:** Create the module, core enums, artifact structs, transition validators, and atomic persistence helpers described in Implementation section 1.

### Issue 2: Add config and policy loading
**Dependencies:** Issue 1
**Acceptance:** Config schema matches the spec, defaults are encoded once, and precedence logic is test-covered.
**Description:** Implement typed config loading and policy defaults from Implementation section 2.

### Issue 3: Build tk-compatible ticket markdown adapter
**Dependencies:** Issue 1
**Acceptance:** Ticket markdown round-trips exactly, unknown frontmatter survives, and readiness predicates use canonical ticket-store status.
**Description:** Implement the `.tickets/` store and scheduling-field validation from Implementation section 3.

### Issue 4: Implement claim, lease, release, and reconciliation logic
**Dependencies:** Issue 1, Issue 3
**Acceptance:** Claims can be acquired, renewed, released, reconciled, and fenced deterministically with tests for divergence and stale leases.
**Description:** Implement the claim lifecycle from Implementation section 4.

### Issue 5: Define runtime adapter contracts and fake adapter
**Dependencies:** Issue 1
**Acceptance:** Common worker/reviewer request-result structs exist, fake runtime supports engine tests, and canonical enums are enforced.
**Description:** Implement the runtime contract seam from Implementation section 5.

### Issue 6: Implement real Codex and Claude runtime adapters
**Dependencies:** Issue 2, Issue 5
**Acceptance:** Codex and Claude adapters can launch workers and reviewers, propagate `lease_id`, normalize canonical results and findings, classify retry behavior, and expose availability checks.
**Description:** Implement the real runtime adapters from Implementation section 6.

### Issue 7: Add git baseline and path normalization adapter
**Dependencies:** Issue 1
**Acceptance:** Repo root, base commit, changed files, overlap detection, and dirty-worktree checks are available with deterministic tests.
**Description:** Implement the git and path-baseline logic from Implementation section 7.

### Issue 8: Add deterministic command verification runner
**Dependencies:** Issue 1, Issue 2
**Acceptance:** Verification commands produce per-command evidence, derived pass/fail results, and command-level failure details suitable for closeout and review auditing.
**Description:** Implement the verification runner from Implementation section 8.

### Issue 9: Implement intake and closeout gate evaluation
**Dependencies:** Issue 1, Issue 2, Issue 3, Issue 8
**Acceptance:** Plan artifacts and closeout artifacts are built deterministically, including effective thresholds, typed gate results, waiver validation, and cross-run evidence rejection.
**Description:** Implement intake and closeout derivation from Implementation section 9.

### Issue 10: Implement ticket execution loop
**Dependencies:** Issue 1, Issue 4, Issue 5, Issue 6, Issue 8, Issue 9
**Acceptance:** Ticket ratchet executes deterministically with repair loops, late-result rejection, persisted transitions, and runtime results rejected when lease fencing fails.
**Description:** Implement the ticket loop from Implementation section 10.

### Issue 11: Implement wave scheduler and epic loop
**Dependencies:** Issue 3, Issue 4, Issue 6, Issue 7, Issue 9, Issue 10
**Acceptance:** Ready tickets are grouped into non-conflicting waves, executed, and accepted or failed based on durable rules, with scope violations and reopen side effects reflected in wave state.
**Description:** Implement the epic outer loop from Implementation section 11.

### Issue 12: Implement reopen, resume, status, and doctor
**Dependencies:** Issue 4, Issue 6, Issue 7, Issue 9, Issue 10, Issue 11
**Acceptance:** Runs can be resumed and reopened without manual artifact edits, doctor reports runtime availability explicitly, and reopen mutates ticket, wave, and run artifacts consistently.
**Description:** Implement the control surfaces from Implementation section 12.

### Issue 13: Build CLI commands
**Dependencies:** Issue 2, Issue 10, Issue 11, Issue 12
**Acceptance:** The v1 CLI surface exists and maps directly to engine APIs.
**Description:** Wire the command surface from Implementation section 13.

### Issue 14: Add end-to-end validation suite
**Dependencies:** Issue 13
**Acceptance:** E2E tests and manual fixtures cover the high-risk flows identified in the spec and external reviews, including claim fencing, cross-run evidence rejection, and failed closeout status reporting.
**Description:** Implement the scenario tests and manual fixtures from Implementation section 14.

## Execution Order

Every new wave must branch from the latest merged SHA of the previous wave; no later wave may execute from the original repo baseline once a prior wave has landed.

**Wave 1** (parallel): Issue 1, Issue 3, Issue 5, Issue 7  
**Wave 2** (after Wave 1): Issue 2, Issue 4, Issue 6, Issue 8  
**Wave 3** (after Waves 1-2): Issue 9  
**Wave 4** (after Wave 3): Issue 10  
**Wave 5** (after Wave 4): Issue 11  
**Wave 6** (after Wave 5): Issue 12  
**Wave 7** (after Wave 6): Issue 13  
**Wave 8** (after Wave 7): Issue 14

## Post-Merge Cleanup

After implementation waves complete:

- search modified files for placeholder names and scaffolding leftovers
- run `grep -rn 'TODO\\|FIXME\\|HACK\\|XXX' internal cmd testdata` and either remove or explicitly justify every hit
- re-read exported structs and enums for spec naming parity

## Next Steps

- Run `/pre-mortem` against this plan before starting execution
- Then run `/crank` for autonomous implementation
- If you want to stay narrow, start with Issue 1 as the first implementation slice
