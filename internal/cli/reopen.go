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
			return cmdError(cmd, fmt.Errorf("--to flag is required (implement or repair)"), 2)
		}
		if !isValidReopenPhase(reopenToPhase) {
			return cmdError(cmd, fmt.Errorf("--to must be one of: implement, repair (got %q)", reopenToPhase), 2)
		}
		repoRoot, err := resolveRepoRoot()
		if err != nil {
			return cmdError(cmd, fmt.Errorf("resolve repo root: %w", err), 1)
		}
		if err := engine.ReopenTicket(context.Background(), engine.ReopenRequest{
			RepoRoot: repoRoot,
			RunID:    args[0],
			TicketID: args[1],
			ToPhase:  state.TicketPhase(reopenToPhase),
		}); err != nil {
			return cmdError(cmd, err, 1)
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "reopened %s in %s to %s\n", args[1], args[0], reopenToPhase)
		return nil
	},
}

func initReopenCmd() {
	reopenCmd.Flags().StringVar(&reopenToPhase, "to", "", "Target phase (implement, repair)")
	rootCmd.AddCommand(reopenCmd)
}

// isValidReopenPhase checks whether the given phase string is a valid
// target for the reopen command.
func isValidReopenPhase(phase string) bool {
	switch state.TicketPhase(phase) {
	case state.TicketPhaseImplement, state.TicketPhaseRepair:
		return true
	default:
		return false
	}
}
