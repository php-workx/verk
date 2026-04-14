package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	repoadapter "verk/internal/adapters/repo/git"
	"verk/internal/adapters/runtime"
	"verk/internal/adapters/ticketstore/tkmd"
	"verk/internal/policy"
	"verk/internal/state"
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

func ResumeRun(ctx context.Context, req ResumeRequest) (ResumeReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return ResumeReport{}, err
	}
	if req.RunID == "" {
		return ResumeReport{}, fmt.Errorf("resume requires run id")
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
		if snapshot.Closeout == nil && (snapshot.CurrentPhase == state.TicketPhaseCloseout || snapshot.CurrentPhase == state.TicketPhaseClosed) {
			plan, ok := artifacts.Plans[ticketID]
			if !ok {
				return ResumeReport{}, fmt.Errorf("resume requires plan artifact for ticket %s", ticketID)
			}
			ticket, err := tkmd.LoadTicket(ticketMarkdownPath(artifacts.RepoRoot, ticketID))
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
			if snapshot.CurrentPhase == state.TicketPhaseCloseout || snapshot.CurrentPhase == state.TicketPhaseClosed {
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
		_ = tkmd.ReleaseClaim(artifacts.RepoRoot, req.RunID, ticketID, "resume_reacquisition")

		// Re-acquire claim
		leaseID := fmt.Sprintf("lease-resume-%s-%d", req.RunID, time.Now().UTC().UnixNano())
		claim, err := tkmd.AcquireClaim(artifacts.RepoRoot, req.RunID, ticketID, leaseID, 30*time.Minute, time.Now().UTC())
		if err != nil {
			return resumed, fmt.Errorf("resume claim for ticket %s: %w", ticketID, err)
		}

		// Load ticket markdown
		ticket, err := tkmd.LoadTicket(ticketMarkdownPath(artifacts.RepoRoot, ticketID))
		if err != nil {
			return resumed, fmt.Errorf("load ticket %s: %w", ticketID, err)
		}
		ticket.Status = tkmd.StatusInProgress

		// Load or build plan
		plan, ok := artifacts.Plans[ticketID]
		if !ok {
			plan, err = BuildPlanArtifact(ticket, req.Config)
			if err != nil {
				return resumed, fmt.Errorf("build plan for ticket %s: %w", ticketID, err)
			}
			plan.RunID = req.RunID
		}
		// Resume at implement phase (the furthest-back supported entry point)
		plan.Phase = state.TicketPhaseImplement

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
			ticket.Status = tkmd.StatusClosed
		case state.TicketPhaseBlocked:
			ticket.Status = tkmd.StatusBlocked
		default:
			ticket.Status = tkmd.StatusBlocked
		}
		if err := tkmd.SaveTicket(ticketMarkdownPath(artifacts.RepoRoot, ticketID), ticket); err != nil {
			return resumed, err
		}

		resumed = append(resumed, ticketID)
	}
	return resumed, nil
}

// resumeEpicMode re-enters the wave loop for an epic-mode run,
// skipping already-completed waves.
func resumeEpicMode(ctx context.Context, req ResumeRequest, artifacts *runArtifacts) ([]string, error) {
	cfg := normalizeEpicConfig(req.Config)
	repo, err := repoadapter.New(artifacts.RepoRoot)
	if err != nil {
		return nil, err
	}

	// Determine the last completed wave ordinal from ResumeCursor
	lastWaveOrdinal := resumeCursorWaveOrdinal(artifacts.Run.ResumeCursor)
	waveOrdinal := lastWaveOrdinal

	baseCommit := artifacts.Run.BaseCommit

	// Reset incomplete tickets (including blocked) to "ready" so ListReadyChildren
	// can pick them up. Only closed tickets are skipped — blocked tickets must be
	// reset so callers can resolve the root cause and resume.
	for _, ticketID := range artifacts.Run.TicketIDs {
		snapshot := artifacts.Tickets[ticketID]
		if snapshot.CurrentPhase == state.TicketPhaseClosed {
			continue
		}
		// Release existing claim
		_ = tkmd.ReleaseClaim(artifacts.RepoRoot, req.RunID, ticketID, "resume_reacquisition")
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
		Config:         cfg,
		Progress:       req.Progress,
	}

	var allResumed []string
	runPath := runJSONPath(artifacts.RepoRoot, req.RunID)

	for {
		if err := ctx.Err(); err != nil {
			return allResumed, err
		}

		// If a prior wave is still pending verification (e.g. the process crashed
		// mid-repair), complete it before scheduling new tickets.
		if err := resumePendingWaveVerification(ctx, epicReq, cfg, artifacts.Run.ResumeCursor, runPath, &artifacts.Run); err != nil {
			artifacts.Run.Status = state.EpicRunStatusBlocked
			artifacts.Run.CurrentPhase = state.TicketPhaseBlocked
			_ = state.SaveJSONAtomic(runPath, artifacts.Run)
			return allResumed, err
		}

		ready, err := tkmd.ListReadyChildren(artifacts.RepoRoot, artifacts.Run.RootTicketID, req.RunID)
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
			_ = state.SaveJSONAtomic(runPath, artifacts.Run)
			return allResumed, err
		}

		waveOrdinal++
		waveID := fmt.Sprintf("wave-%d", waveOrdinal)
		wave.WaveID = waveID
		wave.Ordinal = waveOrdinal
		wave.Status = state.WaveStatusRunning
		wave.WaveBaseCommit = baseCommit
		wave.StartedAt = time.Now().UTC()
		wavePath := filepath.Join(artifacts.RepoRoot, ".verk", "runs", req.RunID, "waves", waveID+".json")
		if err := state.SaveJSONAtomic(wavePath, wave); err != nil {
			return allResumed, err
		}
		SendProgress(req.Progress, ProgressEvent{
			Type:    EventWaveStarted,
			WaveID:  waveOrdinal,
			Tickets: append([]string(nil), wave.TicketIDs...),
		})

		waveBaselineChangedFiles, err := repo.ChangedFilesAgainst(baseCommit)
		if err != nil {
			return allResumed, err
		}
		waveBaselineChangedFiles = filterEngineOwnedFiles(waveBaselineChangedFiles)
		if artifacts.Run.ResumeCursor == nil {
			artifacts.Run.ResumeCursor = map[string]any{}
		}
		artifacts.Run.ResumeCursor["wave_baseline_changed_files"] = append([]string(nil), waveBaselineChangedFiles...)

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
					outcome, crashed := executeWithRecovery(ctx, epicReq, cfg, wave, ticketID)
					if !crashed {
						outcomes[i] = outcome
						return
					}
					SendProgress(req.Progress, ProgressEvent{
						Type:     EventTicketDetail,
						TicketID: ticketID,
						Detail:   fmt.Sprintf("worker crashed (attempt %d/%d), retrying: %v", attempt+1, maxCrashRetries+1, outcome.err),
					})
					_ = tkmd.ReleaseClaim(artifacts.RepoRoot, req.RunID, ticketID,
						fmt.Sprintf("lease-%s-%s", req.RunID, ticketID), "crash recovery")
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
			return allResumed, err
		}
		changedFiles = filterEngineOwnedFiles(changedFiles)
		changedFiles = subtractFiles(changedFiles, waveBaselineChangedFiles)

		claimsReleased, err := waveClaimsReleased(artifacts.RepoRoot, req.RunID, wave.TicketIDs)
		if err != nil {
			return allResumed, err
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
			return allResumed, err
		}
		closedCount := countClosedTickets(outcomes)
		SendProgress(req.Progress, ProgressEvent{
			Type:    EventWaveCompleted,
			WaveID:  waveOrdinal,
			Closed:  closedCount,
			Total:   len(wave.TicketIDs),
			Success: acceptedWave.Status == state.WaveStatusAccepted,
		})
		artifacts.Run.WaveIDs = append(artifacts.Run.WaveIDs, acceptedWave.WaveID)
		artifacts.Run.UpdatedAt = time.Now().UTC()

		// Ticket store is updated by RunTicket's defer for normal completions.
		// Only handle crashed tickets here.
		for i, outcome := range outcomes {
			tid := wave.TicketIDs[i]
			if outcome.err != nil && outcome.phase != state.TicketPhaseClosed && outcome.phase != state.TicketPhaseBlocked {
				_ = updateTicketStoreStatus(artifacts.RepoRoot, tid, tkmd.StatusOpen)
			}
			allResumed = appendIfMissing(allResumed, tid)
		}

		if acceptErr != nil {
			artifacts.Run.Status = state.EpicRunStatusBlocked
			artifacts.Run.CurrentPhase = state.TicketPhaseBlocked
			if err := state.SaveJSONAtomic(runPath, artifacts.Run); err != nil {
				return allResumed, err
			}
			return allResumed, acceptErr
		}

		// Run wave-level verification after all tickets merge. Mark pending in
		// the cursor first so a crash during repair is detectable on resume.
		if acceptedWave.Status == state.WaveStatusAccepted {
			setPendingWaveVerification(artifacts.Run.ResumeCursor, acceptedWave.WaveID)
			if err := state.SaveJSONAtomic(runPath, artifacts.Run); err != nil {
				return allResumed, err
			}
			if verifyErr := runWaveVerificationLoop(ctx, epicReq, cfg, &acceptedWave, wavePath, changedFiles); verifyErr != nil {
				artifacts.Run.Status = state.EpicRunStatusBlocked
				artifacts.Run.CurrentPhase = state.TicketPhaseBlocked
				_ = state.SaveJSONAtomic(runPath, artifacts.Run)
				return allResumed, verifyErr
			}
			clearPendingWaveVerification(artifacts.Run.ResumeCursor)
		}

		if artifacts.Run.ResumeCursor != nil {
			artifacts.Run.ResumeCursor["wave_ordinal"] = waveOrdinal
			artifacts.Run.ResumeCursor["last_wave_base_commit"] = wave.WaveBaseCommit
		}
		if err := state.SaveJSONAtomic(runPath, artifacts.Run); err != nil {
			return allResumed, err
		}
	}
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
	live, err := loadOptionalClaim(liveClaimPath(repoRoot, ticketID))
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
	claim, err := tkmd.ReconcileClaim(live, durable, runID, isTerminalPhase(snapshot.CurrentPhase))
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
		if err := loadTicketSnapshot(repoRoot, runID, ticketID, &snapshot); err == nil {
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
