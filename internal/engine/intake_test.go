package engine

import (
	"testing"

	"verk/internal/adapters/ticketstore/tkmd"
	"verk/internal/policy"
	"verk/internal/state"
)

func TestBuildPlanArtifact_PersistsEffectiveReviewThreshold(t *testing.T) {
	cfg := policy.DefaultConfig()
	ticket := tkmd.Ticket{
		ID:                 "ver-123",
		Title:              "Implement intake",
		AcceptanceCriteria: []string{"Criterion one"},
		TestCases:          []string{"go test ./..."},
		ValidationCommands: []string{"go test ./internal/engine"},
		OwnedPaths:         []string{"internal/engine"},
		ReviewThreshold:    string(state.SeverityP3),
		Runtime:            "codex",
		UnknownFrontmatter: map[string]any{
			"type": "task",
		},
	}

	artifact, err := BuildPlanArtifact(ticket, cfg)
	if err != nil {
		t.Fatalf("BuildPlanArtifact returned error: %v", err)
	}

	if artifact.EffectiveReviewThreshold != state.SeverityP3 {
		t.Fatalf("expected effective threshold P3, got %q", artifact.EffectiveReviewThreshold)
	}
	if artifact.ReviewThreshold != state.SeverityP3 {
		t.Fatalf("expected persisted review threshold P3, got %q", artifact.ReviewThreshold)
	}
	if got := artifact.OwnedPaths; len(got) != 1 || got[0] != "internal/engine" {
		t.Fatalf("expected owned paths to round-trip, got %#v", got)
	}
	if artifact.Phase != state.TicketPhaseIntake {
		t.Fatalf("expected intake phase, got %q", artifact.Phase)
	}
}

func TestBuildPlanArtifact_RejectsMissingOwnedPathsForEpic(t *testing.T) {
	cfg := policy.DefaultConfig()
	ticket := tkmd.Ticket{
		ID:    "ver-epic",
		Title: "Epic ticket",
		UnknownFrontmatter: map[string]any{
			"type": "epic",
		},
	}

	if _, err := BuildPlanArtifact(ticket, cfg); err == nil {
		t.Fatal("expected epic without owned_paths to fail")
	}
}
