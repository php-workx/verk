package tkmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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

// TestRenewClaim_DurableWriteFailure_LiveRestored verifies that when the
// durable write fails, RenewClaim restores the live claim to its pre-renewal
// state so that live and durable remain in sync.
func TestRenewClaim_DurableWriteFailure_LiveRestored(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission-based test cannot run as root")
	}

	dir := t.TempDir()

	acquired, err := AcquireClaim(dir, "run-a", "ticket-1", "lease-a", 10*time.Minute)
	if err != nil {
		t.Fatalf("AcquireClaim: %v", err)
	}

	livePath, durablePath, err := claimPaths(dir, "run-a", "ticket-1")
	if err != nil {
		t.Fatalf("claimPaths: %v", err)
	}

	// Make the durable parent directory read-only so writes fail but reads
	// (and the pre-existing durable file) are still accessible.
	durableParent := filepath.Dir(durablePath)
	if err := os.Chmod(durableParent, 0o555); err != nil {
		t.Fatalf("chmod durable parent: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(durableParent, 0o755) // restore so t.TempDir cleanup can remove files
	})

	_, renewErr := RenewClaim(dir, "run-a", "ticket-1", acquired.LeaseID, 20*time.Minute)
	if renewErr == nil {
		t.Fatal("expected RenewClaim to return an error")
	}
	if !strings.Contains(renewErr.Error(), "live restored") {
		t.Errorf("expected error to mention 'live restored', got: %v", renewErr)
	}

	// Live claim on disk must equal the pre-renewal (acquired) state.
	restoredLive, err := loadClaimArtifact(livePath)
	if err != nil {
		t.Fatalf("loadClaimArtifact after failed renewal: %v", err)
	}
	if restoredLive == nil {
		t.Fatal("expected live claim file to exist after restore")
	}
	if !restoredLive.ExpiresAt.Equal(acquired.ExpiresAt) {
		t.Errorf("live claim ExpiresAt mismatch: got %v, want %v (pre-renewal)", restoredLive.ExpiresAt, acquired.ExpiresAt)
	}
	if restoredLive.LeaseID != acquired.LeaseID {
		t.Errorf("live claim LeaseID mismatch: got %q, want %q", restoredLive.LeaseID, acquired.LeaseID)
	}
}

// TestConcurrent_AcquireAndRenew_NoRace verifies that a concurrent AcquireClaim
// and RenewClaim on the same ticket produce a consistent outcome (one serialises
// before the other) and do not trigger the race detector.
func TestConcurrent_AcquireAndRenew_NoRace(t *testing.T) {
	dir := t.TempDir()

	acquired, err := AcquireClaim(dir, "run-a", "ticket-1", "lease-a", 30*time.Minute)
	if err != nil {
		t.Fatalf("AcquireClaim setup: %v", err)
	}

	start := make(chan struct{})
	renewErr := make(chan error, 1)
	acquireErr := make(chan error, 1)

	var wg sync.WaitGroup
	wg.Add(2)

	// Goroutine 1: renew the existing claim held by run-a.
	go func() {
		defer wg.Done()
		<-start
		_, err := RenewClaim(dir, "run-a", "ticket-1", acquired.LeaseID, 30*time.Minute)
		renewErr <- err
	}()

	// Goroutine 2: attempt to acquire the same ticket for run-b.
	// run-a still holds an active claim, so this must fail.
	go func() {
		defer wg.Done()
		<-start
		_, err := AcquireClaim(dir, "run-b", "ticket-1", "lease-b", 30*time.Minute)
		acquireErr <- err
	}()

	close(start)
	wg.Wait()

	// Renew must succeed; acquire must fail because run-a still holds the claim.
	if err := <-renewErr; err != nil {
		t.Errorf("RenewClaim failed unexpectedly: %v", err)
	}
	if err := <-acquireErr; err == nil {
		t.Error("AcquireClaim by run-b succeeded while run-a still holds claim")
	}
}

// TestConcurrent_AcquireAndRelease_NoRace verifies that a concurrent
// AcquireClaim and ReleaseClaim on the same ticket produce a consistent outcome
// and do not trigger the race detector.
func TestConcurrent_AcquireAndRelease_NoRace(t *testing.T) {
	dir := t.TempDir()

	acquired, err := AcquireClaim(dir, "run-a", "ticket-1", "lease-a", 30*time.Minute)
	if err != nil {
		t.Fatalf("AcquireClaim setup: %v", err)
	}

	start := make(chan struct{})
	releaseErr := make(chan error, 1)
	acquireErr := make(chan error, 1)

	var wg sync.WaitGroup
	wg.Add(2)

	// Goroutine 1: release the claim held by run-a.
	go func() {
		defer wg.Done()
		<-start
		releaseErr <- ReleaseClaim(dir, "run-a", "ticket-1", acquired.LeaseID, "completed")
	}()

	// Goroutine 2: attempt to acquire the same ticket for run-b.
	go func() {
		defer wg.Done()
		<-start
		_, err := AcquireClaim(dir, "run-b", "ticket-1", "lease-b", 30*time.Minute)
		acquireErr <- err
	}()

	close(start)
	wg.Wait()

	rErr := <-releaseErr
	aErr := <-acquireErr

	// Valid outcomes (one serialises before the other):
	//   - Release wins: rErr==nil; acquire sees a free slot: aErr==nil.
	//   - Acquire-check wins: aErr!=nil (active claim blocks it); release still succeeds: rErr==nil.
	// The only invalid outcome is both failing simultaneously.
	if rErr != nil {
		t.Errorf("ReleaseClaim failed unexpectedly: %v", rErr)
	}
	// aErr is allowed to be non-nil when the lock ordering puts the acquire
	// check before the release completes.
	_ = aErr
}

// TestRenewClaim_DurableAndRestoreFailure_ErrorMentionsBoth verifies that when
// both the durable write and the compensating live-restore fail, the returned
// error mentions both failure causes so operators know the state is fully
// wedged.
func TestRenewClaim_DurableAndRestoreFailure_ErrorMentionsBoth(t *testing.T) {
	dir := t.TempDir()

	acquired, err := AcquireClaim(dir, "run-a", "ticket-1", "lease-a", 10*time.Minute)
	if err != nil {
		t.Fatalf("AcquireClaim: %v", err)
	}

	origSave := saveAtomic
	t.Cleanup(func() { saveAtomic = origSave })

	callN := 0
	saveAtomic = func(path string, v any) error {
		callN++
		switch callN {
		case 1: // initial live write: succeed
			return state.SaveJSONAtomic(path, v)
		case 2: // durable write: fail
			return errors.New("simulated durable write failure")
		default: // restore attempt: fail
			return errors.New("simulated restore failure")
		}
	}

	_, renewErr := RenewClaim(dir, "run-a", "ticket-1", acquired.LeaseID, 20*time.Minute)
	if renewErr == nil {
		t.Fatal("expected RenewClaim to return an error")
	}
	msg := renewErr.Error()
	if !strings.Contains(msg, "durable") {
		t.Errorf("expected error to mention durable failure, got: %s", msg)
	}
	if !strings.Contains(msg, "restore") {
		t.Errorf("expected error to mention restore failure, got: %s", msg)
	}
}
