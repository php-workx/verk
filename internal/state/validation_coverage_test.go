package state

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Legacy verification artifact written before ValidationCoverage existed must
// unmarshal cleanly and produce a nil ValidationCoverage pointer so existing
// readers keep working.
func TestVerificationArtifact_LegacyPayloadUnmarshalsWithNilCoverage(t *testing.T) {
	legacy := []byte(`{
		"schema_version": 1,
		"run_id": "run-1",
		"ticket_id": "ver-legacy",
		"attempt": 1,
		"commands": ["go test ./..."],
		"results": [{"command":"go test ./...","exit_code":0,"passed":true}],
		"passed": true,
		"repo_root": "/repo",
		"started_at": "2026-04-19T12:00:00Z",
		"finished_at": "2026-04-19T12:00:10Z"
	}`)

	var got VerificationArtifact
	if err := json.Unmarshal(legacy, &got); err != nil {
		t.Fatalf("unmarshal legacy verification artifact: %v", err)
	}
	if got.TicketID != "ver-legacy" {
		t.Fatalf("expected ticket id to round-trip, got %q", got.TicketID)
	}
	if !got.Passed {
		t.Fatalf("expected passed=true from legacy payload")
	}
	if got.ValidationCoverage != nil {
		t.Fatalf("expected nil ValidationCoverage for legacy payload, got %#v", got.ValidationCoverage)
	}
}

// Legacy closeout artifacts must survive round-tripping without requiring
// the new validation coverage fields.
func TestCloseoutArtifact_LegacyPayloadUnmarshalsWithoutCoverage(t *testing.T) {
	legacy := []byte(`{
		"schema_version": 1,
		"run_id": "run-1",
		"ticket_id": "ver-legacy",
		"criteria_evidence": [],
		"required_artifacts": [],
		"gate_results": {},
		"closable": true,
		"failed_gate": ""
	}`)

	var got CloseoutArtifact
	if err := json.Unmarshal(legacy, &got); err != nil {
		t.Fatalf("unmarshal legacy closeout artifact: %v", err)
	}
	if got.ValidationCoverage != nil {
		t.Fatalf("expected nil ValidationCoverage for legacy payload")
	}
	if got.UnresolvedCheckID != "" {
		t.Fatalf("expected empty UnresolvedCheckID for legacy payload, got %q", got.UnresolvedCheckID)
	}
	if got.BlockReason != "" {
		t.Fatalf("expected empty BlockReason for legacy payload, got %q", got.BlockReason)
	}
}

// A ticket validation coverage artifact round-trips with declared, derived,
// skipped checks and a repaired failure execution.
func TestValidationCoverageArtifact_TicketRoundTrip(t *testing.T) {
	now := time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC)
	coverage := ValidationCoverageArtifact{
		ArtifactMeta: ArtifactMeta{
			SchemaVersion: 1,
			RunID:         "run-1",
			CreatedAt:     now,
			UpdatedAt:     now,
		},
		Scope:    ValidationScopeTicket,
		TicketID: "ver-rcgh",
		DeclaredChecks: []ValidationCheck{{
			ID:       "declared-gotest",
			Scope:    ValidationScopeTicket,
			Source:   ValidationCheckSourceDeclared,
			Command:  "go test ./internal/state/... ./internal/engine/...",
			Reason:   "ticket validation_commands",
			TicketID: "ver-rcgh",
		}},
		DerivedChecks: []ValidationCheck{{
			ID:           "derived-go-state",
			Scope:        ValidationScopeTicket,
			Source:       ValidationCheckSourceDerived,
			Command:      "go test ./internal/state/...",
			Reason:       "matches changed Go files under internal/state",
			MatchedFiles: []string{"internal/state/validation_coverage.go"},
			TicketID:     "ver-rcgh",
		}},
		SkippedChecks: []ValidationCheckSkip{{
			CheckID: "derived-shellcheck",
			Reason:  "shellcheck tooling not installed",
			Detail:  "optional shell linter missing from PATH",
		}},
		ExecutedChecks: []ValidationCheckExecution{
			{
				CheckID:        "derived-go-state",
				Result:         ValidationCheckResultFailed,
				ExitCode:       1,
				Attempt:        1,
				FailureSummary: "compile error in validation_coverage.go",
				StartedAt:      now,
				FinishedAt:     now.Add(5 * time.Second),
			},
			{
				CheckID:       "derived-go-state",
				Result:        ValidationCheckResultRepaired,
				ExitCode:      0,
				Attempt:       2,
				RepairCycleID: "repair-1",
				StartedAt:     now.Add(30 * time.Second),
				FinishedAt:    now.Add(40 * time.Second),
			},
		},
		RepairRefs: []ValidationRepairRef{{
			CheckID:      "derived-go-state",
			CycleID:      "repair-1",
			ArtifactPath: ".verk/runs/run-1/tickets/ver-rcgh/cycles/repair-1.json",
			Result:       ValidationCheckResultRepaired,
			Scope:        ValidationScopeTicket,
		}},
		Closable:      true,
		ClosureReason: "all declared and derived checks passed or repaired",
	}

	data, err := json.Marshal(coverage)
	if err != nil {
		t.Fatalf("marshal coverage: %v", err)
	}

	var got ValidationCoverageArtifact
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal coverage: %v", err)
	}

	if got.Scope != ValidationScopeTicket {
		t.Fatalf("expected scope ticket, got %q", got.Scope)
	}
	if len(got.DeclaredChecks) != 1 || got.DeclaredChecks[0].ID != "declared-gotest" {
		t.Fatalf("expected declared check to round-trip, got %#v", got.DeclaredChecks)
	}
	if len(got.DerivedChecks) != 1 || got.DerivedChecks[0].Source != ValidationCheckSourceDerived {
		t.Fatalf("expected derived check to round-trip, got %#v", got.DerivedChecks)
	}
	if len(got.SkippedChecks) != 1 || got.SkippedChecks[0].CheckID != "derived-shellcheck" {
		t.Fatalf("expected skipped check to round-trip, got %#v", got.SkippedChecks)
	}
	if len(got.ExecutedChecks) != 2 {
		t.Fatalf("expected two executions, got %d", len(got.ExecutedChecks))
	}
	if got.ExecutedChecks[1].Result != ValidationCheckResultRepaired {
		t.Fatalf("expected repaired result on second execution, got %q", got.ExecutedChecks[1].Result)
	}
	if len(got.RepairRefs) != 1 || got.RepairRefs[0].CycleID != "repair-1" {
		t.Fatalf("expected repair ref to round-trip, got %#v", got.RepairRefs)
	}
	if !got.Closable {
		t.Fatalf("expected closable=true to round-trip")
	}

	latest, ok := got.LatestExecution("derived-go-state")
	if !ok {
		t.Fatalf("expected LatestExecution to find entries")
	}
	if latest.Result != ValidationCheckResultRepaired {
		t.Fatalf("expected latest execution to be repaired, got %q", latest.Result)
	}

	check, ok := got.CheckByID("derived-go-state")
	if !ok || check.Reason == "" {
		t.Fatalf("expected CheckByID to locate derived check with reason")
	}
}

// A wave validation coverage artifact can reference child ticket ids and
// repair artifact ids so downstream UIs can follow the chain.
func TestValidationCoverageArtifact_WaveReferencesChildrenAndRepairs(t *testing.T) {
	coverage := ValidationCoverageArtifact{
		ArtifactMeta: ArtifactMeta{
			SchemaVersion: 1,
			RunID:         "run-1",
		},
		Scope:          ValidationScopeWave,
		WaveID:         "wave-2",
		ChildTicketIDs: []string{"ver-a", "ver-b", "ver-c"},
		DeclaredChecks: []ValidationCheck{{
			ID:      "quality-just-lint",
			Scope:   ValidationScopeWave,
			Source:  ValidationCheckSourceQuality,
			Command: "just lint",
			Reason:  "repo-wide quality command",
			WaveID:  "wave-2",
		}},
		DerivedChecks: []ValidationCheck{{
			ID:           "derived-markdown",
			Scope:        ValidationScopeWave,
			Source:       ValidationCheckSourceDerived,
			Command:      "markdownlint docs/**/*.md",
			Reason:       "wave merged changes touched markdown files",
			MatchedFiles: []string{"docs/overview.md"},
			WaveID:       "wave-2",
			Advisory:     true,
		}},
		RepairRefs: []ValidationRepairRef{{
			CheckID:      "quality-just-lint",
			CycleID:      "wave-repair-1",
			ArtifactPath: ".verk/runs/run-1/waves/wave-2/cycles/wave-repair-1.json",
			Result:       ValidationCheckResultRepaired,
			Scope:        ValidationScopeWave,
		}},
		Closable:      true,
		ClosureReason: "wave quality commands passed after 1 repair cycle",
	}

	data, err := json.Marshal(coverage)
	if err != nil {
		t.Fatalf("marshal wave coverage: %v", err)
	}

	var got ValidationCoverageArtifact
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal wave coverage: %v", err)
	}

	if got.Scope != ValidationScopeWave {
		t.Fatalf("expected wave scope, got %q", got.Scope)
	}
	if got.WaveID != "wave-2" {
		t.Fatalf("expected wave id wave-2, got %q", got.WaveID)
	}
	if len(got.ChildTicketIDs) != 3 {
		t.Fatalf("expected 3 child ticket ids, got %d", len(got.ChildTicketIDs))
	}
	if len(got.RepairRefs) != 1 || got.RepairRefs[0].Scope != ValidationScopeWave {
		t.Fatalf("expected wave-scoped repair ref")
	}
	if len(got.DerivedChecks) != 1 || !got.DerivedChecks[0].Advisory {
		t.Fatalf("expected advisory derived check on wave")
	}

	// AllChecks should aggregate declared and derived.
	all := got.AllChecks()
	if len(all) != 2 {
		t.Fatalf("expected AllChecks=2, got %d", len(all))
	}
}

// A blocked closeout references the unresolved validation check id and
// records the human-readable reason alongside the ValidationCoverage
// blocker list.
func TestCloseoutArtifact_BlockedRecordsUnresolvedCheck(t *testing.T) {
	closeout := CloseoutArtifact{
		ArtifactMeta:      ArtifactMeta{SchemaVersion: 1, RunID: "run-1"},
		TicketID:          "ver-rcgh",
		GateResults:       map[string]GateResult{},
		Closable:          false,
		FailedGate:        "declared_checks",
		UnresolvedCheckID: "derived-ruff",
		BlockReason:       "derived ruff check failed after 3 repair attempts",
		ValidationCoverage: &ValidationCoverageArtifact{
			ArtifactMeta: ArtifactMeta{SchemaVersion: 1, RunID: "run-1"},
			Scope:        ValidationScopeTicket,
			TicketID:     "ver-rcgh",
			UnresolvedBlockers: []ValidationBlocker{{
				CheckID:          "derived-ruff",
				Reason:           "ruff SIM117 still failing after exhausting repair cycles",
				RequiresOperator: true,
				RepairCycleID:    "repair-3",
				Scope:            ValidationScopeTicket,
			}},
			Closable:    false,
			BlockReason: "derived ruff check failed after 3 repair attempts",
		},
	}

	data, err := json.Marshal(closeout)
	if err != nil {
		t.Fatalf("marshal blocked closeout: %v", err)
	}
	if !strings.Contains(string(data), "unresolved_check_id") {
		t.Fatalf("expected serialized json to include unresolved_check_id; got:\n%s", data)
	}
	if !strings.Contains(string(data), "unresolved_blockers") {
		t.Fatalf("expected serialized json to include unresolved_blockers; got:\n%s", data)
	}

	var got CloseoutArtifact
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal blocked closeout: %v", err)
	}
	if got.Closable {
		t.Fatalf("expected closable=false to survive round-trip")
	}
	if got.UnresolvedCheckID != "derived-ruff" {
		t.Fatalf("expected unresolved check id to round-trip, got %q", got.UnresolvedCheckID)
	}
	if got.BlockReason == "" {
		t.Fatalf("expected block reason to round-trip")
	}
	if got.ValidationCoverage == nil {
		t.Fatalf("expected validation coverage pointer")
	}
	if len(got.ValidationCoverage.UnresolvedBlockers) != 1 {
		t.Fatalf("expected one unresolved blocker")
	}
	if !got.ValidationCoverage.UnresolvedBlockers[0].RequiresOperator {
		t.Fatalf("expected blocker to require operator input")
	}
}

// A repair cycle artifact can record which policy limit stopped further
// repair and which check ids triggered it, while still round-tripping the
// original review-finding trigger fields.
func TestRepairCycleArtifact_RecordsPolicyLimitAndCheckTriggers(t *testing.T) {
	now := time.Now().UTC()
	cycle := RepairCycleArtifact{
		ArtifactMeta:      ArtifactMeta{SchemaVersion: 1, RunID: "run-1"},
		TicketID:          "ver-rcgh",
		Cycle:             3,
		Scope:             ValidationScopeTicket,
		TriggerFindingIDs: []string{"finding-1"},
		TriggerCheckIDs:   []string{"derived-ruff"},
		RepairNotes:       "repair limit reached",
		Status:            "blocked",
		StartedAt:         now,
		FinishedAt:        now.Add(time.Minute),
		PolicyLimitReached: &ValidationRepairLimit{
			Name:      "policy.max_repair_cycles",
			Limit:     2,
			Reached:   3,
			Reason:    "review findings still blocking after max cycles",
			PolicyRef: "policy.max_repair_cycles",
		},
	}

	data, err := json.Marshal(cycle)
	if err != nil {
		t.Fatalf("marshal repair cycle: %v", err)
	}

	var got RepairCycleArtifact
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal repair cycle: %v", err)
	}

	if got.Scope != ValidationScopeTicket {
		t.Fatalf("expected scope ticket, got %q", got.Scope)
	}
	if len(got.TriggerCheckIDs) != 1 || got.TriggerCheckIDs[0] != "derived-ruff" {
		t.Fatalf("expected trigger check ids to round-trip, got %#v", got.TriggerCheckIDs)
	}
	if len(got.TriggerFindingIDs) != 1 || got.TriggerFindingIDs[0] != "finding-1" {
		t.Fatalf("expected trigger finding ids to round-trip")
	}
	if got.PolicyLimitReached == nil {
		t.Fatalf("expected policy limit to round-trip")
	}
	if got.PolicyLimitReached.Name != "policy.max_repair_cycles" {
		t.Fatalf("unexpected policy limit name: %q", got.PolicyLimitReached.Name)
	}
	if got.PolicyLimitReached.Reached != 3 {
		t.Fatalf("expected reached=3, got %d", got.PolicyLimitReached.Reached)
	}
}

// A repair cycle artifact from an older run (no Scope / TriggerCheckIDs /
// PolicyLimitReached fields) must still unmarshal correctly.
func TestRepairCycleArtifact_LegacyPayloadUnmarshalsWithDefaults(t *testing.T) {
	legacy := []byte(`{
		"schema_version": 1,
		"run_id": "run-1",
		"ticket_id": "ver-rcgh",
		"cycle": 1,
		"trigger_finding_ids": ["finding-1"],
		"input_review_artifact": "review.json",
		"repair_notes": "",
		"verification_artifact": "",
		"review_artifact": "",
		"status": "completed",
		"started_at": "2026-04-19T12:00:00Z",
		"finished_at": "2026-04-19T12:05:00Z"
	}`)

	var got RepairCycleArtifact
	if err := json.Unmarshal(legacy, &got); err != nil {
		t.Fatalf("unmarshal legacy repair cycle: %v", err)
	}
	if got.Scope != "" {
		t.Fatalf("expected empty scope for legacy payload, got %q", got.Scope)
	}
	if len(got.TriggerCheckIDs) != 0 {
		t.Fatalf("expected empty trigger check ids for legacy payload")
	}
	if got.PolicyLimitReached != nil {
		t.Fatalf("expected nil policy limit for legacy payload")
	}
	if got.Cycle != 1 {
		t.Fatalf("expected cycle=1 to round-trip, got %d", got.Cycle)
	}
}

// A wave artifact from an older run (no ValidationCoverage field) must
// unmarshal cleanly and keep ValidationCoverage == nil.
func TestWaveArtifact_LegacyPayloadUnmarshalsWithoutCoverage(t *testing.T) {
	legacy := []byte(`{
		"schema_version": 1,
		"run_id": "run-1",
		"wave_id": "wave-1",
		"ordinal": 1,
		"status": "accepted",
		"ticket_ids": ["ver-a","ver-b"],
		"planned_scope": [],
		"actual_scope": [],
		"acceptance": {"wave_verification_passed": true},
		"wave_base_commit": "abc123",
		"started_at": "2026-04-19T12:00:00Z",
		"finished_at": "2026-04-19T12:30:00Z"
	}`)

	var got WaveArtifact
	if err := json.Unmarshal(legacy, &got); err != nil {
		t.Fatalf("unmarshal legacy wave artifact: %v", err)
	}
	if got.ValidationCoverage != nil {
		t.Fatalf("expected nil ValidationCoverage for legacy wave payload")
	}
	if got.WaveID != "wave-1" {
		t.Fatalf("expected wave_id to round-trip, got %q", got.WaveID)
	}
}

// An epic-scope validation coverage artifact with a repair limit records
// which bounded-loop limit stopped further repair.
func TestValidationCoverageArtifact_EpicRepairLimit(t *testing.T) {
	coverage := ValidationCoverageArtifact{
		ArtifactMeta:   ArtifactMeta{SchemaVersion: 1, RunID: "run-1"},
		Scope:          ValidationScopeEpic,
		EpicID:         "ver-vyag",
		ChildTicketIDs: []string{"ver-rcgh", "ver-y29o", "ver-1qru"},
		RepairLimit: &ValidationRepairLimit{
			Name:      "policy.max_repair_cycles",
			Limit:     2,
			Reached:   2,
			Reason:    "epic reviewer findings exceeded configured repair budget",
			PolicyRef: "policy.max_repair_cycles",
		},
		UnresolvedBlockers: []ValidationBlocker{{
			FindingID:        "epic-review-1",
			Reason:           "missing e2e coverage for stale docs scenario",
			RequiresOperator: true,
			Scope:            ValidationScopeEpic,
		}},
		Closable:    false,
		BlockReason: "epic reviewer still blocking after repair budget exhausted",
	}

	data, err := json.Marshal(coverage)
	if err != nil {
		t.Fatalf("marshal epic coverage: %v", err)
	}
	var got ValidationCoverageArtifact
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal epic coverage: %v", err)
	}
	if got.Scope != ValidationScopeEpic {
		t.Fatalf("expected epic scope")
	}
	if got.RepairLimit == nil {
		t.Fatalf("expected repair limit to survive round-trip")
	}
	if got.RepairLimit.Reached != 2 {
		t.Fatalf("expected reached=2, got %d", got.RepairLimit.Reached)
	}
	if len(got.UnresolvedBlockers) != 1 {
		t.Fatalf("expected one unresolved blocker")
	}
	if got.UnresolvedBlockers[0].Scope != ValidationScopeEpic {
		t.Fatalf("expected epic-scoped blocker")
	}
}

// ValidationCheckResultFromBool is a small adapter; make sure both branches
// map to the expected results so engine code can rely on the helper.
func TestValidationCheckResultFromBool(t *testing.T) {
	if got := ValidationCheckResultFromBool(true); got != ValidationCheckResultPassed {
		t.Fatalf("expected passed, got %q", got)
	}
	if got := ValidationCheckResultFromBool(false); got != ValidationCheckResultFailed {
		t.Fatalf("expected failed, got %q", got)
	}
}
