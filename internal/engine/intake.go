package engine

import (
	"crypto/sha1"
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

	workerProfile := cfg.EffectiveWorkerProfile()
	reviewerProfile := cfg.EffectiveReviewerProfile()

	return state.PlanArtifact{
		ArtifactMeta: state.ArtifactMeta{
			SchemaVersion: artifactSchemaVersion,
			RunID:         ticketRunID(t),
			CreatedAt:     time.Now().UTC(),
			UpdatedAt:     time.Now().UTC(),
		},
		TicketID:                 t.ID,
		Title:                    t.Title,
		Description:              t.Body,
		Phase:                    state.TicketPhaseIntake,
		AcceptanceCriteria:       append([]string(nil), t.AcceptanceCriteria...),
		Criteria:                 buildPlanCriteria(t.AcceptanceCriteria),
		TestCases:                append([]string(nil), t.TestCases...),
		ValidationCommands:       append([]string(nil), t.ValidationCommands...),
		DeclaredChecks:           ticketDeclaredChecks(t),
		OwnedPaths:               append([]string(nil), t.OwnedPaths...),
		ReviewThreshold:          plannedThreshold,
		EffectiveReviewThreshold: effective,
		// RuntimePreference is ticket-level routing only — it can swap the
		// runtime identifier (e.g. force codex for a ticket) but MUST NOT
		// influence model or reasoning selection. Ticket frontmatter `model`
		// is intentionally never copied into the plan; see ver-laq2.
		RuntimePreference: t.Runtime,
		WorkerProfile:     profileSnapshotFor(workerProfile, t.Runtime),
		ReviewerProfile:   profileSnapshotFor(reviewerProfile, t.Runtime),
	}, nil
}

// profileSnapshotFor captures the effective role profile (runtime/model/
// reasoning) that intake resolved for this ticket. The ticket-level
// RuntimePreference, when set, overrides the runtime field so the snapshot
// mirrors what workers and reviewers will actually execute against.
func profileSnapshotFor(profile policy.RoleProfile, runtimePreference string) *state.RoleProfileSnapshot {
	snapshot := state.RoleProfileSnapshot{
		Runtime:   strings.TrimSpace(profile.Runtime),
		Model:     strings.TrimSpace(profile.Model),
		Reasoning: strings.TrimSpace(profile.Reasoning),
	}
	if pref := strings.TrimSpace(runtimePreference); pref != "" {
		snapshot.Runtime = pref
	}
	if snapshot.Runtime == "" && snapshot.Model == "" && snapshot.Reasoning == "" {
		return nil
	}
	return &snapshot
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

func buildPlanCriteria(criteria []string) []state.PlanCriterion {
	out := make([]state.PlanCriterion, 0, len(criteria))
	for i, text := range criteria {
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			continue
		}
		sum := sha1.Sum([]byte(trimmed))
		out = append(out, state.PlanCriterion{
			ID:   fmt.Sprintf("criterion-%02d-%x", i+1, sum[:4]),
			Text: trimmed,
		})
	}
	return out
}

func ticketDeclaredChecks(t tkmd.Ticket) []string {
	if t.UnknownFrontmatter == nil {
		return nil
	}
	raw, ok := t.UnknownFrontmatter["declared_checks"]
	if !ok {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0)
	for _, value := range asStringSlice(raw) {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func asStringSlice(value any) []string {
	switch v := value.(type) {
	case nil:
		return nil
	case []string:
		return append([]string(nil), v...)
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			out = append(out, fmt.Sprint(item))
		}
		return out
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		return []string{v}
	default:
		return []string{fmt.Sprint(v)}
	}
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
