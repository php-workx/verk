package tkmd

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"verk/internal/state"
)

func TestAcquireClaim_Exclusive(t *testing.T) {
	dir := t.TempDir()

	claim, err := AcquireClaim(dir, "run-a", "ticket-1", "lease-a", 15*time.Minute)
	if err != nil {
		t.Fatalf("AcquireClaim: %v", err)
	}
	if claim.LeaseID != "lease-a" {
		t.Fatalf("expected lease-a, got %q", claim.LeaseID)
	}
	if claim.State != "active" {
		t.Fatalf("expected active claim, got %q", claim.State)
	}

	if _, err := AcquireClaim(dir, "run-b", "ticket-1", "lease-b"); err == nil {
		t.Fatal("expected second acquisition to fail")
	}

	livePath, durablePath, err := claimPaths(dir, "run-a", "ticket-1")
	if err != nil {
		t.Fatalf("claimPaths: %v", err)
	}
	if _, err := os.Stat(livePath); err != nil {
		t.Fatalf("expected live claim file: %v", err)
	}
	if _, err := os.Stat(durablePath); err != nil {
		t.Fatalf("expected durable claim file: %v", err)
	}
}

func TestRenewClaim_RejectsStaleLeaseID(t *testing.T) {
	dir := t.TempDir()

	acquired, err := AcquireClaim(dir, "run-a", "ticket-1", "lease-a", 10*time.Minute)
	if err != nil {
		t.Fatalf("AcquireClaim: %v", err)
	}
	if _, err := RenewClaim(dir, "run-a", "ticket-1", "lease-stale", 20*time.Minute); err == nil {
		t.Fatal("expected stale lease renewal to fail")
	}

	renewed, err := RenewClaim(dir, "run-a", "ticket-1", "lease-a", 20*time.Minute)
	if err != nil {
		t.Fatalf("RenewClaim: %v", err)
	}
	if !renewed.ExpiresAt.After(acquired.ExpiresAt) {
		t.Fatalf("expected renewed expiry to extend past %s, got %s", acquired.ExpiresAt, renewed.ExpiresAt)
	}
}

func TestReleaseClaim_PersistsReleasedStateThenRemovesLiveClaim(t *testing.T) {
	dir := t.TempDir()

	acquired, err := AcquireClaim(dir, "run-a", "ticket-1", "lease-a", 10*time.Minute)
	if err != nil {
		t.Fatalf("AcquireClaim: %v", err)
	}
	if err := ReleaseClaim(dir, "run-a", "ticket-1", acquired.LeaseID, "completed"); err != nil {
		t.Fatalf("ReleaseClaim: %v", err)
	}

	livePath, durablePath, err := claimPaths(dir, "run-a", "ticket-1")
	if err != nil {
		t.Fatalf("claimPaths: %v", err)
	}
	if _, err := os.Stat(livePath); !os.IsNotExist(err) {
		t.Fatalf("expected live claim to be removed, got err=%v", err)
	}

	durable, err := loadClaimArtifact(durablePath)
	if err != nil {
		t.Fatalf("loadClaimArtifact: %v", err)
	}
	if durable == nil {
		t.Fatal("expected durable claim snapshot")
	}
	if durable.State != "released" {
		t.Fatalf("expected released durable state, got %q", durable.State)
	}
	if durable.ReleaseReason != "completed" {
		t.Fatalf("expected release reason completed, got %q", durable.ReleaseReason)
	}
	if durable.ReleasedAt.IsZero() {
		t.Fatal("expected released_at to be populated")
	}
	if durable.LeaseID != acquired.LeaseID {
		t.Fatalf("expected lease id to remain stable, got %q", durable.LeaseID)
	}
}

func TestReconcileClaim_LiveOnlyRebuildsDurableForSameRun(t *testing.T) {
	now := time.Now().UTC()
	live := &state.ClaimArtifact{
		ArtifactMeta: state.ArtifactMeta{
			SchemaVersion: 1,
			RunID:         "run-a",
			CreatedAt:     now,
			UpdatedAt:     now,
		},
		TicketID:   "ticket-1",
		OwnerRunID: "run-a",
		LeaseID:    "lease-a",
		LeasedAt:   now,
		ExpiresAt:  now.Add(10 * time.Minute),
		State:      "active",
	}

	got, err := ReconcileClaim(live, nil, "run-a", false)
	if err != nil {
		t.Fatalf("ReconcileClaim: %v", err)
	}
	if got.State != "active" {
		t.Fatalf("expected active state, got %q", got.State)
	}
	if got.LeaseID != live.LeaseID {
		t.Fatalf("expected lease %q, got %q", live.LeaseID, got.LeaseID)
	}
	if got.LastSeenLiveClaimPath != filepath.Join(".tickets", ".claims", "ticket-1.json") {
		t.Fatalf("unexpected last seen live claim path %q", got.LastSeenLiveClaimPath)
	}
}

func TestReconcileClaim_DurableReleasedWithoutLive(t *testing.T) {
	now := time.Now().UTC()
	durable := &state.ClaimArtifact{
		ArtifactMeta: state.ArtifactMeta{
			SchemaVersion: 1,
			RunID:         "run-a",
			CreatedAt:     now,
			UpdatedAt:     now,
		},
		TicketID:      "ticket-1",
		OwnerRunID:    "run-a",
		LeaseID:       "lease-a",
		LeasedAt:      now,
		ExpiresAt:     now.Add(10 * time.Minute),
		ReleasedAt:    now.Add(time.Minute),
		ReleaseReason: "completed",
		State:         "released",
	}

	got, err := ReconcileClaim(nil, durable, "run-a", false)
	if err != nil {
		t.Fatalf("ReconcileClaim: %v", err)
	}
	if got.State != "released" {
		t.Fatalf("expected released state, got %q", got.State)
	}
	if got.ReleaseReason != "completed" {
		t.Fatalf("expected release reason to be preserved, got %q", got.ReleaseReason)
	}
}

func TestReconcileClaim_MismatchedLeaseBlocks(t *testing.T) {
	now := time.Now().UTC()
	live := &state.ClaimArtifact{
		ArtifactMeta: state.ArtifactMeta{
			SchemaVersion: 1,
			RunID:         "run-a",
			CreatedAt:     now,
			UpdatedAt:     now,
		},
		TicketID:   "ticket-1",
		OwnerRunID: "run-a",
		LeaseID:    "lease-a",
		LeasedAt:   now,
		ExpiresAt:  now.Add(10 * time.Minute),
		State:      "active",
	}
	durable := &state.ClaimArtifact{
		ArtifactMeta: state.ArtifactMeta{
			SchemaVersion: 1,
			RunID:         "run-a",
			CreatedAt:     now,
			UpdatedAt:     now,
		},
		TicketID:   "ticket-1",
		OwnerRunID: "run-a",
		LeaseID:    "lease-b",
		LeasedAt:   now,
		ExpiresAt:  now.Add(10 * time.Minute),
		State:      "active",
	}

	if _, err := ReconcileClaim(live, durable, "run-a", false); err == nil {
		t.Fatal("expected mismatched lease reconciliation to fail")
	}
}

func TestValidateLeaseFence_RejectsLateResult(t *testing.T) {
	if err := ValidateLeaseFence("lease-current", "lease-old"); err == nil {
		t.Fatal("expected mismatched lease fence to fail")
	}
}
