package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"verk/internal/adapters/repo/git"
	"verk/internal/adapters/runtime"
	"verk/internal/adapters/ticketstore/tkmd"
	"verk/internal/policy"
	"verk/internal/state"
)

type RunEpicRequest struct {
	RepoRoot             string
	RunID                string
	RootTicketID         string
	BaseCommit           string
	Adapter              runtime.Adapter
	Config               policy.Config
	VerificationByTicket map[string][]string
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
	acceptedScope := make([]string, 0)
	waveOrdinal := 0

	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}

		ready, err := tkmd.ListReadyChildren(req.RepoRoot, req.RootTicketID, req.RunID)
		if err != nil {
			return result, err
		}
		if len(ready) == 0 {
			currentChildren, err := listEpicChildren(req.RepoRoot, req.RootTicketID)
			if err != nil {
				return result, err
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

		ticketPhases := make([]state.TicketPhase, 0, len(wave.TicketIDs))
		waveFailed := false
		var waveErr error

		for _, ticketID := range wave.TicketIDs {
			if err := ctx.Err(); err != nil {
				return result, err
			}

			ticket, err := loadEpicTicket(req.RepoRoot, ticketID)
			if err != nil {
				waveFailed = true
				waveErr = err
				break
			}

			if err := updateTicketStoreStatus(req.RepoRoot, ticket.ID, tkmd.StatusInProgress); err != nil {
				waveFailed = true
				waveErr = err
				break
			}

			plan, err := BuildPlanArtifact(ticket, cfg)
			if err != nil {
				waveFailed = true
				waveErr = err
				if statusErr := updateTicketStoreStatus(req.RepoRoot, ticket.ID, tkmd.StatusBlocked); statusErr != nil {
					waveErr = fmt.Errorf("%w: update ticket %s status to blocked: %v", err, ticket.ID, statusErr)
				}
				break
			}

			leaseID := fmt.Sprintf("lease-%s-%s", req.RunID, ticket.ID)
			claim, err := tkmd.AcquireClaim(req.RepoRoot, req.RunID, ticket.ID, leaseID, wave.WaveID, 30*time.Minute, time.Now().UTC())
			if err != nil {
				waveFailed = true
				waveErr = err
				if statusErr := updateTicketStoreStatus(req.RepoRoot, ticket.ID, tkmd.StatusBlocked); statusErr != nil {
					waveErr = fmt.Errorf("%w: update ticket %s status to blocked: %v", err, ticket.ID, statusErr)
				}
				break
			}

			runTicketReq := RunTicketRequest{
				RepoRoot:             req.RepoRoot,
				RunID:                req.RunID,
				Ticket:               ticket,
				Plan:                 plan,
				Claim:                claim,
				Adapter:              req.Adapter,
				Config:               cfg,
				VerificationCommands: verificationCommandsFor(req, ticket),
			}
			ticketResult, err := RunTicket(ctx, runTicketReq)
			if err != nil {
				waveFailed = true
				waveErr = err
				if statusErr := updateTicketStoreStatus(req.RepoRoot, ticket.ID, tkmd.StatusBlocked); statusErr != nil {
					waveErr = fmt.Errorf("%w: update ticket %s status to blocked: %v", err, ticket.ID, statusErr)
				}
				break
			}

			ticketPhases = append(ticketPhases, ticketResult.Snapshot.CurrentPhase)
			switch ticketResult.Snapshot.CurrentPhase {
			case state.TicketPhaseClosed:
				if err := updateTicketStoreStatus(req.RepoRoot, ticket.ID, tkmd.StatusClosed); err != nil {
					waveFailed = true
					waveErr = err
				}
			case state.TicketPhaseBlocked:
				if err := updateTicketStoreStatus(req.RepoRoot, ticket.ID, tkmd.StatusBlocked); err != nil {
					waveFailed = true
					waveErr = err
				}
			default:
				if err := updateTicketStoreStatus(req.RepoRoot, ticket.ID, tkmd.StatusBlocked); err != nil {
					waveFailed = true
					waveErr = err
				}
			}
			if waveFailed {
				break
			}
		}

		changedFiles, err := repo.ChangedFilesAgainst(baseCommit)
		if err != nil {
			return result, err
		}
		changedFiles = filterEngineOwnedFiles(changedFiles)
		changedFiles = filterCoveredFiles(changedFiles, acceptedScope)

		claimsReleased, err := waveClaimsReleased(req.RepoRoot, req.RunID, wave.TicketIDs)
		if err != nil {
			return result, err
		}

		acceptedWave, acceptErr := AcceptWave(WaveAcceptanceRequest{
			Wave:                 wave,
			TicketPhases:         ticketPhases,
			ChangedFiles:         changedFiles,
			ClaimsReleased:       claimsReleased,
			PersistenceSucceeded: true,
		})
		if waveFailed {
			acceptedWave.Status = state.WaveStatusFailed
			if acceptedWave.Acceptance == nil {
				acceptedWave.Acceptance = map[string]any{}
			}
			if waveErr != nil {
				acceptedWave.Acceptance["reason"] = waveErr.Error()
				acceptErr = waveErr
			}
		}

		acceptedWave.UpdatedAt = time.Now().UTC()
		if acceptedWave.FinishedAt.IsZero() {
			acceptedWave.FinishedAt = acceptedWave.UpdatedAt
		}
		if err := state.SaveJSONAtomic(wavePath, acceptedWave); err != nil {
			return result, err
		}
		result.Waves = append(result.Waves, acceptedWave)
		result.Run.WaveIDs = append(result.Run.WaveIDs, acceptedWave.WaveID)
		result.Run.UpdatedAt = time.Now().UTC()

		if acceptErr != nil {
			result.Run.Status = state.EpicRunStatusBlocked
			result.Run.CurrentPhase = state.TicketPhaseBlocked
			if err := state.SaveJSONAtomic(runPath, result.Run); err != nil {
				return result, err
			}
			if waveFailed {
				return result, acceptErr
			}
			return result, nil
		}

		acceptedScope = append(acceptedScope, acceptedWave.PlannedScope...)
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
	if req.Adapter == nil {
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
	paths, err := filepath.Glob(filepath.Join(repoRoot, ".tickets", "*.md"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)

	children := make([]tkmd.Ticket, 0, len(paths))
	for _, path := range paths {
		ticket, err := tkmd.LoadTicket(path)
		if err != nil {
			return nil, err
		}
		if ticketParent(&ticket) != parentID {
			continue
		}
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

func filterCoveredFiles(changed, covered []string) []string {
	if len(changed) == 0 {
		return nil
	}
	if len(covered) == 0 {
		return append([]string(nil), changed...)
	}

	out := make([]string, 0, len(changed))
	for _, file := range changed {
		if coveredByAny(file, covered) {
			continue
		}
		out = append(out, file)
	}
	return out
}

func coveredByAny(file string, scopes []string) bool {
	for _, scope := range scopes {
		if git.PathsOverlap(file, scope) {
			return true
		}
	}
	return false
}

func ticketParent(ticket *tkmd.Ticket) string {
	if ticket == nil || ticket.UnknownFrontmatter == nil {
		return ""
	}
	parent, _ := ticket.UnknownFrontmatter["parent"].(string)
	return parent
}
