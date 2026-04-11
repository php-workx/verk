package cli

import (
	"context"
	"fmt"

	"verk/internal/engine"
	"verk/internal/state"

	"github.com/spf13/cobra"
)

var reopenToPhase string

var reopenCmd = &cobra.Command{
	Use:   "reopen <run-id> <ticket-id>",
	Short: "Reopen a blocked or closed ticket",
	Long: `Reopen a blocked or closed ticket to a specific phase.

Examples:
  verk reopen run-ver-abc-123 ver-abc --to implement
  verk reopen run-ver-abc-123 ver-abc --to repair`,
	GroupID:      groupExecution,
	SilenceUsage: true,
	Args: func(cmd *cobra.Command, args []string) error {
		if len(args) != 2 {
			return fmt.Errorf("requires <run-id> and <ticket-id>\n\nUsage:\n  verk reopen <run-id> <ticket-id> --to <phase>")
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		if reopenToPhase == "" {
			return withExitCode(fmt.Errorf("--to flag is required (implement or repair)"), 2)
		}
		repoRoot, _ := resolveRepoRoot()
		if err := engine.ReopenTicket(context.Background(), engine.ReopenRequest{
			RepoRoot: repoRoot,
			RunID:    args[0],
			TicketID: args[1],
			ToPhase:  state.TicketPhase(reopenToPhase),
		}); err != nil {
			return withExitCode(err, 1)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "reopened %s in %s to %s\n", args[1], args[0], reopenToPhase)
		return nil
	},
}

func initReopenCmd() {
	reopenCmd.Flags().StringVar(&reopenToPhase, "to", "", "Target phase (implement, repair)")
	rootCmd.AddCommand(reopenCmd)
}
