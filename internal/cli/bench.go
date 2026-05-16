package cli

import (
	"fmt"
	"strings"

	"verk/internal/bench"
	// TODO(task-4): import verk/internal/bench/providers/verknative once Task 4 lands.

	"github.com/spf13/cobra"
)

func initBenchCmd(root *cobra.Command) {
	benchCmd := &cobra.Command{
		Use:     "bench",
		Short:   "Run and compare verk benchmarks",
		GroupID: groupObserve,
	}

	listCmd := &cobra.Command{
		Use:          "list",
		Short:        "List registered benchmark providers and suites",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBenchList(cmd)
		},
	}

	var matrixPath, outDir string
	var allowDirty bool
	runCmd := &cobra.Command{
		Use:          "run <suite>",
		Short:        "Run a benchmark suite (stub)",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBenchRun(cmd, args[0], matrixPath, outDir, allowDirty)
		},
	}
	runCmd.Flags().StringVar(&matrixPath, "matrix", "", "Path to benchmark matrix YAML/JSON")
	runCmd.Flags().StringVar(&outDir, "out", "", "Output directory (default .verk/bench/runs/<run-id>)")
	runCmd.Flags().BoolVar(&allowDirty, "allow-dirty", false, "Allow running with a dirty worktree (results marked non-comparable)")

	var reportFormat string
	reportCmd := &cobra.Command{
		Use:          "report <run-id>",
		Short:        "Render a benchmark run report",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBenchReport(cmd, args[0], reportFormat)
		},
	}
	reportCmd.Flags().StringVar(&reportFormat, "format", "markdown", "Output format: markdown|json")

	var compareFormat string
	compareCmd := &cobra.Command{
		Use:          "compare <baseline> <candidate>",
		Short:        "Compare two benchmark runs",
		Args:         cobra.ExactArgs(2),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBenchCompare(cmd, args[0], args[1], compareFormat)
		},
	}
	compareCmd.Flags().StringVar(&compareFormat, "format", "markdown", "Output format: markdown|json")

	benchCmd.AddCommand(listCmd, runCmd, reportCmd, compareCmd)
	root.AddCommand(benchCmd)
}

// benchRegistryFactory builds the default provider registry used by the CLI.
// Tests may override this variable to inject a different registry.
var benchRegistryFactory = newDefaultBenchRegistry

func newDefaultBenchRegistry() *bench.Registry {
	r := bench.NewRegistry()
	// TODO(task-4): register verknative.New() once verk/internal/bench/providers/verknative lands.
	return r
}

func runBenchList(cmd *cobra.Command) error {
	r := benchRegistryFactory()
	w := cmd.OutOrStdout()
	names := r.List()
	for _, name := range names {
		p, _ := r.Get(name)
		fmt.Fprintf(w, "%s\n", p.Name())
		for _, s := range p.Suites() {
			fmt.Fprintf(w, "  - %s (tasks=%d, mode=%s)\n", s.Name, s.TaskCount, s.SamplingMode)
		}
	}
	return nil
}

func runBenchRun(cmd *cobra.Command, suite, matrixPath, outDir string, allowDirty bool) error {
	return cmdError(cmd, fmt.Errorf("bench run: orchestration not yet implemented (see plan task 6)"), 2)
}

func runBenchReport(cmd *cobra.Command, runID, format string) error {
	return cmdError(cmd, fmt.Errorf("bench report: not yet implemented (see plan task 7)"), 2)
}

func runBenchCompare(cmd *cobra.Command, baseline, candidate, format string) error {
	if !strings.HasPrefix(baseline, "run-") || !strings.HasPrefix(candidate, "run-") {
		return cmdError(cmd, fmt.Errorf("bench compare: expected run ids prefixed with 'run-'"), 2)
	}
	return cmdError(cmd, fmt.Errorf("bench compare: not yet implemented (see plan task 7)"), 2)
}
