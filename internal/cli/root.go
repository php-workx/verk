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

// newRootCmd constructs a fresh *cobra.Command tree on every call. Each
// invocation gets independent flag state, so multiple calls to ExecuteArgs
// within the same process do not share any Cobra-internal state.
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "verk",
		Short: "Deterministic engineering execution engine",
		Long:  "verk executes engineering tickets and epics through a deterministic state machine with fresh-context workers, wave scheduling, and independent review.",
	}

	root.AddGroup(
		&cobra.Group{ID: groupExecution, Title: "Execution"},
		&cobra.Group{ID: groupObserve, Title: "Observe"},
	)

	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "verk %s (%s, %s)\n", Version, GitCommit, BuildDate)
		},
	}
	root.AddCommand(versionCmd)

	initHelp(root)
	initRunCmd(root)
	initReopenCmd(root)
	initStatusCmd(root)
	initDoctorCmd(root)
	initInitCmd(root)

	return root
}

func Execute() {
	root := newRootCmd()
	if err := root.Execute(); err != nil {
		var exitErr interface{ ExitCode() int }
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.ExitCode())
		}
		os.Exit(1)
	}
}

// ExecuteArgs runs the CLI with the given args, writing to the provided writers.
// Each call constructs a fresh command tree via newRootCmd so that flag state,
// parsed args, and Cobra-internal state do not leak between invocations.
// Used by tests to exercise the CLI without os.Exit.
func ExecuteArgs(args []string, stdout, stderr *os.File) int {
	root := newRootCmd()
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
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

func (e *cliExitError) Error() string { return e.err.Error() }
func (e *cliExitError) Unwrap() error { return e.err }
func (e *cliExitError) ExitCode() int { return e.exitCode }

func withExitCode(err error, exitCode int) error {
	if err == nil {
		return nil
	}
	return &cliExitError{err: err, exitCode: exitCode}
}
