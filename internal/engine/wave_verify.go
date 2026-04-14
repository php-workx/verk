package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"verk/internal/adapters/runtime"
	verifycommand "verk/internal/adapters/verify/command"
	"verk/internal/policy"
	"verk/internal/state"
)

const waveVerificationOutputLimit = 4 * 1024 // 4 KB

// runWaveVerificationLoop runs quality commands against the merged wave baseline
// and, if they fail, invokes a repair worker to fix the issues. It repeats up to
// cfg.Policy.MaxWaveRepairCycles times. The wave artifact is updated in place and
// re-saved after each attempt.
//
// Returns nil if verification passes (possibly after repairs), or an error if
// quality commands still fail after exhausting all repair cycles.
func runWaveVerificationLoop(
	ctx context.Context,
	req RunEpicRequest,
	cfg policy.Config,
	wave *state.WaveArtifact,
	wavePath string,
	changedFiles []string,
) error {
	if len(cfg.Verification.QualityCommands) == 0 {
		return nil
	}

	results, err := verifycommand.RunQualityCommands(ctx, req.RepoRoot, cfg.Verification.QualityCommands, cfg.Verification)
	if err != nil {
		return fmt.Errorf("wave verification: run quality commands: %w", err)
	}

	if verifycommand.DeriveVerificationPassed(results) {
		wave.Acceptance["wave_verification_passed"] = true
		wave.Acceptance["wave_verification_cycles"] = 0
		return state.SaveJSONAtomic(wavePath, wave)
	}

	if cfg.Policy.MaxWaveRepairCycles == 0 {
		wave.Acceptance["wave_verification_passed"] = false
		wave.Acceptance["wave_verification_cycles"] = 0
		_ = state.SaveJSONAtomic(wavePath, wave)
		return fmt.Errorf("wave %s: quality commands failed (wave repair disabled)", wave.WaveID)
	}

	adapter, err := resolveWaveAdapter(req)
	if err != nil {
		return fmt.Errorf("wave verification: resolve adapter: %w", err)
	}

	for cycle := 1; cycle <= cfg.Policy.MaxWaveRepairCycles; cycle++ {
		SendProgress(req.Progress, ProgressEvent{
			Type:     EventTicketDetail,
			TicketID: wave.WaveID,
			Detail:   fmt.Sprintf("wave repair cycle %d/%d", cycle, cfg.Policy.MaxWaveRepairCycles),
		})

		output := collectWaveVerificationOutput(results)
		instructions := renderWaveRepairInstructions(wave, changedFiles, cycle, output)

		_, err := adapter.RunWorker(ctx, runtime.WorkerRequest{
			RunID:           req.RunID,
			WaveID:          wave.WaveID,
			LeaseID:         fmt.Sprintf("wave-repair-%s-%d", wave.WaveID, cycle),
			Attempt:         cycle,
			Runtime:         cfg.Runtime.DefaultRuntime,
			WorktreePath:    req.RepoRoot,
			Instructions:    instructions,
			ExecutionConfig: executionConfigFromPolicy(cfg),
			OnProgress: func(detail string) {
				SendProgress(req.Progress, ProgressEvent{
					Type:     EventTicketDetail,
					TicketID: wave.WaveID,
					Detail:   detail,
				})
			},
		})
		if err != nil {
			wave.Acceptance["wave_verification_passed"] = false
			wave.Acceptance["wave_verification_cycles"] = cycle
			_ = state.SaveJSONAtomic(wavePath, wave)
			return fmt.Errorf("wave %s: repair worker cycle %d: %w", wave.WaveID, cycle, err)
		}

		results, err = verifycommand.RunQualityCommands(ctx, req.RepoRoot, cfg.Verification.QualityCommands, cfg.Verification)
		if err != nil {
			return fmt.Errorf("wave verification: run quality commands after repair cycle %d: %w", cycle, err)
		}
		if verifycommand.DeriveVerificationPassed(results) {
			wave.Acceptance["wave_verification_passed"] = true
			wave.Acceptance["wave_verification_cycles"] = cycle
			return state.SaveJSONAtomic(wavePath, wave)
		}
	}

	wave.Acceptance["wave_verification_passed"] = false
	wave.Acceptance["wave_verification_cycles"] = cfg.Policy.MaxWaveRepairCycles
	_ = state.SaveJSONAtomic(wavePath, wave)
	return fmt.Errorf("wave %s: quality commands still failing after %d repair cycle(s)", wave.WaveID, cfg.Policy.MaxWaveRepairCycles)
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

// renderWaveRepairInstructions builds the instruction string passed to the repair worker.
func renderWaveRepairInstructions(wave *state.WaveArtifact, changedFiles []string, cycle int, failureOutput string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "**Phase:** wave-repair\n")
	fmt.Fprintf(&b, "**Wave:** %s (ordinal %d), repair cycle %d\n\n", wave.WaveID, wave.Ordinal, cycle)

	b.WriteString("The following verification commands failed after all wave tickets were merged.\n")
	b.WriteString("Fix ONLY these failures. Do not re-implement wave tickets or change unrelated code.\n")
	b.WriteString("Commit your changes when done.\n\n")

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

	SendProgress(req.Progress, ProgressEvent{
		Type:     EventTicketDetail,
		TicketID: pendingWaveID,
		Detail:   fmt.Sprintf("re-running wave verification for %s", pendingWaveID),
	})

	if err := runWaveVerificationLoop(ctx, req, cfg, &pendingWave, wavePath, pendingWave.ActualScope); err != nil {
		return err
	}

	clearPendingWaveVerification(cursor)
	return state.SaveJSONAtomic(runPath, run)
}
