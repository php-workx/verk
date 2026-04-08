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

func TestDeriveCriteriaEvidence_DiffFromChangedFiles(t *testing.T) {
	req := baseCloseoutRequest()
	req.implementation = &state.ImplementationArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: req.plan.RunID},
		TicketID:     req.ticket.ID,
		Attempt:      1,
		Status:       "done",
		ChangedFiles: []string{"internal/engine/closeout.go", "internal/engine/closeout_test.go"},
	}

	evidence := deriveCriteriaEvidence(req)
	if len(evidence) == 0 {
		t.Fatal("expected evidence to be generated")
	}

	var diffEvidence []state.CriteriaEvidence
	for _, e := range evidence {
		if e.EvidenceType == "diff" {
			diffEvidence = append(diffEvidence, e)
		}
	}
	if len(diffEvidence) == 0 {
		t.Fatal("expected diff evidence to be generated from changed files")
	}
	for _, e := range diffEvidence {
		if e.Source != defaultDiffSource {
			t.Fatalf("expected diff evidence source %q, got %q", defaultDiffSource, e.Source)
		}
		if !strings.Contains(e.Summary, "2 files changed") {
			t.Fatalf("expected diff summary to mention 2 files, got %q", e.Summary)
		}
		if e.RunID != req.plan.RunID {
			t.Fatalf("expected run_id %q, got %q", req.plan.RunID, e.RunID)
		}
		if e.TicketID != req.ticket.ID {
			t.Fatalf("expected ticket_id %q, got %q", req.ticket.ID, e.TicketID)
		}
	}
}

func TestDeriveCriteriaEvidence_ArtifactFromResultArtifacts(t *testing.T) {
	req := baseCloseoutRequest()
	req.implementation = &state.ImplementationArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: req.plan.RunID},
		TicketID:     req.ticket.ID,
		Attempt:      1,
		Status:       "done",
		Artifacts:    []string{"output/report.json"},
	}

	evidence := deriveCriteriaEvidence(req)
	if len(evidence) == 0 {
		t.Fatal("expected evidence to be generated")
	}

	var artifactEvidence []state.CriteriaEvidence
	for _, e := range evidence {
		if e.EvidenceType == "artifact" && e.Source == defaultResultArtifactSource {
			artifactEvidence = append(artifactEvidence, e)
		}
	}
	if len(artifactEvidence) == 0 {
		t.Fatal("expected artifact evidence to be generated from result artifacts")
	}
	for _, e := range artifactEvidence {
		if e.Source != defaultResultArtifactSource {
			t.Fatalf("expected artifact evidence source %q, got %q", defaultResultArtifactSource, e.Source)
		}
		if !strings.Contains(e.Summary, "report.json") {
			t.Fatalf("expected artifact summary to mention result artifact, got %q", e.Summary)
		}
	}
}

func TestDeriveCriteriaEvidence_DiffAndArtifactSupplementVerification(t *testing.T) {
	req := baseCloseoutRequest()
	req.implementation = &state.ImplementationArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: req.plan.RunID},
		TicketID:     req.ticket.ID,
		Attempt:      1,
		Status:       "done",
		ChangedFiles: []string{"main.go"},
		Artifacts:    []string{"build/output.bin"},
	}

	evidence := deriveCriteriaEvidence(req)

	types := map[string]int{}
	for _, e := range evidence {
		types[e.EvidenceType]++
	}

	// Verification evidence should still be present (primary).
	if types["verification"] == 0 {
		t.Fatal("expected verification evidence to still be present")
	}
	// Diff and artifact evidence should supplement it.
	if types["diff"] == 0 {
		t.Fatal("expected diff evidence to supplement verification")
	}
	if types["artifact"] == 0 {
		t.Fatal("expected artifact evidence to supplement verification")
	}
}

func TestDeriveCriteriaEvidence_NoImplementationNoExtraEvidence(t *testing.T) {
	req := baseCloseoutRequest()
	// No implementation artifact set.
	evidence := deriveCriteriaEvidence(req)

	for _, e := range evidence {
		if e.EvidenceType == "diff" {
			t.Fatal("unexpected diff evidence without implementation artifact")
		}
		if e.EvidenceType == "artifact" && e.Source == defaultResultArtifactSource {
			t.Fatal("unexpected result artifact evidence without implementation artifact")
		}
	}
}

func TestDeriveCriteriaEvidence_EmptyChangedFilesNoDiff(t *testing.T) {
	req := baseCloseoutRequest()
	req.implementation = &state.ImplementationArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: req.plan.RunID},
		TicketID:     req.ticket.ID,
		Attempt:      1,
		Status:       "done",
		ChangedFiles: []string{},
		Artifacts:    []string{},
	}

	evidence := deriveCriteriaEvidence(req)
	for _, e := range evidence {
		if e.EvidenceType == "diff" {
			t.Fatal("unexpected diff evidence with empty changed files")
		}
		if e.EvidenceType == "artifact" && e.Source == defaultResultArtifactSource {
			t.Fatal("unexpected result artifact evidence with empty artifacts")
		}
	}
}

func TestValidateCriteriaEvidenceEntry_AcceptsAllFourTypes(t *testing.T) {
	for _, evidenceType := range []string{"verification", "artifact", "diff", "review"} {
		entry := state.CriteriaEvidence{
			CriterionID:   "criterion-1",
			CriterionText: "Criterion one",
			EvidenceType:  evidenceType,
			Source:        "source.json",
			Summary:       "summary",
			RunID:         "run-a",
			TicketID:      "ver-test",
			Attempt:       1,
			ArtifactRef:   "source.json#1",
		}
		if err := validateCriteriaEvidenceEntry(entry, "run-a", "ver-test"); err != nil {
			t.Fatalf("expected evidence_type %q to be accepted, got error: %v", evidenceType, err)
		}
	}
}

func TestValidateCriteriaEvidenceEntry_RejectsUnknownType(t *testing.T) {
	entry := state.CriteriaEvidence{
		CriterionID:   "criterion-1",
		CriterionText: "Criterion one",
		EvidenceType:  "unknown",
		Source:        "source.json",
		Summary:       "summary",
		RunID:         "run-a",
		TicketID:      "ver-test",
		Attempt:       1,
		ArtifactRef:   "source.json#1",
	}
	if err := validateCriteriaEvidenceEntry(entry, "run-a", "ver-test"); err == nil || !strings.Contains(err.Error(), "unsupported evidence_type") {
		t.Fatalf("expected unsupported evidence_type error, got: %v", err)
	}
}

func TestBuildCloseoutArtifact_WithImplementationArtifact(t *testing.T) {
	req := baseCloseoutRequest()
	impl := &state.ImplementationArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: req.plan.RunID},
		TicketID:     req.ticket.ID,
		Attempt:      1,
		Status:       "done",
		ChangedFiles: []string{"internal/engine/closeout.go"},
		Artifacts:    []string{"output/result.json"},
	}

	artifact, err := BuildCloseoutArtifact(req.ticket, req.plan, req.verification, req.review, impl)
	if err != nil {
		t.Fatalf("BuildCloseoutArtifact returned error: %v", err)
	}

	if !artifact.Closable {
		t.Fatalf("expected closeout to be closable, failed gate: %s", artifact.FailedGate)
	}

	types := map[string]int{}
	for _, e := range artifact.CriteriaEvidence {
		types[e.EvidenceType]++
	}
	if types["verification"] == 0 {
		t.Fatal("expected verification evidence")
	}
	if types["diff"] == 0 {
		t.Fatal("expected diff evidence from implementation changed files")
	}
	if types["artifact"] == 0 {
		t.Fatal("expected artifact evidence from implementation artifacts")
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
