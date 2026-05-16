package memory

import "time"

// EscapedDefectStatus is the lifecycle status of a lesson.
type EscapedDefectStatus string

const (
	StatusProposed   EscapedDefectStatus = "proposed"
	StatusPromoted   EscapedDefectStatus = "promoted"
	StatusRejected   EscapedDefectStatus = "rejected"
	StatusSuperseded EscapedDefectStatus = "superseded"
)

// ValidMissedBy lists all accepted missed-by values. Values must be validated at input time.
var ValidMissedBy = map[string]bool{
	"ticket_acceptance": true,
	"reviewer":          true,
	"validation":        true,
	"verification":      true,
	"intake":            true,
	"planner_review":    true,
}

// ValidStatus lists all accepted status values.
var ValidStatus = map[EscapedDefectStatus]bool{
	StatusProposed:   true,
	StatusPromoted:   true,
	StatusRejected:   true,
	StatusSuperseded: true,
}

// EscapedDefect is one captured lesson about what escaped a gate.
type EscapedDefect struct {
	ID                    string              `json:"id"`
	CreatedAt             time.Time           `json:"created_at"`
	SourceRunID           string              `json:"source_run_id,omitempty"`
	SourceTicketIDs       []string            `json:"source_ticket_ids,omitempty"`
	Summary               string              `json:"summary"`
	MissedBy              []string            `json:"missed_by,omitempty"`
	RecommendedRule       string              `json:"recommended_rule,omitempty"`
	CandidateQualityCodes []string            `json:"candidate_quality_codes,omitempty"`
	Status                EscapedDefectStatus `json:"status"`
}

// PromotionEntry records that a lesson was promoted to a rule.
type PromotionEntry struct {
	LessonID   string    `json:"lesson_id"`
	PromotedAt time.Time `json:"promoted_at"`
	Target     string    `json:"target"`
	RuleID     string    `json:"rule_id"`
	Summary    string    `json:"summary,omitempty"`
}
