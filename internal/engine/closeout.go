package engine

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"verk/internal/adapters/runtime"
	"verk/internal/adapters/ticketstore/tkmd"
	"verk/internal/state"
)

const (
	gateCriteriaEvidence  = "criteria_evidence"
	gateVerification      = "verification"
	gateRequiredArtifacts = "required_artifacts"
	gateReview            = "review"
	gateDeclaredChecks    = "declared_checks"
	gateArtifactIntegrity = "artifact_integrity"
	gatePassed            = "passed"
	gateFailed            = "failed"
	defaultEvidenceSource = "verification.json"
	defaultReviewSource   = "review-findings.json"
	defaultArtifactSource = "artifact.json"
	defaultCriteriaPrefix = "criterion-"
)

type closeoutRequest struct {
	ticket            tkmd.Ticket
	plan              state.PlanArtifact
	verification      *state.VerificationArtifact
	review            *state.ReviewFindingsArtifact
	criteriaEvidence  []state.CriteriaEvidence
	requiredArtifacts []string
}

func ReviewFindingBlocks(f any, threshold state.Severity) bool {
	finding, ok := normalizeReviewFinding(f)
	if !ok {
		return false
	}
	if finding.Disposition != "open" {
		return false
	}
	if !severityBlocksAtOrAbove(finding.Severity, threshold) {
		return false
	}
	return true
}

func normalizeReviewFinding(f any) (state.ReviewFinding, bool) {
	switch v := f.(type) {
	case state.ReviewFinding:
		return v, true
	case *state.ReviewFinding:
		if v == nil {
			return state.ReviewFinding{}, false
		}
		return *v, true
	case runtime.ReviewFinding:
		return state.ReviewFinding{
			ID:              v.ID,
			Severity:        state.Severity(v.Severity),
			Title:           v.Title,
			Body:            v.Body,
			File:            v.File,
			Line:            v.Line,
			Disposition:     string(v.Disposition),
			WaivedBy:        v.WaivedBy,
			WaivedAt:        v.WaivedAt,
			WaiverReason:    v.WaiverReason,
			WaiverExpiresAt: derefTime(v.WaiverExpiresAt),
		}, true
	case *runtime.ReviewFinding:
		if v == nil {
			return state.ReviewFinding{}, false
		}
		return normalizeReviewFinding(*v)
	default:
		return state.ReviewFinding{}, false
	}
}

func derefTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

func DeriveGateResults(args ...any) (map[string]state.GateResult, error) {
	req, err := parseCloseoutRequest(args...)
	if err != nil {
		return nil, err
	}

	if len(req.criteriaEvidence) == 0 {
		req.criteriaEvidence = deriveCriteriaEvidence(req)
	}

	return deriveGateResults(req)
}

func BuildCloseoutArtifact(args ...any) (state.CloseoutArtifact, error) {
	req, err := parseCloseoutRequest(args...)
	if err != nil {
		return state.CloseoutArtifact{}, err
	}

	if len(req.criteriaEvidence) == 0 {
		req.criteriaEvidence = deriveCriteriaEvidence(req)
	}

	gates, err := deriveGateResults(req)
	if err != nil {
		return state.CloseoutArtifact{}, err
	}
	if result := gates[gateArtifactIntegrity]; result.Status == gateFailed {
		return state.CloseoutArtifact{}, fmt.Errorf("%s", result.Reason)
	}

	closable := true
	failedGate := ""
	for _, name := range []string{gateArtifactIntegrity, gateCriteriaEvidence, gateVerification, gateRequiredArtifacts, gateReview, gateDeclaredChecks} {
		result := gates[name]
		if result.Status == gateFailed {
			closable = false
			failedGate = name
			break
		}
	}

	criteriaEvidence := req.criteriaEvidence

	runID, err := resolveCloseoutRunID(req, criteriaEvidence)
	if err != nil {
		return state.CloseoutArtifact{}, err
	}

	return state.CloseoutArtifact{
		ArtifactMeta: state.ArtifactMeta{
			SchemaVersion: artifactSchemaVersion,
			RunID:         runID,
			CreatedAt:     stateTime(),
			UpdatedAt:     stateTime(),
		},
		TicketID:          req.ticket.ID,
		CriteriaEvidence:  criteriaEvidence,
		RequiredArtifacts: append([]string(nil), req.requiredArtifacts...),
		GateResults:       gates,
		Closable:          closable,
		FailedGate:        failedGate,
	}, nil
}

func parseCloseoutRequest(args ...any) (closeoutRequest, error) {
	var req closeoutRequest

	for _, arg := range args {
		switch v := arg.(type) {
		case tkmd.Ticket:
			req.ticket = v
		case *tkmd.Ticket:
			if v != nil {
				req.ticket = *v
			}
		case state.PlanArtifact:
			req.plan = v
		case *state.PlanArtifact:
			if v != nil {
				req.plan = *v
			}
		case state.VerificationArtifact:
			tmp := v
			req.verification = &tmp
		case *state.VerificationArtifact:
			req.verification = v
		case state.ReviewFindingsArtifact:
			tmp := v
			req.review = &tmp
		case *state.ReviewFindingsArtifact:
			req.review = v
		case []state.CriteriaEvidence:
			req.criteriaEvidence = append([]state.CriteriaEvidence(nil), v...)
		case []string:
			req.requiredArtifacts = append([]string(nil), v...)
		}
	}

	if req.ticket.ID == "" {
		return closeoutRequest{}, fmt.Errorf("closeout requires ticket metadata")
	}
	if req.plan.TicketID == "" {
		return closeoutRequest{}, fmt.Errorf("closeout requires plan artifact")
	}
	if req.plan.TicketID != req.ticket.ID {
		return closeoutRequest{}, fmt.Errorf("plan ticket %q does not match ticket %q", req.plan.TicketID, req.ticket.ID)
	}

	return req, nil
}

func deriveGateResults(req closeoutRequest) (map[string]state.GateResult, error) {
	gates := map[string]state.GateResult{
		gateArtifactIntegrity: {Status: gatePassed, Reason: "artifacts are internally consistent"},
		gateCriteriaEvidence:  {Status: gatePassed, Reason: "all criteria have evidence"},
		gateVerification:      {Status: gatePassed, Reason: "verification passed"},
		gateRequiredArtifacts: {Status: gatePassed, Reason: "required artifacts present"},
		gateReview:            {Status: gatePassed, Reason: "no blocking review findings"},
		gateDeclaredChecks:    {Status: gatePassed, Reason: "no declared checks failed"},
	}

	currentRunID := req.plan.RunID
	if currentRunID == "" && req.verification != nil {
		currentRunID = req.verification.RunID
	}
	if currentRunID == "" && req.review != nil {
		currentRunID = req.review.RunID
	}
	if currentRunID == "" {
		currentRunID = resolveEvidenceRunID(req.criteriaEvidence)
	}

	if err := validateArtifactIntegrity(req, currentRunID); err != nil {
		gates[gateArtifactIntegrity] = state.GateResult{
			Status: gateFailed,
			Reason: err.Error(),
		}
		return gates, nil
	}

	if err := validateCriteriaEvidence(req, currentRunID); err != nil {
		gates[gateCriteriaEvidence] = state.GateResult{
			Status:        gateFailed,
			Reason:        err.Error(),
			ArtifactPaths: evidenceArtifactRefs(req.criteriaEvidence),
		}
		return gates, nil
	}

	if err := validateVerification(req); err != nil {
		gates[gateVerification] = state.GateResult{
			Status:        gateFailed,
			Reason:        err.Error(),
			ArtifactPaths: verificationArtifactPaths(req.verification),
		}
		return gates, nil
	}

	if err := validateRequiredArtifacts(req); err != nil {
		gates[gateRequiredArtifacts] = state.GateResult{
			Status:        gateFailed,
			Reason:        err.Error(),
			ArtifactPaths: append([]string(nil), req.requiredArtifacts...),
		}
		return gates, nil
	}

	if result, err := validateReview(req); err != nil {
		gates[gateReview] = state.GateResult{
			Status:        gateFailed,
			Reason:        err.Error(),
			FindingIDs:    reviewFindingIDs(req.review),
			ArtifactPaths: reviewArtifactPaths(req.review),
		}
		return gates, nil
	} else {
		gates[gateReview] = result
	}

	if err := validateDeclaredChecks(req); err != nil {
		gates[gateDeclaredChecks] = state.GateResult{
			Status: gateFailed,
			Reason: err.Error(),
		}
		return gates, nil
	}

	return gates, nil
}

func validateArtifactIntegrity(req closeoutRequest, currentRunID string) error {
	if req.plan.EffectiveReviewThreshold == "" {
		return fmt.Errorf("plan missing effective review threshold")
	}
	if err := validateSeverity(req.plan.EffectiveReviewThreshold); err != nil {
		return fmt.Errorf("plan effective review threshold invalid: %w", err)
	}
	if req.review != nil {
		if req.review.TicketID != "" && req.review.TicketID != req.ticket.ID {
			return fmt.Errorf("review artifact ticket %q does not match ticket %q", req.review.TicketID, req.ticket.ID)
		}
		if req.review.TicketID == "" {
			return fmt.Errorf("review artifact missing ticket_id")
		}
		if req.review.EffectiveReviewThreshold != "" && req.review.EffectiveReviewThreshold != req.plan.EffectiveReviewThreshold {
			return fmt.Errorf("review artifact threshold %q does not match plan threshold %q", req.review.EffectiveReviewThreshold, req.plan.EffectiveReviewThreshold)
		}
		if req.review.RunID != "" && currentRunID != "" && req.review.RunID != currentRunID {
			return fmt.Errorf("review artifact run %q does not match current run %q", req.review.RunID, currentRunID)
		}
		for _, finding := range req.review.Findings {
			if err := validateReviewFinding(finding); err != nil {
				return err
			}
		}
		if req.review.Passed != derivedReviewPassed(req.review.Findings, req.plan.EffectiveReviewThreshold) {
			return fmt.Errorf("review artifact passed flag contradicts derived review outcome")
		}
	}
	if req.verification != nil {
		if req.verification.TicketID != "" && req.verification.TicketID != req.ticket.ID {
			return fmt.Errorf("verification artifact ticket %q does not match ticket %q", req.verification.TicketID, req.ticket.ID)
		}
		if req.verification.TicketID == "" {
			return fmt.Errorf("verification artifact missing ticket_id")
		}
		if req.verification.RunID != "" && currentRunID != "" && req.verification.RunID != currentRunID {
			return fmt.Errorf("verification artifact run %q does not match current run %q", req.verification.RunID, currentRunID)
		}
		if req.verification.Passed != derivedVerificationPassed(req.verification.Results) {
			return fmt.Errorf("verification artifact passed flag contradicts derived verification outcome")
		}
	}
	for _, evidence := range req.criteriaEvidence {
		if err := validateCriteriaEvidenceEntry(evidence, currentRunID, req.ticket.ID); err != nil {
			return err
		}
	}
	return nil
}

func validateCriteriaEvidence(req closeoutRequest, currentRunID string) error {
	if len(req.criteriaEvidence) == 0 {
		return nil
	}
	seen := map[string]bool{}
	for _, evidence := range req.criteriaEvidence {
		if err := validateCriteriaEvidenceEntry(evidence, currentRunID, req.ticket.ID); err != nil {
			return err
		}
		seen[evidence.CriterionID] = true
		seen[strings.TrimSpace(evidence.CriterionText)] = true
	}
	for _, criterion := range planCriteria(req.plan) {
		if !seen[criterion.ID] && !seen[strings.TrimSpace(criterion.Text)] {
			return fmt.Errorf("missing evidence for criterion %q", criterion.ID)
		}
	}
	return nil
}

func validateCriteriaEvidenceEntry(evidence state.CriteriaEvidence, currentRunID, ticketID string) error {
	if evidence.CriterionID == "" {
		return fmt.Errorf("criteria evidence missing criterion_id")
	}
	if evidence.CriterionText == "" {
		return fmt.Errorf("criteria evidence %q missing criterion_text", evidence.CriterionID)
	}
	switch evidence.EvidenceType {
	case "verification", "artifact", "diff", "review":
	default:
		return fmt.Errorf("criteria evidence %q has unsupported evidence_type %q", evidence.CriterionID, evidence.EvidenceType)
	}
	if evidence.Source == "" {
		return fmt.Errorf("criteria evidence %q missing source", evidence.CriterionID)
	}
	if evidence.Summary == "" {
		return fmt.Errorf("criteria evidence %q missing summary", evidence.CriterionID)
	}
	if evidence.RunID == "" {
		return fmt.Errorf("criteria evidence %q missing run_id", evidence.CriterionID)
	}
	if currentRunID != "" && evidence.RunID != currentRunID {
		return fmt.Errorf("criteria evidence %q run %q does not match current run %q", evidence.CriterionID, evidence.RunID, currentRunID)
	}
	if evidence.TicketID != ticketID {
		return fmt.Errorf("criteria evidence %q ticket %q does not match ticket %q", evidence.CriterionID, evidence.TicketID, ticketID)
	}
	if evidence.Attempt <= 0 {
		return fmt.Errorf("criteria evidence %q missing attempt", evidence.CriterionID)
	}
	if evidence.ArtifactRef == "" {
		return fmt.Errorf("criteria evidence %q missing artifact_ref", evidence.CriterionID)
	}
	return nil
}

func validateVerification(req closeoutRequest) error {
	if req.verification == nil {
		return fmt.Errorf("missing verification artifact")
	}
	if req.verification.TicketID != req.ticket.ID {
		return fmt.Errorf("verification artifact ticket %q does not match ticket %q", req.verification.TicketID, req.ticket.ID)
	}
	if !derivedVerificationPassed(req.verification.Results) {
		return fmt.Errorf("verification artifact did not pass")
	}
	return nil
}

func validateRequiredArtifacts(req closeoutRequest) error {
	if len(req.requiredArtifacts) == 0 {
		return nil
	}
	available := map[string]struct{}{}
	for _, evidence := range req.criteriaEvidence {
		available[evidence.ArtifactRef] = struct{}{}
	}
	if req.verification != nil {
		available[defaultEvidenceSource] = struct{}{}
	}
	if req.review != nil {
		available[defaultReviewSource] = struct{}{}
	}
	missing := make([]string, 0)
	for _, artifact := range req.requiredArtifacts {
		if artifact == "" {
			continue
		}
		if _, ok := available[artifact]; !ok {
			missing = append(missing, artifact)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("missing required artifacts: %s", strings.Join(missing, ", "))
	}
	return nil
}

func validateReview(req closeoutRequest) (state.GateResult, error) {
	if req.review == nil {
		return state.GateResult{
			Status: gateFailed,
			Reason: "missing review artifact",
		}, fmt.Errorf("missing review artifact")
	}
	if req.review.TicketID != req.ticket.ID {
		return state.GateResult{}, fmt.Errorf("review artifact ticket %q does not match ticket %q", req.review.TicketID, req.ticket.ID)
	}

	threshold := req.plan.EffectiveReviewThreshold
	blocking := make([]string, 0)
	for _, finding := range req.review.Findings {
		if ReviewFindingBlocks(finding, threshold) {
			blocking = append(blocking, finding.ID)
		}
	}
	if len(blocking) > 0 {
		sort.Strings(blocking)
		return state.GateResult{
			Status:        gateFailed,
			Reason:        fmt.Sprintf("blocking review findings at or above %s: %s", threshold, strings.Join(blocking, ", ")),
			FindingIDs:    blocking,
			ArtifactPaths: reviewArtifactPaths(req.review),
		}, nil
	}

	return state.GateResult{
		Status:        gatePassed,
		Reason:        "no blocking review findings",
		FindingIDs:    reviewFindingIDs(req.review),
		ArtifactPaths: reviewArtifactPaths(req.review),
	}, nil
}

func validateDeclaredChecks(req closeoutRequest) error {
	if len(req.plan.DeclaredChecks) == 0 {
		return nil
	}
	if req.verification == nil {
		return fmt.Errorf("declared checks require verification artifacts")
	}
	if !derivedVerificationPassed(req.verification.Results) {
		return fmt.Errorf("declared checks failed because verification did not pass")
	}
	return nil
}

func validateReviewFinding(f state.ReviewFinding) error {
	if f.ID == "" {
		return fmt.Errorf("review finding missing id")
	}
	if err := validateSeverity(f.Severity); err != nil {
		return fmt.Errorf("review finding %q has invalid severity: %w", f.ID, err)
	}
	if strings.TrimSpace(f.Title) == "" {
		return fmt.Errorf("review finding %q missing title", f.ID)
	}
	if strings.TrimSpace(f.Body) == "" {
		return fmt.Errorf("review finding %q missing body", f.ID)
	}
	if strings.TrimSpace(f.File) == "" {
		return fmt.Errorf("review finding %q missing file", f.ID)
	}
	if f.Line <= 0 {
		return fmt.Errorf("review finding %q missing line", f.ID)
	}
	switch f.Disposition {
	case "open", "resolved":
		return nil
	case "waived":
		if strings.TrimSpace(f.WaivedBy) == "" {
			return fmt.Errorf("waived review finding %q missing waived_by", f.ID)
		}
		if f.WaivedAt.IsZero() {
			return fmt.Errorf("waived review finding %q missing waived_at", f.ID)
		}
		if strings.TrimSpace(f.WaiverReason) == "" {
			return fmt.Errorf("waived review finding %q missing waiver_reason", f.ID)
		}
		return nil
	default:
		return fmt.Errorf("review finding %q has invalid disposition %q", f.ID, f.Disposition)
	}
}

func derivedReviewPassed(findings []state.ReviewFinding, threshold state.Severity) bool {
	for _, finding := range findings {
		if ReviewFindingBlocks(finding, threshold) {
			return false
		}
	}
	return true
}

func derivedVerificationPassed(results []state.VerificationResult) bool {
	if len(results) == 0 {
		return false
	}
	for _, result := range results {
		if !result.Passed || result.ExitCode != 0 {
			return false
		}
	}
	return true
}

func severityBlocksAtOrAbove(severity, threshold state.Severity) bool {
	return severityRank(severity) <= severityRank(threshold)
}

func severityRank(severity state.Severity) int {
	switch severity {
	case state.SeverityP0:
		return 0
	case state.SeverityP1:
		return 1
	case state.SeverityP2:
		return 2
	case state.SeverityP3:
		return 3
	case state.SeverityP4:
		return 4
	default:
		return 99
	}
}

func reviewFindingIDs(review *state.ReviewFindingsArtifact) []string {
	if review == nil {
		return nil
	}
	ids := make([]string, 0, len(review.Findings))
	for _, finding := range review.Findings {
		ids = append(ids, finding.ID)
	}
	sort.Strings(ids)
	return ids
}

func reviewArtifactPaths(review *state.ReviewFindingsArtifact) []string {
	if review == nil {
		return nil
	}
	return []string{defaultReviewSource}
}

func verificationArtifactPaths(verification *state.VerificationArtifact) []string {
	if verification == nil {
		return nil
	}
	return []string{defaultEvidenceSource}
}

func evidenceArtifactRefs(evidence []state.CriteriaEvidence) []string {
	refs := make([]string, 0, len(evidence))
	for _, entry := range evidence {
		refs = append(refs, entry.ArtifactRef)
	}
	sort.Strings(refs)
	return refs
}

func resolveEvidenceRunID(evidence []state.CriteriaEvidence) string {
	for _, entry := range evidence {
		if strings.TrimSpace(entry.RunID) != "" {
			return strings.TrimSpace(entry.RunID)
		}
	}
	return ""
}

func resolveCloseoutRunID(req closeoutRequest, evidence []state.CriteriaEvidence) (string, error) {
	current := req.plan.RunID
	if current == "" && req.verification != nil {
		current = req.verification.RunID
	}
	if current == "" && req.review != nil {
		current = req.review.RunID
	}
	if current == "" {
		current = resolveEvidenceRunID(evidence)
	}
	if current == "" {
		return "", nil
	}
	for _, entry := range evidence {
		if entry.RunID != "" && entry.RunID != current {
			return "", fmt.Errorf("criteria evidence %q run %q does not match current run %q", entry.CriterionID, entry.RunID, current)
		}
	}
	if req.verification != nil && req.verification.RunID != "" && req.verification.RunID != current {
		return "", fmt.Errorf("verification artifact run %q does not match current run %q", req.verification.RunID, current)
	}
	if req.review != nil && req.review.RunID != "" && req.review.RunID != current {
		return "", fmt.Errorf("review artifact run %q does not match current run %q", req.review.RunID, current)
	}
	return current, nil
}

func deriveCriteriaEvidence(req closeoutRequest) []state.CriteriaEvidence {
	criteria := planCriteria(req.plan)
	count := len(criteria)
	if count == 0 {
		return nil
	}

	sourceType := "artifact"
	sourceRef := defaultArtifactSource
	summary := "criteria satisfied by plan artifacts"
	if req.verification != nil && derivedVerificationPassed(req.verification.Results) {
		sourceType = "verification"
		sourceRef = defaultEvidenceSource
		summary = verificationSummary(req.verification)
	} else if req.review != nil && derivedReviewPassed(req.review.Findings, req.plan.EffectiveReviewThreshold) {
		sourceType = "review"
		sourceRef = defaultReviewSource
		summary = req.review.Summary
		if strings.TrimSpace(summary) == "" {
			summary = "review passed"
		}
	}

	out := make([]state.CriteriaEvidence, 0, count)
	for i, criterion := range criteria {
		out = append(out, state.CriteriaEvidence{
			CriterionID:   criterion.ID,
			CriterionText: criterion.Text,
			EvidenceType:  sourceType,
			Source:        sourceRef,
			Summary:       summary,
			RunID:         currentRunIDForEvidence(req),
			TicketID:      req.ticket.ID,
			Attempt:       evidenceAttempt(req),
			ArtifactRef:   fmt.Sprintf("%s#%d", sourceRef, i+1),
		})
	}
	return out
}

func planCriteria(plan state.PlanArtifact) []state.PlanCriterion {
	if len(plan.Criteria) > 0 {
		return append([]state.PlanCriterion(nil), plan.Criteria...)
	}
	out := make([]state.PlanCriterion, 0, len(plan.AcceptanceCriteria))
	for _, criterion := range plan.AcceptanceCriteria {
		trimmed := strings.TrimSpace(criterion)
		if trimmed == "" {
			continue
		}
		out = append(out, state.PlanCriterion{
			ID:   criterionEvidenceID(trimmed),
			Text: trimmed,
		})
	}
	return out
}

func currentRunIDForEvidence(req closeoutRequest) string {
	if req.plan.RunID != "" {
		return req.plan.RunID
	}
	if req.verification != nil && req.verification.RunID != "" {
		return req.verification.RunID
	}
	if req.review != nil && req.review.RunID != "" {
		return req.review.RunID
	}
	return resolveEvidenceRunID(req.criteriaEvidence)
}

func evidenceAttempt(req closeoutRequest) int {
	if req.verification != nil && req.verification.Attempt > 0 {
		return req.verification.Attempt
	}
	if req.review != nil && req.review.Attempt > 0 {
		return req.review.Attempt
	}
	for _, entry := range req.criteriaEvidence {
		if entry.Attempt > 0 {
			return entry.Attempt
		}
	}
	return 1
}

func verificationSummary(verification *state.VerificationArtifact) string {
	if verification == nil {
		return "verification passed"
	}
	if len(verification.Commands) > 0 {
		return fmt.Sprintf("verification passed for %d command(s): %s", len(verification.Commands), strings.Join(verification.Commands, ", "))
	}
	if len(verification.Results) > 0 {
		return fmt.Sprintf("verification passed with %d result(s)", len(verification.Results))
	}
	return "verification passed"
}

func criterionEvidenceID(criterion string) string {
	trimmed := strings.TrimSpace(criterion)
	if trimmed == "" {
		return defaultCriteriaPrefix + "empty"
	}
	return defaultCriteriaPrefix + strings.NewReplacer(" ", "-", "\t", "-", "/", "-", "_", "-", ":", "-", ".", "-", ",", "-", "(", "", ")", "", "[", "", "]", "").Replace(strings.ToLower(trimmed))
}
