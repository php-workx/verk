package cli

import (
	"fmt"
	"strings"
	"time"
	"verk/internal/memory"

	"github.com/spf13/cobra"
)

func initLearnCmd(root *cobra.Command) {
	learnCmd := &cobra.Command{
		Use:     "learn",
		Short:   "Capture and manage lessons learned from escaped defects",
		GroupID: groupObserve,
	}

	learnCmd.AddCommand(
		newLearnEscapedCmd(),
		newLearnListCmd(),
		newLearnShowCmd(),
		newLearnPromoteCmd(),
	)

	root.AddCommand(learnCmd)
}

func newLearnEscapedCmd() *cobra.Command {
	var (
		runID           string
		summary         string
		missedBy        string
		sourceTickets   string
		recommendedRule string
	)

	cmd := &cobra.Command{
		Use:          "escaped",
		Short:        "Record an escaped defect lesson",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if summary == "" {
				return cmdError(cmd, fmt.Errorf("--summary is required"), 2)
			}
			if missedBy == "" {
				return cmdError(cmd, fmt.Errorf("--missed-by is required"), 2)
			}

			missedByList := splitComma(missedBy)
			for _, mb := range missedByList {
				if !memory.ValidMissedBy[mb] {
					return cmdError(cmd, fmt.Errorf("unknown --missed-by value %q", mb), 2)
				}
			}

			repoRoot, err := resolveRepoRoot()
			if err != nil {
				return cmdError(cmd, fmt.Errorf("resolve repo root: %w", err), 1)
			}

			id := fmt.Sprintf("learn-%d", time.Now().UnixNano())
			lesson := memory.EscapedDefect{
				ID:              id,
				CreatedAt:       time.Now().UTC(),
				SourceRunID:     runID,
				Summary:         summary,
				MissedBy:        missedByList,
				RecommendedRule: recommendedRule,
				Status:          memory.StatusProposed,
			}
			if sourceTickets != "" {
				lesson.SourceTicketIDs = splitComma(sourceTickets)
			}

			memDir := memoryDir(repoRoot)
			if err := memory.AppendLesson(memDir, lesson); err != nil {
				return cmdError(cmd, fmt.Errorf("record lesson: %w", err), 1)
			}

			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "lesson recorded: %s\n", id)
			return nil
		},
	}

	cmd.Flags().StringVar(&runID, "run", "", "Source run ID")
	cmd.Flags().StringVar(&summary, "summary", "", "Summary of the escaped defect (required)")
	cmd.Flags().StringVar(&missedBy, "missed-by", "", "Comma-separated missed-by values (required)")
	cmd.Flags().StringVar(&sourceTickets, "source-tickets", "", "Comma-separated source ticket IDs")
	cmd.Flags().StringVar(&recommendedRule, "recommended-rule", "", "Recommended rule to prevent recurrence")

	return cmd
}

func newLearnListCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:          "list",
		Short:        "List recorded lessons",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			repoRoot, err := resolveRepoRoot()
			if err != nil {
				return cmdError(cmd, fmt.Errorf("resolve repo root: %w", err), 1)
			}

			lessons, err := memory.ListLessons(memoryDir(repoRoot))
			if err != nil {
				return cmdError(cmd, fmt.Errorf("list lessons: %w", err), 1)
			}

			if jsonOutput {
				if lessons == nil {
					lessons = []memory.EscapedDefect{}
				}
				return printJSON(cmd.OutOrStdout(), lessons)
			}

			if len(lessons) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "no lessons recorded")
				return nil
			}

			w := cmd.OutOrStdout()
			for _, l := range lessons {
				summary := l.Summary
				if len(summary) > 60 {
					summary = summary[:57] + "..."
				}
				_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", l.ID, l.Status, summary)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON array")
	return cmd
}

func newLearnShowCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:          "show <lesson-id>",
		Short:        "Show details of a lesson",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			repoRoot, err := resolveRepoRoot()
			if err != nil {
				return cmdError(cmd, fmt.Errorf("resolve repo root: %w", err), 1)
			}

			lesson, found, err := memory.GetLesson(memoryDir(repoRoot), args[0])
			if err != nil {
				return cmdError(cmd, fmt.Errorf("get lesson: %w", err), 1)
			}
			if !found {
				return cmdError(cmd, fmt.Errorf("lesson %q not found", args[0]), 1)
			}

			if jsonOutput {
				return printJSON(cmd.OutOrStdout(), lesson)
			}

			w := cmd.OutOrStdout()
			_, _ = fmt.Fprintf(w, "id:               %s\n", lesson.ID)
			_, _ = fmt.Fprintf(w, "status:           %s\n", lesson.Status)
			_, _ = fmt.Fprintf(w, "created_at:       %s\n", lesson.CreatedAt.Format(time.RFC3339))
			_, _ = fmt.Fprintf(w, "summary:          %s\n", lesson.Summary)
			if lesson.SourceRunID != "" {
				_, _ = fmt.Fprintf(w, "source_run_id:    %s\n", lesson.SourceRunID)
			}
			if len(lesson.SourceTicketIDs) > 0 {
				_, _ = fmt.Fprintf(w, "source_tickets:   %s\n", strings.Join(lesson.SourceTicketIDs, ", "))
			}
			if len(lesson.MissedBy) > 0 {
				_, _ = fmt.Fprintf(w, "missed_by:        %s\n", strings.Join(lesson.MissedBy, ", "))
			}
			if lesson.RecommendedRule != "" {
				_, _ = fmt.Fprintf(w, "recommended_rule: %s\n", lesson.RecommendedRule)
			}
			if len(lesson.CandidateQualityCodes) > 0 {
				_, _ = fmt.Fprintf(w, "quality_codes:    %s\n", strings.Join(lesson.CandidateQualityCodes, ", "))
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON object")
	return cmd
}

func newLearnPromoteCmd() *cobra.Command {
	var (
		target string
		ruleID string
	)

	cmd := &cobra.Command{
		Use:          "promote <lesson-id>",
		Short:        "Promote a lesson to a rule (not implemented)",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmdError(cmd, fmt.Errorf("not implemented yet"), 1)
		},
	}

	cmd.Flags().StringVar(&target, "target", "", "Target file for the rule")
	cmd.Flags().StringVar(&ruleID, "rule-id", "", "Rule ID to assign")

	return cmd
}

func memoryDir(repoRoot string) string {
	return repoRoot + "/.verk/memory"
}

func splitComma(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
