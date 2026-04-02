package engine

import (
	"context"
	"fmt"

	"verk/internal/adapters/ticketstore/tkmd"
	"verk/internal/state"
)

type ResumeRequest struct {
	RepoRoot string
	RunID    string
}

type ResumeReport struct {
	Run              state.RunArtifact `json:"run"`
	Status           StatusReport      `json:"status"`
	RecoveredTickets []string          `json:"recovered_tickets,omitempty"`
}

func ResumeRun(ctx context.Context, req ResumeRequest) (ResumeReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return ResumeReport{}, err
	}
	if req.RunID == "" {
		return ResumeReport{}, fmt.Errorf("resume requires run id")
	}

	artifacts, err := loadRunArtifacts(req.RepoRoot, req.RunID)
	if err != nil {
		return ResumeReport{}, err
	}

	recovered := make([]string, 0)
	for _, ticketID := range artifacts.Run.TicketIDs {
		snapshot := artifacts.Tickets[ticketID]

		claim, repaired, claimErr := reconcileTicketClaimForResume(artifacts.RepoRoot, req.RunID, ticketID, snapshot)
		if claimErr != nil {
			artifacts.Run.Status = state.EpicRunStatusBlocked
			artifacts.Run.CurrentPhase = state.TicketPhaseBlocked
			appendRunAuditEvent(&artifacts.Run, "resume_claim_divergence", ticketID, snapshot.CurrentPhase, map[string]any{
				"reason": claimErr.Error(),
			})
			if err := state.SaveJSONAtomic(runJSONPath(artifacts.RepoRoot, req.RunID), artifacts.Run); err != nil {
				return ResumeReport{}, err
			}
			status, err := DeriveStatus(StatusRequest{RepoRoot: artifacts.RepoRoot, RunID: req.RunID})
			if err != nil {
				return ResumeReport{}, err
			}
			return ResumeReport{Run: artifacts.Run, Status: status}, nil
		}
		if repaired {
			recovered = append(recovered, ticketID)
		}
		if claim != nil && snapshot.CurrentPhase == state.TicketPhaseBlocked {
			_ = claim
		}

		if snapshot.Closeout == nil && (snapshot.CurrentPhase == state.TicketPhaseCloseout || snapshot.CurrentPhase == state.TicketPhaseClosed) {
			plan, ok := artifacts.Plans[ticketID]
			if !ok {
				return ResumeReport{}, fmt.Errorf("resume requires plan artifact for ticket %s", ticketID)
			}
			ticket, err := tkmd.LoadTicket(ticketMarkdownPath(artifacts.RepoRoot, ticketID))
			if err != nil {
				return ResumeReport{}, err
			}
			if snapshot.Verification == nil || snapshot.Review == nil {
				return ResumeReport{}, fmt.Errorf("resume cannot repair closeout for ticket %s without verification and review artifacts", ticketID)
			}
			closeout, err := BuildCloseoutArtifact(ticket, plan, snapshot.Verification, snapshot.Review)
			if err != nil {
				return ResumeReport{}, err
			}
			snapshot.Closeout = &closeout
			if snapshot.CurrentPhase == state.TicketPhaseCloseout {
				if closeout.Closable {
					snapshot.CurrentPhase = state.TicketPhaseClosed
					snapshot.BlockReason = ""
				} else {
					snapshot.CurrentPhase = state.TicketPhaseBlocked
					snapshot.BlockReason = closeout.FailedGate
				}
			}
			snapshot.UpdatedAt = stateTime()
			artifacts.Tickets[ticketID] = snapshot
			recovered = appendIfMissing(recovered, ticketID)
			if err := state.WriteTransitionCommit(
				state.TransitionPaths{
					TicketArtifactPath: ticketSnapshotPath(artifacts.RepoRoot, req.RunID, ticketID),
					RunArtifactPath:    runJSONPath(artifacts.RepoRoot, req.RunID),
				},
				state.TransitionPayloads{
					TicketArtifact: snapshot,
					RunArtifact:    artifacts.Run,
				},
			); err != nil {
				return ResumeReport{}, err
			}
			if err := state.SaveJSONAtomic(closeoutArtifactPath(artifacts.RepoRoot, req.RunID, ticketID), closeout); err != nil {
				return ResumeReport{}, err
			}
		}
	}

	updateRunStatusFromTickets(&artifacts.Run, artifacts.Tickets)
	if len(recovered) > 0 {
		appendRunAuditEvent(&artifacts.Run, "resume_repaired_committed_state", "", artifacts.Run.CurrentPhase, map[string]any{
			"tickets": recovered,
		})
	}
	if err := state.SaveJSONAtomic(runJSONPath(artifacts.RepoRoot, req.RunID), artifacts.Run); err != nil {
		return ResumeReport{}, err
	}

	status, err := DeriveStatus(StatusRequest{RepoRoot: artifacts.RepoRoot, RunID: req.RunID})
	if err != nil {
		return ResumeReport{}, err
	}
	return ResumeReport{
		Run:              artifacts.Run,
		Status:           status,
		RecoveredTickets: recovered,
	}, nil
}

func reconcileTicketClaimForResume(repoRoot, runID, ticketID string, snapshot TicketRunSnapshot) (*state.ClaimArtifact, bool, error) {
	live, err := loadOptionalClaim(liveClaimPath(repoRoot, ticketID))
	if err != nil {
		return nil, false, err
	}
	durable, err := loadOptionalClaim(durableClaimPath(repoRoot, runID, ticketID))
	if err != nil {
		return nil, false, err
	}
	if live == nil && durable == nil {
		return nil, false, nil
	}
	claim, err := tkmd.ReconcileClaim(live, durable, runID, isTerminalPhase(snapshot.CurrentPhase))
	if err != nil {
		return nil, false, err
	}
	repaired := false
	if durable == nil && live != nil && live.OwnerRunID == runID && !isTerminalPhase(snapshot.CurrentPhase) {
		if err := state.SaveJSONAtomic(durableClaimPath(repoRoot, runID, ticketID), claim); err != nil {
			return nil, false, err
		}
		repaired = true
	}
	if snapshot.Implementation != nil && snapshot.Implementation.LeaseID != "" && claim.State == "active" && snapshot.Implementation.LeaseID != claim.LeaseID {
		return nil, false, fmt.Errorf("ticket %s lease mismatch between implementation artifact %q and claim %q", ticketID, snapshot.Implementation.LeaseID, claim.LeaseID)
	}
	return &claim, repaired, nil
}

func appendIfMissing(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
