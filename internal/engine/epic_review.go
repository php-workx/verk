// Package engine — epic plan-time reviewer (P6).
//
// This file implements the epic plan-time reviewer that runs before the first
// wave dispatches. It invokes a single fresh-context reviewer over the root
// ticket and child ticket plans to catch plan-level gaps before any
// implementation work begins.
//
// This is the plan-time pass only. The acceptance-time epic review that runs
// after all waves complete is handled by the existing epic closure gate in
// epic_gate.go.
//
// In shadow mode the artifact is persisted at
// .verk/runs/<run-id>/epic-review-plan.json but findings do not block the epic.
// In enforce mode any open finding at or above Threshold blocks the epic with
// reason epic_plan_review_gap before any wave is dispatched.
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

// epicPlanReviewArtifactPath returns the path for the epic plan-review artifact:
//
//	.verk/runs/<run-id>/epic-review-plan.json
func epicPlanReviewArtifactPath(repoRoot, runID string) string {
	return filepath.Join(runDir(repoRoot, runID), "epic-review-plan.json")
}

// epicPlanReviewArtifactExists reports whether an epic plan-review artifact
// already exists on disk. Used for crash-recovery idempotency checks.
func epicPlanReviewArtifactExists(repoRoot, runID string) bool {
	path := epicPlanReviewArtifactPath(repoRoot, runID)
	_, err := os.Stat(path)
	return err == nil
}

// RunEpicPlanReview runs a single reviewer call over the epic's plan
// (root ticket + child ticket plans) before the first wave dispatches.
//
// Returns the artifact and nil error on success. Returns nil, nil when skip
// conditions are met (disabled mode or fewer than PlanMinTickets children).
//
// In shadow mode the artifact is persisted but findings never block the epic.
// In enforce mode any open finding at or above cfg.Threshold blocks the epic
// with a non-nil error containing reason "epic_plan_review_gap".
func RunEpicPlanReview(
	ctx context.Context,
	adapter runtime.Adapter,
	repoRoot string,
	rootTicket epos.Ticket,
	children []epos.Ticket,
	cfg policy.EpicReviewConfig,
	runID string,
) (*state.EpicPlanReviewArtifact, error) {
	// --- Skip conditions -------------------------------------------------------

	if strings.TrimSpace(cfg.PlanMode) == "" {
		cfg.PlanMode = "shadow"
	}
	if cfg.PlanMode == "disabled" {
		return nil, nil //nolint:nilnil // skip: no review to run, no error to report
	}
	if len(children) < cfg.PlanMinTickets {
		return nil, nil //nolint:nilnil // skip: too few tickets for a meaningful plan review
	}

	// --- Resolve threshold -----------------------------------------------------

	threshold := state.Severity(cfg.Threshold)
	if threshold == "" {
		threshold = state.SeverityP2
	}

	// --- Build artifact scaffold -----------------------------------------------

	now := time.Now().UTC()

	ticketSummaries := make([]state.WaveTicketSummary, 0, len(children)+1)
	// Root ticket summary.
	ticketSummaries = append(ticketSummaries, state.WaveTicketSummary{
		TicketID:   rootTicket.ID,
		OwnedPaths: append([]string(nil), rootTicket.OwnedPaths...),
	})
	for _, t := range children {
		ticketSummaries = append(ticketSummaries, state.WaveTicketSummary{
			TicketID:   t.ID,
			OwnedPaths: append([]string(nil), t.OwnedPaths...),
		})
	}

	artifact := state.EpicPlanReviewArtifact{
		ArtifactMeta: state.ArtifactMeta{
			SchemaVersion: artifactSchemaVersion,
			RunID:         runID,
			CreatedAt:     now,
			UpdatedAt:     now,
		},
		ReviewScope:     "epic_plan",
		TicketSummaries: ticketSummaries,
		Mode:            cfg.PlanMode,
	}

	// --- Build reviewer instructions -------------------------------------------

	allTickets := append([]epos.Ticket{rootTicket}, children...)
	instructions := buildEpicPlanReviewInstructions(rootTicket.ID, allTickets)

	// --- Invoke reviewer -------------------------------------------------------

	leaseID := fmt.Sprintf("epic-plan-review-%s-%s", runID, rootTicket.ID)

	reviewResult, reviewErr := adapter.RunReviewer(ctx, runtime.ReviewRequest{
		RunID:                    runID,
		TicketID:                 rootTicket.ID,
		LeaseID:                  leaseID,
		Attempt:                  1,
		WorktreePath:             repoRoot,
		Instructions:             instructions,
		EffectiveReviewThreshold: threshold,
		ExecutionConfig:          runtime.ExecutionConfig{},
	})

	artifact.UpdatedAt = time.Now().UTC()

	if reviewErr != nil {
		// Reviewer invocation failed: persist artifact with no findings.
		// In shadow mode, adapter errors don't block the epic.
		artifact.Passed = true // conservative: don't block on adapter errors
		if saveErr := state.SaveJSONAtomic(epicPlanReviewArtifactPath(repoRoot, runID), artifact); saveErr != nil {
			return &artifact, errors.Join(
				fmt.Errorf("epic plan review: reviewer call failed: %w", reviewErr),
				fmt.Errorf("epic plan review: persist artifact: %w", saveErr),
			)
		}
		if cfg.PlanMode == "enforce" {
			return &artifact, fmt.Errorf("epic_plan_review_gap: epic %s: reviewer call failed: %w", rootTicket.ID, reviewErr)
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

	artifact.Passed = len(blockingIDs) == 0

	// --- Persist artifact ------------------------------------------------------

	artifactPath := epicPlanReviewArtifactPath(repoRoot, runID)
	if saveErr := state.SaveJSONAtomic(artifactPath, artifact); saveErr != nil {
		return &artifact, fmt.Errorf("epic plan review: persist artifact for %s: %w", rootTicket.ID, saveErr)
	}

	// --- Enforce mode blocking -------------------------------------------------

	if cfg.PlanMode == "enforce" && len(blockingIDs) > 0 {
		return &artifact, fmt.Errorf(
			"epic_plan_review_gap: epic %s has %d blocking plan-level finding(s) at or above %s: %s",
			rootTicket.ID, len(blockingIDs), threshold, strings.Join(blockingIDs, ", "),
		)
	}

	return &artifact, nil
}

// buildEpicPlanReviewInstructions constructs the reviewer instructions for an
// epic plan-time review. The reviewer is asked to evaluate the plan (ticket
// descriptions, acceptance criteria, owned_paths, deps) before any
// implementation begins.
func buildEpicPlanReviewInstructions(epicID string, tickets []epos.Ticket) string {
	var b strings.Builder

	fmt.Fprintf(&b, "**Phase:** epic-plan-review\n")
	fmt.Fprintf(&b, "**Epic:** %s\n\n", epicID)

	b.WriteString("You are performing a plan-time review of an epic before implementation begins. ")
	b.WriteString("No code has been written yet. Evaluate the ticket plans for:\n\n")
	b.WriteString("- **Completeness**: do the tickets together cover the full scope of the epic?\n")
	b.WriteString("- **Traceability**: can each ticket's acceptance criteria be traced to the epic goal?\n")
	b.WriteString("- **Scope clarity**: are owned_paths realistic and non-overlapping across tickets?\n")
	b.WriteString("- **Dependency correctness**: are ticket deps declared correctly (no cycles, no missing deps)?\n")
	b.WriteString("- **Integration gaps**: are there cross-ticket integration concerns not covered by any ticket?\n")
	b.WriteString("- **Missing tickets**: is there obvious work missing entirely from the plan?\n\n")

	b.WriteString("Do NOT review code — there is no code yet. Focus on the plan only.\n\n")

	b.WriteString("### Tickets In This Epic\n\n")
	for _, t := range tickets {
		fmt.Fprintf(&b, "**%s**: %s\n", t.ID, t.Title)
		if len(t.OwnedPaths) > 0 {
			b.WriteString("  Owned paths:\n")
			for _, p := range t.OwnedPaths {
				fmt.Fprintf(&b, "    - %s\n", p)
			}
		}
		if len(t.Deps) > 0 {
			b.WriteString("  Dependencies:\n")
			for _, d := range t.Deps {
				fmt.Fprintf(&b, "    - %s\n", d)
			}
		}
		if len(t.AcceptanceCriteria) > 0 {
			b.WriteString("  Acceptance criteria:\n")
			for _, ac := range t.AcceptanceCriteria {
				fmt.Fprintf(&b, "    - %s\n", ac)
			}
		}
		if len(t.TestCases) > 0 {
			b.WriteString("  Test cases:\n")
			for _, tc := range t.TestCases {
				fmt.Fprintf(&b, "    - %s\n", tc)
			}
		}
		b.WriteString("\n")
	}

	return b.String()
}
