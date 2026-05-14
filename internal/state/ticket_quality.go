package state

// TicketQualityStatus is the outcome of a ticket quality gate run.
type TicketQualityStatus string

const (
	TicketQualityPassed   TicketQualityStatus = "passed"
	TicketQualityRepaired TicketQualityStatus = "repaired"
	TicketQualityBlocked  TicketQualityStatus = "blocked"
)

// TicketQualityCode identifies the category of a quality finding.
type TicketQualityCode string

const (
	QualityCodeMissingAcceptanceCriteria         TicketQualityCode = "missing_acceptance_criteria"
	QualityCodeAmbiguousAcceptanceCriterion      TicketQualityCode = "ambiguous_acceptance_criterion"
	QualityCodeCompoundAcceptanceCriterion       TicketQualityCode = "compound_acceptance_criterion"
	QualityCodeMissingValidationCommands         TicketQualityCode = "missing_validation_commands"
	QualityCodeMissingOwnedPaths                 TicketQualityCode = "missing_owned_paths"
	QualityCodeOwnedPathMissing                  TicketQualityCode = "owned_path_missing"
	QualityCodeDependencyMissing                 TicketQualityCode = "dependency_missing"
	QualityCodeDependencyBlockedOrClosedMismatch TicketQualityCode = "dependency_blocked_or_closed_mismatch"
	QualityCodeMissingPublicContractScenario     TicketQualityCode = "missing_public_contract_scenario"
	QualityCodeMissingNegativeCase               TicketQualityCode = "missing_negative_case"
	QualityCodeDocsDescopeRisk                   TicketQualityCode = "docs_descope_risk"
	QualityCodeIntegrationGap                    TicketQualityCode = "integration_gap"
	QualityCodePlanTraceabilityGap               TicketQualityCode = "plan_traceability_gap"
	QualityCodeReviewerInstructionGap            TicketQualityCode = "reviewer_instruction_gap"
)

// TicketQualityFinding is one issue surfaced by the ticket quality gate.
type TicketQualityFinding struct {
	ID              string   `json:"id"`
	TicketID        string   `json:"ticket_id"`
	Code            string   `json:"code"`
	Severity        Severity `json:"severity"`
	Title           string   `json:"title"`
	Body            string   `json:"body"`
	Evidence        []string `json:"evidence,omitempty"`
	Repairable      bool     `json:"repairable"`
	AutoRepairable  bool     `json:"auto_repairable"`
	RequiresPlanner bool     `json:"requires_planner,omitempty"`
	Disposition     string   `json:"disposition"`
}

// TicketQualityRepair records one repair action applied to resolve a finding.
type TicketQualityRepair struct {
	FindingID string `json:"finding_id"`
	TicketID  string `json:"ticket_id"`
	Kind      string `json:"kind"`
	Summary   string `json:"summary"`
	Applied   bool   `json:"applied"`
}

// TicketQualityArtifact is the durable record of a ticket quality gate run.
// Status is passed when all checks pass, repaired when auto-repairs resolved
// all findings, and blocked when unresolvable findings remain.
type TicketQualityArtifact struct {
	ArtifactMeta
	Scope        string                 `json:"scope"`
	RootTicketID string                 `json:"root_ticket_id,omitempty"`
	TicketIDs    []string               `json:"ticket_ids"`
	Status       TicketQualityStatus    `json:"status"`
	Findings     []TicketQualityFinding `json:"findings"`
	Repairs      []TicketQualityRepair  `json:"repairs,omitempty"`
	Blocked      bool                   `json:"blocked"`
	BlockReason  string                 `json:"block_reason,omitempty"`
}
