package state

import (
	"encoding/json"
	"testing"
	"time"
)

func TestTicketQualityArtifact_PassedRoundTrip(t *testing.T) {
	artifact := TicketQualityArtifact{
		ArtifactMeta: ArtifactMeta{
			SchemaVersion: 1,
			RunID:         "run-1",
			CreatedAt:     time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
			UpdatedAt:     time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
		},
		Scope:        "ticket",
		RootTicketID: "ticket-1",
		TicketIDs:    []string{"ticket-1"},
		Status:       TicketQualityPassed,
		Findings:     []TicketQualityFinding{},
		Blocked:      false,
	}

	data, err := json.Marshal(artifact)
	if err != nil {
		t.Fatalf("marshal passed artifact: %v", err)
	}

	var got TicketQualityArtifact
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal passed artifact: %v", err)
	}

	if got.Status != TicketQualityPassed {
		t.Fatalf("expected status %q, got %q", TicketQualityPassed, got.Status)
	}
	if got.Blocked {
		t.Fatalf("expected blocked=false for passed artifact")
	}
	if len(got.Findings) != 0 {
		t.Fatalf("expected no findings, got %d", len(got.Findings))
	}
	if got.RootTicketID != "ticket-1" {
		t.Fatalf("expected root_ticket_id %q, got %q", "ticket-1", got.RootTicketID)
	}
}

func TestTicketQualityArtifact_BlockedWithFinding(t *testing.T) {
	artifact := TicketQualityArtifact{
		ArtifactMeta: ArtifactMeta{
			SchemaVersion: 1,
			RunID:         "run-2",
			CreatedAt:     time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
			UpdatedAt:     time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
		},
		Scope:        "ticket",
		RootTicketID: "ticket-2",
		TicketIDs:    []string{"ticket-2"},
		Status:       TicketQualityBlocked,
		Findings: []TicketQualityFinding{
			{
				ID:             "finding-1",
				TicketID:       "ticket-2",
				Code:           string(QualityCodeMissingAcceptanceCriteria),
				Severity:       SeverityP1,
				Title:          "Missing acceptance criteria",
				Body:           "The ticket has no acceptance criteria defined.",
				Evidence:       []string{"no AC section found"},
				Repairable:     true,
				AutoRepairable: false,
				Disposition:    "open",
			},
		},
		Blocked:     true,
		BlockReason: "blocking findings present",
	}

	data, err := json.Marshal(artifact)
	if err != nil {
		t.Fatalf("marshal blocked artifact: %v", err)
	}

	var got TicketQualityArtifact
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal blocked artifact: %v", err)
	}

	if got.Status != TicketQualityBlocked {
		t.Fatalf("expected status %q, got %q", TicketQualityBlocked, got.Status)
	}
	if !got.Blocked {
		t.Fatalf("expected blocked=true")
	}
	if got.BlockReason == "" {
		t.Fatalf("expected non-empty block_reason")
	}
	if len(got.Findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(got.Findings))
	}

	f := got.Findings[0]
	if f.ID != "finding-1" {
		t.Fatalf("expected finding id %q, got %q", "finding-1", f.ID)
	}
	if f.Code != string(QualityCodeMissingAcceptanceCriteria) {
		t.Fatalf("expected code %q, got %q", QualityCodeMissingAcceptanceCriteria, f.Code)
	}
	if f.Severity != SeverityP1 {
		t.Fatalf("expected severity %q, got %q", SeverityP1, f.Severity)
	}
}

func TestTicketQualityArtifact_RepairedWithRepair(t *testing.T) {
	artifact := TicketQualityArtifact{
		ArtifactMeta: ArtifactMeta{
			SchemaVersion: 1,
			RunID:         "run-3",
			CreatedAt:     time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
			UpdatedAt:     time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
		},
		Scope:        "ticket",
		RootTicketID: "ticket-3",
		TicketIDs:    []string{"ticket-3"},
		Status:       TicketQualityRepaired,
		Findings: []TicketQualityFinding{
			{
				ID:             "finding-2",
				TicketID:       "ticket-3",
				Code:           string(QualityCodeMissingValidationCommands),
				Severity:       SeverityP2,
				Title:          "Missing validation commands",
				Body:           "No validation commands specified.",
				Repairable:     true,
				AutoRepairable: true,
				Disposition:    "repaired",
			},
		},
		Repairs: []TicketQualityRepair{
			{
				FindingID: "finding-2",
				TicketID:  "ticket-3",
				Kind:      "add_validation_commands",
				Summary:   "Added default go test validation command",
				Applied:   true,
			},
		},
		Blocked: false,
	}

	data, err := json.Marshal(artifact)
	if err != nil {
		t.Fatalf("marshal repaired artifact: %v", err)
	}

	var got TicketQualityArtifact
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal repaired artifact: %v", err)
	}

	if got.Status != TicketQualityRepaired {
		t.Fatalf("expected status %q, got %q", TicketQualityRepaired, got.Status)
	}
	if got.Blocked {
		t.Fatalf("expected blocked=false for repaired artifact")
	}
	if len(got.Repairs) != 1 {
		t.Fatalf("expected 1 repair, got %d", len(got.Repairs))
	}

	r := got.Repairs[0]
	if r.FindingID != "finding-2" {
		t.Fatalf("expected repair finding_id %q, got %q", "finding-2", r.FindingID)
	}
	if !r.Applied {
		t.Fatalf("expected repair applied=true")
	}
}

func TestTicketQualityStatus_Values(t *testing.T) {
	cases := []struct {
		status TicketQualityStatus
		want   string
	}{
		{TicketQualityPassed, "passed"},
		{TicketQualityRepaired, "repaired"},
		{TicketQualityBlocked, "blocked"},
	}
	for _, tc := range cases {
		if string(tc.status) != tc.want {
			t.Fatalf("expected %q, got %q", tc.want, tc.status)
		}
	}
}

func TestTicketQualityCode_Values(t *testing.T) {
	codes := []TicketQualityCode{
		QualityCodeMissingAcceptanceCriteria,
		QualityCodeAmbiguousAcceptanceCriterion,
		QualityCodeCompoundAcceptanceCriterion,
		QualityCodeMissingValidationCommands,
		QualityCodeMissingOwnedPaths,
		QualityCodeOwnedPathMissing,
		QualityCodeDependencyMissing,
		QualityCodeDependencyBlockedOrClosedMismatch,
		QualityCodeMissingPublicContractScenario,
		QualityCodeMissingNegativeCase,
		QualityCodeDocsDescopeRisk,
		QualityCodeIntegrationGap,
		QualityCodePlanTraceabilityGap,
		QualityCodeReviewerInstructionGap,
	}
	for _, c := range codes {
		if string(c) == "" {
			t.Fatalf("quality code must not be empty string")
		}
	}
}
