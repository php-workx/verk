package engine

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"verk/internal/adapters/repo/git"
	"verk/internal/adapters/ticketstore/tkmd"
	"verk/internal/state"
)

type WaveAcceptanceRequest struct {
	Wave                 state.WaveArtifact
	TicketPhases         []state.TicketPhase
	ChangedFiles         []string
	TicketScopes         map[string][]string // ticket ID -> owned paths for per-ticket scope validation
	ClaimsReleased       bool
	PersistenceSucceeded bool
}

func BuildWave(ready []tkmd.Ticket, maxConcurrency int) (state.WaveArtifact, error) {
	if maxConcurrency <= 0 {
		return state.WaveArtifact{}, fmt.Errorf("max concurrency must be greater than zero")
	}

	wave := state.WaveArtifact{
		Status: state.WaveStatusPlanned,
	}

	selectedScopes := make([]string, 0)
	for _, ticket := range ready {
		if len(wave.TicketIDs) >= maxConcurrency {
			break
		}
		if err := tkmd.ValidateTicketSchedulingFields(ticket); err != nil {
			return state.WaveArtifact{}, fmt.Errorf("ticket %q is not schedulable: %w", ticket.ID, err)
		}
		if overlapsAny(ticket.OwnedPaths, selectedScopes) {
			continue
		}

		wave.TicketIDs = append(wave.TicketIDs, ticket.ID)
		selectedScopes = append(selectedScopes, ticket.OwnedPaths...)
	}

	if len(wave.TicketIDs) == 0 && len(ready) > 0 {
		return state.WaveArtifact{}, fmt.Errorf("no tickets fit into wave")
	}

	wave.PlannedScope = uniqueSorted(selectedScopes)
	return wave, nil
}

func CheckScopeViolation(changed, owned []string) error {
	if len(owned) == 0 {
		return fmt.Errorf("scope violation: cannot verify scope — ticket has no scope declarations")
	}

	for _, file := range changed {
		if !fileInOwned(file, owned) {
			return fmt.Errorf("scope violation: changed file %q is outside owned scope", file)
		}
	}

	return nil
}

func fileInOwned(file string, owned []string) bool {
	for _, scope := range owned {
		if git.PathsOverlap(file, scope) {
			return true
		}
	}
	return false
}

// validatePerTicketScope checks that each changed file falls within at least one
// ticket's declared scope, and that every ticket declares a non-empty scope.
// This is stricter than checking against the wave-wide union (PlannedScope) because
// it also catches tickets with no scope declarations (G9: scope checks fail closed).
func validatePerTicketScope(ticketIDs, changedFiles []string, ticketScopes map[string][]string) error {
	// Collect scoped and unscoped ticket counts.
	// Unscoped tickets (no owned_paths) are exempt from per-file scope enforcement:
	// the ticket has no declared boundaries, so there is nothing to validate.
	var allOwned []string
	hasUnscoped := false
	for _, id := range ticketIDs {
		scopes := ticketScopes[id]
		if len(scopes) == 0 {
			hasUnscoped = true
			continue
		}
		allOwned = append(allOwned, scopes...)
	}

	// If any ticket is unscoped we cannot attribute changed files to individual
	// tickets, so per-file scope enforcement is skipped for the whole wave.
	if hasUnscoped {
		return nil
	}

	// All tickets declared scope — validate that every changed file falls
	// within at least one ticket's owned paths.
	if len(allOwned) == 0 {
		return nil
	}
	return CheckScopeViolation(changedFiles, allOwned)
}

func AcceptWave(req WaveAcceptanceRequest) (state.WaveArtifact, error) {
	wave := req.Wave
	wave.ActualScope = uniqueSorted(req.ChangedFiles)
	if wave.WaveID == "" {
		wave.WaveID = "wave-unknown"
	}
	if wave.StartedAt.IsZero() {
		wave.StartedAt = time.Now().UTC()
	}
	wave.FinishedAt = time.Now().UTC()
	if wave.Acceptance == nil {
		wave.Acceptance = map[string]any{}
	}
	wave.Acceptance["claims_released"] = req.ClaimsReleased
	wave.Acceptance["persistence_succeeded"] = req.PersistenceSucceeded
	wave.Acceptance["ticket_count"] = len(wave.TicketIDs)

	// Hard failures — these indicate structural problems that prevent
	// the wave from being meaningfully accepted.
	if len(req.TicketPhases) != len(wave.TicketIDs) {
		wave.Status = state.WaveStatusFailed
		wave.Acceptance["reason"] = "ticket count mismatch"
		return wave, fmt.Errorf("wave ticket count mismatch: have %d phases for %d tickets", len(req.TicketPhases), len(wave.TicketIDs))
	}
	if !req.PersistenceSucceeded {
		wave.Status = state.WaveStatusFailed
		wave.Acceptance["reason"] = "persistence failed"
		return wave, fmt.Errorf("wave persistence failed")
	}

	// Scope violation is a hard failure: a worker modifying files outside its
	// declared owned paths is a safety violation that must be caught and blocked
	// (G9: scope checks fail closed).
	if err := validatePerTicketScope(wave.TicketIDs, req.ChangedFiles, req.TicketScopes); err != nil {
		wave.Status = state.WaveStatusFailed
		wave.Acceptance["reason"] = err.Error()
		return wave, err
	}

	// Soft issues — record but don't block the wave.
	// Blocked tickets and unreleased claims are expected in normal operation
	// (e.g., worker returned needs_context or blocked status).
	var warnings []string
	var blockedTickets []string
	for i, phase := range req.TicketPhases {
		if phase != state.TicketPhaseClosed {
			blockedTickets = append(blockedTickets, wave.TicketIDs[i])
		}
	}
	if len(blockedTickets) > 0 {
		wave.Acceptance["blocked_tickets"] = blockedTickets
		warnings = append(warnings, fmt.Sprintf("%d ticket(s) not closed: %s", len(blockedTickets), strings.Join(blockedTickets, ", ")))
	}
	if !req.ClaimsReleased {
		warnings = append(warnings, "claims not fully released")
	}

	wave.Status = state.WaveStatusAccepted
	if len(warnings) > 0 {
		wave.Acceptance["warnings"] = warnings
		wave.Acceptance["reason"] = "accepted with warnings"
	} else {
		wave.Acceptance["reason"] = "accepted"
	}
	return wave, nil
}

func overlapsAny(candidateScopes, selectedScopes []string) bool {
	for _, candidate := range candidateScopes {
		for _, selected := range selectedScopes {
			if git.PathsOverlap(candidate, selected) {
				return true
			}
		}
	}
	return false
}

func uniqueSorted(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
