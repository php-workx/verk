package engine

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"verk/internal/adapters/ticketstore/epos"
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
		findings = append(findings, evalMissingOwnedPaths(t)...)
		findings = append(findings, evalDependencyMissing(t, idSet)...)
		findings = append(findings, evalPublicContractScenario(t)...)
		findings = append(findings, evalDocsDescopeRisk(t)...)
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

	return state.TicketQualityArtifact{
		Scope:        scope,
		RootTicketID: input.RootTicket.ID,
		TicketIDs:    ticketIDs,
		Status:       status,
		Findings:     findings,
		Blocked:      blocked,
		BlockReason:  blockReason,
	}
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
// Defaults to P2 (blocks P0/P1/P2; warns on P3/P4) until policy config grows
// a ticket_quality section.
func blockThreshold(_ policy.Config) state.Severity {
	return state.SeverityP2
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
