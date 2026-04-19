package engine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"verk/internal/adapters/repo/git"
	"verk/internal/adapters/runtime"
	"verk/internal/adapters/ticketstore/tkmd"
	"verk/internal/policy"
	"verk/internal/state"
)

// ErrEpicBlocked is returned when RunEpic terminates because the epic cannot
// make further progress (e.g. remaining children are blocked or waiting on
// external leases). Callers can use errors.Is(err, ErrEpicBlocked) to
// distinguish this from hard failures.
var ErrEpicBlocked = errors.New("epic run blocked")

// BlockedTicket is a terminating ticket captured at the end of a blocked epic
// run. It aggregates the minimum information a caller needs to render operator
// guidance ("ticket X blocked because Y, retry with `verk reopen ...`") without
// forcing the CLI to re-load snapshots from disk.
type BlockedTicket struct {
	// ID is the canonical ticket identifier.
	ID string
	// Title is the human-readable ticket title, empty when unavailable.
	Title string
	// Status is the ticket-store status at the moment the run stopped.
	Status tkmd.Status
	// Reason is the best available block reason. It prefers the worker-reported
	// BlockReason from the ticket snapshot and falls back to a structural
	// description (blocked, waiting on deps, …) when no snapshot reason exists.
	Reason string
}

// BlockedRunError is the error returned by RunEpic when a run terminates
// without completing. It unwraps to ErrEpicBlocked so existing
// errors.Is(err, ErrEpicBlocked) checks keep working, and it carries enough
// structured detail for CLI callers to render actionable retry guidance.
type BlockedRunError struct {
	RunID          string
	Status         state.EpicRunStatus
	BlockedTickets []BlockedTicket
	// Cause is the underlying engine error, if any (wave failure, acceptance
	// error, verification error). Nil when the run simply ran out of ready
	// tickets.
	Cause error
}

// Error renders a compact single-line summary. The CLI prints a more detailed
// multi-line version alongside this error; the summary is what shows up in
// logs and when the error is printed by Cobra's default handler.
func (e *BlockedRunError) Error() string {
	if e == nil {
		return ErrEpicBlocked.Error()
	}
	ids := make([]string, 0, len(e.BlockedTickets))
	for _, t := range e.BlockedTickets {
		ids = append(ids, t.ID)
	}
	switch {
	case len(ids) > 0 && e.Cause != nil:
		return fmt.Sprintf("%s: %s (blocked tickets: %s): %v", ErrEpicBlocked.Error(), e.Status, strings.Join(ids, ", "), e.Cause)
	case len(ids) > 0:
		return fmt.Sprintf("%s: %s (blocked tickets: %s)", ErrEpicBlocked.Error(), e.Status, strings.Join(ids, ", "))
	case e.Cause != nil:
		return fmt.Sprintf("%s: %s: %v", ErrEpicBlocked.Error(), e.Status, e.Cause)
	default:
		return fmt.Sprintf("%s: %s", ErrEpicBlocked.Error(), e.Status)
	}
}

// Unwrap returns ErrEpicBlocked so errors.Is(err, ErrEpicBlocked) succeeds for
// any BlockedRunError value. The underlying Cause is intentionally not exposed
// via Unwrap: surfacing the sentinel is what existing callers rely on.
func (e *BlockedRunError) Unwrap() error { return ErrEpicBlocked }

// collectBlockedTickets builds a BlockedTicket list for the supplied children,
// preferring the BlockReason stored in each ticket's run snapshot over the
// structural fallback returned by describeNotReady. Closed tickets are skipped
// so the result only contains the tickets responsible for the blocked state.
// Snapshot read errors are silently ignored: in those cases the structural
// description is used instead, so CLI output still names the ticket even when
// its snapshot artifact is missing or malformed.
func collectBlockedTickets(repoRoot, runID string, children []tkmd.Ticket) []BlockedTicket {
	var out []BlockedTicket
	for _, child := range children {
		if child.Status == tkmd.StatusClosed {
			continue
		}
		reason := describeNotReady(child)
		if runID != "" {
			var snap TicketRunSnapshot
			if err := loadTicketSnapshot(repoRoot, runID, child.ID, &snap); err == nil {
				if trimmed := strings.TrimSpace(snap.BlockReason); trimmed != "" {
					reason = trimmed
				}
			}
		}
		out = append(out, BlockedTicket{
			ID:     child.ID,
			Title:  child.Title,
			Status: child.Status,
			Reason: reason,
		})
	}
	return out
}

type RunEpicRequest struct {
	RepoRoot             string
	RunID                string
	RootTicketID         string
	BaseBranch           string
	BaseCommit           string
	Adapter              runtime.Adapter
	AdapterFactory       func(ticketPreference string) (runtime.Adapter, error)
	Config               policy.Config
	VerificationByTicket map[string][]string
	Progress             chan<- ProgressEvent
}

type RunEpicResult struct {
	Run   state.RunArtifact
	Waves []state.WaveArtifact
	Path  string
}

func RunEpic(ctx context.Context, req RunEpicRequest) (RunEpicResult, error) { //nolint:gocognit,cyclop // complex orchestration loop; refactor into sub-functions
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateRunEpicRequest(req); err != nil {
		return RunEpicResult{}, err
	}

	lock, err := AcquireRunLock(req.RepoRoot, req.RunID)
	if err != nil {
		return RunEpicResult{}, err
	}
	defer func() { _ = lock.Release() }()

	cfg := normalizeEpicConfig(req.Config)
	repo, err := git.New(req.RepoRoot)
	if err != nil {
		return RunEpicResult{}, err
	}
	baseCommit := strings.TrimSpace(req.BaseCommit)
	if baseCommit == "" {
		baseCommit, err = repo.HeadCommit()
		if err != nil {
			return RunEpicResult{}, err
		}
	}

	children, err := listEpicChildren(req.RepoRoot, req.RootTicketID)
	if err != nil {
		return RunEpicResult{}, err
	}

	// Reset orphaned in_progress tickets (from crashed prior runs) to ready
	// so the wave scheduler can pick them up.
	for _, child := range children {
		if child.Status == tkmd.StatusInProgress {
			if err := updateTicketStoreStatus(req.RepoRoot, child.ID, tkmd.StatusOpen); err != nil {
				return RunEpicResult{}, fmt.Errorf("reset orphaned ticket %s: %w", child.ID, err)
			}
			SendProgress(ctx, req.Progress, ProgressEvent{
				Type:     EventTicketDetail,
				TicketID: child.ID,
				Detail:   "reset to ready (was in_progress from prior run)",
			})
		}
	}

	runPath := filepath.Join(req.RepoRoot, ".verk", "runs", req.RunID, "run.json")
	run := state.RunArtifact{
		ArtifactMeta: state.ArtifactMeta{
			SchemaVersion: artifactSchemaVersion,
			RunID:         req.RunID,
			CreatedAt:     time.Now().UTC(),
			UpdatedAt:     time.Now().UTC(),
		},
		Mode:         "epic",
		RootTicketID: req.RootTicketID,
		Status:       state.EpicRunStatusRunning,
		CurrentPhase: state.TicketPhaseImplement,
		Policy:       configMap(cfg.Policy),
		Config:       configMap(cfg),
		BaseBranch:   strings.TrimSpace(req.BaseBranch),
		BaseCommit:   baseCommit,
		TicketIDs:    ticketIDs(children),
		ResumeCursor: map[string]any{
			"wave_ordinal": 0,
		},
	}
	if err := state.SaveJSONAtomic(runPath, run); err != nil {
		return RunEpicResult{}, err
	}

	result := RunEpicResult{Run: run, Path: runPath}
	registrar := &subEpicRegistrar{run: &result.Run, runPath: runPath}
	waveOrdinal := 0

	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}

		// If a prior wave is still pending verification (e.g. the process crashed
		// mid-repair), complete it before scheduling new tickets.
		if err := resumePendingWaveVerification(ctx, req, cfg, result.Run.ResumeCursor, runPath, &result.Run); err != nil {
			result.Run.Status = state.EpicRunStatusBlocked
			result.Run.CurrentPhase = state.TicketPhaseBlocked
			if saveErr := state.SaveJSONAtomic(runPath, result.Run); saveErr != nil {
				return result, errors.Join(err, fmt.Errorf("persist run state: %w", saveErr))
			}
			return result, err
		}

		ready, err := tkmd.ListReadyChildren(req.RepoRoot, req.RootTicketID, req.RunID)
		if err != nil {
			return result, err
		}
		ticketScopes := buildTicketScopes(ready)
		if len(ready) == 0 {
			currentChildren, err := listEpicChildren(req.RepoRoot, req.RootTicketID)
			if err != nil {
				return result, err
			}
			if len(currentChildren) == 0 {
				SendProgress(ctx, req.Progress, ProgressEvent{
					Type:   EventTicketDetail,
					Detail: fmt.Sprintf("no child tickets found for epic %s", req.RootTicketID),
				})
			} else {
				for _, child := range currentChildren {
					reason := describeNotReady(child)
					SendProgress(ctx, req.Progress, ProgressEvent{
						Type:     EventTicketDetail,
						TicketID: child.ID,
						Title:    child.Title,
						Detail:   reason,
					})
				}
			}
			status := epicCompletionStatus(currentChildren)
			result.Run.Status = status
			switch status {
			case state.EpicRunStatusCompleted:
				result.Run.CurrentPhase = state.TicketPhaseClosed
			case state.EpicRunStatusBlocked:
				result.Run.CurrentPhase = state.TicketPhaseBlocked
			default:
				result.Run.CurrentPhase = state.TicketPhaseImplement
			}
			result.Run.UpdatedAt = time.Now().UTC()
			if err := state.SaveJSONAtomic(runPath, result.Run); err != nil {
				return result, err
			}
			if status != state.EpicRunStatusCompleted {
				emitBlockedEpicSummary(ctx, req.Progress, currentChildren)
				return result, &BlockedRunError{
					RunID:          req.RunID,
					Status:         status,
					BlockedTickets: collectBlockedTickets(req.RepoRoot, req.RunID, currentChildren),
				}
			}
			return result, nil
		}

		wave, err := BuildWave(ready, cfg.Scheduler.MaxConcurrency)
		if err != nil {
			result.Run.Status = state.EpicRunStatusBlocked
			result.Run.CurrentPhase = state.TicketPhaseBlocked
			result.Run.UpdatedAt = time.Now().UTC()
			if saveErr := state.SaveJSONAtomic(runPath, result.Run); saveErr != nil {
				return result, errors.Join(err, fmt.Errorf("persist run state: %w", saveErr))
			}
			return result, err
		}

		waveOrdinal++
		waveID := fmt.Sprintf("wave-%d", waveOrdinal)
		wave.WaveID = waveID
		wave.Ordinal = waveOrdinal
		wave.Status = state.WaveStatusRunning
		wave.WaveBaseCommit = baseCommit
		wave.StartedAt = time.Now().UTC()
		wavePath := filepath.Join(req.RepoRoot, ".verk", "runs", req.RunID, "waves", waveID+".json")
		if err := state.SaveJSONAtomic(wavePath, wave); err != nil {
			return result, err
		}
		SendProgress(ctx, req.Progress, ProgressEvent{
			Type:    EventWaveStarted,
			WaveID:  waveOrdinal,
			Tickets: append([]string(nil), wave.TicketIDs...),
		})

		waveBaselineChangedFiles, err := repo.ChangedFilesAgainst(baseCommit)
		if err != nil {
			return result, err
		}
		waveBaselineChangedFiles = filterEngineOwnedFiles(waveBaselineChangedFiles)
		if result.Run.ResumeCursor == nil {
			result.Run.ResumeCursor = map[string]any{}
		}
		result.Run.ResumeCursor["wave_baseline_changed_files"] = append([]string(nil), waveBaselineChangedFiles...)

		outcomes := make([]waveTicketOutcome, len(wave.TicketIDs))
		var wg sync.WaitGroup
		for i, ticketID := range wave.TicketIDs {
			wg.Add(1)
			i := i
			ticketID := ticketID
			go func() {
				defer wg.Done()
				const maxCrashRetries = 2
				for attempt := 0; attempt <= maxCrashRetries; attempt++ {
					outcome, crashed := executeWithRecovery(ctx, req, cfg, wave, ticketID, 1, nil, registrar)
					if !crashed {
						outcomes[i] = outcome
						return
					}
					SendProgress(ctx, req.Progress, ProgressEvent{
						Type:     EventTicketDetail,
						TicketID: ticketID,
						Detail:   fmt.Sprintf("worker crashed (attempt %d/%d), retrying: %v", attempt+1, maxCrashRetries+1, outcome.err),
					})
					_ = tkmd.ReleaseClaim(req.RepoRoot, req.RunID, ticketID, "crash recovery")
					if attempt == maxCrashRetries {
						outcome.phase = state.TicketPhaseBlocked
						outcomes[i] = outcome
						return
					}
				}
			}()
		}
		wg.Wait()

		ticketPhases := make([]state.TicketPhase, len(outcomes))
		waveFailed := false
		var waveErr error
		for i, outcome := range outcomes {
			ticketPhases[i] = outcome.phase
			if outcome.err != nil {
				waveFailed = true
				if waveErr == nil {
					waveErr = outcome.err
				}
			}
		}

		changedFiles, err := repo.ChangedFilesAgainst(baseCommit)
		if err != nil {
			return result, err
		}
		changedFiles = filterEngineOwnedFiles(changedFiles)
		changedFiles = subtractFiles(changedFiles, waveBaselineChangedFiles)

		claimsReleased, err := waveClaimsReleased(req.RepoRoot, req.RunID, wave.TicketIDs)
		if err != nil {
			return result, err
		}

		ticketDetails := buildWaveTicketDetails(req.RepoRoot, req.RunID, wave.TicketIDs, outcomes)
		acceptedWave, acceptErr := AcceptWave(WaveAcceptanceRequest{
			Wave:                 wave,
			TicketPhases:         ticketPhases,
			ChangedFiles:         changedFiles,
			TicketScopes:         ticketScopes,
			ClaimsReleased:       claimsReleased,
			PersistenceSucceeded: true,
			TicketDetails:        ticketDetails,
		})
		if waveFailed {
			acceptedWave.Status = state.WaveStatusFailed
			if acceptedWave.Acceptance == nil {
				acceptedWave.Acceptance = map[string]any{}
			}
			if waveErr != nil {
				acceptedWave.Acceptance["crash_reason"] = waveErr.Error()
			}
		}
		if acceptedWave.Acceptance == nil {
			acceptedWave.Acceptance = map[string]any{}
		}
		acceptedWave.Acceptance["baseline_changed_files"] = append([]string(nil), waveBaselineChangedFiles...)
		acceptedWave.UpdatedAt = time.Now().UTC()
		if acceptedWave.FinishedAt.IsZero() {
			acceptedWave.FinishedAt = acceptedWave.UpdatedAt
		}
		if err := state.SaveJSONAtomic(wavePath, acceptedWave); err != nil {
			return result, err
		}
		closedCount := countClosedTickets(outcomes)
		blockedIDs := collectBlockedTicketIDs(outcomes)
		blockedDetails := waveBlockedTicketEvents(blockedIDs, ticketPhases, wave.TicketIDs, ticketDetails)
		SendProgress(ctx, req.Progress, ProgressEvent{
			Type:                 EventWaveCompleted,
			WaveID:               waveOrdinal,
			Closed:               closedCount,
			Total:                len(wave.TicketIDs),
			Success:              acceptedWave.Status == state.WaveStatusAccepted,
			BlockedTickets:       blockedIDs,
			BlockedTicketDetails: blockedDetails,
		})
		result.Waves = append(result.Waves, acceptedWave)
		result.Run.WaveIDs = append(result.Run.WaveIDs, acceptedWave.WaveID)
		result.Run.UpdatedAt = time.Now().UTC()

		// Ticket store is updated by RunTicket's defer for normal completions.
		// Only handle crashed tickets (panicked goroutines that never entered RunTicket).
		for i, outcome := range outcomes {
			if outcome.err != nil && outcome.phase != state.TicketPhaseClosed && outcome.phase != state.TicketPhaseBlocked {
				_ = updateTicketStoreStatus(req.RepoRoot, wave.TicketIDs[i], tkmd.StatusOpen)
			}
		}

		if acceptErr != nil || waveFailed {
			result.Run.Status = state.EpicRunStatusBlocked
			result.Run.CurrentPhase = state.TicketPhaseBlocked
			blockErr := acceptErr
			if waveFailed && blockErr == nil {
				blockErr = fmt.Errorf("%w: wave %s had ticket failures", ErrEpicBlocked, waveID)
			}
			if err := state.SaveJSONAtomic(runPath, result.Run); err != nil {
				return result, errors.Join(blockErr, fmt.Errorf("persist run state: %w", err))
			}
			// Wrap the wave failure in BlockedRunError so the CLI can render the
			// same retry guidance it uses for the "no ready tickets" branch.
			// Re-read the current ticket store so block reasons reflect the state
			// written by each ticket's finalization defer.
			blockedChildren, childErr := listEpicChildren(req.RepoRoot, req.RootTicketID)
			if childErr != nil {
				return result, blockErr
			}
			return result, &BlockedRunError{
				RunID:          req.RunID,
				Status:         state.EpicRunStatusBlocked,
				BlockedTickets: collectBlockedTickets(req.RepoRoot, req.RunID, blockedChildren),
				Cause:          blockErr,
			}
		}

		// Run wave-level verification after all tickets merge. Mark pending in
		// the cursor first so a crash during repair is detectable on resume.
		if acceptedWave.Status == state.WaveStatusAccepted {
			setPendingWaveVerification(result.Run.ResumeCursor, acceptedWave.WaveID)
			if err := state.SaveJSONAtomic(runPath, result.Run); err != nil {
				return result, err
			}
			if verifyErr := runWaveVerificationLoop(ctx, req, cfg, &acceptedWave, wavePath, changedFiles); verifyErr != nil {
				result.Run.Status = state.EpicRunStatusBlocked
				result.Run.CurrentPhase = state.TicketPhaseBlocked
				if saveErr := state.SaveJSONAtomic(runPath, result.Run); saveErr != nil {
					return result, errors.Join(verifyErr, fmt.Errorf("persist run state: %w", saveErr))
				}
				return result, verifyErr
			}
			clearPendingWaveVerification(result.Run.ResumeCursor)
			result.Waves[len(result.Waves)-1] = acceptedWave
		}

		if result.Run.ResumeCursor != nil {
			result.Run.ResumeCursor["wave_ordinal"] = waveOrdinal
			result.Run.ResumeCursor["last_wave_base_commit"] = wave.WaveBaseCommit
		}
		if err := state.SaveJSONAtomic(runPath, result.Run); err != nil {
			return result, err
		}
	}
}

func validateRunEpicRequest(req RunEpicRequest) error {
	if req.RepoRoot == "" {
		return fmt.Errorf("run epic requires repo root")
	}
	if req.RunID == "" {
		return fmt.Errorf("run epic requires run id")
	}
	if req.RootTicketID == "" {
		return fmt.Errorf("run epic requires root ticket id")
	}
	if req.Adapter == nil && req.AdapterFactory == nil {
		return fmt.Errorf("run epic requires runtime adapter")
	}
	return nil
}

func normalizeEpicConfig(cfg policy.Config) policy.Config {
	if cfg.Scheduler.MaxConcurrency <= 0 {
		cfg.Scheduler.MaxConcurrency = policy.DefaultConfig().Scheduler.MaxConcurrency
	}
	if cfg.Scheduler.MaxDepth <= 0 {
		cfg.Scheduler.MaxDepth = policy.DefaultConfig().Scheduler.MaxDepth
	}
	return cfg
}

func listEpicChildren(repoRoot, parentID string) ([]tkmd.Ticket, error) {
	return tkmd.ListAllChildren(repoRoot, parentID)
}

func ticketIDs(tickets []tkmd.Ticket) []string {
	ids := make([]string, 0, len(tickets))
	for _, ticket := range tickets {
		ids = append(ids, ticket.ID)
	}
	sort.Strings(ids)
	return ids
}

func epicCompletionStatus(children []tkmd.Ticket) state.EpicRunStatus {
	anyReady := false
	anyBlocked := false
	for _, ticket := range children {
		switch ticket.Status {
		case tkmd.StatusClosed:
		case tkmd.StatusBlocked:
			anyBlocked = true
		default:
			anyReady = true
		}
	}
	switch {
	case anyBlocked:
		return state.EpicRunStatusBlocked
	case anyReady:
		return state.EpicRunStatusWaitingOnLeases
	default:
		return state.EpicRunStatusCompleted
	}
}

func updateTicketStoreStatus(repoRoot, ticketID string, status tkmd.Status) error {
	path := filepath.Join(repoRoot, ".tickets", ticketID+".md")
	return updateTicketStatus(path, status)
}

func updateTicketStatus(path string, status tkmd.Status) error {
	ticket, err := tkmd.LoadTicket(path)
	if err != nil {
		return err
	}
	ticket.Status = status
	return tkmd.SaveTicket(path, ticket)
}

func loadEpicTicket(repoRoot, ticketID string) (tkmd.Ticket, error) {
	return tkmd.LoadTicket(filepath.Join(repoRoot, ".tickets", ticketID+".md"))
}

func verificationCommandsFor(req RunEpicRequest, ticket tkmd.Ticket) []string {
	if commands, ok := req.VerificationByTicket[ticket.ID]; ok && len(commands) > 0 {
		return append([]string(nil), commands...)
	}
	if len(ticket.ValidationCommands) > 0 {
		return append([]string(nil), ticket.ValidationCommands...)
	}
	return []string{"true"}
}

func waveClaimsReleased(repoRoot, runID string, ticketIDs []string) (bool, error) {
	for _, ticketID := range ticketIDs {
		claimPath := filepath.Join(repoRoot, ".verk", "runs", runID, "claims", "claim-"+ticketID+".json")
		var claim state.ClaimArtifact
		if err := state.LoadJSON(claimPath, &claim); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return false, nil
			}
			return false, err
		}
		if claim.State != "released" {
			return false, nil
		}
	}
	return true, nil
}

func filterEngineOwnedFiles(changed []string) []string {
	if len(changed) == 0 {
		return nil
	}
	out := make([]string, 0, len(changed))
	for _, file := range changed {
		switch {
		case strings.HasPrefix(file, ".verk/"):
			continue
		case strings.HasPrefix(file, ".tickets/.claims/"):
			continue
		case strings.HasPrefix(file, ".tickets/"):
			continue
		}
		out = append(out, file)
	}
	return out
}

type waveTicketOutcome struct {
	ticketID string
	phase    state.TicketPhase
	err      error
}

// subEpicRegistrar serializes run-metadata updates from concurrent sub-epic
// goroutines. Multiple tickets in the same wave may have children and therefore
// run their own mini-epic loops in parallel; this registrar coordinates their
// writes to Run.WaveIDs, Run.TicketIDs, and the persisted run.json.
type subEpicRegistrar struct {
	mu      sync.Mutex
	run     *state.RunArtifact
	runPath string
}

// registerSubWave atomically appends subWaveID and descendantIDs to the shared
// run artifact and persists it. This makes sub-wave and descendant-ticket
// metadata durable before sub-wave execution begins, so resume can reconstruct
// state after a crash mid-sub-wave.
func (r *subEpicRegistrar) registerSubWave(subWaveID string, descendantIDs []string) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.run.WaveIDs = appendIfMissing(r.run.WaveIDs, subWaveID)
	for _, id := range descendantIDs {
		r.run.TicketIDs = appendIfMissing(r.run.TicketIDs, id)
	}
	r.run.UpdatedAt = time.Now().UTC()
	return state.SaveJSONAtomic(r.runPath, *r.run)
}

// setPendingSubWaveVerification marks a sub-wave as pending verification in
// the run's resume cursor and persists the run artifact. This matches the
// top-level wave path so a crash mid-repair is recoverable via resume.
func (r *subEpicRegistrar) setPendingSubWaveVerification(waveID string) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.run.ResumeCursor == nil {
		r.run.ResumeCursor = map[string]any{}
	}
	setPendingWaveVerification(r.run.ResumeCursor, waveID)
	r.run.UpdatedAt = time.Now().UTC()
	return state.SaveJSONAtomic(r.runPath, *r.run)
}

// clearPendingSubWaveVerification clears the pending-verification marker and
// persists the run artifact, matching the top-level wave path after a
// successful verification loop.
func (r *subEpicRegistrar) clearPendingSubWaveVerification() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	clearPendingWaveVerification(r.run.ResumeCursor)
	r.run.UpdatedAt = time.Now().UTC()
	return state.SaveJSONAtomic(r.runPath, *r.run)
}

// buildTicketScopes creates a map of ticket ID -> owned paths from a ticket list.
// This is used for per-ticket scope validation during wave acceptance.
func buildTicketScopes(tickets []tkmd.Ticket) map[string][]string {
	scopes := make(map[string][]string, len(tickets))
	for _, t := range tickets {
		scopes[t.ID] = t.OwnedPaths
	}
	return scopes
}

func executeWithRecovery(ctx context.Context, req RunEpicRequest, cfg policy.Config, wave state.WaveArtifact, ticketID string, depth int, ancestors map[string]struct{}, reg *subEpicRegistrar) (outcome waveTicketOutcome, crashed bool) {
	defer func() {
		if r := recover(); r != nil {
			outcome = waveTicketOutcome{
				ticketID: ticketID,
				phase:    state.TicketPhaseImplement,
				err:      fmt.Errorf("ticket goroutine crashed: %v", r),
			}
			crashed = true
		}
	}()
	return executeEpicTicket(ctx, req, cfg, wave, ticketID, depth, ancestors, reg), false
}

// runChildWithRetry executes a single child ticket with crash-recovery retries.
// It is called from a goroutine inside runSubEpic and returns the final outcome
// for the child ticket.
func runChildWithRetry(ctx context.Context, req RunEpicRequest, cfg policy.Config, subWave state.WaveArtifact, childID string, depth int, nextAncestors map[string]struct{}, reg *subEpicRegistrar) waveTicketOutcome {
	const maxCrashRetries = 2
	for attempt := 0; attempt <= maxCrashRetries; attempt++ {
		childOutcome, crashed := executeWithRecovery(ctx, req, cfg, subWave, childID, depth+1, nextAncestors, reg)
		if !crashed {
			return childOutcome
		}
		SendProgress(ctx, req.Progress, ProgressEvent{
			Type:     EventTicketDetail,
			TicketID: childID,
			Detail:   fmt.Sprintf("sub-ticket worker crashed (attempt %d/%d), retrying: %v", attempt+1, maxCrashRetries+1, childOutcome.err),
		})
		_ = tkmd.ReleaseClaim(req.RepoRoot, req.RunID, childID, "crash recovery")
		if attempt == maxCrashRetries {
			return waveTicketOutcome{ticketID: childID, phase: state.TicketPhaseBlocked}
		}
	}
	return waveTicketOutcome{ticketID: childID, phase: state.TicketPhaseBlocked}
}

// resetOrphanedChildren resets any children left in_progress from a prior crashed run
// back to open so they are eligible for rescheduling.
func resetOrphanedChildren(repoRoot string, children []tkmd.Ticket) error {
	for _, child := range children {
		if child.Status == tkmd.StatusInProgress {
			if err := updateTicketStoreStatus(repoRoot, child.ID, tkmd.StatusOpen); err != nil {
				return fmt.Errorf("reset orphaned child %s: %w", child.ID, err)
			}
		}
	}
	return nil
}

// runSubEpic executes a mini epic loop for the children of a ticket.
// It discovers children, runs them in waves (up to the configured concurrency),
// and returns a waveTicketOutcome reflecting the aggregate result.
// The parent ticket is not run here — the caller runs it after sub-tickets complete.
// ancestors is the set of epic IDs already in the current call chain; it is used
// to detect circular epic-child relationships and return a blocked outcome early.
func runSubEpic(ctx context.Context, req RunEpicRequest, cfg policy.Config, parentTicketID string, parentWave state.WaveArtifact, depth int, ancestors map[string]struct{}, reg *subEpicRegistrar) waveTicketOutcome { //nolint:gocognit,cyclop // orchestration loop mirroring RunEpic; refactored hotspots live in helpers
	outcome := waveTicketOutcome{ticketID: parentTicketID, phase: state.TicketPhaseBlocked}

	// Cycle guard: if this ticket already appears in the ancestor chain we have
	// a circular epic-child relationship. Return a blocked outcome immediately so
	// the engine can record the state without recursing.
	if err := tkmd.DetectEpicCycle(parentTicketID, ancestors); err != nil {
		outcome.err = fmt.Errorf("runSubEpic blocked: %w", err)
		return outcome
	}

	// Build the ancestors set for the next level: add the current ticket so that
	// any descendant that references it is flagged as a cycle.
	nextAncestors := make(map[string]struct{}, len(ancestors)+1)
	for k := range ancestors {
		nextAncestors[k] = struct{}{}
	}
	nextAncestors[parentTicketID] = struct{}{}

	SendProgress(ctx, req.Progress, ProgressEvent{
		Type:     EventTicketDetail,
		TicketID: parentTicketID,
		Detail:   fmt.Sprintf("running sub-tickets at depth %d", depth+1),
	})

	children, err := tkmd.ListAllChildren(req.RepoRoot, parentTicketID)
	if err != nil {
		outcome.err = fmt.Errorf("list children of %s: %w", parentTicketID, err)
		return outcome
	}
	if len(children) == 0 {
		// No children — nothing to do, let the parent run as a flat ticket.
		outcome.phase = state.TicketPhaseClosed
		return outcome
	}

	// Reset orphaned in_progress children from crashed prior runs.
	if err := resetOrphanedChildren(req.RepoRoot, children); err != nil {
		outcome.err = err
		return outcome
	}

	// Reuse a single git.Repo handle across iterations to compute per-sub-wave
	// baselines and changed file sets, mirroring the top-level RunEpic loop.
	repo, err := git.New(req.RepoRoot)
	if err != nil {
		outcome.err = fmt.Errorf("open repo for sub-epic %s: %w", parentTicketID, err)
		return outcome
	}

	// Run the mini wave loop until all children are closed or the sub-epic is blocked.
	subWaveOrdinal := 0
	for {
		if err := ctx.Err(); err != nil {
			outcome.err = err
			return outcome
		}

		ready, err := tkmd.ListReadyChildren(req.RepoRoot, parentTicketID, req.RunID)
		if err != nil {
			outcome.err = fmt.Errorf("list ready children of %s: %w", parentTicketID, err)
			return outcome
		}
		ticketScopes := buildTicketScopes(ready)
		if len(ready) == 0 {
			// Check completion status of all children.
			currentChildren, err := tkmd.ListAllChildren(req.RepoRoot, parentTicketID)
			if err != nil {
				outcome.err = err
				return outcome
			}
			status := epicCompletionStatus(currentChildren)
			switch status {
			case state.EpicRunStatusCompleted:
				outcome.phase = state.TicketPhaseClosed
			default:
				outcome.phase = state.TicketPhaseBlocked
			}
			return outcome
		}

		subWave, err := BuildWave(ready, cfg.Scheduler.MaxConcurrency)
		if err != nil {
			outcome.err = fmt.Errorf("build sub-wave for %s: %w", parentTicketID, err)
			return outcome
		}
		subWaveOrdinal++
		subWaveID := fmt.Sprintf("sub-%s-wave-%d", parentTicketID, subWaveOrdinal)
		subWave.WaveID = subWaveID
		subWave.ParentTicketID = parentTicketID
		subWave.Ordinal = subWaveOrdinal
		subWave.Status = state.WaveStatusRunning
		subWave.WaveBaseCommit = parentWave.WaveBaseCommit
		subWave.StartedAt = time.Now().UTC()

		if err := reg.registerSubWave(subWaveID, subWave.TicketIDs); err != nil {
			outcome.err = fmt.Errorf("register sub-wave %s: %w", subWaveID, err)
			return outcome
		}

		// Persist the sub-wave artifact to disk before executing, so resume can
		// reconstruct state after a crash mid-sub-wave execution.
		subWavePath := waveArtifactPath(req.RepoRoot, req.RunID, subWaveID)
		if err := state.SaveJSONAtomic(subWavePath, subWave); err != nil {
			outcome.err = fmt.Errorf("persist sub-wave %s: %w", subWaveID, err)
			return outcome
		}

		// Capture the pre-wave changed-file baseline so changes introduced by
		// this sub-wave can be isolated for per-ticket scope validation.
		subWaveBaselineChangedFiles, err := repo.ChangedFilesAgainst(subWave.WaveBaseCommit)
		if err != nil {
			outcome.err = fmt.Errorf("baseline changed files for sub-wave %s: %w", subWaveID, err)
			outcome.phase = state.TicketPhaseBlocked
			return outcome
		}
		subWaveBaselineChangedFiles = filterEngineOwnedFiles(subWaveBaselineChangedFiles)

		SendProgress(ctx, req.Progress, ProgressEvent{
			Type:           EventWaveStarted,
			WaveID:         subWaveOrdinal,
			ParentTicketID: parentTicketID,
			Tickets:        append([]string(nil), subWave.TicketIDs...),
		})

		outcomes := make([]waveTicketOutcome, len(subWave.TicketIDs))
		var wg sync.WaitGroup
		for i, childID := range subWave.TicketIDs {
			wg.Add(1)
			i := i
			childID := childID
			go func() {
				defer wg.Done()
				outcomes[i] = runChildWithRetry(ctx, req, cfg, subWave, childID, depth, nextAncestors, reg)
			}()
		}
		wg.Wait()

		ticketPhases := make([]state.TicketPhase, len(outcomes))
		allClosed := true
		waveFailed := false
		var waveErr error
		for i, o := range outcomes {
			ticketPhases[i] = o.phase
			if o.phase != state.TicketPhaseClosed {
				allClosed = false
			}
			if o.err != nil {
				waveFailed = true
				if waveErr == nil {
					waveErr = o.err
				}
			}
		}

		// Compute the files this sub-wave actually touched so per-ticket scope
		// and baseline accounting mirror the top-level wave gates.
		changedFiles, err := repo.ChangedFilesAgainst(subWave.WaveBaseCommit)
		if err != nil {
			outcome.err = fmt.Errorf("list changed files for sub-wave %s: %w", subWaveID, err)
			outcome.phase = state.TicketPhaseBlocked
			return outcome
		}
		changedFiles = filterEngineOwnedFiles(changedFiles)
		changedFiles = subtractFiles(changedFiles, subWaveBaselineChangedFiles)

		claimsReleased, err := waveClaimsReleased(req.RepoRoot, req.RunID, subWave.TicketIDs)
		if err != nil {
			outcome.err = fmt.Errorf("check claims released for sub-wave %s: %w", subWaveID, err)
			outcome.phase = state.TicketPhaseBlocked
			return outcome
		}

		subTicketDetails := buildWaveTicketDetails(req.RepoRoot, req.RunID, subWave.TicketIDs, outcomes)
		acceptedSubWave, acceptErr := AcceptWave(WaveAcceptanceRequest{
			Wave:                 subWave,
			TicketPhases:         ticketPhases,
			ChangedFiles:         changedFiles,
			TicketScopes:         ticketScopes,
			ClaimsReleased:       claimsReleased,
			PersistenceSucceeded: true,
			TicketDetails:        subTicketDetails,
		})
		if waveFailed {
			acceptedSubWave.Status = state.WaveStatusFailed
			if acceptedSubWave.Acceptance == nil {
				acceptedSubWave.Acceptance = map[string]any{}
			}
			if waveErr != nil {
				acceptedSubWave.Acceptance["crash_reason"] = waveErr.Error()
			}
		}
		// The parent-ticket contract is that every child must close before the
		// sub-wave is accepted. AcceptWave treats blocked tickets as a soft
		// warning, so override to Failed when any child did not close.
		if !allClosed && acceptedSubWave.Status == state.WaveStatusAccepted {
			acceptedSubWave.Status = state.WaveStatusFailed
		}
		if acceptedSubWave.Acceptance == nil {
			acceptedSubWave.Acceptance = map[string]any{}
		}
		acceptedSubWave.Acceptance["baseline_changed_files"] = append([]string(nil), subWaveBaselineChangedFiles...)
		now := time.Now().UTC()
		acceptedSubWave.UpdatedAt = now
		if acceptedSubWave.FinishedAt.IsZero() {
			acceptedSubWave.FinishedAt = now
		}
		if err := state.SaveJSONAtomic(subWavePath, acceptedSubWave); err != nil {
			outcome.err = fmt.Errorf("persist sub-wave %s: %w", subWaveID, err)
			outcome.phase = state.TicketPhaseBlocked
			return outcome
		}

		subBlockedIDs := collectBlockedTicketIDs(outcomes)
		subBlockedDetails := waveBlockedTicketEvents(subBlockedIDs, ticketPhases, subWave.TicketIDs, subTicketDetails)
		SendProgress(ctx, req.Progress, ProgressEvent{
			Type:                 EventWaveCompleted,
			WaveID:               subWaveOrdinal,
			ParentTicketID:       parentTicketID,
			Closed:               countClosedTickets(outcomes),
			Total:                len(outcomes),
			Success:              acceptedSubWave.Status == state.WaveStatusAccepted,
			BlockedTickets:       subBlockedIDs,
			BlockedTicketDetails: subBlockedDetails,
		})

		if acceptErr != nil {
			outcome.err = acceptErr
			outcome.phase = state.TicketPhaseBlocked
			return outcome
		}
		if waveFailed {
			outcome.phase = state.TicketPhaseBlocked
			if outcome.err == nil {
				outcome.err = waveErr
			}
			return outcome
		}
		if !allClosed {
			outcome.phase = state.TicketPhaseBlocked
			return outcome
		}

		// All children closed and the sub-wave was accepted — run wave-level
		// verification under the same pending-marker discipline as top-level
		// waves so a crash mid-repair is recoverable on resume.
		if acceptedSubWave.Status == state.WaveStatusAccepted {
			if err := reg.setPendingSubWaveVerification(acceptedSubWave.WaveID); err != nil {
				outcome.err = fmt.Errorf("mark sub-wave %s pending verification: %w", subWaveID, err)
				outcome.phase = state.TicketPhaseBlocked
				return outcome
			}
			if verifyErr := runWaveVerificationLoop(ctx, req, cfg, &acceptedSubWave, subWavePath, changedFiles); verifyErr != nil {
				outcome.err = verifyErr
				outcome.phase = state.TicketPhaseBlocked
				return outcome
			}
			if err := reg.clearPendingSubWaveVerification(); err != nil {
				outcome.err = fmt.Errorf("clear sub-wave %s pending verification: %w", subWaveID, err)
				outcome.phase = state.TicketPhaseBlocked
				return outcome
			}
		}
	}
}

func executeEpicTicket(ctx context.Context, req RunEpicRequest, cfg policy.Config, wave state.WaveArtifact, ticketID string, depth int, ancestors map[string]struct{}, reg *subEpicRegistrar) waveTicketOutcome {
	outcome := waveTicketOutcome{ticketID: ticketID, phase: state.TicketPhaseBlocked}

	ticket, err := loadEpicTicket(req.RepoRoot, ticketID)
	if err != nil {
		outcome.err = err
		return outcome
	}

	// If this ticket has children and we haven't exceeded max depth, run the
	// sub-tickets first (recursive mini-epic loop), then run the parent ticket.
	// cfg has already been run through normalizeEpicConfig at the top of RunEpic,
	// so cfg.Scheduler.MaxDepth is guaranteed positive here.
	maxDepth := cfg.Scheduler.MaxDepth
	hasChildren, err := tkmd.HasChildren(req.RepoRoot, ticketID)
	if err != nil {
		outcome.err = fmt.Errorf("check children of %s: %w", ticketID, err)
		return outcome
	}
	if hasChildren && depth < maxDepth {
		subOutcome := runSubEpic(ctx, req, cfg, ticketID, wave, depth, ancestors, reg)
		if subOutcome.err != nil {
			return subOutcome
		}
		// If sub-tickets didn't all close, the parent can't proceed.
		// Persist the blocked state durably so the parent ticket is not eligible
		// for rescheduling in a subsequent wave while the descendant remains
		// unresolved. Without this, the outer run loop would find the parent
		// still open/ready and schedule it again, creating a repeated-wave loop.
		if subOutcome.phase != state.TicketPhaseClosed {
			if updateErr := updateTicketStoreStatus(req.RepoRoot, ticketID, tkmd.StatusBlocked); updateErr != nil {
				subOutcome.err = fmt.Errorf("persist blocked status for %s: %w", ticketID, updateErr)
			}
			return subOutcome
		}
		SendProgress(ctx, req.Progress, ProgressEvent{
			Type:     EventTicketDetail,
			TicketID: ticketID,
			Detail:   fmt.Sprintf("sub-tickets completed at depth %d, running parent", depth+1),
		})
	}

	plan, err := BuildPlanArtifact(ticket, cfg)
	if err != nil {
		outcome.err = err
		return outcome
	}
	claim, err := tkmd.AcquireClaim(req.RepoRoot, req.RunID, ticket.ID, fmt.Sprintf("lease-%s-%s-%s", req.RunID, ticket.ID, wave.WaveID), wave.WaveID, 10*time.Minute, time.Now().UTC())
	if err != nil {
		outcome.err = err
		return outcome
	}
	adapter, err := adapterForEpicTicket(req, plan)
	if err != nil {
		_ = tkmd.ReleaseClaim(req.RepoRoot, req.RunID, ticket.ID, claim.LeaseID, "runtime adapter selection failed")
		outcome.err = err
		return outcome
	}

	ticketResult, err := RunTicket(ctx, RunTicketRequest{
		RepoRoot:             req.RepoRoot,
		RunID:                req.RunID,
		BaseCommit:           wave.WaveBaseCommit,
		Ticket:               ticket,
		Plan:                 plan,
		Claim:                claim,
		Adapter:              adapter,
		Config:               cfg,
		VerificationCommands: verificationCommandsFor(req, ticket),
		Progress:             req.Progress,
	})
	if err != nil {
		outcome.err = err
		return outcome
	}
	outcome.phase = ticketResult.Snapshot.CurrentPhase
	return outcome
}

func adapterForEpicTicket(req RunEpicRequest, plan state.PlanArtifact) (runtime.Adapter, error) {
	if req.AdapterFactory != nil {
		return req.AdapterFactory(chosenRuntime(plan, req.Config))
	}
	if req.Adapter != nil {
		return req.Adapter, nil
	}
	return nil, fmt.Errorf("run epic requires runtime adapter")
}

func subtractFiles(changed, baseline []string) []string {
	if len(changed) == 0 {
		return nil
	}
	if len(baseline) == 0 {
		return append([]string(nil), changed...)
	}
	blocked := make(map[string]struct{}, len(baseline))
	for _, file := range baseline {
		blocked[file] = struct{}{}
	}
	out := make([]string, 0, len(changed))
	for _, file := range changed {
		if _, ok := blocked[file]; ok {
			continue
		}
		out = append(out, file)
	}
	return out
}

func describeNotReady(ticket tkmd.Ticket) string {
	switch ticket.Status {
	case tkmd.StatusClosed:
		return "closed"
	case tkmd.StatusBlocked:
		return "blocked"
	case tkmd.StatusInProgress:
		return "in_progress"
	}
	// Status is open/ready — must be deps not resolved
	if len(ticket.Deps) > 0 {
		unresolved := make([]string, 0, len(ticket.Deps))
		unresolved = append(unresolved, ticket.Deps...)
		if len(unresolved) <= 3 {
			return fmt.Sprintf("waiting on deps: %s", strings.Join(unresolved, ", "))
		}
		return fmt.Sprintf("waiting on %d deps", len(unresolved))
	}
	return fmt.Sprintf("status=%s", string(ticket.Status))
}

func countClosedTickets(outcomes []waveTicketOutcome) int {
	count := 0
	for _, o := range outcomes {
		if o.phase == state.TicketPhaseClosed {
			count++
		}
	}
	return count
}

// collectBlockedTicketIDs returns the IDs of wave tickets that did not reach
// TicketPhaseClosed, preserving the original wave ordering. Returns nil when
// every ticket closed.
func collectBlockedTicketIDs(outcomes []waveTicketOutcome) []string {
	var blocked []string
	for _, o := range outcomes {
		if o.phase != state.TicketPhaseClosed {
			blocked = append(blocked, o.ticketID)
		}
	}
	return blocked
}

// buildWaveTicketDetails resolves the per-ticket BlockedTicketSummary fields
// the wave needs to write a clear "ticket X blocked because Y" entry. It
// reads each ticket snapshot for its block reason and falls back to phase-
// based defaults when the snapshot is missing or empty. The returned map is
// keyed by ticket ID; tickets that closed normally are still recorded so
// AcceptWave can pick up titles for any later "blocked" surfacing.
func buildWaveTicketDetails(repoRoot, runID string, ticketIDs []string, outcomes []waveTicketOutcome) map[string]WaveTicketDetail {
	details := make(map[string]WaveTicketDetail, len(ticketIDs))
	outcomeByID := make(map[string]waveTicketOutcome, len(outcomes))
	for _, o := range outcomes {
		outcomeByID[o.ticketID] = o
	}
	for _, id := range ticketIDs {
		detail := WaveTicketDetail{}
		// Prefer the worker- or engine-recorded BlockReason from the
		// per-ticket snapshot, which captures both worker-side
		// needs_context strings and engine-side non-convergent reasons.
		var snap TicketRunSnapshot
		if err := loadTicketSnapshot(repoRoot, runID, id, &snap); err == nil {
			if reason := strings.TrimSpace(snap.BlockReason); reason != "" {
				detail.BlockReason = reason
			}
		}
		// Match the snapshot's title via the ticket store if we can.
		if t, err := loadEpicTicket(repoRoot, id); err == nil {
			detail.Title = t.Title
		}
		// A canonical non-convergent escalation always requires operator
		// input — verk's automated retry loop has already given up.
		if strings.Contains(detail.BlockReason, string(state.EscalationNonConvergentVerification)) ||
			strings.Contains(detail.BlockReason, string(state.EscalationNonConvergentReview)) {
			detail.RequiresOperator = true
		}
		// Outcomes track in-memory phases — anything stuck in a non-terminal
		// phase is still safe for `verk run` to pick up automatically.
		if o, ok := outcomeByID[id]; ok && o.phase != state.TicketPhaseClosed && o.phase != state.TicketPhaseBlocked {
			detail.CanRetryAutomatically = true
		}
		details[id] = detail
	}
	return details
}

// waveBlockedTicketEvents builds the BlockedTicketDetails slice carried on
// EventWaveCompleted progress events. The slice mirrors the wave-level
// blocked_ticket_details acceptance entry so downstream consumers (TUI, log
// scrapers, automation) can render explicit per-ticket explanations without
// re-reading the wave artifact from disk.
func waveBlockedTicketEvents(blockedIDs []string, ticketPhases []state.TicketPhase, ticketIDs []string, details map[string]WaveTicketDetail) []BlockedTicketSummary {
	if len(blockedIDs) == 0 {
		return nil
	}
	phaseByID := make(map[string]state.TicketPhase, len(ticketIDs))
	for i, id := range ticketIDs {
		if i >= len(ticketPhases) {
			break
		}
		phaseByID[id] = ticketPhases[i]
	}
	out := make([]BlockedTicketSummary, 0, len(blockedIDs))
	for _, id := range blockedIDs {
		out = append(out, buildBlockedTicketSummary(id, phaseByID[id], details[id]))
	}
	return out
}

// emitBlockedEpicSummary emits a single progress event summarising the blocked
// or waiting tickets that prevented the epic from completing. Per-ticket
// EventTicketDetail lines still describe each child individually; this
// additional one-line summary makes the terminating state visible at a glance
// even when those per-ticket lines have scrolled off the activity log.
func emitBlockedEpicSummary(ctx context.Context, ch chan<- ProgressEvent, children []tkmd.Ticket) {
	if ch == nil {
		return
	}
	var parts []string
	for _, child := range children {
		if child.Status == tkmd.StatusClosed {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s (%s)", child.ID, describeNotReady(child)))
	}
	if len(parts) == 0 {
		return
	}
	SendProgress(ctx, ch, ProgressEvent{
		Type:   EventTicketDetail,
		Detail: fmt.Sprintf("epic blocked — %d ticket(s) not closed: %s", len(parts), strings.Join(parts, ", ")),
	})
}
