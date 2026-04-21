// Package engine — epic closure gate.
//
// This file implements the epic-level closure gate that runs after all child
// waves complete but before the epic is marked completed. The gate:
//
//  1. Executes configured EpicClosureCommands (broad, potentially expensive
//     commands like `go test ./internal/e2e/...` that are too slow to repeat
//     on every ticket or wave).
//  2. Derives epic-scope stale-wording checks across configured documentation
//     paths so cross-ticket doc inconsistencies are caught before close.
//  3. Invokes an epic reviewer with the canonical EpicReviewFraming wording,
//     supplying child ticket context, artifact paths, and check evidence.
//  4. Routes fixable findings to a repair worker in a bounded cycle
//     (MaxEpicRepairCycles). Unresolved findings mark the epic blocked.
//  5. Persists an EpicClosureArtifact recording the full closure history.
package engine

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"verk/internal/adapters/runtime"
	"verk/internal/adapters/ticketstore/tkmd"
	"verk/internal/policy"
	"verk/internal/state"

	verifycommand "verk/internal/adapters/verify/command"
)

const epicGateOutputLimit = 8 * 1024 // 8 KB per failing command

// defaultEpicClosureDocs is the fallback doc path list used when
// EpicClosureDocs is empty. These are the most common locations for
// cross-ticket documentation inconsistencies.
var defaultEpicClosureDocs = []string{"README.md", "CONTRIBUTING.md", "docs"}

// runEpicClosureGate executes the epic-level broad quality commands, derived
// stale-wording checks, and an epic reviewer pass before the epic closes. It
// implements a bounded repair loop (MaxEpicRepairCycles) and persists an
// EpicClosureArtifact recording findings, cycles, and the final Closable
// outcome.
//
// Returns nil when all checks pass and the reviewer finds no blocking gaps.
// Returns a *BlockedRunError (wrapping ErrEpicBlocked) when repair is
// exhausted or when operator input is required.
//
//nolint:cyclop // explicit terminal branches keep closure-gate failure handling readable.
func runEpicClosureGate(
	ctx context.Context,
	req RunEpicRequest,
	cfg policy.Config,
	children []tkmd.Ticket,
	changedFiles []string,
) error {
	childIDs := sortedChildIDs(children)

	// --- Build the initial artifact so partial progress is persisted. ---
	artifactPath := epicClosureArtifactPath(req.RepoRoot, req.RunID)
	reviewProfile := cfg.EffectiveReviewerProfile()
	now := stateTime()
	artifact := state.EpicClosureArtifact{
		ArtifactMeta: state.ArtifactMeta{
			SchemaVersion: artifactSchemaVersion,
			RunID:         req.RunID,
			CreatedAt:     now,
			UpdatedAt:     now,
		},
		EpicID:          req.RootTicketID,
		ChildTicketIDs:  childIDs,
		BroadCommands:   epicBroadCommandLines(cfg.Verification.EpicClosureCommands),
		ReviewerRuntime: reviewProfile.Runtime,
		ReviewerModel:   reviewProfile.Model,
	}

	// --- Derive epic-scope checks (stale wording across doc paths). ---
	derivedChecks := deriveEpicScopedChecks(req.RootTicketID, cfg.Verification)
	artifact.DerivedCommands = epicDerivedCommandLines(derivedChecks)

	// --- Build coverage artifact (declared broad + derived). ---
	coverage := newEpicValidationCoverage(
		req.RunID, req.RootTicketID, childIDs,
		cfg.Verification.EpicClosureCommands, derivedChecks,
	)
	artifact.Coverage = coverage

	if err := state.SaveJSONAtomic(artifactPath, artifact); err != nil {
		return fmt.Errorf("epic closure gate: persist initial artifact: %w", err)
	}

	SendProgress(ctx, req.Progress, ProgressEvent{
		Type:   EventTicketDetail,
		Detail: fmt.Sprintf("epic closure gate: running checks for %s", req.RootTicketID),
	})

	// --- Execute broad closure commands + derived stale-wording checks. ---
	hasClosureCmds := len(cfg.Verification.EpicClosureCommands) > 0
	hasDerivedCmds := epicDerivedHasCommand(derivedChecks)

	var (
		broadResults   []verifycommand.CommandResult
		derivedResults map[string]verifycommand.CommandResult
	)

	if hasClosureCmds || hasDerivedCmds {
		var err error
		broadResults, derivedResults, err = executeEpicChecks(
			ctx, req.RepoRoot, cfg.Verification, hasClosureCmds, derivedChecks,
		)
		if err != nil {
			return fmt.Errorf("epic closure gate: run checks: %w", err)
		}
		recordEpicCheckExecutions(coverage, req.RootTicketID, broadResults, derivedChecks, derivedResults, 0)
	}
	checksAllPassed := epicChecksAllPassed(broadResults, derivedChecks, derivedResults)

	// --- Resolve adapter for reviewer and repair workers. ---
	adapter, err := resolveWaveAdapter(req)
	if err != nil {
		return fmt.Errorf("epic closure gate: resolve adapter: %w", err)
	}
	workerProfile := cfg.EffectiveWorkerProfile()

	// --- Load child snapshots for reviewer context. ---
	childSnapshots := loadChildSnapshotsForEpicGate(req.RepoRoot, req.RunID, childIDs)

	// --- Run the epic reviewer (attempt 1). ---
	reviewAttempt := 1
	reviewResult, reviewErr := runEpicReviewerAttempt(
		ctx, req, cfg, adapter, reviewProfile,
		childIDs, childSnapshots, children, changedFiles,
		checksAllPassed, broadResults, derivedChecks, derivedResults,
		reviewAttempt,
	)

	// Map reviewer findings; merge with check-failure findings.
	if reviewErr == nil {
		newFindings := mapReviewToEpicFindings(reviewResult, cfg.Policy.ReviewThreshold, children)
		artifact.Findings = mergeEpicFindings(artifact.Findings, newFindings)
	}
	checkFindings := collectEpicCheckFailureFindings(broadResults, derivedChecks, derivedResults, 0)
	artifact.Findings = mergeEpicFindings(artifact.Findings, checkFindings)

	allClear := checksAllPassed && reviewErr == nil && !hasUnresolvedFindings(artifact.Findings)
	if allClear {
		epicCloseSuccess(&artifact, coverage, 0)
		artifact.UpdatedAt = stateTime()
		if saveErr := saveEpicClosureArtifact(artifactPath, artifact, "persist success artifact"); saveErr != nil {
			return saveErr
		}
		SendProgress(ctx, req.Progress, ProgressEvent{
			Type:   EventTicketDetail,
			Detail: fmt.Sprintf("epic closure gate: all checks passed for %s", req.RootTicketID),
		})
		return nil
	}

	if reviewErr != nil {
		blockReason := buildEpicBlockReason(checksAllPassed, artifact.Findings, reviewErr)
		epicCloseBlocked(&artifact, coverage, blockReason, 0, "epic reviewer failed before repair")
		artifact.UpdatedAt = stateTime()
		if saveErr := saveEpicClosureArtifact(artifactPath, artifact, "persist reviewer error block"); saveErr != nil {
			return saveErr
		}
		return &BlockedRunError{
			RunID:  req.RunID,
			Status: state.EpicRunStatusBlocked,
			Cause:  fmt.Errorf("epic gate reviewer failed: %s", blockReason),
		}
	}

	// --- Handle repair-disabled case. ---
	if cfg.Policy.MaxEpicRepairCycles == 0 {
		blockReason := buildEpicBlockReason(checksAllPassed, artifact.Findings, reviewErr)
		epicCloseBlocked(&artifact, coverage, blockReason, 0, "epic repair disabled by policy.max_epic_repair_cycles=0")
		artifact.UpdatedAt = stateTime()
		if saveErr := saveEpicClosureArtifact(artifactPath, artifact, "persist repair-disabled block"); saveErr != nil {
			return saveErr
		}
		return &BlockedRunError{
			RunID:  req.RunID,
			Status: state.EpicRunStatusBlocked,
			Cause:  fmt.Errorf("epic gate blocked (repair disabled): %s", blockReason),
		}
	}

	// --- Bounded repair loop. ---
	for cycle := 1; cycle <= cfg.Policy.MaxEpicRepairCycles; cycle++ {
		SendProgress(ctx, req.Progress, ProgressEvent{
			Type:   EventTicketDetail,
			Detail: fmt.Sprintf("epic closure gate: repair cycle %d/%d for %s", cycle, cfg.Policy.MaxEpicRepairCycles, req.RootTicketID),
		})

		// Record the cycle before dispatching so a crash is detectable on resume.
		cycleRec := state.EpicClosureCycle{
			Cycle:           cycle,
			StartedAt:       stateTime(),
			Status:          "repair_pending",
			TriggerFindings: collectUnresolvedFindingIDs(artifact.Findings),
		}
		artifact.Cycles = append(artifact.Cycles, cycleRec)
		artifact.UpdatedAt = stateTime()
		if saveErr := state.SaveJSONAtomic(artifactPath, artifact); saveErr != nil {
			return fmt.Errorf("epic gate: persist cycle %d state: %w", cycle, saveErr)
		}

		// Dispatch a repair worker with context about failing checks and findings.
		repairInstructions := buildEpicRepairInstructions(
			req.RootTicketID, artifact.Findings, broadResults, derivedChecks, derivedResults, cycle,
		)
		repairLeaseID := fmt.Sprintf("epic-repair-%s-%d", req.RootTicketID, cycle)
		_, repairErr := adapter.RunWorker(ctx, runtime.WorkerRequest{
			RunID:           req.RunID,
			TicketID:        req.RootTicketID,
			LeaseID:         repairLeaseID,
			Attempt:         cycle,
			Runtime:         workerProfile.Runtime,
			Model:           workerProfile.Model,
			Reasoning:       workerProfile.Reasoning,
			WorktreePath:    req.RepoRoot,
			Instructions:    repairInstructions,
			ExecutionConfig: executionConfigFromPolicy(cfg),
			OnProgress: func(detail string) {
				SendProgress(ctx, req.Progress, ProgressEvent{
					Type:   EventTicketDetail,
					Detail: fmt.Sprintf("epic repair (cycle %d): %s", cycle, detail),
				})
			},
		})

		cycleIdx := len(artifact.Cycles) - 1
		if repairErr != nil {
			artifact.Cycles[cycleIdx].Status = "repair_failed"
			artifact.Cycles[cycleIdx].FinishedAt = stateTime()
			artifact.Cycles[cycleIdx].RepairNotes = repairErr.Error()
			blockReason := fmt.Sprintf("epic repair cycle %d worker failed: %v", cycle, repairErr)
			epicCloseBlocked(&artifact, coverage, blockReason, cycle, blockReason)
			artifact.UpdatedAt = stateTime()
			if saveErr := saveEpicClosureArtifact(artifactPath, artifact, fmt.Sprintf("persist repair cycle %d failure", cycle)); saveErr != nil {
				return saveErr
			}
			return &BlockedRunError{
				RunID:  req.RunID,
				Status: state.EpicRunStatusBlocked,
				Cause:  fmt.Errorf("epic gate repair cycle %d: %w", cycle, repairErr),
			}
		}

		// Re-run checks after repair.
		if hasClosureCmds || hasDerivedCmds {
			broadResults, derivedResults, err = executeEpicChecks(
				ctx, req.RepoRoot, cfg.Verification, hasClosureCmds, derivedChecks,
			)
			if err != nil {
				return fmt.Errorf("epic gate: run checks after repair cycle %d: %w", cycle, err)
			}
			recordEpicCheckExecutions(coverage, req.RootTicketID, broadResults, derivedChecks, derivedResults, cycle)
		}
		checksAllPassed = epicChecksAllPassed(broadResults, derivedChecks, derivedResults)

		// Re-run the epic reviewer.
		reviewAttempt++
		reviewResult, reviewErr = runEpicReviewerAttempt(
			ctx, req, cfg, adapter, reviewProfile,
			childIDs, childSnapshots, children, changedFiles,
			checksAllPassed, broadResults, derivedChecks, derivedResults,
			reviewAttempt,
		)

		// Refresh findings: resolve ones that are no longer present, add new ones.
		if reviewErr == nil {
			freshFindings := mapReviewToEpicFindings(reviewResult, cfg.Policy.ReviewThreshold, children)
			resolveRepairedFindings(artifact.Findings, freshFindings)
			artifact.Findings = mergeEpicFindings(artifact.Findings, freshFindings)
		}
		freshCheckFindings := collectEpicCheckFailureFindings(broadResults, derivedChecks, derivedResults, cycle)
		artifact.Findings = mergeEpicFindings(artifact.Findings, freshCheckFindings)

		artifact.Cycles[cycleIdx].Status = "completed"
		artifact.Cycles[cycleIdx].FinishedAt = stateTime()

		if reviewErr != nil {
			artifact.Cycles[cycleIdx].Status = "review_failed"
			blockReason := buildEpicBlockReason(checksAllPassed, artifact.Findings, reviewErr)
			epicCloseBlocked(&artifact, coverage, blockReason, cycle, blockReason)
			artifact.UpdatedAt = stateTime()
			if saveErr := saveEpicClosureArtifact(artifactPath, artifact, fmt.Sprintf("persist cycle %d reviewer error block", cycle)); saveErr != nil {
				return saveErr
			}
			return &BlockedRunError{
				RunID:  req.RunID,
				Status: state.EpicRunStatusBlocked,
				Cause:  fmt.Errorf("epic gate reviewer failed after repair cycle %d: %s", cycle, blockReason),
			}
		}

		allClear = checksAllPassed && reviewErr == nil && !hasUnresolvedFindings(artifact.Findings)
		if allClear {
			epicCloseSuccess(&artifact, coverage, cycle)
			artifact.UpdatedAt = stateTime()
			if saveErr := saveEpicClosureArtifact(artifactPath, artifact, fmt.Sprintf("persist cycle %d success", cycle)); saveErr != nil {
				return saveErr
			}
			SendProgress(ctx, req.Progress, ProgressEvent{
				Type:   EventTicketDetail,
				Detail: fmt.Sprintf("epic closure gate: all checks passed after %d repair cycle(s) for %s", cycle, req.RootTicketID),
			})
			return nil
		}

		artifact.UpdatedAt = stateTime()
		if saveErr := state.SaveJSONAtomic(artifactPath, artifact); saveErr != nil {
			return fmt.Errorf("epic gate: persist cycle %d result: %w", cycle, saveErr)
		}
	}

	// --- Exhausted repair budget. ---
	blockReason := buildEpicBlockReason(checksAllPassed, artifact.Findings, reviewErr)
	limitReason := fmt.Sprintf("epic repair exhausted after %d cycle(s)", cfg.Policy.MaxEpicRepairCycles)
	epicCloseBlocked(&artifact, coverage, blockReason, cfg.Policy.MaxEpicRepairCycles, limitReason)
	artifact.UpdatedAt = stateTime()
	if saveErr := saveEpicClosureArtifact(artifactPath, artifact, "persist repair-exhausted block"); saveErr != nil {
		return saveErr
	}

	return &BlockedRunError{
		RunID:  req.RunID,
		Status: state.EpicRunStatusBlocked,
		Cause:  fmt.Errorf("epic gate blocked after %d repair cycle(s): %s", cfg.Policy.MaxEpicRepairCycles, blockReason),
	}
}

// ---- Artifact helpers -------------------------------------------------------

// epicCloseSuccess marks the artifact and coverage as closable.
func epicCloseSuccess(artifact *state.EpicClosureArtifact, coverage *state.ValidationCoverageArtifact, cycles int) {
	artifact.Closable = true
	if cycles == 0 {
		artifact.ClosureReason = "all epic-level checks passed and reviewer found no blocking gaps"
	} else {
		artifact.ClosureReason = fmt.Sprintf("all epic-level checks passed after %d repair cycle(s)", cycles)
	}
	if coverage != nil {
		coverage.Closable = true
		coverage.UpdatedAt = stateTime()
		coverage.ClosureReason = artifact.ClosureReason
	}
}

func saveEpicClosureArtifact(artifactPath string, artifact state.EpicClosureArtifact, reason string) error {
	if err := state.SaveJSONAtomic(artifactPath, artifact); err != nil {
		return fmt.Errorf("epic closure gate: %s: %w", reason, err)
	}
	return nil
}

// epicCloseBlocked marks the artifact and coverage as non-closable and records
// the repair limit when repair cycles were exhausted.
func epicCloseBlocked(
	artifact *state.EpicClosureArtifact,
	coverage *state.ValidationCoverageArtifact,
	blockReason string,
	cycles int,
	limitReason string,
) {
	artifact.Closable = false
	artifact.BlockReason = blockReason
	if cycles > 0 && limitReason != "" {
		limit := &state.ValidationRepairLimit{
			Name:      "max_epic_repair_cycles",
			Limit:     cycles,
			Reached:   cycles,
			Reason:    limitReason,
			PolicyRef: "policy.max_epic_repair_cycles",
		}
		artifact.RepairLimit = limit
		if coverage != nil {
			coverage.RepairLimit = limit
		}
	}
	if coverage != nil {
		coverage.Closable = false
		coverage.UpdatedAt = stateTime()
		if coverage.BlockReason == "" {
			coverage.BlockReason = blockReason
		}
	}
}

// ---- Coverage helpers -------------------------------------------------------

// newEpicValidationCoverage initializes a ValidationCoverageArtifact scoped
// to the epic. Broad closure commands become DeclaredChecks; derived
// stale-wording commands become DerivedChecks.
func newEpicValidationCoverage(
	runID, epicID string,
	childIDs []string,
	closureCmds []policy.QualityCommand,
	derivedChecks []state.ValidationCheck,
) *state.ValidationCoverageArtifact {
	now := stateTime()
	coverage := &state.ValidationCoverageArtifact{
		ArtifactMeta: state.ArtifactMeta{
			SchemaVersion: artifactSchemaVersion,
			RunID:         runID,
			CreatedAt:     now,
			UpdatedAt:     now,
		},
		Scope:          state.ValidationScopeEpic,
		EpicID:         epicID,
		ChildTicketIDs: append([]string(nil), childIDs...),
	}
	for _, qc := range closureCmds {
		for _, raw := range qc.Run {
			cmd := strings.TrimSpace(raw)
			if cmd == "" {
				continue
			}
			coverage.DeclaredChecks = append(coverage.DeclaredChecks, state.ValidationCheck{
				ID:      declaredCheckID(epicID, cmd),
				Scope:   state.ValidationScopeEpic,
				Source:  state.ValidationCheckSourceQuality,
				Command: cmd,
				Reason:  "epic_closure_commands",
			})
		}
	}
	if len(derivedChecks) > 0 {
		coverage.DerivedChecks = append(coverage.DerivedChecks, derivedChecks...)
	}
	return coverage
}

// recordEpicCheckExecutions appends ValidationCheckExecution entries for the
// broad and derived check results just completed.
func recordEpicCheckExecutions(
	coverage *state.ValidationCoverageArtifact,
	epicID string,
	broadResults []verifycommand.CommandResult,
	derivedChecks []state.ValidationCheck,
	derivedResults map[string]verifycommand.CommandResult,
	cycle int,
) {
	if coverage == nil {
		return
	}
	for _, r := range broadResults {
		coverage.ExecutedChecks = append(coverage.ExecutedChecks, state.ValidationCheckExecution{
			CheckID:    declaredCheckID(epicID, r.Command),
			Result:     validationResultFromCommand(r),
			ExitCode:   r.ExitCode,
			DurationMS: r.DurationMS,
			StdoutPath: r.StdoutPath,
			StderrPath: r.StderrPath,
			Attempt:    cycle,
			StartedAt:  r.StartedAt,
			FinishedAt: r.FinishedAt,
		})
	}
	for _, c := range derivedChecks {
		r, ok := derivedResults[c.ID]
		if !ok {
			continue
		}
		coverage.ExecutedChecks = append(coverage.ExecutedChecks, state.ValidationCheckExecution{
			CheckID:    c.ID,
			Result:     validationResultFromCommand(r),
			ExitCode:   r.ExitCode,
			DurationMS: r.DurationMS,
			StdoutPath: r.StdoutPath,
			StderrPath: r.StderrPath,
			Attempt:    cycle,
			StartedAt:  r.StartedAt,
			FinishedAt: r.FinishedAt,
		})
	}
	coverage.UpdatedAt = stateTime()
}

// ---- Check execution --------------------------------------------------------

// executeEpicChecks runs the configured epic closure commands and/or the
// derived stale-wording checks. Either set may be empty.
func executeEpicChecks(
	ctx context.Context,
	repoRoot string,
	cfg policy.VerificationConfig,
	hasClosureCmds bool,
	derivedChecks []state.ValidationCheck,
) ([]verifycommand.CommandResult, map[string]verifycommand.CommandResult, error) {
	var broadResults []verifycommand.CommandResult
	if hasClosureCmds {
		results, err := verifycommand.RunQualityCommands(ctx, repoRoot, cfg.EpicClosureCommands, cfg)
		if err != nil {
			return nil, nil, fmt.Errorf("run epic closure commands: %w", err)
		}
		broadResults = results
	}

	derivedResults := make(map[string]verifycommand.CommandResult)
	if len(derivedChecks) > 0 {
		commands := make([]string, 0, len(derivedChecks))
		ids := make([]string, 0, len(derivedChecks))
		for _, c := range derivedChecks {
			cmd := strings.TrimSpace(c.Command)
			if cmd == "" {
				continue
			}
			commands = append(commands, cmd)
			ids = append(ids, c.ID)
		}
		if len(commands) > 0 {
			results, err := verifycommand.RunCommands(ctx, repoRoot, commands, cfg)
			if err != nil {
				return nil, nil, fmt.Errorf("run derived epic checks: %w", err)
			}
			for i, r := range results {
				if i < len(ids) {
					derivedResults[ids[i]] = r
				}
			}
		}
	}
	return broadResults, derivedResults, nil
}

// epicChecksAllPassed returns true when every broad and derived check passed.
func epicChecksAllPassed(
	broadResults []verifycommand.CommandResult,
	derivedChecks []state.ValidationCheck,
	derivedResults map[string]verifycommand.CommandResult,
) bool {
	for _, r := range broadResults {
		if r.TimedOut || r.ExitCode != 0 {
			return false
		}
	}
	for _, c := range derivedChecks {
		r, ok := derivedResults[c.ID]
		if !ok {
			continue
		}
		if r.TimedOut || r.ExitCode != 0 {
			return false
		}
	}
	return true
}

// ---- Derivation -------------------------------------------------------------

// deriveEpicScopedChecks derives stale-wording ValidationChecks for the epic
// scope based on configured EpicStaleWordingTerms and EpicClosureDocs.
// Returns nil when no stale wording terms are configured.
func deriveEpicScopedChecks(epicID string, cfg policy.VerificationConfig) []state.ValidationCheck {
	terms := normalizeStaleWordingTerms(cfg.EpicStaleWordingTerms)
	if len(terms) == 0 {
		return nil
	}
	docPaths := cfg.EpicClosureDocs
	if len(docPaths) == 0 {
		docPaths = defaultEpicClosureDocs
	}
	cmd := buildEpicStaleWordingCommand(terms, docPaths)
	return []state.ValidationCheck{{
		ID:      declaredCheckID(epicID, cmd),
		Scope:   state.ValidationScopeEpic,
		Source:  state.ValidationCheckSourceDerived,
		Command: cmd,
		Reason:  "epic-scoped stale wording sweep across documentation",
	}}
}

// buildEpicStaleWordingCommand builds a shell command that exits non-zero when
// stale wording is detected in the configured doc paths. The command uses
// `! grep -nrE` so that:
//   - when stale wording is found (grep exits 0), the overall command exits 1
//     (failure), causing epicChecksAllPassed to treat the check as failing and
//     routing a repair cycle with the matching lines shown as output;
//   - when docs are clean (grep exits 1 — no matches), the overall command
//     exits 0 (success) and the gate may proceed.
func buildEpicStaleWordingCommand(terms, docPaths []string) string {
	return buildStaleWordingGrepCommand(terms, docPaths, true)
}

// epicBroadCommandLines extracts the raw command strings from a QualityCommand
// list for provenance recording in the EpicClosureArtifact.
func epicBroadCommandLines(cmds []policy.QualityCommand) []string {
	var out []string
	for _, qc := range cmds {
		for _, cmd := range qc.Run {
			trimmed := strings.TrimSpace(cmd)
			if trimmed != "" {
				out = append(out, trimmed)
			}
		}
	}
	return out
}

// epicDerivedCommandLines extracts command strings from derived checks.
func epicDerivedCommandLines(checks []state.ValidationCheck) []string {
	var out []string
	for _, c := range checks {
		if cmd := strings.TrimSpace(c.Command); cmd != "" {
			out = append(out, cmd)
		}
	}
	return out
}

// epicDerivedHasCommand reports whether any derived check has an executable command.
func epicDerivedHasCommand(checks []state.ValidationCheck) bool {
	for _, c := range checks {
		if strings.TrimSpace(c.Command) != "" {
			return true
		}
	}
	return false
}

// sortedChildIDs extracts ticket IDs from the children slice in sorted order.
func sortedChildIDs(children []tkmd.Ticket) []string {
	ids := make([]string, 0, len(children))
	for _, c := range children {
		ids = append(ids, c.ID)
	}
	sort.Strings(ids)
	return ids
}

// ---- Reviewer ---------------------------------------------------------------

// runEpicReviewerAttempt invokes the epic reviewer with child context and
// evidence from the last check run.
func runEpicReviewerAttempt(
	ctx context.Context,
	req RunEpicRequest,
	cfg policy.Config,
	adapter runtime.Adapter,
	reviewProfile policy.RoleProfile,
	childIDs []string,
	childSnapshots map[string]TicketRunSnapshot,
	children []tkmd.Ticket,
	changedFiles []string,
	checksAllPassed bool,
	broadResults []verifycommand.CommandResult,
	derivedChecks []state.ValidationCheck,
	derivedResults map[string]verifycommand.CommandResult,
	attempt int,
) (runtime.ReviewResult, error) {
	leaseID := fmt.Sprintf("epic-review-%s-%d", req.RootTicketID, attempt-1)
	extra := buildEpicReviewExtra(
		childIDs, childSnapshots, children, changedFiles,
		checksAllPassed, broadResults, derivedChecks, derivedResults,
	)
	instructions := runtime.BuildEpicReviewInstructions(req.RootTicketID, childIDs, extra)

	return adapter.RunReviewer(ctx, runtime.ReviewRequest{
		RunID:                    req.RunID,
		TicketID:                 req.RootTicketID,
		LeaseID:                  leaseID,
		Attempt:                  attempt,
		Runtime:                  reviewProfile.Runtime,
		Model:                    reviewProfile.Model,
		Reasoning:                reviewProfile.Reasoning,
		Instructions:             instructions,
		EffectiveReviewThreshold: cfg.Policy.ReviewThreshold,
		ExecutionConfig:          executionConfigFromPolicy(cfg),
		OnProgress: func(detail string) {
			SendProgress(ctx, req.Progress, ProgressEvent{
				Type:   EventTicketDetail,
				Detail: fmt.Sprintf("epic reviewer (attempt %d): %s", attempt, detail),
			})
		},
	})
}

// buildEpicReviewExtra builds the extra context block appended to the epic
// reviewer's instructions. It includes:
//   - A compact table of child ticket phases and key artifact paths.
//   - A summary of check results (pass/fail per command).
//   - Truncated output from any failing checks.
//   - The set of files changed across the epic.
func buildEpicReviewExtra(
	childIDs []string,
	childSnapshots map[string]TicketRunSnapshot,
	children []tkmd.Ticket,
	changedFiles []string,
	checksAllPassed bool,
	broadResults []verifycommand.CommandResult,
	derivedChecks []state.ValidationCheck,
	derivedResults map[string]verifycommand.CommandResult,
) string {
	var b strings.Builder

	// Child ticket summary.
	if len(childIDs) > 0 {
		b.WriteString("### Child Ticket Summary\n\n")
		for _, id := range childIDs {
			snap, ok := childSnapshots[id]
			if !ok {
				fmt.Fprintf(&b, "- **%s**: snapshot unavailable\n", id)
				continue
			}
			phase := string(snap.CurrentPhase)
			if phase == "" {
				phase = "unknown"
			}
			fmt.Fprintf(&b, "- **%s** (%s)", id, phase)
			if snap.Review != nil && !snap.Review.Passed {
				fmt.Fprintf(&b, " — review did not pass")
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	// Check results summary.
	b.WriteString("### Broad Check Results\n\n")
	if len(broadResults) == 0 && len(derivedChecks) == 0 {
		b.WriteString("No broad or derived checks were configured.\n\n")
	} else {
		for _, r := range broadResults {
			status := "✓ passed"
			if r.TimedOut {
				status = "✗ timed out"
			} else if r.ExitCode != 0 {
				status = fmt.Sprintf("✗ exit %d", r.ExitCode)
			}
			fmt.Fprintf(&b, "- `%s`: %s\n", r.Command, status)
		}
		for _, c := range derivedChecks {
			r, ok := derivedResults[c.ID]
			if !ok {
				fmt.Fprintf(&b, "- `%s`: did not run\n", c.Command)
				continue
			}
			status := "✓ passed"
			if r.TimedOut {
				status = "✗ timed out"
			} else if r.ExitCode != 0 {
				status = fmt.Sprintf("✗ exit %d", r.ExitCode)
			}
			fmt.Fprintf(&b, "- `%s`: %s\n", c.Command, status)
		}
		b.WriteString("\n")
	}

	// Failing check output (truncated).
	failOutput := collectEpicVerificationOutput(broadResults, derivedChecks, derivedResults)
	if failOutput != "" {
		b.WriteString("### Failing Check Output\n\n")
		b.WriteString("```\n")
		b.WriteString(failOutput)
		if !strings.HasSuffix(failOutput, "\n") {
			b.WriteByte('\n')
		}
		b.WriteString("```\n\n")
	}

	// Changed files across the epic (compact).
	if len(changedFiles) > 0 {
		b.WriteString("### Files Changed in This Epic\n\n")
		const maxFiles = 30
		for i, f := range changedFiles {
			if i >= maxFiles {
				fmt.Fprintf(&b, "- … and %d more\n", len(changedFiles)-maxFiles)
				break
			}
			fmt.Fprintf(&b, "- `%s`\n", f)
		}
		b.WriteString("\n")
	}

	_ = children // available for future owned-path cross-referencing
	_ = checksAllPassed

	return b.String()
}

// collectEpicVerificationOutput reads stdout/stderr from failing check results
// and returns a combined string capped at epicGateOutputLimit.
func collectEpicVerificationOutput(
	broadResults []verifycommand.CommandResult,
	derivedChecks []state.ValidationCheck,
	derivedResults map[string]verifycommand.CommandResult,
) string {
	all := make([]verifycommand.CommandResult, 0, len(broadResults)+len(derivedChecks))
	all = append(all, broadResults...)
	for _, c := range derivedChecks {
		if r, ok := derivedResults[c.ID]; ok {
			all = append(all, r)
		}
	}
	var b strings.Builder
	for _, r := range all {
		if r.ExitCode == 0 && !r.TimedOut {
			continue
		}
		fmt.Fprintf(&b, "$ %s (exit %d)\n", r.Command, r.ExitCode)
		if r.TimedOut {
			b.WriteString("(timed out)\n")
		}
		for _, path := range []string{r.StderrPath, r.StdoutPath} {
			if path == "" {
				continue
			}
			file, err := os.Open(path)
			if err != nil {
				continue
			}
			remaining := epicGateOutputLimit - b.Len()
			if remaining <= 0 {
				_ = file.Close()
				return b.String()
			}
			data, readErr := io.ReadAll(io.LimitReader(file, int64(remaining)))
			closeErr := file.Close()
			if readErr != nil || closeErr != nil || len(data) == 0 {
				continue
			}
			b.Write(data)
			if data[len(data)-1] != '\n' && b.Len() < epicGateOutputLimit {
				b.WriteByte('\n')
			}
		}
		if b.Len() >= epicGateOutputLimit {
			return b.String()
		}
	}
	return b.String()
}

// ---- Finding helpers --------------------------------------------------------

// mapReviewToEpicFindings converts blocking reviewer findings to
// EpicClosureFinding, mapping each to a child ticket when its file path
// falls within that ticket's owned paths.
func mapReviewToEpicFindings(
	result runtime.ReviewResult,
	threshold state.Severity,
	children []tkmd.Ticket,
) []state.EpicClosureFinding {
	var out []state.EpicClosureFinding
	for _, f := range result.Findings {
		if !ReviewFindingBlocks(f, threshold) {
			continue
		}
		owningTicket := findOwningTicketByFile(f.File, children)
		// Classify auto-repairability: operator-level keywords in the body
		// signal that human input is needed rather than a code fix.
		body := strings.ToLower(f.Body)
		requiresOperator := strings.Contains(body, "requires operator") ||
			strings.Contains(body, "needs_context") ||
			strings.Contains(body, "needs context")
		autoRepair := f.Disposition == runtime.ReviewDispositionOpen && !requiresOperator

		id := epicReviewFindingID(f)
		out = append(out, state.EpicClosureFinding{
			ID:                 id,
			Source:             "epic_reviewer",
			Severity:           f.Severity,
			Title:              f.Title,
			Body:               f.Body,
			File:               f.File,
			Line:               f.Line,
			OwningTicketID:     owningTicket,
			RequiresOperator:   requiresOperator,
			AutoRepairPossible: autoRepair,
			NextAction:         epicFindingNextAction(autoRepair, owningTicket),
		})
	}
	return out
}

// epicFindingNextAction returns a short next-action hint for a finding.
func epicFindingNextAction(autoRepair bool, owningTicket string) string {
	switch {
	case autoRepair && owningTicket != "":
		return fmt.Sprintf("route repair to child ticket %s", owningTicket)
	case autoRepair:
		return "route to epic repair worker"
	default:
		return "requires operator input"
	}
}

// collectEpicCheckFailureFindings creates EpicClosureFinding entries for
// commands that exited non-zero so the reviewer output and repair instructions
// refer to them by stable ID.
func collectEpicCheckFailureFindings(
	broadResults []verifycommand.CommandResult,
	derivedChecks []state.ValidationCheck,
	derivedResults map[string]verifycommand.CommandResult,
	cycle int,
) []state.EpicClosureFinding {
	var out []state.EpicClosureFinding
	for _, r := range broadResults {
		if r.ExitCode == 0 && !r.TimedOut {
			continue
		}
		id := epicCheckFindingID("broad_check", r.Command, cycle)
		body := fmt.Sprintf("Command `%s` exited with code %d", r.Command, r.ExitCode)
		if r.TimedOut {
			body = fmt.Sprintf("Command `%s` timed out", r.Command)
		}
		out = append(out, state.EpicClosureFinding{
			ID:                 id,
			Source:             "broad_check",
			Title:              fmt.Sprintf("broad check failed: %s", r.Command),
			Body:               body,
			AutoRepairPossible: true,
			NextAction:         "route to epic repair worker",
		})
	}
	for _, c := range derivedChecks {
		r, ok := derivedResults[c.ID]
		if !ok || (r.ExitCode == 0 && !r.TimedOut) {
			continue
		}
		id := epicCheckFindingID("derived_check", c.ID, cycle)
		body := fmt.Sprintf("Derived check `%s` exited with code %d: %s", c.Command, r.ExitCode, c.Reason)
		if r.TimedOut {
			body = fmt.Sprintf("Derived check `%s` timed out: %s", c.Command, c.Reason)
		}
		out = append(out, state.EpicClosureFinding{
			ID:                 id,
			Source:             "derived_check",
			Title:              fmt.Sprintf("derived check failed: %s", c.Reason),
			Body:               body,
			AutoRepairPossible: true,
			NextAction:         "route to epic repair worker",
		})
	}
	return out
}

// mergeEpicFindings merges fresh findings into existing, deduplicating by ID.
func mergeEpicFindings(existing, fresh []state.EpicClosureFinding) []state.EpicClosureFinding {
	if len(fresh) == 0 {
		return existing
	}
	seen := make(map[string]struct{}, len(existing))
	for _, f := range existing {
		if f.ID != "" {
			seen[f.ID] = struct{}{}
		}
	}
	out := append([]state.EpicClosureFinding(nil), existing...)
	for _, f := range fresh {
		if f.ID == "" {
			out = append(out, f)
			continue
		}
		if _, ok := seen[f.ID]; ok {
			continue
		}
		seen[f.ID] = struct{}{}
		out = append(out, f)
	}
	return out
}

// resolveRepairedFindings marks existing findings as resolved when they no
// longer appear in the fresh set (evidence that the repair fixed them).
func resolveRepairedFindings(existing, fresh []state.EpicClosureFinding) {
	freshIDs := make(map[string]struct{}, len(fresh))
	for _, f := range fresh {
		if f.ID != "" {
			freshIDs[f.ID] = struct{}{}
		}
	}
	for i := range existing {
		if existing[i].Resolved || existing[i].ID == "" {
			continue
		}
		if _, stillPresent := freshIDs[existing[i].ID]; !stillPresent {
			existing[i].Resolved = true
		}
	}
}

// hasUnresolvedFindings reports whether any finding is still open.
func hasUnresolvedFindings(findings []state.EpicClosureFinding) bool {
	for _, f := range findings {
		if !f.Resolved {
			return true
		}
	}
	return false
}

// collectUnresolvedFindingIDs returns the IDs of unresolved findings.
func collectUnresolvedFindingIDs(findings []state.EpicClosureFinding) []string {
	var ids []string
	for _, f := range findings {
		if !f.Resolved && f.ID != "" {
			ids = append(ids, f.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

// buildEpicBlockReason builds a human-readable block reason from the
// available evidence: check failures, reviewer error, or unresolved findings.
func buildEpicBlockReason(checksAllPassed bool, findings []state.EpicClosureFinding, reviewErr error) string {
	var parts []string
	if !checksAllPassed {
		parts = append(parts, "broad checks failed")
	}
	if reviewErr != nil {
		parts = append(parts, fmt.Sprintf("reviewer error: %v", reviewErr))
	}
	unresolved := collectUnresolvedFindingIDs(findings)
	if len(unresolved) > 0 {
		parts = append(parts, fmt.Sprintf("unresolved findings: %s", strings.Join(unresolved, ", ")))
	}
	if len(parts) == 0 {
		return "epic closure gate could not confirm closure"
	}
	return strings.Join(parts, "; ")
}

// ---- Repair instructions ----------------------------------------------------

// buildEpicRepairInstructions builds the instructions passed to the epic
// repair worker. The worker is told which checks failed and which reviewer
// findings are unresolved so it can focus its changes.
func buildEpicRepairInstructions(
	epicID string,
	findings []state.EpicClosureFinding,
	broadResults []verifycommand.CommandResult,
	derivedChecks []state.ValidationCheck,
	derivedResults map[string]verifycommand.CommandResult,
	cycle int,
) string {
	var b strings.Builder

	fmt.Fprintf(&b, "**Phase:** epic-repair\n")
	fmt.Fprintf(&b, "**Epic:** %s, repair cycle %d\n\n", epicID, cycle)

	b.WriteString("The epic closure gate found blocking issues after all child waves completed.\n")
	b.WriteString("Fix ONLY these issues. Do not re-implement tickets or change unrelated code.\n")
	b.WriteString("Commit your changes when done.\n\n")

	// Failing checks.
	failOutput := collectEpicVerificationOutput(broadResults, derivedChecks, derivedResults)
	if failOutput != "" {
		b.WriteString("### Failing Check Output\n\n")
		b.WriteString("```\n")
		b.WriteString(failOutput)
		if !strings.HasSuffix(failOutput, "\n") {
			b.WriteByte('\n')
		}
		b.WriteString("```\n\n")
	}

	// Unresolved reviewer findings.
	var unresolved []state.EpicClosureFinding
	for _, f := range findings {
		if !f.Resolved {
			unresolved = append(unresolved, f)
		}
	}
	if len(unresolved) > 0 {
		b.WriteString("### Unresolved Findings\n\n")
		for _, f := range unresolved {
			severity := string(f.Severity)
			if severity == "" {
				severity = "unspecified"
			}
			fmt.Fprintf(&b, "**[%s] %s** (`%s`)\n", severity, f.Title, f.Source)
			if f.File != "" {
				fmt.Fprintf(&b, "File: `%s`", f.File)
				if f.Line > 0 {
					fmt.Fprintf(&b, " line %d", f.Line)
				}
				b.WriteString("\n")
			}
			if f.OwningTicketID != "" {
				fmt.Fprintf(&b, "Owning ticket: %s\n", f.OwningTicketID)
			}
			if f.Body != "" {
				fmt.Fprintf(&b, "\n%s\n", f.Body)
			}
			b.WriteString("\n")
		}
	}

	return b.String()
}

// ---- Child snapshot loading -------------------------------------------------

// loadChildSnapshotsForEpicGate loads TicketRunSnapshot values for each child
// ticket ID. Missing or unreadable snapshots are silently skipped — the
// reviewer still runs with whatever context is available.
func loadChildSnapshotsForEpicGate(repoRoot, runID string, childIDs []string) map[string]TicketRunSnapshot {
	snapshots := make(map[string]TicketRunSnapshot, len(childIDs))
	for _, id := range childIDs {
		var snap TicketRunSnapshot
		if err := loadTicketSnapshot(repoRoot, runID, id, &snap); err == nil {
			snapshots[id] = snap
		}
	}
	return snapshots
}

// ---- ID generation ----------------------------------------------------------

// epicReviewFindingID returns a stable finding ID from a review finding.
// Uses the reviewer-assigned ID when present; otherwise derives one from
// the file, line, and title so the same finding is deduplicated across runs.
func epicReviewFindingID(f runtime.ReviewFinding) string {
	if f.ID != "" {
		return "review-" + f.ID
	}
	h := sha256.Sum256([]byte(strings.Join([]string{f.File, fmt.Sprintf("%d", f.Line), f.Title}, ":")))
	return fmt.Sprintf("review-%x", h[:4])
}

// epicCheckFindingID returns a stable finding ID for a failed check command.
func epicCheckFindingID(source, key string, cycle int) string {
	h := sha256.Sum256([]byte(strings.Join([]string{source, key, fmt.Sprintf("%d", cycle)}, ":")))
	return fmt.Sprintf("%s-%x", source, h[:4])
}

// ---- Ticket ownership mapping -----------------------------------------------

// findOwningTicketByFile returns the ID of the child ticket whose owned paths
// contain the given file. Returns "" when no match is found.
func findOwningTicketByFile(file string, children []tkmd.Ticket) string {
	if file == "" {
		return ""
	}
	cleanFile := filepath.ToSlash(filepath.Clean(file))
	for _, child := range children {
		for _, owned := range child.OwnedPaths {
			cleanOwned := strings.TrimRight(filepath.ToSlash(filepath.Clean(owned)), "/")
			if cleanFile == cleanOwned || strings.HasPrefix(cleanFile, cleanOwned+"/") {
				return child.ID
			}
		}
	}
	return ""
}

// ---- Path helpers -----------------------------------------------------------

// epicClosureArtifactPath returns the file path for the epic closure artifact.
func epicClosureArtifactPath(repoRoot, runID string) string {
	return filepath.Join(runDir(repoRoot, runID), "epic-closure.json")
}

// ---- Resume cursor helpers --------------------------------------------------

// setPendingEpicGate marks the run cursor to indicate that the epic closure
// gate is in progress. The marker lets resume detect a crash mid-gate and
// re-enter the gate on the next run.
func setPendingEpicGate(cursor map[string]any) {
	if cursor != nil {
		cursor["pending_epic_gate"] = true
	}
}

// clearPendingEpicGate removes the pending-epic-gate marker from the run
// cursor after the gate has completed successfully.
func clearPendingEpicGate(cursor map[string]any) {
	if cursor != nil {
		delete(cursor, "pending_epic_gate")
	}
}

// ---- Unused import guard: ensure errors is imported via errors.Is usage -----
var _ = errors.Is
