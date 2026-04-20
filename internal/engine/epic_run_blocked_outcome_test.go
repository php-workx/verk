package engine

import (
	"errors"
	"testing"
	"verk/internal/state"
)

// TestSnapshotPhaseOrDefault_MissingDerivesIntake verifies that when no
// snapshot nor any per-phase artifact is on disk, the engine's existing
// derivation returns Intake. The important property for the wave-summary
// fix is that the phase is NOT Blocked — a ticket that never ran is not
// blocked, and must not be rendered as such. Any non-{Closed,Blocked} phase
// satisfies the downstream CanRetryAutomatically gate.
func TestSnapshotPhaseOrDefault_MissingDerivesIntake(t *testing.T) {
	repo := t.TempDir()
	phase := snapshotPhaseOrImplement(repo, "run-missing", "mm-nope")
	if phase == state.TicketPhaseBlocked || phase == state.TicketPhaseClosed {
		t.Fatalf("missing snapshot must not produce a terminal phase; got %q", phase)
	}
	if phase != state.TicketPhaseIntake {
		t.Fatalf("expected derived Intake for a ticket with no artifacts; got %q", phase)
	}
}

// TestSnapshotPhaseOrDefault_UsesPersistedPhase verifies that when a snapshot
// exists, its CurrentPhase is returned verbatim. This is what lets the wave
// summary report Review / Verify / Repair honestly when a worker errors
// mid-phase rather than printing a bogus "blocked without reason".
func TestSnapshotPhaseOrDefault_UsesPersistedPhase(t *testing.T) {
	repo := t.TempDir()
	runID := "run-honest"
	ticketID := "mm-e7e1"
	snap := TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:     ticketID,
		CurrentPhase: state.TicketPhaseReview,
	}
	if err := state.SaveJSONAtomic(ticketSnapshotPath(repo, runID, ticketID), snap); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}
	if got := snapshotPhaseOrImplement(repo, runID, ticketID); got != state.TicketPhaseReview {
		t.Fatalf("expected Review from snapshot, got %q", got)
	}
}

// TestBuildWaveTicketDetails_FallsBackToOutcomeError verifies that when no
// BlockReason is persisted on the snapshot, the wave summary uses the
// outcome error string so operators never see the generic "transitioned to
// blocked phase without an explicit reason" message for a ticket whose run
// actually errored with a real cause.
func TestBuildWaveTicketDetails_FallsBackToOutcomeError(t *testing.T) {
	repo := t.TempDir()
	runID := "run-error-fallback"
	ticketID := "mm-p7qg"

	// Persist a minimal snapshot with no BlockReason, mirroring the shape
	// that exists on disk when RunTicket errors mid-phase and returns
	// before calling transitionTo(Blocked).
	snap := TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:     ticketID,
		CurrentPhase: state.TicketPhaseReview,
	}
	if err := state.SaveJSONAtomic(ticketSnapshotPath(repo, runID, ticketID), snap); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}

	outcomes := []waveTicketOutcome{{
		ticketID: ticketID,
		phase:    state.TicketPhaseReview,
		err:      errors.New("reviewer runtime: mcp context7 unavailable"),
	}}

	details := buildWaveTicketDetails(repo, runID, []string{ticketID}, outcomes)
	detail, ok := details[ticketID]
	if !ok {
		t.Fatalf("expected detail for %s", ticketID)
	}
	if detail.BlockReason != "reviewer runtime: mcp context7 unavailable" {
		t.Fatalf("unexpected BlockReason: %q", detail.BlockReason)
	}
}

// TestBuildWaveTicketDetails_PrefersSnapshotReason verifies that a persisted
// BlockReason on the ticket snapshot still wins over any outcome error —
// snapshots reflect the ticket's own recorded block, which is higher-fidelity
// than a wave-level wrapper error that may not be specific.
func TestBuildWaveTicketDetails_PrefersSnapshotReason(t *testing.T) {
	repo := t.TempDir()
	runID := "run-prefer-snapshot"
	ticketID := "mm-vm6p"
	snap := TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:     ticketID,
		CurrentPhase: state.TicketPhaseBlocked,
		BlockReason:  "worker needs_context: missing API key",
	}
	if err := state.SaveJSONAtomic(ticketSnapshotPath(repo, runID, ticketID), snap); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}
	outcomes := []waveTicketOutcome{{
		ticketID: ticketID,
		phase:    state.TicketPhaseBlocked,
		err:      errors.New("generic wave wrapper error"),
	}}
	details := buildWaveTicketDetails(repo, runID, []string{ticketID}, outcomes)
	if got := details[ticketID].BlockReason; got != "worker needs_context: missing API key" {
		t.Fatalf("expected snapshot reason to win, got %q", got)
	}
}
