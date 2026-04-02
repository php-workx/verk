package tkmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"verk/internal/state"
)

const (
	claimSchemaVersion = 1
	defaultClaimTTL    = 10 * time.Minute
)

func AcquireClaim(rootDir string, args ...any) (state.ClaimArtifact, error) {
	req, err := parseAcquireClaimRequest(rootDir, args...)
	if err != nil {
		return state.ClaimArtifact{}, err
	}

	livePath, durablePath, err := claimPaths(req.rootDir, req.runID, req.ticketID)
	if err != nil {
		return state.ClaimArtifact{}, err
	}

	live, err := loadClaimArtifact(livePath)
	if err != nil {
		return state.ClaimArtifact{}, err
	}
	if live != nil && !claimReleased(live) {
		if claimActiveAt(*live, req.now) {
			return state.ClaimArtifact{}, fmt.Errorf("claim %s already held by run %s", req.ticketID, live.OwnerRunID)
		}
		return state.ClaimArtifact{}, fmt.Errorf("claim %s has stale live state", req.ticketID)
	}

	durable, err := loadClaimArtifact(durablePath)
	if err != nil {
		return state.ClaimArtifact{}, err
	}
	if durable != nil && !claimReleased(durable) {
		if claimActiveAt(*durable, req.now) {
			return state.ClaimArtifact{}, fmt.Errorf("claim %s already recorded for run %s", req.ticketID, durable.OwnerRunID)
		}
		return state.ClaimArtifact{}, fmt.Errorf("claim %s has stale durable state", req.ticketID)
	}

	leaseID := req.leaseID
	if leaseID == "" {
		leaseID = generateLeaseID(req.runID, req.ticketID, req.now)
	}

	claim := state.ClaimArtifact{
		ArtifactMeta: state.ArtifactMeta{
			SchemaVersion: claimSchemaVersion,
			RunID:         req.runID,
			CreatedAt:     req.now,
			UpdatedAt:     req.now,
		},
		TicketID:              req.ticketID,
		OwnerRunID:            req.runID,
		OwnerWaveID:           req.ownerWaveID,
		LeaseID:               leaseID,
		LeasedAt:              req.now,
		ExpiresAt:             req.now.Add(req.ttl),
		State:                 "active",
		LastSeenLiveClaimPath: liveClaimRelativePath(req.ticketID),
	}

	if err := state.SaveJSONAtomic(livePath, claim); err != nil {
		return state.ClaimArtifact{}, fmt.Errorf("write live claim: %w", err)
	}
	if err := state.SaveJSONAtomic(durablePath, claim); err != nil {
		return state.ClaimArtifact{}, fmt.Errorf("write durable claim: %w", err)
	}
	return claim, nil
}

func RenewClaim(rootDir string, args ...any) (state.ClaimArtifact, error) {
	req, err := parseRenewClaimRequest(rootDir, args...)
	if err != nil {
		return state.ClaimArtifact{}, err
	}

	livePath, durablePath, err := claimPaths(req.rootDir, req.runID, req.ticketID)
	if err != nil {
		return state.ClaimArtifact{}, err
	}

	live, err := loadClaimArtifact(livePath)
	if err != nil {
		return state.ClaimArtifact{}, err
	}
	if live == nil {
		return state.ClaimArtifact{}, fmt.Errorf("claim %s not found for renewal", req.ticketID)
	}
	if claimReleased(live) {
		return state.ClaimArtifact{}, fmt.Errorf("claim %s has already been released", req.ticketID)
	}
	if live.OwnerRunID != req.runID {
		return state.ClaimArtifact{}, fmt.Errorf("claim %s belongs to run %s", req.ticketID, live.OwnerRunID)
	}
	if err := ValidateLeaseFence(live.LeaseID, req.leaseID); err != nil {
		return state.ClaimArtifact{}, err
	}
	if claimExpired(*live, req.now) {
		return state.ClaimArtifact{}, fmt.Errorf("claim %s lease expired at %s", req.ticketID, live.ExpiresAt.UTC().Format(time.RFC3339Nano))
	}

	durable, err := loadClaimArtifact(durablePath)
	if err != nil {
		return state.ClaimArtifact{}, err
	}
	if durable != nil && !claimReleased(durable) {
		if durable.OwnerRunID != live.OwnerRunID || durable.LeaseID != live.LeaseID {
			return state.ClaimArtifact{}, fmt.Errorf("claim %s diverged between live and durable state", req.ticketID)
		}
	}

	renewed := *live
	normalizeClaimArtifact(&renewed, req.now)
	renewed.RunID = req.runID
	renewed.OwnerRunID = req.runID
	if req.ownerWaveID != "" {
		renewed.OwnerWaveID = req.ownerWaveID
	}
	renewed.LeaseID = live.LeaseID
	renewed.LeasedAt = live.LeasedAt
	renewed.ExpiresAt = req.now.Add(req.ttl)
	renewed.State = "active"
	renewed.LastSeenLiveClaimPath = liveClaimRelativePath(req.ticketID)

	if err := state.SaveJSONAtomic(livePath, renewed); err != nil {
		return state.ClaimArtifact{}, fmt.Errorf("write live claim: %w", err)
	}
	if err := state.SaveJSONAtomic(durablePath, renewed); err != nil {
		return state.ClaimArtifact{}, fmt.Errorf("write durable claim: %w", err)
	}
	return renewed, nil
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

	live, err := loadClaimArtifact(livePath)
	if err != nil {
		return err
	}
	durable, err := loadClaimArtifact(durablePath)
	if err != nil {
		return err
	}

	current := live
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

	released := *current
	normalizeClaimArtifact(&released, req.now)
	released.RunID = req.runID
	released.OwnerRunID = req.runID
	released.LeaseID = current.LeaseID
	released.State = "released"
	released.ReleasedAt = req.now
	if req.releaseReason == "" {
		req.releaseReason = "released"
	}
	released.ReleaseReason = req.releaseReason
	released.LastSeenLiveClaimPath = liveClaimRelativePath(req.ticketID)

	if err := state.SaveJSONAtomic(durablePath, released); err != nil {
		return fmt.Errorf("write durable claim: %w", err)
	}
	if err := os.Remove(livePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove live claim: %w", err)
	}
	return nil
}

func ReconcileClaim(live *state.ClaimArtifact, durable *state.ClaimArtifact, runID string, terminal bool) (state.ClaimArtifact, error) {
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

func ValidateLeaseFence(expected, actual string) error {
	if expected == "" || actual == "" || expected != actual {
		return fmt.Errorf("lease fence mismatch: expected %q, got %q", expected, actual)
	}
	return nil
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

	if leaseID, ok := args[2].(string); ok && leaseID != "" {
		req.leaseID = leaseID
	} else {
		return req, errors.New("renew claim requires lease_id")
	}

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

func claimPaths(rootDir, runID, ticketID string) (string, string, error) {
	if rootDir == "" {
		return "", "", errors.New("root dir is required")
	}
	if runID == "" {
		return "", "", errors.New("run_id is required")
	}
	if ticketID == "" {
		return "", "", errors.New("ticket_id is required")
	}
	repoRoot := resolveRepoRoot(rootDir)
	ticketsDir := resolveTicketsDir(rootDir)
	livePath := filepath.Join(ticketsDir, ".claims", ticketID+".json")
	durablePath := filepath.Join(repoRoot, ".verk", "runs", runID, "claims", "claim-"+ticketID+".json")
	return livePath, durablePath, nil
}

func resolveRepoRoot(rootDir string) string {
	cleaned := filepath.Clean(rootDir)
	switch filepath.Base(cleaned) {
	case ".tickets", ".verk":
		return filepath.Dir(cleaned)
	default:
		return cleaned
	}
}

func loadClaimArtifact(path string) (*state.ClaimArtifact, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
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

func claimActiveAt(claim state.ClaimArtifact, now time.Time) bool {
	if claimReleased(&claim) {
		return false
	}
	if claim.OwnerRunID == "" {
		return false
	}
	if claim.ExpiresAt.IsZero() {
		return true
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return now.Before(claim.ExpiresAt.UTC())
}

func claimExpired(claim state.ClaimArtifact, now time.Time) bool {
	if claim.ExpiresAt.IsZero() {
		return false
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return !now.Before(claim.ExpiresAt.UTC())
}

func liveClaimRelativePath(ticketID string) string {
	return filepath.Join(".tickets", ".claims", ticketID+".json")
}

func generateLeaseID(runID, ticketID string, now time.Time) string {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return fmt.Sprintf("lease-%s-%s-%d", runID, ticketID, now.UnixNano())
}

func looksLikeLeaseID(value string) bool {
	return strings.HasPrefix(value, "lease-")
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
