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

func TestRenewClaim_RejectsPathTraversalIdentifiers(t *testing.T) {
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
			_, err := RenewClaim(dir, tc.runID, tc.ticketID, "lease-x", 10*time.Minute)
			if err == nil {
				t.Fatalf("expected renew to be rejected for %s", tc.name)
			}
		})
	}
}

func TestReleaseClaim_RejectsPathTraversalIdentifiers(t *testing.T) {
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
			err := ReleaseClaim(dir, tc.runID, tc.ticketID, "lease-x", "test")
			if err == nil {
				t.Fatalf("expected release to be rejected for %s", tc.name)
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
			liveRoot := filepath.Join(filepath.Clean(dir), ".tickets", ".claims")
			durableRoot := filepath.Join(filepath.Clean(dir), ".verk", "runs", tc.runID, "claims")
			assertPathWithin(t, livePath, liveRoot)
			assertPathWithin(t, durablePath, durableRoot)
		})
	}
}

func TestClaimPaths_RejectsSymlinkEscapingBase(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, dir string)
	}{
		{
			name: "live claim dir symlink",
			setup: func(t *testing.T, dir string) {
				t.Helper()
				ticketsDir := filepath.Join(dir, ".tickets")
				outside := filepath.Join(dir, "outside-live")
				if err := os.MkdirAll(ticketsDir, 0o755); err != nil {
					t.Fatalf("mkdir tickets dir: %v", err)
				}
				if err := os.MkdirAll(outside, 0o755); err != nil {
					t.Fatalf("mkdir outside dir: %v", err)
				}
				if err := os.Symlink(outside, filepath.Join(ticketsDir, ".claims")); err != nil {
					t.Skipf("symlink not supported: %v", err)
				}
			},
		},
		{
			name: "durable claim dir symlink",
			setup: func(t *testing.T, dir string) {
				t.Helper()
				durableParent := filepath.Join(dir, ".verk", "runs", "run-a")
				outside := filepath.Join(dir, "outside-durable")
				if err := os.MkdirAll(durableParent, 0o755); err != nil {
					t.Fatalf("mkdir durable parent: %v", err)
				}
				if err := os.MkdirAll(outside, 0o755); err != nil {
					t.Fatalf("mkdir outside dir: %v", err)
				}
				if err := os.Symlink(outside, filepath.Join(durableParent, "claims")); err != nil {
					t.Skipf("symlink not supported: %v", err)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.setup(t, dir)
			_, _, err := claimPaths(dir, "run-a", "ticket-1")
			if err == nil {
				t.Fatal("expected symlink escape to be rejected")
			}
			if !strings.Contains(err.Error(), "claim path escapes base directory") {
				t.Fatalf("expected escape error, got %v", err)
			}
		})
	}
}

func assertPathWithin(t *testing.T, child, parent string) {
	t.Helper()
	rel, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(child))
	if err != nil {
		t.Fatalf("resolve relative path from %q to %q: %v", parent, child, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatalf("path %q is not within base %q (rel=%q)", child, parent, rel)
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
	livePath, _, err := claimPaths(dir, "run-a", "ticket-1")
	if err != nil {
		t.Fatalf("claimPaths: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(livePath), 0o755); err != nil {
		t.Fatalf("prepare claim lock directory: %v", err)
	}

	start := make(chan struct{})
	renewErr := make(chan error, 1)
	acquireErr := make(chan error, 1)

	var wg sync.WaitGroup
	wg.Add(2)

	// Goroutine 1: attempt to renew the claim for run-a.
	go func() {
		defer wg.Done()
		<-start
		_, err := RenewClaim(dir, "run-a", "ticket-1", "lease-a", 30*time.Minute)
		renewErr <- err
	}()

	// Goroutine 2: attempt to acquire the same ticket for run-a.
	// One of two serial outcomes is valid:
	//   - acquire then renew: both succeed
	//   - renew then acquire: acquire succeeds, renew fails as not-found
	go func() {
		defer wg.Done()
		<-start
		_, err := AcquireClaim(dir, "run-a", "ticket-1", "lease-a", 30*time.Minute)
		acquireErr <- err
	}()

	close(start)
	wg.Wait()

	reqAcquireErr := <-acquireErr
	reqRenewErr := <-renewErr

	_, durablePath, err := claimPaths(dir, "run-a", "ticket-1")
	if err != nil {
		t.Fatalf("claimPaths: %v", err)
	}
	live, err := loadClaimArtifact(livePath)
	if err != nil {
		t.Fatalf("load live claim: %v", err)
	}
	durable, err := loadClaimArtifact(durablePath)
	if err != nil {
		t.Fatalf("load durable claim: %v", err)
	}

	if reqAcquireErr != nil {
		t.Fatalf("Acquire should never lose when both operations race from no-claim state: %v", reqAcquireErr)
	}

	switch {
	case reqRenewErr == nil:
		if live == nil || durable == nil {
			t.Fatal("expected both claim files to exist after acquire-then-renew")
		}
		if live.State != "active" || durable.State != "active" {
			t.Fatalf("expected active state in both files after acquire-then-renew, got live=%q durable=%q", live.State, durable.State)
		}
		if live.LeaseID != "lease-a" || durable.LeaseID != "lease-a" {
			t.Fatalf("expected lease-id to remain lease-a, got live=%q durable=%q", live.LeaseID, durable.LeaseID)
		}
	case reqRenewErr != nil && strings.Contains(reqRenewErr.Error(), "not found for renewal"):
		if live == nil || durable == nil {
			t.Fatal("expected active claim file for successful acquire")
		}
		if live.State != "active" || durable.State != "active" {
			t.Fatalf("expected both claim files to be active after renew-before-acquire, got live=%q durable=%q", live.State, durable.State)
		}
	default:
		t.Fatalf("expected acquire-then-renew or renew-then-acquire, got acquireErr=%v renewErr=%v", reqAcquireErr, reqRenewErr)
	}

	if reqRenewErr != nil && !strings.Contains(reqRenewErr.Error(), "not found for renewal") {
		t.Fatalf("unexpected RenewClaim failure: %v", reqRenewErr)
	}
}

// TestConcurrent_AcquireAndRelease_NoRace verifies that a concurrent
// AcquireClaim and ReleaseClaim on the same ticket produce a consistent outcome
// and do not trigger the race detector.
func TestConcurrent_AcquireAndRelease_NoRace(t *testing.T) {
	dir := t.TempDir()
	livePath, _, err := claimPaths(dir, "run-a", "ticket-1")
	if err != nil {
		t.Fatalf("claimPaths: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(livePath), 0o755); err != nil {
		t.Fatalf("prepare claim lock directory: %v", err)
	}

	start := make(chan struct{})
	releaseErr := make(chan error, 1)
	acquireErr := make(chan error, 1)

	var wg sync.WaitGroup
	wg.Add(2)

	// Goroutine 1: attempt to release the claim for run-a.
	go func() {
		defer wg.Done()
		<-start
		releaseErr <- ReleaseClaim(dir, "run-a", "ticket-1", "lease-a", "completed")
	}()

	// Goroutine 2: attempt to acquire the same ticket for run-a.
	// Valid serial outcomes are:
	//   - acquire then release: both succeed, durable is released and live is gone
	//   - release then acquire: acquire succeeds, release fails with not-found
	go func() {
		defer wg.Done()
		<-start
		_, err := AcquireClaim(dir, "run-a", "ticket-1", "lease-a", 30*time.Minute)
		acquireErr <- err
	}()

	close(start)
	wg.Wait()

	rErr := <-releaseErr
	aErr := <-acquireErr

	_, durablePath, err := claimPaths(dir, "run-a", "ticket-1")
	if err != nil {
		t.Fatalf("claimPaths: %v", err)
	}
	live, err := loadClaimArtifact(livePath)
	if err != nil {
		t.Fatalf("load live claim: %v", err)
	}
	durable, err := loadClaimArtifact(durablePath)
	if err != nil {
		t.Fatalf("load durable claim: %v", err)
	}

	if aErr != nil {
		t.Fatalf("Acquire should succeed in both serial orders: %v", aErr)
	}

	if rErr == nil {
		if durable == nil {
			t.Fatal("expected durable claim after successful release")
		}
		if durable.State != "released" {
			t.Fatalf("expected released durable state, got %q", durable.State)
		}
		if durable.ReleaseReason != "completed" {
			t.Fatalf("expected release reason completed, got %q", durable.ReleaseReason)
		}
		if live != nil {
			t.Fatalf("expected live claim removed after successful release")
		}
		return
	}

	if !strings.Contains(rErr.Error(), "not found for release") {
		t.Fatalf("expected release to report not-found when raced first, got: %v", rErr)
	}
	if live == nil || durable == nil {
		t.Fatal("expected both claim files after acquire")
	}
	if live.State != "active" {
		t.Fatalf("expected active live claim when acquire precedes release, got %q", live.State)
	}
	if durable.State != "active" {
		t.Fatalf("expected active durable claim when acquire precedes release, got %q", durable.State)
	}
	if durable.LeaseID != "lease-a" {
		t.Fatalf("expected durable lease-a, got %q", durable.LeaseID)
	}
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
