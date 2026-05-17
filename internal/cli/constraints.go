package cli

import (
	"fmt"
	"strings"
	"time"
	"verk/internal/engine/constraints"

	"github.com/spf13/cobra"
)

func initConstraintsCmd(root *cobra.Command) {
	constraintsCmd := &cobra.Command{
		Use:          "constraints",
		Short:        "Manage compiled constraint index",
		GroupID:      groupObserve,
		SilenceUsage: true,
	}

	constraintsCmd.AddCommand(
		newConstraintsListCmd(),
		newConstraintsShowCmd(),
		newConstraintsDisableCmd(),
		newConstraintsHistoryCmd(),
	)

	root.AddCommand(constraintsCmd)
}

func newConstraintsListCmd() *cobra.Command {
	var flagActive, flagCandidate, flagDisabled bool

	cmd := &cobra.Command{
		Use:          "list",
		Short:        "List constraints or candidates",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			repoRoot, err := resolveRepoRoot()
			if err != nil {
				return cmdError(cmd, fmt.Errorf("resolve repo root: %w", err), 1)
			}
			store := constraints.NewStore(repoRoot)

			if flagCandidate {
				return runConstraintListCandidates(cmd, store)
			}

			idx, err := store.Load()
			if err != nil {
				return cmdError(cmd, fmt.Errorf("load constraints: %w", err), 1)
			}

			w := cmd.OutOrStdout()
			if len(idx.Constraints) == 0 {
				_, _ = fmt.Fprintln(w, "No constraints found.")
				return nil
			}

			for _, c := range idx.Constraints {
				if flagActive && !c.Active {
					continue
				}
				if flagDisabled && c.DisabledAt == "" {
					continue
				}
				status := constraintStatus(c)
				_, _ = fmt.Fprintf(w, "  [%s] %s  type=%s  promoted_from=%d\n",
					status, c.ID, c.Check.Type, c.PromotedFromCount)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&flagActive, "active", false, "Show only active constraints")
	cmd.Flags().BoolVar(&flagCandidate, "candidate", false, "Show candidates (not yet promoted)")
	cmd.Flags().BoolVar(&flagDisabled, "disabled", false, "Show only disabled constraints")
	return cmd
}

func runConstraintListCandidates(cmd *cobra.Command, store *constraints.Store) error {
	candidates, err := constraints.ListCandidates(store)
	if err != nil {
		return cmdError(cmd, fmt.Errorf("list candidates: %w", err), 1)
	}
	w := cmd.OutOrStdout()
	if len(candidates) == 0 {
		_, _ = fmt.Fprintln(w, "No candidates found.")
		return nil
	}
	for _, c := range candidates {
		_, _ = fmt.Fprintf(w, "  %s  distinct_tickets=%d\n", c.Signature, c.DistinctTickets)
	}
	return nil
}

func newConstraintsShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "show <id>",
		Short:        "Show constraint details",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			repoRoot, err := resolveRepoRoot()
			if err != nil {
				return cmdError(cmd, fmt.Errorf("resolve repo root: %w", err), 1)
			}
			store := constraints.NewStore(repoRoot)
			idx, err := store.Load()
			if err != nil {
				return cmdError(cmd, fmt.Errorf("load constraints: %w", err), 1)
			}
			for _, c := range idx.Constraints {
				if c.ID == id {
					return printJSON(cmd.OutOrStdout(), c)
				}
			}
			return cmdError(cmd, fmt.Errorf("constraint %q not found", id), 1)
		},
	}
}

func newConstraintsDisableCmd() *cobra.Command {
	var flagReason string

	cmd := &cobra.Command{
		Use:          "disable <id>",
		Short:        "Disable a constraint",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			if strings.TrimSpace(flagReason) == "" {
				return cmdError(cmd, fmt.Errorf("--reason is required"), 1)
			}
			repoRoot, err := resolveRepoRoot()
			if err != nil {
				return cmdError(cmd, fmt.Errorf("resolve repo root: %w", err), 1)
			}
			store := constraints.NewStore(repoRoot)
			idx, err := store.Load()
			if err != nil {
				return cmdError(cmd, fmt.Errorf("load constraints: %w", err), 1)
			}
			found := false
			for i := range idx.Constraints {
				if idx.Constraints[i].ID != id {
					continue
				}
				idx.Constraints[i].Active = false
				idx.Constraints[i].DisabledAt = time.Now().UTC().Format(time.RFC3339)
				idx.Constraints[i].DisabledReason = strings.TrimSpace(flagReason)
				found = true
				break
			}
			if !found {
				return cmdError(cmd, fmt.Errorf("constraint %q not found", id), 1)
			}
			if err := store.Save(idx); err != nil {
				return cmdError(cmd, fmt.Errorf("save constraints: %w", err), 1)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Disabled constraint %s\n", id)
			return nil
		},
	}

	cmd.Flags().StringVar(&flagReason, "reason", "", "Reason for disabling (required)")
	return cmd
}

func newConstraintsHistoryCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "history <id>",
		Short:        "Show tickets that triggered a constraint",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			repoRoot, err := resolveRepoRoot()
			if err != nil {
				return cmdError(cmd, fmt.Errorf("resolve repo root: %w", err), 1)
			}
			store := constraints.NewStore(repoRoot)
			idx, err := store.Load()
			if err != nil {
				return cmdError(cmd, fmt.Errorf("load constraints: %w", err), 1)
			}
			var found *constraints.Constraint
			for i := range idx.Constraints {
				if idx.Constraints[i].ID == id {
					found = &idx.Constraints[i]
					break
				}
			}
			if found == nil {
				return cmdError(cmd, fmt.Errorf("constraint %q not found", id), 1)
			}
			w := cmd.OutOrStdout()
			_, _ = fmt.Fprintf(w, "Constraint: %s\n", found.ID)
			_, _ = fmt.Fprintf(w, "Promoted from %d ticket(s):\n", len(found.PromotedFrom))
			for _, pf := range found.PromotedFrom {
				_, _ = fmt.Fprintf(w, "  ticket=%s  run=%s  finding=%s\n", pf.TicketID, pf.RunID, pf.FindingID)
			}
			if found.LastTriggeredAt != "" {
				_, _ = fmt.Fprintf(w, "Last triggered: %s\n", found.LastTriggeredAt)
			}
			return nil
		},
	}
}

func constraintStatus(c constraints.Constraint) string {
	if c.DisabledAt != "" {
		return "disabled"
	}
	if c.Active {
		return "active"
	}
	return "inactive"
}
