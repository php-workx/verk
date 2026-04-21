package engine

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"verk/internal/adapters/ticketstore/tkmd"
	"verk/internal/state"
)

type StatusRequest struct {
	RepoRoot string
	RunID    string
}

type StatusTicket struct {
	TicketID                 string            `json:"ticket_id"`
	Title                    string            `json:"title,omitempty"`
	Phase                    state.TicketPhase `json:"phase"`
	EffectiveReviewThreshold state.Severity    `json:"effective_review_threshold,omitempty"`
	BlockReason              string            `json:"block_reason,omitempty"`
	Steps                    []StatusStep      `json:"steps,omitempty"`
	Failures                 []StatusFailure   `json:"failures,omitempty"`
	FailedGate               string            `json:"failed_gate,omitempty"`
	ClaimState               string            `json:"claim_state,omitempty"`
	LeaseID                  string            `json:"lease_id,omitempty"`
	ClaimDivergence          bool              `json:"claim_divergence"`
	ClaimDivergenceReason    string            `json:"claim_divergence_reason,omitempty"`
}

type StatusStep struct {
	Name          string                      `json:"name"`
	DurationMS    int64                       `json:"duration_ms,omitempty"`
	Runtime       string                      `json:"runtime,omitempty"`
	Model         string                      `json:"model,omitempty"`
	Reasoning     string                      `json:"reasoning,omitempty"`
	CommandCount  int                         `json:"command_count,omitempty"`
	TokenUsage    *state.RuntimeTokenUsage    `json:"token_usage,omitempty"`
	ActivityStats *state.RuntimeActivityStats `json:"activity_stats,omitempty"`
}

type StatusFailure struct {
	Kind       string `json:"kind"`
	Summary    string `json:"summary"`
	Detail     string `json:"detail,omitempty"`
	Command    string `json:"command,omitempty"`
	CheckID    string `json:"check_id,omitempty"`
	ExitCode   int    `json:"exit_code,omitempty"`
	StdoutPath string `json:"stdout_path,omitempty"`
	StderrPath string `json:"stderr_path,omitempty"`
}

type StatusReport struct {
	RunID                    string                `json:"run_id"`
	RootTicketID             string                `json:"root_ticket_id"`
	RunStatus                state.EpicRunStatus   `json:"run_status"`
	CurrentPhase             state.TicketPhase     `json:"current_phase"`
	CurrentWave              string                `json:"current_wave,omitempty"`
	EffectiveReviewThreshold state.Severity        `json:"effective_review_threshold,omitempty"`
	LastFailedGate           string                `json:"last_failed_gate,omitempty"`
	ClaimDivergence          bool                  `json:"claim_divergence"`
	Tickets                  []StatusTicket        `json:"tickets"`
	ActiveClaims             []state.ClaimArtifact `json:"active_claims,omitempty"`
}

func DeriveStatus(req StatusRequest) (StatusReport, error) {
	artifacts, err := loadRunArtifacts(req.RepoRoot, req.RunID)
	if err != nil {
		return StatusReport{}, err
	}

	ticketIDs := append([]string(nil), artifacts.Run.TicketIDs...)
	sort.Strings(ticketIDs)

	report := StatusReport{
		RunID:        artifacts.RunID,
		RootTicketID: artifacts.Run.RootTicketID,
		RunStatus:    artifacts.Run.Status,
		CurrentPhase: artifacts.Run.CurrentPhase,
		CurrentWave:  deriveCurrentWaveID(artifacts.Waves),
		Tickets:      make([]StatusTicket, 0, len(ticketIDs)),
	}

	for _, ticketID := range ticketIDs {
		snapshot := artifacts.Tickets[ticketID]
		plan := artifacts.Plans[ticketID]
		title := plan.Title
		if title == "" {
			title = loadTicketTitle(artifacts.RepoRoot, ticketID)
		}
		entry := StatusTicket{
			TicketID:                 ticketID,
			Title:                    title,
			Phase:                    snapshot.CurrentPhase,
			EffectiveReviewThreshold: plan.EffectiveReviewThreshold,
			BlockReason:              snapshot.BlockReason,
			Steps:                    deriveStatusSteps(snapshot),
			Failures:                 deriveTicketFailures(snapshot),
		}
		if snapshot.Closeout != nil {
			entry.FailedGate = snapshot.Closeout.FailedGate
			if report.LastFailedGate == "" && snapshot.Closeout.FailedGate != "" {
				report.LastFailedGate = snapshot.Closeout.FailedGate
			}
		}
		if plan.EffectiveReviewThreshold != "" {
			report.EffectiveReviewThreshold = mostRestrictiveThreshold(report.EffectiveReviewThreshold, plan.EffectiveReviewThreshold)
		}

		claim, claimErr := deriveTicketClaim(artifacts.RepoRoot, artifacts.RunID, ticketID, snapshot)
		if claimErr != nil {
			report.ClaimDivergence = true
			entry.ClaimDivergence = true
			entry.ClaimDivergenceReason = claimErr.Error()
			entry.ClaimState = "diverged"
		} else if claim != nil {
			entry.ClaimState = claim.State
			entry.LeaseID = claim.LeaseID
			if claim.State == "active" {
				report.ActiveClaims = append(report.ActiveClaims, *claim)
			}
		}
		report.Tickets = append(report.Tickets, entry)
	}

	sort.Slice(report.ActiveClaims, func(i, j int) bool {
		return report.ActiveClaims[i].TicketID < report.ActiveClaims[j].TicketID
	})
	return report, nil
}

func deriveStatusSteps(snapshot TicketRunSnapshot) []StatusStep {
	steps := make([]StatusStep, 0, 3)
	if artifact := snapshot.Implementation; artifact != nil {
		steps = append(steps, StatusStep{
			Name:          "worker",
			DurationMS:    durationMS(artifact.StartedAt, artifact.FinishedAt),
			Runtime:       artifact.Runtime,
			Model:         artifact.Model,
			Reasoning:     artifact.Reasoning,
			TokenUsage:    cloneStateTokenUsage(artifact.TokenUsage),
			ActivityStats: cloneStateActivityStats(artifact.ActivityStats),
		})
	}
	if artifact := snapshot.Verification; artifact != nil {
		steps = append(steps, StatusStep{
			Name:         "verification",
			DurationMS:   durationMS(artifact.StartedAt, artifact.FinishedAt),
			CommandCount: len(artifact.Results),
		})
	}
	if artifact := snapshot.Review; artifact != nil {
		steps = append(steps, StatusStep{
			Name:          "review",
			DurationMS:    durationMS(artifact.StartedAt, artifact.FinishedAt),
			Runtime:       artifact.ReviewerRuntime,
			Model:         artifact.ReviewerModel,
			Reasoning:     artifact.ReviewerReasoning,
			TokenUsage:    cloneStateTokenUsage(artifact.TokenUsage),
			ActivityStats: cloneStateActivityStats(artifact.ActivityStats),
		})
	}
	return steps
}

func durationMS(start, finish time.Time) int64 {
	if start.IsZero() || finish.IsZero() || finish.Before(start) {
		return 0
	}
	return finish.Sub(start).Milliseconds()
}

func cloneStateTokenUsage(usage *state.RuntimeTokenUsage) *state.RuntimeTokenUsage {
	if usage == nil {
		return nil
	}
	copy := *usage
	return &copy
}

func cloneStateActivityStats(stats *state.RuntimeActivityStats) *state.RuntimeActivityStats {
	if stats == nil {
		return nil
	}
	copy := *stats
	return &copy
}

func deriveTicketFailures(snapshot TicketRunSnapshot) []StatusFailure {
	failures := make([]StatusFailure, 0, 4)
	failures = append(failures, verificationFailures(snapshot.Verification)...)
	failures = append(failures, reviewFailures(snapshot.Review)...)
	failures = append(failures, repairFailures(snapshot.RepairCycles)...)
	failures = append(failures, closeoutFailures(snapshot.Closeout)...)
	return failures
}

func verificationFailures(artifact *state.VerificationArtifact) []StatusFailure {
	if artifact == nil || artifact.Passed {
		return nil
	}
	failures := make([]StatusFailure, 0)
	for i, result := range artifact.Results {
		if result.Passed {
			continue
		}
		command := strings.TrimSpace(result.Command)
		if command == "" && i < len(artifact.Commands) {
			command = strings.TrimSpace(artifact.Commands[i])
		}
		if command == "" {
			command = "verification command"
		}
		summary := fmt.Sprintf("verification: %s failed", command)
		switch {
		case result.TimedOut:
			summary = fmt.Sprintf("verification: %s timed out", command)
		case result.ExitCode != 0:
			summary = fmt.Sprintf("verification: %s failed with exit code %d", command, result.ExitCode)
		}
		failures = append(failures, StatusFailure{
			Kind:       "verification",
			Summary:    summary,
			Detail:     verificationFailureDetail(artifact, result),
			Command:    command,
			ExitCode:   result.ExitCode,
			StdoutPath: result.StdoutPath,
			StderrPath: result.StderrPath,
		})
	}
	if len(failures) == 0 {
		return []StatusFailure{{
			Kind:    "verification",
			Summary: "verification: artifact is marked failed but no failed command was recorded",
		}}
	}
	return failures
}

func verificationFailureDetail(artifact *state.VerificationArtifact, result state.VerificationResult) string {
	if artifact != nil && artifact.ValidationCoverage != nil {
		for _, exec := range artifact.ValidationCoverage.ExecutedChecks {
			if exec.Result != state.ValidationCheckResultFailed || exec.FailureSummary == "" {
				continue
			}
			if exec.StdoutPath == result.StdoutPath && exec.StderrPath == result.StderrPath {
				return exec.FailureSummary
			}
		}
	}
	if summary := summarizeCommandOutput(result.StdoutPath); summary != "" {
		return summary
	}
	return summarizeCommandOutput(result.StderrPath)
}

func summarizeCommandOutput(path string) string {
	if path == "" {
		return ""
	}
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	first := ""
	mdLineCount := 0
	mdFirstFile := ""
	mdFirstLine := ""
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if first == "" {
			first = line
		}
		if strings.Contains(line, " MD013/line-length ") {
			mdLineCount++
			if mdFirstFile == "" {
				mdFirstFile, mdFirstLine = markdownlintLocation(line)
			}
		}
	}
	if mdLineCount > 0 && mdFirstFile != "" && mdFirstLine != "" {
		return fmt.Sprintf("%s: %d line-length violation(s); first at line %s", mdFirstFile, mdLineCount, mdFirstLine)
	}
	return shortenFailureDetail(first)
}

func markdownlintLocation(line string) (string, string) {
	parts := strings.SplitN(line, ":", 3)
	if len(parts) < 2 {
		return "", ""
	}
	return parts[0], parts[1]
}

func shortenFailureDetail(detail string) string {
	const maxLen = 180
	if len(detail) <= maxLen {
		return detail
	}
	return detail[:maxLen-3] + "..."
}

func reviewFailures(artifact *state.ReviewFindingsArtifact) []StatusFailure {
	if artifact == nil || artifact.Passed || len(artifact.BlockingFindings) == 0 {
		return nil
	}
	return []StatusFailure{{
		Kind:    "review",
		Summary: fmt.Sprintf("review: blocking findings: %s", strings.Join(artifact.BlockingFindings, ", ")),
	}}
}

func repairFailures(cycles []state.RepairCycleArtifact) []StatusFailure {
	if len(cycles) == 0 {
		return nil
	}
	last := cycles[len(cycles)-1]
	if last.Status == "" || last.Status == "completed" || last.Status == "repaired" {
		return nil
	}
	parts := []string{fmt.Sprintf("repair: cycle %d is %s", last.Cycle, last.Status)}
	if len(last.TriggerCheckIDs) > 0 {
		parts = append(parts, fmt.Sprintf("checks: %s", strings.Join(last.TriggerCheckIDs, ", ")))
	}
	if len(last.TriggerFindingIDs) > 0 {
		parts = append(parts, fmt.Sprintf("findings: %s", strings.Join(last.TriggerFindingIDs, ", ")))
	}
	if last.PolicyLimitReached != nil && last.PolicyLimitReached.Reason != "" {
		parts = append(parts, last.PolicyLimitReached.Reason)
	}
	return []StatusFailure{{
		Kind:    "repair",
		Summary: strings.Join(parts, "; "),
	}}
}

func closeoutFailures(artifact *state.CloseoutArtifact) []StatusFailure {
	if artifact == nil || artifact.Closable {
		return nil
	}
	reason := strings.TrimSpace(artifact.BlockReason)
	if reason == "" {
		reason = strings.TrimSpace(artifact.FailedGate)
	}
	if reason == "" {
		return nil
	}
	failure := StatusFailure{
		Kind:    "closeout",
		Summary: "closeout: " + reason,
	}
	if artifact.UnresolvedCheckID != "" {
		failure.CheckID = artifact.UnresolvedCheckID
	}
	return []StatusFailure{failure}
}

func ticketStatusReason(snapshot TicketRunSnapshot) string {
	reason := strings.TrimSpace(snapshot.BlockReason)
	failures := deriveTicketFailures(snapshot)
	if len(failures) == 0 {
		return reason
	}
	if reason == "" || lowSignalBlockReason(reason, snapshot.CurrentPhase) {
		return conciseFailures(failures, 2)
	}
	return reason
}

func lowSignalBlockReason(reason string, phase state.TicketPhase) bool {
	if reason == defaultPhaseReason(phase) {
		return true
	}
	if strings.HasPrefix(reason, "worker blocked by operator input: {\"type\":") {
		return true
	}
	if reason == "verify" || reason == "verification" || reason == "review" {
		return true
	}
	return false
}

func conciseFailures(failures []StatusFailure, limit int) string {
	if len(failures) == 0 {
		return ""
	}
	if limit <= 0 || limit > len(failures) {
		limit = len(failures)
	}
	parts := make([]string, 0, limit+1)
	for _, failure := range failures[:limit] {
		parts = append(parts, failure.Summary)
	}
	if remaining := len(failures) - limit; remaining > 0 {
		parts = append(parts, fmt.Sprintf("%d more failure(s)", remaining))
	}
	return strings.Join(parts, "; ")
}

func loadTicketTitle(repoRoot, ticketID string) string {
	ticket, err := tkmd.LoadTicket(filepath.Join(repoRoot, ".tickets", ticketID+".md"))
	if err != nil {
		return ""
	}
	return ticket.Title
}

func deriveCurrentWaveID(waves map[string]state.WaveArtifact) string {
	current := state.WaveArtifact{}
	found := false
	for _, wave := range waves {
		if wave.Status == state.WaveStatusAccepted {
			continue
		}
		if !found || wave.Ordinal > current.Ordinal {
			current = wave
			found = true
		}
	}
	if found {
		return current.WaveID
	}
	return ""
}

func deriveTicketClaim(repoRoot, runID, ticketID string, snapshot TicketRunSnapshot) (*state.ClaimArtifact, error) {
	live, err := loadOptionalClaim(liveClaimPath(repoRoot, ticketID))
	if err != nil {
		return nil, err
	}
	durable, err := loadOptionalClaim(durableClaimPath(repoRoot, runID, ticketID))
	if err != nil {
		return nil, err
	}
	if live == nil && durable == nil {
		return nil, nil //nolint:nilnil // not-found: nil value + nil error = "no data, no problem"
	}
	claim, err := tkmd.ReconcileClaim(live, durable, runID, isTerminalPhase(snapshot.CurrentPhase))
	if err != nil {
		return nil, err
	}
	if snapshot.Implementation != nil && snapshot.Implementation.LeaseID != "" && claim.State == "active" && snapshot.Implementation.LeaseID != claim.LeaseID {
		return nil, fmt.Errorf("ticket %s lease mismatch between implementation artifact %q and claim %q", ticketID, snapshot.Implementation.LeaseID, claim.LeaseID)
	}
	return &claim, nil
}

// mostRestrictiveThreshold returns the more restrictive of two review thresholds.
// Precedence: strict > standard > lenient > ""(empty).
// If both are equal, either is returned. If one is empty, the other is returned.
func mostRestrictiveThreshold(a, b state.Severity) state.Severity {
	thresholdPrecedence := map[state.Severity]int{
		state.SeverityP0: 4, // strict
		"strict":         4,
		state.SeverityP1: 3, // standard
		"standard":       3,
		state.SeverityP2: 2, // lenient
		"lenient":        2,
		state.SeverityP3: 1,
		state.SeverityP4: 0,
	}
	aRank := thresholdPrecedence[a]
	bRank := thresholdPrecedence[b]
	if aRank >= bRank {
		return a
	}
	return b
}
