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

// waveReviewTestTime is a stable timestamp for wave review test fixtures.
var waveReviewTestTime = time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)

// waveReviewPassedResult builds a minimal valid passing ReviewResult for wave review.
func waveReviewPassedResult(leaseID string) runtime.ReviewResult {
	return runtime.ReviewResult{
		Status:             runtime.WorkerStatusDone,
		RetryClass:         runtime.RetryClassTerminal,
		LeaseID:            leaseID,
		StartedAt:          waveReviewTestTime,
		FinishedAt:         waveReviewTestTime.Add(time.Second),
		ReviewStatus:       runtime.ReviewStatusPassed,
		Summary:            "no cross-ticket issues found",
		Findings:           nil,
		ResultArtifactPath: "review-artifact.json",
	}
}

// waveReviewP2FindingResult builds a valid ReviewResult with one open P2 finding.
func waveReviewP2FindingResult(leaseID string) runtime.ReviewResult {
	return runtime.ReviewResult{
		Status:             runtime.WorkerStatusDone,
		RetryClass:         runtime.RetryClassTerminal,
		LeaseID:            leaseID,
		StartedAt:          waveReviewTestTime,
		FinishedAt:         waveReviewTestTime.Add(time.Second),
		ReviewStatus:       runtime.ReviewStatusFindings,
		Summary:            "cross-ticket fanout gap detected",
		ResultArtifactPath: "review-artifact.json",
		Findings: []runtime.ReviewFinding{
			{
				ID:          "wave-finding-1",
				Severity:    runtime.SeverityP2,
				Title:       "incomplete fanout: rename not applied in all files",
				Body:        "The identifier renamed in ticket A was not updated in the files owned by ticket B.",
				File:        "internal/worker/handler.go",
				Line:        55,
				Disposition: runtime.ReviewDispositionOpen,
			},
		},
	}
}

// makeWaveTickets builds a minimal slice of wave tickets.
func makeWaveTickets() []epos.Ticket {
	return []epos.Ticket{
		{
			ID:         "ticket-a",
			Title:      "Ticket A",
			Status:     epos.StatusClosed,
			OwnedPaths: []string{"internal/api"},
		},
		{
			ID:         "ticket-b",
			Title:      "Ticket B",
			Status:     epos.StatusClosed,
			OwnedPaths: []string{"internal/worker"},
		},
	}
}

// TestRunWaveReview_SkipSingleTicket verifies that when SkipSingleTicket=true
// and the wave has exactly one ticket, RunWaveReview returns nil, nil.
func TestRunWaveReview_SkipSingleTicket(t *testing.T) {
	repoRoot := t.TempDir()

	// Even if the adapter would block, the skip condition fires first.
	adapter := runtimefake.New(nil, nil)

	cfg := policy.WaveReviewConfig{
		Mode:             "enforce",
		Threshold:        "P2",
		SkipSingleTicket: true,
	}

	singleTicket := []epos.Ticket{
		{ID: "ticket-only", Title: "Only Ticket", OwnedPaths: []string{"internal/api"}},
	}

	artifact, err := RunWaveReview(
		context.Background(), adapter, repoRoot,
		"wave-1", 1, singleTicket, "abc123", cfg, "run-skip-single",
	)
	if err != nil {
		t.Fatalf("expected nil error for single-ticket skip, got: %v", err)
	}
	if artifact != nil {
		t.Errorf("expected nil artifact for single-ticket skip, got: %+v", artifact)
	}

	// No reviewer call should have been made.
	if calls := adapter.ReviewRequests(); len(calls) != 0 {
		t.Errorf("expected 0 reviewer calls for single-ticket skip, got %d", len(calls))
	}
}

// TestRunWaveReview_DisabledMode verifies that when Mode="disabled",
// RunWaveReview returns nil, nil without calling the reviewer.
func TestRunWaveReview_DisabledMode(t *testing.T) {
	repoRoot := t.TempDir()
	adapter := runtimefake.New(nil, nil)

	cfg := policy.WaveReviewConfig{
		Mode:             "disabled",
		Threshold:        "P2",
		SkipSingleTicket: true,
	}

	artifact, err := RunWaveReview(
		context.Background(), adapter, repoRoot,
		"wave-1", 1, makeWaveTickets(), "abc123", cfg, "run-disabled",
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

// TestRunWaveReview_ShadowMode_P2Finding verifies that in shadow mode,
// an open P2 finding is recorded in the artifact but the wave is NOT blocked
// (no error is returned).
func TestRunWaveReview_ShadowMode_P2Finding(t *testing.T) {
	repoRoot := t.TempDir()

	leaseID := "wave-review-run-shadow-wave-1"
	adapter := runtimefake.New(nil, []runtime.ReviewResult{
		waveReviewP2FindingResult(leaseID),
	})

	cfg := policy.WaveReviewConfig{
		Mode:             "shadow",
		Threshold:        "P2",
		SkipSingleTicket: false,
	}

	artifact, err := RunWaveReview(
		context.Background(), adapter, repoRoot,
		"wave-1", 1, makeWaveTickets(), "abc123", cfg, "run-shadow",
	)
	if err != nil {
		t.Fatalf("shadow mode: expected nil error even with P2 finding, got: %v", err)
	}
	if artifact == nil {
		t.Fatalf("shadow mode: expected non-nil artifact, got nil")
	}

	// Artifact should record the finding.
	if len(artifact.Findings) != 1 {
		t.Errorf("shadow mode: expected 1 finding in artifact, got %d", len(artifact.Findings))
	}
	if len(artifact.BlockingFindings) != 1 {
		t.Errorf("shadow mode: expected 1 blocking finding ID, got %d", len(artifact.BlockingFindings))
	}
	// Passed should be false because there is an open P2 finding.
	if artifact.Passed {
		t.Error("shadow mode: expected Passed=false with open P2 finding")
	}
	if artifact.Mode != "shadow" {
		t.Errorf("shadow mode: expected Mode=shadow, got %q", artifact.Mode)
	}

	// Artifact must be persisted on disk.
	var loaded state.WaveReviewArtifact
	artifactPath := waveReviewArtifactPath(repoRoot, "run-shadow", "wave-1")
	if err := state.LoadJSON(artifactPath, &loaded); err != nil {
		t.Fatalf("shadow mode: load persisted artifact: %v", err)
	}
	if len(loaded.Findings) != 1 {
		t.Errorf("shadow mode: persisted artifact: expected 1 finding, got %d", len(loaded.Findings))
	}
}

// TestRunWaveReview_EnforceMode_P2FindingBlocksWave verifies that in enforce
// mode, an open P2 finding causes RunWaveReview to return a non-nil error
// containing "wave_review_failed".
func TestRunWaveReview_EnforceMode_P2FindingBlocksWave(t *testing.T) {
	repoRoot := t.TempDir()

	leaseID := "wave-review-run-enforce-wave-1"
	adapter := runtimefake.New(nil, []runtime.ReviewResult{
		waveReviewP2FindingResult(leaseID),
	})

	cfg := policy.WaveReviewConfig{
		Mode:             "enforce",
		Threshold:        "P2",
		SkipSingleTicket: false,
	}

	artifact, err := RunWaveReview(
		context.Background(), adapter, repoRoot,
		"wave-1", 1, makeWaveTickets(), "abc123", cfg, "run-enforce",
	)

	if err == nil {
		t.Fatal("enforce mode: expected non-nil error for open P2 finding, got nil")
	}
	if !strings.Contains(err.Error(), "wave_review_failed") {
		t.Errorf("enforce mode: expected error to contain 'wave_review_failed', got: %v", err)
	}
	if artifact == nil {
		t.Fatal("enforce mode: expected non-nil artifact even on block, got nil")
	}
	if artifact.Passed {
		t.Error("enforce mode: expected Passed=false with open P2 finding")
	}

	// Artifact must still be persisted on disk despite the error.
	var loaded state.WaveReviewArtifact
	artifactPath := waveReviewArtifactPath(repoRoot, "run-enforce", "wave-1")
	if err := state.LoadJSON(artifactPath, &loaded); err != nil {
		t.Fatalf("enforce mode: load persisted artifact after block: %v", err)
	}
	if loaded.Mode != "enforce" {
		t.Errorf("enforce mode: persisted artifact Mode=%q, want %q", loaded.Mode, "enforce")
	}
}

// TestRunWaveReview_ShadowMode_Passes verifies that in shadow mode with no
// findings, the wave proceeds and the artifact is persisted with Passed=true.
func TestRunWaveReview_ShadowMode_Passes(t *testing.T) {
	repoRoot := t.TempDir()

	leaseID := "wave-review-run-shadow-pass-wave-2"
	adapter := runtimefake.New(nil, []runtime.ReviewResult{
		waveReviewPassedResult(leaseID),
	})

	cfg := policy.WaveReviewConfig{
		Mode:             "shadow",
		Threshold:        "P2",
		SkipSingleTicket: true,
	}

	tickets := makeWaveTickets()
	artifact, err := RunWaveReview(
		context.Background(), adapter, repoRoot,
		"wave-2", 2, tickets, "def456", cfg, "run-shadow-pass",
	)
	if err != nil {
		t.Fatalf("expected nil error for passing review, got: %v", err)
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
	if artifact.WaveID != "wave-2" {
		t.Errorf("expected WaveID=wave-2, got %q", artifact.WaveID)
	}
	if artifact.Ordinal != 2 {
		t.Errorf("expected Ordinal=2, got %d", artifact.Ordinal)
	}
	if artifact.ReviewScope != "wave" {
		t.Errorf("expected ReviewScope=wave, got %q", artifact.ReviewScope)
	}
	if artifact.BaseCommit != "def456" {
		t.Errorf("expected BaseCommit=def456, got %q", artifact.BaseCommit)
	}

	// Union of owned_paths should contain both ticket paths.
	expectedPaths := map[string]bool{"internal/api": false, "internal/worker": false}
	for _, p := range artifact.ScopeUnion {
		expectedPaths[p] = true
	}
	for p, found := range expectedPaths {
		if !found {
			t.Errorf("expected ScopeUnion to contain %q", p)
		}
	}
}

// TestRunWaveReview_EmptyTickets verifies that an empty ticket list skips
// the review (returns nil, nil).
func TestRunWaveReview_EmptyTickets(t *testing.T) {
	repoRoot := t.TempDir()
	adapter := runtimefake.New(nil, nil)

	cfg := policy.WaveReviewConfig{
		Mode:             "enforce",
		Threshold:        "P2",
		SkipSingleTicket: false,
	}

	artifact, err := RunWaveReview(
		context.Background(), adapter, repoRoot,
		"wave-1", 1, nil, "abc123", cfg, "run-empty",
	)
	if err != nil {
		t.Fatalf("expected nil error for empty tickets, got: %v", err)
	}
	if artifact != nil {
		t.Errorf("expected nil artifact for empty tickets, got: %+v", artifact)
	}
}
