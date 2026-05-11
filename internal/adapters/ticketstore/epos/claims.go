package epos

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	"verk/internal/state"

	"github.com/gofrs/flock"
	eposticket "github.com/php-workx/epos/ticket"
	eposruntime "github.com/php-workx/epos/ticket/runtime"
)

const (
	claimSchemaVersion = 1
	defaultClaimTTL    = 30 * time.Minute
)

// saveAtomic is the atomic JSON save function used by claim flows.
// Package-internal; overridable in tests.
var saveAtomic = state.SaveJSONAtomic

func AcquireClaim(rootDir string, args ...any) (state.ClaimArtifact, error) {
	req, err := parseAcquireClaimRequest(rootDir, args...)
	if err != nil {
		return state.ClaimArtifact{}, err
	}

	_, durablePath, err := claimPaths(req.rootDir, req.runID, req.ticketID)
	if err != nil {
		return state.ClaimArtifact{}, err
	}

	leaseID := req.leaseID
	if leaseID == "" {
		leaseID = generateLeaseID(req.runID, req.ticketID, req.now)
	}

	repoRoot := resolveRepoRoot(req.rootDir)
	if err := eposruntime.Claim(repoRoot, req.ticketID, req.runID, req.runID, req.ttl, eposruntime.WithLeaseID(leaseID)); err != nil {
		if !canReclaimExpired(repoRoot, req.ticketID, req.runID, req.now) {
			return state.ClaimArtifact{}, err
		}
		if reclaimErr := eposruntime.ReclaimExpired(repoRoot, req.ticketID, req.runID, req.runID, req.ttl, eposruntime.WithLeaseID(leaseID)); reclaimErr != nil {
			return state.ClaimArtifact{}, reclaimErr
		}
	}

	live, err := eposruntime.ReadRuntimeState(repoRoot, req.ticketID)
	if err != nil {
		return state.ClaimArtifact{}, err
	}
	claim := RuntimeStateToClaimArtifact(live)
	if claim == nil {
		return state.ClaimArtifact{}, fmt.Errorf("claim %s did not produce active runtime state", req.ticketID)
	}
	claim.ArtifactMeta = state.ArtifactMeta{
		SchemaVersion: claimSchemaVersion,
		RunID:         req.runID,
		CreatedAt:     req.now,
		UpdatedAt:     req.now,
	}
	claim.OwnerWaveID = req.ownerWaveID
	claim.State = "active"
	claim.LastSeenLiveClaimPath = liveClaimRelativePath(req.ticketID)

	if err := saveAtomic(durablePath, *claim); err != nil {
		if releaseErr := eposruntime.Release(repoRoot, req.ticketID, req.runID, eposticket.StatusPending, "durable claim write failed"); releaseErr != nil {
			return state.ClaimArtifact{}, fmt.Errorf("write durable claim: %w; rollback release failed: %v", err, releaseErr)
		}
		return state.ClaimArtifact{}, fmt.Errorf("write durable claim: %w", err)
	}
	return *claim, nil
}

func RenewClaim(rootDir string, args ...any) (state.ClaimArtifact, error) {
	req, err := parseRenewClaimRequest(rootDir, args...)
	if err != nil {
		return state.ClaimArtifact{}, err
	}

	_, durablePath, err := claimPaths(req.rootDir, req.runID, req.ticketID)
	if err != nil {
		return state.ClaimArtifact{}, err
	}

	repoRoot := resolveRepoRoot(req.rootDir)
	live, err := eposruntime.ReadRuntimeState(repoRoot, req.ticketID)
	if err != nil {
		return state.ClaimArtifact{}, err
	}
	liveClaim := RuntimeStateToClaimArtifact(live)

	durable, err := loadClaimArtifact(durablePath)
	if err != nil {
		return state.ClaimArtifact{}, err
	}
	current := liveClaim
	if current == nil {
		current = durable
	}
	if current == nil {
		return state.ClaimArtifact{}, fmt.Errorf("claim %s not found for renewal", req.ticketID)
	}
	if claimExpiredAt(current, req.now) {
		return state.ClaimArtifact{}, fmt.Errorf("claim %s lease expired at %s", req.ticketID, current.ExpiresAt.UTC().Format(time.RFC3339Nano))
	}
	if current.OwnerRunID != req.runID {
		return state.ClaimArtifact{}, fmt.Errorf("claim %s belongs to run %s", req.ticketID, current.OwnerRunID)
	}
	if err := ValidateLeaseFence(current.LeaseID, req.leaseID); err != nil {
		return state.ClaimArtifact{}, err
	}
	if liveClaim != nil {
		if err := ValidateLeaseFence(liveClaim.LeaseID, req.leaseID); err != nil {
			return state.ClaimArtifact{}, err
		}
	}
	if durable != nil {
		if claimReleased(durable) {
			return state.ClaimArtifact{}, fmt.Errorf("claim %s has already been released (durable state)", req.ticketID)
		}
		if durable.OwnerRunID != current.OwnerRunID {
			return state.ClaimArtifact{}, fmt.Errorf("claim %s diverged between live and durable state", req.ticketID)
		}
		if err := ValidateLeaseFence(durable.LeaseID, req.leaseID); err != nil {
			return state.ClaimArtifact{}, err
		}
	}

	if liveClaim == nil {
		if err := eposruntime.Claim(repoRoot, req.ticketID, req.runID, req.runID, req.ttl, eposruntime.WithLeaseID(req.leaseID)); err != nil {
			return state.ClaimArtifact{}, err
		}
	} else {
		if err := eposruntime.Renew(repoRoot, req.ticketID, req.runID, req.ttl, eposruntime.WithLeaseID(req.leaseID)); err != nil {
			return state.ClaimArtifact{}, err
		}
	}
	renewedLive, err := eposruntime.ReadRuntimeState(repoRoot, req.ticketID)
	if err != nil {
		return state.ClaimArtifact{}, err
	}
	renewed := RuntimeStateToClaimArtifact(renewedLive)
	if renewed == nil {
		return state.ClaimArtifact{}, fmt.Errorf("claim %s missing after live renew", req.ticketID)
	}
	renewed.ArtifactMeta = state.ArtifactMeta{
		SchemaVersion: claimSchemaVersion,
		RunID:         req.runID,
		CreatedAt:     pickTime(durableTime(durable, func(c *state.ClaimArtifact) time.Time { return c.CreatedAt }), current.LeasedAt),
		UpdatedAt:     req.now,
	}
	if !renewed.CreatedAt.IsZero() && renewed.UpdatedAt.Before(renewed.CreatedAt) {
		renewed.UpdatedAt = renewed.CreatedAt
	}
	renewed.OwnerWaveID = req.ownerWaveID
	if renewed.OwnerWaveID == "" && durable != nil {
		renewed.OwnerWaveID = durable.OwnerWaveID
	}
	renewed.State = "active"
	renewed.LastSeenLiveClaimPath = liveClaimRelativePath(req.ticketID)

	if err := saveAtomic(durablePath, *renewed); err != nil {
		return state.ClaimArtifact{}, fmt.Errorf("write durable claim after live renew: live/durable divergence: %w", err)
	}
	return *renewed, nil
}

func ReleaseClaim(rootDir string, args ...any) error {
	req, err := parseReleaseClaimRequest(rootDir, args...)
	if err != nil {
		return err
	}

	livePath, durablePath, err := claimPaths(req.rootDir, req.runID, req.ticketID)
	if err != nil {
		return err
	}

	repoRoot := resolveRepoRoot(req.rootDir)
	live, err := eposruntime.ReadRuntimeState(repoRoot, req.ticketID)
	if err != nil {
		return err
	}
	liveClaim := RuntimeStateToClaimArtifact(live)

	durable, err := loadClaimArtifact(durablePath)
	if err != nil {
		return err
	}
	current := liveClaim
	if current == nil {
		current = durable
	}
	if current == nil {
		return fmt.Errorf("claim %s not found for release", req.ticketID)
	}
	if current.OwnerRunID != req.runID {
		return fmt.Errorf("claim %s belongs to run %s", req.ticketID, current.OwnerRunID)
	}
	leaseID := req.leaseID
	if leaseID == "" {
		leaseID = current.LeaseID
	}
	if err := ValidateLeaseFence(current.LeaseID, leaseID); err != nil {
		return err
	}
	if liveClaim != nil {
		if err := ValidateLeaseFence(liveClaim.LeaseID, leaseID); err != nil {
			return err
		}
	}
	if durable != nil {
		if durable.OwnerRunID != current.OwnerRunID {
			return fmt.Errorf("claim %s diverged between live and durable state", req.ticketID)
		}
		if err := ValidateLeaseFence(durable.LeaseID, leaseID); err != nil {
			return err
		}
	}

	if req.releaseReason == "" {
		req.releaseReason = "released"
	}
	if liveClaim != nil {
		if err := eposruntime.Release(repoRoot, req.ticketID, req.runID, eposticket.StatusPending, req.releaseReason); err != nil {
			return err
		}
	} else {
		if err := writeReleasedRuntimeStateIfUnclaimed(repoRoot, livePath, req.ticketID); err != nil {
			return err
		}
	}

	released := *current
	if durable != nil {
		released = *durable
		normalizeClaimArtifact(&released, req.now)
	}
	released.ArtifactMeta = state.ArtifactMeta{
		SchemaVersion: claimSchemaVersion,
		RunID:         req.runID,
		CreatedAt:     pickTime(released.CreatedAt, current.LeasedAt),
		UpdatedAt:     req.now,
	}
	released.TicketID = req.ticketID
	released.OwnerRunID = req.runID
	released.LeaseID = current.LeaseID
	released.State = "released"
	released.ReleasedAt = req.now
	released.ReleaseReason = req.releaseReason
	released.LastSeenLiveClaimPath = liveClaimRelativePath(req.ticketID)
	return state.SaveJSONAtomic(durablePath, released)
}

func ReconcileClaim(live, durable *state.ClaimArtifact, runID string, terminal bool) (state.ClaimArtifact, error) {
	switch {
	case live == nil && durable == nil:
		return state.ClaimArtifact{}, errors.New("missing live and durable claim state")
	case live != nil && durable != nil:
		if live.TicketID != durable.TicketID {
			return state.ClaimArtifact{}, fmt.Errorf("claim divergence for ticket %s: live ticket %s durable ticket %s", durable.TicketID, live.TicketID, durable.TicketID)
		}
		if live.OwnerRunID != durable.OwnerRunID || live.LeaseID != durable.LeaseID {
			return state.ClaimArtifact{}, fmt.Errorf("claim divergence for ticket %s: owner or lease mismatch", durable.TicketID)
		}
		if !claimReleased(durable) && !claimReleased(live) && live.OwnerRunID != runID {
			return state.ClaimArtifact{}, fmt.Errorf("claim divergence for ticket %s: owned by run %s", durable.TicketID, live.OwnerRunID)
		}
		result := *durable
		normalizeClaimArtifact(&result, result.UpdatedAt)
		if result.LastSeenLiveClaimPath == "" {
			result.LastSeenLiveClaimPath = liveClaimRelativePath(result.TicketID)
		}
		if claimReleased(durable) || claimReleased(live) {
			result.State = "released"
			if result.ReleasedAt.IsZero() {
				result.ReleasedAt = pickTime(live.ReleasedAt, durable.ReleasedAt)
			}
			if result.ReleaseReason == "" {
				result.ReleaseReason = pickReleaseReason(live.ReleaseReason, durable.ReleaseReason)
			}
			return result, nil
		}
		result.State = "active"
		return result, nil
	case live != nil:
		if live.OwnerRunID != runID {
			return state.ClaimArtifact{}, fmt.Errorf("claim divergence for ticket %s: owned by run %s", live.TicketID, live.OwnerRunID)
		}
		if terminal {
			return state.ClaimArtifact{}, fmt.Errorf("claim %s cannot rebuild durable state after terminal transition", live.TicketID)
		}
		result := *live
		normalizeClaimArtifact(&result, result.UpdatedAt)
		result.State = "active"
		if result.LastSeenLiveClaimPath == "" {
			result.LastSeenLiveClaimPath = liveClaimRelativePath(result.TicketID)
		}
		return result, nil
	default:
		if durable.OwnerRunID != runID && !claimReleased(durable) {
			if terminal {
				return *durable, nil
			}
			return state.ClaimArtifact{}, fmt.Errorf("stale live-claim loss for ticket %s owned by run %s", durable.TicketID, durable.OwnerRunID)
		}
		if claimReleased(durable) {
			result := *durable
			normalizeClaimArtifact(&result, result.UpdatedAt)
			result.State = "released"
			return result, nil
		}
		if terminal {
			return *durable, nil
		}
		return state.ClaimArtifact{}, fmt.Errorf("stale live-claim loss for ticket %s", durable.TicketID)
	}
}

type acquireClaimRequest struct {
	rootDir     string
	runID       string
	ticketID    string
	leaseID     string
	ownerWaveID string
	ttl         time.Duration
	now         time.Time
}

type renewClaimRequest struct {
	rootDir     string
	runID       string
	ticketID    string
	leaseID     string
	ownerWaveID string
	ttl         time.Duration
	now         time.Time
}

type releaseClaimRequest struct {
	rootDir       string
	runID         string
	ticketID      string
	leaseID       string
	releaseReason string
	now           time.Time
}

func parseAcquireClaimRequest(rootDir string, args ...any) (acquireClaimRequest, error) {
	req := acquireClaimRequest{
		rootDir: rootDir,
		ttl:     defaultClaimTTL,
		now:     time.Now().UTC(),
	}
	if len(args) < 2 {
		return req, errors.New("acquire claim requires run_id and ticket_id")
	}
	runID, ok := args[0].(string)
	if !ok || runID == "" {
		return req, errors.New("acquire claim requires run_id")
	}
	ticketID, ok := args[1].(string)
	if !ok || ticketID == "" {
		return req, errors.New("acquire claim requires ticket_id")
	}
	req.runID = runID
	req.ticketID = ticketID
	if err := validateClaimIdentifier(req.runID, "run_id"); err != nil {
		return req, err
	}
	if err := validateClaimIdentifier(req.ticketID, "ticket_id"); err != nil {
		return req, err
	}

	nextString := 2
	for _, arg := range args[2:] {
		switch v := arg.(type) {
		case string:
			switch nextString {
			case 2:
				req.leaseID = v
			case 3:
				req.ownerWaveID = v
			default:
				return req, fmt.Errorf("unexpected string argument %q for acquire claim", v)
			}
			nextString++
		case time.Duration:
			req.ttl = v
		case time.Time:
			req.now = v.UTC()
		case state.ClaimArtifact:
			if req.leaseID == "" {
				req.leaseID = v.LeaseID
			}
			if req.ownerWaveID == "" {
				req.ownerWaveID = v.OwnerWaveID
			}
			if req.ttl == defaultClaimTTL && !v.ExpiresAt.IsZero() && !v.LeasedAt.IsZero() {
				if ttl := v.ExpiresAt.Sub(v.LeasedAt); ttl > 0 {
					req.ttl = ttl
				}
			}
		case *state.ClaimArtifact:
			if v != nil {
				if req.leaseID == "" {
					req.leaseID = v.LeaseID
				}
				if req.ownerWaveID == "" {
					req.ownerWaveID = v.OwnerWaveID
				}
			}
		default:
			return req, fmt.Errorf("unsupported acquire claim argument %T", arg)
		}
	}
	return req, nil
}

func parseRenewClaimRequest(rootDir string, args ...any) (renewClaimRequest, error) {
	req := renewClaimRequest{
		rootDir: rootDir,
		ttl:     defaultClaimTTL,
		now:     time.Now().UTC(),
	}
	if len(args) < 3 {
		return req, errors.New("renew claim requires run_id, ticket_id, and lease_id")
	}
	runID, ok := args[0].(string)
	if !ok || runID == "" {
		return req, errors.New("renew claim requires run_id")
	}
	ticketID, ok := args[1].(string)
	if !ok || ticketID == "" {
		return req, errors.New("renew claim requires ticket_id")
	}
	req.runID = runID
	req.ticketID = ticketID
	if err := validateClaimIdentifier(req.runID, "run_id"); err != nil {
		return req, err
	}
	if err := validateClaimIdentifier(req.ticketID, "ticket_id"); err != nil {
		return req, err
	}

	leaseID, ok := args[2].(string)
	if !ok || leaseID == "" {
		return req, errors.New("renew claim requires lease_id")
	}
	req.leaseID = leaseID

	for _, arg := range args[3:] {
		switch v := arg.(type) {
		case time.Duration:
			req.ttl = v
		case time.Time:
			req.now = v.UTC()
		case state.ClaimArtifact:
			if req.ownerWaveID == "" {
				req.ownerWaveID = v.OwnerWaveID
			}
		case *state.ClaimArtifact:
			if v != nil && req.ownerWaveID == "" {
				req.ownerWaveID = v.OwnerWaveID
			}
		default:
			return req, fmt.Errorf("unsupported renew claim argument %T", arg)
		}
	}
	return req, nil
}

func parseReleaseClaimRequest(rootDir string, args ...any) (releaseClaimRequest, error) {
	req := releaseClaimRequest{
		rootDir: rootDir,
		now:     time.Now().UTC(),
	}
	if len(args) < 2 {
		return req, errors.New("release claim requires run_id and ticket_id")
	}
	runID, ok := args[0].(string)
	if !ok || runID == "" {
		return req, errors.New("release claim requires run_id")
	}
	ticketID, ok := args[1].(string)
	if !ok || ticketID == "" {
		return req, errors.New("release claim requires ticket_id")
	}
	req.runID = runID
	req.ticketID = ticketID
	if err := validateClaimIdentifier(req.runID, "run_id"); err != nil {
		return req, err
	}
	if err := validateClaimIdentifier(req.ticketID, "ticket_id"); err != nil {
		return req, err
	}

	switch len(args) - 2 {
	case 0:
	case 1:
		switch v := args[2].(type) {
		case string:
			if looksLikeLeaseID(v) {
				req.leaseID = v
			} else {
				req.releaseReason = v
			}
		case time.Time:
			req.now = v.UTC()
		case state.ClaimArtifact:
			req.leaseID = v.LeaseID
			req.releaseReason = v.ReleaseReason
		case *state.ClaimArtifact:
			if v != nil {
				req.leaseID = v.LeaseID
				req.releaseReason = v.ReleaseReason
			}
		default:
			return req, fmt.Errorf("unsupported release claim argument %T", args[2])
		}
	default:
		leaseID, ok := args[2].(string)
		if !ok || leaseID == "" {
			return req, errors.New("release claim requires lease_id")
		}
		req.leaseID = leaseID
		switch v := args[3].(type) {
		case string:
			req.releaseReason = v
		case time.Time:
			req.now = v.UTC()
		case state.ClaimArtifact:
			req.releaseReason = v.ReleaseReason
		case *state.ClaimArtifact:
			if v != nil {
				req.releaseReason = v.ReleaseReason
			}
		default:
			return req, fmt.Errorf("unsupported release claim argument %T", args[3])
		}
	}
	return req, nil
}

func ValidateLeaseFence(expected, actual string) error {
	if expected == "" || actual == "" || expected != actual {
		return fmt.Errorf("lease fence mismatch: expected %q, got %q", expected, actual)
	}
	return nil
}

func loadClaimArtifact(path string) (*state.ClaimArtifact, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil //nolint:nilnil // not-found: nil value + nil error = "no data, no problem"
		}
		return nil, fmt.Errorf("read claim: %w", err)
	}
	var claim state.ClaimArtifact
	if err := json.Unmarshal(data, &claim); err != nil {
		return nil, fmt.Errorf("decode claim: %w", err)
	}
	normalizeClaimArtifact(&claim, claim.UpdatedAt)
	return &claim, nil
}

func normalizeClaimArtifact(claim *state.ClaimArtifact, now time.Time) {
	if claim == nil {
		return
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if claim.SchemaVersion == 0 {
		claim.SchemaVersion = claimSchemaVersion
	}
	if claim.CreatedAt.IsZero() {
		if !claim.LeasedAt.IsZero() {
			claim.CreatedAt = claim.LeasedAt
		} else {
			claim.CreatedAt = now
		}
	}
	if claim.UpdatedAt.IsZero() {
		claim.UpdatedAt = now
	}
	if claim.RunID == "" {
		claim.RunID = claim.OwnerRunID
	}
}

func claimReleased(claim *state.ClaimArtifact) bool {
	return claim != nil && strings.EqualFold(claim.State, "released")
}

func claimExpiredAt(claim *state.ClaimArtifact, now time.Time) bool {
	if claim == nil || claim.ExpiresAt.IsZero() {
		return false
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return !now.Before(claim.ExpiresAt.UTC())
}

func canReclaimExpired(repoRoot, ticketID, runID string, now time.Time) bool {
	runtimeState, err := eposruntime.ReadRuntimeState(repoRoot, ticketID)
	if err != nil || runtimeState == nil || runtimeState.Claim == nil || runtimeState.Lease == nil {
		return false
	}
	if runtimeState.Claim.ClaimedBy == runID {
		return false
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return !now.Before(runtimeState.Lease.ExpiresAt.UTC())
}

func writeReleasedRuntimeStateIfUnclaimed(repoRoot, livePath, ticketID string) error {
	lock := flock.New(livePath + ".lock")
	if err := lock.Lock(); err != nil {
		return fmt.Errorf("acquire claim lock %q: %w", ticketID, err)
	}
	defer func() { _ = lock.Unlock() }()

	runtimeState, err := eposruntime.ReadRuntimeState(repoRoot, ticketID)
	if err != nil {
		return err
	}
	if runtimeState.Claim != nil {
		return fmt.Errorf("claim %s gained live owner %s before durable-only release", ticketID, runtimeState.Claim.ClaimedBy)
	}
	return eposruntime.WriteRuntimeState(repoRoot, &eposticket.RuntimeState{
		TicketID: ticketID,
		Status:   eposticket.StatusPending,
	})
}

func generateLeaseID(runID, ticketID string, now time.Time) string {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return fmt.Sprintf("lease-%s-%s-%d", runID, ticketID, now.UnixNano())
}

// looksLikeLeaseID recognizes the lease/fence values generated by verk
// callsites. Release reasons such as "lease expired" are intentionally not
// treated as fences.
func looksLikeLeaseID(value string) bool {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "lease-") {
		parts := strings.Split(value, "-")
		if len(parts) < 4 {
			return false
		}
		_, err := strconv.ParseInt(parts[len(parts)-1], 10, 64)
		return err == nil
	}
	if strings.HasPrefix(value, "fence-") {
		return !strings.ContainsAny(strings.TrimPrefix(value, "fence-"), " \t\r\n/\\")
	}
	return false
}

func pickTime(a, b time.Time) time.Time {
	if !a.IsZero() {
		return a
	}
	return b
}

func pickReleaseReason(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func durableTime(claim *state.ClaimArtifact, pick func(*state.ClaimArtifact) time.Time) time.Time {
	if claim == nil {
		return time.Time{}
	}
	return pick(claim)
}
