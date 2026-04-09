package cli

import (
	"fmt"

	"verk/internal/engine"

	"github.com/spf13/cobra"
)

var statusJSONFlag bool

var statusCmd = &cobra.Command{
	Use:     "status <run-id>",
	Short:   "Show run status",
	GroupID: groupObserve,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		report, err := engine.DeriveStatus(engine.StatusRequest{RunID: args[0]})
		if err != nil {
			return withExitCode(err, 1)
		}
		if statusJSONFlag {
			return printJSON(cmd.OutOrStdout(), report)
		}

		w := cmd.OutOrStdout()
		fmt.Fprintf(w, "run %s: %s (%s)\n", report.RunID, report.RunStatus, report.CurrentPhase)
		if report.CurrentWave != "" {
			fmt.Fprintf(w, "current wave: %s\n", report.CurrentWave)
		}
		if report.LastFailedGate != "" {
			fmt.Fprintf(w, "last failed gate: %s\n", report.LastFailedGate)
		}
		for _, ticket := range report.Tickets {
			fmt.Fprintf(w, "- %s: %s", ticket.TicketID, ticket.Phase)
			if ticket.BlockReason != "" {
				fmt.Fprintf(w, " (%s)", ticket.BlockReason)
			}
			if ticket.ClaimState != "" {
				fmt.Fprintf(w, " [claim=%s", ticket.ClaimState)
				if ticket.LeaseID != "" {
					fmt.Fprintf(w, " lease=%s", ticket.LeaseID)
				}
				fmt.Fprint(w, "]")
			}
			fmt.Fprintln(w)
		}
		return nil
	},
}

func initStatusCmd() {
	statusCmd.Flags().BoolVar(&statusJSONFlag, "json", false, "Output as JSON")
	rootCmd.AddCommand(statusCmd)
}
