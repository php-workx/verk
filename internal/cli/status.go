package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"verk/internal/engine"
	"verk/internal/state"

	"github.com/spf13/cobra"
)

func initStatusCmd(root *cobra.Command) {
	var statusJSONFlag bool

	statusCmd := &cobra.Command{
		Use:          "status [run-id]",
		Short:        "Show run status",
		GroupID:      groupObserve,
		SilenceUsage: true,
		Args:         cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runID, err := resolveRunID(args)
			if err != nil {
				return cmdError(cmd, err, 1)
			}
			repoRoot, err := resolveRepoRoot()
			if err != nil {
				return cmdError(cmd, fmt.Errorf("resolve repo root: %w", err), 1)
			}

			report, err := engine.DeriveStatus(engine.StatusRequest{RepoRoot: repoRoot, RunID: runID})
			if err != nil {
				return cmdError(cmd, err, 1)
			}
			if statusJSONFlag {
				return printJSON(cmd.OutOrStdout(), report)
			}

			w := cmd.OutOrStdout()
			color := shouldColorizeFunc()
			r := doctorRenderer{color: color}

			_, _ = fmt.Fprintln(w, r.bold("verk status"))
			_, _ = fmt.Fprintln(w, r.dim(strings.Repeat("─", 40)))
			_, _ = fmt.Fprintln(w)

			_, _ = fmt.Fprintf(w, "  Run:    %s\n", report.RunID)
			runStatus := formatRunStatus(r, report.RunStatus)
			if report.RunStatus == state.EpicRunStatusRunning {
				if repoRoot != "" && !engine.IsRunLockHeld(repoRoot, report.RunID) {
					runStatus = r.fail("stale") + r.dim(" (process died — use 'verk run' to resume)")
				}
			}
			_, _ = fmt.Fprintf(w, "  Status: %s\n", runStatus)
			if report.CurrentWave != "" {
				_, _ = fmt.Fprintf(w, "  Wave:   %s\n", report.CurrentWave)
			}
			if report.LastFailedGate != "" {
				_, _ = fmt.Fprintf(w, "  Gate:   %s\n", r.fail(report.LastFailedGate))
			}

			_, _ = fmt.Fprintf(w, "\n  %s\n\n", r.bold("Tickets:"))

			tickets := sortTicketsByPhase(report.Tickets)
			closed, blocked, active, pending := 0, 0, 0, 0
			for _, ticket := range tickets {
				tag, tagFn := statusTag(ticket.Phase)
				title := ticket.TicketID
				if ticket.Title != "" {
					title = fmt.Sprintf("%-10s %s", ticket.TicketID, ticket.Title)
				}

				_, _ = fmt.Fprintf(w, "  %s %s\n", tagFn(r, tag), title)

				if ticket.BlockReason != "" {
					reason := shortenBlockReason(ticket.BlockReason)
					_, _ = fmt.Fprintf(w, "  %s %s\n", strings.Repeat(" ", len(tag)), r.dim(reason))
				}
				for _, step := range ticket.Steps {
					renderStatusStep(w, r, strings.Repeat(" ", len(tag)), step)
				}
				for _, failure := range ticket.Failures {
					renderStatusFailure(w, r, strings.Repeat(" ", len(tag)), failure)
				}

				switch ticket.Phase {
				case state.TicketPhaseClosed:
					closed++
				case state.TicketPhaseBlocked:
					blocked++
				case state.TicketPhaseImplement, state.TicketPhaseVerify, state.TicketPhaseReview, state.TicketPhaseRepair, state.TicketPhaseCloseout:
					active++
				default:
					pending++
				}
			}

			_, _ = fmt.Fprintln(w)
			parts := make([]string, 0, 4)
			if closed > 0 {
				parts = append(parts, fmt.Sprintf("%d closed", closed))
			}
			if active > 0 {
				parts = append(parts, fmt.Sprintf("%d active", active))
			}
			if blocked > 0 {
				parts = append(parts, fmt.Sprintf("%d blocked", blocked))
			}
			if pending > 0 {
				parts = append(parts, fmt.Sprintf("%d pending", pending))
			}
			if len(parts) > 0 {
				_, _ = fmt.Fprintf(w, "  %s\n", strings.Join(parts, ", "))
			}

			return nil
		},
	}

	statusCmd.Flags().BoolVar(&statusJSONFlag, "json", false, "Output as JSON")
	root.AddCommand(statusCmd)
}

func renderStatusStep(w io.Writer, r doctorRenderer, indent string, step engine.StatusStep) {
	if step.Name == "" {
		return
	}
	parts := []string{step.Name}
	if step.DurationMS > 0 {
		parts = append(parts, formatStatusDuration(step.DurationMS))
	}
	if step.Runtime != "" {
		profile := step.Runtime
		if step.Model != "" {
			profile += "/" + step.Model
		}
		if step.Reasoning != "" {
			profile += "/" + step.Reasoning
		}
		parts = append(parts, profile)
	}
	if step.CommandCount > 0 {
		parts = append(parts, fmt.Sprintf("%d command(s)", step.CommandCount))
	}
	if step.ActivityStats != nil {
		if step.ActivityStats.CommandCount > 0 {
			parts = append(parts, fmt.Sprintf("%d agent command(s)", step.ActivityStats.CommandCount))
		}
		if step.ActivityStats.AgentMessageCount > 0 {
			parts = append(parts, fmt.Sprintf("%d message(s)", step.ActivityStats.AgentMessageCount))
		}
	}
	if step.TokenUsage != nil {
		parts = append(parts, formatTokenUsage(step.TokenUsage.InputTokens, step.TokenUsage.CachedInputTokens, step.TokenUsage.OutputTokens))
	}
	_, _ = fmt.Fprintf(w, "  %s %s\n", indent, r.dim(strings.Join(parts, " · ")))
}

func renderStatusFailure(w io.Writer, r doctorRenderer, indent string, failure engine.StatusFailure) {
	if failure.Summary == "" {
		return
	}
	_, _ = fmt.Fprintf(w, "  %s %s\n", indent, r.dim(failure.Summary))
	if failure.Detail != "" {
		_, _ = fmt.Fprintf(w, "  %s %s\n", indent, r.dim(failure.Detail))
	}
}

func formatStatusDuration(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	seconds := ms / 1000
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	minutes := seconds / 60
	seconds %= 60
	return fmt.Sprintf("%dm%02ds", minutes, seconds)
}

func formatTokenUsage(input, cached, output int64) string {
	parts := []string{fmt.Sprintf("%s in", compactTokenCount(input))}
	if cached > 0 {
		parts = append(parts, fmt.Sprintf("%s cached", compactTokenCount(cached)))
	}
	parts = append(parts, fmt.Sprintf("%s out", compactTokenCount(output)))
	return strings.Join(parts, ", ")
}

func compactTokenCount(tokens int64) string {
	switch {
	case tokens >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(tokens)/1_000_000)
	case tokens >= 1_000:
		return fmt.Sprintf("%.1fk", float64(tokens)/1_000)
	default:
		return fmt.Sprintf("%d", tokens)
	}
}

type tagFormatter func(r doctorRenderer, tag string) string

func statusTag(phase state.TicketPhase) (string, tagFormatter) {
	switch phase {
	case state.TicketPhaseClosed:
		return "[CLOSED] ", func(r doctorRenderer, tag string) string { return r.ok(tag) }
	case state.TicketPhaseBlocked:
		return "[BLOCKED]", func(r doctorRenderer, tag string) string { return r.fail(tag) }
	case state.TicketPhaseImplement:
		return "[IMPL]   ", func(r doctorRenderer, tag string) string { return r.warn(tag) }
	case state.TicketPhaseVerify:
		return "[VERIFY] ", func(r doctorRenderer, tag string) string { return r.warn(tag) }
	case state.TicketPhaseReview:
		return "[REVIEW] ", func(r doctorRenderer, tag string) string { return r.warn(tag) }
	case state.TicketPhaseRepair:
		return "[REPAIR] ", func(r doctorRenderer, tag string) string { return r.warn(tag) }
	case state.TicketPhaseCloseout:
		return "[CLOSE]  ", func(r doctorRenderer, tag string) string { return r.warn(tag) }
	default:
		return "[PENDING]", func(r doctorRenderer, tag string) string { return r.dim(tag) }
	}
}

func formatRunStatus(r doctorRenderer, status state.EpicRunStatus) string {
	switch status {
	case state.EpicRunStatusCompleted:
		return r.ok(string(status))
	case state.EpicRunStatusBlocked:
		return r.fail(string(status))
	case state.EpicRunStatusRunning:
		return r.warn(string(status))
	default:
		return string(status)
	}
}

func phaseOrder(phase state.TicketPhase) int {
	switch phase {
	case state.TicketPhaseBlocked:
		return 0
	case state.TicketPhaseImplement, state.TicketPhaseVerify, state.TicketPhaseReview, state.TicketPhaseRepair, state.TicketPhaseCloseout:
		return 1
	case state.TicketPhaseClosed:
		return 3
	default:
		return 2 // intake/pending
	}
}

func sortTicketsByPhase(tickets []engine.StatusTicket) []engine.StatusTicket {
	sorted := append([]engine.StatusTicket(nil), tickets...)
	sort.SliceStable(sorted, func(i, j int) bool {
		oi, oj := phaseOrder(sorted[i].Phase), phaseOrder(sorted[j].Phase)
		if oi != oj {
			return oi < oj
		}
		return sorted[i].TicketID < sorted[j].TicketID
	})
	return sorted
}

func shortenBlockReason(reason string) string {
	// Strip nested prefixes that add no information
	prefixes := []string{
		"retryable worker failure after 2 retries: ",
		"retryable reviewer failure after 2 retries: ",
		"retryable worker failure: ",
		"retryable reviewer failure: ",
	}
	changed := true
	for changed {
		changed = false
		for _, prefix := range prefixes {
			if strings.HasPrefix(reason, prefix) {
				reason = strings.TrimPrefix(reason, prefix)
				changed = true
			}
		}
	}
	// Collapse the claim-renewal sentinel emitted by ticket execution.
	if strings.HasPrefix(reason, "claim renewal failed:") {
		reason = "lease expired"
	}
	if strings.HasPrefix(reason, "worker blocked by operator input: {\"type\":") {
		reason = "worker blocked by operator input from runtime event stream"
	}
	if len(reason) > 72 {
		reason = reason[:69] + "..."
	}
	return reason
}
