package engine

import (
	"context"
	"fmt"
	"verk/internal/state"
)

type ReopenRequest struct {
	RepoRoot string
	RunID    string
	TicketID string
	ToPhase  state.TicketPhase
}

func ReopenTicket(ctx context.Context, req ReopenRequest) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if req.RunID == "" {
		return fmt.Errorf("reopen requires run id")
	}
	if req.TicketID == "" {
		return fmt.Errorf("reopen requires ticket id")
	}
	if req.ToPhase == "" {
		return fmt.Errorf("reopen requires target phase")
	}

	artifacts, err := loadRunArtifacts(req.RepoRoot, req.RunID)
	if err != nil {
		return err
	}

	snapshot, ok := artifacts.Tickets[req.TicketID]
	if !ok {
		return fmt.Errorf("ticket %s is not part of run %s", req.TicketID, req.RunID)
	}
	if err := validateReopenTransition(snapshot.CurrentPhase, req.ToPhase); err != nil {
		return err
	}

	fromPhase := snapshot.CurrentPhase
	snapshot.CurrentPhase = req.ToPhase
	snapshot.BlockReason = ""
	snapshot.UpdatedAt = stateTime()
	artifacts.Tickets[req.TicketID] = snapshot

	var wavePath string
	var wavePayload any
	if wave, found := findWaveForTicket(artifacts.Waves, req.TicketID); found {
		wave.Status = state.WaveStatusFailedReopened
		wave.Acceptance = cloneMap(wave.Acceptance)
		if wave.Acceptance == nil {
			wave.Acceptance = map[string]any{}
		}
		wave.Acceptance["reopened_ticket_id"] = req.TicketID
		wave.Acceptance["reopen_target_phase"] = string(req.ToPhase)
		wave.FinishedAt = stateTime()
		artifacts.Waves[wave.WaveID] = wave
		wavePath = waveArtifactPath(artifacts.RepoRoot, req.RunID, wave.WaveID)
		wavePayload = wave
	}

	updateRunStatusFromTickets(&artifacts.Run, artifacts.Tickets)
	appendRunAuditEvent(&artifacts.Run, "ticket_reopened", req.TicketID, req.ToPhase, map[string]any{
		"from_phase": string(fromPhase),
		"to_phase":   string(req.ToPhase),
	})

	if err := state.WriteTransitionCommit(
		state.TransitionPaths{
			TicketArtifactPath: ticketSnapshotPath(artifacts.RepoRoot, req.RunID, req.TicketID),
			WaveArtifactPath:   wavePath,
			RunArtifactPath:    runJSONPath(artifacts.RepoRoot, req.RunID),
		},
		state.TransitionPayloads{
			TicketArtifact: snapshot,
			WaveArtifact:   wavePayload,
			RunArtifact:    artifacts.Run,
		},
	); err != nil {
		return err
	}

	// Update ticket markdown status only after the atomic commit succeeds.
	// If setTicketReady fails, the run state is consistent — the markdown
	// is stale but can be reconciled on the next status update.
	if err := setTicketReady(artifacts.RepoRoot, req.TicketID); err != nil {
		return fmt.Errorf("reopen committed but ticket store update failed: %w", err)
	}
	return nil
}

func validateReopenTransition(from, to state.TicketPhase) error {
	defaultTarget, ok := DefaultReopenTargetForPhase(from)
	switch {
	case ok && to == defaultTarget:
		return nil
	case from == state.TicketPhaseBlocked && to == state.TicketPhaseRepair:
		return nil
	default:
		return fmt.Errorf("reopen transition %s -> %s is not allowed", from, to)
	}
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
