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
}

type PlanCriterion struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type ImplementationArtifact struct {
	ArtifactMeta
	TicketID          string     `json:"ticket_id"`
	Attempt           int        `json:"attempt"`
	Runtime           string     `json:"runtime"`
	Status            string     `json:"status"`
	CompletionCode    string     `json:"completion_code"`
	RetryClass        RetryClass `json:"retry_class"`
	Concerns          []string   `json:"concerns"`
	FailureReason     string     `json:"failure_reason"`
	BlockReason       string     `json:"block_reason"`
	ChangedFiles      []string   `json:"changed_files"`
	Artifacts         []string   `json:"artifacts"`
	LeaseID           string     `json:"lease_id"`
	InputArtifactPath string     `json:"input_artifact_path,omitempty"`
	StartedAt         time.Time  `json:"started_at"`
	FinishedAt        time.Time  `json:"finished_at"`
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
	TicketID                 string          `json:"ticket_id"`
	Attempt                  int             `json:"attempt"`
	ReviewerRuntime          string          `json:"reviewer_runtime"`
	Summary                  string          `json:"summary"`
	Findings                 []ReviewFinding `json:"findings"`
	BlockingFindings         []string        `json:"blocking_findings"`
	Passed                   bool            `json:"passed"`
	EffectiveReviewThreshold Severity        `json:"effective_review_threshold"`
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
