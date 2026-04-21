// Package state — validation coverage artifacts.
//
// These types describe what was required, derived, executed, skipped,
// repaired, or blocked for a ticket, wave, or epic gate. They are designed
// to be reusable across scopes (ticket / wave / epic) and to extend existing
// artifacts in a backward-compatible way: every field uses omitempty so
// older artifacts written without validation coverage still round-trip
// through LoadJSON and produce zero-valued defaults.
package state

import "time"

// ValidationScope identifies the gate that a validation artifact describes.
//
// The same schema is used for ticket, wave, and epic closure so the engine
// can reason uniformly about what ran and what is blocking closure.
type ValidationScope string

const (
	// ValidationScopeTicket covers per-ticket verification and repair.
	ValidationScopeTicket ValidationScope = "ticket"
	// ValidationScopeWave covers post-merge wave verification and repair.
	ValidationScopeWave ValidationScope = "wave"
	// ValidationScopeEpic covers epic-level closure gates and broad reviews.
	ValidationScopeEpic ValidationScope = "epic"
)

// ValidationCheckSource explains why a check exists.
//
// This lets downstream UIs and policy decisions tell the difference between
// a check the ticket explicitly declared vs. one the engine derived from
// changed files or global config.
type ValidationCheckSource string

const (
	// ValidationCheckSourceDeclared means the ticket asked for this check.
	ValidationCheckSourceDeclared ValidationCheckSource = "declared"
	// ValidationCheckSourceDerived means the engine derived this check from
	// changed files, ticket scope, or repository tooling signals.
	ValidationCheckSourceDerived ValidationCheckSource = "derived"
	// ValidationCheckSourceQuality means this check came from repo-wide
	// quality_commands policy.
	ValidationCheckSourceQuality ValidationCheckSource = "quality"
	// ValidationCheckSourceReviewer means a reviewer finding requested this
	// check be re-run or added.
	ValidationCheckSourceReviewer ValidationCheckSource = "reviewer"
	// ValidationCheckSourceOperator means a human operator added this check
	// via CLI or follow-up input.
	ValidationCheckSourceOperator ValidationCheckSource = "operator"
)

// ValidationCheckResult is the terminal state of an execution attempt.
//
// Pending is valid while a check is scheduled or running. Repaired means a
// previously failed check was re-run and passed after a repair cycle — this
// is distinct from Passed so history remains auditable.
type ValidationCheckResult string

const (
	ValidationCheckResultPending  ValidationCheckResult = "pending"
	ValidationCheckResultPassed   ValidationCheckResult = "passed"
	ValidationCheckResultFailed   ValidationCheckResult = "failed"
	ValidationCheckResultSkipped  ValidationCheckResult = "skipped"
	ValidationCheckResultRepaired ValidationCheckResult = "repaired"
)

// ValidationCheck describes a single check the engine is aware of.
//
// A check is the stable identity across executions: the same check id can be
// run multiple times (declared + repaired) and the individual outcomes are
// recorded as ValidationCheckExecution entries. Reason and MatchedFiles
// document the provenance of a derived check.
type ValidationCheck struct {
	ID           string                `json:"id"`
	Scope        ValidationScope       `json:"scope,omitempty"`
	Source       ValidationCheckSource `json:"source,omitempty"`
	Command      string                `json:"command,omitempty"`
	Reason       string                `json:"reason,omitempty"`
	MatchedFiles []string              `json:"matched_files,omitempty"`
	TicketID     string                `json:"ticket_id,omitempty"`
	WaveID       string                `json:"wave_id,omitempty"`
	Severity     Severity              `json:"severity,omitempty"`
	// Advisory checks report results but never block closure by themselves.
	// Skipped advisory checks should not appear in UnresolvedBlockers.
	Advisory bool `json:"advisory,omitempty"`
}

// ValidationCheckExecution records one run of a check.
//
// If the run was part of a repair cycle, RepairCycleID links back to the
// RepairCycleArtifact so the history is navigable.
type ValidationCheckExecution struct {
	CheckID        string                `json:"check_id"`
	Result         ValidationCheckResult `json:"result"`
	ExitCode       int                   `json:"exit_code,omitempty"`
	DurationMS     int64                 `json:"duration_ms,omitempty"`
	StdoutPath     string                `json:"stdout_path,omitempty"`
	StderrPath     string                `json:"stderr_path,omitempty"`
	FailureSummary string                `json:"failure_summary,omitempty"`
	Attempt        int                   `json:"attempt,omitempty"`
	RepairCycleID  string                `json:"repair_cycle_id,omitempty"`
	StartedAt      time.Time             `json:"started_at,omitempty"`
	FinishedAt     time.Time             `json:"finished_at,omitempty"`
}

// ValidationCheckSkip records that a check was intentionally not executed.
//
// The most common reasons are missing optional tooling (e.g. shellcheck not
// installed) and advisory checks disabled by policy. Skip entries with a
// required (non-advisory) check are expected to surface as unresolved
// blockers unless the engine also records a repair or waiver.
type ValidationCheckSkip struct {
	CheckID string `json:"check_id"`
	Reason  string `json:"reason"`
	Detail  string `json:"detail,omitempty"`
}

// ValidationBlocker explains why a scope cannot close.
//
// Either CheckID or FindingID (or both) should be populated — CheckID for
// check-based blockers (e.g. verification failed), FindingID for review
// finding blockers. RequiresOperator signals that the engine cannot safely
// repair this automatically and needs human input.
type ValidationBlocker struct {
	CheckID          string          `json:"check_id,omitempty"`
	FindingID        string          `json:"finding_id,omitempty"`
	Reason           string          `json:"reason"`
	RequiresOperator bool            `json:"requires_operator,omitempty"`
	RepairCycleID    string          `json:"repair_cycle_id,omitempty"`
	Scope            ValidationScope `json:"scope,omitempty"`
}

// ValidationRepairRef links a check or review finding to a repair cycle
// artifact so downstream UIs can show the full repair history.
type ValidationRepairRef struct {
	CheckID      string                `json:"check_id,omitempty"`
	FindingID    string                `json:"finding_id,omitempty"`
	CycleID      string                `json:"cycle_id"`
	ArtifactPath string                `json:"artifact_path,omitempty"`
	Result       ValidationCheckResult `json:"result,omitempty"`
	Scope        ValidationScope       `json:"scope,omitempty"`
}

// ValidationRepairLimit records the bounded-loop stopping condition.
//
// When a repair loop gives up because it hit a policy limit (e.g.
// policy.max_repair_cycles), the engine writes this so operators can see
// which limit stopped further repair and decide whether to raise it.
type ValidationRepairLimit struct {
	Name      string `json:"name"`
	Limit     int    `json:"limit"`
	Reached   int    `json:"reached"`
	Reason    string `json:"reason,omitempty"`
	PolicyRef string `json:"policy_ref,omitempty"`
}

// ValidationCoverageArtifact is the durable record of a scope's validation
// coverage and closure state.
//
// The same shape is reused for ticket, wave, and epic closure. Scope plus
// the *ID fields (TicketID / WaveID / EpicID / ChildTicketIDs) tell consumers
// which entity it describes. The Closable flag is authoritative for
// downstream closure logic; ClosureReason and BlockReason are the
// human-readable explanations that must be present alongside.
//
// This artifact is append-only in spirit: the engine should add new
// executions rather than rewriting prior ones so the history of repair
// cycles stays auditable.
type ValidationCoverageArtifact struct {
	ArtifactMeta
	Scope              ValidationScope            `json:"scope"`
	TicketID           string                     `json:"ticket_id,omitempty"`
	WaveID             string                     `json:"wave_id,omitempty"`
	EpicID             string                     `json:"epic_id,omitempty"`
	ChildTicketIDs     []string                   `json:"child_ticket_ids,omitempty"`
	DeclaredChecks     []ValidationCheck          `json:"declared_checks,omitempty"`
	DerivedChecks      []ValidationCheck          `json:"derived_checks,omitempty"`
	ExecutedChecks     []ValidationCheckExecution `json:"executed_checks,omitempty"`
	SkippedChecks      []ValidationCheckSkip      `json:"skipped_checks,omitempty"`
	RepairRefs         []ValidationRepairRef      `json:"repair_refs,omitempty"`
	UnresolvedBlockers []ValidationBlocker        `json:"unresolved_blockers,omitempty"`
	RepairLimit        *ValidationRepairLimit     `json:"repair_limit,omitempty"`
	Closable           bool                       `json:"closable,omitempty"`
	ClosureReason      string                     `json:"closure_reason,omitempty"`
	BlockReason        string                     `json:"block_reason,omitempty"`
}

// AllChecks returns declared and derived checks concatenated.
// Useful for iteration when callers do not need to distinguish the source.
func (a ValidationCoverageArtifact) AllChecks() []ValidationCheck {
	if len(a.DeclaredChecks) == 0 && len(a.DerivedChecks) == 0 {
		return nil
	}
	out := make([]ValidationCheck, 0, len(a.DeclaredChecks)+len(a.DerivedChecks))
	out = append(out, a.DeclaredChecks...)
	out = append(out, a.DerivedChecks...)
	return out
}

// CheckByID finds a check by its stable id across declared and derived
// lists. Returns (check, true) on a match; otherwise the zero value and false.
func (a ValidationCoverageArtifact) CheckByID(id string) (ValidationCheck, bool) {
	for _, c := range a.DeclaredChecks {
		if c.ID == id {
			return c, true
		}
	}
	for _, c := range a.DerivedChecks {
		if c.ID == id {
			return c, true
		}
	}
	return ValidationCheck{}, false
}

// LatestExecution returns the most recent execution for a given check id,
// or the zero value and false if none exists. "Most recent" is determined
// by FinishedAt, falling back to the order entries appear in the slice.
func (a ValidationCoverageArtifact) LatestExecution(checkID string) (ValidationCheckExecution, bool) {
	var (
		best  ValidationCheckExecution
		found bool
	)
	for _, e := range a.ExecutedChecks {
		if e.CheckID != checkID {
			continue
		}
		if !found {
			best = e
			found = true
			continue
		}
		if e.FinishedAt.After(best.FinishedAt) {
			best = e
		}
	}
	return best, found
}

// ValidationCheckResultFromBool is a small helper for adapters that only
// know "passed bool": true -> passed, false -> failed.
func ValidationCheckResultFromBool(passed bool) ValidationCheckResult {
	if passed {
		return ValidationCheckResultPassed
	}
	return ValidationCheckResultFailed
}
