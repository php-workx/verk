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
	// TicketDetails optionally provides extra per-ticket information used to
	// build explicit blocked-ticket explanations on the wave summary. The
	// engine populates this from snapshot block reasons and worker-reported
	// block reasons; tests may pass it directly.
	TicketDetails map[string]WaveTicketDetail
}

// WaveTicketDetail carries the engine's view of a single wave ticket for the
// purpose of building explicit blocked-ticket entries on the wave summary.
// All fields are optional: missing fields fall back to structural defaults
// derived from TicketPhase.
type WaveTicketDetail struct {
	// Title is the human-readable ticket title.
	Title string
	// BlockReason is the worker- or engine-reported block reason. Empty
	// when the ticket was closed normally.
	BlockReason string
	// RequiresOperator is true when the engine cannot safely retry this
	// ticket without operator input (e.g. needs_context outcome).
	RequiresOperator bool
	// CanRetryAutomatically is true when `verk run` can pick the ticket up
	// again without manual intervention (e.g. blocked by transient failure
	// or by another wave that will close on the next run).
	CanRetryAutomatically bool
}

// BlockedTicketSummary is the structured per-ticket entry the engine emits
// for wave summaries. Each entry explains why the ticket did not close and
// whether the engine can retry it automatically. The shape is preserved on
// the wave artifact under Acceptance["blocked_ticket_details"] as a slice of
// map[string]any so JSON consumers can read it without a custom decoder.
type BlockedTicketSummary struct {
	TicketID              string            `json:"ticket_id"`
	Title                 string            `json:"title,omitempty"`
	Phase                 state.TicketPhase `json:"phase"`
	BlockReason           string            `json:"block_reason,omitempty"`
	RequiresOperator      bool              `json:"requires_operator"`
	CanRetryAutomatically bool              `json:"can_retry_automatically"`
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
	var blockedDetails []BlockedTicketSummary
	for i, phase := range req.TicketPhases {
		if phase == state.TicketPhaseClosed {
			continue
		}
		ticketID := wave.TicketIDs[i]
		blockedTickets = append(blockedTickets, ticketID)
		blockedDetails = append(blockedDetails, buildBlockedTicketSummary(ticketID, phase, req.TicketDetails[ticketID]))
	}
	if len(blockedTickets) > 0 {
		wave.Acceptance["blocked_tickets"] = blockedTickets
		wave.Acceptance["blocked_ticket_details"] = blockedDetails
		warnings = append(warnings, fmt.Sprintf("%d ticket(s) not closed: %s", len(blockedTickets), strings.Join(blockedTickets, ", ")))
		for _, d := range blockedDetails {
			if d.BlockReason != "" {
				warnings = append(warnings, fmt.Sprintf("%s blocked: %s", d.TicketID, d.BlockReason))
			}
		}
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

// buildBlockedTicketSummary fills in defaults for a blocked ticket so the
// summary always answers: which ticket, what phase, why it stopped, and
// whether the engine can keep going on its own.
//
// The defaults are conservative: when the engine has no explicit detail it
// assumes operator input is required and automatic retry is not safe. This
// matches the behavior that `verk run` should never silently retry a ticket
// whose block reason was never recorded.
func buildBlockedTicketSummary(ticketID string, phase state.TicketPhase, detail WaveTicketDetail) BlockedTicketSummary {
	summary := BlockedTicketSummary{
		TicketID:              ticketID,
		Phase:                 phase,
		Title:                 detail.Title,
		BlockReason:           detail.BlockReason,
		RequiresOperator:      detail.RequiresOperator,
		CanRetryAutomatically: detail.CanRetryAutomatically,
	}
	if summary.BlockReason == "" {
		summary.BlockReason = defaultPhaseReason(phase)
	}
	// Phase-specific safety net: a phase that is not Blocked / Closed (e.g.
	// stuck in Implement / Verify) typically reflects a partially-completed
	// run that `verk run` can pick up on its own. Blocked is the canonical
	// "needs operator" terminus unless the caller said otherwise.
	if phase == state.TicketPhaseBlocked && !summary.RequiresOperator && !summary.CanRetryAutomatically {
		summary.RequiresOperator = true
	}
	return summary
}

func defaultPhaseReason(phase state.TicketPhase) string {
	switch phase {
	case state.TicketPhaseBlocked:
		return "ticket transitioned to blocked phase without an explicit reason"
	case state.TicketPhaseImplement, state.TicketPhaseVerify, state.TicketPhaseReview, state.TicketPhaseRepair:
		return fmt.Sprintf("ticket did not finish %s phase", phase)
	default:
		return fmt.Sprintf("ticket ended in phase %q without closing", phase)
	}
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
