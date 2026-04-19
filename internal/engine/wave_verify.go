package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"verk/internal/adapters/runtime"
	"verk/internal/policy"
	"verk/internal/state"

	verifycommand "verk/internal/adapters/verify/command"
)

const waveVerificationOutputLimit = 4 * 1024 // 4 KB

// runWaveVerificationLoop runs the configured quality commands and any
// engine-derived wave-scoped checks against the merged wave baseline. If any
// check fails it routes the failure into a wave repair worker, re-running
// only the relevant failed checks plus the configured quality gate. The wave
// artifact is updated in place: ValidationCoverage records the declared,
// derived, and executed checks, plus the repair cycles that were spawned.
//
// Returns nil if verification passes (possibly after repairs), or an error if
// quality / derived checks still fail after exhausting all repair cycles or
// when the policy disables wave repair entirely.
func runWaveVerificationLoop(
	ctx context.Context,
	req RunEpicRequest,
	cfg policy.Config,
	wave *state.WaveArtifact,
	wavePath string,
	changedFiles []string,
) error {
	plan := waveDerivationPlan(*wave, changedFiles)
	derivation := DeriveChecks(DeriveChecksInput{
		Plan:              plan,
		ChangedFiles:      changedFiles,
		Tools:             toolSignalsProvider(req.RepoRoot),
		StaleWordingTerms: staleWordingTermsProvider(),
	})
	derivedChecks := promoteDerivedToWave(derivation.Checks, wave.WaveID)

	hasQualityCmds := len(cfg.Verification.QualityCommands) > 0
	hasDerivedCmds := derivedHasExecutableCommand(derivedChecks)

	// No quality and no derived checks → nothing to verify.
	if !hasQualityCmds && !hasDerivedCmds {
		return nil
	}

	coverage := newWaveValidationCoverage(*wave, plan, cfg.Verification.QualityCommands, derivedChecks, derivation.Skipped)
	wave.ValidationCoverage = coverage
	syncWaveAcceptanceFromCoverage(wave, coverage)

	qualityResults, derivedResults, err := executeWaveChecks(ctx, req.RepoRoot, cfg.Verification, hasQualityCmds, derivedChecks)
	if err != nil {
		return fmt.Errorf("wave verification: run checks: %w", err)
	}
	recordWaveExecutions(coverage, qualityResults, derivedChecks, derivedResults, 0)

	if waveExecutionsAllPassed(qualityResults, derivedChecks, derivedResults) {
		markWaveCoveragePassed(coverage, 0)
		syncWaveAcceptanceFromCoverage(wave, coverage)
		return state.SaveJSONAtomic(wavePath, wave)
	}

	if cfg.Policy.MaxWaveRepairCycles == 0 {
		failingIDs := failingExecutionIDs(coverage)
		appendWaveCheckBlockers(coverage, failingIDs, "wave repair disabled")
		recordWaveRepairLimit(coverage, failingIDs, 0, 0, "wave repair disabled by policy.max_wave_repair_cycles=0")
		markWaveCoverageFailed(coverage, 0, failingIDs, "wave repair disabled")
		syncWaveAcceptanceFromCoverage(wave, coverage)
		primary := fmt.Errorf("wave %s: checks failed (wave repair disabled)", wave.WaveID)
		if saveErr := state.SaveJSONAtomic(wavePath, wave); saveErr != nil {
			return errors.Join(primary, fmt.Errorf("persist wave state: %w", saveErr))
		}
		return primary
	}

	adapter, err := resolveWaveAdapter(req)
	if err != nil {
		return fmt.Errorf("wave verification: resolve adapter: %w", err)
	}

	// Track which derived check ids are still failing so subsequent cycles
	// only re-run the relevant ones (plus the broader quality gate).
	failingIDs := failingExecutionIDs(coverage)

	for cycle := 1; cycle <= cfg.Policy.MaxWaveRepairCycles; cycle++ {
		SendProgress(ctx, req.Progress, ProgressEvent{
			Type:     EventTicketDetail,
			TicketID: wave.WaveID,
			Detail:   fmt.Sprintf("wave repair cycle %d/%d", cycle, cfg.Policy.MaxWaveRepairCycles),
		})

		failureOutput := collectWaveVerificationOutput(combineCommandResults(qualityResults, derivedResults))
		instructions := renderWaveRepairInstructions(wave, changedFiles, failingIDs, cycle, failureOutput)
		appendWaveRepairCycle(coverage, cycle, failingIDs, "repair_pending", "wave repair worker dispatched")

		_, workerErr := adapter.RunWorker(ctx, runtime.WorkerRequest{
			RunID:           req.RunID,
			WaveID:          wave.WaveID,
			LeaseID:         fmt.Sprintf("wave-repair-%s-%d", wave.WaveID, cycle),
			Attempt:         cycle,
			Runtime:         cfg.Runtime.DefaultRuntime,
			WorktreePath:    req.RepoRoot,
			Instructions:    instructions,
			ExecutionConfig: executionConfigFromPolicy(cfg),
			OnProgress: func(detail string) {
				SendProgress(ctx, req.Progress, ProgressEvent{
					Type:     EventTicketDetail,
					TicketID: wave.WaveID,
					Detail:   detail,
				})
			},
		})
		if workerErr != nil {
			markWaveCoverageFailed(coverage, cycle, failingIDs, fmt.Sprintf("wave repair worker cycle %d: %v", cycle, workerErr))
			syncWaveAcceptanceFromCoverage(wave, coverage)
			primary := fmt.Errorf("wave %s: repair worker cycle %d: %w", wave.WaveID, cycle, workerErr)
			if saveErr := state.SaveJSONAtomic(wavePath, wave); saveErr != nil {
				return errors.Join(primary, fmt.Errorf("persist wave state: %w", saveErr))
			}
			return primary
		}

		// Re-run only the previously failing derived checks plus the broader
		// quality gate, not the entire derived set: relevant failures plus
		// the required broad gate, per the ticket's ACs.
		retryDerivedChecks := derivedChecksByIDs(derivedChecks, failingIDs)
		qualityResults, derivedResults, err = executeWaveChecks(ctx, req.RepoRoot, cfg.Verification, hasQualityCmds, retryDerivedChecks)
		if err != nil {
			return fmt.Errorf("wave verification: run checks after repair cycle %d: %w", cycle, err)
		}
		recordWaveExecutions(coverage, qualityResults, retryDerivedChecks, derivedResults, cycle)

		if waveExecutionsAllPassed(qualityResults, retryDerivedChecks, derivedResults) {
			markCycleCompleted(coverage, cycle)
			markRetriedFailuresRepaired(coverage, failingIDs)
			markWaveCoveragePassed(coverage, cycle)
			syncWaveAcceptanceFromCoverage(wave, coverage)
			return state.SaveJSONAtomic(wavePath, wave)
		}

		// Recompute failing ids for the next cycle: anything still failing
		// in either the broader quality gate or the retried derived checks.
		failingIDs = failingExecutionIDs(coverage)
		markCycleStillFailing(coverage, cycle, failingIDs)
	}

	finalFailingIDs := failingExecutionIDs(coverage)
	appendWaveCheckBlockers(coverage, finalFailingIDs, "unresolved after repair budget exhausted")
	recordWaveRepairLimit(coverage, finalFailingIDs, cfg.Policy.MaxWaveRepairCycles, cfg.Policy.MaxWaveRepairCycles,
		fmt.Sprintf("wave repair exhausted after %d cycle(s)", cfg.Policy.MaxWaveRepairCycles))
	markWaveCoverageFailed(coverage, cfg.Policy.MaxWaveRepairCycles, finalFailingIDs,
		fmt.Sprintf("wave checks still failing after %d repair cycle(s)", cfg.Policy.MaxWaveRepairCycles))
	syncWaveAcceptanceFromCoverage(wave, coverage)
	primary := fmt.Errorf("wave %s: checks still failing after %d repair cycle(s)", wave.WaveID, cfg.Policy.MaxWaveRepairCycles)
	if saveErr := state.SaveJSONAtomic(wavePath, wave); saveErr != nil {
		return errors.Join(primary, fmt.Errorf("persist wave state: %w", saveErr))
	}
	return primary
}

// resolveWaveAdapter returns the runtime adapter for wave-level repair workers.
func resolveWaveAdapter(req RunEpicRequest) (runtime.Adapter, error) {
	if req.Adapter != nil {
		return req.Adapter, nil
	}
	if req.AdapterFactory != nil {
		return req.AdapterFactory("")
	}
	return nil, fmt.Errorf("no runtime adapter available for wave repair")
}

// waveDerivationPlan synthesizes a minimal PlanArtifact suitable for the
// derivation layer. The wave doesn't have a real plan, so we use the wave id
// as the "ticket" identity and set a docs-leaning title when any markdown
// files were touched so the docs heuristic in DeriveChecks fires.
func waveDerivationPlan(wave state.WaveArtifact, changedFiles []string) state.PlanArtifact {
	title := fmt.Sprintf("wave %s", wave.WaveID)
	if anyMarkdownFile(changedFiles) {
		title += " (touches docs)"
	}
	return state.PlanArtifact{
		ArtifactMeta: state.ArtifactMeta{RunID: wave.RunID},
		TicketID:     wave.WaveID,
		Title:        title,
		OwnedPaths:   append([]string(nil), wave.PlannedScope...),
	}
}

func anyMarkdownFile(files []string) bool {
	for _, f := range files {
		if strings.HasSuffix(f, ".md") {
			return true
		}
	}
	return false
}

// promoteDerivedToWave rewrites a slice of ticket-scoped derived checks so
// they describe the wave: scope=wave, ticket id cleared, wave id stamped,
// advisory cleared (wave-level derived checks are required for the wave gate).
func promoteDerivedToWave(checks []state.ValidationCheck, waveID string) []state.ValidationCheck {
	if len(checks) == 0 {
		return nil
	}
	out := make([]state.ValidationCheck, 0, len(checks))
	for _, c := range checks {
		c.Scope = state.ValidationScopeWave
		c.WaveID = waveID
		c.TicketID = ""
		// Wave-level checks gate the wave; if they fail they should route
		// repair, so clear the advisory flag inherited from the ticket
		// derivation defaults.
		c.Advisory = false
		out = append(out, c)
	}
	return out
}

func derivedHasExecutableCommand(checks []state.ValidationCheck) bool {
	for _, c := range checks {
		if strings.TrimSpace(c.Command) != "" {
			return true
		}
	}
	return false
}

func derivedChecksByIDs(checks []state.ValidationCheck, ids []string) []state.ValidationCheck {
	if len(ids) == 0 || len(checks) == 0 {
		return nil
	}
	want := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}
	out := make([]state.ValidationCheck, 0, len(ids))
	for _, c := range checks {
		if _, ok := want[c.ID]; ok {
			out = append(out, c)
		}
	}
	return out
}

// newWaveValidationCoverage builds the initial wave-scope coverage artifact
// by recording declared (quality_commands → declared) and derived checks
// plus any skip notes from the derivation layer.
func newWaveValidationCoverage(
	wave state.WaveArtifact,
	plan state.PlanArtifact,
	qualityCommands []policy.QualityCommand,
	derivedChecks []state.ValidationCheck,
	skipped []state.ValidationCheckSkip,
) *state.ValidationCoverageArtifact {
	now := stateTime()
	coverage := &state.ValidationCoverageArtifact{
		ArtifactMeta: state.ArtifactMeta{
			SchemaVersion: artifactSchemaVersion,
			RunID:         wave.RunID,
			CreatedAt:     now,
			UpdatedAt:     now,
		},
		Scope:          state.ValidationScopeWave,
		WaveID:         wave.WaveID,
		ChildTicketIDs: append([]string(nil), wave.TicketIDs...),
	}

	if len(qualityCommands) > 0 {
		coverage.DeclaredChecks = buildWaveQualityChecks(plan.RunID, wave.WaveID, qualityCommands)
	}
	if len(derivedChecks) > 0 {
		coverage.DerivedChecks = append(coverage.DerivedChecks, derivedChecks...)
	}
	if len(skipped) > 0 {
		coverage.SkippedChecks = append(coverage.SkippedChecks, skipped...)
	}
	return coverage
}

// buildWaveQualityChecks materializes a ValidationCheck for each quality
// command line so executions can be attributed back to a stable id.
func buildWaveQualityChecks(runID, waveID string, cmds []policy.QualityCommand) []state.ValidationCheck {
	out := make([]state.ValidationCheck, 0)
	_ = runID
	for _, qc := range cmds {
		for _, raw := range qc.Run {
			cmd := strings.TrimSpace(raw)
			if cmd == "" {
				continue
			}
			out = append(out, state.ValidationCheck{
				ID:      declaredCheckID(waveID, cmd),
				Scope:   state.ValidationScopeWave,
				Source:  state.ValidationCheckSourceQuality,
				Command: cmd,
				Reason:  "quality_commands (wave gate)",
				WaveID:  waveID,
			})
		}
	}
	return out
}

// executeWaveChecks runs the configured quality commands and the supplied
// derived checks. Either set may be empty (when the policy disables quality
// commands or when no failing derived checks remain to retry). Returns the
// quality results, the derived results map (keyed by check id), and any
// fatal error.
func executeWaveChecks(
	ctx context.Context,
	repoRoot string,
	cfg policy.VerificationConfig,
	hasQualityCmds bool,
	derivedChecks []state.ValidationCheck,
) ([]verifycommand.CommandResult, map[string]verifycommand.CommandResult, error) {
	var qualityResults []verifycommand.CommandResult
	if hasQualityCmds {
		results, err := verifycommand.RunQualityCommands(ctx, repoRoot, cfg.QualityCommands, cfg)
		if err != nil {
			return nil, nil, fmt.Errorf("run quality commands: %w", err)
		}
		qualityResults = results
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
				return nil, nil, fmt.Errorf("run derived wave checks: %w", err)
			}
			for i, r := range results {
				if i >= len(ids) {
					break
				}
				derivedResults[ids[i]] = r
			}
		}
	}
	return qualityResults, derivedResults, nil
}

// recordWaveExecutions appends ValidationCheckExecution entries for the
// quality and derived results just completed. Each entry references the
// stable check id and the cycle attempt number.
func recordWaveExecutions(
	coverage *state.ValidationCoverageArtifact,
	qualityResults []verifycommand.CommandResult,
	derivedChecks []state.ValidationCheck,
	derivedResults map[string]verifycommand.CommandResult,
	cycle int,
) {
	if coverage == nil {
		return
	}
	for _, r := range qualityResults {
		coverage.ExecutedChecks = append(coverage.ExecutedChecks, state.ValidationCheckExecution{
			CheckID:    declaredCheckID(coverage.WaveID, r.Command),
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

// waveExecutionsAllPassed returns true when every recorded result in this
// pass passed (exit 0, no timeouts).
func waveExecutionsAllPassed(
	qualityResults []verifycommand.CommandResult,
	derivedChecks []state.ValidationCheck,
	derivedResults map[string]verifycommand.CommandResult,
) bool {
	if !verifycommand.DeriveVerificationPassed(qualityResults) {
		return false
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

// failingExecutionIDs collects the ids of executions whose latest result is
// failed, sorted for deterministic output.
func failingExecutionIDs(coverage *state.ValidationCoverageArtifact) []string {
	if coverage == nil {
		return nil
	}
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

// appendWaveCheckBlockers records a ValidationBlocker for each failing
// check id so the wave summary explains exactly which checks could not be
// repaired.
func appendWaveCheckBlockers(coverage *state.ValidationCoverageArtifact, failingIDs []string, suffix string) {
	if coverage == nil || len(failingIDs) == 0 {
		return
	}
	seen := make(map[string]struct{}, len(coverage.UnresolvedBlockers))
	for _, b := range coverage.UnresolvedBlockers {
		if b.CheckID != "" {
			seen[b.CheckID] = struct{}{}
		}
	}
	for _, id := range failingIDs {
		if _, ok := seen[id]; ok {
			continue
		}
		coverage.UnresolvedBlockers = append(coverage.UnresolvedBlockers, state.ValidationBlocker{
			CheckID: id,
			Reason:  fmt.Sprintf("wave check %s failed (%s)", id, suffix),
			Scope:   state.ValidationScopeWave,
		})
		seen[id] = struct{}{}
	}
}

// appendWaveRepairCycle records a repair cycle reference on the wave-scope
// coverage artifact. Each invocation creates one ref per failing check id so
// downstream UIs can link a check to the cycle that tried to repair it.
func appendWaveRepairCycle(coverage *state.ValidationCoverageArtifact, cycle int, failingIDs []string, status, notes string) {
	if coverage == nil {
		return
	}
	cycleID := fmt.Sprintf("%s-cycle-%d", coverage.WaveID, cycle)
	result := state.ValidationCheckResultPending
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "passed", "repaired":
		result = state.ValidationCheckResultRepaired
	case "blocked", "failed":
		result = state.ValidationCheckResultFailed
	}
	if len(failingIDs) == 0 {
		coverage.RepairRefs = append(coverage.RepairRefs, state.ValidationRepairRef{
			CycleID: cycleID,
			Result:  result,
			Scope:   state.ValidationScopeWave,
		})
		return
	}
	for _, id := range failingIDs {
		coverage.RepairRefs = append(coverage.RepairRefs, state.ValidationRepairRef{
			CheckID: id,
			CycleID: cycleID,
			Result:  result,
			Scope:   state.ValidationScopeWave,
		})
	}
	_ = notes
}

// markCycleCompleted updates the most recent repair cycle's repair refs from
// pending → repaired so the coverage history shows a successful cycle.
func markCycleCompleted(coverage *state.ValidationCoverageArtifact, cycle int) {
	if coverage == nil {
		return
	}
	cycleID := fmt.Sprintf("%s-cycle-%d", coverage.WaveID, cycle)
	for i := range coverage.RepairRefs {
		if coverage.RepairRefs[i].CycleID == cycleID && coverage.RepairRefs[i].Result == state.ValidationCheckResultPending {
			coverage.RepairRefs[i].Result = state.ValidationCheckResultRepaired
		}
	}
}

// markCycleStillFailing flips the cycle's pending refs to failed so the
// coverage history shows that the repair cycle did not converge.
func markCycleStillFailing(coverage *state.ValidationCoverageArtifact, cycle int, failingIDs []string) {
	if coverage == nil {
		return
	}
	cycleID := fmt.Sprintf("%s-cycle-%d", coverage.WaveID, cycle)
	stillFailing := make(map[string]struct{}, len(failingIDs))
	for _, id := range failingIDs {
		stillFailing[id] = struct{}{}
	}
	for i := range coverage.RepairRefs {
		ref := coverage.RepairRefs[i]
		if ref.CycleID != cycleID || ref.Result != state.ValidationCheckResultPending {
			continue
		}
		if _, still := stillFailing[ref.CheckID]; still {
			coverage.RepairRefs[i].Result = state.ValidationCheckResultFailed
		} else {
			coverage.RepairRefs[i].Result = state.ValidationCheckResultRepaired
		}
	}
}

// markRetriedFailuresRepaired walks failing executions and marks the most
// recent failed execution per check id as repaired by appending a synthetic
// repaired execution. This keeps the prior failed execution visible (audit
// trail) while letting downstream consumers inspect LatestExecution.
func markRetriedFailuresRepaired(coverage *state.ValidationCoverageArtifact, repairedIDs []string) {
	if coverage == nil || len(repairedIDs) == 0 {
		return
	}
	now := stateTime()
	want := make(map[string]struct{}, len(repairedIDs))
	for _, id := range repairedIDs {
		want[id] = struct{}{}
	}
	for _, id := range repairedIDs {
		if _, ok := want[id]; !ok {
			continue
		}
		coverage.ExecutedChecks = append(coverage.ExecutedChecks, state.ValidationCheckExecution{
			CheckID:    id,
			Result:     state.ValidationCheckResultRepaired,
			Attempt:    -1,
			FinishedAt: now,
		})
	}
}

// recordWaveRepairLimit attaches a ValidationRepairLimit to the wave coverage
// so artifacts and summaries can show which policy bound stopped the loop.
func recordWaveRepairLimit(coverage *state.ValidationCoverageArtifact, failingIDs []string, limit, reached int, reason string) {
	if coverage == nil {
		return
	}
	coverage.RepairLimit = &state.ValidationRepairLimit{
		Name:      "max_wave_repair_cycles",
		Limit:     limit,
		Reached:   reached,
		Reason:    reason,
		PolicyRef: "policy.max_wave_repair_cycles",
	}
	if coverage.BlockReason == "" && len(failingIDs) > 0 {
		coverage.BlockReason = fmt.Sprintf("%s; unresolved checks: %s", reason, strings.Join(failingIDs, ", "))
	} else if coverage.BlockReason == "" {
		coverage.BlockReason = reason
	}
	coverage.Closable = false
}

// markWaveCoveragePassed flips the coverage to closable and records a
// human-readable closure reason. cycles==0 means it passed on the first run.
func markWaveCoveragePassed(coverage *state.ValidationCoverageArtifact, cycles int) {
	if coverage == nil {
		return
	}
	coverage.Closable = true
	coverage.UpdatedAt = stateTime()
	if cycles == 0 {
		coverage.ClosureReason = "all wave checks passed on first attempt"
	} else {
		coverage.ClosureReason = fmt.Sprintf("all wave checks passed after %d repair cycle(s)", cycles)
	}
}

// markWaveCoverageFailed flips the coverage to non-closable and records the
// failing check ids and reason.
func markWaveCoverageFailed(coverage *state.ValidationCoverageArtifact, cycles int, failingIDs []string, reason string) {
	if coverage == nil {
		return
	}
	coverage.Closable = false
	coverage.UpdatedAt = stateTime()
	if coverage.BlockReason == "" {
		if len(failingIDs) > 0 {
			coverage.BlockReason = fmt.Sprintf("%s; unresolved checks: %s", reason, strings.Join(failingIDs, ", "))
		} else {
			coverage.BlockReason = reason
		}
	}
	_ = cycles
}

// syncWaveAcceptanceFromCoverage mirrors the closable/cycle/blocker info onto
// the wave's Acceptance map so legacy consumers (dashboards, log scrapers)
// keep working without reading the coverage artifact directly.
func syncWaveAcceptanceFromCoverage(wave *state.WaveArtifact, coverage *state.ValidationCoverageArtifact) {
	if wave == nil {
		return
	}
	if wave.Acceptance == nil {
		wave.Acceptance = map[string]any{}
	}
	if coverage == nil {
		return
	}
	wave.Acceptance["wave_verification_passed"] = coverage.Closable
	wave.Acceptance["wave_verification_cycles"] = waveLatestCycle(coverage)
	if len(coverage.RepairRefs) > 0 {
		ids := make([]string, 0, len(coverage.RepairRefs))
		seen := make(map[string]struct{}, len(coverage.RepairRefs))
		for _, ref := range coverage.RepairRefs {
			if _, ok := seen[ref.CycleID]; ok {
				continue
			}
			seen[ref.CycleID] = struct{}{}
			ids = append(ids, ref.CycleID)
		}
		sort.Strings(ids)
		wave.Acceptance["wave_repair_cycle_ids"] = ids
	}
	if len(coverage.UnresolvedBlockers) > 0 {
		reasons := make([]string, 0, len(coverage.UnresolvedBlockers))
		for _, b := range coverage.UnresolvedBlockers {
			reasons = append(reasons, b.Reason)
		}
		wave.Acceptance["wave_unresolved_blockers"] = reasons
	}
	if coverage.BlockReason != "" {
		wave.Acceptance["wave_block_reason"] = coverage.BlockReason
	}
}

// waveLatestCycle returns the highest cycle number present in the coverage's
// repair refs (cycle ids are formatted "<wave>-cycle-N").
func waveLatestCycle(coverage *state.ValidationCoverageArtifact) int {
	if coverage == nil {
		return 0
	}
	prefix := coverage.WaveID + "-cycle-"
	var max int
	for _, ref := range coverage.RepairRefs {
		if !strings.HasPrefix(ref.CycleID, prefix) {
			continue
		}
		n, err := parsePositiveInt(strings.TrimPrefix(ref.CycleID, prefix))
		if err == nil && n > max {
			max = n
		}
	}
	return max
}

func parsePositiveInt(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty integer")
	}
	var n int
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("not an integer: %q", s)
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}

// renderWaveRepairInstructions builds the instruction string passed to the
// repair worker. When failingIDs is non-empty the worker is told to focus on
// those specific checks; otherwise it sees the raw command output.
func renderWaveRepairInstructions(wave *state.WaveArtifact, changedFiles, failingIDs []string, cycle int, failureOutput string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "**Phase:** wave-repair\n")
	fmt.Fprintf(&b, "**Wave:** %s (ordinal %d), repair cycle %d\n\n", wave.WaveID, wave.Ordinal, cycle)

	b.WriteString("The following wave-level verification checks failed after all wave tickets were merged.\n")
	b.WriteString("Fix ONLY these failures. Do not re-implement wave tickets or change unrelated code.\n")
	b.WriteString("Commit your changes when done.\n\n")

	if len(failingIDs) > 0 {
		b.WriteString("### Failing Wave Checks\n\n")
		for _, id := range failingIDs {
			fmt.Fprintf(&b, "- `%s`\n", id)
		}
		b.WriteString("\n")
	}

	b.WriteString("### Failing Verification Output\n\n")
	if failureOutput != "" {
		b.WriteString("```\n")
		b.WriteString(failureOutput)
		b.WriteString("\n```\n\n")
	} else {
		b.WriteString("(no output captured — check exit codes above)\n\n")
	}

	if len(changedFiles) > 0 {
		b.WriteString("### Files Changed in This Wave\n\n")
		for _, f := range changedFiles {
			fmt.Fprintf(&b, "- `%s`\n", f)
		}
		b.WriteString("\n")
	}

	return b.String()
}

// combineCommandResults flattens the quality results and any derived results
// into a single slice ordered quality-first → derived. The derived map is
// iterated in a stable order to keep failure output reproducible across runs.
func combineCommandResults(quality []verifycommand.CommandResult, derived map[string]verifycommand.CommandResult) []verifycommand.CommandResult {
	out := make([]verifycommand.CommandResult, 0, len(quality)+len(derived))
	out = append(out, quality...)
	if len(derived) == 0 {
		return out
	}
	ids := make([]string, 0, len(derived))
	for id := range derived {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		out = append(out, derived[id])
	}
	return out
}

// collectWaveVerificationOutput reads stdout/stderr from failed verification
// results and returns a combined string capped at waveVerificationOutputLimit bytes.
func collectWaveVerificationOutput(results []verifycommand.CommandResult) string {
	var b strings.Builder
	for _, r := range results {
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
			data, err := os.ReadFile(path)
			if err != nil || len(data) == 0 {
				continue
			}
			remaining := waveVerificationOutputLimit - b.Len()
			if remaining <= 0 {
				return b.String()
			}
			if len(data) > remaining {
				data = data[:remaining]
			}
			b.Write(data)
			if !strings.HasSuffix(b.String(), "\n") {
				b.WriteByte('\n')
			}
		}
		if b.Len() >= waveVerificationOutputLimit {
			return b.String()
		}
	}
	return b.String()
}

// setPendingWaveVerification records in the run cursor that wave verification
// for waveID must complete before the next wave can start.
func setPendingWaveVerification(cursor map[string]any, waveID string) {
	if cursor != nil {
		cursor["pending_wave_verification"] = waveID
	}
}

// clearPendingWaveVerification removes the pending wave verification marker.
func clearPendingWaveVerification(cursor map[string]any) {
	if cursor != nil {
		delete(cursor, "pending_wave_verification")
	}
}

// pendingWaveVerificationID returns the wave ID pending verification, if any.
func pendingWaveVerificationID(cursor map[string]any) (string, bool) {
	if cursor == nil {
		return "", false
	}
	id, ok := cursor["pending_wave_verification"].(string)
	return id, ok && id != ""
}

// resumePendingWaveVerification checks if a prior wave is still pending
// verification in the run cursor and re-runs the loop if so. Returns an
// error if verification fails, in which case the caller should block the epic.
func resumePendingWaveVerification(
	ctx context.Context,
	req RunEpicRequest,
	cfg policy.Config,
	cursor map[string]any,
	runPath string,
	run *state.RunArtifact,
) error {
	pendingWaveID, ok := pendingWaveVerificationID(cursor)
	if !ok {
		return nil
	}

	wavePath := filepath.Join(req.RepoRoot, ".verk", "runs", req.RunID, "waves", pendingWaveID+".json")
	var pendingWave state.WaveArtifact
	if err := state.LoadJSON(wavePath, &pendingWave); err != nil {
		return fmt.Errorf("load pending wave %s for re-verification: %w", pendingWaveID, err)
	}

	// Skip if a prior verification already passed for this wave (resume after
	// a transient marker that was never cleared). The Acceptance flag is the
	// authoritative outcome; if it's true we just clear the cursor and move
	// on so we don't repeat completed work.
	if passed, _ := pendingWave.Acceptance["wave_verification_passed"].(bool); passed {
		clearPendingWaveVerification(cursor)
		run.UpdatedAt = time.Now().UTC()
		return state.SaveJSONAtomic(runPath, run)
	}

	SendProgress(ctx, req.Progress, ProgressEvent{
		Type:     EventTicketDetail,
		TicketID: pendingWaveID,
		Detail:   fmt.Sprintf("re-running wave verification for %s", pendingWaveID),
	})

	if err := runWaveVerificationLoop(ctx, req, cfg, &pendingWave, wavePath, pendingWave.ActualScope); err != nil {
		return err
	}

	clearPendingWaveVerification(cursor)
	run.UpdatedAt = time.Now().UTC()
	return state.SaveJSONAtomic(runPath, run)
}
