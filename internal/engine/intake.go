package engine

import (
	"fmt"
	"strings"
	"time"

	"verk/internal/adapters/ticketstore/tkmd"
	"verk/internal/policy"
	"verk/internal/state"
)

const artifactSchemaVersion = 1

func BuildPlanArtifact(t tkmd.Ticket, cfg policy.Config) (state.PlanArtifact, error) {
	if isEpicTicket(t) && len(t.OwnedPaths) == 0 {
		return state.PlanArtifact{}, fmt.Errorf("epic %q requires owned_paths", t.ID)
	}

	plannedThreshold, err := parseTicketThreshold(t.ReviewThreshold, cfg.Policy.ReviewThreshold)
	if err != nil {
		return state.PlanArtifact{}, err
	}

	effective := cfg.EffectiveReviewThreshold(nil, &plannedThreshold)

	return state.PlanArtifact{
		ArtifactMeta: state.ArtifactMeta{
			SchemaVersion: artifactSchemaVersion,
			RunID:         ticketRunID(t),
			CreatedAt:     time.Time{},
			UpdatedAt:     time.Time{},
		},
		TicketID:                 t.ID,
		Phase:                    state.TicketPhaseIntake,
		AcceptanceCriteria:       append([]string(nil), t.AcceptanceCriteria...),
		TestCases:                append([]string(nil), t.TestCases...),
		ValidationCommands:       append([]string(nil), t.ValidationCommands...),
		OwnedPaths:               append([]string(nil), t.OwnedPaths...),
		ReviewThreshold:          plannedThreshold,
		EffectiveReviewThreshold: effective,
		RuntimePreference:        t.Runtime,
	}, nil
}

func parseTicketThreshold(raw string, fallback state.Severity) (state.Severity, error) {
	if strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	threshold := state.Severity(strings.TrimSpace(raw))
	if err := validateSeverity(threshold); err != nil {
		return "", fmt.Errorf("invalid ticket review_threshold: %w", err)
	}
	return threshold, nil
}

func ticketRunID(t tkmd.Ticket) string {
	if t.UnknownFrontmatter == nil {
		return ""
	}
	if runID, ok := t.UnknownFrontmatter["run_id"].(string); ok {
		return strings.TrimSpace(runID)
	}
	return ""
}

func isEpicTicket(t tkmd.Ticket) bool {
	if t.UnknownFrontmatter == nil {
		return false
	}
	typeValue, _ := t.UnknownFrontmatter["type"].(string)
	return strings.EqualFold(strings.TrimSpace(typeValue), "epic")
}

func validateSeverity(severity state.Severity) error {
	switch severity {
	case state.SeverityP0, state.SeverityP1, state.SeverityP2, state.SeverityP3, state.SeverityP4:
		return nil
	default:
		return fmt.Errorf("severity must be one of P0, P1, P2, P3, or P4")
	}
}
