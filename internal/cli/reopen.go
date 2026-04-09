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
	Use:     "reopen <run-id> <ticket-id>",
	Short:   "Reopen a blocked or closed ticket",
	GroupID: groupExecution,
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if reopenToPhase == "" {
			return withExitCode(fmt.Errorf("--to flag is required"), 2)
		}
		if err := engine.ReopenTicket(context.Background(), engine.ReopenRequest{
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
