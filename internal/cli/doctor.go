package cli

import (
	"fmt"

	"verk/internal/engine"

	"github.com/spf13/cobra"
)

var doctorJSONFlag bool

var doctorCmd = &cobra.Command{
	Use:     "doctor",
	Short:   "Check environment health",
	GroupID: groupObserve,
	RunE: func(cmd *cobra.Command, args []string) error {
		report, code, err := engine.RunDoctor(".")
		if err != nil {
			return withExitCode(err, 2)
		}
		w := cmd.OutOrStdout()
		if doctorJSONFlag {
			if err := printJSON(w, report); err != nil {
				return withExitCode(err, 2)
			}
			if code != 0 {
				return withExitCode(fmt.Errorf("doctor found issues"), code)
			}
			return nil
		}
		fmt.Fprintf(w, "repo root: %s\n", report.RepoRoot)
		for _, check := range report.Checks {
			fmt.Fprintf(w, "- %s: %s", check.Name, check.Status)
			if check.Details != "" {
				fmt.Fprintf(w, " (%s)", check.Details)
			}
			fmt.Fprintln(w)
		}
		for _, rt := range report.Runtimes {
			status := "unavailable"
			if rt.Available {
				status = "available"
			}
			fmt.Fprintf(w, "- runtime %s: %s", rt.Runtime, status)
			if rt.Details != "" {
				fmt.Fprintf(w, " (%s)", rt.Details)
			}
			fmt.Fprintln(w)
		}
		if code != 0 {
			return withExitCode(fmt.Errorf("doctor found issues"), code)
		}
		return nil
	},
}

func initDoctorCmd() {
	doctorCmd.Flags().BoolVar(&doctorJSONFlag, "json", false, "Output as JSON")
	rootCmd.AddCommand(doctorCmd)
}
