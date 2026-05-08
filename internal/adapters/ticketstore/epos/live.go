package epos

import (
	"path/filepath"
	"verk/internal/state"

	eposticket "github.com/php-workx/epos/ticket"
	eposruntime "github.com/php-workx/epos/ticket/runtime"
)

func LoadLiveClaim(rootDir, ticketID string) (*state.ClaimArtifact, error) {
	runtimeState, err := eposruntime.ReadRuntimeState(resolveRepoRoot(rootDir), ticketID)
	if err != nil {
		return nil, err
	}
	return RuntimeStateToClaimArtifact(runtimeState), nil
}

func RuntimeStateToClaimArtifact(runtimeState *eposticket.RuntimeState) *state.ClaimArtifact {
	if runtimeState == nil || runtimeState.Claim == nil || runtimeState.Lease == nil {
		return nil
	}
	return &state.ClaimArtifact{
		TicketID:              runtimeState.TicketID,
		OwnerRunID:            runtimeState.Claim.ClaimedBy,
		LeaseID:               runtimeState.Lease.LeaseID,
		LeasedAt:              runtimeState.Claim.ClaimedAt,
		ExpiresAt:             runtimeState.Lease.ExpiresAt,
		State:                 "active",
		LastSeenLiveClaimPath: liveClaimRelativePath(runtimeState.TicketID),
	}
}

func liveClaimRelativePath(ticketID string) string {
	return filepath.Join(".tickets", ".claims", ticketID+".json")
}
