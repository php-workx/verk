package cli

import (
	"context"
	"fmt"
	"strings"

	"verk/internal/adapters/runtime"
	"verk/internal/engine"

	"github.com/spf13/cobra"
)

var resumeJSONFlag bool

var resumeCmd = &cobra.Command{
	Use:     "resume <run-id>",
	Short:   "Resume an interrupted run",
	GroupID: groupExecution,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		repoRoot, cfg, _, err := loadExecutionContext()
		if err != nil {
			return withExitCode(err, 1)
		}

		report, err := engine.ResumeRun(context.Background(), engine.ResumeRequest{
			RepoRoot: repoRoot,
			RunID:    args[0],
			AdapterFactory: func(ticketPreference string) (runtime.Adapter, error) {
				return runtimeAdapterFor(ticketPreference, cfg.Runtime.DefaultRuntime)
			},
			Config: cfg,
		})
		if err != nil {
			return withExitCode(err, 1)
		}
		w := cmd.OutOrStdout()
		if resumeJSONFlag {
			return printJSON(w, report)
		}
		fmt.Fprintf(w, "run %s: %s\n", report.Run.RunID, report.Run.Status)
		if len(report.RecoveredTickets) > 0 {
			fmt.Fprintf(w, "recovered: %s\n", strings.Join(report.RecoveredTickets, ", "))
		}
		if len(report.ResumedTickets) > 0 {
			fmt.Fprintf(w, "resumed: %s\n", strings.Join(report.ResumedTickets, ", "))
		}
		return nil
	},
}

func initResumeCmd() {
	resumeCmd.Flags().BoolVar(&resumeJSONFlag, "json", false, "Output as JSON")
	rootCmd.AddCommand(resumeCmd)
}
