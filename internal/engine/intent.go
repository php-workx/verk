package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"verk/internal/adapters/runtime"
	"verk/internal/policy"
	"verk/internal/state"
)

// intentGateResult is the outcome of a single runIntentGate call.
type intentGateResult struct {
	// Artifact is the last intent artifact written (always non-nil when gate ran
	// at least one attempt). Nil when the gate is disabled.
	Artifact *state.IntentArtifact
	// Passed is true when the gate validated successfully.
	Passed bool
	// BlockReason is set when Passed is false after all retries are exhausted.
	BlockReason string
}

// runIntentGate runs the intent echo phase for one ticket attempt.
// It calls adapter.RunIntent up to cfg.MaxAttempts times, validating
// each result against the plan's acceptance criteria and owned paths.
//
// Returns immediately with Passed=true when cfg.Enabled is false.
//
// Validation rules applied to each attempt result:
//   - Every criterion ID from plan.Criteria must appear in result.CoveredCriteria.
//     Missing criteria → reject reason "missing_criteria".
//   - result.TargetFiles must be a subset of plan.OwnedPaths (empty TargetFiles is OK).
//     Files outside the owned set → reject reason "superset_paths".
//   - result.TestPlan must be non-empty.
//     Empty → reject reason "empty_test_plan".
//
// After MaxAttempts failures the gate returns BlockReason "intent_non_convergent".
// Each attempt artifact is persisted to:
//
//	.verk/runs/<run-id>/tickets/<ticket-id>/intent-<attempt>.json
func runIntentGate(
	ctx context.Context,
	repoRoot string,
	runID string,
	ticketID string,
	adapter runtime.Adapter,
	intentReq runtime.IntentRequest,
	plan state.PlanArtifact,
	cfg policy.IntentConfig,
) (intentGateResult, error) {
	if !cfg.Enabled {
		return intentGateResult{Passed: true}, nil
	}

	maxAttempts := cfg.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 2
	}

	// Build the set of criterion IDs required by the plan.
	// Normalize to trimmed form so whitespace differences don't cause false mismatches.
	requiredCriteria := make(map[string]struct{}, len(plan.Criteria))
	for _, c := range plan.Criteria {
		if id := strings.TrimSpace(c.ID); id != "" {
			requiredCriteria[id] = struct{}{}
		}
	}

	var lastArtifact *state.IntentArtifact

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return intentGateResult{}, err
		}

		req := intentReq
		req.Attempt = attempt

		result, err := adapter.RunIntent(ctx, req)
		if err != nil {
			return intentGateResult{}, fmt.Errorf("run intent attempt %d: %w", attempt, err)
		}

		now := time.Now().UTC()
		artifact := &state.IntentArtifact{
			ArtifactMeta: state.ArtifactMeta{
				SchemaVersion: artifactSchemaVersion,
				RunID:         runID,
				CreatedAt:     now,
				UpdatedAt:     now,
			},
			Attempt:         attempt,
			CoveredCriteria: append([]string(nil), result.CoveredCriteria...),
			TargetFiles:     append([]string(nil), result.TargetFiles...),
			TestPlan:        result.TestPlan,
			RawResponse:     result.RawResponse,
		}

		artifactPath := filepath.Join(
			repoRoot, ".verk", "runs", runID,
			"tickets", ticketID,
			fmt.Sprintf("intent-%d.json", attempt),
		)
		if err := state.SaveJSONAtomic(artifactPath, artifact); err != nil {
			return intentGateResult{}, fmt.Errorf("persist intent artifact attempt %d: %w", attempt, err)
		}

		lastArtifact = artifact

		rejectReason := validateIntentResult(result, requiredCriteria, plan.OwnedPaths)
		if rejectReason == "" {
			return intentGateResult{
				Artifact: artifact,
				Passed:   true,
			}, nil
		}
	}

	// All attempts exhausted without a passing result.
	return intentGateResult{
		Artifact:    lastArtifact,
		Passed:      false,
		BlockReason: state.EscalationNonConvergentIntent,
	}, nil
}

// validateIntentResult checks a single RunIntent result against the plan's
// required criteria and owned paths. Returns an empty string on success or a
// short reject reason string on failure.
func validateIntentResult(
	result runtime.IntentResult,
	requiredCriteria map[string]struct{},
	ownedPaths []string,
) string {
	// Rule 1: every required criterion must appear in CoveredCriteria.
	if len(requiredCriteria) > 0 {
		coveredSet := make(map[string]struct{}, len(result.CoveredCriteria))
		for _, id := range result.CoveredCriteria {
			if norm := strings.TrimSpace(id); norm != "" {
				coveredSet[norm] = struct{}{}
			}
		}
		for id := range requiredCriteria {
			if _, ok := coveredSet[id]; !ok {
				return "missing_criteria"
			}
		}
	}

	// Rule 2: TargetFiles must be a subset of OwnedPaths using prefix matching
	// (consistent with fileInOwned in wave_scheduler.go).
	for _, f := range result.TargetFiles {
		if !fileInOwned(f, ownedPaths) {
			return "superset_paths"
		}
	}

	// Rule 3: TestPlan must be non-empty.
	if strings.TrimSpace(result.TestPlan) == "" {
		return "empty_test_plan"
	}

	return ""
}
