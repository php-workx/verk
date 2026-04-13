package tkmd

import (
	"os"
	"path/filepath"
	"sync"
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

func TestAcquireClaim_ConcurrentOnlyOneWins(t *testing.T) {
	dir := t.TempDir()
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 2)

	acquire := func(runID string) {
		defer wg.Done()
		<-start
		_, err := AcquireClaim(dir, runID, "ticket-1", "lease-"+runID, 15*time.Minute)
		errs <- err
	}

	wg.Add(2)
	go acquire("run-a")
	go acquire("run-b")
	close(start)
	wg.Wait()
	close(errs)

	successes := 0
	failures := 0
	for err := range errs {
		if err != nil {
			failures++
			continue
		}
		successes++
	}
	if successes != 1 || failures != 1 {
		t.Fatalf("expected exactly one claim winner and one loser, got successes=%d failures=%d", successes, failures)
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

func TestAcquireClaim_RejectsPathTraversalIdentifiers(t *testing.T) {
	dir := t.TempDir()

	maliciousIDs := []struct {
		name     string
		runID    string
		ticketID string
	}{
		{"dotdot in runID", "../escape", "ticket-1"},
		{"dotdot in ticketID", "run-a", "../escape"},
		{"slash in runID", "run/evil", "ticket-1"},
		{"slash in ticketID", "run-a", "ticket/evil"},
		{"backslash in runID", "run\\evil", "ticket-1"},
		{"backslash in ticketID", "run-a", "ticket\\evil"},
		{"absolute runID", "/etc/passwd", "ticket-1"},
		{"absolute ticketID", "run-a", "/etc/passwd"},
		{"dotdot with slash in runID", "../../etc", "ticket-1"},
		{"dotdot with slash in ticketID", "run-a", "../../etc"},
		{"embedded dotdot in runID", "foo/../bar", "ticket-1"},
		{"embedded dotdot in ticketID", "run-a", "foo/../bar"},
		{"single dot runID", ".", "ticket-1"},
		{"single dot ticketID", "run-a", "."},
		{"double dot runID", "..", "ticket-1"},
		{"double dot ticketID", "run-a", ".."},
	}

	for _, tc := range maliciousIDs {
		t.Run(tc.name, func(t *testing.T) {
			_, err := AcquireClaim(dir, tc.runID, tc.ticketID, "lease-x", 10*time.Minute)
			if err == nil {
				t.Fatalf("expected claim to be rejected for %s", tc.name)
			}
		})
	}
}

func TestClaimPaths_PreservesValidIdentifiers(t *testing.T) {
	dir := t.TempDir()

	validIDs := []struct {
		name     string
		runID    string
		ticketID string
	}{
		{"simple", "run-a", "ticket-1"},
		{"with prefix", "run-ticket-1-1234567890", "ver-l3yx"},
		{"alphanumeric", "run123", "abc456"},
		{"with dots", "run-1.0", "ticket-1.0"},
		{"with underscores", "run_a", "ticket_1"},
		{"long id", "run-some-very-long-ticket-id-1234567890123456789", "some-very-long-ticket-id"},
	}

	for _, tc := range validIDs {
		t.Run(tc.name, func(t *testing.T) {
			livePath, durablePath, err := claimPaths(dir, tc.runID, tc.ticketID)
			if err != nil {
				t.Fatalf("expected valid ID pair to be accepted: %v", err)
			}
			if livePath == "" || durablePath == "" {
				t.Fatal("expected non-empty paths")
			}
		})
	}
}

func TestValidateLeaseFence_RejectsLateResult(t *testing.T) {
	if err := ValidateLeaseFence("lease-current", "lease-old"); err == nil {
		t.Fatal("expected mismatched lease fence to fail")
	}
}
