package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"verk/internal/adapters/runtime"
	"verk/internal/adapters/ticketstore/tkmd"
	verifycommand "verk/internal/adapters/verify/command"
	"verk/internal/policy"
	"verk/internal/state"
)

type RunTicketRequest struct {
	RepoRoot             string
	RunID                string
	Ticket               tkmd.Ticket
	Plan                 state.PlanArtifact
	Claim                state.ClaimArtifact
	Adapter              runtime.Adapter
	Config               policy.Config
	VerificationCommands []string
}

type RunTicketResult struct {
	Snapshot TicketRunSnapshot
	Path     string
}

type TicketRunSnapshot struct {
	state.ArtifactMeta
	TicketID               string                        `json:"ticket_id"`
	CurrentPhase           state.TicketPhase             `json:"current_phase"`
	BlockReason            string                        `json:"block_reason,omitempty"`
	ImplementationAttempts int                           `json:"implementation_attempts"`
	VerificationAttempts   int                           `json:"verification_attempts"`
	ReviewAttempts         int                           `json:"review_attempts"`
	RepairCycles           []state.RepairCycleArtifact   `json:"repair_cycles,omitempty"`
	Implementation         *state.ImplementationArtifact `json:"implementation,omitempty"`
	Verification           *state.VerificationArtifact   `json:"verification,omitempty"`
	Review                 *state.ReviewFindingsArtifact `json:"review,omitempty"`
	Closeout               *state.CloseoutArtifact       `json:"closeout,omitempty"`
}

type ticketRunState struct {
	req                    RunTicketRequest
	cfg                    policy.Config
	paths                  ticketRunPaths
	repoRoot               string
	currentPhase           state.TicketPhase
	blockReason            string
	implementation         *state.ImplementationArtifact
	verification           *state.VerificationArtifact
	review                 *state.ReviewFindingsArtifact
	closeout               *state.CloseoutArtifact
	repairCycles           []state.RepairCycleArtifact
	implementationAttempts int
	verificationAttempts   int
	reviewAttempts         int
}

type ticketRunPaths struct {
	snapshotPath       string
	implementationPath string
	verificationPath   string
	reviewPath         string
	closeoutPath       string
	runDir             string
}

func RunTicket(ctx context.Context, req RunTicketRequest) (RunTicketResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if err := validateRunTicketRequest(req); err != nil {
		return RunTicketResult{}, err
	}

	cfg := normalizeRunTicketConfig(req.Config)
	absRepoRoot, err := filepath.Abs(req.RepoRoot)
	if err != nil {
		return RunTicketResult{}, fmt.Errorf("resolve repo root %q: %w", req.RepoRoot, err)
	}
	plan := req.Plan
	if plan.Phase == "" {
		plan.Phase = state.TicketPhaseIntake
	}
	if plan.EffectiveReviewThreshold == "" {
		plan.EffectiveReviewThreshold = cfg.Policy.ReviewThreshold
	}
	if plan.ReviewThreshold == "" {
		plan.ReviewThreshold = plan.EffectiveReviewThreshold
	}
	req.Plan = plan

	st := &ticketRunState{
		req:          req,
		cfg:          cfg,
		paths:        buildTicketRunPaths(absRepoRoot, req.RunID, req.Ticket.ID),
		repoRoot:     absRepoRoot,
		currentPhase: req.Plan.Phase,
	}
	if st.currentPhase == "" {
		st.currentPhase = state.TicketPhaseIntake
	}
	if st.currentPhase != state.TicketPhaseIntake && st.currentPhase != state.TicketPhaseImplement {
		return RunTicketResult{}, fmt.Errorf("ticket run cannot start from phase %q", st.currentPhase)
	}
	if err := state.SaveJSONAtomic(filepath.Join(st.paths.runDir, "plan.json"), req.Plan); err != nil {
		return RunTicketResult{}, err
	}

	if st.currentPhase == state.TicketPhaseIntake {
		if err := st.transitionTo(state.TicketPhaseImplement); err != nil {
			return RunTicketResult{}, err
		}
		if err := st.persist(); err != nil {
			return RunTicketResult{}, err
		}
	}

	for {
		if err := ctx.Err(); err != nil {
			return RunTicketResult{}, err
		}

		switch st.currentPhase {
		case state.TicketPhaseImplement:
			workerReq := runtime.WorkerRequest{
				RunID:        req.RunID,
				TicketID:     req.Ticket.ID,
				LeaseID:      req.Claim.LeaseID,
				Attempt:      st.implementationAttempts + 1,
				Runtime:      chosenRuntime(req.Plan, cfg),
				WorktreePath: absRepoRoot,
				Instructions: renderImplementInstructions(req.Plan, st.currentPhase, st.implementationAttempts+1),
			}
			result, err := req.Adapter.RunWorker(ctx, workerReq)
			if err != nil {
				return RunTicketResult{}, err
			}
			if err := handleImplementResult(st, result, workerReq.Attempt); err != nil {
				return RunTicketResult{}, err
			}

			if st.currentPhase == state.TicketPhaseVerify {
				if err := st.persist(); err != nil {
					return RunTicketResult{}, err
				}
				verifyArtifact, verifyPassed, err := st.runVerification(ctx, absRepoRoot)
				if err != nil {
					return RunTicketResult{}, err
				}
				st.verification = verifyArtifact
				st.verificationAttempts = verifyArtifact.Attempt
				if err := st.persist(); err != nil {
					return RunTicketResult{}, err
				}
				if !verifyPassed {
					if err := handleVerificationFailure(st, *verifyArtifact); err != nil {
						return RunTicketResult{}, err
					}
					if err := st.persist(); err != nil {
						return RunTicketResult{}, err
					}
					continue
				}

				if err := st.transitionTo(state.TicketPhaseReview); err != nil {
					return RunTicketResult{}, err
				}
				if err := st.persist(); err != nil {
					return RunTicketResult{}, err
				}
				continue
			}

			if st.currentPhase == state.TicketPhaseBlocked || st.currentPhase == state.TicketPhaseClosed {
				if err := st.persist(); err != nil {
					return RunTicketResult{}, err
				}
				if err := st.releaseClaim(); err != nil {
					return RunTicketResult{}, err
				}
				return RunTicketResult{Snapshot: st.snapshot(), Path: st.paths.snapshotPath}, nil
			}

		case state.TicketPhaseReview:
			reviewReq := runtime.ReviewRequest{
				RunID:                    req.RunID,
				TicketID:                 req.Ticket.ID,
				LeaseID:                  req.Claim.LeaseID,
				Attempt:                  st.reviewAttempts + 1,
				Runtime:                  chosenRuntime(req.Plan, cfg),
				InputArtifactPath:        st.paths.verificationPath,
				EffectiveReviewThreshold: req.Plan.EffectiveReviewThreshold,
			}
			result, err := req.Adapter.RunReviewer(ctx, reviewReq)
			if err != nil {
				return RunTicketResult{}, err
			}
			if err := handleReviewOutcome(st, result, reviewReq.Attempt); err != nil {
				return RunTicketResult{}, err
			}
			if err := st.persist(); err != nil {
				return RunTicketResult{}, err
			}
			if st.currentPhase == state.TicketPhaseBlocked || st.currentPhase == state.TicketPhaseClosed {
				if err := st.releaseClaim(); err != nil {
					return RunTicketResult{}, err
				}
				return RunTicketResult{Snapshot: st.snapshot(), Path: st.paths.snapshotPath}, nil
			}

		case state.TicketPhaseRepair:
			workerReq := runtime.WorkerRequest{
				RunID:        req.RunID,
				TicketID:     req.Ticket.ID,
				LeaseID:      req.Claim.LeaseID,
				Attempt:      st.implementationAttempts + 1,
				Runtime:      chosenRuntime(req.Plan, cfg),
				WorktreePath: absRepoRoot,
				Instructions: renderRepairInstructions(st),
			}
			result, err := req.Adapter.RunWorker(ctx, workerReq)
			if err != nil {
				return RunTicketResult{}, err
			}
			if err := handleImplementResult(st, result, workerReq.Attempt); err != nil {
				return RunTicketResult{}, err
			}
			if st.currentPhase == state.TicketPhaseVerify {
				if err := st.persist(); err != nil {
					return RunTicketResult{}, err
				}
				verifyArtifact, verifyPassed, err := st.runVerification(ctx, absRepoRoot)
				if err != nil {
					return RunTicketResult{}, err
				}
				st.verification = verifyArtifact
				st.verificationAttempts = verifyArtifact.Attempt
				if err := st.persist(); err != nil {
					return RunTicketResult{}, err
				}
				if !verifyPassed {
					if err := handleVerificationFailure(st, *verifyArtifact); err != nil {
						return RunTicketResult{}, err
					}
					if err := st.persist(); err != nil {
						return RunTicketResult{}, err
					}
					continue
				}

				if err := st.transitionTo(state.TicketPhaseReview); err != nil {
					return RunTicketResult{}, err
				}
				if err := st.persist(); err != nil {
					return RunTicketResult{}, err
				}
				continue
			}
			if st.currentPhase == state.TicketPhaseBlocked || st.currentPhase == state.TicketPhaseClosed {
				if err := st.persist(); err != nil {
					return RunTicketResult{}, err
				}
				if err := st.releaseClaim(); err != nil {
					return RunTicketResult{}, err
				}
				return RunTicketResult{Snapshot: st.snapshot(), Path: st.paths.snapshotPath}, nil
			}

		case state.TicketPhaseBlocked, state.TicketPhaseClosed:
			if err := st.persist(); err != nil {
				return RunTicketResult{}, err
			}
			if err := st.releaseClaim(); err != nil {
				return RunTicketResult{}, err
			}
			return RunTicketResult{Snapshot: st.snapshot(), Path: st.paths.snapshotPath}, nil

		default:
			return RunTicketResult{}, fmt.Errorf("unsupported ticket phase %q", st.currentPhase)
		}

		if st.currentPhase == state.TicketPhaseReview {
			continue
		}
	}
}

func handleImplementResult(st *ticketRunState, result runtime.WorkerResult, attempt int) error {
	if err := result.Validate(); err != nil {
		return err
	}
	if err := tkmd.ValidateLeaseFence(st.req.Claim.LeaseID, result.LeaseID); err != nil {
		return err
	}

	st.implementationAttempts = attempt
	st.implementation = &state.ImplementationArtifact{
		ArtifactMeta: state.ArtifactMeta{
			SchemaVersion: artifactSchemaVersion,
			RunID:         st.req.RunID,
		},
		TicketID:       st.req.Ticket.ID,
		Attempt:        st.implementationAttempts,
		Runtime:        chosenRuntime(st.req.Plan, st.cfg),
		Status:         string(result.Status),
		CompletionCode: result.CompletionCode,
		RetryClass:     state.RetryClass(result.RetryClass),
		LeaseID:        result.LeaseID,
		StartedAt:      result.StartedAt,
		FinishedAt:     result.FinishedAt,
		Artifacts:      compactStrings([]string{result.StdoutPath, result.StderrPath, result.ResultArtifactPath}),
	}

	switch result.Status {
	case runtime.WorkerStatusDone, runtime.WorkerStatusDoneWithConcerns:
		if err := st.transitionTo(state.TicketPhaseVerify); err != nil {
			return err
		}
		st.blockReason = ""
		st.implementation.BlockReason = ""
	case runtime.WorkerStatusNeedsContext, runtime.WorkerStatusBlocked:
		reason := workerBlockReason(result)
		st.blockReason = reason
		st.implementation.BlockReason = reason
		if err := st.transitionTo(state.TicketPhaseBlocked); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unexpected implement result status %q", result.Status)
	}

	if err := st.persist(); err != nil {
		return err
	}
	return nil
}

func handleVerificationFailure(st *ticketRunState, verification state.VerificationArtifact) error {
	if !verification.Passed {
		st.verification = &verification
	}
	if st.implementationAttempts >= st.cfg.Policy.MaxImplementationAttempts {
		st.blockReason = fmt.Sprintf("non-convergence: verification failed after %d implementation attempt(s)", st.implementationAttempts)
		if err := st.transitionTo(state.TicketPhaseBlocked); err != nil {
			return err
		}
		if st.verification != nil {
			st.verification.Passed = false
		}
		return nil
	}

	if err := st.transitionTo(state.TicketPhaseImplement); err != nil {
		return err
	}
	return nil
}

func handleReviewOutcome(st *ticketRunState, result runtime.ReviewResult, attempt int) error {
	if err := result.Validate(st.req.Plan.EffectiveReviewThreshold); err != nil {
		return err
	}
	if err := tkmd.ValidateLeaseFence(st.req.Claim.LeaseID, result.LeaseID); err != nil {
		return err
	}

	blockingIDs := make([]string, 0)
	for _, finding := range result.Findings {
		if ReviewFindingBlocks(finding, st.req.Plan.EffectiveReviewThreshold) {
			blockingIDs = append(blockingIDs, finding.ID)
		}
	}
	sort.Strings(blockingIDs)

	st.reviewAttempts = attempt
	st.review = &state.ReviewFindingsArtifact{
		ArtifactMeta: state.ArtifactMeta{
			SchemaVersion: artifactSchemaVersion,
			RunID:         st.req.RunID,
		},
		TicketID:                 st.req.Ticket.ID,
		Attempt:                  st.reviewAttempts,
		ReviewerRuntime:          chosenRuntime(st.req.Plan, st.cfg),
		Summary:                  result.Summary,
		Findings:                 convertReviewFindings(result.Findings),
		BlockingFindings:         append([]string(nil), blockingIDs...),
		Passed:                   len(blockingIDs) == 0,
		EffectiveReviewThreshold: st.req.Plan.EffectiveReviewThreshold,
	}

	if len(blockingIDs) == 0 {
		if len(st.repairCycles) > 0 {
			last := &st.repairCycles[len(st.repairCycles)-1]
			last.VerificationArtifact = st.paths.verificationPath
			last.ReviewArtifact = st.paths.reviewPath
			last.Status = "completed"
			last.FinishedAt = time.Now().UTC()
		}
		closeout, err := BuildCloseoutArtifact(st.req.Ticket, st.req.Plan, st.verification, st.review)
		if err != nil {
			return err
		}
		st.closeout = &closeout
		st.blockReason = ""
		if err := st.transitionTo(state.TicketPhaseCloseout); err != nil {
			return err
		}
		if err := st.transitionTo(state.TicketPhaseClosed); err != nil {
			return err
		}
		if err := st.persist(); err != nil {
			return err
		}
		return nil
	}

	cycle := state.RepairCycleArtifact{
		ArtifactMeta: state.ArtifactMeta{
			SchemaVersion: artifactSchemaVersion,
			RunID:         st.req.RunID,
		},
		TicketID:            st.req.Ticket.ID,
		Cycle:               len(st.repairCycles) + 1,
		TriggerFindingIDs:   append([]string(nil), blockingIDs...),
		InputReviewArtifact: st.paths.reviewPath,
		RepairNotes:         fmt.Sprintf("repair cycle %d triggered by findings: %s", len(st.repairCycles)+1, strings.Join(blockingIDs, ", ")),
		ReviewArtifact:      st.paths.reviewPath,
		Status:              "repair_pending",
		StartedAt:           time.Now().UTC(),
	}
	st.repairCycles = append(st.repairCycles, cycle)

	if len(st.repairCycles) > st.cfg.Policy.MaxRepairCycles {
		st.blockReason = fmt.Sprintf("non-convergence: repair limit reached after %d repair cycle(s)", len(st.repairCycles)-1)
		last := &st.repairCycles[len(st.repairCycles)-1]
		last.Status = "blocked"
		last.FinishedAt = time.Now().UTC()
		if err := st.transitionTo(state.TicketPhaseBlocked); err != nil {
			return err
		}
		return nil
	}

	if err := st.transitionTo(state.TicketPhaseRepair); err != nil {
		return err
	}
	return nil
}

func (st *ticketRunState) runVerification(ctx context.Context, repoRoot string) (*state.VerificationArtifact, bool, error) {
	commands := st.req.VerificationCommands
	if len(commands) == 0 {
		commands = st.req.Plan.ValidationCommands
	}

	results, err := verifycommand.RunCommands(ctx, repoRoot, commands, st.cfg.Verification)
	if err != nil {
		return nil, false, err
	}

	converted := convertVerificationResults(commands, results)
	artifact := &state.VerificationArtifact{
		ArtifactMeta: state.ArtifactMeta{
			SchemaVersion: artifactSchemaVersion,
			RunID:         st.req.RunID,
		},
		TicketID:   st.req.Ticket.ID,
		Attempt:    st.verificationAttempts + 1,
		Commands:   append([]string(nil), commands...),
		Results:    converted,
		Passed:     verifycommand.DeriveVerificationPassed(results),
		RepoRoot:   repoRoot,
		StartedAt:  verificationStartedAt(converted),
		FinishedAt: verificationFinishedAt(converted),
	}
	return artifact, artifact.Passed, nil
}

func (st *ticketRunState) transitionTo(next state.TicketPhase) error {
	if st.currentPhase == next {
		return nil
	}
	if err := state.ValidateTicketTransition(st.currentPhase, next); err != nil {
		return err
	}
	st.currentPhase = next
	return nil
}

func (st *ticketRunState) persist() error {
	snapshot := st.snapshot()
	artifacts := []struct {
		path string
		data any
	}{
		{path: st.paths.implementationPath, data: st.implementation},
		{path: st.paths.verificationPath, data: st.verification},
		{path: st.paths.reviewPath, data: st.review},
		{path: st.paths.closeoutPath, data: st.closeout},
	}
	for i := range st.repairCycles {
		cycle := st.repairCycles[i]
		if err := state.SaveJSONAtomic(st.paths.repairCyclePath(cycle.Cycle), cycle); err != nil {
			return err
		}
	}
	for _, artifact := range artifacts {
		if artifact.path == "" || isNilArtifactData(artifact.data) {
			continue
		}
		if err := state.SaveJSONAtomic(artifact.path, artifact.data); err != nil {
			return err
		}
	}
	return state.SaveJSONAtomic(st.paths.snapshotPath, snapshot)
}

func isNilArtifactData(data any) bool {
	if data == nil {
		return true
	}
	value := reflect.ValueOf(data)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func (st *ticketRunState) snapshot() TicketRunSnapshot {
	snapshot := TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{
			SchemaVersion: artifactSchemaVersion,
			RunID:         st.req.RunID,
		},
		TicketID:               st.req.Ticket.ID,
		CurrentPhase:           st.currentPhase,
		BlockReason:            st.blockReason,
		ImplementationAttempts: st.implementationAttempts,
		VerificationAttempts:   st.verificationAttempts,
		ReviewAttempts:         st.reviewAttempts,
		Implementation:         st.implementation,
		Verification:           st.verification,
		Review:                 st.review,
		Closeout:               st.closeout,
	}
	if len(st.repairCycles) > 0 {
		snapshot.RepairCycles = append([]state.RepairCycleArtifact(nil), st.repairCycles...)
	}
	return snapshot
}

func (st *ticketRunState) releaseClaim() error {
	reason := st.blockReason
	if reason == "" && st.currentPhase == state.TicketPhaseClosed {
		reason = "completed"
	}
	if reason == "" {
		reason = "released"
	}
	return tkmd.ReleaseClaim(st.repoRoot, st.req.RunID, st.req.Ticket.ID, st.req.Claim.LeaseID, reason)
}

func (st *ticketRunState) repairCyclePath(cycle int) string {
	return st.paths.repairCyclePath(cycle)
}

func buildTicketRunPaths(repoRoot, runID, ticketID string) ticketRunPaths {
	runDir := filepath.Join(repoRoot, ".verk", "runs", runID, "tickets", ticketID)
	return ticketRunPaths{
		runDir:             runDir,
		snapshotPath:       filepath.Join(runDir, "ticket-run.json"),
		implementationPath: filepath.Join(runDir, "implement.json"),
		verificationPath:   filepath.Join(runDir, "verification.json"),
		reviewPath:         filepath.Join(runDir, "review.json"),
		closeoutPath:       filepath.Join(runDir, "closeout.json"),
	}
}

func (p ticketRunPaths) repairCyclePath(cycle int) string {
	return filepath.Join(p.runDir, fmt.Sprintf("repair-cycle-%02d.json", cycle))
}

func validateRunTicketRequest(req RunTicketRequest) error {
	if req.RepoRoot == "" {
		return fmt.Errorf("run ticket requires repo root")
	}
	if req.RunID == "" {
		return fmt.Errorf("run ticket requires run id")
	}
	if req.Ticket.ID == "" {
		return fmt.Errorf("run ticket requires ticket metadata")
	}
	if req.Plan.TicketID == "" {
		return fmt.Errorf("run ticket requires plan artifact")
	}
	if req.Plan.TicketID != req.Ticket.ID {
		return fmt.Errorf("plan ticket %q does not match ticket %q", req.Plan.TicketID, req.Ticket.ID)
	}
	if req.Claim.TicketID == "" {
		return fmt.Errorf("run ticket requires claim artifact")
	}
	if req.Claim.TicketID != req.Ticket.ID {
		return fmt.Errorf("claim ticket %q does not match ticket %q", req.Claim.TicketID, req.Ticket.ID)
	}
	if req.Claim.LeaseID == "" {
		return fmt.Errorf("claim lease id is required")
	}
	if req.Adapter == nil {
		return fmt.Errorf("run ticket requires runtime adapter")
	}
	return nil
}

func normalizeRunTicketConfig(cfg policy.Config) policy.Config {
	defaults := policy.DefaultConfig()
	if cfg.Policy.ReviewThreshold == "" {
		cfg.Policy.ReviewThreshold = defaults.Policy.ReviewThreshold
	}
	if cfg.Policy.MaxImplementationAttempts <= 0 {
		cfg.Policy.MaxImplementationAttempts = defaults.Policy.MaxImplementationAttempts
	}
	if cfg.Verification.DefaultTimeoutMinutes <= 0 {
		cfg.Verification.DefaultTimeoutMinutes = defaults.Verification.DefaultTimeoutMinutes
	}
	if cfg.Runtime.DefaultRuntime == "" {
		cfg.Runtime.DefaultRuntime = defaults.Runtime.DefaultRuntime
	}
	if len(cfg.Verification.EnvPassthrough) == 0 {
		cfg.Verification.EnvPassthrough = append([]string(nil), defaults.Verification.EnvPassthrough...)
	}
	return cfg
}

func chosenRuntime(plan state.PlanArtifact, cfg policy.Config) string {
	if strings.TrimSpace(plan.RuntimePreference) != "" {
		return strings.TrimSpace(plan.RuntimePreference)
	}
	return strings.TrimSpace(cfg.Runtime.DefaultRuntime)
}

func renderImplementInstructions(plan state.PlanArtifact, phase state.TicketPhase, attempt int) string {
	parts := []string{
		fmt.Sprintf("phase=%s", phase),
		fmt.Sprintf("attempt=%d", attempt),
	}
	if len(plan.AcceptanceCriteria) > 0 {
		parts = append(parts, fmt.Sprintf("criteria=%d", len(plan.AcceptanceCriteria)))
	}
	return strings.Join(parts, "; ")
}

func renderRepairInstructions(st *ticketRunState) string {
	if len(st.repairCycles) == 0 {
		return "repair cycle"
	}
	last := st.repairCycles[len(st.repairCycles)-1]
	return fmt.Sprintf("repair cycle %d: %s", last.Cycle, strings.Join(last.TriggerFindingIDs, ", "))
}

func workerBlockReason(result runtime.WorkerResult) string {
	reason := string(result.Status)
	if strings.TrimSpace(result.CompletionCode) != "" {
		reason = fmt.Sprintf("%s: %s", reason, strings.TrimSpace(result.CompletionCode))
	}
	if result.RetryClass != "" {
		reason = fmt.Sprintf("%s (%s)", reason, result.RetryClass)
	}
	return reason
}

func compactStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		out = append(out, value)
	}
	return out
}

func convertVerificationResults(commands []string, results []verifycommand.CommandResult) []state.VerificationResult {
	converted := make([]state.VerificationResult, 0, len(results))
	for i, result := range results {
		command := result.Command
		if command == "" && i < len(commands) {
			command = commands[i]
		}
		converted = append(converted, state.VerificationResult{
			Command:    command,
			ExitCode:   result.ExitCode,
			Passed:     result.ExitCode == 0 && !result.TimedOut,
			DurationMS: result.DurationMS,
			StdoutPath: result.StdoutPath,
			StderrPath: result.StderrPath,
			StartedAt:  result.StartedAt,
			FinishedAt: result.FinishedAt,
		})
	}
	return converted
}

func verificationStartedAt(results []state.VerificationResult) time.Time {
	if len(results) == 0 {
		return time.Time{}
	}
	return results[0].StartedAt
}

func verificationFinishedAt(results []state.VerificationResult) time.Time {
	if len(results) == 0 {
		return time.Time{}
	}
	return results[len(results)-1].FinishedAt
}

func convertReviewFindings(findings []runtime.ReviewFinding) []state.ReviewFinding {
	converted := make([]state.ReviewFinding, 0, len(findings))
	for _, finding := range findings {
		converted = append(converted, state.ReviewFinding{
			ID:              finding.ID,
			Severity:        state.Severity(finding.Severity),
			Title:           finding.Title,
			Body:            finding.Body,
			File:            finding.File,
			Line:            finding.Line,
			Disposition:     string(finding.Disposition),
			WaivedBy:        finding.WaivedBy,
			WaivedAt:        finding.WaivedAt,
			WaiverReason:    finding.WaiverReason,
			WaiverExpiresAt: derefReviewExpiry(finding.WaiverExpiresAt),
		})
	}
	return converted
}

func derefReviewExpiry(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}
