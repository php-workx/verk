// Package engine — validation coverage helpers.
//
// This file owns the engine-side glue that builds and projects durable
// validation coverage artifacts (state.ValidationCoverageArtifact) for
// ticket, wave, and epic scopes. It is intentionally small: the data
// model lives in internal/state so it can be reused across scopes
// without pulling engine imports.
//
// The helpers here are pure projections. They read existing plan /
// verification / review / repair-cycle artifacts and synthesize a
// validation coverage view. They never mutate their inputs.
package engine

import (
	"crypto/sha1"
	"fmt"
	"strings"
	"verk/internal/state"
)

// BuildTicketValidationCoverage projects validation coverage for a single
// ticket from its plan, verification, review, and repair cycle artifacts.
//
// Any input may be nil — a zero value is treated as "no coverage yet" so
// callers can invoke this on partially-populated state (e.g. after
// verification has run but before review).
//
// The resulting artifact is scoped to ValidationScopeTicket and uses the
// plan's RunID / TicketID as identity.
func BuildTicketValidationCoverage(
	plan state.PlanArtifact,
	verification *state.VerificationArtifact,
	review *state.ReviewFindingsArtifact,
	repairCycles []state.RepairCycleArtifact,
) state.ValidationCoverageArtifact {
	now := stateTime()
	coverage := state.ValidationCoverageArtifact{
		ArtifactMeta: state.ArtifactMeta{
			SchemaVersion: artifactSchemaVersion,
			RunID:         plan.RunID,
			CreatedAt:     now,
			UpdatedAt:     now,
		},
		Scope:    state.ValidationScopeTicket,
		TicketID: plan.TicketID,
	}

	coverage.DeclaredChecks = buildDeclaredChecks(plan)

	if verification != nil {
		addVerificationExecutions(&coverage, verification)
	}

	if review != nil {
		addReviewBlockers(&coverage, review, plan.EffectiveReviewThreshold)
	}

	addRepairRefs(&coverage, repairCycles)

	finalizeCoverageClosure(&coverage, verification, review, plan.EffectiveReviewThreshold)
	return coverage
}

// buildDeclaredChecks materializes ValidationCheck entries from the ticket
// plan. Each declared validation command becomes a distinct check with a
// stable id derived from the command string.
func buildDeclaredChecks(plan state.PlanArtifact) []state.ValidationCheck {
	commands := plan.ValidationCommands
	if len(commands) == 0 {
		commands = plan.DeclaredChecks
	}
	if len(commands) == 0 {
		return nil
	}
	out := make([]state.ValidationCheck, 0, len(commands))
	for _, cmd := range commands {
		trimmed := strings.TrimSpace(cmd)
		if trimmed == "" {
			continue
		}
		out = append(out, state.ValidationCheck{
			ID:       declaredCheckID(plan.TicketID, trimmed),
			Scope:    state.ValidationScopeTicket,
			Source:   state.ValidationCheckSourceDeclared,
			Command:  trimmed,
			Reason:   "ticket validation_commands",
			TicketID: plan.TicketID,
		})
	}
	return out
}

// addVerificationExecutions records one ValidationCheckExecution per
// verification result. Results whose commands don't match a declared check
// are attributed to a synthetic quality check so quality_commands output
// is still captured.
func addVerificationExecutions(coverage *state.ValidationCoverageArtifact, verification *state.VerificationArtifact) {
	for _, result := range verification.Results {
		checkID := declaredCheckID(coverage.TicketID, result.Command)
		if _, ok := coverage.CheckByID(checkID); !ok {
			coverage.DerivedChecks = append(coverage.DerivedChecks, state.ValidationCheck{
				ID:       checkID,
				Scope:    state.ValidationScopeTicket,
				Source:   state.ValidationCheckSourceQuality,
				Command:  result.Command,
				Reason:   "verification command (quality_commands or equivalent)",
				TicketID: coverage.TicketID,
			})
		}
		coverage.ExecutedChecks = append(coverage.ExecutedChecks, state.ValidationCheckExecution{
			CheckID:    checkID,
			Result:     state.ValidationCheckResultFromBool(result.Passed),
			ExitCode:   result.ExitCode,
			DurationMS: result.DurationMS,
			StdoutPath: result.StdoutPath,
			StderrPath: result.StderrPath,
			Attempt:    verification.Attempt,
			StartedAt:  result.StartedAt,
			FinishedAt: result.FinishedAt,
		})
	}
}

// addReviewBlockers copies blocking review findings into the coverage
// artifact as UnresolvedBlockers. Resolved or below-threshold findings are
// ignored — they don't block closure.
func addReviewBlockers(coverage *state.ValidationCoverageArtifact, review *state.ReviewFindingsArtifact, threshold state.Severity) {
	for _, finding := range review.Findings {
		if !ReviewFindingBlocks(finding, threshold) {
			continue
		}
		coverage.UnresolvedBlockers = append(coverage.UnresolvedBlockers, state.ValidationBlocker{
			FindingID: finding.ID,
			Reason:    fmt.Sprintf("review finding %s: %s", finding.Severity, finding.Title),
			Scope:     state.ValidationScopeTicket,
		})
	}
}

// addRepairRefs records repair cycle references and marks previously failed
// check executions as repaired when a later cycle completed successfully.
func addRepairRefs(coverage *state.ValidationCoverageArtifact, cycles []state.RepairCycleArtifact) {
	for _, cycle := range cycles {
		ref := state.ValidationRepairRef{
			CycleID:      cycleID(cycle),
			ArtifactPath: cycle.ReviewArtifact,
			Result:       repairResultFor(cycle),
			Scope:        cycle.Scope,
		}
		if ref.Scope == "" {
			ref.Scope = state.ValidationScopeTicket
		}
		if len(cycle.TriggerCheckIDs) == 0 && len(cycle.TriggerFindingIDs) == 0 {
			coverage.RepairRefs = append(coverage.RepairRefs, ref)
			continue
		}
		for _, checkID := range cycle.TriggerCheckIDs {
			r := ref
			r.CheckID = checkID
			coverage.RepairRefs = append(coverage.RepairRefs, r)
		}
		for _, findingID := range cycle.TriggerFindingIDs {
			r := ref
			r.FindingID = findingID
			coverage.RepairRefs = append(coverage.RepairRefs, r)
		}
	}
}

// finalizeCoverageClosure decides whether the coverage artifact is closable
// based on executed checks and unresolved review blockers. The rule mirrors
// the existing closeout gates: any failed execution or blocking review
// finding blocks closure and sets BlockReason accordingly.
func finalizeCoverageClosure(
	coverage *state.ValidationCoverageArtifact,
	verification *state.VerificationArtifact,
	review *state.ReviewFindingsArtifact,
	threshold state.Severity,
) {
	if len(coverage.UnresolvedBlockers) > 0 {
		coverage.Closable = false
		first := coverage.UnresolvedBlockers[0]
		coverage.BlockReason = first.Reason
		return
	}
	if verification != nil && !verification.Passed {
		coverage.Closable = false
		coverage.BlockReason = "verification did not pass"
		return
	}
	if review != nil && !review.Passed {
		coverage.Closable = false
		coverage.BlockReason = fmt.Sprintf("review blocking at threshold %s", threshold)
		return
	}
	coverage.Closable = true
	coverage.ClosureReason = "all declared and derived checks passed"
}

// declaredCheckID returns a stable id for a declared/derived check based on
// the owning ticket id and command string. The id is deterministic so
// re-running the projection produces the same value for the same inputs.
func declaredCheckID(ticketID, command string) string {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return fmt.Sprintf("check-%s-empty", ticketID)
	}
	sum := sha1.Sum([]byte(ticketID + "\x00" + trimmed))
	return fmt.Sprintf("check-%s-%x", ticketID, sum[:4])
}

// cycleID derives a stable cycle id from a RepairCycleArtifact. If the
// artifact already carries an explicit id it is preferred; otherwise the
// cycle number is used.
func cycleID(cycle state.RepairCycleArtifact) string {
	if cycle.TicketID != "" && cycle.Cycle > 0 {
		return fmt.Sprintf("%s-cycle-%d", cycle.TicketID, cycle.Cycle)
	}
	if cycle.WaveID != "" && cycle.Cycle > 0 {
		return fmt.Sprintf("%s-cycle-%d", cycle.WaveID, cycle.Cycle)
	}
	return fmt.Sprintf("cycle-%d", cycle.Cycle)
}

// repairResultFor maps a repair cycle's status string to a
// ValidationCheckResult so downstream consumers can interpret it without
// special-casing the legacy status strings.
func repairResultFor(cycle state.RepairCycleArtifact) state.ValidationCheckResult {
	switch strings.ToLower(strings.TrimSpace(cycle.Status)) {
	case "completed", "passed", "repaired":
		return state.ValidationCheckResultRepaired
	case "blocked":
		return state.ValidationCheckResultFailed
	case "repair_pending", "pending":
		return state.ValidationCheckResultPending
	default:
		return state.ValidationCheckResultPending
	}
}
