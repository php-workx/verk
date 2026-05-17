package bench

import (
	"encoding/json"
	"testing"
	"time"
	"verk/internal/state"
)

func TestSuiteMeta_RoundTrip(t *testing.T) {
	orig := SuiteMeta{
		Name:         "polyglot-v2",
		Provider:     "aider",
		Description:  "Multi-language benchmark",
		TaskCount:    133,
		SamplingMode: "regression",
		Labels:       map[string]string{"tier": "core"},
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got SuiteMeta
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Name != orig.Name || got.Provider != orig.Provider || got.TaskCount != orig.TaskCount {
		t.Errorf("round-trip mismatch: got %+v", got)
	}
}

func TestSuiteMeta_ZeroValue(t *testing.T) {
	var s SuiteMeta
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal zero value: %v", err)
	}
	var got SuiteMeta
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal zero value: %v", err)
	}
}

func TestMatrixProfile_RoundTrip(t *testing.T) {
	orig := MatrixProfile{
		ID:       "p1",
		Worker:   ModelRef{Runtime: "claude-code", Model: "claude-sonnet-4-6"},
		Reviewer: ModelRef{Runtime: "claude-code", Model: "claude-opus-4"},
		Fallback: ModelRef{Runtime: "claude-code", Model: "claude-haiku-3-5"},
		Notes:    "primary profile",
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got MatrixProfile
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ID != orig.ID || got.Worker.Model != orig.Worker.Model {
		t.Errorf("round-trip mismatch: got %+v", got)
	}
}

func TestMatrix_RoundTrip(t *testing.T) {
	orig := Matrix{
		Mode: ModeFullVerk,
		Profiles: []MatrixProfile{
			{ID: "p1", Worker: ModelRef{Runtime: "r", Model: "m"}},
		},
		FallbackPolicy:   "strict",
		ComparisonDesign: "fixed-reviewer",
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Matrix
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Mode != orig.Mode || len(got.Profiles) != 1 {
		t.Errorf("round-trip mismatch: got %+v", got)
	}
}

func TestLockedTaskManifest_RoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	orig := LockedTaskManifest{
		Suite:     "polyglot",
		LockedAt:  now,
		TaskIDs:   []string{"t1", "t2", "t3"},
		SourceRef: "main@abc123",
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got LockedTaskManifest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Suite != orig.Suite || len(got.TaskIDs) != 3 {
		t.Errorf("round-trip mismatch: got %+v", got)
	}
	if !got.LockedAt.Equal(orig.LockedAt) {
		t.Errorf("time mismatch: got %v want %v", got.LockedAt, orig.LockedAt)
	}
}

func TestResolvedProfileSnapshot_RoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	orig := ResolvedProfileSnapshot{
		MatrixProfile: MatrixProfile{
			ID:     "p1",
			Worker: ModelRef{Runtime: "r", Model: "m"},
		},
		ResolvedAt: now,
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got ResolvedProfileSnapshot
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ID != orig.ID || !got.ResolvedAt.Equal(orig.ResolvedAt) {
		t.Errorf("round-trip mismatch: got %+v", got)
	}
}

func TestUsageRecord_RoundTrip(t *testing.T) {
	orig := UsageRecord{
		Role:           "worker",
		Runtime:        "claude-code",
		Model:          "claude-sonnet-4-6",
		InputTokens:    1000,
		OutputTokens:   500,
		CachedTokens:   200,
		CostUSD:        0.015,
		Confidence:     "exact",
		PricingVersion: "2025-01",
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got UsageRecord
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Role != orig.Role || got.InputTokens != orig.InputTokens {
		t.Errorf("round-trip mismatch: got %+v", got)
	}
}

func TestTaskResult_RoundTrip(t *testing.T) {
	orig := TaskResult{
		TaskID:       "task-001",
		ProfileID:    "p1",
		Status:       TaskStatusSolved,
		Score:        Score{Solved: true, CostUSD: 0.02},
		RepairCycles: 1,
		ReviewCycles: 2,
		DurationMS:   45000,
		Usage: []UsageRecord{
			{Role: "worker", Runtime: "r", Model: "m", InputTokens: 100, OutputTokens: 50},
		},
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got TaskResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.TaskID != orig.TaskID || got.Status != orig.Status {
		t.Errorf("round-trip mismatch: got %+v", got)
	}
}

func TestScore_RoundTrip(t *testing.T) {
	orig := Score{
		Solved: true,
		TokenUsage: state.RuntimeTokenUsage{
			InputTokens:       1000,
			CachedInputTokens: 200,
			OutputTokens:      500,
			TotalTokens:       1700,
		},
		CostUSD:    0.03,
		Confidence: "estimated",
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Score
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Solved != orig.Solved || got.TokenUsage.InputTokens != orig.TokenUsage.InputTokens {
		t.Errorf("round-trip mismatch: got %+v", got)
	}
}

func TestRunCheckpoint_RoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	orig := RunCheckpoint{
		RunID:     "run-abc",
		UpdatedAt: now,
		SuiteName: "polyglot",
		Matrix: Matrix{
			Mode:             ModeWorkerOnly,
			ComparisonDesign: "exploratory",
		},
		LockedManifest: LockedTaskManifest{
			Suite:    "polyglot",
			LockedAt: now,
			TaskIDs:  []string{"t1"},
		},
		CompletedTasks: []string{"t1"},
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got RunCheckpoint
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.RunID != orig.RunID || len(got.CompletedTasks) != 1 {
		t.Errorf("round-trip mismatch: got %+v", got)
	}
}

func TestCompleteMarker_RoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	orig := CompleteMarker{
		RunID:       "run-abc",
		CompletedAt: now,
		ReportPaths: []string{"/tmp/report.json"},
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got CompleteMarker
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.RunID != orig.RunID || len(got.ReportPaths) != 1 {
		t.Errorf("round-trip mismatch: got %+v", got)
	}
}
