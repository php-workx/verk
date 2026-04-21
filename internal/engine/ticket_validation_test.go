// Package engine — regression tests for ticket-scoped validation helpers.
//
// These tests cover the glue layer in ticket_validation.go that wires
// derived checks, validation coverage, and repair routing into the
// ticket lifecycle. They are intentionally scoped to the pure helper
// functions so they run without needing a runtime adapter or a real git
// repository — the RunTicket end-to-end tests live in ticket_run_test.go.
package engine

import (
	"context"
	"strings"
	"testing"
	"verk/internal/adapters/ticketstore/tkmd"
	verifycommand "verk/internal/adapters/verify/command"
	"verk/internal/policy"
	"verk/internal/state"
)

func TestDeriveTicketChecks_UsesConfiguredStaleWordingTermsAndDocs(t *testing.T) {
	originalToolSignals := toolSignalsProvider
	originalStaleTerms := staleWordingTermsProvider
	originalRepoRoot := t.TempDir()
	t.Cleanup(func() {
		toolSignalsProvider = originalToolSignals
		staleWordingTermsProvider = originalStaleTerms
	})
	toolSignalsProvider = func(string) ToolSignals {
		return ToolSignals{HasMarkdownlint: true}
	}
	staleWordingTermsProvider = func() []string { return nil }

	cfg := policy.DefaultConfig()
	cfg.Verification.EpicStaleWordingTerms = []string{"old-scanner", "betterleaks"}
	cfg.Verification.EpicClosureDocs = []string{"POLICY_SCOPE.md"}
	st := &ticketRunState{
		cfg: cfg,
		req: RunTicketRequest{
			Plan: state.PlanArtifact{
				TicketID:    "ver-y29o",
				Title:       "docs refresh",
				Description: "refresh docs and wording",
				OwnedPaths:  []string{"docs"},
			},
		},
		repoRoot:       originalRepoRoot,
		implementation: &state.ImplementationArtifact{ChangedFiles: []string{"docs/self-hosting.md"}},
	}

	result := deriveTicketChecks(st)
	stale, ok := findCheck(result.Checks, "grep -nE")
	if !ok {
		t.Fatalf("expected stale wording check derived from policy config, got %#v", result.Checks)
	}
	if !strings.Contains(stale.Command, "POLICY_SCOPE.md") {
		t.Fatalf("expected policy-scoped docs path in stale-wording command, got %q", stale.Command)
	}
	if strings.Contains(stale.Command, "README.md") {
		t.Fatalf("expected default docs fallback not to be used when policy docs are configured, got %q", stale.Command)
	}
	if _, ok := findCheck(result.Checks, "markdownlint docs/self-hosting.md"); !ok {
		t.Fatalf("expected markdownlint check to be derived with configured terms available, got %#v", result.Checks)
	}
}

// TestAssembleTicketValidationCoverage_FailingRequiredDerivedCheckBlocks
// verifies that a non-advisory (required) derived check that fails is
// recorded in ExecutedChecks and surfaces as an UnresolvedBlocker with the
// coverage marked non-closable (AC4 for ticket ver-1qru: closeout artifacts
// show exactly which checks passed, failed, were repaired, or need user
// input).
func TestAssembleTicketValidationCoverage_FailingRequiredDerivedCheckBlocks(t *testing.T) {
	plan := state.PlanArtifact{
		ArtifactMeta:             state.ArtifactMeta{RunID: "run-a"},
		TicketID:                 "ver-derived-fail",
		ValidationCommands:       []string{"go test ./..."},
		EffectiveReviewThreshold: state.SeverityP2,
	}
	derived := DeriveChecksResult{
		Checks: []state.ValidationCheck{{
			ID:       "derived-ruff",
			Scope:    state.ValidationScopeTicket,
			Source:   state.ValidationCheckSourceDerived,
			Command:  "ruff check .",
			Reason:   "ruff check promoted to required",
			Advisory: false,
			TicketID: plan.TicketID,
		}},
	}
	derivedResults := map[string]verifycommand.CommandResult{
		"derived-ruff": {Command: "ruff check .", ExitCode: 1},
	}

	coverage := assembleTicketValidationCoverage(
		plan, "run-a", 1,
		nil, // no declared results
		derived, derivedResults,
		nil, nil, nil,
	)

	if len(coverage.DerivedChecks) != 1 {
		t.Fatalf("expected 1 derived check, got %d", len(coverage.DerivedChecks))
	}
	if len(coverage.ExecutedChecks) != 1 {
		t.Fatalf("expected 1 executed check, got %d", len(coverage.ExecutedChecks))
	}
	if coverage.ExecutedChecks[0].Result != state.ValidationCheckResultFailed {
		t.Fatalf("expected failed execution result, got %q", coverage.ExecutedChecks[0].Result)
	}
	if coverage.Closable {
		t.Fatalf("expected coverage to be non-closable when a required derived check fails")
	}
	if len(coverage.UnresolvedBlockers) != 1 {
		t.Fatalf("expected 1 unresolved blocker, got %d", len(coverage.UnresolvedBlockers))
	}
	if coverage.UnresolvedBlockers[0].CheckID != "derived-ruff" {
		t.Fatalf("expected blocker to reference derived-ruff, got %q", coverage.UnresolvedBlockers[0].CheckID)
	}
	if coverage.BlockReason == "" {
		t.Fatalf("expected a block reason on non-closable coverage")
	}
}

// TestAssembleTicketValidationCoverage_FailingAdvisoryDerivedCheckStaysClosable
// verifies that advisory derived checks record their failures in coverage
// but do not by themselves prevent closure — a key invariant of AC6
// (existing ticket run behavior remains compatible for tickets with no
// derived checks; advisory derived checks must not regress tickets that
// otherwise pass declared checks).
func TestAssembleTicketValidationCoverage_FailingAdvisoryDerivedCheckStaysClosable(t *testing.T) {
	plan := state.PlanArtifact{
		ArtifactMeta:             state.ArtifactMeta{RunID: "run-a"},
		TicketID:                 "ver-advisory",
		EffectiveReviewThreshold: state.SeverityP2,
	}
	derived := DeriveChecksResult{
		Checks: []state.ValidationCheck{{
			ID:       "derived-shellcheck",
			Scope:    state.ValidationScopeTicket,
			Source:   state.ValidationCheckSourceDerived,
			Command:  "shellcheck foo.sh",
			Advisory: true,
			TicketID: plan.TicketID,
		}},
	}
	derivedResults := map[string]verifycommand.CommandResult{
		"derived-shellcheck": {Command: "shellcheck foo.sh", ExitCode: 1},
	}

	coverage := assembleTicketValidationCoverage(
		plan, "run-a", 1,
		nil, derived, derivedResults, nil, nil, nil,
	)

	if !coverage.Closable {
		t.Fatalf("expected advisory derived check failure to remain closable, got block reason %q", coverage.BlockReason)
	}
	if len(coverage.UnresolvedBlockers) != 0 {
		t.Fatalf("expected no unresolved blockers for advisory check, got %#v", coverage.UnresolvedBlockers)
	}
}

// TestAssembleTicketValidationCoverage_RepairLimitPropagates verifies that a
// ValidationRepairLimit passed into the assembler is reflected on the
// returned coverage and flips it to non-closable. This is the assembly-side
// of AC7 (repair attempts stop at the configured policy limit and persist
// the limiting reason in artifacts).
func TestAssembleTicketValidationCoverage_RepairLimitPropagates(t *testing.T) {
	plan := state.PlanArtifact{
		ArtifactMeta:             state.ArtifactMeta{RunID: "run-a"},
		TicketID:                 "ver-exhausted",
		EffectiveReviewThreshold: state.SeverityP2,
	}
	limit := &state.ValidationRepairLimit{
		Name:      "max_implementation_attempts",
		Limit:     3,
		Reached:   3,
		Reason:    "non_convergent_verification: failed after 3 attempt(s)",
		PolicyRef: "policy.max_implementation_attempts",
	}

	coverage := assembleTicketValidationCoverage(
		plan, "run-a", 3,
		nil, DeriveChecksResult{}, nil, nil, nil, limit,
	)

	if coverage.Closable {
		t.Fatalf("expected coverage with repair limit to be non-closable")
	}
	if coverage.RepairLimit == nil {
		t.Fatalf("expected repair limit to be persisted on coverage")
	}
	if coverage.RepairLimit.Name != "max_implementation_attempts" {
		t.Fatalf("expected repair limit name to round-trip, got %q", coverage.RepairLimit.Name)
	}
	if coverage.BlockReason == "" {
		t.Fatalf("expected block reason derived from repair limit, got empty")
	}
}

// TestRequiredDerivedChecksPassed ensures that advisory derived checks do
// not flip the verify loop's Passed flag, while non-advisory (required)
// derived checks do when they fail or time out. This guards the invariant
// behind AC2 (failing required derived checks route into the repair loop)
// without silently coupling advisory checks to the gating path.
func TestRequiredDerivedChecksPassed(t *testing.T) {
	checks := []state.ValidationCheck{
		{ID: "advisory", Advisory: true},
		{ID: "required", Advisory: false},
	}
	t.Run("advisory_failure_passes", func(t *testing.T) {
		results := map[string]verifycommand.CommandResult{
			"advisory": {Command: "advisory", ExitCode: 1},
			"required": {Command: "required", ExitCode: 0},
		}
		if !requiredDerivedChecksPassed(checks, results) {
			t.Fatalf("expected advisory failure alone not to flip required-passed")
		}
	})
	t.Run("required_failure_blocks", func(t *testing.T) {
		results := map[string]verifycommand.CommandResult{
			"advisory": {Command: "advisory", ExitCode: 0},
			"required": {Command: "required", ExitCode: 1},
		}
		if requiredDerivedChecksPassed(checks, results) {
			t.Fatalf("expected required check failure to flip required-passed")
		}
	})
	t.Run("required_timeout_blocks", func(t *testing.T) {
		results := map[string]verifycommand.CommandResult{
			"required": {Command: "required", TimedOut: true},
		}
		if requiredDerivedChecksPassed(checks, results) {
			t.Fatalf("expected timeout on required check to flip required-passed")
		}
	})
	t.Run("missing_result_does_not_block", func(t *testing.T) {
		// A required check with no recorded result (e.g. skipped due to a
		// missing optional tool) should not flip required-passed. Skipped
		// checks surface in SkippedChecks instead of ExecutedChecks and
		// must not silently block closure (AC6).
		results := map[string]verifycommand.CommandResult{}
		if !requiredDerivedChecksPassed(checks, results) {
			t.Fatalf("expected missing result on required check not to flip required-passed")
		}
	})
}

// TestFailingCheckIDs_ReturnsLatestFailedExecutionsOnly verifies that a
// check which originally failed and was later repaired is NOT listed as
// failing — only executions whose latest result is failed surface. This is
// the helper behind AC3 (only unresolved failures after repair become
// blockers).
func TestFailingCheckIDs_ReturnsLatestFailedExecutionsOnly(t *testing.T) {
	coverage := state.ValidationCoverageArtifact{
		DerivedChecks: []state.ValidationCheck{
			{ID: "derived-a"},
			{ID: "derived-b"},
		},
		ExecutedChecks: []state.ValidationCheckExecution{
			{CheckID: "derived-a", Result: state.ValidationCheckResultFailed, Attempt: 1},
			{CheckID: "derived-a", Result: state.ValidationCheckResultPassed, Attempt: 2},
			{CheckID: "derived-b", Result: state.ValidationCheckResultFailed, Attempt: 1},
		},
	}

	ids := failingCheckIDs(coverage)
	if len(ids) != 1 || ids[0] != "derived-b" {
		t.Fatalf("expected only derived-b in failing ids, got %#v", ids)
	}
}

// TestVerificationFailingCheckIDs_NilCoverageIsSafe verifies that the
// failing-ids helper tolerates missing coverage (legacy artifact without
// ValidationCoverage). This protects AC6 (existing ticket run behavior
// remains compatible for tickets without derived checks).
func TestVerificationFailingCheckIDs_NilCoverageIsSafe(t *testing.T) {
	if ids := verificationFailingCheckIDs(nil); ids != nil {
		t.Fatalf("expected nil for nil verification, got %#v", ids)
	}
	legacy := &state.VerificationArtifact{Passed: false}
	if ids := verificationFailingCheckIDs(legacy); ids != nil {
		t.Fatalf("expected nil for legacy verification without coverage, got %#v", ids)
	}
}

// TestHandleVerificationFailure_AppendsRepairCycleWithTriggerCheckIDs is
// the main regression test for AC2 and AC3: a failing verification run
// (with failing validation coverage check ids) while repair budget remains
// must enqueue a repair cycle whose TriggerCheckIDs reference the failing
// checks and transition the ticket back to the implement phase.
func TestHandleVerificationFailure_AppendsRepairCycleWithTriggerCheckIDs(t *testing.T) {
	cfg := policy.DefaultConfig()
	cfg.Policy.MaxImplementationAttempts = 3

	st := newVerificationTestState(t, "run-repair", "ver-repair", cfg, 1)

	verification := state.VerificationArtifact{
		ArtifactMeta: state.ArtifactMeta{RunID: "run-repair"},
		TicketID:     "ver-repair",
		Attempt:      1,
		Passed:       false,
		ValidationCoverage: &state.ValidationCoverageArtifact{
			ArtifactMeta: state.ArtifactMeta{RunID: "run-repair"},
			Scope:        state.ValidationScopeTicket,
			TicketID:     "ver-repair",
			DerivedChecks: []state.ValidationCheck{{
				ID:      "derived-ruff",
				Source:  state.ValidationCheckSourceDerived,
				Command: "ruff check .",
			}},
			ExecutedChecks: []state.ValidationCheckExecution{{
				CheckID: "derived-ruff",
				Result:  state.ValidationCheckResultFailed,
			}},
		},
	}

	if err := handleVerificationFailure(st, verification); err != nil {
		t.Fatalf("handleVerificationFailure: %v", err)
	}

	if st.currentPhase != state.TicketPhaseImplement {
		t.Fatalf("expected implement phase after verify failure with budget remaining, got %q", st.currentPhase)
	}
	if len(st.repairCycles) != 1 {
		t.Fatalf("expected one repair cycle, got %d", len(st.repairCycles))
	}
	cycle := st.repairCycles[0]
	if len(cycle.TriggerCheckIDs) != 1 || cycle.TriggerCheckIDs[0] != "derived-ruff" {
		t.Fatalf("expected cycle to reference derived-ruff check, got %#v", cycle.TriggerCheckIDs)
	}
	if cycle.Scope != state.ValidationScopeTicket {
		t.Fatalf("expected ticket-scoped repair cycle, got %q", cycle.Scope)
	}
	if cycle.Status != "repair_pending" {
		t.Fatalf("expected repair_pending status, got %q", cycle.Status)
	}
	if !strings.Contains(cycle.RepairNotes, "derived-ruff") {
		t.Fatalf("expected repair notes to cite failing check id, got %q", cycle.RepairNotes)
	}
}

// TestHandleVerificationFailure_BudgetExhaustedBlocksWithRepairLimit
// verifies AC5 and AC7: when the implementation-attempts budget is
// exhausted, the ticket transitions to blocked, the block reason cites the
// failing check ids, and the verification artifact's coverage records a
// ValidationRepairLimit that identifies the policy limit which stopped
// further repair work.
func TestHandleVerificationFailure_BudgetExhaustedBlocksWithRepairLimit(t *testing.T) {
	cfg := policy.DefaultConfig()
	cfg.Policy.MaxImplementationAttempts = 2

	st := newVerificationTestState(t, "run-exhaust", "ver-exhaust", cfg, 2)

	verification := state.VerificationArtifact{
		ArtifactMeta: state.ArtifactMeta{RunID: "run-exhaust"},
		TicketID:     "ver-exhaust",
		Attempt:      2,
		Passed:       false,
		ValidationCoverage: &state.ValidationCoverageArtifact{
			ArtifactMeta: state.ArtifactMeta{RunID: "run-exhaust"},
			Scope:        state.ValidationScopeTicket,
			TicketID:     "ver-exhaust",
			DerivedChecks: []state.ValidationCheck{{
				ID:      "derived-ruff",
				Source:  state.ValidationCheckSourceDerived,
				Command: "ruff check .",
			}},
			ExecutedChecks: []state.ValidationCheckExecution{{
				CheckID: "derived-ruff",
				Result:  state.ValidationCheckResultFailed,
			}},
		},
	}

	if err := handleVerificationFailure(st, verification); err != nil {
		t.Fatalf("handleVerificationFailure: %v", err)
	}

	if st.currentPhase != state.TicketPhaseBlocked {
		t.Fatalf("expected blocked phase after budget exhausted, got %q", st.currentPhase)
	}
	if !strings.Contains(st.blockReason, string(state.EscalationNonConvergentVerification)) {
		t.Fatalf("expected non-convergent verification block reason, got %q", st.blockReason)
	}
	if !strings.Contains(st.blockReason, "derived-ruff") {
		t.Fatalf("expected block reason to cite failing check id, got %q", st.blockReason)
	}
	if st.verification == nil || st.verification.ValidationCoverage == nil {
		t.Fatalf("expected verification and coverage to be set")
	}
	limit := st.verification.ValidationCoverage.RepairLimit
	if limit == nil {
		t.Fatalf("expected ValidationRepairLimit to be recorded on coverage")
	}
	if limit.Name != "max_implementation_attempts" {
		t.Fatalf("expected repair limit name max_implementation_attempts, got %q", limit.Name)
	}
	if limit.Limit != 2 || limit.Reached != 2 {
		t.Fatalf("expected limit=2 reached=2, got limit=%d reached=%d", limit.Limit, limit.Reached)
	}
	if limit.PolicyRef != "policy.max_implementation_attempts" {
		t.Fatalf("expected policy_ref policy.max_implementation_attempts, got %q", limit.PolicyRef)
	}
	if st.verification.ValidationCoverage.Closable {
		t.Fatalf("expected coverage to be non-closable after repair limit")
	}
	if !strings.Contains(st.verification.ValidationCoverage.BlockReason, "derived-ruff") {
		t.Fatalf("expected coverage block reason to cite failing check id, got %q", st.verification.ValidationCoverage.BlockReason)
	}
}

// TestBuildVerificationBlockReason_IncludesCheckIDs guards the message the
// engine writes into the block reason whenever verification stays failing
// after the implementation budget runs out. Operators rely on this string
// to identify which commands they need to act on (AC5).
func TestBuildVerificationBlockReason_IncludesCheckIDs(t *testing.T) {
	reason := buildVerificationBlockReason(3, []string{"derived-ruff", "derived-pytest"})
	if !strings.Contains(reason, "3 attempt") {
		t.Fatalf("expected reason to include attempt count, got %q", reason)
	}
	if !strings.Contains(reason, "derived-ruff") || !strings.Contains(reason, "derived-pytest") {
		t.Fatalf("expected reason to include all failing check ids, got %q", reason)
	}
	if !strings.Contains(reason, string(state.EscalationNonConvergentVerification)) {
		t.Fatalf("expected canonical non-convergent prefix, got %q", reason)
	}
}

// TestBuildVerificationBlockReason_EmptyCheckIDs still returns a useful
// reason when the engine cannot narrow down the failure to specific
// checks (e.g. legacy coverage). Verifies AC6 compatibility with tickets
// that have no derived checks.
func TestBuildVerificationBlockReason_EmptyCheckIDs(t *testing.T) {
	reason := buildVerificationBlockReason(2, nil)
	if !strings.Contains(reason, "2 attempt") {
		t.Fatalf("expected reason to mention 2 attempts, got %q", reason)
	}
	if strings.Contains(reason, "unresolved checks") {
		t.Fatalf("expected no unresolved-checks tail when list is empty, got %q", reason)
	}
}

// newVerificationTestState assembles a minimal ticketRunState suitable for
// exercising handleVerificationFailure without a runtime adapter or a real
// git repository.
func newVerificationTestState(t *testing.T, runID, ticketID string, cfg policy.Config, attempts int) *ticketRunState {
	t.Helper()
	return &ticketRunState{
		ctx: context.Background(),
		req: RunTicketRequest{
			RunID:  runID,
			Ticket: tkmd.Ticket{ID: ticketID},
			Plan: state.PlanArtifact{
				TicketID:                 ticketID,
				EffectiveReviewThreshold: state.SeverityP2,
			},
			Claim: state.ClaimArtifact{TicketID: ticketID, LeaseID: "lease-" + ticketID},
		},
		cfg:                    cfg,
		paths:                  buildTicketRunPaths(t.TempDir(), runID, ticketID),
		repoRoot:               t.TempDir(),
		currentPhase:           state.TicketPhaseVerify,
		implementationAttempts: attempts,
	}
}
