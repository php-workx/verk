package engine

import (
	"encoding/json"
	"testing"
	"time"
	"verk/internal/state"
)

func TestBuildTicketValidationCoverage_DeclaredPassedNoReview(t *testing.T) {
	plan := state.PlanArtifact{
		ArtifactMeta:             state.ArtifactMeta{RunID: "run-1"},
		TicketID:                 "ver-rcgh",
		ValidationCommands:       []string{"go test ./internal/state/..."},
		EffectiveReviewThreshold: state.SeverityP2,
	}
	verification := &state.VerificationArtifact{
		ArtifactMeta: state.ArtifactMeta{RunID: "run-1"},
		TicketID:     "ver-rcgh",
		Attempt:      1,
		Commands:     []string{"go test ./internal/state/..."},
		Results: []state.VerificationResult{{
			Command:  "go test ./internal/state/...",
			ExitCode: 0,
			Passed:   true,
		}},
		Passed: true,
	}

	coverage := BuildTicketValidationCoverage(plan, verification, nil, nil)

	if coverage.Scope != state.ValidationScopeTicket {
		t.Fatalf("expected ticket scope, got %q", coverage.Scope)
	}
	if len(coverage.DeclaredChecks) != 1 {
		t.Fatalf("expected 1 declared check, got %d", len(coverage.DeclaredChecks))
	}
	if len(coverage.ExecutedChecks) != 1 {
		t.Fatalf("expected 1 executed check, got %d", len(coverage.ExecutedChecks))
	}
	if coverage.ExecutedChecks[0].Result != state.ValidationCheckResultPassed {
		t.Fatalf("expected passed result, got %q", coverage.ExecutedChecks[0].Result)
	}
	if !coverage.Closable {
		t.Fatalf("expected closable=true when all checks pass")
	}
}

func TestBuildTicketValidationCoverage_FailedVerificationBlocks(t *testing.T) {
	plan := state.PlanArtifact{
		ArtifactMeta:             state.ArtifactMeta{RunID: "run-1"},
		TicketID:                 "ver-rcgh",
		ValidationCommands:       []string{"go test ./internal/state/..."},
		EffectiveReviewThreshold: state.SeverityP2,
	}
	verification := &state.VerificationArtifact{
		TicketID: "ver-rcgh",
		Results: []state.VerificationResult{{
			Command:  "go test ./internal/state/...",
			ExitCode: 1,
			Passed:   false,
		}},
		Passed: false,
	}

	coverage := BuildTicketValidationCoverage(plan, verification, nil, nil)

	if coverage.Closable {
		t.Fatalf("expected failed verification to block closure")
	}
	if coverage.BlockReason == "" {
		t.Fatalf("expected block reason on failed verification")
	}
	if coverage.ExecutedChecks[0].Result != state.ValidationCheckResultFailed {
		t.Fatalf("expected failed execution, got %q", coverage.ExecutedChecks[0].Result)
	}
}

func TestBuildTicketValidationCoverage_ReviewBlockerSurfacesAsUnresolved(t *testing.T) {
	plan := state.PlanArtifact{
		TicketID:                 "ver-rcgh",
		EffectiveReviewThreshold: state.SeverityP2,
	}
	review := &state.ReviewFindingsArtifact{
		TicketID:                 "ver-rcgh",
		EffectiveReviewThreshold: state.SeverityP2,
		Findings: []state.ReviewFinding{{
			ID:          "finding-1",
			Severity:    state.SeverityP1,
			Title:       "missing error handling",
			Body:        "body",
			File:        "internal/engine/foo.go",
			Line:        10,
			Disposition: "open",
		}},
		Passed: false,
	}

	coverage := BuildTicketValidationCoverage(plan, nil, review, nil)

	if coverage.Closable {
		t.Fatalf("expected blocking review to prevent closure")
	}
	if len(coverage.UnresolvedBlockers) != 1 {
		t.Fatalf("expected 1 unresolved blocker, got %d", len(coverage.UnresolvedBlockers))
	}
	if coverage.UnresolvedBlockers[0].FindingID != "finding-1" {
		t.Fatalf("expected blocker to reference finding-1, got %q", coverage.UnresolvedBlockers[0].FindingID)
	}
	if coverage.UnresolvedBlockers[0].Scope != state.ValidationScopeTicket {
		t.Fatalf("expected ticket-scoped blocker")
	}
}

func TestBuildTicketValidationCoverage_RepairCycleAppendsRefs(t *testing.T) {
	plan := state.PlanArtifact{
		TicketID:                 "ver-rcgh",
		EffectiveReviewThreshold: state.SeverityP2,
	}
	cycles := []state.RepairCycleArtifact{{
		TicketID:          "ver-rcgh",
		Cycle:             1,
		Scope:             state.ValidationScopeTicket,
		Status:            "completed",
		TriggerFindingIDs: []string{"finding-1"},
		TriggerCheckIDs:   []string{"check-ver-rcgh-abcd"},
		StartedAt:         time.Now().UTC(),
		FinishedAt:        time.Now().UTC(),
	}}

	coverage := BuildTicketValidationCoverage(plan, nil, nil, cycles)

	if len(coverage.RepairRefs) != 2 {
		t.Fatalf("expected one ref per trigger id (2 total), got %d", len(coverage.RepairRefs))
	}
	for _, ref := range coverage.RepairRefs {
		if ref.Result != state.ValidationCheckResultRepaired {
			t.Fatalf("expected repaired result on completed cycle, got %q", ref.Result)
		}
		if ref.Scope != state.ValidationScopeTicket {
			t.Fatalf("expected ticket-scoped ref, got %q", ref.Scope)
		}
	}
}

func TestBuildTicketValidationCoverage_SerializesCompactly(t *testing.T) {
	plan := state.PlanArtifact{TicketID: "ver-rcgh"}
	coverage := BuildTicketValidationCoverage(plan, nil, nil, nil)

	data, err := json.Marshal(coverage)
	if err != nil {
		t.Fatalf("marshal coverage: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("expected non-empty serialization")
	}

	var round state.ValidationCoverageArtifact
	if err := json.Unmarshal(data, &round); err != nil {
		t.Fatalf("unmarshal coverage: %v", err)
	}
	if round.TicketID != "ver-rcgh" {
		t.Fatalf("expected ticket id to round-trip")
	}
	if round.Scope != state.ValidationScopeTicket {
		t.Fatalf("expected ticket scope")
	}
}
