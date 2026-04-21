package engine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
	"verk/internal/adapters/repo/git"
	"verk/internal/adapters/runtime"
	"verk/internal/adapters/ticketstore/tkmd"
	"verk/internal/policy"
	"verk/internal/state"

	verifycommand "verk/internal/adapters/verify/command"
)

const maxRuntimeRetryAttempts = 2

var (
	errRuntimeExecutionBlocked = errors.New("runtime execution blocked")
	errClaimRenewalLost        = errors.New("claim renewal failed")
)

type RunTicketRequest struct {
	RepoRoot             string
	RunID                string
	BaseCommit           string
	Ticket               tkmd.Ticket
	Plan                 state.PlanArtifact
	Claim                state.ClaimArtifact
	Adapter              runtime.Adapter
	Config               policy.Config
	VerificationCommands []string
	EnforceSingleScope   bool
	Progress             chan<- ProgressEvent
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
	ctx                    context.Context
	req                    RunTicketRequest
	cfg                    policy.Config
	paths                  ticketRunPaths
	repoRoot               string
	currentPhase           state.TicketPhase
	blockReason            string
	createdAt              time.Time // set once; zero means lazy-init on first snapshot()
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

func RunTicket(ctx context.Context, req RunTicketRequest) (result RunTicketResult, retErr error) { //nolint:gocognit,cyclop // complex ticket state machine; refactor into sub-functions
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
		ctx:          ctx,
		req:          req,
		cfg:          cfg,
		paths:        buildTicketRunPaths(absRepoRoot, req.RunID, req.Ticket.ID),
		repoRoot:     absRepoRoot,
		currentPhase: req.Plan.Phase,
	}

	restoredPhaseFromSnapshot := false

	// Restore persisted lifecycle state when the ticket is being resumed.
	// Ignore errors for fresh tickets: missing artifacts are normal before
	// first run or when run artifacts were manually reset.
	var prevSnapshot TicketRunSnapshot
	if err := loadTicketSnapshot(absRepoRoot, req.RunID, req.Ticket.ID, &prevSnapshot); err == nil {
		if !prevSnapshot.CreatedAt.IsZero() {
			st.createdAt = prevSnapshot.CreatedAt
		}
		switch {
		case prevSnapshot.CurrentPhase == "":
		case prevSnapshot.CurrentPhase == st.currentPhase:
			restoredPhaseFromSnapshot = true
		case st.currentPhase == state.TicketPhaseIntake && prevSnapshot.CurrentPhase != state.TicketPhaseBlocked:
			st.currentPhase = prevSnapshot.CurrentPhase
			restoredPhaseFromSnapshot = true
		}
		st.blockReason = prevSnapshot.BlockReason
		st.implementationAttempts = prevSnapshot.ImplementationAttempts
		st.verificationAttempts = prevSnapshot.VerificationAttempts
		st.reviewAttempts = prevSnapshot.ReviewAttempts
		st.implementation = prevSnapshot.Implementation
		st.verification = prevSnapshot.Verification
		st.review = prevSnapshot.Review
		st.closeout = prevSnapshot.Closeout
		if len(prevSnapshot.RepairCycles) > 0 {
			st.repairCycles = append(st.repairCycles, prevSnapshot.RepairCycles...)
		}
	}

	// Release claim on any error return to prevent leaked claims blocking retries.
	// Successful paths release the claim explicitly before returning nil.
	defer func() {
		if retErr != nil {
			_ = st.releaseClaim()
		}
	}()

	// Update ticket store on exit — the ticket's own phase determines its store status.
	defer func() {
		ticketPath := filepath.Join(absRepoRoot, ".tickets", req.Ticket.ID+".md")
		var targetStatus tkmd.Status
		switch st.currentPhase {
		case state.TicketPhaseClosed:
			targetStatus = tkmd.StatusClosed
		case state.TicketPhaseBlocked:
			targetStatus = tkmd.StatusBlocked
		default:
			targetStatus = tkmd.StatusOpen
		}
		if err := updateTicketStatus(ticketPath, targetStatus); err != nil {
			log.Printf("failed to update ticket %s status to %s: %v", req.Ticket.ID, targetStatus, err)
		}
	}()

	if st.currentPhase == "" {
		st.currentPhase = state.TicketPhaseIntake
	}
	if !canStartTicketRunFromPhase(st.currentPhase, restoredPhaseFromSnapshot) {
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
			workerProfile := workerProfileForPlan(req.Plan, cfg)
			workerReq := runtime.WorkerRequest{
				RunID:           req.RunID,
				TicketID:        req.Ticket.ID,
				LeaseID:         req.Claim.LeaseID,
				Attempt:         st.implementationAttempts + 1,
				Runtime:         workerProfile.Runtime,
				Model:           workerProfile.Model,
				Reasoning:       workerProfile.Reasoning,
				WorktreePath:    absRepoRoot,
				Instructions:    buildImplementPhaseInstructions(st, st.implementationAttempts+1),
				ExecutionConfig: executionConfigFromPolicy(cfg),
				OnProgress:      func(detail string) { st.progressDetail(detail) },
			}
			st.progressDetail(fmt.Sprintf("%s worker running", workerProfile.Runtime))
			result, effectiveWorkerReq, err := st.runWorkerWithRuntimeControls(ctx, workerReq)
			if err != nil {
				if errors.Is(err, errRuntimeExecutionBlocked) {
					if err := st.persist(); err != nil {
						return RunTicketResult{}, err
					}
					if err := st.releaseClaim(); err != nil {
						return RunTicketResult{}, err
					}
					return RunTicketResult{Snapshot: st.snapshot(), Path: st.paths.snapshotPath}, nil
				}
				return RunTicketResult{}, err
			}
			if err := handleImplementResult(st, result, effectiveWorkerReq); err != nil {
				return RunTicketResult{}, err
			}
			st.progressDetail(fmt.Sprintf("worker %s (%s)", result.Status, result.CompletionCode))
			if st.implementation != nil && len(st.implementation.ChangedFiles) > 0 {
				st.progressDetail(fmt.Sprintf("%d file(s) changed", len(st.implementation.ChangedFiles)))
			}

			if st.currentPhase == state.TicketPhaseVerify {
				blocked, err := st.executeVerification(ctx, absRepoRoot)
				if err != nil {
					return RunTicketResult{}, err
				}
				if blocked {
					if err := st.persist(); err != nil {
						return RunTicketResult{}, err
					}
					if err := st.releaseClaim(); err != nil {
						return RunTicketResult{}, err
					}
					return RunTicketResult{Snapshot: st.snapshot(), Path: st.paths.snapshotPath}, nil
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
			diffForReview, err := collectDiff(absRepoRoot, req.BaseCommit)
			if err != nil {
				return RunTicketResult{}, fmt.Errorf("collect diff for review: %w", err)
			}
			reviewerProfile := reviewerProfileForPlan(req.Plan, cfg)
			reviewReq := runtime.ReviewRequest{
				RunID:                    req.RunID,
				TicketID:                 req.Ticket.ID,
				LeaseID:                  req.Claim.LeaseID,
				Attempt:                  st.reviewAttempts + 1,
				Runtime:                  reviewerProfile.Runtime,
				Model:                    reviewerProfile.Model,
				Reasoning:                reviewerProfile.Reasoning,
				InputArtifactPath:        st.paths.verificationPath,
				Instructions:             renderReviewInstructions(req.Plan, st.reviewAttempts+1),
				Diff:                     diffForReview,
				Standards:                runtime.BuildReviewStandards(runtime.DetectLanguages(diffForReview)),
				EffectiveReviewThreshold: req.Plan.EffectiveReviewThreshold,
				ExecutionConfig:          executionConfigFromPolicy(cfg),
				OnProgress:               func(detail string) { st.progressDetail(detail) },
			}
			st.progressDetail(fmt.Sprintf("%s reviewer running", reviewerProfile.Runtime))
			result, effectiveReviewReq, err := st.runReviewerWithRuntimeControls(ctx, reviewReq)
			if err != nil {
				if errors.Is(err, errRuntimeExecutionBlocked) {
					if err := st.persist(); err != nil {
						return RunTicketResult{}, err
					}
					if err := st.releaseClaim(); err != nil {
						return RunTicketResult{}, err
					}
					return RunTicketResult{Snapshot: st.snapshot(), Path: st.paths.snapshotPath}, nil
				}
				return RunTicketResult{}, err
			}
			if err := handleReviewOutcome(st, result, effectiveReviewReq); err != nil {
				return RunTicketResult{}, err
			}
			if result.ReviewStatus == runtime.ReviewStatusPassed {
				st.progressDetail(fmt.Sprintf("review passed (%d finding(s))", len(result.Findings)))
			} else {
				blocking := 0
				for _, f := range result.Findings {
					if f.Disposition == runtime.ReviewDispositionOpen {
						blocking++
					}
				}
				st.progressDetail(fmt.Sprintf("review: %d finding(s), %d blocking", len(result.Findings), blocking))
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
			if st.currentPhase == state.TicketPhaseCloseout {
				continue
			}

		case state.TicketPhaseVerify:
			blocked, err := st.executeVerification(ctx, absRepoRoot)
			if err != nil {
				return RunTicketResult{}, err
			}
			if blocked {
				if err := st.persist(); err != nil {
					return RunTicketResult{}, err
				}
				if err := st.releaseClaim(); err != nil {
					return RunTicketResult{}, err
				}
				return RunTicketResult{Snapshot: st.snapshot(), Path: st.paths.snapshotPath}, nil
			}
			continue

		case state.TicketPhaseCloseout:
			if st.closeout == nil {
				closeout, err := BuildCloseoutArtifact(st.req.Ticket, st.req.Plan, st.verification, st.review)
				if err != nil {
					return RunTicketResult{}, err
				}
				st.closeout = &closeout
			}
			if st.closeout.Closable {
				st.progressDetail("all gates passed")
				st.blockReason = ""
				if err := st.transitionTo(state.TicketPhaseClosed); err != nil {
					return RunTicketResult{}, err
				}
			} else {
				st.blockReason = st.closeout.FailedGate
				next := state.TicketPhaseBlocked
				if st.closeout.FailedGate == gateReview {
					next = state.TicketPhaseRepair
				}
				if err := st.transitionTo(next); err != nil {
					return RunTicketResult{}, err
				}
			}
			if err := st.persist(); err != nil {
				return RunTicketResult{}, err
			}
			continue

		case state.TicketPhaseRepair:
			workerProfile := workerProfileForPlan(req.Plan, cfg)
			workerReq := runtime.WorkerRequest{
				RunID:           req.RunID,
				TicketID:        req.Ticket.ID,
				LeaseID:         req.Claim.LeaseID,
				Attempt:         st.implementationAttempts + 1,
				Runtime:         workerProfile.Runtime,
				Model:           workerProfile.Model,
				Reasoning:       workerProfile.Reasoning,
				WorktreePath:    absRepoRoot,
				Instructions:    renderRepairInstructions(st),
				ExecutionConfig: executionConfigFromPolicy(cfg),
				OnProgress:      func(detail string) { st.progressDetail(detail) },
			}
			st.progressDetail(fmt.Sprintf("%s repair worker running", workerProfile.Runtime))
			result, effectiveRepairReq, err := st.runWorkerWithRuntimeControls(ctx, workerReq)
			if err != nil {
				if errors.Is(err, errRuntimeExecutionBlocked) {
					if err := st.persist(); err != nil {
						return RunTicketResult{}, err
					}
					if err := st.releaseClaim(); err != nil {
						return RunTicketResult{}, err
					}
					return RunTicketResult{Snapshot: st.snapshot(), Path: st.paths.snapshotPath}, nil
				}
				return RunTicketResult{}, err
			}
			if err := handleImplementResult(st, result, effectiveRepairReq); err != nil {
				return RunTicketResult{}, err
			}
			if st.currentPhase == state.TicketPhaseVerify {
				blocked, err := st.executeVerification(ctx, absRepoRoot)
				if err != nil {
					return RunTicketResult{}, err
				}
				if blocked {
					if err := st.persist(); err != nil {
						return RunTicketResult{}, err
					}
					if err := st.releaseClaim(); err != nil {
						return RunTicketResult{}, err
					}
					return RunTicketResult{Snapshot: st.snapshot(), Path: st.paths.snapshotPath}, nil
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

func handleImplementResult(st *ticketRunState, result runtime.WorkerResult, req runtime.WorkerRequest) error {
	if err := result.Validate(); err != nil {
		return err
	}
	if err := tkmd.ValidateLeaseFence(st.req.Claim.LeaseID, result.LeaseID); err != nil {
		return err
	}

	st.implementationAttempts = req.Attempt
	// Record the attempt profile actually used (runtime/model/reasoning)
	// from the request so benchmark and audit consumers can reproduce
	// or evaluate runs. This is attempt metadata, not an execution lock:
	// retries and resumes are free to use the current or fallback profile.
	attemptRuntime := strings.TrimSpace(req.Runtime)
	if attemptRuntime == "" {
		attemptRuntime = chosenRuntime(st.req.Plan, st.cfg)
	}
	st.implementation = &state.ImplementationArtifact{
		ArtifactMeta: state.ArtifactMeta{
			SchemaVersion: artifactSchemaVersion,
			RunID:         st.req.RunID,
			CreatedAt:     stateTime(),
			UpdatedAt:     stateTime(),
		},
		TicketID:          st.req.Ticket.ID,
		Attempt:           st.implementationAttempts,
		Runtime:           attemptRuntime,
		Model:             strings.TrimSpace(req.Model),
		Reasoning:         strings.TrimSpace(req.Reasoning),
		FallbackReason:    strings.TrimSpace(req.FallbackReason),
		Status:            string(result.Status),
		CompletionCode:    result.CompletionCode,
		RetryClass:        result.RetryClass,
		Concerns:          result.Concerns,
		LeaseID:           result.LeaseID,
		InputArtifactPath: req.InputArtifactPath,
		StartedAt:         result.StartedAt,
		FinishedAt:        result.FinishedAt,
		Artifacts:         compactStrings([]string{result.StdoutPath, result.StderrPath, result.ResultArtifactPath}),
		TokenUsage:        cloneRuntimeTokenUsage(result.TokenUsage),
		ActivityStats:     cloneRuntimeActivityStats(result.ActivityStats),
		ChangedFiles:      []string{},
	}

	switch result.Status {
	case runtime.WorkerStatusDone, runtime.WorkerStatusDoneWithConcerns:
		if err := st.transitionTo(state.TicketPhaseVerify); err != nil {
			return err
		}
		st.blockReason = ""
		st.implementation.BlockReason = ""
		changedFiles, err := collectChangedFiles(st.repoRoot, st.req.BaseCommit)
		if err != nil {
			return fmt.Errorf("collect changed files: %w", err)
		}
		st.implementation.ChangedFiles = changedFiles
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

func canStartTicketRunFromPhase(phase state.TicketPhase, restoredFromSnapshot bool) bool {
	switch phase {
	case state.TicketPhaseIntake, state.TicketPhaseImplement, state.TicketPhaseVerify:
		return true
	case state.TicketPhaseReview, state.TicketPhaseRepair, state.TicketPhaseCloseout:
		return restoredFromSnapshot
	default:
		return false
	}
}

// executeVerification runs the verification gate for the current ticket and
// transitions into review on success. A true return value indicates the ticket
// is blocked and can be safely returned immediately by the caller.
func (st *ticketRunState) executeVerification(ctx context.Context, repoRoot string) (bool, error) {
	if err := checkSingleTicketScope(st); err != nil {
		return false, err
	}
	if st.currentPhase == state.TicketPhaseBlocked {
		return true, nil
	}

	if err := st.persist(); err != nil {
		return false, err
	}
	verifyArtifact, verifyPassed, err := st.runVerification(ctx, repoRoot)
	if err != nil {
		return false, err
	}
	st.verification = verifyArtifact
	st.verificationAttempts = verifyArtifact.Attempt
	if err := st.persist(); err != nil {
		return false, err
	}
	if !verifyPassed {
		if err := handleVerificationFailure(st, *verifyArtifact); err != nil {
			return false, err
		}
		if err := st.persist(); err != nil {
			return false, err
		}
		return false, nil
	}

	if err := st.transitionTo(state.TicketPhaseReview); err != nil {
		return false, err
	}
	if err := st.persist(); err != nil {
		return false, err
	}
	return false, nil
}

func checkSingleTicketScope(st *ticketRunState) error {
	if !st.req.EnforceSingleScope {
		return nil
	}
	ownedPaths := st.req.Ticket.OwnedPaths
	if len(ownedPaths) == 0 {
		st.blockReason = fmt.Sprintf("single-ticket scope violation: ticket %q has no scope declarations", st.req.Ticket.ID)
		return st.transitionTo(state.TicketPhaseBlocked)
	}
	var changedFiles []string
	if st.implementation != nil {
		changedFiles = st.implementation.ChangedFiles
	}
	if len(changedFiles) == 0 {
		return nil
	}
	if err := CheckScopeViolation(changedFiles, ownedPaths); err != nil {
		st.blockReason = fmt.Sprintf("single-ticket scope violation: %v", err)
		return st.transitionTo(state.TicketPhaseBlocked)
	}
	return nil
}

func collectDiff(repoRoot, baseCommit string) (string, error) {
	baseCommit = strings.TrimSpace(baseCommit)
	if baseCommit == "" {
		return "", nil
	}
	repo, err := git.New(repoRoot)
	if err != nil {
		return "", fmt.Errorf("open git repo for diff: %w", err)
	}
	diff, err := repo.DiffAgainst(baseCommit)
	if err != nil {
		return "", fmt.Errorf("collect diff against %s: %w", baseCommit, err)
	}
	return diff, nil
}

func collectChangedFiles(repoRoot, baseCommit string) ([]string, error) {
	baseCommit = strings.TrimSpace(baseCommit)
	if baseCommit == "" {
		return nil, nil
	}
	repo, err := git.New(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("open git repo for changed files: %w", err)
	}
	files, err := repo.ChangedFilesAgainst(baseCommit)
	if err != nil {
		return nil, fmt.Errorf("collect changed files against %s: %w", baseCommit, err)
	}
	filtered := filterEngineOwnedFiles(files)
	return filtered, nil
}

func handleVerificationFailure(st *ticketRunState, verification state.VerificationArtifact) error {
	if !verification.Passed {
		st.verification = &verification
	}
	failingIDs := verificationFailingCheckIDs(st.verification)
	if st.implementationAttempts >= st.cfg.Policy.MaxImplementationAttempts {
		st.blockReason = buildVerificationBlockReason(st.implementationAttempts, failingIDs)
		if err := st.transitionTo(state.TicketPhaseBlocked); err != nil {
			return err
		}
		if st.verification != nil {
			st.verification.Passed = false
			recordVerificationRepairLimit(st.verification, st.implementationAttempts, st.cfg.Policy.MaxImplementationAttempts, failingIDs)
		}
		return nil
	}

	if st.verification != nil && len(failingIDs) > 0 {
		// Record a repair trigger cycle so validation coverage carries
		// the referenced check ids. The cycle is marked repair_pending
		// until the next verification run resolves it.
		appendVerificationRepairCycle(st, failingIDs)
	}

	if err := st.transitionTo(state.TicketPhaseImplement); err != nil {
		return err
	}
	return nil
}

// verificationFailingCheckIDs returns the ids of failed executions in
// the verification artifact's coverage. Empty when coverage is missing
// or all checks passed.
func verificationFailingCheckIDs(verification *state.VerificationArtifact) []string {
	if verification == nil || verification.ValidationCoverage == nil {
		return nil
	}
	return failingCheckIDs(*verification.ValidationCoverage)
}

// buildVerificationBlockReason composes a clear block reason for a
// ticket that exhausted its verify-loop budget. When the coverage
// artifact recorded specific failing check ids, they are included so
// operators can see exactly which commands could not be repaired.
func buildVerificationBlockReason(attempts int, failingIDs []string) string {
	base := fmt.Sprintf("%s: failed after %d attempt(s)", state.EscalationNonConvergentVerification, attempts)
	if len(failingIDs) == 0 {
		return base
	}
	return fmt.Sprintf("%s; unresolved checks: %s", base, strings.Join(failingIDs, ", "))
}

// buildReviewRepairBlockReason composes a block reason for a ticket that
// exhausted its review-repair budget. The reason keeps the canonical
// non-convergent prefix so downstream parsers continue to work, cites the
// unresolved finding ids (when known), and names operator input as the
// suggested next action so the ticket becomes a user-needed blocker with
// a concrete recovery path.
func buildReviewRepairBlockReason(cycles int, findingIDs []string) string {
	base := fmt.Sprintf("%s: repair limit reached after %d cycle(s)", state.EscalationNonConvergentReview, cycles)
	if len(findingIDs) == 0 {
		return fmt.Sprintf("%s; operator input required to resolve unresolved review findings", base)
	}
	return fmt.Sprintf("%s; unresolved findings: %s; operator input required", base, strings.Join(findingIDs, ", "))
}

// recordVerificationRepairLimit attaches a ValidationRepairLimit marker
// to the verification artifact's coverage so the bounded-loop stopping
// condition is auditable in closeout artifacts and resume reports.
func recordVerificationRepairLimit(verification *state.VerificationArtifact, reached, limit int, failingIDs []string) {
	if verification == nil || verification.ValidationCoverage == nil {
		return
	}
	verification.ValidationCoverage.RepairLimit = &state.ValidationRepairLimit{
		Name:      "max_implementation_attempts",
		Limit:     limit,
		Reached:   reached,
		Reason:    buildVerificationBlockReason(reached, failingIDs),
		PolicyRef: "policy.max_implementation_attempts",
	}
	verification.ValidationCoverage.Closable = false
	if verification.ValidationCoverage.BlockReason == "" {
		verification.ValidationCoverage.BlockReason = verification.ValidationCoverage.RepairLimit.Reason
	}
}

// appendVerificationRepairCycle records a repair cycle in the ticket
// state whose trigger check ids reference the failing verification
// checks. The cycle's Status stays repair_pending until the next
// verification run settles it.
func appendVerificationRepairCycle(st *ticketRunState, failingCheckIDs []string) {
	if len(failingCheckIDs) == 0 {
		return
	}
	cycle := state.RepairCycleArtifact{
		ArtifactMeta: state.ArtifactMeta{
			SchemaVersion: artifactSchemaVersion,
			RunID:         st.req.RunID,
			CreatedAt:     stateTime(),
			UpdatedAt:     stateTime(),
		},
		TicketID:             st.req.Ticket.ID,
		Cycle:                len(st.repairCycles) + 1,
		TriggerCheckIDs:      append([]string(nil), failingCheckIDs...),
		InputReviewArtifact:  "",
		VerificationArtifact: st.paths.verificationPath,
		Status:               "repair_pending",
		StartedAt:            time.Now().UTC(),
		Scope:                state.ValidationScopeTicket,
		RepairNotes:          fmt.Sprintf("repair cycle %d triggered by failing checks: %s", len(st.repairCycles)+1, strings.Join(failingCheckIDs, ", ")),
	}
	st.repairCycles = append(st.repairCycles, cycle)
}

func handleReviewOutcome(st *ticketRunState, result runtime.ReviewResult, req runtime.ReviewRequest) error {
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

	st.reviewAttempts = req.Attempt
	// Record attempt profile actually used so each review attempt is auditable.
	reviewerRuntime := strings.TrimSpace(req.Runtime)
	if reviewerRuntime == "" {
		reviewerRuntime = reviewerProfileForPlan(st.req.Plan, st.cfg).Runtime
	}
	st.review = &state.ReviewFindingsArtifact{
		ArtifactMeta: state.ArtifactMeta{
			SchemaVersion: artifactSchemaVersion,
			RunID:         st.req.RunID,
			CreatedAt:     stateTime(),
			UpdatedAt:     stateTime(),
		},
		TicketID:                 st.req.Ticket.ID,
		Attempt:                  st.reviewAttempts,
		ReviewerRuntime:          reviewerRuntime,
		ReviewerModel:            strings.TrimSpace(req.Model),
		ReviewerReasoning:        strings.TrimSpace(req.Reasoning),
		FallbackReason:           strings.TrimSpace(req.FallbackReason),
		Summary:                  result.Summary,
		Findings:                 convertReviewFindings(result.Findings),
		BlockingFindings:         append([]string(nil), blockingIDs...),
		Passed:                   len(blockingIDs) == 0,
		EffectiveReviewThreshold: st.req.Plan.EffectiveReviewThreshold,
		Artifacts:                compactStrings([]string{result.StdoutPath, result.StderrPath, result.ResultArtifactPath}),
		TokenUsage:               cloneRuntimeTokenUsage(result.TokenUsage),
		ActivityStats:            cloneRuntimeActivityStats(result.ActivityStats),
		StartedAt:                result.StartedAt,
		FinishedAt:               result.FinishedAt,
	}

	if len(blockingIDs) == 0 {
		// Mark the last repair cycle as completed now that review has passed.
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
		if err := st.persist(); err != nil {
			return err
		}
		return nil
	}

	cycle := state.RepairCycleArtifact{
		ArtifactMeta: state.ArtifactMeta{
			SchemaVersion: artifactSchemaVersion,
			RunID:         st.req.RunID,
			CreatedAt:     stateTime(),
			UpdatedAt:     stateTime(),
		},
		TicketID:            st.req.Ticket.ID,
		Cycle:               len(st.repairCycles) + 1,
		TriggerFindingIDs:   append([]string(nil), blockingIDs...),
		InputReviewArtifact: st.paths.reviewPath,
		RepairNotes:         fmt.Sprintf("repair cycle %d triggered by findings: %s", len(st.repairCycles)+1, strings.Join(blockingIDs, ", ")),
		ReviewArtifact:      st.paths.reviewPath,
		Status:              "repair_pending",
		StartedAt:           time.Now().UTC(),
		Scope:               state.ValidationScopeTicket,
	}
	st.repairCycles = append(st.repairCycles, cycle)

	if len(st.repairCycles) > st.cfg.Policy.MaxRepairCycles {
		// Compose an actionable block reason that names the unresolved
		// finding ids, because operators rely on this string to decide
		// whether to escalate, waive, or rework the ticket. Keeping the
		// non-convergent prefix preserves downstream parsers that key off
		// the canonical escalation reason.
		st.blockReason = buildReviewRepairBlockReason(len(st.repairCycles)-1, blockingIDs)
		last := &st.repairCycles[len(st.repairCycles)-1]
		last.Status = "blocked"
		last.FinishedAt = time.Now().UTC()
		// Persist a ValidationRepairLimit marker on the cycle so the
		// limiting reason survives in artifacts for audit and resume.
		last.PolicyLimitReached = &state.ValidationRepairLimit{
			Name:      "max_repair_cycles",
			Limit:     st.cfg.Policy.MaxRepairCycles,
			Reached:   len(st.repairCycles) - 1,
			Reason:    st.blockReason,
			PolicyRef: "policy.max_repair_cycles",
		}
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

	// Quality commands run first (from global config) and gate validation commands.
	// They are prepended to the combined result set so the artifact records all runs.
	var declaredResults []verifycommand.CommandResult
	if len(st.cfg.Verification.QualityCommands) > 0 {
		qualityResults, err := verifycommand.RunQualityCommands(ctx, repoRoot, st.cfg.Verification.QualityCommands, st.cfg.Verification)
		if err != nil {
			return nil, false, fmt.Errorf("run quality commands: %w", err)
		}
		declaredResults = append(declaredResults, qualityResults...)
	}

	// Only run per-ticket validation commands when there are any declared.
	if len(commands) > 0 {
		validationResults, err := verifycommand.RunCommands(ctx, repoRoot, commands, st.cfg.Verification)
		if err != nil {
			return nil, false, fmt.Errorf("run validation commands: %w", err)
		}
		declaredResults = append(declaredResults, validationResults...)
	}

	// Derive focused checks from the implementation's changed files and
	// ticket scope. Running derived commands here (after declared/quality)
	// gives closeout a chance to catch file-scoped regressions that the
	// ticket forgot to declare — a failed ruff check after a Python edit,
	// for example. Derived checks are advisory by default: their results
	// feed ValidationCoverage and can trigger repair routing, but they do
	// not flip the legacy `Passed` flag unless promoted to required.
	derivation := deriveTicketChecks(st)
	derivedCommandResults, derivedOrdered, err := runDerivedChecks(ctx, repoRoot, derivation.Checks, st.cfg.Verification)
	if err != nil {
		return nil, false, err
	}

	allResults := make([]verifycommand.CommandResult, 0, len(declaredResults)+len(derivedOrdered))
	allResults = append(allResults, declaredResults...)
	allResults = append(allResults, derivedOrdered...)

	// Build flat command list for the artifact (CommandResult.Command is canonical).
	allCommands := make([]string, 0, len(allResults))
	for _, r := range allResults {
		allCommands = append(allCommands, r.Command)
	}

	converted := convertVerificationResults(nil, allResults)
	// `Passed` is keyed on declared/quality commands AND non-advisory
	// derived checks. Advisory derived checks (the default) are captured
	// in ValidationCoverage for auditing and repair routing but do not
	// block the verify loop on their own. Failing required derived
	// checks, once repair policy promotes them, participate in this
	// gating flag so the verify → implement loop retries them.
	declaredPassed := true
	if len(declaredResults) > 0 {
		declaredPassed = verifycommand.DeriveVerificationPassed(declaredResults)
	}
	requiredDerivedPassed := requiredDerivedChecksPassed(derivation.Checks, derivedCommandResults)
	attempt := st.verificationAttempts + 1
	coverage := assembleTicketValidationCoverage(
		st.req.Plan,
		st.req.RunID,
		attempt,
		declaredResults,
		derivation,
		derivedCommandResults,
		st.review,
		st.repairCycles,
		nil,
	)

	artifact := &state.VerificationArtifact{
		ArtifactMeta: state.ArtifactMeta{
			SchemaVersion: artifactSchemaVersion,
			RunID:         st.req.RunID,
			CreatedAt:     stateTime(),
			UpdatedAt:     stateTime(),
		},
		TicketID:           st.req.Ticket.ID,
		Attempt:            attempt,
		Commands:           allCommands,
		Results:            converted,
		Passed:             declaredPassed && requiredDerivedPassed,
		RepoRoot:           repoRoot,
		StartedAt:          verificationStartedAt(converted),
		FinishedAt:         verificationFinishedAt(converted),
		ValidationCoverage: &coverage,
	}
	for _, vr := range converted {
		mark := "✓"
		if !vr.Passed {
			mark = "✗"
		}
		st.progressDetail(fmt.Sprintf("%s %s", vr.Command, mark))
	}
	return artifact, artifact.Passed, nil
}

// runWorkerWithRuntimeControls executes a worker with retry logic and fallback
// profile support. On the first retry (attempt > 0), it switches to the
// configured WorkerFallback profile when one is set, so that transient model
// unavailability or rate-limiting can recover without operator intervention.
// The effective request (primary or fallback) is returned alongside the result
// so the caller can record the runtime/model/reasoning that was actually used.
func (st *ticketRunState) runWorkerWithRuntimeControls(ctx context.Context, req runtime.WorkerRequest) (runtime.WorkerResult, runtime.WorkerRequest, error) {
	var lastErr error
	effectiveReq := req
	for attempt := 0; attempt <= maxRuntimeRetryAttempts; attempt++ {
		effectiveReq = req
		if attempt > 0 && !st.cfg.Runtime.WorkerFallback.IsZero() {
			// Apply the configured fallback profile on any retry so that
			// transient model failures (rate-limit, model-unavailable, etc.)
			// can recover automatically without operator input.
			fallback := st.cfg.Runtime.WorkerFallback
			effectiveReq.Runtime = strings.TrimSpace(fallback.Runtime)
			effectiveReq.Model = strings.TrimSpace(fallback.Model)
			effectiveReq.Reasoning = strings.TrimSpace(fallback.Reasoning)
			effectiveReq.FallbackReason = "primary profile unavailable, retrying with fallback"
		}
		result, err := st.runWorkerWithClaimRenewal(ctx, effectiveReq)
		if err != nil {
			if errors.Is(err, errClaimRenewalLost) {
				if err := st.blockRuntimeExecution(fmt.Sprintf("claim renewal lost during worker execution: %v", err)); err != nil {
					return runtime.WorkerResult{}, effectiveReq, err
				}
				return runtime.WorkerResult{}, effectiveReq, errRuntimeExecutionBlocked
			}
			if errors.Is(err, context.Canceled) {
				return runtime.WorkerResult{}, effectiveReq, err
			}
			if shouldRetryRuntimeError(err) && attempt < maxRuntimeRetryAttempts {
				lastErr = err
				continue
			}
			lastErr = err
			break
		}

		switch result.RetryClass {
		case runtime.RetryClassRetryable:
			lastErr = fmt.Errorf("retryable worker failure: %s", workerBlockReason(runtime.WorkerResult{
				Status:         result.Status,
				CompletionCode: result.CompletionCode,
				BlockReason:    result.BlockReason,
				RetryClass:     result.RetryClass,
			}))
			if attempt < maxRuntimeRetryAttempts {
				continue
			}
		case runtime.RetryClassBlockedByOperatorInput:
			reason := fmt.Sprintf("worker blocked by operator input: %s", workerBlockReason(runtime.WorkerResult{
				Status:         result.Status,
				CompletionCode: result.CompletionCode,
				BlockReason:    result.BlockReason,
				RetryClass:     result.RetryClass,
			}))
			if err := st.blockRuntimeExecution(reason); err != nil {
				return runtime.WorkerResult{}, effectiveReq, err
			}
			return runtime.WorkerResult{}, effectiveReq, errRuntimeExecutionBlocked
		default:
			return result, effectiveReq, nil
		}

		if attempt >= maxRuntimeRetryAttempts {
			break
		}
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("worker runtime failed")
	}
	if !shouldRetryRuntimeError(lastErr) {
		return runtime.WorkerResult{}, effectiveReq, lastErr
	}
	if err := st.blockRuntimeExecution(fmt.Sprintf("retryable worker failure after %d retries: %v", maxRuntimeRetryAttempts, lastErr)); err != nil {
		return runtime.WorkerResult{}, effectiveReq, err
	}
	return runtime.WorkerResult{}, effectiveReq, errRuntimeExecutionBlocked
}

// runReviewerWithRuntimeControls executes a reviewer with retry logic and
// fallback profile support. On the first retry (attempt > 0), it switches to
// the configured ReviewerFallback profile when one is set. The effective
// request is returned so the caller can record the runtime/model/reasoning
// actually used for the attempt.
func (st *ticketRunState) runReviewerWithRuntimeControls(ctx context.Context, req runtime.ReviewRequest) (runtime.ReviewResult, runtime.ReviewRequest, error) {
	var lastErr error
	effectiveReq := req
	for attempt := 0; attempt <= maxRuntimeRetryAttempts; attempt++ {
		effectiveReq = req
		if attempt > 0 && !st.cfg.Runtime.ReviewerFallback.IsZero() {
			fallback := st.cfg.Runtime.ReviewerFallback
			effectiveReq.Runtime = strings.TrimSpace(fallback.Runtime)
			effectiveReq.Model = strings.TrimSpace(fallback.Model)
			effectiveReq.Reasoning = strings.TrimSpace(fallback.Reasoning)
			effectiveReq.FallbackReason = "primary profile unavailable, retrying with fallback"
		}
		result, err := st.runReviewerWithClaimRenewal(ctx, effectiveReq)
		if err != nil {
			if errors.Is(err, errClaimRenewalLost) {
				if err := st.blockRuntimeExecution(fmt.Sprintf("claim renewal lost during reviewer execution: %v", err)); err != nil {
					return runtime.ReviewResult{}, effectiveReq, err
				}
				return runtime.ReviewResult{}, effectiveReq, errRuntimeExecutionBlocked
			}
			if errors.Is(err, context.Canceled) {
				return runtime.ReviewResult{}, effectiveReq, err
			}
			if shouldRetryRuntimeError(err) && attempt < maxRuntimeRetryAttempts {
				lastErr = err
				continue
			}
			lastErr = err
			break
		}

		switch result.RetryClass {
		case runtime.RetryClassRetryable:
			lastErr = fmt.Errorf("retryable reviewer failure: %s", workerBlockReason(runtime.WorkerResult{
				Status:         result.Status,
				CompletionCode: result.CompletionCode,
				RetryClass:     result.RetryClass,
			}))
			if attempt < maxRuntimeRetryAttempts {
				continue
			}
		case runtime.RetryClassBlockedByOperatorInput:
			reason := fmt.Sprintf("reviewer blocked by operator input: %s", workerBlockReason(runtime.WorkerResult{
				Status:         result.Status,
				CompletionCode: result.CompletionCode,
				RetryClass:     result.RetryClass,
			}))
			if err := st.blockRuntimeExecution(reason); err != nil {
				return runtime.ReviewResult{}, effectiveReq, err
			}
			return runtime.ReviewResult{}, effectiveReq, errRuntimeExecutionBlocked
		default:
			return result, effectiveReq, nil
		}

		if attempt >= maxRuntimeRetryAttempts {
			break
		}
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("reviewer runtime failed")
	}
	if !shouldRetryRuntimeError(lastErr) {
		return runtime.ReviewResult{}, effectiveReq, lastErr
	}
	if err := st.blockRuntimeExecution(fmt.Sprintf("retryable reviewer failure after %d retries: %v", maxRuntimeRetryAttempts, lastErr)); err != nil {
		return runtime.ReviewResult{}, effectiveReq, err
	}
	return runtime.ReviewResult{}, effectiveReq, errRuntimeExecutionBlocked
}

func (st *ticketRunState) runWorkerWithClaimRenewal(ctx context.Context, req runtime.WorkerRequest) (runtime.WorkerResult, error) {
	renewCtx, stopRenewal := st.startClaimRenewal(ctx)
	result, err := st.req.Adapter.RunWorker(renewCtx, req)
	renewErr := stopRenewal()
	if renewErr != nil {
		return runtime.WorkerResult{}, fmt.Errorf("%w: %v", errClaimRenewalLost, renewErr)
	}
	return result, err
}

func (st *ticketRunState) runReviewerWithClaimRenewal(ctx context.Context, req runtime.ReviewRequest) (runtime.ReviewResult, error) {
	renewCtx, stopRenewal := st.startClaimRenewal(ctx)
	result, err := st.req.Adapter.RunReviewer(renewCtx, req)
	renewErr := stopRenewal()
	if renewErr != nil {
		return runtime.ReviewResult{}, fmt.Errorf("%w: %v", errClaimRenewalLost, renewErr)
	}
	return result, err
}

func (st *ticketRunState) startClaimRenewal(ctx context.Context) (context.Context, func() error) {
	ttl := st.claimTTL()
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	remaining := st.remainingTTL()
	if remaining <= 0 {
		remaining = 30 * time.Minute
	}
	interval := remaining / 3
	if interval < 25*time.Millisecond {
		interval = 25 * time.Millisecond
	}

	renewCtx, cancel := context.WithCancel(ctx)
	errCh := make(chan error, 1)
	done := make(chan struct{})

	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-renewCtx.Done():
				return
			case <-ticker.C:
				if _, err := tkmd.RenewClaim(st.repoRoot, st.req.RunID, st.req.Ticket.ID, st.req.Claim.LeaseID, ttl, time.Now().UTC()); err != nil {
					select {
					case errCh <- err:
					default:
					}
					cancel()
					return
				}
			}
		}
	}()

	stop := func() error {
		cancel()
		<-done
		select {
		case err := <-errCh:
			return err
		default:
			return nil
		}
	}
	return renewCtx, stop
}

func (st *ticketRunState) claimTTL() time.Duration {
	if st.req.Claim.LeasedAt.IsZero() || st.req.Claim.ExpiresAt.IsZero() {
		return 30 * time.Minute
	}
	ttl := st.req.Claim.ExpiresAt.Sub(st.req.Claim.LeasedAt)
	if ttl <= 0 {
		return 30 * time.Minute
	}
	return ttl
}

// remainingTTL computes the time remaining until the claim expires,
// used for scheduling renewal intervals. Unlike claimTTL which returns
// the original full TTL, this reflects the actual remaining time.
func (st *ticketRunState) remainingTTL() time.Duration {
	if st.req.Claim.ExpiresAt.IsZero() {
		return 0
	}
	remaining := st.req.Claim.ExpiresAt.Sub(time.Now().UTC())
	if remaining <= 0 {
		return 0
	}
	return remaining
}

func shouldRetryRuntimeError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, errRuntimeExecutionBlocked) || errors.Is(err, errClaimRenewalLost) {
		return false
	}
	if strings.Contains(strings.ToLower(err.Error()), "invalid") {
		return false
	}
	return true
}

func (st *ticketRunState) blockRuntimeExecution(reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "runtime execution blocked"
	}
	st.blockReason = reason
	if err := st.transitionTo(state.TicketPhaseBlocked); err != nil {
		return err
	}
	return st.persist()
}

func (st *ticketRunState) transitionTo(next state.TicketPhase) error {
	if st.currentPhase == next {
		return nil
	}
	if err := state.ValidateTicketTransition(st.currentPhase, next); err != nil {
		return err
	}
	st.currentPhase = next
	st.emitProgress(next)
	return nil
}

func (st *ticketRunState) emitProgress(phase state.TicketPhase) {
	detail := ""
	switch phase {
	case state.TicketPhaseImplement:
		detail = fmt.Sprintf("attempt %d", st.implementationAttempts+1)
	case state.TicketPhaseRepair:
		detail = fmt.Sprintf("cycle %d", len(st.repairCycles))
	case state.TicketPhaseBlocked:
		detail = st.blockReason
		if len(detail) > 60 {
			detail = detail[:57] + "..."
		}
	}
	SendProgress(st.ctx, st.req.Progress, ProgressEvent{
		Type:     EventTicketPhaseChanged,
		TicketID: st.req.Ticket.ID,
		Title:    st.req.Plan.Title,
		Phase:    phase,
		Detail:   detail,
	})
}

func (st *ticketRunState) progressDetail(detail string) {
	SendProgress(st.ctx, st.req.Progress, ProgressEvent{
		Type:     EventTicketDetail,
		TicketID: st.req.Ticket.ID,
		Title:    st.req.Plan.Title,
		Detail:   detail,
	})
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
	if st.createdAt.IsZero() {
		st.createdAt = stateTime()
	}
	snapshot := TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{
			SchemaVersion: artifactSchemaVersion,
			RunID:         st.req.RunID,
			CreatedAt:     st.createdAt,
			UpdatedAt:     stateTime(),
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

func buildTicketRunPaths(repoRoot, runID, ticketID string) ticketRunPaths {
	runDir := filepath.Join(repoRoot, ".verk", "runs", runID, "tickets", ticketID)
	return ticketRunPaths{
		runDir:             runDir,
		snapshotPath:       filepath.Join(runDir, "ticket-run.json"),
		implementationPath: filepath.Join(runDir, "implementation.json"),
		verificationPath:   filepath.Join(runDir, "verification.json"),
		reviewPath:         filepath.Join(runDir, "review-findings.json"),
		closeoutPath:       filepath.Join(runDir, "closeout.json"),
	}
}

func (p ticketRunPaths) repairCyclePath(cycle int) string {
	return filepath.Join(p.runDir, "cycles", fmt.Sprintf("repair-%d.json", cycle))
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
	if cfg.Policy.MaxRepairCycles <= 0 {
		cfg.Policy.MaxRepairCycles = defaults.Policy.MaxRepairCycles
	}
	if cfg.Verification.DefaultTimeoutMinutes <= 0 {
		cfg.Verification.DefaultTimeoutMinutes = defaults.Verification.DefaultTimeoutMinutes
	}
	if cfg.Runtime.DefaultRuntime == "" {
		cfg.Runtime.DefaultRuntime = defaults.Runtime.DefaultRuntime
	}
	// Fill in the role profiles from defaults when an embedded caller passes a
	// bare Config that skipped LoadConfig/Validate. This keeps worker/reviewer
	// selection deterministic (claude/sonnet/high and claude/opus/xhigh) even
	// in tests that construct Config manually without role profiles.
	if cfg.Runtime.Worker.IsZero() {
		cfg.Runtime.Worker = defaults.Runtime.Worker
	}
	if cfg.Runtime.Reviewer.IsZero() {
		cfg.Runtime.Reviewer = defaults.Runtime.Reviewer
	}
	if len(cfg.Verification.EnvPassthrough) == 0 {
		cfg.Verification.EnvPassthrough = append([]string(nil), defaults.Verification.EnvPassthrough...)
	}
	return cfg
}

func executionConfigFromPolicy(cfg policy.Config) runtime.ExecutionConfig {
	return runtime.ExecutionConfig{
		WorkerTimeoutMinutes:   cfg.Runtime.WorkerTimeoutMinutes,
		ReviewerTimeoutMinutes: cfg.Runtime.ReviewerTimeoutMinutes,
		AuthEnvVars:            append([]string(nil), cfg.Runtime.AuthEnvVars...),
	}
}

// chosenRuntime resolves the runtime identifier used for a ticket's
// execution. Role-specific profiles (worker/reviewer) now own model and
// reasoning selection; the ticket-level RuntimePreference is retained as an
// explicit routing hint only, taking precedence over the worker profile's
// runtime. This is the ONLY ticket-frontmatter field permitted to influence
// runtime selection. Model selection is intentionally policy-owned and must
// not be derived from ticket frontmatter (see ver-laq2).
func chosenRuntime(plan state.PlanArtifact, cfg policy.Config) string {
	if strings.TrimSpace(plan.RuntimePreference) != "" {
		return strings.TrimSpace(plan.RuntimePreference)
	}
	if rt := strings.TrimSpace(cfg.Runtime.Worker.Runtime); rt != "" {
		return rt
	}
	return strings.TrimSpace(cfg.Runtime.DefaultRuntime)
}

// workerProfileForPlan resolves the effective worker role profile for a
// ticket run. The plan's RuntimePreference (from ticket frontmatter) can
// override the runtime identifier, but model and reasoning come strictly
// from config. Ticket frontmatter `model` is intentionally ignored.
func workerProfileForPlan(plan state.PlanArtifact, cfg policy.Config) policy.RoleProfile {
	profile := cfg.EffectiveWorkerProfile()
	if pref := strings.TrimSpace(plan.RuntimePreference); pref != "" {
		profile.Runtime = pref
	}
	if strings.TrimSpace(profile.Runtime) == "" {
		profile.Runtime = strings.TrimSpace(cfg.Runtime.DefaultRuntime)
	}
	return profile
}

// reviewerProfileForPlan resolves the effective reviewer role profile.
// The ticket's RuntimePreference is an explicit routing hint for the
// runtime only; model and reasoning come from config.
func reviewerProfileForPlan(plan state.PlanArtifact, cfg policy.Config) policy.RoleProfile {
	profile := cfg.EffectiveReviewerProfile()
	if pref := strings.TrimSpace(plan.RuntimePreference); pref != "" {
		profile.Runtime = pref
	}
	if strings.TrimSpace(profile.Runtime) == "" {
		profile.Runtime = strings.TrimSpace(cfg.Runtime.DefaultRuntime)
	}
	return profile
}

func renderImplementInstructions(plan state.PlanArtifact, phase state.TicketPhase, attempt int) string {
	var b strings.Builder

	fmt.Fprintf(&b, "**Ticket ID:** %s\n", plan.TicketID)
	if plan.Title != "" {
		fmt.Fprintf(&b, "**Title:** %s\n", plan.Title)
	}
	fmt.Fprintf(&b, "**Phase:** %s\n", phase)
	fmt.Fprintf(&b, "**Attempt:** %d\n\n", attempt)

	if plan.Description != "" {
		b.WriteString("### Description\n\n")
		b.WriteString(plan.Description)
		b.WriteString("\n\n")
	}

	if len(plan.AcceptanceCriteria) > 0 {
		b.WriteString("### Acceptance Criteria\n\n")
		for i, criterion := range plan.AcceptanceCriteria {
			fmt.Fprintf(&b, "%d. %s\n", i+1, criterion)
		}
		b.WriteString("\n")
	}

	if len(plan.OwnedPaths) > 0 {
		b.WriteString("### Scope (owned paths)\n\n")
		b.WriteString("You may ONLY modify files within these paths:\n\n")
		for _, p := range plan.OwnedPaths {
			fmt.Fprintf(&b, "- `%s`\n", p)
		}
		b.WriteString("\n")
	}

	if len(plan.TestCases) > 0 {
		b.WriteString("### Test Cases\n\n")
		for _, tc := range plan.TestCases {
			fmt.Fprintf(&b, "- %s\n", tc)
		}
		b.WriteString("\n")
	}

	if len(plan.ValidationCommands) > 0 {
		b.WriteString("### Verification Commands\n\n")
		b.WriteString("These commands will be run to verify your implementation:\n\n")
		for _, cmd := range plan.ValidationCommands {
			fmt.Fprintf(&b, "```\n%s\n```\n", cmd)
		}
		b.WriteString("\n")
	}

	if attempt > 1 {
		b.WriteString("### Note\n\n")
		b.WriteString("This is a retry. The previous attempt failed verification. ")
		b.WriteString("Review the prior artifact for failure details and fix the issues.\n")
	}

	return b.String()
}

// buildImplementPhaseInstructions constructs instructions for a worker in the
// implement phase. On the first attempt this is identical to
// renderImplementInstructions. On subsequent attempts triggered by verification
// failures, it enriches the instructions with the prior verification summary,
// changed files, and the specific failing check details from the most recent
// verification repair cycle so the worker can focus its repair effort.
func buildImplementPhaseInstructions(st *ticketRunState, attempt int) string {
	base := renderImplementInstructions(st.req.Plan, st.currentPhase, attempt)
	if attempt <= 1 {
		return base
	}
	verifyCycles := filterVerificationRepairCycles(st.repairCycles)
	if len(verifyCycles) == 0 {
		return base
	}

	var b strings.Builder
	b.WriteString(base)

	// Changed files focus the worker on which files triggered the derived checks.
	if st.implementation != nil && len(st.implementation.ChangedFiles) > 0 {
		b.WriteString("\n### Changed Files\n\n")
		for _, f := range st.implementation.ChangedFiles {
			fmt.Fprintf(&b, "- `%s`\n", f)
		}
		b.WriteString("\n")
	}

	// Prior verification shows which commands failed.
	if summary := priorVerificationSummary(st.verification); summary != "" {
		b.WriteString("### Prior Verification\n\n")
		b.WriteString(summary)
		b.WriteString("\n")
	}

	// Failing check details from the most recent verification repair cycle.
	last := verifyCycles[len(verifyCycles)-1]
	if len(last.TriggerCheckIDs) > 0 {
		b.WriteString("### Checks to Fix\n\n")
		b.WriteString("These checks triggered the retry and must pass:\n\n")
		for _, id := range last.TriggerCheckIDs {
			cmd := lookupCheckCommand(st.verification, id)
			if cmd == "" {
				fmt.Fprintf(&b, "- check `%s` failed\n", id)
				continue
			}
			matched := lookupCheckMatchedFiles(st.verification, id)
			if len(matched) > 0 {
				fmt.Fprintf(&b, "- `%s` (covering: %s)\n", cmd, strings.Join(matched, ", "))
			} else {
				fmt.Fprintf(&b, "- `%s`\n", cmd)
			}
		}
		b.WriteString("\n")
	}

	return b.String()
}

// filterVerificationRepairCycles returns repair cycles that were triggered by
// failing verification checks (TriggerCheckIDs set). These are distinct from
// review-triggered repair cycles (TriggerFindingIDs set).
func filterVerificationRepairCycles(cycles []state.RepairCycleArtifact) []state.RepairCycleArtifact {
	out := make([]state.RepairCycleArtifact, 0, len(cycles))
	for _, c := range cycles {
		if len(c.TriggerCheckIDs) > 0 {
			out = append(out, c)
		}
	}
	return out
}

// lookupCheckCommand returns the command for a check in the verification
// coverage's declared or derived checks. Returns "" when the check cannot be
// found or the coverage is absent.
func lookupCheckCommand(verification *state.VerificationArtifact, checkID string) string {
	if verification == nil || verification.ValidationCoverage == nil {
		return ""
	}
	check, ok := verification.ValidationCoverage.CheckByID(checkID)
	if !ok {
		return ""
	}
	return check.Command
}

// lookupCheckMatchedFiles returns the matched files for a check in the
// verification coverage. Returns nil when the check is not found or has no
// matched files recorded.
func lookupCheckMatchedFiles(verification *state.VerificationArtifact, checkID string) []string {
	if verification == nil || verification.ValidationCoverage == nil {
		return nil
	}
	check, ok := verification.ValidationCoverage.CheckByID(checkID)
	if !ok {
		return nil
	}
	return check.MatchedFiles
}

func renderRepairInstructions(st *ticketRunState) string {
	var b strings.Builder

	fmt.Fprintf(&b, "**Ticket ID:** %s\n", st.req.Plan.TicketID)
	if st.req.Plan.Title != "" {
		fmt.Fprintf(&b, "**Title:** %s\n", st.req.Plan.Title)
	}
	fmt.Fprintf(&b, "**Phase:** repair\n")

	cycleNum := len(st.repairCycles)
	if cycleNum > 0 {
		last := st.repairCycles[cycleNum-1]
		fmt.Fprintf(&b, "**Repair Cycle:** %d\n\n", last.Cycle)

		if len(last.TriggerFindingIDs) > 0 {
			b.WriteString("### Findings to Address\n\n")
			b.WriteString("The following review findings triggered this repair cycle:\n\n")
			for _, id := range last.TriggerFindingIDs {
				fmt.Fprintf(&b, "- `%s`\n", id)
			}
			b.WriteString("\n")
		}
	} else {
		b.WriteString("**Repair Cycle:** 1\n\n")
	}

	// Ticket description keeps the repair worker anchored to the original
	// problem so it doesn't over-rotate on the single finding.
	if strings.TrimSpace(st.req.Plan.Description) != "" {
		b.WriteString("### Original Ticket Description\n\n")
		b.WriteString(st.req.Plan.Description)
		b.WriteString("\n\n")
	}

	// Surface per-finding detail so the repair worker can act on exactly
	// the issues the reviewer flagged without re-reading the review
	// artifact. Open findings are listed first; waived/resolved findings
	// are omitted — their state already says no repair is required.
	if st.review != nil && len(st.review.Findings) > 0 {
		openFindings := make([]state.ReviewFinding, 0, len(st.review.Findings))
		for _, f := range st.review.Findings {
			if f.Disposition == "open" {
				openFindings = append(openFindings, f)
			}
		}
		if len(openFindings) > 0 {
			b.WriteString("### Review Findings Detail\n\n")
			for _, finding := range openFindings {
				location := finding.File
				if finding.Line > 0 {
					location = fmt.Sprintf("%s:%d", finding.File, finding.Line)
				}
				fmt.Fprintf(&b, "- **[%s] %s** (id=`%s`", finding.Severity, finding.Title, finding.ID)
				if location != "" {
					fmt.Fprintf(&b, ", %s", location)
				}
				fmt.Fprintf(&b, "): %s\n", finding.Body)
			}
			b.WriteString("\n")
		}
	}

	if len(st.req.Plan.AcceptanceCriteria) > 0 {
		b.WriteString("### Acceptance Criteria\n\n")
		for i, criterion := range st.req.Plan.AcceptanceCriteria {
			fmt.Fprintf(&b, "%d. %s\n", i+1, criterion)
		}
		b.WriteString("\n")
	}

	if len(st.req.Plan.TestCases) > 0 {
		b.WriteString("### Test Cases\n\n")
		for _, tc := range st.req.Plan.TestCases {
			fmt.Fprintf(&b, "- %s\n", tc)
		}
		b.WriteString("\n")
	}

	if len(st.req.Plan.OwnedPaths) > 0 {
		b.WriteString("### Scope (owned paths)\n\n")
		for _, p := range st.req.Plan.OwnedPaths {
			fmt.Fprintf(&b, "- `%s`\n", p)
		}
		b.WriteString("\n")
	}

	// Changed files + prior verification results keep the worker from
	// rebuilding context by re-running the whole ticket blindly.
	if st.implementation != nil && len(st.implementation.ChangedFiles) > 0 {
		b.WriteString("### Changed Files So Far\n\n")
		for _, file := range st.implementation.ChangedFiles {
			fmt.Fprintf(&b, "- `%s`\n", file)
		}
		b.WriteString("\n")
	}

	if summary := priorVerificationSummary(st.verification); summary != "" {
		b.WriteString("### Prior Verification\n\n")
		b.WriteString(summary)
		b.WriteString("\n")
	}

	b.WriteString("Fix the review findings while maintaining all acceptance criteria. ")
	b.WriteString("Do not regress the changes already on disk; extend them.\n")

	return b.String()
}

// priorVerificationSummary renders a compact, human-readable summary of
// the most recent verification run for use in repair prompts. Returns an
// empty string when there is no verification artifact yet so callers can
// avoid emitting an empty section.
func priorVerificationSummary(verification *state.VerificationArtifact) string {
	if verification == nil {
		return ""
	}
	var b strings.Builder
	if verification.Passed {
		fmt.Fprintf(&b, "Previous verification (attempt %d) passed on %d command(s).\n", verification.Attempt, len(verification.Results))
		return b.String()
	}
	fmt.Fprintf(&b, "Previous verification (attempt %d) did not pass.\n", verification.Attempt)
	failing := make([]string, 0, len(verification.Results))
	for _, r := range verification.Results {
		if !r.Passed {
			failing = append(failing, r.Command)
		}
	}
	if len(failing) > 0 {
		b.WriteString("Failing commands:\n")
		for _, cmd := range failing {
			fmt.Fprintf(&b, "- `%s`\n", cmd)
		}
	}
	return b.String()
}

func renderReviewInstructions(plan state.PlanArtifact, attempt int) string {
	var b strings.Builder

	fmt.Fprintf(&b, "**Ticket ID:** %s\n", plan.TicketID)
	if plan.Title != "" {
		fmt.Fprintf(&b, "**Title:** %s\n", plan.Title)
	}
	fmt.Fprintf(&b, "**Review attempt:** %d\n", attempt)
	fmt.Fprintf(&b, "**Review threshold:** %s\n\n", plan.EffectiveReviewThreshold)

	if plan.Description != "" {
		b.WriteString("### Original Ticket Description\n\n")
		b.WriteString(plan.Description)
		b.WriteString("\n\n")
	}

	if len(plan.AcceptanceCriteria) > 0 {
		b.WriteString("### Acceptance Criteria to Verify\n\n")
		for i, criterion := range plan.AcceptanceCriteria {
			fmt.Fprintf(&b, "%d. %s\n", i+1, criterion)
		}
		b.WriteString("\n")
	}

	if len(plan.OwnedPaths) > 0 {
		b.WriteString("### Expected Scope\n\n")
		b.WriteString("Changes should be limited to these paths:\n\n")
		for _, p := range plan.OwnedPaths {
			fmt.Fprintf(&b, "- `%s`\n", p)
		}
		b.WriteString("\n")
	}

	if len(plan.TestCases) > 0 {
		b.WriteString("### Test Cases\n\n")
		for _, tc := range plan.TestCases {
			fmt.Fprintf(&b, "- %s\n", tc)
		}
		b.WriteString("\n")
	}

	b.WriteString("Review the diff below against the ticket description and acceptance criteria. ")
	b.WriteString("Flag any issues with appropriate severity.\n\n")

	b.WriteString("Treat this as a dry run for a brutally honest external review: find real gaps, incomplete implementations, ")
	b.WriteString("and missing tests rather than manufacturing nits. For each finding, document the severity and the owning ticket (use ")
	fmt.Fprintf(&b, "`%s` when the issue is scoped to this ticket), the affected file or behavior and the concrete risk, the specific ", plan.TicketID)
	b.WriteString("missing validation or test evidence, and whether you believe the issue can be auto-repaired.\n")

	return b.String()
}

func workerBlockReason(result runtime.WorkerResult) string {
	if strings.TrimSpace(result.BlockReason) != "" {
		return strings.TrimSpace(result.BlockReason)
	}
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

func cloneRuntimeTokenUsage(usage *state.RuntimeTokenUsage) *state.RuntimeTokenUsage {
	if usage == nil {
		return nil
	}
	copy := *usage
	return &copy
}

func cloneRuntimeActivityStats(stats *state.RuntimeActivityStats) *state.RuntimeActivityStats {
	if stats == nil {
		return nil
	}
	copy := *stats
	return &copy
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
			Cwd:        result.Cwd,
			ExitCode:   result.ExitCode,
			TimedOut:   result.TimedOut,
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
			Severity:        finding.Severity,
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
