// Package engine — ticket-scoped validation orchestration.
//
// This file wires the derived-check layer (derived_checks.go) and the
// validation coverage schema (validation_coverage.go) into the ticket
// lifecycle. It is the glue layer that:
//
//   - derives focused checks from the implementation's changed files
//   - runs declared, quality, and derived check commands
//   - assembles a ValidationCoverageArtifact that records what ran,
//     what passed, what failed, what was skipped, and (when repair
//     budget is exhausted) which policy limit stopped further work
//
// The helper functions below are intentionally pure projections over
// existing artifacts. They never mutate their inputs so callers can
// continue to use the VerificationArtifact / PlanArtifact / repair
// cycle values after projection.
package engine

import (
	"context"
	"fmt"
	"sort"
	"strings"
	verifycommand "verk/internal/adapters/verify/command"
	"verk/internal/policy"
	"verk/internal/state"
)

// derivedCheckLookup is the tool lookup used when deriving checks for a
// real ticket run. Tests that want deterministic derivation can override
// this by constructing their own ToolSignals; the engine itself always
// uses the real PATH-backed lookup.
var derivedCheckLookup = DefaultToolLookup

// toolSignalsProvider resolves tool signals for a given repo root.
// Declared as a package variable so tests exercising the ticket run can
// inject a deterministic signal set without touching the host PATH. The
// production value calls DetectToolSignals with derivedCheckLookup.
var toolSignalsProvider = func(repoRoot string) ToolSignals {
	return DetectToolSignals(repoRoot, derivedCheckLookup())
}

// staleWordingTermsProvider supplies the optional stale-wording terms
// used by the markdown derivation layer. The engine does not invent
// terms; they must be configured explicitly. The provider is a variable
// so future configuration sources (e.g. policy config) can inject values
// without changing the derivation entry point.
var staleWordingTermsProvider = func() []string { return nil }

// deriveTicketChecks projects derived checks for a ticket based on the
// files touched by its implementation. Returns a zero value when the
// implementation artifact is missing — the caller falls back to running
// only declared and quality commands.
func deriveTicketChecks(st *ticketRunState) DeriveChecksResult {
	var changed []string
	if st.implementation != nil {
		changed = append(changed, st.implementation.ChangedFiles...)
	}
	return DeriveChecks(DeriveChecksInput{
		Plan:              st.req.Plan,
		ChangedFiles:      changed,
		Tools:             toolSignalsProvider(st.repoRoot),
		StaleWordingTerms: staleWordingTermsProvider(),
	})
}

// runDerivedChecks executes the command lines for each derived check and
// returns a map keyed by check id so the caller can attribute results
// back to their check. Checks with empty commands are skipped silently
// (callers should inspect DeriveChecksResult.Skipped for tooling gaps).
func runDerivedChecks(
	ctx context.Context,
	repoRoot string,
	checks []state.ValidationCheck,
	cfg policy.VerificationConfig,
) (map[string]verifycommand.CommandResult, []verifycommand.CommandResult, error) {
	out := make(map[string]verifycommand.CommandResult, len(checks))
	ordered := make([]verifycommand.CommandResult, 0, len(checks))
	if len(checks) == 0 {
		return out, ordered, nil
	}
	commands := make([]string, 0, len(checks))
	mapping := make([]string, 0, len(checks))
	for _, c := range checks {
		cmd := strings.TrimSpace(c.Command)
		if cmd == "" {
			continue
		}
		commands = append(commands, cmd)
		mapping = append(mapping, c.ID)
	}
	if len(commands) == 0 {
		return out, ordered, nil
	}
	results, err := verifycommand.RunCommands(ctx, repoRoot, commands, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("run derived checks: %w", err)
	}
	for i, r := range results {
		if i >= len(mapping) {
			break
		}
		out[mapping[i]] = r
	}
	ordered = append(ordered, results...)
	return out, ordered, nil
}

// assembleTicketValidationCoverage builds the ticket-scope coverage
// artifact from the plan, the declared/quality results, the derived
// checks and their execution results, skipped checks, the review, and
// any repair cycles recorded so far.
//
// The resulting artifact is self-contained: downstream code should not
// need to cross-reference the verification artifact to know what ran.
func assembleTicketValidationCoverage(
	plan state.PlanArtifact,
	runID string,
	attempt int,
	declaredResults []verifycommand.CommandResult,
	derived DeriveChecksResult,
	derivedResults map[string]verifycommand.CommandResult,
	review *state.ReviewFindingsArtifact,
	cycles []state.RepairCycleArtifact,
	limit *state.ValidationRepairLimit,
) state.ValidationCoverageArtifact {
	now := stateTime()
	coverage := state.ValidationCoverageArtifact{
		ArtifactMeta: state.ArtifactMeta{
			SchemaVersion: artifactSchemaVersion,
			RunID:         runID,
			CreatedAt:     now,
			UpdatedAt:     now,
		},
		Scope:    state.ValidationScopeTicket,
		TicketID: plan.TicketID,
	}

	// Declared checks: materialize from plan so execution results can be
	// attributed back to them by command-string match.
	coverage.DeclaredChecks = buildDeclaredChecks(plan)

	// Derived + skipped checks come straight from the derivation layer.
	coverage.DerivedChecks = append(coverage.DerivedChecks, derived.Checks...)
	coverage.SkippedChecks = append(coverage.SkippedChecks, derived.Skipped...)

	// Record executions for declared/quality results.
	for _, r := range declaredResults {
		id, source := matchDeclaredCheckID(coverage, plan.TicketID, r.Command)
		if id == "" {
			id = declaredCheckID(plan.TicketID, r.Command)
			coverage.DerivedChecks = append(coverage.DerivedChecks, state.ValidationCheck{
				ID:       id,
				Scope:    state.ValidationScopeTicket,
				Source:   state.ValidationCheckSourceQuality,
				Command:  r.Command,
				Reason:   "verification command (quality_commands or equivalent)",
				TicketID: plan.TicketID,
			})
			source = state.ValidationCheckSourceQuality
		}
		_ = source // retained for future severity routing
		coverage.ExecutedChecks = append(coverage.ExecutedChecks, state.ValidationCheckExecution{
			CheckID:    id,
			Result:     validationResultFromCommand(r),
			ExitCode:   r.ExitCode,
			DurationMS: r.DurationMS,
			StdoutPath: r.StdoutPath,
			StderrPath: r.StderrPath,
			Attempt:    attempt,
			StartedAt:  r.StartedAt,
			FinishedAt: r.FinishedAt,
		})
	}

	// Record executions for derived checks.
	for _, c := range derived.Checks {
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
			Attempt:    attempt,
			StartedAt:  r.StartedAt,
			FinishedAt: r.FinishedAt,
		})
	}

	// Review blockers (if any) surface as unresolved blockers so
	// downstream closure logic can treat them uniformly with
	// verification-side failures.
	if review != nil {
		addReviewBlockers(&coverage, review, plan.EffectiveReviewThreshold)
	}

	// Failing executions that remain unrepaired become blockers. A
	// failing derived-check execution with no later repaired-result
	// override is considered unresolved.
	appendUnresolvedCheckBlockers(&coverage)

	// Repair cycle references.
	addRepairRefs(&coverage, cycles)

	if limit != nil {
		copy := *limit
		coverage.RepairLimit = &copy
	}

	finalizeTicketCoverageClosure(&coverage, plan.EffectiveReviewThreshold)
	return coverage
}

// matchDeclaredCheckID attempts to match an executed command to a
// declared check already present in the coverage artifact. Returns the
// matched check id and its source when a match exists; otherwise empty
// strings so the caller can record a synthetic quality check.
func matchDeclaredCheckID(cov state.ValidationCoverageArtifact, ticketID, command string) (string, state.ValidationCheckSource) {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return "", ""
	}
	candidate := declaredCheckID(ticketID, trimmed)
	for _, c := range cov.DeclaredChecks {
		if c.ID == candidate || c.Command == trimmed {
			return c.ID, c.Source
		}
	}
	for _, c := range cov.DerivedChecks {
		if c.ID == candidate || c.Command == trimmed {
			return c.ID, c.Source
		}
	}
	return "", ""
}

// validationResultFromCommand maps a CommandResult's pass/fail/timeout
// state to the canonical ValidationCheckResult enum.
func validationResultFromCommand(r verifycommand.CommandResult) state.ValidationCheckResult {
	if r.TimedOut {
		return state.ValidationCheckResultFailed
	}
	if r.ExitCode == 0 {
		return state.ValidationCheckResultPassed
	}
	return state.ValidationCheckResultFailed
}

// appendUnresolvedCheckBlockers walks the executed checks and records a
// ValidationBlocker for each failing execution whose check is not marked
// advisory. Advisory checks contribute to coverage but never block on
// their own — only after policy promotes them to required.
func appendUnresolvedCheckBlockers(coverage *state.ValidationCoverageArtifact) {
	seen := make(map[string]bool)
	for _, exec := range coverage.ExecutedChecks {
		if exec.Result != state.ValidationCheckResultFailed {
			continue
		}
		if seen[exec.CheckID] {
			continue
		}
		check, ok := coverage.CheckByID(exec.CheckID)
		if !ok {
			continue
		}
		if check.Advisory {
			continue
		}
		seen[exec.CheckID] = true
		coverage.UnresolvedBlockers = append(coverage.UnresolvedBlockers, state.ValidationBlocker{
			CheckID: exec.CheckID,
			Reason:  fmt.Sprintf("check %s failed with exit code %d", check.Command, exec.ExitCode),
			Scope:   state.ValidationScopeTicket,
		})
	}
}

// finalizeTicketCoverageClosure sets Closable / ClosureReason /
// BlockReason on the coverage artifact based on unresolved blockers and
// repair-limit state. Called after blockers and repair refs are in.
func finalizeTicketCoverageClosure(coverage *state.ValidationCoverageArtifact, threshold state.Severity) {
	if len(coverage.UnresolvedBlockers) > 0 {
		coverage.Closable = false
		coverage.BlockReason = coverage.UnresolvedBlockers[0].Reason
		return
	}
	if coverage.RepairLimit != nil {
		coverage.Closable = false
		if coverage.BlockReason == "" {
			coverage.BlockReason = coverage.RepairLimit.Reason
		}
		return
	}
	coverage.Closable = true
	coverage.ClosureReason = fmt.Sprintf("all declared, derived, and quality checks passed at threshold %s", threshold)
}

// requiredDerivedChecksPassed reports whether every non-advisory derived
// check either passed or was skipped (no result recorded). Advisory
// checks are excluded so their failures alone do not flip the verify
// loop's Passed flag — they surface in ValidationCoverage instead.
func requiredDerivedChecksPassed(checks []state.ValidationCheck, results map[string]verifycommand.CommandResult) bool {
	for _, c := range checks {
		if c.Advisory {
			continue
		}
		r, ok := results[c.ID]
		if !ok {
			continue
		}
		if r.TimedOut || r.ExitCode != 0 {
			return false
		}
	}
	return true
}

// failingCheckIDs returns the ids of checks whose most recent executed
// result is failed. Used to attribute repair cycles to the specific
// checks that triggered them.
func failingCheckIDs(coverage state.ValidationCoverageArtifact) []string {
	latest := make(map[string]state.ValidationCheckResult, len(coverage.ExecutedChecks))
	for _, e := range coverage.ExecutedChecks {
		latest[e.CheckID] = e.Result
	}
	out := make([]string, 0, len(latest))
	for id, result := range latest {
		if result == state.ValidationCheckResultFailed {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}
