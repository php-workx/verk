package runtime

import (
	"context"
	"fmt"
	"time"

	"verk/internal/state"
)

type WorkerStatus string
type ReviewStatus string
type ReviewDisposition string

type RetryClass = state.RetryClass
type Severity = state.Severity

const (
	WorkerStatusDone             WorkerStatus = "done"
	WorkerStatusDoneWithConcerns WorkerStatus = "done_with_concerns"
	WorkerStatusNeedsContext     WorkerStatus = "needs_context"
	WorkerStatusBlocked          WorkerStatus = "blocked"
)

const (
	RetryClassRetryable              RetryClass = state.RetryClassRetryable
	RetryClassTerminal               RetryClass = state.RetryClassTerminal
	RetryClassBlockedByOperatorInput RetryClass = state.RetryClassBlockedByOperatorInput
)

const (
	SeverityP0 Severity = state.SeverityP0
	SeverityP1 Severity = state.SeverityP1
	SeverityP2 Severity = state.SeverityP2
	SeverityP3 Severity = state.SeverityP3
	SeverityP4 Severity = state.SeverityP4
)

const (
	ReviewStatusPassed   ReviewStatus = "passed"
	ReviewStatusFindings ReviewStatus = "findings"
)

const (
	ReviewDispositionOpen     ReviewDisposition = "open"
	ReviewDispositionResolved ReviewDisposition = "resolved"
	ReviewDispositionWaived   ReviewDisposition = "waived"
)

type WorkerRequest struct {
	RunID             string          `json:"run_id,omitempty"`
	TicketID          string          `json:"ticket_id,omitempty"`
	WaveID            string          `json:"wave_id,omitempty"`
	LeaseID           string          `json:"lease_id"`
	Attempt           int             `json:"attempt,omitempty"`
	Runtime           string          `json:"runtime,omitempty"`
	WorktreePath      string          `json:"worktree_path,omitempty"`
	InputArtifactPath string          `json:"input_artifact_path,omitempty"`
	Instructions      string          `json:"instructions,omitempty"`
	ExecutionConfig   ExecutionConfig `json:"execution_config,omitempty"`
}

type ReviewRequest struct {
	RunID                    string          `json:"run_id,omitempty"`
	TicketID                 string          `json:"ticket_id,omitempty"`
	WaveID                   string          `json:"wave_id,omitempty"`
	LeaseID                  string          `json:"lease_id"`
	Attempt                  int             `json:"attempt,omitempty"`
	Runtime                  string          `json:"runtime,omitempty"`
	InputArtifactPath        string          `json:"input_artifact_path,omitempty"`
	Instructions             string          `json:"instructions,omitempty"`
	Diff                     string          `json:"diff,omitempty"`
	EffectiveReviewThreshold Severity        `json:"effective_review_threshold"`
	ExecutionConfig          ExecutionConfig `json:"execution_config,omitempty"`
}

type ExecutionConfig struct {
	WorkerTimeoutMinutes   int      `json:"worker_timeout_minutes,omitempty"`
	ReviewerTimeoutMinutes int      `json:"reviewer_timeout_minutes,omitempty"`
	AuthEnvVars            []string `json:"auth_env_vars,omitempty"`
}

type WorkerResult struct {
	Status             WorkerStatus `json:"status"`
	CompletionCode     string       `json:"completion_code,omitempty"`
	Concerns           []string     `json:"concerns,omitempty"`
	BlockReason        string       `json:"block_reason,omitempty"`
	RetryClass         RetryClass   `json:"retry_class"`
	StdoutPath         string       `json:"stdout_path,omitempty"`
	StderrPath         string       `json:"stderr_path,omitempty"`
	ResultArtifactPath string       `json:"result_artifact_path,omitempty"`
	LeaseID            string       `json:"lease_id"`
	StartedAt          time.Time    `json:"started_at"`
	FinishedAt         time.Time    `json:"finished_at"`
}

type ReviewFinding struct {
	ID              string            `json:"id"`
	Severity        Severity          `json:"severity"`
	Title           string            `json:"title"`
	Body            string            `json:"body"`
	File            string            `json:"file"`
	Line            int               `json:"line"`
	Disposition     ReviewDisposition `json:"disposition"`
	WaivedBy        string            `json:"waived_by,omitempty"`
	WaivedAt        time.Time         `json:"waived_at,omitempty"`
	WaiverReason    string            `json:"waiver_reason,omitempty"`
	WaiverExpiresAt *time.Time        `json:"waiver_expires_at,omitempty"`
}

type ReviewResult struct {
	Status             WorkerStatus    `json:"status"`
	CompletionCode     string          `json:"completion_code,omitempty"`
	RetryClass         RetryClass      `json:"retry_class"`
	StdoutPath         string          `json:"stdout_path,omitempty"`
	StderrPath         string          `json:"stderr_path,omitempty"`
	ResultArtifactPath string          `json:"result_artifact_path,omitempty"`
	LeaseID            string          `json:"lease_id"`
	StartedAt          time.Time       `json:"started_at"`
	FinishedAt         time.Time       `json:"finished_at"`
	ReviewStatus       ReviewStatus    `json:"review_status"`
	Summary            string          `json:"summary"`
	Findings           []ReviewFinding `json:"findings"`
}

type Adapter interface {
	RunWorker(ctx context.Context, req WorkerRequest) (WorkerResult, error)
	RunReviewer(ctx context.Context, req ReviewRequest) (ReviewResult, error)
}

var severityOrder = map[Severity]int{
	SeverityP0: 0,
	SeverityP1: 1,
	SeverityP2: 2,
	SeverityP3: 3,
	SeverityP4: 4,
}

func ValidateWorkerStatus(status WorkerStatus) error {
	switch status {
	case WorkerStatusDone, WorkerStatusDoneWithConcerns, WorkerStatusNeedsContext, WorkerStatusBlocked:
		return nil
	default:
		return fmt.Errorf("invalid worker status %q", status)
	}
}

func ValidateRetryClass(class RetryClass) error {
	switch class {
	case RetryClassRetryable, RetryClassTerminal, RetryClassBlockedByOperatorInput:
		return nil
	default:
		return fmt.Errorf("invalid retry class %q", class)
	}
}

func ValidateSeverity(severity Severity) error {
	if _, ok := severityOrder[severity]; ok {
		return nil
	}
	return fmt.Errorf("invalid severity %q", severity)
}

func ValidateReviewStatus(status ReviewStatus) error {
	switch status {
	case ReviewStatusPassed, ReviewStatusFindings:
		return nil
	default:
		return fmt.Errorf("invalid review status %q", status)
	}
}

func ValidateReviewDisposition(disposition ReviewDisposition) error {
	switch disposition {
	case ReviewDispositionOpen, ReviewDispositionResolved, ReviewDispositionWaived:
		return nil
	default:
		return fmt.Errorf("invalid review disposition %q", disposition)
	}
}

func (r WorkerResult) Validate() error {
	if err := ValidateWorkerStatus(r.Status); err != nil {
		return err
	}
	if err := ValidateRetryClass(r.RetryClass); err != nil {
		return err
	}
	if r.LeaseID == "" {
		return fmt.Errorf("worker result missing lease_id")
	}
	if r.StartedAt.IsZero() {
		return fmt.Errorf("worker result missing started_at")
	}
	if r.FinishedAt.IsZero() {
		return fmt.Errorf("worker result missing finished_at")
	}
	if r.FinishedAt.Before(r.StartedAt) {
		return fmt.Errorf("worker result finished_at precedes started_at")
	}
	if r.StdoutPath == "" && r.StderrPath == "" && r.ResultArtifactPath == "" {
		return fmt.Errorf("worker result must include stdout_path, stderr_path, or result_artifact_path")
	}
	return nil
}

func (r ReviewFinding) Validate() error {
	if r.ID == "" {
		return fmt.Errorf("review finding missing id")
	}
	if err := ValidateSeverity(r.Severity); err != nil {
		return err
	}
	if r.Title == "" {
		return fmt.Errorf("review finding missing title")
	}
	if r.Body == "" {
		return fmt.Errorf("review finding missing body")
	}
	if r.File == "" {
		return fmt.Errorf("review finding missing file")
	}
	if r.Line <= 0 {
		return fmt.Errorf("review finding missing line")
	}
	if err := ValidateReviewDisposition(r.Disposition); err != nil {
		return err
	}
	if r.Disposition != ReviewDispositionWaived {
		return nil
	}
	if r.WaivedBy == "" {
		return fmt.Errorf("waived review finding missing waived_by")
	}
	if r.WaivedAt.IsZero() {
		return fmt.Errorf("waived review finding missing waived_at")
	}
	if r.WaiverReason == "" {
		return fmt.Errorf("waived review finding missing waiver_reason")
	}
	return nil
}

func (r ReviewResult) Validate(threshold Severity) error {
	if err := ValidateWorkerStatus(r.Status); err != nil {
		return err
	}
	if err := ValidateRetryClass(r.RetryClass); err != nil {
		return err
	}
	if r.LeaseID == "" {
		return fmt.Errorf("review result missing lease_id")
	}
	if r.StartedAt.IsZero() {
		return fmt.Errorf("review result missing started_at")
	}
	if r.FinishedAt.IsZero() {
		return fmt.Errorf("review result missing finished_at")
	}
	if r.FinishedAt.Before(r.StartedAt) {
		return fmt.Errorf("review result finished_at precedes started_at")
	}
	if r.StdoutPath == "" && r.StderrPath == "" && r.ResultArtifactPath == "" {
		return fmt.Errorf("review result must include stdout_path, stderr_path, or result_artifact_path")
	}
	if err := ValidateReviewStatus(r.ReviewStatus); err != nil {
		return err
	}
	if err := ValidateSeverity(threshold); err != nil {
		return err
	}
	for _, finding := range r.Findings {
		if err := finding.Validate(); err != nil {
			return err
		}
	}
	if r.ReviewStatus != r.DerivedReviewStatus(threshold) {
		return fmt.Errorf("review status %q contradicts derived status %q", r.ReviewStatus, r.DerivedReviewStatus(threshold))
	}
	return nil
}

func (r ReviewResult) DerivedReviewStatus(threshold Severity) ReviewStatus {
	if !isCanonicalSeverity(threshold) {
		return ReviewStatusFindings
	}
	for _, finding := range r.Findings {
		if isBlockingFinding(finding, threshold) {
			return ReviewStatusFindings
		}
	}
	return ReviewStatusPassed
}

func isCanonicalSeverity(severity Severity) bool {
	_, ok := severityOrder[severity]
	return ok
}

func isBlockingFinding(finding ReviewFinding, threshold Severity) bool {
	if finding.Disposition != ReviewDispositionOpen {
		return false
	}
	findingRank := severityOrder[finding.Severity]
	thresholdRank := severityOrder[threshold]
	return findingRank <= thresholdRank
}
