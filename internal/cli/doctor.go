package cli

import (
	"fmt"
	"strings"
	"verk/internal/engine"

	"github.com/spf13/cobra"
)

func initDoctorCmd(root *cobra.Command) {
	var doctorJSONFlag bool

	doctorCmd := &cobra.Command{
		Use:     "doctor",
		Short:   "Check environment health",
		GroupID: groupObserve,
		RunE: func(cmd *cobra.Command, args []string) error {
			repoRoot, err := resolveRepoRoot()
			if err != nil {
				repoRoot = "."
			}
			report, code, err := engine.RunDoctor(repoRoot)
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

			color := shouldColorizeFunc()
			r := doctorRenderer{color: color}

			_, _ = fmt.Fprintln(w, r.bold("verk Doctor"))
			_, _ = fmt.Fprintln(w, r.dim(strings.Repeat("─", 40)))
			_, _ = fmt.Fprintln(w)

			warnings := 0
			failures := 0

			for _, check := range report.Checks {
				switch check.Status {
				case "passed":
					_, _ = fmt.Fprintf(w, "  %s %s\n", r.ok("[OK]"), r.bold(humanizeName(check.Name)))
				case "warning":
					_, _ = fmt.Fprintf(w, "  %s %s\n", r.warn("[WARN]"), r.bold(humanizeName(check.Name)))
					warnings++
				default:
					_, _ = fmt.Fprintf(w, "  %s %s\n", r.fail("[FAIL]"), r.bold(humanizeName(check.Name)))
					failures++
				}
				if check.Details != "" {
					_, _ = fmt.Fprintf(w, "       %s\n", r.dim(check.Details))
				}
			}

			for _, rt := range report.Runtimes {
				name := "Runtime " + rt.Runtime
				if rt.Available {
					_, _ = fmt.Fprintf(w, "  %s %s\n", r.ok("[OK]"), r.bold(name))
				} else {
					_, _ = fmt.Fprintf(w, "  %s %s\n", r.fail("[FAIL]"), r.bold(name))
					failures++
				}
				if rt.Details != "" {
					_, _ = fmt.Fprintf(w, "       %s\n", r.dim(rt.Details))
				}
			}

			_, _ = fmt.Fprintln(w)
			if failures == 0 && warnings == 0 {
				_, _ = fmt.Fprintln(w, r.ok("All checks passed!"))
			} else {
				parts := make([]string, 0, 2)
				if warnings > 0 {
					parts = append(parts, fmt.Sprintf("%d warning(s)", warnings))
				}
				if failures > 0 {
					parts = append(parts, fmt.Sprintf("%d failure(s)", failures))
				}
				_, _ = fmt.Fprintln(w, r.warn(strings.Join(parts, ", ")))
			}

			if code != 0 {
				return withExitCode(fmt.Errorf("doctor found issues"), code)
			}
			return nil
		},
	}

	doctorCmd.Flags().BoolVar(&doctorJSONFlag, "json", false, "Output as JSON")
	root.AddCommand(doctorCmd)
}

type doctorRenderer struct{ color bool }

func (r doctorRenderer) bold(s string) string {
	if !r.color {
		return s
	}
	return styleBold.Render(s)
}

func (r doctorRenderer) dim(s string) string {
	if !r.color {
		return s
	}
	return styleDim.Render(s)
}

func (r doctorRenderer) ok(s string) string {
	if !r.color {
		return s
	}
	return styleOK.Render(s)
}

func (r doctorRenderer) warn(s string) string {
	if !r.color {
		return s
	}
	return styleWarn.Render(s)
}

func (r doctorRenderer) fail(s string) string {
	if !r.color {
		return s
	}
	return styleFail.Render(s)
}

func humanizeName(name string) string {
	name = strings.ReplaceAll(name, "_", " ")
	if len(name) > 0 {
		return strings.ToUpper(name[:1]) + name[1:]
	}
	return name
}
