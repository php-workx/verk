package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"verk/internal/policy"
	"verk/internal/state"

	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:     "init",
	Short:   "Initialize or update verk configuration",
	Long:    "Initialize or update .verk/config.yaml. Safe to run multiple times — existing values are used as defaults.",
	GroupID: groupExecution,
	RunE: func(cmd *cobra.Command, args []string) error {
		repoRoot, err := resolveRepoRoot()
		if err != nil {
			// Fall back to cwd if not inside a git repo.
			repoRoot, err = os.Getwd()
			if err != nil {
				return cmdError(cmd, fmt.Errorf("cannot determine working directory: %w", err), 1)
			}
		}

		// Load existing config for re-run defaults; fall back to defaults on first run.
		cfg, loadErr := policy.LoadConfig(repoRoot)
		if loadErr != nil {
			cfg = policy.DefaultConfig()
		}

		w := cmd.OutOrStdout()
		scanner := bufio.NewScanner(cmd.InOrStdin())
		color := shouldColorizeFunc()
		r := doctorRenderer{color: color}

		_, _ = fmt.Fprintln(w, r.bold("verk init"))
		_, _ = fmt.Fprintln(w, r.dim(strings.Repeat("─", 40)))
		_, _ = fmt.Fprintln(w)

		// --- Quality commands ---
		toolings := policy.DetectProjectTooling(repoRoot)
		var qualityCommands []policy.QualityCommand

		if len(toolings) > 0 {
			_, _ = fmt.Fprintln(w, r.bold("Detected project tooling:"))
			for _, t := range toolings {
				_, _ = fmt.Fprintf(w, "  %s %s → %s\n",
					r.ok("✓"),
					r.bold(t.File),
					strings.Join(t.SuggestedCommands, ", "))
			}
			_, _ = fmt.Fprintln(w)

			suggested := toolings[0].SuggestedCommands
			_, _ = fmt.Fprintf(w, "Use suggested quality commands [%s]? (Y/n or enter custom): ",
				strings.Join(suggested, ", "))

			if scanner.Scan() {
				input := strings.TrimSpace(scanner.Text())
				switch {
				case input == "" || strings.EqualFold(input, "y") || strings.EqualFold(input, "yes"):
					qualityCommands = append(qualityCommands, policy.QualityCommand{Run: suggested})
				case strings.EqualFold(input, "n") || strings.EqualFold(input, "no") || strings.EqualFold(input, "skip"):
					// skip
				default:
					// Treat input as custom commands.
					if run := splitCommands(input); len(run) > 0 {
						qualityCommands = append(qualityCommands, policy.QualityCommand{Run: run})
					}
				}
			}
		} else {
			_, _ = fmt.Fprintln(w, r.dim("No recognized build tooling detected (Justfile, Makefile, package.json, go.mod, Cargo.toml, pyproject.toml)."))
			_, _ = fmt.Fprintln(w)
			_, _ = fmt.Fprint(w, "Enter quality commands (comma-separated, or empty to skip): ")
			if scanner.Scan() {
				if run := splitCommands(scanner.Text()); len(run) > 0 {
					qualityCommands = append(qualityCommands, policy.QualityCommand{Run: run})
				}
			}
		}

		// Optional monorepo subdirectory commands.
		for {
			_, _ = fmt.Fprint(w, "Add quality commands for a subdirectory? (path or empty to finish): ")
			if !scanner.Scan() {
				break
			}
			path := strings.TrimSpace(scanner.Text())
			if path == "" {
				break
			}
			_, _ = fmt.Fprintf(w, "Commands for %q (comma-separated): ", path)
			if !scanner.Scan() {
				break
			}
			if run := splitCommands(scanner.Text()); len(run) > 0 {
				qualityCommands = append(qualityCommands, policy.QualityCommand{Path: path, Run: run})
			}
		}

		if len(qualityCommands) > 0 {
			cfg.Verification.QualityCommands = qualityCommands
		}

		_, _ = fmt.Fprintln(w)

		// --- Policy settings ---
		_, _ = fmt.Fprintln(w, r.bold("Policy settings:"))
		cfg.Policy.ReviewThreshold = promptSeverity(w, scanner,
			"  Review threshold (P0–P4)", cfg.Policy.ReviewThreshold)
		cfg.Policy.MaxImplementationAttempts = promptInt(w, scanner,
			"  Max implementation attempts", cfg.Policy.MaxImplementationAttempts, 1, 20)
		cfg.Policy.MaxRepairCycles = promptInt(w, scanner,
			"  Max repair cycles", cfg.Policy.MaxRepairCycles, 0, 10)

		_, _ = fmt.Fprintln(w)

		// --- Runtime settings ---
		_, _ = fmt.Fprintln(w, r.bold("Runtime settings:"))
		cfg.Runtime.WorkerTimeoutMinutes = promptInt(w, scanner,
			"  Worker timeout (minutes)", cfg.Runtime.WorkerTimeoutMinutes, 1, 240)
		cfg.Runtime.ReviewerTimeoutMinutes = promptInt(w, scanner,
			"  Reviewer timeout (minutes)", cfg.Runtime.ReviewerTimeoutMinutes, 1, 120)

		_, _ = fmt.Fprintln(w)

		// Surface any scanner I/O error before persisting config so a partial
		// read cannot silently produce an incomplete configuration.
		if err := scanner.Err(); err != nil {
			return cmdError(cmd, fmt.Errorf("read input: %w", err), 1)
		}

		// Write config.
		if err := policy.WriteConfig(repoRoot, cfg); err != nil {
			return cmdError(cmd, err, 1)
		}

		_, _ = fmt.Fprintf(w, "%s Configuration written to .verk/config.yaml\n", r.ok("[OK]"))
		if loadErr == nil {
			_, _ = fmt.Fprintln(w, r.dim("    (existing config updated)"))
		}
		return nil
	},
}

func initInitCmd() {
	rootCmd.AddCommand(initCmd)
}

// splitCommands splits a comma-separated string of commands, trimming whitespace,
// and returns only non-empty entries.
func splitCommands(input string) []string {
	parts := strings.Split(input, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func promptSeverity(w io.Writer, scanner *bufio.Scanner, label string, defaultVal state.Severity) state.Severity {
	_, _ = fmt.Fprintf(w, "%s [%s]: ", label, defaultVal)
	if scanner.Scan() {
		input := strings.TrimSpace(strings.ToUpper(scanner.Text()))
		if input == "" {
			return defaultVal
		}
		switch state.Severity(input) {
		case state.SeverityP0, state.SeverityP1, state.SeverityP2, state.SeverityP3, state.SeverityP4:
			return state.Severity(input)
		}
	}
	return defaultVal
}

func promptInt(w io.Writer, scanner *bufio.Scanner, label string, defaultVal, min, max int) int {
	_, _ = fmt.Fprintf(w, "%s [%d]: ", label, defaultVal)
	if scanner.Scan() {
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			return defaultVal
		}
		if n, err := strconv.Atoi(input); err == nil && n >= min && n <= max {
			return n
		}
	}
	return defaultVal
}
