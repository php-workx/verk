package bench

import (
	"bytes"
	"strings"
	"testing"
)

func makeBaselineRun() RunResult {
	return RunResult{
		RunID:     "run-baseline-001",
		SuiteName: "smoke",
		Mode:      ModeFullVerk,
		Manifest: LockedTaskManifest{
			Suite:   "smoke",
			TaskIDs: []string{"task-1", "task-2", "task-3"},
		},
		Results: []TaskResult{
			{TaskID: "task-1", ProfileID: "p1", Status: TaskStatusSolved},
			{TaskID: "task-2", ProfileID: "p1", Status: TaskStatusSolved},
			{TaskID: "task-3", ProfileID: "p1", Status: TaskStatusUnsolved},
		},
	}
}

func makeCandidateRun() RunResult {
	return RunResult{
		RunID:     "run-candidate-002",
		SuiteName: "smoke",
		Mode:      ModeFullVerk,
		Manifest: LockedTaskManifest{
			Suite:   "smoke",
			TaskIDs: []string{"task-1", "task-2", "task-3"},
		},
		Results: []TaskResult{
			{TaskID: "task-1", ProfileID: "p1", Status: TaskStatusSolved},
			{TaskID: "task-2", ProfileID: "p1", Status: TaskStatusUnsolved}, // regressed
			{TaskID: "task-3", ProfileID: "p1", Status: TaskStatusSolved},   // improved
		},
	}
}

func TestCompare_PairsByTaskID(t *testing.T) {
	base := makeBaselineRun()
	cand := makeCandidateRun()

	cmp := Compare(base, cand)

	if cmp.Refusal != "" {
		t.Fatalf("unexpected refusal: %s", cmp.Refusal)
	}
	if len(cmp.Pairs) != 3 {
		t.Fatalf("expected 3 pairs, got %d", len(cmp.Pairs))
	}

	// Build a map for easy lookup.
	byID := make(map[string]PairedTaskComparison)
	for _, p := range cmp.Pairs {
		byID[p.TaskID] = p
	}

	if byID["task-1"].Outcome != "unchanged" {
		t.Errorf("task-1: expected unchanged, got %q", byID["task-1"].Outcome)
	}
	if byID["task-2"].Outcome != "regressed" {
		t.Errorf("task-2: expected regressed, got %q", byID["task-2"].Outcome)
	}
	if byID["task-3"].Outcome != "improved" {
		t.Errorf("task-3: expected improved, got %q", byID["task-3"].Outcome)
	}
}

func TestCompare_DiffCountsAreCorrect(t *testing.T) {
	base := makeBaselineRun()
	cand := makeCandidateRun()

	cmp := Compare(base, cand)
	if cmp.Refusal != "" {
		t.Fatalf("unexpected refusal: %s", cmp.Refusal)
	}

	if cmp.Diff.UnchangedCount != 1 {
		t.Errorf("UnchangedCount: got %d, want 1", cmp.Diff.UnchangedCount)
	}
	if cmp.Diff.RegressedCount != 1 {
		t.Errorf("RegressedCount: got %d, want 1", cmp.Diff.RegressedCount)
	}
	if cmp.Diff.ImprovedCount != 1 {
		t.Errorf("ImprovedCount: got %d, want 1", cmp.Diff.ImprovedCount)
	}
	if cmp.Diff.MissingCount != 0 {
		t.Errorf("MissingCount: got %d, want 0", cmp.Diff.MissingCount)
	}
}

func TestCompare_RefusesDifferentSuites(t *testing.T) {
	base := makeBaselineRun()
	cand := makeCandidateRun()
	cand.SuiteName = "regression" // different suite

	cmp := Compare(base, cand)
	if cmp.Refusal == "" {
		t.Fatal("expected refusal for different suites, got none")
	}
	if !strings.Contains(cmp.Refusal, "suite") {
		t.Errorf("refusal message should mention 'suite', got: %q", cmp.Refusal)
	}
}

func TestCompare_RefusesDifferentManifests(t *testing.T) {
	base := makeBaselineRun()
	cand := makeCandidateRun()
	cand.Manifest.TaskIDs = []string{"task-1", "task-2", "task-4"} // different tasks

	cmp := Compare(base, cand)
	if cmp.Refusal == "" {
		t.Fatal("expected refusal for different manifests, got none")
	}
	if !strings.Contains(cmp.Refusal, "manifest") {
		t.Errorf("refusal message should mention 'manifest', got: %q", cmp.Refusal)
	}
}

func TestCompare_RefusesMissingCompleteMarker(t *testing.T) {
	base := makeBaselineRun()
	cand := makeCandidateRun()
	cand.Labels = []string{"incomplete"} // missing complete marker

	cmp := Compare(base, cand)
	if cmp.Refusal == "" {
		t.Fatal("expected refusal for missing complete marker, got none")
	}
	if !strings.Contains(cmp.Refusal, "complete") {
		t.Errorf("refusal message should mention 'complete', got: %q", cmp.Refusal)
	}
}

func TestCompare_LabelsNonComparable(t *testing.T) {
	base := makeBaselineRun()
	cand := makeCandidateRun()
	base.Labels = []string{"non-comparable"}

	cmp := Compare(base, cand)
	if cmp.Refusal == "" {
		t.Fatal("expected refusal for non-comparable baseline, got none")
	}
	if !strings.Contains(cmp.Refusal, "non-comparable") {
		t.Errorf("refusal message should mention 'non-comparable', got: %q", cmp.Refusal)
	}
}

func TestCompare_CandidateNonComparable(t *testing.T) {
	base := makeBaselineRun()
	cand := makeCandidateRun()
	cand.Labels = []string{"non-comparable"}

	cmp := Compare(base, cand)
	if cmp.Refusal == "" {
		t.Fatal("expected refusal for non-comparable candidate, got none")
	}
}

func TestCompare_MissingTaskInCandidate(t *testing.T) {
	base := makeBaselineRun()
	cand := makeCandidateRun()
	// Remove task-3 from candidate results but keep same manifest.
	cand.Results = cand.Results[:2]

	cmp := Compare(base, cand)
	if cmp.Refusal != "" {
		t.Fatalf("unexpected refusal: %s", cmp.Refusal)
	}

	byID := make(map[string]PairedTaskComparison)
	for _, p := range cmp.Pairs {
		byID[p.TaskID] = p
	}

	if byID["task-3"].Outcome != "missing" {
		t.Errorf("task-3: expected missing, got %q", byID["task-3"].Outcome)
	}
	if cmp.Diff.MissingCount != 1 {
		t.Errorf("MissingCount: got %d, want 1", cmp.Diff.MissingCount)
	}
}

func TestRenderComparisonMarkdown_IncludesRequiredContent(t *testing.T) {
	base := makeBaselineRun()
	cand := makeCandidateRun()
	cmp := Compare(base, cand)

	var buf bytes.Buffer
	if err := RenderComparisonMarkdown(&buf, cmp); err != nil {
		t.Fatalf("RenderComparisonMarkdown failed: %v", err)
	}

	md := buf.String()
	required := []string{
		"# Benchmark Comparison",
		"**Baseline:**",
		"**Candidate:**",
		"## Summary",
		"Improved",
		"Regressed",
		"## Per-Task Comparison",
		"Task ID",
		"Outcome",
	}
	for _, s := range required {
		if !strings.Contains(md, s) {
			t.Errorf("comparison markdown missing: %q", s)
		}
	}
}
