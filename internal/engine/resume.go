package engine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"verk/internal/adapters/runtime"
	"verk/internal/adapters/ticketstore/epos"
	"verk/internal/policy"
	"verk/internal/state"

	repoadapter "verk/internal/adapters/repo/git"
)

type ResumeRequest struct {
	RepoRoot       string
	RunID          string
	Adapter        runtime.Adapter
	AdapterFactory func(ticketPreference string) (runtime.Adapter, error)
	Config         policy.Config
	Progress       chan<- ProgressEvent
}

type ResumeReport struct {
	Run              state.RunArtifact `json:"run"`
	Status           StatusReport      `json:"status"`
	RecoveredTickets []string          `json:"recovered_tickets,omitempty"`
	ResumedTickets   []string          `json:"resumed_tickets,omitempty"`
}

func ResumeRun(ctx context.Context, req ResumeRequest) (ResumeReport, error) { //nolint:gocognit,cyclop // complex resume orchestration; refactor into sub-functions
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return ResumeReport{}, err
	}
	if req.RunID == "" {
		return ResumeReport{}, fmt.Errorf("resume requires run id")
	}
	if err := validateArtifactIdentifier(req.RunID, "run id"); err != nil {
		return ResumeReport{}, err
	}
	if req.RepoRoot == "" {
		return ResumeReport{}, fmt.Errorf("resume requires repo root")
	}

	lock, err := AcquireRunLock(req.RepoRoot, req.RunID)
	if err != nil {
		return ResumeReport{}, err
	}
	defer func() { _ = lock.Release() }()

	artifacts, err := loadRunArtifacts(req.RepoRoot, req.RunID)
	if err != nil {
		return ResumeReport{}, err
	}

	// Phase 1: Reconciliation (existing logic)
	recovered := make([]string, 0)
	for _, ticketID := range artifacts.Run.TicketIDs {
		snapshot := artifacts.Tickets[ticketID]

		_, repaired, claimErr := reconcileTicketClaimForResume(artifacts.RepoRoot, req.RunID, ticketID, snapshot)
		if claimErr != nil {
			artifacts.Run.Status = state.EpicRunStatusBlocked
			artifacts.Run.CurrentPhase = state.TicketPhaseBlocked
			appendRunAuditEvent(&artifacts.Run, "resume_claim_divergence", ticketID, snapshot.CurrentPhase, map[string]any{
				"reason": claimErr.Error(),
			})
			if err := state.SaveJSONAtomic(runJSONPath(artifacts.RepoRoot, req.RunID), artifacts.Run); err != nil {
				return ResumeReport{}, err
			}
			status, err := DeriveStatus(StatusRequest{RepoRoot: artifacts.RepoRoot, RunID: req.RunID})
			if err != nil {
				return ResumeReport{}, err
			}
			return ResumeReport{Run: artifacts.Run, Status: status}, nil
		}
		if repaired {
			recovered = append(recovered, ticketID)
		}
		sourcePhase := snapshot.CurrentPhase
		if snapshot.Closeout == nil && (sourcePhase == state.TicketPhaseCloseout || sourcePhase == state.TicketPhaseClosed) {
			plan, ok := artifacts.Plans[ticketID]
			if !ok {
				return ResumeReport{}, fmt.Errorf("resume requires plan artifact for ticket %s", ticketID)
			}
			ticket, err := epos.LoadTicket(ticketMarkdownPath(artifacts.RepoRoot, ticketID))
			if err != nil {
				return ResumeReport{}, err
			}
			if snapshot.Verification == nil || snapshot.Review == nil {
				return ResumeReport{}, fmt.Errorf("resume cannot repair closeout for ticket %s without verification and review artifacts", ticketID)
			}
			closeout, err := BuildCloseoutArtifact(ticket, plan, snapshot.Verification, snapshot.Review)
			if err != nil {
				return ResumeReport{}, err
			}
			snapshot.Closeout = &closeout
			snapshot.UpdatedAt = stateTime()
			switch sourcePhase {
			case state.TicketPhaseCloseout, state.TicketPhaseClosed:
				if closeout.Closable {
					snapshot.CurrentPhase = state.TicketPhaseClosed
					snapshot.BlockReason = ""
				} else {
					snapshot.CurrentPhase = state.TicketPhaseBlocked
					snapshot.BlockReason = closeout.FailedGate
				}
			}
			artifacts.Tickets[ticketID] = snapshot
			recovered = appendIfMissing(recovered, ticketID)
			if err := state.WriteTransitionCommit(
				state.TransitionPaths{
					TicketArtifactPath: ticketSnapshotPath(artifacts.RepoRoot, req.RunID, ticketID),
					RunArtifactPath:    runJSONPath(artifacts.RepoRoot, req.RunID),
				},
				state.TransitionPayloads{
					TicketArtifact: snapshot,
					RunArtifact:    artifacts.Run,
				},
			); err != nil {
				return ResumeReport{}, err
			}
			if err := state.SaveJSONAtomic(closeoutArtifactPath(artifacts.RepoRoot, req.RunID, ticketID), closeout); err != nil {
				return ResumeReport{}, err
			}
		}
	}

	updateRunStatusFromTickets(&artifacts.Run, artifacts.Tickets)
	if len(recovered) > 0 {
		appendRunAuditEvent(&artifacts.Run, "resume_repaired_committed_state", "", artifacts.Run.CurrentPhase, map[string]any{
			"tickets": recovered,
		})
	}
	if err := state.SaveJSONAtomic(runJSONPath(artifacts.RepoRoot, req.RunID), artifacts.Run); err != nil {
		return ResumeReport{}, err
	}

	// Phase 2: Re-execution
	var resumed []string
	if resumeExecutionAllowed(artifacts.Run, req) {
		appendRunAuditEvent(&artifacts.Run, "resume_reexecution_start", "", artifacts.Run.CurrentPhase, nil)
		if err := state.SaveJSONAtomic(runJSONPath(artifacts.RepoRoot, req.RunID), artifacts.Run); err != nil {
			return ResumeReport{}, err
		}

		switch artifacts.Run.Mode {
		case "ticket":
			resumed, err = resumeTicketMode(ctx, req, &artifacts)
		case "epic":
			resumed, err = resumeEpicMode(ctx, req, &artifacts)
		default:
			err = fmt.Errorf("unsupported run mode %q for resume", artifacts.Run.Mode)
		}
		if err != nil {
			return ResumeReport{}, err
		}

		// Re-derive run status after execution.
		// Reload ticket snapshots from disk because resumeEpicMode does not
		// update artifacts.Tickets in-place — RunTicket writes new snapshots
		// to disk via its defer, but the in-memory map stays stale.
		reloadTicketSnapshots(artifacts.RepoRoot, req.RunID, artifacts.Tickets)
		updateRunStatusFromTickets(&artifacts.Run, artifacts.Tickets)
		if len(resumed) > 0 {
			appendRunAuditEvent(&artifacts.Run, "resume_reexecution_complete", "", artifacts.Run.CurrentPhase, map[string]any{
				"tickets": resumed,
			})
		}
		if err := state.SaveJSONAtomic(runJSONPath(artifacts.RepoRoot, req.RunID), artifacts.Run); err != nil {
			return ResumeReport{}, err
		}
	}

	status, err := DeriveStatus(StatusRequest{RepoRoot: artifacts.RepoRoot, RunID: req.RunID})
	if err != nil {
		return ResumeReport{}, err
	}
	return ResumeReport{
		Run:              artifacts.Run,
		Status:           status,
		RecoveredTickets: recovered,
		ResumedTickets:   resumed,
	}, nil
}

// resumeExecutionAllowed returns true if the run has non-terminal tickets
// and the caller provided a runtime adapter for re-execution.
// Blocked runs are allowed to resume — blocked tickets will be reset to
// ready and re-executed, giving them another chance after conditions change.
func resumeExecutionAllowed(run state.RunArtifact, req ResumeRequest) bool {
	if req.Adapter == nil && req.AdapterFactory == nil {
		return false
	}
	if _, ok := pendingWaveVerificationID(run.ResumeCursor); ok {
		return true
	}
	return run.Status == state.EpicRunStatusRunning || run.Status == state.EpicRunStatusBlocked
}

// resumeTicketMode re-executes non-terminal tickets in a ticket-mode run.
func resumeTicketMode(ctx context.Context, req ResumeRequest, artifacts *runArtifacts) ([]string, error) {
	var resumed []string
	for _, ticketID := range artifacts.Run.TicketIDs {
		if err := ctx.Err(); err != nil {
			return resumed, err
		}
		snapshot := artifacts.Tickets[ticketID]
		if snapshot.CurrentPhase == state.TicketPhaseClosed {
			continue
		}

		// Release existing claim so we can re-acquire
		if err := releaseClaimForResume(artifacts.RepoRoot, req.RunID, ticketID, "resume_reacquisition"); err != nil {
			return resumed, err
		}

		// Re-acquire claim
		leaseID := fmt.Sprintf("lease-resume-%s-%d", req.RunID, time.Now().UTC().UnixNano())
		claim, err := epos.AcquireClaim(artifacts.RepoRoot, req.RunID, ticketID, leaseID, 30*time.Minute, time.Now().UTC())
		if err != nil {
			return resumed, fmt.Errorf("resume claim for ticket %s: %w", ticketID, err)
		}

		// Load ticket markdown
		ticket, err := epos.LoadTicket(ticketMarkdownPath(artifacts.RepoRoot, ticketID))
		if err != nil {
			return resumed, fmt.Errorf("load ticket %s: %w", ticketID, err)
		}
		ticket.Status = epos.StatusInProgress

		// Load or build plan
		plan, ok := artifacts.Plans[ticketID]
		if !ok {
			plan, err = BuildPlanArtifact(ticket, req.Config)
			if err != nil {
				return resumed, fmt.Errorf("build plan for ticket %s: %w", ticketID, err)
			}
			plan.RunID = req.RunID
		}
		// Resume as close as possible to the last persisted phase so
		// pending verify/review/repair work continues correctly without
		// losing provenance. Blocked tickets are intentionally moved back to
		// implement so resume can unblock under new conditions.
		if snapshot.CurrentPhase == state.TicketPhaseBlocked {
			plan.Phase = state.TicketPhaseImplement
		} else if snapshot.CurrentPhase != "" {
			plan.Phase = snapshot.CurrentPhase
		} else {
			plan.Phase = state.TicketPhaseImplement
		}

		// Resolve adapter
		adapter, err := resumeAdapter(req, plan)
		if err != nil {
			return resumed, fmt.Errorf("adapter for ticket %s: %w", ticketID, err)
		}

		// Execute ticket
		result, runErr := RunTicket(ctx, RunTicketRequest{
			RepoRoot:             artifacts.RepoRoot,
			RunID:                req.RunID,
			BaseCommit:           artifacts.Run.BaseCommit,
			Ticket:               ticket,
			Plan:                 plan,
			Claim:                claim,
			Adapter:              adapter,
			Config:               req.Config,
			VerificationCommands: plan.ValidationCommands,
			Progress:             req.Progress,
		})
		if runErr != nil {
			return resumed, fmt.Errorf("run ticket %s: %w", ticketID, runErr)
		}

		// Update artifacts with result
		artifacts.Tickets[ticketID] = result.Snapshot

		// Update ticket store
		switch result.Snapshot.CurrentPhase {
		case state.TicketPhaseClosed:
			ticket.Status = epos.StatusClosed
		case state.TicketPhaseBlocked:
			ticket.Status = epos.StatusBlocked
		default:
			ticket.Status = epos.StatusBlocked
		}
		if err := epos.SaveTicket(ticketMarkdownPath(artifacts.RepoRoot, ticketID), ticket); err != nil {
			return resumed, err
		}

		resumed = append(resumed, ticketID)
	}
	return resumed, nil
}

func releaseClaimForResume(repoRoot, runID, ticketID, reason string) error {
	if err := epos.ReleaseClaim(repoRoot, runID, ticketID, reason); err != nil && !errors.Is(err, epos.ErrClaimNotFound) {
		return fmt.Errorf("release claim for ticket %s: %w", ticketID, err)
	}
	return nil
}

// resumeEpicMode re-enters the wave loop for an epic-mode run,
// skipping already-completed waves.
func resumeEpicMode(ctx context.Context, req ResumeRequest, artifacts *runArtifacts) ([]string, error) { //nolint:gocognit,cyclop // complex resume orchestration; refactor into sub-functions
	cfg := normalizeEpicConfig(req.Config)
	workRoot, resolveErr := ResolveWorktreeRoot(artifacts.RepoRoot)
	if resolveErr != nil {
		log.Printf("[WARN] reconcile worktrees: resolve cache root: %v", resolveErr)
	} else if err := ReconcileWorktrees(ctx, artifacts.RepoRoot, workRoot); err != nil {
		log.Printf("[WARN] reconcile worktrees: %v", err)
	}
	repo, err := repoadapter.New(artifacts.RepoRoot)
	if err != nil {
		return nil, err
	}
	baseCommit, err := resolveResumeWaveBase(artifacts.RepoRoot, artifacts.Run)
	if err != nil {
		return nil, err
	}
	currentBaseRef := baseCommit

	waveOrdinal, err := maxDurableWaveOrdinal(artifacts.RepoRoot, req.RunID, artifacts.Run)
	if err != nil {
		return nil, err
	}

	// Reset incomplete tickets (including blocked) to "ready" so ListReadyChildren
	// can pick them up. Only closed tickets are skipped — blocked tickets must be
	// reset so callers can resolve the root cause and resume.
	for _, ticketID := range artifacts.Run.TicketIDs {
		snapshot := artifacts.Tickets[ticketID]
		if snapshot.CurrentPhase == state.TicketPhaseClosed {
			continue
		}
		// Release existing claim
		if err := releaseClaimForResume(artifacts.RepoRoot, req.RunID, ticketID, "resume_reacquisition"); err != nil {
			return nil, err
		}
		// Reset ticket store status to ready
		if err := setTicketReady(artifacts.RepoRoot, ticketID); err != nil {
			return nil, fmt.Errorf("reset ticket %s to ready: %w", ticketID, err)
		}
	}

	// Build an epic request for use by executeEpicTicket
	epicReq := RunEpicRequest{
		RepoRoot:       artifacts.RepoRoot,
		RunID:          req.RunID,
		RootTicketID:   artifacts.Run.RootTicketID,
		BaseBranch:     artifacts.Run.BaseBranch,
		BaseCommit:     baseCommit,
		Adapter:        req.Adapter,
		AdapterFactory: req.AdapterFactory,
		WorktreeRoot:   workRoot,
		Config:         cfg,
		Progress:       req.Progress,
	}

	var allResumed []string
	runPath := runJSONPath(artifacts.RepoRoot, req.RunID)
	registrar := &subEpicRegistrar{run: &artifacts.Run, runPath: runPath}

	for {
		if err := ctx.Err(); err != nil {
			return allResumed, err
		}

		// If a prior wave is still pending verification (e.g. the process crashed
		// mid-repair), complete it before scheduling new tickets.
		if err := resumePendingWaveVerification(ctx, epicReq, cfg, artifacts.Run.ResumeCursor, runPath, &artifacts.Run); err != nil {
			artifacts.Run.Status = state.EpicRunStatusBlocked
			artifacts.Run.CurrentPhase = state.TicketPhaseBlocked
			if saveErr := state.SaveJSONAtomic(runPath, artifacts.Run); saveErr != nil {
				return allResumed, errors.Join(err, fmt.Errorf("persist run state: %w", saveErr))
			}
			return allResumed, err
		}

		ready, err := epos.ListReadyChildren(artifacts.RepoRoot, artifacts.Run.RootTicketID, req.RunID)
		if err != nil {
			return allResumed, err
		}
		ticketScopes := buildTicketScopes(ready)
		if len(ready) == 0 {
			// Determine final epic status
			currentChildren, err := listEpicChildren(artifacts.RepoRoot, artifacts.Run.RootTicketID)
			if err != nil {
				return allResumed, err
			}
			status := epicCompletionStatus(currentChildren)
			artifacts.Run.Status = status
			switch status {
			case state.EpicRunStatusCompleted:
				artifacts.Run.CurrentPhase = state.TicketPhaseClosed
			case state.EpicRunStatusBlocked:
				artifacts.Run.CurrentPhase = state.TicketPhaseBlocked
			default:
				artifacts.Run.CurrentPhase = state.TicketPhaseImplement
			}
			artifacts.Run.UpdatedAt = time.Now().UTC()
			if err := state.SaveJSONAtomic(runPath, artifacts.Run); err != nil {
				return allResumed, err
			}
			return allResumed, nil
		}

		wave, err := BuildWave(ready, cfg.Scheduler.MaxConcurrency)
		if err != nil {
			artifacts.Run.Status = state.EpicRunStatusBlocked
			artifacts.Run.CurrentPhase = state.TicketPhaseBlocked
			artifacts.Run.UpdatedAt = time.Now().UTC()
			if saveErr := state.SaveJSONAtomic(runPath, artifacts.Run); saveErr != nil {
				return allResumed, errors.Join(err, fmt.Errorf("persist run state: %w", saveErr))
			}
			return allResumed, err
		}
		if err := assertMainTreeMatchesWaveBase(artifacts.RepoRoot, currentBaseRef); err != nil {
			artifacts.Run.Status = state.EpicRunStatusBlocked
			artifacts.Run.CurrentPhase = state.TicketPhaseBlocked
			artifacts.Run.UpdatedAt = time.Now().UTC()
			if saveErr := state.SaveJSONAtomic(runPath, artifacts.Run); saveErr != nil {
				return allResumed, errors.Join(err, fmt.Errorf("persist run state: %w", saveErr))
			}
			return allResumed, err
		}

		waveOrdinal++
		waveID := fmt.Sprintf("wave-%d", waveOrdinal)
		wave.WaveID = waveID
		wave.Ordinal = waveOrdinal
		wave.Status = state.WaveStatusRunning
		wave.WaveBaseCommit = currentBaseRef
		wave.StartedAt = time.Now().UTC()
		wavePath := waveArtifactPath(artifacts.RepoRoot, req.RunID, waveID)
		if err := ensureFreshWaveArtifactPath(artifacts.RepoRoot, req.RunID, waveID); err != nil {
			return allResumed, err
		}
		if err := state.SaveJSONAtomic(wavePath, wave); err != nil {
			return allResumed, err
		}
		if err := persistScheduledWaveRunState(&artifacts.Run, runPath, waveID, waveOrdinal); err != nil {
			return allResumed, err
		}
		SendProgress(ctx, req.Progress, ProgressEvent{
			Type:    EventWaveStarted,
			WaveID:  waveOrdinal,
			Tickets: append([]string(nil), wave.TicketIDs...),
		})

		waveBaselineRawChangedFiles, err := repo.ChangedFilesAgainst(currentBaseRef)
		if err != nil {
			return allResumed, err
		}
		waveBaselineChangedFiles := filterEngineOwnedFiles(waveBaselineRawChangedFiles)
		if artifacts.Run.ResumeCursor == nil {
			artifacts.Run.ResumeCursor = map[string]any{}
		}
		artifacts.Run.ResumeCursor["wave_baseline_changed_files"] = append([]string(nil), waveBaselineChangedFiles...)
		artifacts.Run.ResumeCursor["wave_baseline_raw_changed_files"] = append([]string(nil), waveBaselineRawChangedFiles...)

		waveManager, err := prepareWaveWorktrees(ctx, artifacts.RepoRoot, wave.WaveBaseCommit, req.RunID, epicReq.WorktreeRoot, wave.TicketIDs)
		if err != nil {
			return allResumed, err
		}
		cleanupWaveManager := func() {
			if waveManager != nil {
				_ = waveManager.CleanupAll()
			}
		}

		// Execute wave tickets in parallel with crash recovery
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
					worktreePath := ""
					if waveManager != nil {
						worktreePath = waveManager.WorktreePath(ticketID)
					}
					ticketReq := epicReq
					ticketReq.WorktreePath = worktreePath
					outcome, crashed := executeWithRecovery(ctx, ticketReq, cfg, wave, ticketID, 1, nil, registrar)
					if !crashed {
						outcomes[i] = outcome
						return
					}
					SendProgress(ctx, req.Progress, ProgressEvent{
						Type:     EventTicketDetail,
						TicketID: ticketID,
						Detail:   fmt.Sprintf("worker crashed (attempt %d/%d), retrying: %v", attempt+1, maxCrashRetries+1, outcome.err),
					})
					if releaseErr := releaseClaimForResume(artifacts.RepoRoot, req.RunID, ticketID, "crash recovery"); releaseErr != nil {
						outcome.err = releaseErr
						outcome.phase = state.TicketPhaseBlocked
						outcomes[i] = outcome
						return
					}
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
		allClosed := true
		mergeEligibleTicketIDs := make([]string, 0, len(outcomes))
		for i, outcome := range outcomes {
			ticketPhases[i] = outcome.phase
			if outcome.phase != state.TicketPhaseClosed {
				allClosed = false
			} else {
				mergeEligibleTicketIDs = append(mergeEligibleTicketIDs, wave.TicketIDs[i])
			}
		}

		waveFailed := !allClosed
		var waveErr error
		var conflictErr error
		if waveManager != nil {
			conflicts, detectErr := waveManager.DetectConflictsFor(mergeEligibleTicketIDs)
			if detectErr != nil {
				if waveErr == nil {
					waveErr = detectErr
				}
				waveFailed = true
			} else if len(conflicts) > 0 {
				conflictErr = &IntraWaveConflictError{Conflicts: conflicts}
				if waveErr == nil {
					waveErr = conflictErr
				}
				waveFailed = true
			}
		}
		if waveErr == nil && conflictErr != nil {
			waveFailed = true
		}
		for _, outcome := range outcomes {
			if outcome.err != nil {
				waveFailed = true
				if waveErr == nil {
					waveErr = outcome.err
				}
			}
		}

		var rawChangedFiles []string
		var changedFiles []string
		if waveManager != nil {
			rawChangedFiles, err = rawChangedFilesFromManager(waveManager, wave.TicketIDs)
			if err != nil {
				cleanupWaveManager()
				return allResumed, err
			}
			changedFiles, err = changedFilesFromManager(waveManager, wave.TicketIDs)
		} else {
			rawChangedFiles, err = repo.ChangedFilesAgainst(currentBaseRef)
			if err == nil {
				changedFiles = append([]string(nil), rawChangedFiles...)
			}
		}
		if err != nil {
			cleanupWaveManager()
			return allResumed, err
		}
		changedFiles = filterEngineOwnedFiles(changedFiles)
		changedFiles = subtractFiles(changedFiles, waveBaselineChangedFiles)
		rawChangedFiles = subtractFiles(rawChangedFiles, waveBaselineRawChangedFiles)

		claimsReleased, err := waveClaimsReleased(artifacts.RepoRoot, req.RunID, wave.TicketIDs)
		if err != nil {
			cleanupWaveManager()
			return allResumed, err
		}

		ticketDetails := buildWaveTicketDetails(artifacts.RepoRoot, req.RunID, wave.TicketIDs, outcomes)
		acceptedWave, acceptErr := AcceptWave(WaveAcceptanceRequest{
			Wave:                 wave,
			TicketPhases:         ticketPhases,
			RawChangedFiles:      rawChangedFiles,
			ChangedFiles:         changedFiles,
			TicketScopes:         ticketScopes,
			ClaimsReleased:       claimsReleased,
			PersistenceSucceeded: true,
			TicketDetails:        ticketDetails,
		})
		if waveFailed {
			acceptedWave.Status = state.WaveStatusFailed
			if conflictErr != nil {
				if acceptedWave.Acceptance == nil {
					acceptedWave.Acceptance = map[string]any{}
				}
				acceptedWave.Acceptance["conflict_scope"] = "deliverable"
				acceptedWave.Acceptance["intra_wave_conflicts"] = conflictErr.(*IntraWaveConflictError).Conflicts
			}
			if acceptedWave.Acceptance == nil {
				acceptedWave.Acceptance = map[string]any{}
			}
			if waveErr != nil {
				acceptedWave.Acceptance["crash_reason"] = waveErr.Error()
			}
		}
		if !allClosed && acceptedWave.Status == state.WaveStatusAccepted {
			acceptedWave.Status = state.WaveStatusFailed
		}
		if acceptedWave.Acceptance == nil {
			acceptedWave.Acceptance = map[string]any{}
		}
		acceptedWave.Acceptance["baseline_changed_files"] = append([]string(nil), waveBaselineChangedFiles...)
		acceptedWave.Acceptance["baseline_raw_changed_files"] = append([]string(nil), waveBaselineRawChangedFiles...)
		acceptedWave.UpdatedAt = time.Now().UTC()
		if acceptedWave.FinishedAt.IsZero() {
			acceptedWave.FinishedAt = acceptedWave.UpdatedAt
		}
		if err := state.SaveJSONAtomic(wavePath, acceptedWave); err != nil {
			cleanupWaveManager()
			return allResumed, err
		}
		if !allClosed && !waveFailed {
			waveFailed = true
			if waveErr == nil {
				waveErr = fmt.Errorf("wave %s has non-closed tickets", waveID)
			}
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
		artifacts.Run.WaveIDs = appendIfMissing(artifacts.Run.WaveIDs, acceptedWave.WaveID)
		artifacts.Run.UpdatedAt = time.Now().UTC()

		// Ticket store is updated by RunTicket's defer for normal completions.
		// Only handle crashed tickets here.
		for i, outcome := range outcomes {
			tid := wave.TicketIDs[i]
			if outcome.err != nil && outcome.phase != state.TicketPhaseClosed && outcome.phase != state.TicketPhaseBlocked {
				_ = updateTicketStoreStatus(artifacts.RepoRoot, tid, epos.StatusOpen)
			}
			allResumed = appendIfMissing(allResumed, tid)
		}
		for _, tid := range wave.TicketIDs {
			// Always persist diffs, even for closed tickets — if the wave later
			// fails, closed-ticket worktrees get cleaned up and their changes
			// would be lost without a persisted diff.
			if waveManager == nil {
				continue
			}
			diff, diffErr := waveManager.Diff(tid)
			if diffErr != nil {
				cleanupWaveManager()
				return allResumed, fmt.Errorf("persist diff artifact for %s: %w", tid, diffErr)
			}
			if persistErr := persistWorktreeDiff(artifacts.RepoRoot, req.RunID, tid, diff); persistErr != nil {
				cleanupWaveManager()
				return allResumed, fmt.Errorf("persist diff artifact for %s: %w", tid, persistErr)
			}
		}

		var blockErr error
		if acceptErr != nil || waveFailed {
			blockErr = acceptErr
			if conflictErr != nil {
				blockErr = conflictErr
			} else if waveErr != nil && blockErr == nil {
				blockErr = waveErr
			} else if waveErr != nil && blockErr != nil {
				blockErr = errors.Join(blockErr, waveErr)
			}
			if waveFailed && blockErr == nil {
				blockErr = fmt.Errorf("%w: wave %s had ticket failures", ErrEpicBlocked, waveID)
			}
		}

		if acceptErr != nil {
			artifacts.Run.Status = state.EpicRunStatusBlocked
			artifacts.Run.CurrentPhase = state.TicketPhaseBlocked
			if err := state.SaveJSONAtomic(runPath, artifacts.Run); err != nil {
				cleanupWaveManager()
				return allResumed, err
			}
			cleanupWaveManager()
			return allResumed, acceptErr
		}

		// Run wave-level verification for the merge-eligible ticket subset.
		// A wave may still block on sibling tickets, but closed tickets can be
		// safely integrated after the accepted subset passes the same gate.
		var integration *WaveIntegrationManager
		cleanupIntegration := func() {
			if integration == nil {
				return
			}
			if cleanupErr := integration.Cleanup(); cleanupErr != nil {
				log.Printf("[WARN] cleanup wave integration worktree: %v", cleanupErr)
			}
		}
		cleanupWaveResources := func() {
			cleanupIntegration()
			cleanupWaveManager()
		}
		if len(mergeEligibleTicketIDs) > 0 && conflictErr == nil && acceptErr == nil {
			if waveManager == nil {
				cleanupWaveResources()
				return allResumed, fmt.Errorf("wave %s requires prepared worktrees to integrate accepted tickets", waveID)
			}
			mergeChangedFiles, changedErr := changedFilesFromManager(waveManager, mergeEligibleTicketIDs)
			if changedErr != nil {
				cleanupWaveResources()
				return allResumed, changedErr
			}
			mergeChangedFiles = filterEngineOwnedFiles(mergeChangedFiles)
			mergeChangedFiles = subtractFiles(mergeChangedFiles, waveBaselineChangedFiles)

			integration, err = prepareWaveIntegration(ctx, artifacts.RepoRoot, req.RunID, epicReq.WorktreeRoot, wave.WaveBaseCommit)
			if err != nil {
				cleanupWaveResources()
				return allResumed, err
			}
			acceptedRefs := make([]string, 0, len(mergeEligibleTicketIDs))
			for _, ticketID := range mergeEligibleTicketIDs {
				effectiveFiles, changedErr := waveManager.ChangedFiles(ticketID)
				if changedErr != nil {
					cleanupWaveResources()
					return allResumed, changedErr
				}
				refName, freezeErr := integration.FreezeAcceptedTicket(ticketID, waveManager.WorktreePath(ticketID), effectiveFiles)
				if freezeErr != nil {
					cleanupWaveResources()
					return allResumed, freezeErr
				}
				acceptedRefs = append(acceptedRefs, refName)
			}
			if err := integration.ApplyAcceptedTicketRefs(ctx, acceptedRefs); err != nil {
				cleanupWaveResources()
				return allResumed, err
			}
			epicReq.WorktreePath = integration.WorktreePath()
			oldBaseHead, err := gitRevParse(artifacts.RepoRoot, wave.WaveBaseCommit)
			if err != nil {
				cleanupWaveResources()
				return allResumed, err
			}
			pendingTx := pendingWaveIntegrationTransaction{
				WaveID:       acceptedWave.WaveID,
				BaseCommit:   oldBaseHead,
				AcceptedRefs: acceptedRefs,
				ChangedFiles: mergeChangedFiles,
				WorktreePath: integration.WorktreePath(),
			}
			setPendingWaveIntegration(artifacts.Run.ResumeCursor, pendingTx)
			if err := state.SaveJSONAtomic(runPath, artifacts.Run); err != nil {
				cleanupWaveResources()
				return allResumed, err
			}
			if verifyErr := runWaveVerificationLoop(ctx, epicReq, cfg, &acceptedWave, wavePath, mergeChangedFiles, epicReq.WorktreePath); verifyErr != nil {
				if clearErr := clearPendingWaveVerificationOnTerminalFailure(artifacts.Run.ResumeCursor, runPath, &artifacts.Run, &acceptedWave); clearErr != nil {
					cleanupWaveResources()
					return allResumed, errors.Join(verifyErr, fmt.Errorf("clear terminal pending wave verification: %w", clearErr))
				}
				artifacts.Run.Status = state.EpicRunStatusBlocked
				artifacts.Run.CurrentPhase = state.TicketPhaseBlocked
				if saveErr := state.SaveJSONAtomic(runPath, artifacts.Run); saveErr != nil {
					cleanupWaveResources()
					return allResumed, errors.Join(verifyErr, fmt.Errorf("persist run state: %w", saveErr))
				}
				cleanupWaveResources()
				return allResumed, verifyErr
			}
			if err := completePendingWaveIntegrationTransaction(epicReq, artifacts.Run.ResumeCursor, runPath, &artifacts.Run, &acceptedWave, wavePath, pendingTx, integration); err != nil {
				cleanupWaveResources()
				return allResumed, err
			}
			currentBaseRef = integration.BaseRef()
		}

		if artifacts.Run.ResumeCursor != nil {
			artifacts.Run.ResumeCursor["wave_ordinal"] = waveOrdinal
			if _, ok := artifacts.Run.ResumeCursor["last_wave_base_commit"]; !ok {
				artifacts.Run.ResumeCursor["last_wave_base_commit"] = wave.WaveBaseCommit
			}
		}
		if err := state.SaveJSONAtomic(runPath, artifacts.Run); err != nil {
			cleanupWaveResources()
			return allResumed, err
		}
		cleanupWaveResources()
		if blockErr != nil {
			artifacts.Run.Status = state.EpicRunStatusBlocked
			artifacts.Run.CurrentPhase = state.TicketPhaseBlocked
			artifacts.Run.UpdatedAt = time.Now().UTC()
			if err := state.SaveJSONAtomic(runPath, artifacts.Run); err != nil {
				return allResumed, errors.Join(blockErr, fmt.Errorf("persist run state: %w", err))
			}
		}
	}
}

func resolveResumeWaveBase(repoRoot string, run state.RunArtifact) (string, error) {
	refName := integrationBaseRef(run.RunID)
	if _, err := gitRevParse(repoRoot, refName); err == nil {
		return refName, nil
	}

	lastBaseCommit := ""
	if run.ResumeCursor != nil {
		if raw, ok := run.ResumeCursor["last_wave_base_commit"].(string); ok {
			lastBaseCommit = strings.TrimSpace(raw)
		}
	}
	if lastBaseCommit != "" {
		if err := gitUpdateRef(repoRoot, refName, lastBaseCommit); err != nil {
			return "", fmt.Errorf("reconstruct integration base %q from last_wave_base_commit %q: %w", refName, lastBaseCommit, err)
		}
		return refName, nil
	}

	if resumeCursorWaveOrdinal(run.ResumeCursor) > 0 || len(run.WaveIDs) > 0 {
		return "", fmt.Errorf("cannot reconstruct integration base %q for resume: missing hidden ref and last_wave_base_commit", refName)
	}

	return ensureIntegrationBaseRef(repoRoot, run.RunID, run.BaseCommit)
}

// resumeAdapter resolves a runtime adapter for the given plan from the resume request.
func resumeAdapter(req ResumeRequest, plan state.PlanArtifact) (runtime.Adapter, error) {
	if req.AdapterFactory != nil {
		return req.AdapterFactory(plan.RuntimePreference)
	}
	if req.Adapter != nil {
		return req.Adapter, nil
	}
	return nil, fmt.Errorf("resume requires a runtime adapter")
}

// resumeCursorWaveOrdinal extracts the wave_ordinal from a ResumeCursor map.
func resumeCursorWaveOrdinal(cursor map[string]any) int {
	if cursor == nil {
		return 0
	}
	raw, ok := cursor["wave_ordinal"]
	if !ok {
		return 0
	}
	switch v := raw.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	default:
		return 0
	}
}

func reconcileTicketClaimForResume(repoRoot, runID, ticketID string, snapshot TicketRunSnapshot) (*state.ClaimArtifact, bool, error) {
	live, err := epos.LoadLiveClaim(repoRoot, ticketID)
	if err != nil {
		return nil, false, err
	}
	durable, err := loadOptionalClaim(durableClaimPath(repoRoot, runID, ticketID))
	if err != nil {
		return nil, false, err
	}
	if live == nil && durable == nil {
		return nil, false, nil
	}
	claim, err := epos.ReconcileClaim(live, durable, runID, isTerminalPhase(snapshot.CurrentPhase))
	if err != nil {
		return nil, false, err
	}
	repaired := false
	if durable == nil && live != nil && live.OwnerRunID == runID && !isTerminalPhase(snapshot.CurrentPhase) {
		if err := state.SaveJSONAtomic(durableClaimPath(repoRoot, runID, ticketID), claim); err != nil {
			return nil, false, err
		}
		repaired = true
	}
	if snapshot.Implementation != nil && snapshot.Implementation.LeaseID != "" && claim.State == "active" && snapshot.Implementation.LeaseID != claim.LeaseID {
		return nil, false, fmt.Errorf("ticket %s lease mismatch between implementation artifact %q and claim %q", ticketID, snapshot.Implementation.LeaseID, claim.LeaseID)
	}
	return &claim, repaired, nil
}

// reloadTicketSnapshots re-reads ticket snapshot data from disk into the
// provided map, replacing stale in-memory entries with the latest persisted
// state. This is needed after resumeEpicMode because RunTicket updates
// snapshot artifacts on disk via its defer, but the in-memory tickets map
// is never refreshed — causing updateRunStatusFromTickets to operate on
// outdated phase data.
func reloadTicketSnapshots(repoRoot, runID string, tickets map[string]TicketRunSnapshot) {
	for ticketID := range tickets {
		var snapshot TicketRunSnapshot
		if err := loadTicketSnapshot(repoRoot, runID, ticketID, &snapshot); err != nil {
			log.Printf("reloadTicketSnapshots: %s/%s: %v (removing stale entry)", runID, ticketID, err)
			delete(tickets, ticketID)
		} else {
			tickets[ticketID] = snapshot
		}
	}
}

func appendIfMissing(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
