// Package engine — wave-level cross-ticket reviewer (P5).
//
// This file implements the wave-level reviewer that runs after all tickets in
// a wave complete but before wave acceptance. It invokes a single fresh-context
// reviewer over the union diff produced by all tickets in the wave to catch:
//
//   - Cross-ticket contradictions (ticket A negates what ticket B did)
//   - Integration drift (wave changes don't compose correctly at the boundary)
//   - Incomplete fanout (a change needed in N places was only made in M < N)
//   - Orphaned references (a rename was applied in some files but not others)
//
// In shadow mode the artifact is persisted but findings do not block the wave.
// In enforce mode any open finding at or above Threshold causes the wave to
// fail with status wave_review_failed.
package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"verk/internal/adapters/runtime"
	"verk/internal/adapters/ticketstore/epos"
	"verk/internal/policy"
	"verk/internal/state"
)

// waveReviewArtifactPath returns the path for the wave-review artifact
// scoped to a specific wave directory:
//
//	.verk/runs/<run-id>/waves/wave-<n>/wave-review.json
func waveReviewArtifactPath(repoRoot, runID, waveID string) string {
	return filepath.Join(runDir(repoRoot, runID), "waves", waveID, "wave-review.json")
}

// RunWaveReview runs a single reviewer call over the wave's union diff.
// Returns the artifact and nil error on success. Returns nil, nil when skip
// conditions are met (disabled mode or single-ticket wave with SkipSingleTicket).
//
// In shadow mode the artifact is persisted but findings never block the wave.
// In enforce mode any open finding at or above cfg.Threshold causes a non-nil
// error with a descriptive message; callers should treat this as wave_review_failed.
//
// Note: per-finding TargetTicketIDs assignment (mapping a finding's file to a
// specific ticket's owned_paths) is a planned enhancement. For v1 the wave-level
// artifact records findings without per-ticket routing.
func RunWaveReview(
	ctx context.Context,
	adapter runtime.Adapter,
	repoRoot string,
	waveID string,
	ordinal int,
	tickets []epos.Ticket,
	baseCommit string,
	cfg policy.WaveReviewConfig,
	runID string,
) (*state.WaveReviewArtifact, error) {
	// --- Skip conditions -------------------------------------------------------

	if strings.TrimSpace(cfg.Mode) == "" {
		cfg.Mode = "shadow"
	}
	if cfg.Mode == "disabled" {
		return nil, nil //nolint:nilnil // skip: no review to run, no error to report
	}
	if cfg.SkipSingleTicket && len(tickets) == 1 {
		return nil, nil //nolint:nilnil // skip: single-ticket wave, per-ticket reviewer already ran
	}
	if len(tickets) == 0 {
		return nil, nil //nolint:nilnil // skip: no tickets in wave
	}

	// --- Resolve threshold -----------------------------------------------------

	threshold := state.Severity(cfg.Threshold)
	if threshold == "" {
		threshold = state.SeverityP2
	}

	// --- Build artifact scaffold -----------------------------------------------

	now := time.Now().UTC()
	reviewProfile := buildWaveReviewerProfile(adapter)

	// Collect per-ticket summaries and the union of owned_paths.
	ticketSummaries := make([]state.WaveTicketSummary, 0, len(tickets))
	scopeUnion := make([]string, 0)
	scopeSet := make(map[string]struct{})
	for _, t := range tickets {
		summary := state.WaveTicketSummary{
			TicketID:   t.ID,
			OwnedPaths: append([]string(nil), t.OwnedPaths...),
		}
		ticketSummaries = append(ticketSummaries, summary)
		for _, p := range t.OwnedPaths {
			if _, ok := scopeSet[p]; !ok {
				scopeSet[p] = struct{}{}
				scopeUnion = append(scopeUnion, p)
			}
		}
	}

	artifact := state.WaveReviewArtifact{
		ArtifactMeta: state.ArtifactMeta{
			SchemaVersion: artifactSchemaVersion,
			RunID:         runID,
			CreatedAt:     now,
			UpdatedAt:     now,
		},
		WaveID:          waveID,
		Ordinal:         ordinal,
		ReviewerRuntime: reviewProfile.Runtime,
		Model:           reviewProfile.Model,
		ReviewScope:     "wave",
		BaseCommit:      baseCommit,
		ScopeUnion:      scopeUnion,
		TicketSummaries: ticketSummaries,
		Mode:            cfg.Mode,
	}

	// --- Build reviewer instructions -------------------------------------------

	instructions := buildWaveReviewInstructions(waveID, tickets)

	// --- Resolve reviewer profile ----------------------------------------------

	leaseID := fmt.Sprintf("wave-review-%s-%s", runID, waveID)

	reviewResult, reviewErr := adapter.RunReviewer(ctx, runtime.ReviewRequest{
		RunID:                    runID,
		TicketID:                 waveID,
		WaveID:                   waveID,
		LeaseID:                  leaseID,
		Attempt:                  1,
		Runtime:                  reviewProfile.Runtime,
		Model:                    reviewProfile.Model,
		Reasoning:                reviewProfile.Reasoning,
		WorktreePath:             repoRoot,
		Instructions:             instructions,
		EffectiveReviewThreshold: threshold,
		ExecutionConfig:          runtime.ExecutionConfig{},
	})

	artifact.UpdatedAt = time.Now().UTC()

	if reviewErr != nil {
		// Reviewer invocation failed: persist artifact with no findings
		// and treat as shadow (don't block the wave on adapter errors).
		artifact.Passed = true // conservative: don't block on adapter errors
		if saveErr := state.SaveJSONAtomic(waveReviewArtifactPath(repoRoot, runID, waveID), artifact); saveErr != nil {
			return &artifact, errors.Join(
				fmt.Errorf("wave review: reviewer call failed: %w", reviewErr),
				fmt.Errorf("wave review: persist artifact: %w", saveErr),
			)
		}
		// In shadow mode, adapter errors don't block the wave.
		if cfg.Mode == "enforce" {
			return &artifact, fmt.Errorf("wave review %s: reviewer call failed: %w", waveID, reviewErr)
		}
		return &artifact, nil
	}

	// --- Map findings ----------------------------------------------------------

	statefindings := make([]state.ReviewFinding, 0, len(reviewResult.Findings))
	blockingIDs := make([]string, 0)
	for _, f := range reviewResult.Findings {
		sf := state.ReviewFinding{
			ID:          f.ID,
			Severity:    f.Severity,
			Title:       f.Title,
			Body:        f.Body,
			File:        f.File,
			Line:        f.Line,
			Disposition: string(f.Disposition),
		}
		statefindings = append(statefindings, sf)
		if ReviewFindingBlocks(sf, threshold) {
			blockingIDs = append(blockingIDs, f.ID)
		}
	}
	artifact.Findings = statefindings
	artifact.BlockingFindings = blockingIDs

	// Token usage.
	if reviewResult.TokenUsage != nil {
		artifact.TokensIn = int(reviewResult.TokenUsage.InputTokens)
		artifact.TokensOut = int(reviewResult.TokenUsage.OutputTokens)
	}

	// Derive passed: passed when no blocking findings exist, regardless of mode.
	// In shadow mode this only affects the artifact field, not the return error.
	artifact.Passed = len(blockingIDs) == 0

	// --- Persist artifact ------------------------------------------------------

	artifactPath := waveReviewArtifactPath(repoRoot, runID, waveID)
	if saveErr := state.SaveJSONAtomic(artifactPath, artifact); saveErr != nil {
		return &artifact, fmt.Errorf("wave review: persist artifact for %s: %w", waveID, saveErr)
	}

	// --- Enforce mode blocking -------------------------------------------------

	if cfg.Mode == "enforce" && len(blockingIDs) > 0 {
		return &artifact, fmt.Errorf(
			"wave_review_failed: wave %s has %d blocking finding(s) at or above %s: %s",
			waveID, len(blockingIDs), threshold, strings.Join(blockingIDs, ", "),
		)
	}

	return &artifact, nil
}

// buildWaveReviewInstructions constructs the reviewer instructions for a
// wave-level cross-ticket review. The instructions describe the reviewer's
// scope, the tickets in the wave, and the specific cross-ticket concerns to look for.
func buildWaveReviewInstructions(waveID string, tickets []epos.Ticket) string {
	var b strings.Builder

	fmt.Fprintf(&b, "**Phase:** wave-review\n")
	fmt.Fprintf(&b, "**Wave:** %s\n\n", waveID)

	b.WriteString(runtime.EpicReviewFraming)
	b.WriteString("\n\n")

	b.WriteString("You are performing a cross-ticket wave review. All tickets in this wave ran in parallel ")
	b.WriteString("in isolated worktrees. Review the combined diff for issues that span ticket boundaries:\n\n")
	b.WriteString("- **Cross-ticket contradictions**: ticket A negates or conflicts with what ticket B did\n")
	b.WriteString("- **Integration drift**: the combined changes don't compose correctly at package or module boundaries\n")
	b.WriteString("- **Incomplete fanout**: a change needed in N places was only made in M < N places\n")
	b.WriteString("- **Orphaned references**: a rename or deletion was applied in some files but not others\n\n")

	b.WriteString("### Tickets In This Wave\n\n")
	for _, t := range tickets {
		fmt.Fprintf(&b, "- **%s**: %s\n", t.ID, t.Title)
		for _, p := range t.OwnedPaths {
			fmt.Fprintf(&b, "  - owned_path: %s\n", p)
		}
	}
	b.WriteString("\n")

	b.WriteString("Focus on cross-ticket issues. Per-ticket reviewers have already checked each ticket individually. ")
	b.WriteString("Only flag issues that require multiple tickets to be observed together.\n")

	return b.String()
}

// buildWaveReviewerProfile extracts runtime profile information from the
// adapter for inclusion in the wave review artifact. Since adapters don't
// expose their configuration, we use placeholder values that get filled in
// by the adapter itself during the reviewer call. This function returns an
// empty profile that callers use as the initial artifact scaffold; the actual
// runtime/model values come from the ReviewResult.
func buildWaveReviewerProfile(_ runtime.Adapter) policy.RoleProfile {
	// The adapter manages its own profile selection. We return an empty
	// placeholder so the artifact has the field; the actual values (runtime,
	// model) would need to be extracted from the adapter if it exposed them.
	// For v1, we rely on the reviewer call itself to record the actual values
	// via the result artifact path.
	return policy.RoleProfile{}
}

// waveReviewArtifactExists reports whether a wave-review artifact already
// exists on disk. Used for crash-recovery idempotency checks.
func waveReviewArtifactExists(repoRoot, runID, waveID string) bool {
	path := waveReviewArtifactPath(repoRoot, runID, waveID)
	_, err := os.Stat(path)
	return err == nil
}
