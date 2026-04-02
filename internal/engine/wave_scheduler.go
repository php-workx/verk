package engine

import (
	"fmt"
	"sort"
	"time"

	"verk/internal/adapters/repo/git"
	"verk/internal/adapters/ticketstore/tkmd"
	"verk/internal/state"
)

type WaveAcceptanceRequest struct {
	Wave                 state.WaveArtifact
	TicketPhases         []state.TicketPhase
	ChangedFiles         []string
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
		return fmt.Errorf("owned scope is required")
	}

	for _, file := range changed {
		for _, scope := range owned {
			if git.PathsOverlap(file, scope) {
				goto nextFile
			}
		}
		return fmt.Errorf("scope violation: changed file %q is outside owned scope", file)
	nextFile:
	}

	return nil
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

	if len(req.TicketPhases) != len(wave.TicketIDs) {
		wave.Status = state.WaveStatusFailed
		wave.Acceptance["reason"] = "ticket count mismatch"
		return wave, fmt.Errorf("wave ticket count mismatch: have %d phases for %d tickets", len(req.TicketPhases), len(wave.TicketIDs))
	}
	for i, phase := range req.TicketPhases {
		if phase != state.TicketPhaseClosed {
			wave.Status = state.WaveStatusFailed
			wave.Acceptance["reason"] = fmt.Sprintf("ticket %q ended %s", wave.TicketIDs[i], phase)
			return wave, fmt.Errorf("wave ticket %q ended %s", wave.TicketIDs[i], phase)
		}
	}
	if !req.ClaimsReleased {
		wave.Status = state.WaveStatusFailed
		wave.Acceptance["reason"] = "claims not released"
		return wave, fmt.Errorf("wave claims were not released")
	}
	if !req.PersistenceSucceeded {
		wave.Status = state.WaveStatusFailed
		wave.Acceptance["reason"] = "persistence failed"
		return wave, fmt.Errorf("wave persistence failed")
	}
	if err := CheckScopeViolation(req.ChangedFiles, wave.PlannedScope); err != nil {
		wave.Status = state.WaveStatusFailed
		wave.Acceptance["reason"] = err.Error()
		return wave, err
	}

	wave.Status = state.WaveStatusAccepted
	wave.Acceptance["reason"] = "accepted"
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
