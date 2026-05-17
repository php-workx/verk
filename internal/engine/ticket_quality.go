package engine

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"verk/internal/adapters/ticketstore/epos"
	"verk/internal/memory"
	"verk/internal/policy"
	"verk/internal/state"
)

// TicketQualityInput collects all the data the deterministic ticket-quality
// evaluator needs. RootTicket is the scope root; Tickets is the full ticket set
// being evaluated (for an epic, this is root plus children; for a single
// ticket, it is just that ticket). ExistingPaths is optional and used by
// owned-path existence checks (nil disables that check).
type TicketQualityInput struct {
	RootTicket    epos.Ticket
	Tickets       []epos.Ticket
	ExistingPaths map[string]bool
	Config        policy.Config
}

// EvaluateTicketQuality runs the deterministic ticket-quality lint rules over
// the input and returns a TicketQualityArtifact. It does not perform any LLM
// or filesystem work; callers wire it into the run pipeline separately.
func EvaluateTicketQuality(input TicketQualityInput) state.TicketQualityArtifact {
	tickets := input.Tickets
	if len(tickets) == 0 {
		tickets = []epos.Ticket{input.RootTicket}
	}

	idSet := make(map[string]bool, len(tickets))
	for _, t := range tickets {
		idSet[t.ID] = true
	}
	idSet[input.RootTicket.ID] = true

	scope := "ticket"
	if isEpic(input.RootTicket) {
		scope = "epic"
	}

	var findings []state.TicketQualityFinding
	for _, t := range tickets {
		findings = append(findings, evalMissingAcceptanceCriteria(t)...)
		findings = append(findings, evalAmbiguousCriteria(t)...)
		findings = append(findings, evalCompoundAcceptanceCriterion(t)...)
		findings = append(findings, evalMissingValidationCommands(t)...)
		findings = append(findings, evalMissingOwnedPaths(t)...)
		findings = append(findings, evalOwnedPathMissing(t, input.ExistingPaths)...)
		findings = append(findings, evalDependencyMissing(t, idSet)...)
		findings = append(findings, evalDependencyBlockedOrClosed(t, input.Tickets)...)
		findings = append(findings, evalPublicContractScenario(t)...)
		findings = append(findings, evalMissingNegativeCase(t)...)
		findings = append(findings, evalDocsDescopeRisk(t)...)
		findings = append(findings, evalPlanTraceabilityGap(t)...)
		findings = append(findings, evalReviewerInstructionGap(t)...)
	}
	if isEpic(input.RootTicket) {
		findings = append(findings, evalIntegrationGap(input.RootTicket, tickets)...)
	}

	sort.Slice(findings, func(i, j int) bool {
		if findings[i].TicketID != findings[j].TicketID {
			return findings[i].TicketID < findings[j].TicketID
		}
		return findings[i].Code < findings[j].Code
	})

	threshold := blockThreshold(input.Config)
	blocked := false
	for _, f := range findings {
		if severityAtLeast(f.Severity, threshold) {
			blocked = true
			break
		}
	}

	status := state.TicketQualityPassed
	blockReason := ""
	if blocked {
		status = state.TicketQualityBlocked
		blockReason = "blocking findings at or above threshold"
	}

	ticketIDs := make([]string, 0, len(tickets))
	for _, t := range tickets {
		ticketIDs = append(ticketIDs, t.ID)
	}
	sort.Strings(ticketIDs)

	traces := buildTraces(tickets)

	return state.TicketQualityArtifact{
		Scope:        scope,
		RootTicketID: input.RootTicket.ID,
		TicketIDs:    ticketIDs,
		Status:       status,
		Findings:     findings,
		Traces:       traces,
		Blocked:      blocked,
		BlockReason:  blockReason,
	}
}

// buildTraces extracts one trace per acceptance criterion across all
// tickets in the input. Traces propagate source refs from ticket body
// (links to docs/plans/), test cases, validation commands, and detected
// public-contract status.
func buildTraces(tickets []epos.Ticket) []state.TicketQualityTrace {
	var traces []state.TicketQualityTrace
	for _, t := range tickets {
		srcRef := extractSourceRef(t)
		validationRefs := append([]string{}, t.TestCases...)
		validationRefs = append(validationRefs, t.ValidationCommands...)
		pc := isPublicContractTicket(t)
		for _, c := range t.AcceptanceCriteria {
			traces = append(traces, state.TicketQualityTrace{
				SourceRef:      srcRef,
				TicketID:       t.ID,
				Criterion:      c,
				ValidationRefs: validationRefs,
				PublicContract: pc,
			})
		}
	}
	return traces
}

// extractSourceRef returns the first plan reference found in the ticket
// body (line containing "docs/plans/") or "" if none found. Also checks
// UnknownFrontmatter["plan_refs"] for a list of strings.
func extractSourceRef(t epos.Ticket) string {
	// Check plan_refs frontmatter first
	if t.UnknownFrontmatter != nil {
		if refs, ok := t.UnknownFrontmatter["plan_refs"].([]any); ok && len(refs) > 0 {
			if s, ok := refs[0].(string); ok {
				return s
			}
		}
	}
	// Scan body for docs/plans/ link
	for _, line := range strings.Split(t.Body, "\n") {
		if i := strings.Index(line, "docs/plans/"); i >= 0 {
			ref := line[i:]
			// Trim trailing punctuation/whitespace
			ref = strings.TrimRight(ref, ".,;: )")
			// Stop at next whitespace
			if j := strings.IndexAny(ref, " \t)"); j >= 0 {
				ref = ref[:j]
			}
			return ref
		}
	}
	return ""
}

// ticketType returns the ticket "type" stored in UnknownFrontmatter["type"].
// Returns "" if absent or wrong shape.
func ticketType(t epos.Ticket) string {
	if t.UnknownFrontmatter == nil {
		return ""
	}
	if v, ok := t.UnknownFrontmatter["type"].(string); ok {
		return v
	}
	return ""
}

func isEpic(t epos.Ticket) bool {
	return ticketType(t) == "epic"
}

func isDocsTicket(t epos.Ticket) bool {
	if ticketType(t) == "docs" {
		return true
	}
	for _, p := range t.OwnedPaths {
		if strings.HasPrefix(p, "docs/") {
			return true
		}
	}
	return false
}

func isChoreTicket(t epos.Ticket) bool {
	return ticketType(t) == "chore"
}

// makeFindingID generates a stable finding ID from ticketID + code + sorted
// evidence. The byte-slice pattern matches epic_gate.go's epicCheckFindingID.
func makeFindingID(ticketID string, code state.TicketQualityCode, evidence []string) string {
	sorted := append([]string(nil), evidence...)
	sort.Strings(sorted)
	payload := ticketID + "|" + string(code) + "|" + strings.Join(sorted, "|")
	h := sha256.Sum256([]byte(payload))
	return fmt.Sprintf("%s-%x", code, h[:6])
}

// severityAtLeast reports whether sev is at or more severe than threshold.
// P0 > P1 > P2 > P3 > P4.
func severityAtLeast(sev, threshold state.Severity) bool {
	return severityRank(sev) <= severityRank(threshold)
}

// blockThreshold returns the effective block threshold for ticket quality.
// Reads from policy.Config.TicketQuality.BlockThreshold; defaults to P2.
func blockThreshold(cfg policy.Config) state.Severity {
	if cfg.TicketQuality.BlockThreshold == "" {
		return state.SeverityP2
	}
	return state.Severity(cfg.TicketQuality.BlockThreshold)
}

// --- Rule implementations --------------------------------------------------

func evalMissingAcceptanceCriteria(t epos.Ticket) []state.TicketQualityFinding {
	if isEpic(t) {
		return nil
	}
	if len(t.AcceptanceCriteria) > 0 || len(t.TestCases) > 0 || len(t.ValidationCommands) > 0 {
		return nil
	}
	code := state.QualityCodeMissingAcceptanceCriteria
	return []state.TicketQualityFinding{{
		ID:             makeFindingID(t.ID, code, nil),
		TicketID:       t.ID,
		Code:           string(code),
		Severity:       state.SeverityP1,
		Title:          "ticket has no acceptance criteria, test cases, or validation commands",
		Body:           "Add at least one observable acceptance criterion, test case, or validation command so workers know what success looks like.",
		Repairable:     false,
		AutoRepairable: false,
		Disposition:    "open",
	}}
}

var (
	vagueWords     = []string{"works", "done", "handled", "state", "support", "properly", "ready", "complete"}
	concreteSignal = regexp.MustCompile(`(?i)--\w+|\bexit code\b|\bstatus\s*\d+|\b\d{3}\b|/[\w.\-/]+\.\w+|\bstdout\b|\bstderr\b|\bHTTP\b`)
)

func evalAmbiguousCriteria(t epos.Ticket) []state.TicketQualityFinding {
	var out []state.TicketQualityFinding
	for _, c := range t.AcceptanceCriteria {
		if isAmbiguousCriterion(c) {
			code := state.QualityCodeAmbiguousAcceptanceCriterion
			out = append(out, state.TicketQualityFinding{
				ID:             makeFindingID(t.ID, code, []string{c}),
				TicketID:       t.ID,
				Code:           string(code),
				Severity:       state.SeverityP2,
				Title:          "acceptance criterion is too vague to verify",
				Body:           "Replace vague wording with a concrete observable: command + flag, exit code, output text, file path, HTTP status, or named field.",
				Evidence:       []string{c},
				Repairable:     false,
				AutoRepairable: false,
				Disposition:    "open",
			})
		}
	}
	return out
}

func isAmbiguousCriterion(c string) bool {
	lc := strings.ToLower(c)
	hasVague := false
	for _, w := range vagueWords {
		if strings.Contains(lc, w) {
			hasVague = true
			break
		}
	}
	if !hasVague {
		return false
	}
	return !concreteSignal.MatchString(c)
}

func evalMissingOwnedPaths(t epos.Ticket) []state.TicketQualityFinding {
	if isEpic(t) || isDocsTicket(t) || isChoreTicket(t) {
		return nil
	}
	if len(t.OwnedPaths) > 0 {
		return nil
	}
	code := state.QualityCodeMissingOwnedPaths
	return []state.TicketQualityFinding{{
		ID:             makeFindingID(t.ID, code, nil),
		TicketID:       t.ID,
		Code:           string(code),
		Severity:       state.SeverityP1,
		Title:          "implementation ticket has no owned paths",
		Body:           "Add owned_paths so the scope validator can keep this ticket's worker focused.",
		Repairable:     false,
		AutoRepairable: false,
		Disposition:    "open",
	}}
}

func evalDependencyMissing(t epos.Ticket, idSet map[string]bool) []state.TicketQualityFinding {
	var out []state.TicketQualityFinding
	for _, dep := range t.Deps {
		if dep == "" {
			continue
		}
		if !idSet[dep] {
			code := state.QualityCodeDependencyMissing
			out = append(out, state.TicketQualityFinding{
				ID:             makeFindingID(t.ID, code, []string{dep}),
				TicketID:       t.ID,
				Code:           string(code),
				Severity:       state.SeverityP1,
				Title:          fmt.Sprintf("dependency %q does not exist", dep),
				Body:           "Either correct the dependency id or add the missing ticket.",
				Evidence:       []string{dep},
				Repairable:     false,
				AutoRepairable: false,
				Disposition:    "open",
			})
		}
	}
	return out
}

var (
	publicContractTitleBody = regexp.MustCompile(`(?i)subcommand|--\w+|\bexit code\b|\bstdout\b|\bstderr\b|\bHTTP\b|\bendpoint\b|\bAPI\b`)
	concreteInvocation      = regexp.MustCompile(`(?i)--\w+|\bexit code\b|\bstdout\b|\bstderr\b|\b[1-5]\d{2}\b|/[\w.\-/]+\.\w+`)
)

func isPublicContractTicket(t epos.Ticket) bool {
	for _, p := range t.OwnedPaths {
		if strings.HasPrefix(p, "cmd/") || strings.HasPrefix(p, "internal/cli") {
			return true
		}
	}
	if publicContractTitleBody.MatchString(t.Title) || publicContractTitleBody.MatchString(t.Body) {
		return true
	}
	return false
}

func hasConcreteScenario(t epos.Ticket) bool {
	all := strings.Join(t.AcceptanceCriteria, "\n") + "\n" + strings.Join(t.TestCases, "\n")
	return concreteInvocation.MatchString(all)
}

func evalPublicContractScenario(t epos.Ticket) []state.TicketQualityFinding {
	if !isPublicContractTicket(t) {
		return nil
	}
	if hasConcreteScenario(t) {
		return nil
	}
	code := state.QualityCodeMissingPublicContractScenario
	return []state.TicketQualityFinding{{
		ID:             makeFindingID(t.ID, code, nil),
		TicketID:       t.ID,
		Code:           string(code),
		Severity:       state.SeverityP1,
		Title:          "public CLI/API ticket lacks a black-box command scenario",
		Body:           "Add at least one acceptance criterion or test case that specifies a concrete invocation (flag, command), expected exit code, and stdout/stderr or response.",
		Repairable:     false,
		AutoRepairable: false,
		Disposition:    "open",
	}}
}

var docsDescopePhrases = []string{
	"not supported",
	"no --",
	"does not support",
	"only supports",
	"remove support",
}

func evalDocsDescopeRisk(t epos.Ticket) []state.TicketQualityFinding {
	if !isDocsTicket(t) {
		return nil
	}
	lc := strings.ToLower(t.Body)
	matched := ""
	for _, p := range docsDescopePhrases {
		if strings.Contains(lc, p) {
			matched = p
			break
		}
	}
	if matched == "" {
		return nil
	}
	if strings.Contains(t.Body, "docs/plans/") {
		return nil
	}
	if t.UnknownFrontmatter != nil {
		if _, ok := t.UnknownFrontmatter["plan_refs"]; ok {
			return nil
		}
	}
	code := state.QualityCodeDocsDescopeRisk
	return []state.TicketQualityFinding{{
		ID:              makeFindingID(t.ID, code, []string{matched}),
		TicketID:        t.ID,
		Code:            string(code),
		Severity:        state.SeverityP1,
		Title:           "docs ticket appears to remove planned behavior without a plan reference",
		Body:            "Link this ticket to a plan update under docs/plans/ that authorizes the descope, or rewrite the docs to keep the planned behavior.",
		Evidence:        []string{matched},
		Repairable:      false,
		AutoRepairable:  false,
		RequiresPlanner: true,
		Disposition:     "open",
	}}
}

// evalCompoundAcceptanceCriterion flags a single criterion that appears to pack
// two independently verifiable claims into one line. Heuristic: criterion
// contains " and " (count > 0) AND length > 100 characters.
func evalCompoundAcceptanceCriterion(t epos.Ticket) []state.TicketQualityFinding {
	var out []state.TicketQualityFinding
	for _, c := range t.AcceptanceCriteria {
		lc := strings.ToLower(c)
		andCount := strings.Count(lc, " and ")
		if andCount > 0 && len(c) > 100 {
			code := state.QualityCodeCompoundAcceptanceCriterion
			out = append(out, state.TicketQualityFinding{
				ID:             makeFindingID(t.ID, code, []string{c}),
				TicketID:       t.ID,
				Code:           string(code),
				Severity:       state.SeverityP3,
				Title:          "acceptance criterion may pack two independently verifiable claims",
				Body:           "Split the criterion into two separate lines so each is verifiable on its own.",
				Evidence:       []string{c},
				Repairable:     false,
				AutoRepairable: false,
				Disposition:    "open",
			})
		}
	}
	return out
}

// evalMissingValidationCommands flags tickets that have acceptance criteria but
// no validation_commands and no code-file owned paths that would imply a
// default lint/test derivation. Skipped for docs and chore tickets.
func evalMissingValidationCommands(t epos.Ticket) []state.TicketQualityFinding {
	if isDocsTicket(t) || isChoreTicket(t) {
		return nil
	}
	// Must have acceptance criteria to be expected to have validation commands.
	if len(t.AcceptanceCriteria) == 0 {
		return nil
	}
	if len(t.ValidationCommands) > 0 {
		return nil
	}
	// If any owned path is a code file or code directory (extensionless path
	// that could be a Go/Python/Rust/TS package dir), assume lint/test can
	// be derived. We skip the warning when a path has no file extension (no
	// dot after the last "/") since that strongly suggests a source directory.
	codeExtensions := []string{".go", ".ts", ".py", ".rs"}
	for _, p := range t.OwnedPaths {
		// Exact code file match.
		for _, ext := range codeExtensions {
			if strings.HasSuffix(p, ext) {
				return nil
			}
		}
		// Extensionless path with a "/" is almost certainly a source directory.
		if strings.Contains(p, "/") {
			base := p[strings.LastIndex(p, "/")+1:]
			if !strings.Contains(base, ".") {
				return nil
			}
		}
	}
	code := state.QualityCodeMissingValidationCommands
	return []state.TicketQualityFinding{{
		ID:             makeFindingID(t.ID, code, nil),
		TicketID:       t.ID,
		Code:           string(code),
		Severity:       state.SeverityP2,
		Title:          "ticket has acceptance criteria but no validation commands",
		Body:           "Add validation_commands (e.g. a test or lint invocation) so the worker can verify the acceptance criteria automatically.",
		Repairable:     false,
		AutoRepairable: false,
		Disposition:    "open",
	}}
}

// ownedPathLooksNew returns true when the path pattern suggests it is a
// newly-introduced file (e.g. lacks any slash-separated component, or the
// path is entirely a leaf name). The heuristic is kept very conservative to
// avoid false positives: we only skip the warning when the path contains no
// "/" at all (i.e. is a top-level plain name with no directory component).
// Paths under stable directories like "internal/" are always considered
// potentially existing.
func ownedPathLooksNew(p string) bool {
	// A path with no directory separator cannot be a stable internal path.
	return !strings.Contains(p, "/")
}

// evalOwnedPathMissing warns when an owned path is given but does not appear
// in ExistingPaths. Skipped when ExistingPaths is nil (check not configured).
// Very conservative: only warns when path contains "/" (i.e. is not
// trivially new) to keep false-positive rate low.
func evalOwnedPathMissing(t epos.Ticket, existing map[string]bool) []state.TicketQualityFinding {
	if existing == nil {
		return nil
	}
	var out []state.TicketQualityFinding
	for _, p := range t.OwnedPaths {
		if existing[p] {
			continue
		}
		if ownedPathLooksNew(p) {
			continue
		}
		code := state.QualityCodeOwnedPathMissing
		out = append(out, state.TicketQualityFinding{
			ID:             makeFindingID(t.ID, code, []string{p}),
			TicketID:       t.ID,
			Code:           string(code),
			Severity:       state.SeverityP2,
			Title:          fmt.Sprintf("owned path %q does not appear to exist", p),
			Body:           "Verify the path is correct; if the path will be created by this ticket, add it under a clearly new directory to suppress this warning.",
			Evidence:       []string{p},
			Repairable:     false,
			AutoRepairable: false,
			Disposition:    "open",
		})
	}
	return out
}

// evalDependencyBlockedOrClosed flags tickets whose dependencies are in
// Blocked or Closed status when the relationship is inconsistent. Requires
// the full ticket slice to be passed so we can look up dep status.
func evalDependencyBlockedOrClosed(t epos.Ticket, all []epos.Ticket) []state.TicketQualityFinding {
	if len(t.Deps) == 0 {
		return nil
	}
	// Build a quick lookup from the all-tickets slice.
	byID := make(map[string]epos.Ticket, len(all))
	for _, dep := range all {
		byID[dep.ID] = dep
	}
	var out []state.TicketQualityFinding
	for _, depID := range t.Deps {
		dep, ok := byID[depID]
		if !ok {
			// dependency_missing handles the absent case; skip here.
			continue
		}
		switch dep.Status {
		case epos.StatusBlocked:
			code := state.QualityCodeDependencyBlockedOrClosedMismatch
			out = append(out, state.TicketQualityFinding{
				ID:             makeFindingID(t.ID, code, []string{depID, "blocked"}),
				TicketID:       t.ID,
				Code:           string(code),
				Severity:       state.SeverityP2,
				Title:          fmt.Sprintf("dependency %q is currently Blocked", depID),
				Body:           "Resolve the blocked dependency before this ticket can proceed, or remove the dependency if it no longer applies.",
				Evidence:       []string{depID},
				Repairable:     false,
				AutoRepairable: false,
				Disposition:    "open",
			})
		case epos.StatusClosed:
			if t.Status != epos.StatusClosed {
				code := state.QualityCodeDependencyBlockedOrClosedMismatch
				out = append(out, state.TicketQualityFinding{
					ID:             makeFindingID(t.ID, code, []string{depID, "closed"}),
					TicketID:       t.ID,
					Code:           string(code),
					Severity:       state.SeverityP2,
					Title:          fmt.Sprintf("dependency %q is Closed but this ticket is not", depID),
					Body:           "A closed dependency on an open ticket may indicate the work was already completed separately. Verify whether this dependency is still needed.",
					Evidence:       []string{depID},
					Repairable:     false,
					AutoRepairable: false,
					Disposition:    "open",
				})
			}
		}
	}
	return out
}

var (
	negativeCaseTrigger  = regexp.MustCompile(`(?i)\bvalidation\b|\berror\b|\bfails\b|\brejects\b`)
	negativeCaseEvidence = regexp.MustCompile(`(?i)\berror\b|\bfailure\b|\breject\b|\binvalid\b|\bexception\b|\bnon.zero exit\b|\bexit [1-9]\b`)
)

// evalMissingNegativeCase flags tickets about validation/error handling that
// have no acceptance criterion or test case mentioning failure paths.
func evalMissingNegativeCase(t epos.Ticket) []state.TicketQualityFinding {
	combined := t.Title + " " + t.Body
	if !negativeCaseTrigger.MatchString(combined) {
		return nil
	}
	// Check all criteria and test cases for negative-path evidence.
	all := strings.Join(t.AcceptanceCriteria, "\n") + "\n" + strings.Join(t.TestCases, "\n")
	if negativeCaseEvidence.MatchString(all) {
		return nil
	}
	code := state.QualityCodeMissingNegativeCase
	return []state.TicketQualityFinding{{
		ID:             makeFindingID(t.ID, code, nil),
		TicketID:       t.ID,
		Code:           string(code),
		Severity:       state.SeverityP2,
		Title:          "ticket mentions validation/errors but has no negative-case acceptance criterion",
		Body:           "Add at least one acceptance criterion or test case that specifies the expected failure path (error message, non-zero exit, rejection reason).",
		Repairable:     false,
		AutoRepairable: false,
		Disposition:    "open",
	}}
}

var planRefTrigger = regexp.MustCompile(`(?i)\bplan\b|\bspec\b|\bRFC\b`)

// evalPlanTraceabilityGap flags tickets that reference a plan/spec/RFC but
// do not link to a docs/plans/ path and have no plan_refs frontmatter.
func evalPlanTraceabilityGap(t epos.Ticket) []state.TicketQualityFinding {
	combined := t.Title + " " + t.Body
	if !planRefTrigger.MatchString(combined) {
		return nil
	}
	// Check whether the ticket already has a plan link.
	if strings.Contains(t.Body, "docs/plans/") {
		return nil
	}
	if t.UnknownFrontmatter != nil {
		if _, ok := t.UnknownFrontmatter["plan_refs"]; ok {
			return nil
		}
	}
	code := state.QualityCodePlanTraceabilityGap
	return []state.TicketQualityFinding{{
		ID:             makeFindingID(t.ID, code, nil),
		TicketID:       t.ID,
		Code:           string(code),
		Severity:       state.SeverityP2,
		Title:          "ticket references a plan/spec/RFC but provides no docs/plans/ link",
		Body:           "Add a docs/plans/ link in the ticket body or a plan_refs frontmatter entry so reviewers can trace requirements.",
		Repairable:     false,
		AutoRepairable: false,
		Disposition:    "open",
	}}
}

var reviewerGuidanceTrigger = regexp.MustCompile(`(?i)\bsecurity\b|\bmigration\b|\bbreaking\b|\bauth\b|\bpayment\b`)

// evalReviewerInstructionGap flags tickets that involve security, migrations,
// breaking changes, auth, or payments but provide no reviewer guidance.
func evalReviewerInstructionGap(t epos.Ticket) []state.TicketQualityFinding {
	combined := t.Title + " " + t.Body
	// Also check tags stored in UnknownFrontmatter["tags"].
	if t.UnknownFrontmatter != nil {
		if tags, ok := t.UnknownFrontmatter["tags"].([]any); ok {
			for _, tag := range tags {
				if s, ok := tag.(string); ok {
					combined += " " + s
				}
			}
		}
	}
	if !reviewerGuidanceTrigger.MatchString(combined) {
		return nil
	}
	// Check for existing reviewer guidance frontmatter.
	if t.UnknownFrontmatter != nil {
		for _, key := range []string{"reviewer_notes", "review_focus"} {
			if _, ok := t.UnknownFrontmatter[key]; ok {
				return nil
			}
		}
	}
	code := state.QualityCodeReviewerInstructionGap
	return []state.TicketQualityFinding{{
		ID:             makeFindingID(t.ID, code, nil),
		TicketID:       t.ID,
		Code:           string(code),
		Severity:       state.SeverityP3,
		Title:          "ticket involves security/auth/migration/payment but has no reviewer guidance",
		Body:           "Add reviewer_notes or review_focus frontmatter to guide reviewers on what to check.",
		Repairable:     false,
		AutoRepairable: false,
		Disposition:    "open",
	}}
}

func evalIntegrationGap(root epos.Ticket, all []epos.Ticket) []state.TicketQualityFinding {
	surfaces := map[string]bool{}
	for _, t := range all {
		if t.ID == root.ID {
			continue
		}
		for _, p := range t.OwnedPaths {
			switch {
			case strings.HasPrefix(p, "cmd/"), strings.HasPrefix(p, "internal/cli"):
				surfaces["cli"] = true
			case strings.HasPrefix(p, "policy/"), strings.HasPrefix(p, "config/"), strings.HasPrefix(p, "internal/policy"):
				surfaces["config"] = true
			case strings.HasPrefix(p, "internal/adapters/runtime"):
				surfaces["runtime"] = true
			case strings.HasPrefix(p, "docs/"):
				surfaces["docs"] = true
			case strings.HasPrefix(p, "internal/engine"):
				surfaces["engine"] = true
			case strings.HasSuffix(p, "_test.go"), strings.HasPrefix(p, "internal/e2e"):
				surfaces["tests"] = true
			}
		}
	}
	if len(surfaces) < 2 {
		return nil
	}
	for _, t := range all {
		if hasIntegrationMarker(t) {
			return nil
		}
	}
	keys := make([]string, 0, len(surfaces))
	for k := range surfaces {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	code := state.QualityCodeIntegrationGap
	return []state.TicketQualityFinding{{
		ID:             makeFindingID(root.ID, code, keys),
		TicketID:       root.ID,
		Code:           string(code),
		Severity:       state.SeverityP1,
		Title:          "multi-surface epic has no integration or traceability child ticket",
		Body:           "Add a child ticket whose title or body mentions integration, traceability, e2e, or end-to-end so the cross-surface behavior is verified.",
		Evidence:       keys,
		Repairable:     false,
		AutoRepairable: false,
		Disposition:    "open",
	}}
}

var integrationMarker = regexp.MustCompile(`(?i)\bintegration\b|\btraceability\b|\be2e\b|\bend[- ]to[- ]end\b`)

func hasIntegrationMarker(t epos.Ticket) bool {
	return integrationMarker.MatchString(t.Title) || integrationMarker.MatchString(t.Body)
}

// --- Advisory Findings From Promoted Rules ----------------------------------

// AdvisoryFindingsFromPromotedRules converts promoted memory rules into
// non-blocking advisory findings for the given ticket set. Rules are NEVER
// upgraded to blocking automatically — they appear as severity P3 (advisory)
// regardless of the original lesson. Use BuildPlannerReviewPrompt or the
// engine review pipeline to surface them.
func AdvisoryFindingsFromPromotedRules(rules []memory.PromotionEntry, tickets []epos.Ticket) []state.TicketQualityFinding {
	out := make([]state.TicketQualityFinding, 0, len(tickets)*len(rules))
	for _, r := range rules {
		for _, t := range tickets {
			out = append(out, state.TicketQualityFinding{
				ID:          makeFindingID(t.ID, state.TicketQualityCode("promoted_rule:"+r.RuleID), nil),
				TicketID:    t.ID,
				Code:        "promoted_rule:" + r.RuleID,
				Severity:    state.SeverityP3,
				Title:       fmt.Sprintf("lesson: %s", r.Summary),
				Body:        fmt.Sprintf("Promoted memory rule %s applies. Reviewer should verify the lesson is addressed.", r.RuleID),
				Repairable:  false,
				Disposition: "advisory",
			})
		}
	}
	return out
}

// --- Safe Auto-Repair -------------------------------------------------------

// TicketQualityRepairPlan describes a planned set of safe ticket repairs for a
// quality artifact. Tickets maps ticket id -> repaired ticket struct; callers
// can save those back to the ticket store. Repairs lists the repair records to
// attach to the artifact.
type TicketQualityRepairPlan struct {
	Tickets map[string]epos.Ticket
	Repairs []state.TicketQualityRepair
}

// BuildTicketQualityRepairPlan computes which safe repairs would resolve the
// findings in the artifact. It is pure: it does not write tickets or
// filesystem. Filesystem writes happen in the CLI/run integration layer.
//
// Safe repairs:
//   - epic gets union of child OwnedPaths only if all children have non-empty
//     OwnedPaths (otherwise the inferred union could be misleading)
//   - planner-required findings get an advisory ticket_quality_notes
//     frontmatter entry so future runs see the open finding
//
// Unsafe repairs (NOT applied):
//   - inventing acceptance criteria, public CLI scenarios, or test expectations
//   - splitting compound criteria
//   - rewriting docs body
func BuildTicketQualityRepairPlan(input TicketQualityInput, artifact state.TicketQualityArtifact) TicketQualityRepairPlan {
	plan := TicketQualityRepairPlan{Tickets: map[string]epos.Ticket{}}

	if isEpic(input.RootTicket) {
		if repaired, repair, ok := repairEpicOwnedPaths(input, artifact); ok {
			plan.Tickets[repaired.ID] = repaired
			plan.Repairs = append(plan.Repairs, repair)
		}
	}

	for _, f := range artifact.Findings {
		if !f.RequiresPlanner {
			continue
		}
		repaired, repair, ok := repairAddPlannerNote(input, f)
		if !ok {
			continue
		}
		if existing, present := plan.Tickets[repaired.ID]; present {
			repaired = mergeNotes(existing, repaired)
		}
		plan.Tickets[repaired.ID] = repaired
		plan.Repairs = append(plan.Repairs, repair)
	}

	return plan
}

// repairEpicOwnedPaths returns a repaired epic ticket with OwnedPaths set to
// the sorted, deduplicated union of all child OwnedPaths. Only applied when
// the epic itself has no OwnedPaths AND every child has non-empty OwnedPaths.
func repairEpicOwnedPaths(input TicketQualityInput, artifact state.TicketQualityArtifact) (epos.Ticket, state.TicketQualityRepair, bool) {
	root := input.RootTicket
	if len(root.OwnedPaths) > 0 {
		return epos.Ticket{}, state.TicketQualityRepair{}, false
	}
	children := childrenOf(input, root.ID)
	if len(children) == 0 {
		return epos.Ticket{}, state.TicketQualityRepair{}, false
	}
	union := map[string]bool{}
	for _, c := range children {
		if len(c.OwnedPaths) == 0 {
			return epos.Ticket{}, state.TicketQualityRepair{}, false
		}
		for _, p := range c.OwnedPaths {
			union[p] = true
		}
	}
	paths := make([]string, 0, len(union))
	for p := range union {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	repaired := root
	repaired.OwnedPaths = paths

	findingID := ""
	for _, f := range artifact.Findings {
		if f.TicketID == root.ID && f.Code == string(state.QualityCodeMissingOwnedPaths) {
			findingID = f.ID
			break
		}
	}
	return repaired, state.TicketQualityRepair{
		FindingID: findingID,
		TicketID:  root.ID,
		Kind:      "epic_owned_paths_union",
		Summary:   "set epic owned_paths to union of child owned_paths",
		Applied:   true,
	}, true
}

// repairAddPlannerNote attaches a ticket_quality_notes frontmatter entry that
// records a planner-required finding. This is informational only; it does not
// change any acceptance criteria, body text, or other semantic content.
func repairAddPlannerNote(input TicketQualityInput, f state.TicketQualityFinding) (epos.Ticket, state.TicketQualityRepair, bool) {
	t, ok := findTicket(input, f.TicketID)
	if !ok {
		return epos.Ticket{}, state.TicketQualityRepair{}, false
	}
	if t.UnknownFrontmatter == nil {
		t.UnknownFrontmatter = map[string]any{}
	}
	notes, _ := t.UnknownFrontmatter["ticket_quality_notes"].([]any)
	note := map[string]any{
		"code":    f.Code,
		"finding": f.ID,
		"title":   f.Title,
	}
	for _, n := range notes {
		if existing, ok := n.(map[string]any); ok {
			if existing["finding"] == f.ID {
				return epos.Ticket{}, state.TicketQualityRepair{}, false
			}
		}
	}
	notes = append(notes, note)
	t.UnknownFrontmatter["ticket_quality_notes"] = notes
	return t, state.TicketQualityRepair{
		FindingID: f.ID,
		TicketID:  f.TicketID,
		Kind:      "ticket_quality_note",
		Summary:   "record planner-required finding in ticket_quality_notes",
		Applied:   true,
	}, true
}

func childrenOf(input TicketQualityInput, rootID string) []epos.Ticket {
	var out []epos.Ticket
	for _, t := range input.Tickets {
		if t.ID == rootID {
			continue
		}
		out = append(out, t)
	}
	return out
}

func findTicket(input TicketQualityInput, id string) (epos.Ticket, bool) {
	if input.RootTicket.ID == id {
		return input.RootTicket, true
	}
	for _, t := range input.Tickets {
		if t.ID == id {
			return t, true
		}
	}
	return epos.Ticket{}, false
}

func mergeNotes(a, b epos.Ticket) epos.Ticket {
	if a.UnknownFrontmatter == nil {
		a.UnknownFrontmatter = map[string]any{}
	}
	aNotes, _ := a.UnknownFrontmatter["ticket_quality_notes"].([]any)
	bNotes, _ := b.UnknownFrontmatter["ticket_quality_notes"].([]any)
	merged := append([]any{}, aNotes...)
	for _, bn := range bNotes {
		dup := false
		bm, _ := bn.(map[string]any)
		for _, an := range merged {
			am, _ := an.(map[string]any)
			if am != nil && bm != nil && am["finding"] == bm["finding"] {
				dup = true
				break
			}
		}
		if !dup {
			merged = append(merged, bn)
		}
	}
	a.UnknownFrontmatter["ticket_quality_notes"] = merged
	return a
}
