package engine

import (
	"fmt"
	"path/filepath"
	"sort"
	"verk/internal/adapters/ticketstore/tkmd"
	"verk/internal/state"
)

type StatusRequest struct {
	RepoRoot string
	RunID    string
}

type StatusTicket struct {
	TicketID                 string            `json:"ticket_id"`
	Title                    string            `json:"title,omitempty"`
	Phase                    state.TicketPhase `json:"phase"`
	EffectiveReviewThreshold state.Severity    `json:"effective_review_threshold,omitempty"`
	BlockReason              string            `json:"block_reason,omitempty"`
	FailedGate               string            `json:"failed_gate,omitempty"`
	ClaimState               string            `json:"claim_state,omitempty"`
	LeaseID                  string            `json:"lease_id,omitempty"`
	ClaimDivergence          bool              `json:"claim_divergence"`
	ClaimDivergenceReason    string            `json:"claim_divergence_reason,omitempty"`
}

type StatusReport struct {
	RunID                    string                `json:"run_id"`
	RootTicketID             string                `json:"root_ticket_id"`
	RunStatus                state.EpicRunStatus   `json:"run_status"`
	CurrentPhase             state.TicketPhase     `json:"current_phase"`
	CurrentWave              string                `json:"current_wave,omitempty"`
	EffectiveReviewThreshold state.Severity        `json:"effective_review_threshold,omitempty"`
	LastFailedGate           string                `json:"last_failed_gate,omitempty"`
	ClaimDivergence          bool                  `json:"claim_divergence"`
	Tickets                  []StatusTicket        `json:"tickets"`
	ActiveClaims             []state.ClaimArtifact `json:"active_claims,omitempty"`
}

func DeriveStatus(req StatusRequest) (StatusReport, error) {
	artifacts, err := loadRunArtifacts(req.RepoRoot, req.RunID)
	if err != nil {
		return StatusReport{}, err
	}

	ticketIDs := append([]string(nil), artifacts.Run.TicketIDs...)
	sort.Strings(ticketIDs)

	report := StatusReport{
		RunID:        artifacts.RunID,
		RootTicketID: artifacts.Run.RootTicketID,
		RunStatus:    artifacts.Run.Status,
		CurrentPhase: artifacts.Run.CurrentPhase,
		CurrentWave:  deriveCurrentWaveID(artifacts.Waves),
		Tickets:      make([]StatusTicket, 0, len(ticketIDs)),
	}

	for _, ticketID := range ticketIDs {
		snapshot := artifacts.Tickets[ticketID]
		plan := artifacts.Plans[ticketID]
		title := plan.Title
		if title == "" {
			title = loadTicketTitle(artifacts.RepoRoot, ticketID)
		}
		entry := StatusTicket{
			TicketID:                 ticketID,
			Title:                    title,
			Phase:                    snapshot.CurrentPhase,
			EffectiveReviewThreshold: plan.EffectiveReviewThreshold,
			BlockReason:              snapshot.BlockReason,
		}
		if snapshot.Closeout != nil {
			entry.FailedGate = snapshot.Closeout.FailedGate
			if report.LastFailedGate == "" && snapshot.Closeout.FailedGate != "" {
				report.LastFailedGate = snapshot.Closeout.FailedGate
			}
		}
		if plan.EffectiveReviewThreshold != "" {
			report.EffectiveReviewThreshold = mostRestrictiveThreshold(report.EffectiveReviewThreshold, plan.EffectiveReviewThreshold)
		}

		claim, claimErr := deriveTicketClaim(artifacts.RepoRoot, artifacts.RunID, ticketID, snapshot)
		if claimErr != nil {
			report.ClaimDivergence = true
			entry.ClaimDivergence = true
			entry.ClaimDivergenceReason = claimErr.Error()
			entry.ClaimState = "diverged"
		} else if claim != nil {
			entry.ClaimState = claim.State
			entry.LeaseID = claim.LeaseID
			if claim.State == "active" {
				report.ActiveClaims = append(report.ActiveClaims, *claim)
			}
		}
		report.Tickets = append(report.Tickets, entry)
	}

	sort.Slice(report.ActiveClaims, func(i, j int) bool {
		return report.ActiveClaims[i].TicketID < report.ActiveClaims[j].TicketID
	})
	return report, nil
}

func loadTicketTitle(repoRoot, ticketID string) string {
	ticket, err := tkmd.LoadTicket(filepath.Join(repoRoot, ".tickets", ticketID+".md"))
	if err != nil {
		return ""
	}
	return ticket.Title
}

func deriveCurrentWaveID(waves map[string]state.WaveArtifact) string {
	current := state.WaveArtifact{}
	found := false
	for _, wave := range waves {
		if wave.Status == state.WaveStatusAccepted {
			continue
		}
		if !found || wave.Ordinal > current.Ordinal {
			current = wave
			found = true
		}
	}
	if found {
		return current.WaveID
	}
	return ""
}

func deriveTicketClaim(repoRoot, runID, ticketID string, snapshot TicketRunSnapshot) (*state.ClaimArtifact, error) {
	live, err := loadOptionalClaim(liveClaimPath(repoRoot, ticketID))
	if err != nil {
		return nil, err
	}
	durable, err := loadOptionalClaim(durableClaimPath(repoRoot, runID, ticketID))
	if err != nil {
		return nil, err
	}
	if live == nil && durable == nil {
		return nil, nil //nolint:nilnil // not-found: nil value + nil error = "no data, no problem"
	}
	claim, err := tkmd.ReconcileClaim(live, durable, runID, isTerminalPhase(snapshot.CurrentPhase))
	if err != nil {
		return nil, err
	}
	if snapshot.Implementation != nil && snapshot.Implementation.LeaseID != "" && claim.State == "active" && snapshot.Implementation.LeaseID != claim.LeaseID {
		return nil, fmt.Errorf("ticket %s lease mismatch between implementation artifact %q and claim %q", ticketID, snapshot.Implementation.LeaseID, claim.LeaseID)
	}
	return &claim, nil
}

// mostRestrictiveThreshold returns the more restrictive of two review thresholds.
// Precedence: strict > standard > lenient > ""(empty).
// If both are equal, either is returned. If one is empty, the other is returned.
func mostRestrictiveThreshold(a, b state.Severity) state.Severity {
	thresholdPrecedence := map[state.Severity]int{
		state.SeverityP0: 4, // strict
		"strict":         4,
		state.SeverityP1: 3, // standard
		"standard":       3,
		state.SeverityP2: 2, // lenient
		"lenient":        2,
		state.SeverityP3: 1,
		state.SeverityP4: 0,
	}
	aRank := thresholdPrecedence[a]
	bRank := thresholdPrecedence[b]
	if aRank >= bRank {
		return a
	}
	return b
}
