package bench

import (
	"time"
	"verk/internal/state"
)

// SuiteMeta describes a benchmark suite.
type SuiteMeta struct {
	Name         string            `json:"name"`
	Provider     string            `json:"provider"`
	Description  string            `json:"description,omitempty"`
	TaskCount    int               `json:"task_count"`
	SamplingMode string            `json:"sampling_mode,omitempty"` // smoke|regression|holdout|public
	Labels       map[string]string `json:"labels,omitempty"`
}

// BenchmarkMode controls which dimension the matrix varies.
type BenchmarkMode string

const (
	ModeFullVerk     BenchmarkMode = "full-verk"     // headline system result
	ModeWorkerOnly   BenchmarkMode = "worker-only"   // bypass review/repair
	ModeRuntimeProbe BenchmarkMode = "runtime-probe" // adapter reliability
)

// MatrixProfile is one (worker, reviewer) pairing in the benchmark matrix.
type MatrixProfile struct {
	ID       string   `json:"id"                 yaml:"id"`
	Worker   ModelRef `json:"worker"             yaml:"worker"`
	Reviewer ModelRef `json:"reviewer,omitempty" yaml:"reviewer"`
	Fallback ModelRef `json:"fallback,omitempty" yaml:"fallback"`
	Notes    string   `json:"notes,omitempty"    yaml:"notes"`
}

// ModelRef identifies a model and runtime.
type ModelRef struct {
	Runtime   string `json:"runtime"             yaml:"runtime"`
	Model     string `json:"model"               yaml:"model"`
	Reasoning string `json:"reasoning,omitempty" yaml:"reasoning"`
}

// Matrix is the full benchmark execution plan.
type Matrix struct {
	Mode             BenchmarkMode   `json:"mode"                       yaml:"mode"`
	Profiles         []MatrixProfile `json:"profiles"                   yaml:"profiles"`
	FallbackPolicy   string          `json:"fallback_policy,omitempty"  yaml:"fallback_policy"`   // strict|allow|sticky
	ComparisonDesign string          `json:"comparison_design"          yaml:"comparison_design"` // fixed-reviewer|full-factorial|exploratory
}

// LockedTaskManifest records exactly which tasks were locked at execution time.
type LockedTaskManifest struct {
	Suite     string    `json:"suite"`
	LockedAt  time.Time `json:"locked_at"`
	TaskIDs   []string  `json:"task_ids"`
	SourceRef string    `json:"source_ref,omitempty"`
}

// ResolvedProfileSnapshot freezes the (worker, reviewer, fallback) chain
// for a benchmark execution so reports can be reproduced.
type ResolvedProfileSnapshot struct {
	MatrixProfile
	ResolvedAt time.Time `json:"resolved_at"`
}

// UsageRecord captures token/cost usage for one model call.
type UsageRecord struct {
	Role           string  `json:"role"` // worker|reviewer|repair
	Runtime        string  `json:"runtime"`
	Model          string  `json:"model"`
	InputTokens    int     `json:"input_tokens"`
	OutputTokens   int     `json:"output_tokens"`
	CachedTokens   int     `json:"cached_tokens,omitempty"`
	CostUSD        float64 `json:"cost_usd,omitempty"`
	Confidence     string  `json:"confidence,omitempty"` // exact|estimated|derived
	PricingVersion string  `json:"pricing_version,omitempty"`
}

// TaskResultStatus is the outcome of one benchmark task.
type TaskResultStatus string

const (
	TaskStatusSolved        TaskResultStatus = "solved"
	TaskStatusUnsolved      TaskResultStatus = "unsolved"
	TaskStatusVerifierFlaky TaskResultStatus = "verifier_flaky"
	TaskStatusBlocked       TaskResultStatus = "blocked"
	TaskStatusCancelled     TaskResultStatus = "cancelled"
)

// FailureCategory groups failures into a coarse taxonomy.
type FailureCategory string

const (
	FailureModelLimit     FailureCategory = "model_limit"
	FailureWorkerCrash    FailureCategory = "worker_crash"
	FailureReviewerBlock  FailureCategory = "reviewer_block"
	FailureScopeViolation FailureCategory = "scope_violation"
	FailureVerifier       FailureCategory = "verifier"
	FailureSetup          FailureCategory = "setup"
	FailureOther          FailureCategory = "other"
)

// TaskResult is the per-task outcome.
type TaskResult struct {
	TaskID       string           `json:"task_id"`
	ProfileID    string           `json:"profile_id"`
	Status       TaskResultStatus `json:"status"`
	Failure      FailureCategory  `json:"failure,omitempty"`
	Score        Score            `json:"score"`
	RepairCycles int              `json:"repair_cycles,omitempty"`
	ReviewCycles int              `json:"review_cycles,omitempty"`
	DurationMS   int64            `json:"duration_ms"`
	Usage        []UsageRecord    `json:"usage,omitempty"`
	Artifacts    []string         `json:"artifacts,omitempty"`
	Notes        string           `json:"notes,omitempty"`
}

// Score is the headline numeric score for a task.
type Score struct {
	Solved     bool                    `json:"solved"`
	TokenUsage state.RuntimeTokenUsage `json:"token_usage,omitempty"`
	CostUSD    float64                 `json:"cost_usd,omitempty"`
	Confidence string                  `json:"confidence,omitempty"`
}

// RunCheckpoint records partial run state for resumption.
type RunCheckpoint struct {
	RunID          string             `json:"run_id"`
	UpdatedAt      time.Time          `json:"updated_at"`
	SuiteName      string             `json:"suite_name"`
	Matrix         Matrix             `json:"matrix"`
	LockedManifest LockedTaskManifest `json:"locked_manifest"`
	CompletedTasks []string           `json:"completed_tasks,omitempty"`
}

// CompleteMarker is written ONLY after reports are persisted.
type CompleteMarker struct {
	RunID       string    `json:"run_id"`
	CompletedAt time.Time `json:"completed_at"`
	ReportPaths []string  `json:"report_paths"`
}
