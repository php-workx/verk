package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"verk/internal/bench"
	"verk/internal/bench/providers/verknative"

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
	var allowDirty, includeHoldout bool
	runCmd := &cobra.Command{
		Use:          "run <suite>",
		Short:        "Run a benchmark suite (stub)",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBenchRun(cmd, args[0], matrixPath, outDir, allowDirty, includeHoldout)
		},
	}
	runCmd.Flags().StringVar(&matrixPath, "matrix", "", "Path to benchmark matrix YAML/JSON")
	runCmd.Flags().StringVar(&outDir, "out", "", "Output directory (default .verk/bench/runs/<run-id>)")
	runCmd.Flags().BoolVar(&allowDirty, "allow-dirty", false, "Allow running with a dirty worktree (results marked non-comparable)")
	runCmd.Flags().BoolVar(&includeHoldout, "include-holdout", false, "Allow running holdout suites (required when suite sampling_mode is holdout)")

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
	reportCmd.Flags().StringVar(&reportFormat, "format", "markdown", "Output format: markdown|json|csv")

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
	_ = r.Register(verknative.New())
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

func runBenchRun(cmd *cobra.Command, suite, matrixPath, outDir string, allowDirty, includeHoldout bool) error {
	repoRoot, err := resolveRepoRoot()
	if err != nil {
		return cmdError(cmd, fmt.Errorf("resolve repo root: %w", err), 1)
	}
	r := benchRegistryFactory()
	providerName, suiteName, err := parseProviderSuite(suite)
	if err != nil {
		return cmdError(cmd, err, 1)
	}
	provider, ok := r.Get(providerName)
	if !ok {
		return cmdError(cmd, fmt.Errorf("unknown provider %q", providerName), 1)
	}

	// Holdout guard: refuse to run a holdout suite unless --include-holdout is set.
	for _, sm := range provider.Suites() {
		if sm.Name == suiteName && sm.SamplingMode == bench.SamplingModeHoldout && !includeHoldout {
			return cmdError(cmd, fmt.Errorf("refusing to run holdout suite without --include-holdout"), 2)
		}
	}

	matrix := defaultMatrixFor(provider)
	if matrixPath != "" {
		data, err := os.ReadFile(matrixPath)
		if err != nil {
			return cmdError(cmd, err, 1)
		}
		matrix, err = bench.ParseMatrix(data)
		if err != nil {
			return cmdError(cmd, err, 1)
		}
	}
	result, err := bench.Run(cmd.Context(), bench.RunOptions{
		RepoRoot:   repoRoot,
		OutDir:     outDir,
		SuiteName:  suiteName,
		Provider:   provider,
		Matrix:     matrix,
		AllowDirty: allowDirty,
	})
	if err != nil {
		return cmdError(cmd, err, 1)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "bench run complete: %s (results=%d)\n", result.RunID, len(result.Results))
	return nil
}

// parseProviderSuite handles "provider/suite" or just "suite" (default provider verk-native).
func parseProviderSuite(s string) (provider, suite string, err error) {
	if s == "" {
		return "", "", fmt.Errorf("suite name must not be empty")
	}
	if provider, suite, ok := strings.Cut(s, "/"); ok {
		if provider == "" {
			return "", "", fmt.Errorf("provider name must not be empty in %q", s)
		}
		if suite == "" {
			return "", "", fmt.Errorf("suite name must not be empty in %q", s)
		}
		return provider, suite, nil
	}
	return "verk-native", s, nil
}

// defaultMatrixFor builds a minimal full-verk matrix for the given provider.
// When the provider does not support full-verk, it falls back to worker-only.
func defaultMatrixFor(provider bench.Provider) bench.Matrix {
	mode := bench.ModeFullVerk
	caps := provider.Capabilities()
	supportsFullVerk := false
	for _, m := range caps.SupportedModes {
		if m == bench.ModeFullVerk {
			supportsFullVerk = true
			break
		}
	}
	if !supportsFullVerk {
		mode = bench.ModeWorkerOnly
	}

	profile := bench.MatrixProfile{
		ID:     "default",
		Worker: bench.ModelRef{Runtime: "claude", Model: "claude-sonnet-4-5"},
	}
	if mode == bench.ModeFullVerk {
		profile.Reviewer = bench.ModelRef{Runtime: "claude", Model: "claude-sonnet-4-5"}
	}

	design := "fixed-reviewer"
	if mode == bench.ModeWorkerOnly {
		design = "fixed-reviewer"
	}

	return bench.Matrix{
		Mode:             mode,
		ComparisonDesign: design,
		Profiles:         []bench.MatrixProfile{profile},
	}
}

func runBenchReport(cmd *cobra.Command, runID, format string) error {
	repoRoot, _ := resolveRepoRoot()
	outDir := filepath.Join(repoRoot, ".verk", "bench", "runs", runID)
	rr, err := bench.LoadRunResult(outDir)
	if err != nil {
		return cmdError(cmd, err, 1)
	}
	r := bench.BuildReport(rr)
	switch format {
	case "json":
		return bench.RenderJSON(cmd.OutOrStdout(), r)
	case "csv":
		return bench.RenderCSV(cmd.OutOrStdout(), r)
	default:
		return bench.RenderMarkdown(cmd.OutOrStdout(), r)
	}
}

func runBenchCompare(cmd *cobra.Command, baseline, candidate, format string) error {
	repoRoot, _ := resolveRepoRoot()
	base, err := bench.LoadRunResult(filepath.Join(repoRoot, ".verk", "bench", "runs", baseline))
	if err != nil {
		return cmdError(cmd, err, 1)
	}
	cand, err := bench.LoadRunResult(filepath.Join(repoRoot, ".verk", "bench", "runs", candidate))
	if err != nil {
		return cmdError(cmd, err, 1)
	}
	cmp := bench.Compare(base, cand)
	if cmp.Refusal != "" {
		return cmdError(cmd, fmt.Errorf("non-comparable: %s", cmp.Refusal), 2)
	}
	switch format {
	case "json":
		return bench.RenderComparisonJSON(cmd.OutOrStdout(), cmp)
	default:
		return bench.RenderComparisonMarkdown(cmd.OutOrStdout(), cmp)
	}
}
