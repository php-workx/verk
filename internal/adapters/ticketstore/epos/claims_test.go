package epos

import (
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"verk/internal/state"

	eposticket "github.com/php-workx/epos/ticket"
	eposruntime "github.com/php-workx/epos/ticket/runtime"
)

func TestClaimPaths_RejectsSymlinkEscapingBase(t *testing.T) {
	dir := t.TempDir()
	verkDir := filepath.Join(dir, ".verk")
	outside := t.TempDir()

	if err := os.MkdirAll(verkDir, 0o755); err != nil {
		t.Fatalf("mkdir verk dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(outside, "run-a"), 0o755); err != nil {
		t.Fatalf("mkdir outside run dir: %v", err)
	}
	createSymlinkOrSkip(t, outside, filepath.Join(verkDir, "runs"))

	_, _, err := claimPaths(dir, "run-a", "ticket-1")
	if err == nil {
		t.Fatal("expected symlink escape to be rejected")
	}
	if !strings.Contains(err.Error(), "claim path escapes base directory") {
		t.Fatalf("expected escape error, got %v", err)
	}
}

func TestClaimPaths_PreservesValidDurablePath(t *testing.T) {
	dir := t.TempDir()

	livePath, durablePath, err := claimPaths(dir, "run-a", "ticket-1")
	if err != nil {
		t.Fatalf("claimPaths: %v", err)
	}

	wantLivePath := filepath.Join(filepath.Clean(dir), ".tickets", ".claims", "ticket-1.json")
	if livePath != wantLivePath {
		t.Fatalf("expected live path %q, got %q", wantLivePath, livePath)
	}
	wantDurablePath := filepath.Join(filepath.Clean(dir), ".verk", "runs", "run-a", "claims", "claim-ticket-1.json")
	if durablePath != wantDurablePath {
		t.Fatalf("expected durable path %q, got %q", wantDurablePath, durablePath)
	}
	if _, err := os.Stat(filepath.Dir(durablePath)); err != nil {
		t.Fatalf("expected durable claim dir to exist: %v", err)
	}
}

func TestValidateClaimIdentifier_RejectsTraversal(t *testing.T) {
	for _, id := range []string{"", ".", "..", "../run", "run/evil", "run\\evil", "/tmp/run", "run..evil"} {
		t.Run(id, func(t *testing.T) {
			if err := validateClaimIdentifier(id, "run_id"); err == nil {
				t.Fatal("expected identifier to be rejected")
			}
		})
	}
}

func TestAcquireClaim_PreservesCallerLeaseID(t *testing.T) {
	dir := t.TempDir()

	claim, err := AcquireClaim(dir, "run-a", "ticket-1", "lease-a", 15*time.Minute)
	if err != nil {
		t.Fatalf("AcquireClaim: %v", err)
	}
	if claim.LeaseID != "lease-a" {
		t.Fatalf("LeaseID = %q, want lease-a", claim.LeaseID)
	}
	if claim.OwnerRunID != "run-a" {
		t.Fatalf("OwnerRunID = %q, want run-a", claim.OwnerRunID)
	}
}

func TestAcquireClaim_GeneratesLeaseOnlyWhenMissing(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(0, 123).UTC()

	claim, err := AcquireClaim(dir, "run-a", "ticket-1", 15*time.Minute, now)
	if err != nil {
		t.Fatalf("AcquireClaim: %v", err)
	}
	want := "lease-run-a-ticket-1-123"
	if claim.LeaseID != want {
		t.Fatalf("LeaseID = %q, want %q", claim.LeaseID, want)
	}
}

func TestAcquireClaim_WritesLiveAndDurableLeaseID(t *testing.T) {
	dir := t.TempDir()

	claim, err := AcquireClaim(dir, "run-a", "ticket-1", "lease-a", "wave-1", 15*time.Minute)
	if err != nil {
		t.Fatalf("AcquireClaim: %v", err)
	}

	runtimeState, err := eposruntime.ReadRuntimeState(dir, "ticket-1")
	if err != nil {
		t.Fatalf("ReadRuntimeState: %v", err)
	}
	if runtimeState.Claim == nil || runtimeState.Lease == nil {
		t.Fatalf("expected active epos RuntimeState, got %#v", runtimeState)
	}
	if runtimeState.Lease.LeaseID != "lease-a" {
		t.Fatalf("live LeaseID = %q, want lease-a", runtimeState.Lease.LeaseID)
	}

	_, durablePath, err := claimPaths(dir, "run-a", "ticket-1")
	if err != nil {
		t.Fatalf("claimPaths: %v", err)
	}
	durable, err := loadClaimArtifact(durablePath)
	if err != nil {
		t.Fatalf("loadClaimArtifact: %v", err)
	}
	if durable == nil {
		t.Fatal("expected durable claim")
	}
	if durable.LeaseID != "lease-a" {
		t.Fatalf("durable LeaseID = %q, want lease-a", durable.LeaseID)
	}
	if durable.ExpiresAt != runtimeState.Lease.ExpiresAt {
		t.Fatalf("durable ExpiresAt = %s, want live %s", durable.ExpiresAt, runtimeState.Lease.ExpiresAt)
	}
	if durable.OwnerWaveID != "wave-1" {
		t.Fatalf("OwnerWaveID = %q, want wave-1", durable.OwnerWaveID)
	}
	if durable.LastSeenLiveClaimPath != claim.LastSeenLiveClaimPath {
		t.Fatalf("durable LastSeenLiveClaimPath = %q, want %q", durable.LastSeenLiveClaimPath, claim.LastSeenLiveClaimPath)
	}
}

func TestAcquireClaim_RollbackUsesPendingStatusOnDurableWriteFailure(t *testing.T) {
	dir := t.TempDir()
	originalSaveAtomic := saveAtomic
	saveAtomic = func(string, any) error {
		return errors.New("durable write failed")
	}
	t.Cleanup(func() {
		saveAtomic = originalSaveAtomic
	})

	_, err := AcquireClaim(dir, "run-a", "ticket-1", "lease-a", 10*time.Minute)
	if err == nil {
		t.Fatal("expected durable write failure")
	}

	runtimeState, readErr := eposruntime.ReadRuntimeState(dir, "ticket-1")
	if readErr != nil {
		t.Fatalf("ReadRuntimeState: %v", readErr)
	}
	if runtimeState.Claim != nil {
		t.Fatalf("expected rollback to release active claim, got %#v", runtimeState.Claim)
	}
	if runtimeState.Lease != nil {
		t.Fatalf("expected rollback to release active lease, got %#v", runtimeState.Lease)
	}
	if runtimeState.Status != eposticket.StatusPending {
		t.Fatalf("runtime Status = %q, want pending", runtimeState.Status)
	}
}

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

	_, durablePath, err := claimPaths(dir, "run-a", "ticket-1")
	if err != nil {
		t.Fatalf("claimPaths: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".tickets", ".claims", "ticket-1.json")); err != nil {
		t.Fatalf("expected live runtime state file: %v", err)
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

func TestAcquireClaim_ReclaimsExpiredLiveClaimFromOtherRun(t *testing.T) {
	dir := t.TempDir()
	if err := eposruntime.Claim(dir, "ticket-1", "run-old", "run-old", time.Hour, eposruntime.WithLeaseID("lease-old")); err != nil {
		t.Fatalf("seed old claim: %v", err)
	}
	expired := time.Now().UTC().Add(-time.Minute)
	if err := eposruntime.WriteRuntimeState(dir, &eposticket.RuntimeState{
		TicketID: "ticket-1",
		Status:   eposticket.StatusPending,
		Claim: &eposticket.Claim{
			ClaimedBy:    "run-old",
			ClaimBackend: "run-old",
			ClaimedAt:    expired.Add(-time.Hour),
		},
		Lease: &eposticket.Lease{
			LeaseID:   "lease-old",
			ExpiresAt: expired,
		},
	}); err != nil {
		t.Fatalf("write expired runtime state: %v", err)
	}

	claim, err := AcquireClaim(dir, "run-new", "ticket-1", "lease-new", 10*time.Minute)
	if err != nil {
		t.Fatalf("AcquireClaim reclaim expired: %v", err)
	}
	if claim.OwnerRunID != "run-new" || claim.LeaseID != "lease-new" {
		t.Fatalf("claim = %#v, want run-new/lease-new", claim)
	}
	runtimeState, err := eposruntime.ReadRuntimeState(dir, "ticket-1")
	if err != nil {
		t.Fatalf("ReadRuntimeState: %v", err)
	}
	if runtimeState.Claim == nil || runtimeState.Claim.ClaimedBy != "run-new" {
		t.Fatalf("runtime claim = %#v, want run-new", runtimeState.Claim)
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

func TestRenewClaim_RejectsExpiredLease(t *testing.T) {
	dir := t.TempDir()
	acquired, err := AcquireClaim(dir, "run-a", "ticket-1", "lease-a", 10*time.Minute)
	if err != nil {
		t.Fatalf("AcquireClaim: %v", err)
	}
	expired := time.Now().UTC().Add(-time.Minute)
	if err := eposruntime.WriteRuntimeState(dir, &eposticket.RuntimeState{
		TicketID: "ticket-1",
		Status:   eposticket.StatusClaimed,
		Claim: &eposticket.Claim{
			ClaimedBy:    "run-a",
			ClaimBackend: "run-a",
			ClaimedAt:    acquired.LeasedAt,
		},
		Lease: &eposticket.Lease{
			LeaseID:   acquired.LeaseID,
			ExpiresAt: expired,
		},
	}); err != nil {
		t.Fatalf("write expired runtime state: %v", err)
	}

	if _, err := RenewClaim(dir, "run-a", "ticket-1", acquired.LeaseID, 20*time.Minute, time.Now().UTC()); err == nil {
		t.Fatal("expected expired lease renewal to fail")
	}
}

func TestRenewClaim_PreservesLiveLeaseID(t *testing.T) {
	dir := t.TempDir()

	acquired, err := AcquireClaim(dir, "run-a", "ticket-1", "lease-a", "wave-1", 10*time.Minute)
	if err != nil {
		t.Fatalf("AcquireClaim: %v", err)
	}
	renewed, err := RenewClaim(dir, "run-a", "ticket-1", acquired.LeaseID, 20*time.Minute, acquired)
	if err != nil {
		t.Fatalf("RenewClaim: %v", err)
	}
	if renewed.LeaseID != acquired.LeaseID {
		t.Fatalf("renewed LeaseID = %q, want %q", renewed.LeaseID, acquired.LeaseID)
	}
	if renewed.OwnerWaveID != "wave-1" {
		t.Fatalf("OwnerWaveID = %q, want wave-1", renewed.OwnerWaveID)
	}

	live, err := eposruntime.ReadRuntimeState(dir, "ticket-1")
	if err != nil {
		t.Fatalf("ReadRuntimeState: %v", err)
	}
	if live.Lease == nil || live.Lease.LeaseID != acquired.LeaseID {
		t.Fatalf("live LeaseID = %#v, want %q", live.Lease, acquired.LeaseID)
	}
}

func TestRenewClaim_DurableOnlyRecoversLiveRuntimeState(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	writeDurableClaim(t, dir, "run-a", state.ClaimArtifact{
		TicketID:   "ticket-1",
		OwnerRunID: "run-a",
		LeaseID:    "fence-durable",
		LeasedAt:   now.Add(-time.Minute),
		ExpiresAt:  now.Add(10 * time.Minute),
		State:      "active",
	})

	renewed, err := RenewClaim(dir, "run-a", "ticket-1", "fence-durable", 20*time.Minute)
	if err != nil {
		t.Fatalf("RenewClaim durable-only: %v", err)
	}
	if renewed.LeaseID != "fence-durable" {
		t.Fatalf("renewed LeaseID = %q, want fence-durable", renewed.LeaseID)
	}

	runtimeState, err := eposruntime.ReadRuntimeState(dir, "ticket-1")
	if err != nil {
		t.Fatalf("ReadRuntimeState: %v", err)
	}
	if runtimeState.Claim == nil || runtimeState.Lease == nil {
		t.Fatalf("expected live runtime state to be recovered from durable claim, got %#v", runtimeState)
	}
	if runtimeState.Claim.ClaimedBy != "run-a" || runtimeState.Lease.LeaseID != "fence-durable" {
		t.Fatalf("recovered runtime state = %#v", runtimeState)
	}
}

func TestConcurrent_AcquireAndRenew_NoRace(t *testing.T) {
	dir := t.TempDir()
	acquired, err := AcquireClaim(dir, "run-a", "ticket-1", "lease-a", 10*time.Minute)
	if err != nil {
		t.Fatalf("AcquireClaim: %v", err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, err := RenewClaim(dir, "run-a", "ticket-1", acquired.LeaseID, 20*time.Minute, acquired)
		errs <- err
	}()
	go func() {
		defer wg.Done()
		<-start
		_, err := AcquireClaim(dir, "run-b", "ticket-1", "lease-b", 20*time.Minute)
		errs <- err
	}()
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
		t.Fatalf("expected renew winner and acquire loser, got successes=%d failures=%d", successes, failures)
	}
}

func TestReleaseClaim_PersistsReleasedDurableAndKeepsRuntimeState(t *testing.T) {
	dir := t.TempDir()

	acquired, err := AcquireClaim(dir, "run-a", "ticket-1", "lease-a", "wave-1", 10*time.Minute)
	if err != nil {
		t.Fatalf("AcquireClaim: %v", err)
	}
	if err := ReleaseClaim(dir, "run-a", "ticket-1", acquired.LeaseID, "completed"); err != nil {
		t.Fatalf("ReleaseClaim: %v", err)
	}

	livePath := filepath.Join(dir, ".tickets", ".claims", "ticket-1.json")
	if _, err := os.Stat(livePath); err != nil {
		t.Fatalf("expected epos runtime state sidecar to remain: %v", err)
	}
	runtimeState, err := eposruntime.ReadRuntimeState(dir, "ticket-1")
	if err != nil {
		t.Fatalf("ReadRuntimeState: %v", err)
	}
	if runtimeState.Claim != nil {
		t.Fatalf("expected released runtime state to have no active claim, got %#v", runtimeState.Claim)
	}
	if runtimeState.Lease != nil {
		t.Fatalf("expected released runtime state to have no active lease, got %#v", runtimeState.Lease)
	}
	if runtimeState.Status != eposticket.StatusPending {
		t.Fatalf("runtime Status = %q, want pending", runtimeState.Status)
	}

	_, durablePath, err := claimPaths(dir, "run-a", "ticket-1")
	if err != nil {
		t.Fatalf("claimPaths: %v", err)
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

func TestReleaseClaim_RejectsMismatchedLeaseBeforeRuntimeMutation(t *testing.T) {
	dir := t.TempDir()

	if _, err := AcquireClaim(dir, "run-a", "ticket-1", "lease-a", 10*time.Minute); err != nil {
		t.Fatalf("AcquireClaim: %v", err)
	}
	if err := ReleaseClaim(dir, "run-a", "ticket-1", "lease-stale", "completed"); err == nil {
		t.Fatal("expected stale lease release to fail")
	}

	runtimeState, err := eposruntime.ReadRuntimeState(dir, "ticket-1")
	if err != nil {
		t.Fatalf("ReadRuntimeState: %v", err)
	}
	if runtimeState.Claim == nil {
		t.Fatal("expected runtime state to remain actively claimed")
	}
	if runtimeState.Lease == nil || runtimeState.Lease.LeaseID != "lease-a" {
		t.Fatalf("runtime lease = %#v, want lease-a", runtimeState.Lease)
	}
}

func TestReleaseClaim_RejectsCustomLeaseFenceMismatch(t *testing.T) {
	dir := t.TempDir()

	if _, err := AcquireClaim(dir, "run-a", "ticket-1", "fence-current", 10*time.Minute); err != nil {
		t.Fatalf("AcquireClaim: %v", err)
	}
	if err := ReleaseClaim(dir, "run-a", "ticket-1", "fence-stale"); err == nil {
		t.Fatal("expected custom fence mismatch to fail")
	}

	runtimeState, err := eposruntime.ReadRuntimeState(dir, "ticket-1")
	if err != nil {
		t.Fatalf("ReadRuntimeState: %v", err)
	}
	if runtimeState.Claim == nil {
		t.Fatal("expected runtime state to remain actively claimed")
	}
	if runtimeState.Lease == nil || runtimeState.Lease.LeaseID != "fence-current" {
		t.Fatalf("runtime lease = %#v, want fence-current", runtimeState.Lease)
	}
}

func TestReleaseClaim_ReasonOnlyCallStillWorks(t *testing.T) {
	dir := t.TempDir()

	if _, err := AcquireClaim(dir, "run-a", "ticket-1", "lease-a", 10*time.Minute); err != nil {
		t.Fatalf("AcquireClaim: %v", err)
	}
	if err := ReleaseClaim(dir, "run-a", "ticket-1", "resume_reacquisition"); err != nil {
		t.Fatalf("ReleaseClaim reason-only: %v", err)
	}

	_, durablePath, err := claimPaths(dir, "run-a", "ticket-1")
	if err != nil {
		t.Fatalf("claimPaths: %v", err)
	}
	durable, err := loadClaimArtifact(durablePath)
	if err != nil {
		t.Fatalf("loadClaimArtifact: %v", err)
	}
	if durable == nil || durable.State != "released" {
		t.Fatalf("expected released durable claim, got %#v", durable)
	}
	if durable.ReleaseReason != "resume_reacquisition" {
		t.Fatalf("ReleaseReason = %q, want resume_reacquisition", durable.ReleaseReason)
	}
}

func TestReleaseClaim_DurableOnlyWritesReleasedRuntimeState(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	writeDurableClaim(t, dir, "run-a", state.ClaimArtifact{
		TicketID:   "ticket-1",
		OwnerRunID: "run-a",
		LeaseID:    "fence-durable",
		LeasedAt:   now.Add(-time.Minute),
		ExpiresAt:  now.Add(10 * time.Minute),
		State:      "active",
	})

	if err := ReleaseClaim(dir, "run-a", "ticket-1", "fence-durable", "completed"); err != nil {
		t.Fatalf("ReleaseClaim durable-only: %v", err)
	}

	runtimeState, err := eposruntime.ReadRuntimeState(dir, "ticket-1")
	if err != nil {
		t.Fatalf("ReadRuntimeState: %v", err)
	}
	if runtimeState.Claim != nil || runtimeState.Lease != nil || runtimeState.Status != eposticket.StatusPending {
		t.Fatalf("expected released runtime state from durable-only release, got %#v", runtimeState)
	}

	_, durablePath, err := claimPaths(dir, "run-a", "ticket-1")
	if err != nil {
		t.Fatalf("claimPaths: %v", err)
	}
	durable, err := loadClaimArtifact(durablePath)
	if err != nil {
		t.Fatalf("loadClaimArtifact: %v", err)
	}
	if durable == nil || durable.State != "released" || durable.ReleaseReason != "completed" {
		t.Fatalf("expected released durable claim, got %#v", durable)
	}
}

func TestReleaseClaim_DurableOnlyDoesNotOverwriteConcurrentLiveClaim(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	writeDurableClaim(t, dir, "run-a", state.ClaimArtifact{
		TicketID:   "ticket-1",
		OwnerRunID: "run-a",
		LeaseID:    "fence-durable",
		LeasedAt:   now.Add(-time.Minute),
		ExpiresAt:  now.Add(10 * time.Minute),
		State:      "active",
	})
	if err := eposruntime.Claim(dir, "ticket-1", "run-b", "run-b", time.Hour, eposruntime.WithLeaseID("lease-b")); err != nil {
		t.Fatalf("seed concurrent live claim: %v", err)
	}

	if err := ReleaseClaim(dir, "run-a", "ticket-1", "fence-durable", "completed"); err == nil {
		t.Fatal("expected durable-only release to reject concurrent live claim")
	}

	runtimeState, err := eposruntime.ReadRuntimeState(dir, "ticket-1")
	if err != nil {
		t.Fatalf("ReadRuntimeState: %v", err)
	}
	if runtimeState.Claim == nil || runtimeState.Claim.ClaimedBy != "run-b" {
		t.Fatalf("expected concurrent live claim to remain, got %#v", runtimeState)
	}
	if runtimeState.Lease == nil || runtimeState.Lease.LeaseID != "lease-b" {
		t.Fatalf("expected concurrent live lease to remain, got %#v", runtimeState.Lease)
	}
}

func TestReleaseClaim_DurableOnlyDoesNotOverwriteSameRunNewLease(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	writeDurableClaim(t, dir, "run-a", state.ClaimArtifact{
		TicketID:   "ticket-1",
		OwnerRunID: "run-a",
		LeaseID:    "fence-durable",
		LeasedAt:   now.Add(-time.Minute),
		ExpiresAt:  now.Add(10 * time.Minute),
		State:      "active",
	})
	if err := eposruntime.Claim(dir, "ticket-1", "run-a", "run-a", time.Hour, eposruntime.WithLeaseID("lease-newer")); err != nil {
		t.Fatalf("seed same-run live claim: %v", err)
	}

	if err := ReleaseClaim(dir, "run-a", "ticket-1", "fence-durable", "completed"); err == nil {
		t.Fatal("expected durable-only release to reject same-run live claim with a newer lease")
	}

	runtimeState, err := eposruntime.ReadRuntimeState(dir, "ticket-1")
	if err != nil {
		t.Fatalf("ReadRuntimeState: %v", err)
	}
	if runtimeState.Claim == nil || runtimeState.Claim.ClaimedBy != "run-a" {
		t.Fatalf("expected same-run live claim to remain, got %#v", runtimeState)
	}
	if runtimeState.Lease == nil || runtimeState.Lease.LeaseID != "lease-newer" {
		t.Fatalf("expected same-run live lease to remain, got %#v", runtimeState.Lease)
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
	if err := ValidateLeaseFence("lease-current", "lease-late"); err == nil {
		t.Fatal("expected late lease result to be rejected")
	}
	if err := ValidateLeaseFence("", "lease-current"); err == nil {
		t.Fatal("expected empty expected lease to be rejected")
	}
	if err := ValidateLeaseFence("lease-current", "lease-current"); err != nil {
		t.Fatalf("expected matching lease fence, got %v", err)
	}
}

func TestLoadLiveClaim_ConvertsActiveRuntimeState(t *testing.T) {
	dir := t.TempDir()
	if err := eposruntime.Claim(dir, "ticket-1", "run-1", "backend-1", time.Hour, eposruntime.WithLeaseID("lease-1")); err != nil {
		t.Fatalf("claim: %v", err)
	}

	claim, err := LoadLiveClaim(dir, "ticket-1")
	if err != nil {
		t.Fatalf("LoadLiveClaim: %v", err)
	}
	if claim == nil {
		t.Fatal("expected active live claim")
	}
	if claim.TicketID != "ticket-1" {
		t.Fatalf("TicketID = %q, want ticket-1", claim.TicketID)
	}
	if claim.OwnerRunID != "run-1" {
		t.Fatalf("OwnerRunID = %q, want run-1", claim.OwnerRunID)
	}
	if claim.LeaseID != "lease-1" {
		t.Fatalf("LeaseID = %q, want lease-1", claim.LeaseID)
	}
	if claim.State != "active" {
		t.Fatalf("State = %q, want active", claim.State)
	}
	if claim.LeasedAt.IsZero() {
		t.Fatal("expected LeasedAt to be set")
	}
	if !claim.ExpiresAt.After(claim.LeasedAt) {
		t.Fatalf("ExpiresAt = %s, LeasedAt = %s", claim.ExpiresAt, claim.LeasedAt)
	}
	if claim.LastSeenLiveClaimPath != path.Join(".tickets", ".claims", "ticket-1.json") {
		t.Fatalf("LastSeenLiveClaimPath = %q", claim.LastSeenLiveClaimPath)
	}
}

func TestLoadLiveClaim_ReturnsNilForMissingOrReleased(t *testing.T) {
	dir := t.TempDir()

	missing, err := LoadLiveClaim(dir, "ticket-1")
	if err != nil {
		t.Fatalf("LoadLiveClaim missing: %v", err)
	}
	if missing != nil {
		t.Fatalf("expected nil missing claim, got %#v", missing)
	}

	if err := eposruntime.Claim(dir, "ticket-1", "run-1", "backend-1", time.Hour, eposruntime.WithLeaseID("lease-1")); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := eposruntime.Release(dir, "ticket-1", "run-1", eposticket.StatusPending, "released"); err != nil {
		t.Fatalf("release: %v", err)
	}

	released, err := LoadLiveClaim(dir, "ticket-1")
	if err != nil {
		t.Fatalf("LoadLiveClaim released: %v", err)
	}
	if released != nil {
		t.Fatalf("expected nil released claim, got %#v", released)
	}
}

func createSymlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
}
