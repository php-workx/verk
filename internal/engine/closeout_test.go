package engine

import (
	"strings"
	"testing"
	"time"

	"verk/internal/adapters/ticketstore/tkmd"
	"verk/internal/state"
)

func TestBuildCloseoutArtifact_FailsOnCrossRunEvidence(t *testing.T) {
	req := baseCloseoutRequest()
	req.criteriaEvidence = []state.CriteriaEvidence{
		{
			CriterionID:   "criterion-1",
			CriterionText: "Criterion one",
			EvidenceType:  "verification",
			Source:        "verification.json",
			Summary:       "passed",
			RunID:         "run-b",
			TicketID:      req.ticket.ID,
			Attempt:       1,
			ArtifactRef:   "verification.json",
		},
	}

	if _, err := BuildCloseoutArtifact(req.ticket, req.plan, req.verification, req.review, req.criteriaEvidence); err == nil || !strings.Contains(err.Error(), "does not match current run") {
		t.Fatalf("expected cross-run evidence to fail, got: %v", err)
	}
}

func TestBuildCloseoutArtifact_FailsOnMissingWaiverMetadata(t *testing.T) {
	req := baseCloseoutRequest()
	req.review = &state.ReviewFindingsArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: req.plan.RunID},
		TicketID:     req.ticket.ID,
		Attempt:      1,
		Findings: []state.ReviewFinding{
			{
				ID:          "finding-1",
				Severity:    state.SeverityP1,
				Title:       "waived finding",
				Body:        "waived finding",
				File:        "internal/engine/closeout.go",
				Line:        10,
				Disposition: "waived",
			},
		},
		Passed:                   true,
		EffectiveReviewThreshold: state.SeverityP2,
	}

	if _, err := BuildCloseoutArtifact(req.ticket, req.plan, req.verification, req.review); err == nil || !strings.Contains(err.Error(), "waived review finding") {
		t.Fatalf("expected missing waiver metadata to fail, got: %v", err)
	}
}

func TestBuildCloseoutArtifact_GateResultsAreTyped(t *testing.T) {
	req := baseCloseoutRequest()

	artifact, err := BuildCloseoutArtifact(req.ticket, req.plan, req.verification, req.review)
	if err != nil {
		t.Fatalf("BuildCloseoutArtifact returned error: %v", err)
	}

	if artifact.FailedGate != "" {
		t.Fatalf("expected closable closeout, got failed gate %q", artifact.FailedGate)
	}
	if !artifact.Closable {
		t.Fatal("expected closeout to be closable")
	}
	if len(artifact.GateResults) == 0 {
		t.Fatal("expected gate results to be populated")
	}

	for _, gate := range []string{gateCriteriaEvidence, gateVerification, gateRequiredArtifacts, gateReview, gateDeclaredChecks, gateArtifactIntegrity} {
		result, ok := artifact.GateResults[gate]
		if !ok {
			t.Fatalf("missing gate result %q", gate)
		}
		if result.Status != gatePassed {
			t.Fatalf("expected gate %q to pass, got %q", gate, result.Status)
		}
		if result.Reason == "" {
			t.Fatalf("expected gate %q to include a reason", gate)
		}
	}
}

func TestReviewFindingBlocks(t *testing.T) {
	now := fixedTime()
	cases := []struct {
		name      string
		finding   state.ReviewFinding
		threshold state.Severity
		want      bool
	}{
		{
			name: "open at threshold blocks",
			finding: state.ReviewFinding{
				ID:          "f-1",
				Severity:    state.SeverityP1,
				Title:       "open",
				Body:        "open",
				File:        "internal/engine/closeout.go",
				Line:        10,
				Disposition: "open",
			},
			threshold: state.SeverityP1,
			want:      true,
		},
		{
			name: "resolved does not block",
			finding: state.ReviewFinding{
				ID:          "f-2",
				Severity:    state.SeverityP1,
				Title:       "resolved",
				Body:        "resolved",
				File:        "internal/engine/closeout.go",
				Line:        10,
				Disposition: "resolved",
			},
			threshold: state.SeverityP1,
			want:      false,
		},
		{
			name: "waived does not block when metadata present",
			finding: state.ReviewFinding{
				ID:              "f-3",
				Severity:        state.SeverityP1,
				Title:           "waived",
				Body:            "waived",
				File:            "internal/engine/closeout.go",
				Line:            10,
				Disposition:     "waived",
				WaivedBy:        "reviewer",
				WaivedAt:        now,
				WaiverReason:    "accepted risk",
				WaiverExpiresAt: now.Add(24 * time.Hour),
			},
			threshold: state.SeverityP1,
			want:      false,
		},
	}

	for _, tc := range cases {
		if got := ReviewFindingBlocks(tc.finding, tc.threshold); got != tc.want {
			t.Fatalf("%s: expected %v, got %v", tc.name, tc.want, got)
		}
	}
}

func baseCloseoutRequest() closeoutRequest {
	now := fixedTime()
	ticket := tkmd.Ticket{
		ID:    "ver-closable",
		Title: "Closable ticket",
		UnknownFrontmatter: map[string]any{
			"type": "task",
		},
	}
	plan := state.PlanArtifact{
		ArtifactMeta: state.ArtifactMeta{
			SchemaVersion: 1,
			RunID:         "run-a",
		},
		TicketID:                 ticket.ID,
		Phase:                    state.TicketPhaseIntake,
		AcceptanceCriteria:       []string{"Criterion one"},
		EffectiveReviewThreshold: state.SeverityP2,
		ReviewThreshold:          state.SeverityP2,
	}
	verification := &state.VerificationArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: "run-a"},
		TicketID:     ticket.ID,
		Attempt:      1,
		Commands:     []string{"go test ./..."},
		Results: []state.VerificationResult{
			{
				Command:    "go test ./...",
				ExitCode:   0,
				Passed:     true,
				StartedAt:  now,
				FinishedAt: now.Add(2 * time.Minute),
			},
		},
		Passed:     true,
		RepoRoot:   "/repo",
		StartedAt:  now,
		FinishedAt: now.Add(2 * time.Minute),
	}
	review := &state.ReviewFindingsArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: "run-a"},
		TicketID:     ticket.ID,
		Attempt:      1,
		Summary:      "clean",
		Findings: []state.ReviewFinding{
			{
				ID:          "finding-1",
				Severity:    state.SeverityP3,
				Title:       "informational",
				Body:        "informational",
				File:        "internal/engine/closeout.go",
				Line:        10,
				Disposition: "resolved",
			},
		},
		BlockingFindings:         []string{},
		Passed:                   true,
		EffectiveReviewThreshold: state.SeverityP2,
	}

	return closeoutRequest{
		ticket:            ticket,
		plan:              plan,
		verification:      verification,
		review:            review,
		requiredArtifacts: []string{"verification.json", "review-findings.json"},
	}
}

func fixedTime() time.Time {
	return time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC)
}
