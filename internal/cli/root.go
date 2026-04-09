package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	Version   = "dev"
	GitCommit = "unknown"
	BuildDate = "unknown"
)

const (
	groupExecution = "execution"
	groupObserve   = "observe"
)

var rootCmd = &cobra.Command{
	Use:   "verk",
	Short: "Deterministic engineering execution engine",
	Long:  "verk executes engineering tickets and epics through a deterministic state machine with fresh-context workers, wave scheduling, and independent review.",
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("verk %s (%s, %s)\n", Version, GitCommit, BuildDate)
	},
}

func init() {
	rootCmd.SilenceErrors = true
	rootCmd.AddGroup(
		&cobra.Group{ID: groupExecution, Title: "Execution"},
		&cobra.Group{ID: groupObserve, Title: "Observe"},
	)

	rootCmd.AddCommand(versionCmd)
	initHelp()
	initRunCmd()
	initResumeCmd()
	initReopenCmd()
	initStatusCmd()
	initDoctorCmd()
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		var exitErr interface{ ExitCode() int }
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.ExitCode())
		}
		os.Exit(1)
	}
}

// ExecuteArgs runs the CLI with the given args, writing to the provided writers.
// Used by tests to exercise the CLI without os.Exit.
func ExecuteArgs(args []string, stdout, stderr *os.File) int {
	rootCmd.SetOut(stdout)
	rootCmd.SetErr(stderr)
	rootCmd.SetArgs(args)
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(stderr, err)
		var exitErr interface{ ExitCode() int }
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		return 1
	}
	return 0
}

type cliExitError struct {
	err      error
	exitCode int
}

func (e *cliExitError) Error() string   { return e.err.Error() }
func (e *cliExitError) Unwrap() error   { return e.err }
func (e *cliExitError) ExitCode() int   { return e.exitCode }

func withExitCode(err error, exitCode int) error {
	if err == nil {
		return nil
	}
	return &cliExitError{err: err, exitCode: exitCode}
}
