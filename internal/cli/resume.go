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
	Use:     "resume [run-id]",
	Short:   "Resume an interrupted run",
	GroupID:      groupExecution,
	SilenceUsage: true,
	Args:         cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		repoRoot, cfg, _, err := loadExecutionContext()
		if err != nil {
			return withExitCode(err, 1)
		}

		var runID string
		if len(args) > 0 {
			runID = args[0]
		} else {
			runID, err = readCurrentRunID(repoRoot)
			if err != nil {
				return withExitCode(fmt.Errorf("could not read current run: %w", err), 1)
			}
			if runID == "" {
				return withExitCode(fmt.Errorf("no current run — start one with: verk run ticket <id>"), 1)
			}
		}

		if wErr := writeCurrentRunID(repoRoot, runID); wErr != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not write current run: %v\n", wErr)
		}

		report, err := engine.ResumeRun(context.Background(), engine.ResumeRequest{
			RepoRoot: repoRoot,
			RunID:    runID,
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
