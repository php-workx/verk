package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"verk/internal/adapters/ticketstore/epos"
	"verk/internal/engine"
	"verk/internal/policy"
	"verk/internal/state"

	"github.com/spf13/cobra"
)

func initInspectCmd(root *cobra.Command) {
	inspectCmd := &cobra.Command{
		Use:     "inspect",
		Short:   "Inspect ticket or epic quality",
		GroupID: groupObserve,
	}

	var ticketFix, ticketPlannerReview bool
	ticketSub := &cobra.Command{
		Use:          "ticket <ticket-id>",
		Short:        "Lint a single ticket and optionally repair it",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInspect(cmd, inspectArgs{
				ticketID:      args[0],
				epic:          false,
				fix:           ticketFix,
				plannerReview: ticketPlannerReview,
			})
		},
	}
	ticketSub.Flags().BoolVar(&ticketFix, "fix", false, "Apply safe ticket quality repairs in place")
	ticketSub.Flags().BoolVar(&ticketPlannerReview, "planner-review", false, "Reserved for planner-role review (no-op until adapter wired)")

	var epicFix, epicPlannerReview bool
	epicSub := &cobra.Command{
		Use:          "epic <ticket-id>",
		Short:        "Lint an epic and its children, optionally repair",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInspect(cmd, inspectArgs{
				ticketID:      args[0],
				epic:          true,
				fix:           epicFix,
				plannerReview: epicPlannerReview,
			})
		},
	}
	epicSub.Flags().BoolVar(&epicFix, "fix", false, "Apply safe ticket quality repairs in place")
	epicSub.Flags().BoolVar(&epicPlannerReview, "planner-review", false, "Reserved for planner-role review (no-op until adapter wired)")

	inspectCmd.AddCommand(ticketSub, epicSub)
	root.AddCommand(inspectCmd)
}

type inspectArgs struct {
	ticketID      string
	epic          bool
	fix           bool
	plannerReview bool
}

func runInspect(cmd *cobra.Command, args inspectArgs) error {
	repoRoot, err := resolveRepoRoot()
	if err != nil {
		return cmdError(cmd, fmt.Errorf("resolve repo root: %w", err), 1)
	}
	cfg, err := policy.LoadConfig(repoRoot)
	if err != nil {
		return cmdError(cmd, fmt.Errorf("load config: %w", err), 1)
	}

	ticketsDir := filepath.Join(repoRoot, ".tickets")
	rootPath := filepath.Join(ticketsDir, args.ticketID+".md")
	root, err := epos.LoadTicket(rootPath)
	if err != nil {
		return cmdError(cmd, fmt.Errorf("load %s: %w", args.ticketID, err), 1)
	}

	tickets := []epos.Ticket{root}
	if args.epic {
		children, err := epos.ListAllChildren(repoRoot, args.ticketID)
		if err != nil {
			return cmdError(cmd, fmt.Errorf("list children: %w", err), 1)
		}
		tickets = append(tickets, children...)
	}

	input := engine.TicketQualityInput{
		RootTicket: root,
		Tickets:    tickets,
		Config:     cfg,
	}
	artifact := engine.EvaluateTicketQuality(input)

	if args.fix {
		plan := engine.BuildTicketQualityRepairPlan(input, artifact)
		applied := make([]state.TicketQualityRepair, 0, len(plan.Repairs))
		for id, repaired := range plan.Tickets {
			path := filepath.Join(ticketsDir, id+".md")
			if err := epos.SaveTicket(path, repaired); err != nil {
				return cmdError(cmd, fmt.Errorf("save repaired ticket %s: %w", id, err), 1)
			}
		}
		applied = append(applied, plan.Repairs...)
		artifact.Repairs = applied
		// Re-evaluate to reflect post-repair state
		freshTickets := []epos.Ticket{}
		for _, t := range tickets {
			if repaired, ok := plan.Tickets[t.ID]; ok {
				freshTickets = append(freshTickets, repaired)
			} else {
				freshTickets = append(freshTickets, t)
			}
		}
		input.Tickets = freshTickets
		if repaired, ok := plan.Tickets[root.ID]; ok {
			input.RootTicket = repaired
		}
		artifact = engine.EvaluateTicketQuality(input)
		artifact.Repairs = applied
	}

	printInspectResult(cmd.OutOrStdout(), artifact, args.epic)

	if artifact.Blocked {
		return cmdError(cmd, fmt.Errorf("ticket quality: blocked"), 2)
	}
	return nil
}

func printInspectResult(w io.Writer, artifact state.TicketQualityArtifact, epicMode bool) {
	scope := "ticket"
	if epicMode {
		scope = "epic"
	}
	_, _ = fmt.Fprintf(w, "ticket quality (%s): %s\n", scope, artifact.Status)

	if len(artifact.Repairs) > 0 {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "Repairs applied:")
		for _, r := range artifact.Repairs {
			_, _ = fmt.Fprintf(w, "  %s [%s] %s\n", r.TicketID, r.Kind, r.Summary)
		}
	}

	if len(artifact.Findings) == 0 {
		return
	}
	findings := append([]state.TicketQualityFinding(nil), artifact.Findings...)
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].TicketID != findings[j].TicketID {
			return findings[i].TicketID < findings[j].TicketID
		}
		return findings[i].Code < findings[j].Code
	})
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "Findings:")
	for _, f := range findings {
		_, _ = fmt.Fprintf(w, "  %s %s %s\n    %s\n", f.TicketID, f.Severity, f.Code, f.Title)
	}
}
