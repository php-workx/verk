package state

import "time"

type (
	TicketPhase   string
	EpicRunStatus string
	WaveStatus    string
	RetryClass    string
	Severity      string
)

const (
	TicketPhaseIntake    TicketPhase = "intake"
	TicketPhaseImplement TicketPhase = "implement"
	TicketPhaseVerify    TicketPhase = "verify"
	TicketPhaseReview    TicketPhase = "review"
	TicketPhaseRepair    TicketPhase = "repair"
	TicketPhaseCloseout  TicketPhase = "closeout"
	TicketPhaseClosed    TicketPhase = "closed"
	TicketPhaseBlocked   TicketPhase = "blocked"
)

const (
	EpicRunStatusRunning         EpicRunStatus = "running"
	EpicRunStatusWaitingOnLeases EpicRunStatus = "waiting_on_leases"
	EpicRunStatusBlocked         EpicRunStatus = "blocked"
	EpicRunStatusCompleted       EpicRunStatus = "completed"
)

const (
	WaveStatusPlanned        WaveStatus = "planned"
	WaveStatusRunning        WaveStatus = "running"
	WaveStatusAccepted       WaveStatus = "accepted"
	WaveStatusFailed         WaveStatus = "failed"
	WaveStatusFailedReopened WaveStatus = "failed_reopened"
)

const (
	RetryClassRetryable              RetryClass = "retryable"
	RetryClassTerminal               RetryClass = "terminal"
	RetryClassBlockedByOperatorInput RetryClass = "blocked_by_operator_input"
)

const (
	SeverityP0 Severity = "P0"
	SeverityP1 Severity = "P1"
	SeverityP2 Severity = "P2"
	SeverityP3 Severity = "P3"
	SeverityP4 Severity = "P4"
)

// Canonical escalation reason prefixes for non-convergent ticket blocking.
const (
	EscalationNonConvergentVerification = "non_convergent_verification"
	EscalationNonConvergentReview       = "non_convergent_review"
)

type ArtifactMeta struct {
	SchemaVersion int       `json:"schema_version"`
	RunID         string    `json:"run_id"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type AuditEvent struct {
	At       time.Time      `json:"at"`
	Type     string         `json:"type"`
	TicketID string         `json:"ticket_id,omitempty"`
	Phase    TicketPhase    `json:"phase,omitempty"`
	Details  map[string]any `json:"details,omitempty"`
}

type RunArtifact struct {
	ArtifactMeta
	Mode         string         `json:"mode"`
	RootTicketID string         `json:"root_ticket_id"`
	Status       EpicRunStatus  `json:"status"`
	CurrentPhase TicketPhase    `json:"current_phase"`
	Policy       map[string]any `json:"policy"`
	Config       map[string]any `json:"config"`
	WaveIDs      []string       `json:"wave_ids"`
	TicketIDs    []string       `json:"ticket_ids"`
	BaseBranch   string         `json:"base_branch"`
	BaseCommit   string         `json:"base_commit"`
	ResumeCursor map[string]any `json:"resume_cursor"`
	AuditEvents  []AuditEvent   `json:"audit_events"`
}

type WaveArtifact struct {
	ArtifactMeta
	WaveID         string         `json:"wave_id"`
	ParentTicketID string         `json:"parent_ticket_id,omitempty"` // non-empty for sub-epic waves
	Ordinal        int            `json:"ordinal"`
	Status         WaveStatus     `json:"status"`
	TicketIDs      []string       `json:"ticket_ids"`
	PlannedScope   []string       `json:"planned_scope"`
	ActualScope    []string       `json:"actual_scope"`
	Acceptance     map[string]any `json:"acceptance"`
	WaveBaseCommit string         `json:"wave_base_commit"`
	StartedAt      time.Time      `json:"started_at"`
	FinishedAt     time.Time      `json:"finished_at"`
	// ValidationCoverage captures wave-level declared/derived/executed
	// checks and merged-state repair routing. Optional so older wave
	// artifacts unmarshal unchanged.
	ValidationCoverage *ValidationCoverageArtifact `json:"validation_coverage,omitempty"`
}

type PlanArtifact struct {
	ArtifactMeta
	TicketID                 string          `json:"ticket_id"`
	Title                    string          `json:"title,omitempty"`
	Description              string          `json:"description,omitempty"`
	Phase                    TicketPhase     `json:"phase"`
	AcceptanceCriteria       []string        `json:"acceptance_criteria"`
	Criteria                 []PlanCriterion `json:"criteria,omitempty"`
	TestCases                []string        `json:"test_cases"`
	ValidationCommands       []string        `json:"validation_commands"`
	DeclaredChecks           []string        `json:"declared_checks,omitempty"`
	OwnedPaths               []string        `json:"owned_paths"`
	ReviewThreshold          Severity        `json:"review_threshold"`
	EffectiveReviewThreshold Severity        `json:"effective_review_threshold"`
	RuntimePreference        string          `json:"runtime_preference"`
	// WorkerProfile and ReviewerProfile snapshot the effective role profiles
	// (runtime/model/reasoning) as of intake. They are informational only:
	// retry and resume always re-resolve profiles from the current config,
	// so updating config between runs takes effect immediately. The snapshot
	// is preserved so later audits can compare the profile planned at intake
	// against the profiles actually used by each worker/reviewer attempt.
	WorkerProfile   *RoleProfileSnapshot `json:"worker_profile,omitempty"`
	ReviewerProfile *RoleProfileSnapshot `json:"reviewer_profile,omitempty"`
}

// RoleProfileSnapshot is a JSON-encodable copy of a runtime role profile
// (worker or reviewer) at a specific moment. The state package owns this
// type rather than importing policy to avoid a circular dependency.
type RoleProfileSnapshot struct {
	Runtime   string `json:"runtime"`
	Model     string `json:"model,omitempty"`
	Reasoning string `json:"reasoning,omitempty"`
}

type PlanCriterion struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type ImplementationArtifact struct {
	ArtifactMeta
	TicketID          string                `json:"ticket_id"`
	Attempt           int                   `json:"attempt"`
	Runtime           string                `json:"runtime"`
	Model             string                `json:"model,omitempty"`
	Reasoning         string                `json:"reasoning,omitempty"`
	FallbackReason    string                `json:"fallback_reason,omitempty"`
	Status            string                `json:"status"`
	CompletionCode    string                `json:"completion_code"`
	RetryClass        RetryClass            `json:"retry_class"`
	Concerns          []string              `json:"concerns"`
	FailureReason     string                `json:"failure_reason"`
	BlockReason       string                `json:"block_reason"`
	ChangedFiles      []string              `json:"changed_files"`
	Artifacts         []string              `json:"artifacts"`
	TokenUsage        *RuntimeTokenUsage    `json:"token_usage,omitempty"`
	ActivityStats     *RuntimeActivityStats `json:"activity_stats,omitempty"`
	LeaseID           string                `json:"lease_id"`
	InputArtifactPath string                `json:"input_artifact_path,omitempty"`
	StartedAt         time.Time             `json:"started_at"`
	FinishedAt        time.Time             `json:"finished_at"`
}

type RuntimeTokenUsage struct {
	InputTokens       int64 `json:"input_tokens,omitempty"`
	CachedInputTokens int64 `json:"cached_input_tokens,omitempty"`
	OutputTokens      int64 `json:"output_tokens,omitempty"`
	TotalTokens       int64 `json:"total_tokens,omitempty"`
}

type RuntimeActivityStats struct {
	EventCount        int `json:"event_count,omitempty"`
	CommandCount      int `json:"command_count,omitempty"`
	AgentMessageCount int `json:"agent_message_count,omitempty"`
}

type VerificationResult struct {
	Command    string    `json:"command"`
	Cwd        string    `json:"cwd"`
	ExitCode   int       `json:"exit_code"`
	TimedOut   bool      `json:"timed_out"`
	Passed     bool      `json:"passed"`
	DurationMS int64     `json:"duration_ms"`
	StdoutPath string    `json:"stdout_path,omitempty"`
	StderrPath string    `json:"stderr_path,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
}

type VerificationArtifact struct {
	ArtifactMeta
	TicketID   string               `json:"ticket_id"`
	Attempt    int                  `json:"attempt"`
	Commands   []string             `json:"commands"`
	Results    []VerificationResult `json:"results"`
	Passed     bool                 `json:"passed"`
	RepoRoot   string               `json:"repo_root"`
	StartedAt  time.Time            `json:"started_at"`
	FinishedAt time.Time            `json:"finished_at"`
	// ValidationCoverage describes the declared/derived/executed/skipped
	// checks that produced Results. Older artifacts written before this
	// field existed unmarshal with ValidationCoverage == nil.
	ValidationCoverage *ValidationCoverageArtifact `json:"validation_coverage,omitempty"`
}

type ReviewFinding struct {
	ID              string    `json:"id"`
	Severity        Severity  `json:"severity"`
	Title           string    `json:"title"`
	Body            string    `json:"body"`
	File            string    `json:"file,omitempty"`
	Line            int       `json:"line,omitempty"`
	Disposition     string    `json:"disposition"`
	WaivedBy        string    `json:"waived_by,omitempty"`
	WaivedAt        time.Time `json:"waived_at,omitempty"`
	WaiverReason    string    `json:"waiver_reason,omitempty"`
	WaiverExpiresAt time.Time `json:"waiver_expires_at,omitempty"`
}

type ReviewFindingsArtifact struct {
	ArtifactMeta
	TicketID                 string                `json:"ticket_id"`
	Attempt                  int                   `json:"attempt"`
	ReviewerRuntime          string                `json:"reviewer_runtime"`
	ReviewerModel            string                `json:"reviewer_model,omitempty"`
	ReviewerReasoning        string                `json:"reviewer_reasoning,omitempty"`
	FallbackReason           string                `json:"fallback_reason,omitempty"`
	Summary                  string                `json:"summary"`
	Findings                 []ReviewFinding       `json:"findings"`
	BlockingFindings         []string              `json:"blocking_findings"`
	Passed                   bool                  `json:"passed"`
	EffectiveReviewThreshold Severity              `json:"effective_review_threshold"`
	Artifacts                []string              `json:"artifacts,omitempty"`
	TokenUsage               *RuntimeTokenUsage    `json:"token_usage,omitempty"`
	ActivityStats            *RuntimeActivityStats `json:"activity_stats,omitempty"`
	StartedAt                time.Time             `json:"started_at,omitempty"`
	FinishedAt               time.Time             `json:"finished_at,omitempty"`
}

type GateResult struct {
	Status        string   `json:"status"`
	Reason        string   `json:"reason"`
	ArtifactPaths []string `json:"artifact_paths"`
	FindingIDs    []string `json:"finding_ids"`
}

type CriteriaEvidence struct {
	CriterionID   string `json:"criterion_id"`
	CriterionText string `json:"criterion_text"`
	EvidenceType  string `json:"evidence_type"`
	Source        string `json:"source"`
	Summary       string `json:"summary"`
	RunID         string `json:"run_id"`
	TicketID      string `json:"ticket_id"`
	Attempt       int    `json:"attempt"`
	ArtifactRef   string `json:"artifact_ref"`
}

type CloseoutArtifact struct {
	ArtifactMeta
	TicketID          string                `json:"ticket_id"`
	CriteriaEvidence  []CriteriaEvidence    `json:"criteria_evidence"`
	RequiredArtifacts []string              `json:"required_artifacts"`
	GateResults       map[string]GateResult `json:"gate_results"`
	Closable          bool                  `json:"closable"`
	FailedGate        string                `json:"failed_gate"`
	// ValidationCoverage carries declared/derived/executed/skipped check
	// coverage and repair routing decisions. Optional for backward
	// compatibility: closeouts written before this field existed unmarshal
	// with ValidationCoverage == nil and keep working.
	ValidationCoverage *ValidationCoverageArtifact `json:"validation_coverage,omitempty"`
	// UnresolvedCheckID points at the validation check that prevents
	// closure, when FailedGate alone is not specific enough (e.g. a
	// specific derived check id).
	UnresolvedCheckID string `json:"unresolved_check_id,omitempty"`
	// BlockReason is a human-readable explanation of why this closeout is
	// not closable. Required whenever Closable is false and a reviewer or
	// derived check is the root cause.
	BlockReason string `json:"block_reason,omitempty"`
}

type RepairCycleArtifact struct {
	ArtifactMeta
	TicketID             string    `json:"ticket_id"`
	Cycle                int       `json:"cycle"`
	TriggerFindingIDs    []string  `json:"trigger_finding_ids"`
	InputReviewArtifact  string    `json:"input_review_artifact"`
	RepairNotes          string    `json:"repair_notes"`
	VerificationArtifact string    `json:"verification_artifact"`
	ReviewArtifact       string    `json:"review_artifact"`
	Status               string    `json:"status"`
	StartedAt            time.Time `json:"started_at"`
	FinishedAt           time.Time `json:"finished_at"`
	// Scope is the validation scope that triggered this repair cycle.
	// Defaults to ticket scope for older artifacts.
	Scope ValidationScope `json:"scope,omitempty"`
	// WaveID identifies the owning wave for wave-scoped repair cycles.
	WaveID string `json:"wave_id,omitempty"`
	// EpicID identifies the owning epic for epic-scoped repair cycles.
	EpicID string `json:"epic_id,omitempty"`
	// TriggerCheckIDs lists validation check ids that triggered this
	// cycle in addition to (or instead of) review findings. Empty for
	// review-only repairs preserves backward compatibility.
	TriggerCheckIDs []string `json:"trigger_check_ids,omitempty"`
	// PolicyLimitReached is set when this cycle terminated because a
	// policy-bounded limit was hit; the field explains which one.
	PolicyLimitReached *ValidationRepairLimit `json:"policy_limit_reached,omitempty"`
}

// EpicClosureFinding captures one issue surfaced by the epic closure gate.
// It may originate from a broad gate command, a derived epic-scope check
// (e.g. stale-wording sweep), or an epic reviewer pass. The OwningTicketID
// field is populated when the engine can map the finding to a specific
// child ticket so downstream routing can point operators at the owning
// work item. RequiresOperator is true when no automated repair is safe —
// this is the signal that the epic must block pending human input.
type EpicClosureFinding struct {
	ID                 string   `json:"id"`
	Source             string   `json:"source"`
	Severity           Severity `json:"severity,omitempty"`
	Title              string   `json:"title"`
	Body               string   `json:"body,omitempty"`
	File               string   `json:"file,omitempty"`
	Line               int      `json:"line,omitempty"`
	OwningTicketID     string   `json:"owning_ticket_id,omitempty"`
	RequiresOperator   bool     `json:"requires_operator,omitempty"`
	AutoRepairPossible bool     `json:"auto_repair_possible,omitempty"`
	Resolved           bool     `json:"resolved,omitempty"`
	NextAction         string   `json:"next_action,omitempty"`
}

// EpicClosureCycle records the state of a single epic-level repair cycle.
// Each cycle invokes a repair worker with the accumulated failing gate
// output plus epic reviewer findings; a cycle is marked Completed only
// when the subsequent re-run of the gate passes every check it spawned.
type EpicClosureCycle struct {
	Cycle           int       `json:"cycle"`
	StartedAt       time.Time `json:"started_at"`
	FinishedAt      time.Time `json:"finished_at,omitempty"`
	Status          string    `json:"status"`
	TriggerFindings []string  `json:"trigger_findings,omitempty"`
	RepairNotes     string    `json:"repair_notes,omitempty"`
}

// EpicClosureArtifact is the durable record of the epic closure gate run.
// It captures which broad commands executed, any findings produced by
// derived checks or the epic reviewer, the sequence of repair cycles
// spawned to resolve them, and the authoritative Closable flag that
// determines whether the epic can transition to completed. BlockReason
// is mandatory whenever Closable is false so operators get a clear,
// specific explanation instead of a vague "something failed".
type EpicClosureArtifact struct {
	ArtifactMeta
	EpicID          string                      `json:"epic_id"`
	ChildTicketIDs  []string                    `json:"child_ticket_ids,omitempty"`
	BroadCommands   []string                    `json:"broad_commands,omitempty"`
	DerivedCommands []string                    `json:"derived_commands,omitempty"`
	Findings        []EpicClosureFinding        `json:"findings,omitempty"`
	Cycles          []EpicClosureCycle          `json:"cycles,omitempty"`
	Coverage        *ValidationCoverageArtifact `json:"validation_coverage,omitempty"`
	Closable        bool                        `json:"closable"`
	ClosureReason   string                      `json:"closure_reason,omitempty"`
	BlockReason     string                      `json:"block_reason,omitempty"`
	RepairLimit     *ValidationRepairLimit      `json:"repair_limit,omitempty"`
	ReviewerRuntime string                      `json:"reviewer_runtime,omitempty"`
	ReviewerModel   string                      `json:"reviewer_model,omitempty"`
}

type ClaimArtifact struct {
	ArtifactMeta
	TicketID              string    `json:"ticket_id"`
	OwnerRunID            string    `json:"owner_run_id"`
	OwnerWaveID           string    `json:"owner_wave_id,omitempty"`
	LeaseID               string    `json:"lease_id"`
	LeasedAt              time.Time `json:"leased_at"`
	ExpiresAt             time.Time `json:"expires_at"`
	ReleasedAt            time.Time `json:"released_at,omitempty"`
	ReleaseReason         string    `json:"release_reason,omitempty"`
	State                 string    `json:"state"`
	LastSeenLiveClaimPath string    `json:"last_seen_live_claim_path,omitempty"`
}
