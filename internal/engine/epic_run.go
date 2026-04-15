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

func RunEpic(ctx context.Context, req RunEpicRequest) (RunEpicResult, error) {
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
			SendProgress(req.Progress, ProgressEvent{
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
			_ = state.SaveJSONAtomic(runPath, result.Run)
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
				SendProgress(req.Progress, ProgressEvent{
					Type:   EventTicketDetail,
					Detail: fmt.Sprintf("no child tickets found for epic %s", req.RootTicketID),
				})
			} else {
				for _, child := range currentChildren {
					reason := describeNotReady(child)
					SendProgress(req.Progress, ProgressEvent{
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
				return result, fmt.Errorf("%w: %s", ErrEpicBlocked, status)
			}
			return result, nil
		}

		wave, err := BuildWave(ready, cfg.Scheduler.MaxConcurrency)
		if err != nil {
			result.Run.Status = state.EpicRunStatusBlocked
			result.Run.CurrentPhase = state.TicketPhaseBlocked
			result.Run.UpdatedAt = time.Now().UTC()
			_ = state.SaveJSONAtomic(runPath, result.Run)
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
		SendProgress(req.Progress, ProgressEvent{
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
					outcome, crashed := executeWithRecovery(ctx, req, cfg, wave, ticketID)
					if !crashed {
						outcomes[i] = outcome
						return
					}
					SendProgress(req.Progress, ProgressEvent{
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

		acceptedWave, acceptErr := AcceptWave(WaveAcceptanceRequest{
			Wave:                 wave,
			TicketPhases:         ticketPhases,
			ChangedFiles:         changedFiles,
			TicketScopes:         ticketScopes,
			ClaimsReleased:       claimsReleased,
			PersistenceSucceeded: true,
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
		SendProgress(req.Progress, ProgressEvent{
			Type:    EventWaveCompleted,
			WaveID:  waveOrdinal,
			Closed:  closedCount,
			Total:   len(wave.TicketIDs),
			Success: acceptedWave.Status == state.WaveStatusAccepted,
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

		if acceptErr != nil {
			result.Run.Status = state.EpicRunStatusBlocked
			result.Run.CurrentPhase = state.TicketPhaseBlocked
			if err := state.SaveJSONAtomic(runPath, result.Run); err != nil {
				return result, err
			}
			return result, acceptErr
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
				_ = state.SaveJSONAtomic(runPath, result.Run)
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
	return cfg
}

func listEpicChildren(repoRoot, parentID string) ([]tkmd.Ticket, error) {
	// Load the epic ticket to get its deps/links list for child discovery.
	epicPath := filepath.Join(repoRoot, ".tickets", parentID+".md")
	epicTicket, err := tkmd.LoadTicket(epicPath)
	if err != nil {
		return nil, fmt.Errorf("load epic %s: %w", parentID, err)
	}
	epicChildren := make(map[string]struct{})
	for _, dep := range epicTicket.Deps {
		epicChildren[dep] = struct{}{}
	}
	if epicTicket.UnknownFrontmatter != nil {
		if links, ok := epicTicket.UnknownFrontmatter["links"]; ok {
			for _, link := range toStringSlice(links) {
				epicChildren[link] = struct{}{}
			}
		}
	}

	paths, err := filepath.Glob(filepath.Join(repoRoot, ".tickets", "*.md"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)

	children := make([]tkmd.Ticket, 0, len(paths))
	seen := make(map[string]struct{})
	for _, path := range paths {
		ticket, err := tkmd.LoadTicket(path)
		if err != nil {
			return nil, err
		}
		if ticket.ID == parentID {
			continue
		}
		// Child if: has parent field, OR is in epic's deps list, OR is in epic's links list
		isChild := ticketParent(&ticket) == parentID
		if !isChild {
			_, isChild = epicChildren[ticket.ID]
		}
		if !isChild {
			continue
		}
		if _, ok := seen[ticket.ID]; ok {
			continue
		}
		seen[ticket.ID] = struct{}{}
		children = append(children, ticket)
	}
	return children, nil
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

// buildTicketScopes creates a map of ticket ID -> owned paths from a ticket list.
// This is used for per-ticket scope validation during wave acceptance.
func buildTicketScopes(tickets []tkmd.Ticket) map[string][]string {
	scopes := make(map[string][]string, len(tickets))
	for _, t := range tickets {
		scopes[t.ID] = t.OwnedPaths
	}
	return scopes
}

func executeWithRecovery(ctx context.Context, req RunEpicRequest, cfg policy.Config, wave state.WaveArtifact, ticketID string) (outcome waveTicketOutcome, crashed bool) {
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
	return executeEpicTicket(ctx, req, cfg, wave, ticketID), false
}

func executeEpicTicket(ctx context.Context, req RunEpicRequest, cfg policy.Config, wave state.WaveArtifact, ticketID string) waveTicketOutcome {
	outcome := waveTicketOutcome{ticketID: ticketID, phase: state.TicketPhaseBlocked}

	ticket, err := loadEpicTicket(req.RepoRoot, ticketID)
	if err != nil {
		outcome.err = err
		return outcome
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

func ticketParent(ticket *tkmd.Ticket) string {
	if ticket == nil || ticket.UnknownFrontmatter == nil {
		return ""
	}
	parent, _ := ticket.UnknownFrontmatter["parent"].(string)
	return parent
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
		unresolved := make([]string, 0)
		unresolved = append(unresolved, ticket.Deps...)
		if len(unresolved) <= 3 {
			return fmt.Sprintf("waiting on deps: %s", strings.Join(unresolved, ", "))
		}
		return fmt.Sprintf("waiting on %d deps", len(unresolved))
	}
	return fmt.Sprintf("status=%s", string(ticket.Status))
}

func toStringSlice(value any) []string {
	switch v := value.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
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
