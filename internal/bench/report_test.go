package bench

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func makeTestRunResult() RunResult {
	return RunResult{
		RunID:     "run-test-123",
		SuiteName: "smoke",
		Mode:      ModeFullVerk,
		Profiles: []ResolvedProfileSnapshot{
			{
				MatrixProfile: MatrixProfile{
					ID:       "p1",
					Worker:   ModelRef{Runtime: "claude", Model: "claude-3-5-sonnet"},
					Reviewer: ModelRef{Runtime: "claude", Model: "claude-3-opus"},
				},
				ResolvedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			},
		},
		Manifest: LockedTaskManifest{
			Suite:   "smoke",
			TaskIDs: []string{"task-1", "task-2", "task-3", "task-4", "task-5"},
		},
		Results: []TaskResult{
			{
				TaskID: "task-1", ProfileID: "p1", Status: TaskStatusSolved, DurationMS: 1000,
				Usage: []UsageRecord{{Confidence: "exact", CostUSD: 0.01}},
			},
			{
				TaskID: "task-2", ProfileID: "p1", Status: TaskStatusSolved, DurationMS: 2000,
				Usage: []UsageRecord{{Confidence: "estimated", CostUSD: 0.02}},
			},
			{TaskID: "task-3", ProfileID: "p1", Status: TaskStatusUnsolved, DurationMS: 3000},
			{TaskID: "task-4", ProfileID: "p1", Status: TaskStatusBlocked, DurationMS: 500},
			{TaskID: "task-5", ProfileID: "p1", Status: TaskStatusVerifierFlaky, DurationMS: 800},
		},
	}
}

func TestBuildReport_TotalsAggregateCorrectly(t *testing.T) {
	rr := makeTestRunResult()
	report := BuildReport(rr)

	if report.Totals.TotalTasks != 5 {
		t.Errorf("TotalTasks: got %d, want 5", report.Totals.TotalTasks)
	}
	if report.Totals.Solved != 2 {
		t.Errorf("Solved: got %d, want 2", report.Totals.Solved)
	}
	if report.Totals.Unsolved != 1 {
		t.Errorf("Unsolved: got %d, want 1", report.Totals.Unsolved)
	}
	if report.Totals.Blocked != 1 {
		t.Errorf("Blocked: got %d, want 1", report.Totals.Blocked)
	}
	if report.Totals.VerifierFlaky != 1 {
		t.Errorf("VerifierFlaky: got %d, want 1", report.Totals.VerifierFlaky)
	}

	// Raw: 2/5 = 0.4
	if report.Totals.RawSolveRate != 0.4 {
		t.Errorf("RawSolveRate: got %.4f, want 0.4000", report.Totals.RawSolveRate)
	}
	// Flake-adjusted: 2 / (5 - 1) = 0.5
	if report.Totals.FlakeAdjusted != 0.5 {
		t.Errorf("FlakeAdjusted: got %.4f, want 0.5000", report.Totals.FlakeAdjusted)
	}

	// ExactCostUSD from task-1 usage = 0.01
	if report.Totals.ExactCostUSD != 0.01 {
		t.Errorf("ExactCostUSD: got %.4f, want 0.0100", report.Totals.ExactCostUSD)
	}
	// EstimatedCostUSD from task-2 usage = 0.02
	if report.Totals.EstimatedCostUSD != 0.02 {
		t.Errorf("EstimatedCostUSD: got %.4f, want 0.0200", report.Totals.EstimatedCostUSD)
	}
}

func TestBuildReport_ZeroTasks(t *testing.T) {
	rr := RunResult{RunID: "empty-run", SuiteName: "smoke"}
	report := BuildReport(rr)
	if report.Totals.TotalTasks != 0 {
		t.Errorf("expected 0 total tasks, got %d", report.Totals.TotalTasks)
	}
	if report.Totals.RawSolveRate != 0 {
		t.Errorf("expected 0 raw solve rate for empty run, got %.4f", report.Totals.RawSolveRate)
	}
}

func TestRenderMarkdown_IncludesAllRequiredSections(t *testing.T) {
	rr := makeTestRunResult()
	report := BuildReport(rr)

	var buf bytes.Buffer
	if err := RenderMarkdown(&buf, report); err != nil {
		t.Fatalf("RenderMarkdown failed: %v", err)
	}

	md := buf.String()

	requiredSections := []string{
		"# Benchmark Report",
		"**Run ID:**",
		"**Suite:**",
		"**Mode:**",
		"## Profiles",
		"## Solve Rate",
		"Raw Solve Rate",
		"Flake-Adjusted Rate",
		"## Cost",
		"Exact Cost (USD)",
		"Estimated Cost (USD)",
		"## Per-Task Results",
		"Task ID",
		"system result", // ModeFullVerk claim label
	}

	for _, section := range requiredSections {
		if !strings.Contains(md, section) {
			t.Errorf("Markdown missing expected section/content: %q", section)
		}
	}
}

func TestRenderMarkdown_NonComparableLabel(t *testing.T) {
	rr := makeTestRunResult()
	rr.Labels = []string{"non-comparable"}
	report := BuildReport(rr)

	var buf bytes.Buffer
	if err := RenderMarkdown(&buf, report); err != nil {
		t.Fatalf("RenderMarkdown failed: %v", err)
	}

	md := buf.String()
	if !strings.Contains(md, "non-comparable") {
		t.Error("Markdown should include non-comparable warning")
	}
	if !strings.Contains(md, "Warning") {
		t.Error("Markdown should include warning text for non-comparable runs")
	}
}

func TestRenderMarkdown_ModeClaimLabels(t *testing.T) {
	modes := []struct {
		mode  BenchmarkMode
		claim string
	}{
		{ModeFullVerk, "system result"},
		{ModeWorkerOnly, "capability result"},
		{ModeRuntimeProbe, "runtime result"},
		{"custom", "exploratory"},
	}
	for _, tc := range modes {
		rr := RunResult{RunID: "r", SuiteName: "s", Mode: tc.mode}
		report := BuildReport(rr)
		var buf bytes.Buffer
		if err := RenderMarkdown(&buf, report); err != nil {
			t.Fatalf("RenderMarkdown(%s) failed: %v", tc.mode, err)
		}
		if !strings.Contains(buf.String(), tc.claim) {
			t.Errorf("mode %q: expected claim %q not found in output", tc.mode, tc.claim)
		}
	}
}

func TestRenderJSON_RoundTrips(t *testing.T) {
	rr := makeTestRunResult()
	report := BuildReport(rr)

	var buf bytes.Buffer
	if err := RenderJSON(&buf, report); err != nil {
		t.Fatalf("RenderJSON failed: %v", err)
	}

	var decoded Report
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("JSON decode failed: %v", err)
	}

	if decoded.RunID != report.RunID {
		t.Errorf("RunID: got %q, want %q", decoded.RunID, report.RunID)
	}
	if decoded.Totals.TotalTasks != report.Totals.TotalTasks {
		t.Errorf("Totals.TotalTasks: got %d, want %d", decoded.Totals.TotalTasks, report.Totals.TotalTasks)
	}
	if decoded.Totals.Solved != report.Totals.Solved {
		t.Errorf("Totals.Solved: got %d, want %d", decoded.Totals.Solved, report.Totals.Solved)
	}
	if len(decoded.PerTask) != len(report.PerTask) {
		t.Errorf("PerTask length: got %d, want %d", len(decoded.PerTask), len(report.PerTask))
	}
}

func TestRenderCSV_HasHeaderAndRows(t *testing.T) {
	rr := makeTestRunResult()
	report := BuildReport(rr)

	var buf bytes.Buffer
	if err := RenderCSV(&buf, report); err != nil {
		t.Fatalf("RenderCSV failed: %v", err)
	}

	r := csv.NewReader(&buf)
	records, err := r.ReadAll()
	if err != nil {
		t.Fatalf("csv.ReadAll failed: %v", err)
	}

	// Header row + 5 task rows
	if len(records) != 6 {
		t.Fatalf("expected 6 rows (1 header + 5 tasks), got %d", len(records))
	}

	header := records[0]
	expectedCols := []string{"task_id", "profile_id", "status", "failure", "repair_cycles", "review_cycles", "duration_ms", "cost_usd", "cost_confidence"}
	if len(header) != len(expectedCols) {
		t.Fatalf("header column count: got %d, want %d", len(header), len(expectedCols))
	}
	for i, col := range expectedCols {
		if header[i] != col {
			t.Errorf("header[%d]: got %q, want %q", i, header[i], col)
		}
	}

	// Verify first data row corresponds to task-1.
	row1 := records[1]
	if row1[0] != "task-1" {
		t.Errorf("row1 task_id: got %q, want %q", row1[0], "task-1")
	}
	if row1[2] != "solved" {
		t.Errorf("row1 status: got %q, want %q", row1[2], "solved")
	}
	// task-1 has confidence="exact" and cost=0.01
	if row1[8] != "exact" {
		t.Errorf("row1 cost_confidence: got %q, want %q", row1[8], "exact")
	}

	// task-3 is unsolved with no usage — confidence should be "unavailable"
	row3 := records[3]
	if row3[0] != "task-3" {
		t.Errorf("row3 task_id: got %q, want %q", row3[0], "task-3")
	}
	if row3[8] != "unavailable" {
		t.Errorf("row3 cost_confidence: got %q, want %q", row3[8], "unavailable")
	}
}

func TestRenderMarkdown_IncludesFailureCategorySection(t *testing.T) {
	rr := makeTestRunResult()
	// Add a task with an explicit failure category.
	rr.Results = append(rr.Results, TaskResult{
		TaskID:     "task-6",
		ProfileID:  "p1",
		Status:     TaskStatusUnsolved,
		Failure:    FailureModelLimit,
		DurationMS: 500,
	})
	report := BuildReport(rr)

	var buf bytes.Buffer
	if err := RenderMarkdown(&buf, report); err != nil {
		t.Fatalf("RenderMarkdown failed: %v", err)
	}

	md := buf.String()
	if !strings.Contains(md, "## Failure Categories") {
		t.Error("Markdown missing '## Failure Categories' section")
	}
	if !strings.Contains(md, string(FailureModelLimit)) {
		t.Errorf("Markdown missing failure category %q", FailureModelLimit)
	}
}

func TestRenderMarkdown_IncludesPerProfileTable(t *testing.T) {
	rr := makeTestRunResult()
	report := BuildReport(rr)

	var buf bytes.Buffer
	if err := RenderMarkdown(&buf, report); err != nil {
		t.Fatalf("RenderMarkdown failed: %v", err)
	}

	md := buf.String()
	if !strings.Contains(md, "## Per-Profile Breakdown") {
		t.Error("Markdown missing '## Per-Profile Breakdown' section")
	}
	if !strings.Contains(md, "Profile") {
		t.Error("Markdown per-profile table missing 'Profile' column header")
	}
	if !strings.Contains(md, "Solve %") {
		t.Error("Markdown per-profile table missing 'Solve %' column header")
	}
	// Profile p1 should appear in the table.
	if !strings.Contains(md, "p1") {
		t.Error("Markdown per-profile table missing profile 'p1'")
	}
}
