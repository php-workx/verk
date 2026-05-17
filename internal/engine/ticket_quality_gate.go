package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"
	"verk/internal/adapters/ticketstore/epos"
	"verk/internal/policy"
	"verk/internal/state"
)

// RunTicketQualityGate runs the deterministic ticket-quality evaluator for an
// epic and persists the artifact under .verk/runs/<run-id>/ticket-quality.json.
// Returns the artifact (may be blocked) and any IO error.
func RunTicketQualityGate(ctx context.Context, repoRoot, runID string, cfg policy.Config, root epos.Ticket, children []epos.Ticket) (state.TicketQualityArtifact, error) {
	_ = ctx // reserved for future cancellation propagation

	tickets := append([]epos.Ticket{root}, children...)
	input := TicketQualityInput{
		RootTicket: root,
		Tickets:    tickets,
		Config:     cfg,
	}
	artifact := EvaluateTicketQuality(input)
	artifact.ArtifactMeta = state.ArtifactMeta{
		SchemaVersion: 1,
		RunID:         runID,
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}

	if err := persistTicketQualityArtifact(repoRoot, runID, artifact); err != nil {
		return artifact, fmt.Errorf("persist ticket quality artifact: %w", err)
	}
	return artifact, nil
}

func persistTicketQualityArtifact(repoRoot, runID string, artifact state.TicketQualityArtifact) error {
	if err := ValidateArtifactIdentifier(runID, "run id"); err != nil {
		return fmt.Errorf("ticket quality gate: %w", err)
	}
	dir := filepath.Join(repoRoot, ".verk", "runs", runID)
	path := filepath.Join(dir, "ticket-quality.json")
	data, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		return err
	}
	return state.SaveFileAtomic(path, data, 0o644)
}
