package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"verk/internal/adapters/runtime"
	runtimefake "verk/internal/adapters/runtime/fake"
	"verk/internal/policy"
	"verk/internal/state"
)

func defaultIntentCfg() policy.IntentConfig {
	return policy.IntentConfig{
		Enabled:     true,
		MaxAttempts: 2,
	}
}

func planWithCriteria(ownedPaths []string, criteriaIDs ...string) state.PlanArtifact {
	criteria := make([]state.PlanCriterion, len(criteriaIDs))
	for i, id := range criteriaIDs {
		criteria[i] = state.PlanCriterion{ID: id, Text: "criterion " + id}
	}
	return state.PlanArtifact{
		OwnedPaths: ownedPaths,
		Criteria:   criteria,
	}
}

// TestIntentGate_Disabled verifies the gate is a no-op when Enabled is false.
func TestIntentGate_Disabled(t *testing.T) {
	repoRoot := t.TempDir()
	adapter := runtimefake.NewWithIntents(nil, nil, nil) // no scripted results

	plan := planWithCriteria([]string{"internal/app"}, "AC-1")
	cfg := policy.IntentConfig{Enabled: false, MaxAttempts: 2}
	req := runtime.IntentRequest{
		RunID:    "run-disabled",
		TicketID: "ticket-disabled",
		LeaseID:  "lease-disabled",
	}

	result, err := runIntentGate(context.Background(), repoRoot, "run-disabled", "ticket-disabled", adapter, req, plan, cfg)
	if err != nil {
		t.Fatalf("runIntentGate returned error: %v", err)
	}
	if !result.Passed {
		t.Errorf("expected Passed=true when gate is disabled, got Passed=false")
	}
	if result.Artifact != nil {
		t.Errorf("expected no artifact when gate is disabled, got non-nil")
	}
	// Adapter should not have been called.
	if reqs := adapter.IntentRequests(); len(reqs) != 0 {
		t.Errorf("expected 0 RunIntent calls when disabled, got %d", len(reqs))
	}
}

// TestIntentGate_HappyPath verifies the gate passes when all rules are satisfied.
func TestIntentGate_HappyPath(t *testing.T) {
	repoRoot := t.TempDir()
	plan := planWithCriteria([]string{"internal/app", "internal/lib"}, "AC-1", "AC-2")

	intentResult := runtime.IntentResult{
		CoveredCriteria: []string{"AC-1", "AC-2"},
		TargetFiles:     []string{"internal/app"},
		TestPlan:        "run go test ./...",
	}
	adapter := runtimefake.NewWithIntents(nil, nil, []runtime.IntentResult{intentResult})

	req := runtime.IntentRequest{
		RunID:    "run-happy",
		TicketID: "ticket-happy",
		LeaseID:  "lease-happy",
	}

	result, err := runIntentGate(context.Background(), repoRoot, "run-happy", "ticket-happy", adapter, req, plan, defaultIntentCfg())
	if err != nil {
		t.Fatalf("runIntentGate error: %v", err)
	}
	if !result.Passed {
		t.Errorf("expected Passed=true on valid intent result, got Passed=false, BlockReason=%q", result.BlockReason)
	}
	if result.Artifact == nil {
		t.Fatal("expected non-nil artifact on pass")
	}
	if result.Artifact.Attempt != 1 {
		t.Errorf("expected attempt=1, got %d", result.Artifact.Attempt)
	}

	// Verify artifact was persisted.
	artifactPath := filepath.Join(repoRoot, ".verk", "runs", "run-happy", "tickets", "ticket-happy", "intent-1.json")
	if _, err := os.Stat(artifactPath); err != nil {
		t.Errorf("expected intent artifact file to exist at %s: %v", artifactPath, err)
	}
}

// TestIntentGate_MissingCriteriaRejects verifies rule 1: missing criterion → reject.
func TestIntentGate_MissingCriteriaRejects(t *testing.T) {
	repoRoot := t.TempDir()
	plan := planWithCriteria([]string{"internal/app"}, "AC-1", "AC-2")

	// Only covers AC-1, missing AC-2.
	badResult := runtime.IntentResult{
		CoveredCriteria: []string{"AC-1"},
		TargetFiles:     []string{"internal/app"},
		TestPlan:        "run go test ./...",
	}
	adapter := runtimefake.NewWithIntents(nil, nil, []runtime.IntentResult{badResult, badResult})
	req := runtime.IntentRequest{RunID: "run-missing-ac", TicketID: "ticket-missing-ac", LeaseID: "lease-missing-ac"}

	result, err := runIntentGate(context.Background(), repoRoot, "run-missing-ac", "ticket-missing-ac", adapter, req, plan, defaultIntentCfg())
	if err != nil {
		t.Fatalf("runIntentGate error: %v", err)
	}
	if result.Passed {
		t.Error("expected gate to reject when criterion is missing, but Passed=true")
	}
	if result.BlockReason != state.EscalationNonConvergentIntent {
		t.Errorf("expected BlockReason=%q, got %q", state.EscalationNonConvergentIntent, result.BlockReason)
	}
}

// TestIntentGate_SupersetPathsRejects verifies rule 2: files outside owned paths → reject.
func TestIntentGate_SupersetPathsRejects(t *testing.T) {
	repoRoot := t.TempDir()
	plan := planWithCriteria([]string{"internal/app"}, "AC-1")

	// TargetFiles includes "internal/other" which is not in OwnedPaths.
	badResult := runtime.IntentResult{
		CoveredCriteria: []string{"AC-1"},
		TargetFiles:     []string{"internal/app", "internal/other"},
		TestPlan:        "run go test ./...",
	}
	adapter := runtimefake.NewWithIntents(nil, nil, []runtime.IntentResult{badResult, badResult})
	req := runtime.IntentRequest{RunID: "run-superset", TicketID: "ticket-superset", LeaseID: "lease-superset"}

	result, err := runIntentGate(context.Background(), repoRoot, "run-superset", "ticket-superset", adapter, req, plan, defaultIntentCfg())
	if err != nil {
		t.Fatalf("runIntentGate error: %v", err)
	}
	if result.Passed {
		t.Error("expected gate to reject on superset paths, but Passed=true")
	}
	if result.BlockReason != state.EscalationNonConvergentIntent {
		t.Errorf("expected BlockReason=%q, got %q", state.EscalationNonConvergentIntent, result.BlockReason)
	}
}

// TestIntentGate_EmptyTestPlanRejects verifies rule 3: empty test plan → reject.
func TestIntentGate_EmptyTestPlanRejects(t *testing.T) {
	repoRoot := t.TempDir()
	plan := planWithCriteria([]string{"internal/app"}, "AC-1")

	badResult := runtime.IntentResult{
		CoveredCriteria: []string{"AC-1"},
		TargetFiles:     []string{"internal/app"},
		TestPlan:        "", // empty
	}
	adapter := runtimefake.NewWithIntents(nil, nil, []runtime.IntentResult{badResult, badResult})
	req := runtime.IntentRequest{RunID: "run-no-test", TicketID: "ticket-no-test", LeaseID: "lease-no-test"}

	result, err := runIntentGate(context.Background(), repoRoot, "run-no-test", "ticket-no-test", adapter, req, plan, defaultIntentCfg())
	if err != nil {
		t.Fatalf("runIntentGate error: %v", err)
	}
	if result.Passed {
		t.Error("expected gate to reject on empty test plan, but Passed=true")
	}
	if result.BlockReason != state.EscalationNonConvergentIntent {
		t.Errorf("expected BlockReason=%q, got %q", state.EscalationNonConvergentIntent, result.BlockReason)
	}
}

// TestIntentGate_EmptyTargetFilesAllowed verifies that empty TargetFiles passes rule 2.
func TestIntentGate_EmptyTargetFilesAllowed(t *testing.T) {
	repoRoot := t.TempDir()
	plan := planWithCriteria([]string{"internal/app"}, "AC-1")

	goodResult := runtime.IntentResult{
		CoveredCriteria: []string{"AC-1"},
		TargetFiles:     nil, // empty is OK
		TestPlan:        "run tests",
	}
	adapter := runtimefake.NewWithIntents(nil, nil, []runtime.IntentResult{goodResult})
	req := runtime.IntentRequest{RunID: "run-empty-targets", TicketID: "ticket-empty-targets", LeaseID: "lease-empty-targets"}

	result, err := runIntentGate(context.Background(), repoRoot, "run-empty-targets", "ticket-empty-targets", adapter, req, plan, defaultIntentCfg())
	if err != nil {
		t.Fatalf("runIntentGate error: %v", err)
	}
	if !result.Passed {
		t.Errorf("expected empty TargetFiles to pass rule 2, got Passed=false, BlockReason=%q", result.BlockReason)
	}
}

// TestIntentGate_ThirdRetryBlocks verifies that after MaxAttempts=3 failures the
// gate returns BlockReason "intent_non_convergent" (not an error).
func TestIntentGate_ThirdRetryBlocks(t *testing.T) {
	repoRoot := t.TempDir()
	// MaxAttempts=3 to ensure exactly 3 attempts before blocking.
	cfg := policy.IntentConfig{Enabled: true, MaxAttempts: 3}
	plan := planWithCriteria([]string{"internal/app"}, "AC-1")

	// Always-failing result (missing test plan).
	bad := runtime.IntentResult{
		CoveredCriteria: []string{"AC-1"},
		TargetFiles:     []string{"internal/app"},
		TestPlan:        "",
	}
	adapter := runtimefake.NewWithIntents(nil, nil, []runtime.IntentResult{bad, bad, bad})
	req := runtime.IntentRequest{RunID: "run-exhaust", TicketID: "ticket-exhaust", LeaseID: "lease-exhaust"}

	result, err := runIntentGate(context.Background(), repoRoot, "run-exhaust", "ticket-exhaust", adapter, req, plan, cfg)
	if err != nil {
		t.Fatalf("runIntentGate error: %v", err)
	}
	if result.Passed {
		t.Error("expected gate to block after max attempts, but Passed=true")
	}
	if result.BlockReason != state.EscalationNonConvergentIntent {
		t.Errorf("expected BlockReason=%q, got %q", state.EscalationNonConvergentIntent, result.BlockReason)
	}

	// All 3 attempts must have been consumed.
	if reqs := adapter.IntentRequests(); len(reqs) != 3 {
		t.Errorf("expected exactly 3 RunIntent calls, got %d", len(reqs))
	}

	// All 3 artifact files must exist.
	for attempt := 1; attempt <= 3; attempt++ {
		p := filepath.Join(repoRoot, ".verk", "runs", "run-exhaust", "tickets", "ticket-exhaust",
			fmt.Sprintf("intent-%d.json", attempt))
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected artifact for attempt %d at %s: %v", attempt, p, err)
		}
	}
}

// TestIntentGate_PassesOnSecondAttempt verifies retry: first attempt fails,
// second passes, gate returns Passed=true without blocking.
func TestIntentGate_PassesOnSecondAttempt(t *testing.T) {
	repoRoot := t.TempDir()
	plan := planWithCriteria([]string{"internal/app"}, "AC-1")

	bad := runtime.IntentResult{
		CoveredCriteria: []string{"AC-1"},
		TargetFiles:     []string{"internal/app"},
		TestPlan:        "", // fail first attempt
	}
	good := runtime.IntentResult{
		CoveredCriteria: []string{"AC-1"},
		TargetFiles:     []string{"internal/app"},
		TestPlan:        "go test ./...",
	}
	adapter := runtimefake.NewWithIntents(nil, nil, []runtime.IntentResult{bad, good})
	req := runtime.IntentRequest{RunID: "run-retry", TicketID: "ticket-retry", LeaseID: "lease-retry"}

	result, err := runIntentGate(context.Background(), repoRoot, "run-retry", "ticket-retry", adapter, req, plan, defaultIntentCfg())
	if err != nil {
		t.Fatalf("runIntentGate error: %v", err)
	}
	if !result.Passed {
		t.Errorf("expected gate to pass on second attempt, got Passed=false, BlockReason=%q", result.BlockReason)
	}
	if result.Artifact == nil || result.Artifact.Attempt != 2 {
		t.Errorf("expected artifact from attempt 2, got %v", result.Artifact)
	}
}

// TestValidateIntentResult_AllRulesPass is a direct unit test of validateIntentResult.
func TestValidateIntentResult_AllRulesPass(t *testing.T) {
	required := map[string]struct{}{"AC-1": {}, "AC-2": {}}
	owned := []string{"internal/app", "internal/lib"}

	result := runtime.IntentResult{
		CoveredCriteria: []string{"AC-1", "AC-2"},
		TargetFiles:     []string{"internal/app"},
		TestPlan:        "go test ./...",
	}
	if reason := validateIntentResult(result, required, owned); reason != "" {
		t.Errorf("expected empty reject reason, got %q", reason)
	}
}

// TestValidateIntentResult_MissingCriteria unit test for rule 1.
func TestValidateIntentResult_MissingCriteria(t *testing.T) {
	required := map[string]struct{}{"AC-1": {}, "AC-2": {}}
	owned := []string{"internal/app"}

	result := runtime.IntentResult{
		CoveredCriteria: []string{"AC-1"}, // AC-2 missing
		TargetFiles:     []string{"internal/app"},
		TestPlan:        "go test ./...",
	}
	if reason := validateIntentResult(result, required, owned); reason != "missing_criteria" {
		t.Errorf("expected missing_criteria, got %q", reason)
	}
}

// TestValidateIntentResult_SupersetPaths unit test for rule 2.
func TestValidateIntentResult_SupersetPaths(t *testing.T) {
	required := map[string]struct{}{}
	owned := []string{"internal/app"}

	result := runtime.IntentResult{
		CoveredCriteria: nil,
		TargetFiles:     []string{"internal/app", "internal/other"}, // other is not owned
		TestPlan:        "go test ./...",
	}
	if reason := validateIntentResult(result, required, owned); reason != "superset_paths" {
		t.Errorf("expected superset_paths, got %q", reason)
	}
}

// TestValidateIntentResult_EmptyTestPlan unit test for rule 3.
func TestValidateIntentResult_EmptyTestPlan(t *testing.T) {
	required := map[string]struct{}{}
	owned := []string{"internal/app"}

	result := runtime.IntentResult{
		CoveredCriteria: nil,
		TargetFiles:     []string{"internal/app"},
		TestPlan:        "   ", // whitespace-only = empty
	}
	if reason := validateIntentResult(result, required, owned); reason != "empty_test_plan" {
		t.Errorf("expected empty_test_plan, got %q", reason)
	}
}
