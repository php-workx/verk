package engine

import (
	"context"
	"strings"
	"testing"
	"time"
	"verk/internal/adapters/runtime"
	runtimefake "verk/internal/adapters/runtime/fake"
	"verk/internal/adapters/ticketstore/epos"
	"verk/internal/policy"
	"verk/internal/state"
)

// epicReviewTestTime is a stable timestamp for epic review test fixtures.
var epicReviewTestTime = time.Date(2026, 4, 16, 10, 0, 0, 0, time.UTC)

// epicPlanReviewPassedResult builds a minimal valid passing ReviewResult for
// plan-time epic review.
func epicPlanReviewPassedResult(leaseID string) runtime.ReviewResult {
	return runtime.ReviewResult{
		Status:             runtime.WorkerStatusDone,
		RetryClass:         runtime.RetryClassTerminal,
		LeaseID:            leaseID,
		StartedAt:          epicReviewTestTime,
		FinishedAt:         epicReviewTestTime.Add(time.Second),
		ReviewStatus:       runtime.ReviewStatusPassed,
		Summary:            "plan looks complete and coherent",
		Findings:           nil,
		ResultArtifactPath: "plan-review-artifact.json",
	}
}

// epicPlanReviewP2FindingResult builds a valid ReviewResult with one open P2
// finding at or above the default P2 threshold.
func epicPlanReviewP2FindingResult(leaseID string) runtime.ReviewResult {
	return runtime.ReviewResult{
		Status:             runtime.WorkerStatusDone,
		RetryClass:         runtime.RetryClassTerminal,
		LeaseID:            leaseID,
		StartedAt:          epicReviewTestTime,
		FinishedAt:         epicReviewTestTime.Add(time.Second),
		ReviewStatus:       runtime.ReviewStatusFindings,
		Summary:            "plan has integration gap",
		ResultArtifactPath: "plan-review-artifact.json",
		Findings: []runtime.ReviewFinding{
			{
				ID:          "plan-finding-1",
				Severity:    runtime.SeverityP2,
				Title:       "integration ticket missing for cross-service boundary",
				Body:        "No ticket covers the integration between the API and the worker service.",
				File:        "",
				Line:        0,
				Disposition: runtime.ReviewDispositionOpen,
			},
		},
	}
}

// makeEpicPlanTickets builds a root ticket and child tickets for testing.
func makeEpicPlanTickets(childCount int) (epos.Ticket, []epos.Ticket) {
	root := epos.Ticket{
		ID:    "epic-root",
		Title: "Root Epic Ticket",
		AcceptanceCriteria: []string{
			"All child tickets closed",
			"Integration tested",
		},
		OwnedPaths: []string{"internal"},
	}
	children := make([]epos.Ticket, childCount)
	for i := range children {
		children[i] = epos.Ticket{
			ID:         "child-" + string(rune('a'+i)),
			Title:      "Child Ticket " + string(rune('A'+i)),
			Status:     epos.StatusOpen,
			OwnedPaths: []string{"internal/pkg" + string(rune('a'+i))},
			AcceptanceCriteria: []string{
				"Feature implemented",
				"Tests added",
			},
		}
	}
	return root, children
}

// TestRunEpicPlanReview_FewerThanMinTickets verifies that when there are
// fewer children than PlanMinTickets, the review is skipped.
func TestRunEpicPlanReview_FewerThanMinTickets(t *testing.T) {
	repoRoot := t.TempDir()
	adapter := runtimefake.New(nil, nil)

	cfg := policy.EpicReviewConfig{
		PlanMode:       "enforce",
		PlanMinTickets: 3,
		Threshold:      "P2",
	}

	root, children := makeEpicPlanTickets(2) // 2 < 3 minimum

	artifact, err := RunEpicPlanReview(
		context.Background(), adapter, repoRoot,
		root, children, cfg, "run-few-tickets",
	)
	if err != nil {
		t.Fatalf("expected nil error when fewer than min tickets, got: %v", err)
	}
	if artifact != nil {
		t.Errorf("expected nil artifact when skipping (fewer than min tickets), got: %+v", artifact)
	}

	if calls := adapter.ReviewRequests(); len(calls) != 0 {
		t.Errorf("expected 0 reviewer calls, got %d", len(calls))
	}
}

// TestRunEpicPlanReview_DisabledMode verifies that when PlanMode="disabled",
// the review is skipped with nil, nil.
func TestRunEpicPlanReview_DisabledMode(t *testing.T) {
	repoRoot := t.TempDir()
	adapter := runtimefake.New(nil, nil)

	cfg := policy.EpicReviewConfig{
		PlanMode:       "disabled",
		PlanMinTickets: 2,
		Threshold:      "P2",
	}

	root, children := makeEpicPlanTickets(5) // enough tickets, but disabled

	artifact, err := RunEpicPlanReview(
		context.Background(), adapter, repoRoot,
		root, children, cfg, "run-disabled-epic",
	)
	if err != nil {
		t.Fatalf("expected nil error for disabled mode, got: %v", err)
	}
	if artifact != nil {
		t.Errorf("expected nil artifact for disabled mode, got: %+v", artifact)
	}

	if calls := adapter.ReviewRequests(); len(calls) != 0 {
		t.Errorf("expected 0 reviewer calls for disabled mode, got %d", len(calls))
	}
}

// TestRunEpicPlanReview_ShadowMode_P2Finding verifies that in shadow mode an
// open P2 finding is recorded but the epic is NOT blocked (no error returned).
func TestRunEpicPlanReview_ShadowMode_P2Finding(t *testing.T) {
	repoRoot := t.TempDir()

	leaseID := "epic-plan-review-run-shadow-epic-root"
	adapter := runtimefake.New(nil, []runtime.ReviewResult{
		epicPlanReviewP2FindingResult(leaseID),
	})

	cfg := policy.EpicReviewConfig{
		PlanMode:       "shadow",
		PlanMinTickets: 2,
		Threshold:      "P2",
	}

	root, children := makeEpicPlanTickets(3)

	artifact, err := RunEpicPlanReview(
		context.Background(), adapter, repoRoot,
		root, children, cfg, "run-shadow",
	)
	if err != nil {
		t.Fatalf("shadow mode: expected nil error even with P2 finding, got: %v", err)
	}
	if artifact == nil {
		t.Fatalf("shadow mode: expected non-nil artifact, got nil")
	}

	if len(artifact.Findings) != 1 {
		t.Errorf("shadow mode: expected 1 finding, got %d", len(artifact.Findings))
	}
	if len(artifact.BlockingFindings) != 1 {
		t.Errorf("shadow mode: expected 1 blocking finding ID, got %d", len(artifact.BlockingFindings))
	}
	if artifact.Passed {
		t.Error("shadow mode: expected Passed=false with open P2 finding")
	}
	if artifact.ReviewScope != "epic_plan" {
		t.Errorf("shadow mode: expected ReviewScope=epic_plan, got %q", artifact.ReviewScope)
	}
	if artifact.Mode != "shadow" {
		t.Errorf("shadow mode: expected Mode=shadow, got %q", artifact.Mode)
	}

	// Artifact must be persisted to disk.
	var loaded state.EpicPlanReviewArtifact
	artifactPath := epicPlanReviewArtifactPath(repoRoot, "run-shadow")
	if err := state.LoadJSON(artifactPath, &loaded); err != nil {
		t.Fatalf("shadow mode: load persisted artifact: %v", err)
	}
	if len(loaded.Findings) != 1 {
		t.Errorf("shadow mode: persisted artifact: expected 1 finding, got %d", len(loaded.Findings))
	}
}

// TestRunEpicPlanReview_EnforceMode_P2FindingBlocksEpic verifies that in
// enforce mode, an open P2 finding causes RunEpicPlanReview to return a
// non-nil error containing "epic_plan_review_gap".
func TestRunEpicPlanReview_EnforceMode_P2FindingBlocksEpic(t *testing.T) {
	repoRoot := t.TempDir()

	leaseID := "epic-plan-review-run-enforce-epic-root"
	adapter := runtimefake.New(nil, []runtime.ReviewResult{
		epicPlanReviewP2FindingResult(leaseID),
	})

	cfg := policy.EpicReviewConfig{
		PlanMode:       "enforce",
		PlanMinTickets: 2,
		Threshold:      "P2",
	}

	root, children := makeEpicPlanTickets(3)

	artifact, err := RunEpicPlanReview(
		context.Background(), adapter, repoRoot,
		root, children, cfg, "run-enforce",
	)

	if err == nil {
		t.Fatal("enforce mode: expected non-nil error for open P2 finding, got nil")
	}
	if !strings.Contains(err.Error(), "epic_plan_review_gap") {
		t.Errorf("enforce mode: expected error to contain 'epic_plan_review_gap', got: %v", err)
	}
	if artifact == nil {
		t.Fatal("enforce mode: expected non-nil artifact even on block, got nil")
	}
	if artifact.Passed {
		t.Error("enforce mode: expected Passed=false with open P2 finding")
	}

	// Artifact must be persisted to disk.
	var loaded state.EpicPlanReviewArtifact
	artifactPath := epicPlanReviewArtifactPath(repoRoot, "run-enforce")
	if err := state.LoadJSON(artifactPath, &loaded); err != nil {
		t.Fatalf("enforce mode: load persisted artifact after block: %v", err)
	}
	if loaded.Mode != "enforce" {
		t.Errorf("enforce mode: persisted artifact Mode=%q, want %q", loaded.Mode, "enforce")
	}
}

// TestRunEpicPlanReview_ShadowMode_Passes verifies that in shadow mode with
// no findings, the epic proceeds and the artifact records Passed=true.
func TestRunEpicPlanReview_ShadowMode_Passes(t *testing.T) {
	repoRoot := t.TempDir()

	leaseID := "epic-plan-review-run-shadow-pass-epic-root"
	adapter := runtimefake.New(nil, []runtime.ReviewResult{
		epicPlanReviewPassedResult(leaseID),
	})

	cfg := policy.EpicReviewConfig{
		PlanMode:       "shadow",
		PlanMinTickets: 2,
		Threshold:      "P2",
	}

	root, children := makeEpicPlanTickets(4)

	artifact, err := RunEpicPlanReview(
		context.Background(), adapter, repoRoot,
		root, children, cfg, "run-shadow-pass",
	)
	if err != nil {
		t.Fatalf("expected nil error for passing plan review, got: %v", err)
	}
	if artifact == nil {
		t.Fatal("expected non-nil artifact, got nil")
	}
	if !artifact.Passed {
		t.Error("expected Passed=true for review with no findings")
	}
	if len(artifact.Findings) != 0 {
		t.Errorf("expected 0 findings, got %d", len(artifact.Findings))
	}
	if artifact.ReviewScope != "epic_plan" {
		t.Errorf("expected ReviewScope=epic_plan, got %q", artifact.ReviewScope)
	}

	// TicketSummaries should include root + all children.
	expectedCount := 1 + len(children)
	if len(artifact.TicketSummaries) != expectedCount {
		t.Errorf("expected %d ticket summaries (root + children), got %d", expectedCount, len(artifact.TicketSummaries))
	}
	// First summary is the root ticket.
	if artifact.TicketSummaries[0].TicketID != root.ID {
		t.Errorf("expected first summary to be root ticket %q, got %q", root.ID, artifact.TicketSummaries[0].TicketID)
	}
}

// TestRunEpicPlanReview_ExactlyMinTickets verifies that when children count
// equals PlanMinTickets, the review runs (boundary condition).
func TestRunEpicPlanReview_ExactlyMinTickets(t *testing.T) {
	repoRoot := t.TempDir()

	leaseID := "epic-plan-review-run-exact-min-epic-root"
	adapter := runtimefake.New(nil, []runtime.ReviewResult{
		epicPlanReviewPassedResult(leaseID),
	})

	cfg := policy.EpicReviewConfig{
		PlanMode:       "shadow",
		PlanMinTickets: 3,
		Threshold:      "P2",
	}

	root, children := makeEpicPlanTickets(3) // exactly 3

	artifact, err := RunEpicPlanReview(
		context.Background(), adapter, repoRoot,
		root, children, cfg, "run-exact-min",
	)
	if err != nil {
		t.Fatalf("expected nil error at exactly min tickets, got: %v", err)
	}
	if artifact == nil {
		t.Fatal("expected non-nil artifact at exactly min tickets, got nil")
	}

	// Reviewer must have been called (not skipped).
	if calls := adapter.ReviewRequests(); len(calls) != 1 {
		t.Errorf("expected 1 reviewer call at exactly min tickets, got %d", len(calls))
	}
}
