package bench

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"text/tabwriter"
	"time"
)

// RunResult captures the full output of a benchmark run.
// It is the canonical on-disk and in-memory run record; runner.go writes it
// to <outDir>/run.json and LoadRunResult reads it back for reporting.
type RunResult struct {
	RunID       string                    `json:"run_id"`
	SuiteName   string                    `json:"suite_name"`
	Mode        BenchmarkMode             `json:"mode,omitempty"`
	Matrix      Matrix                    `json:"matrix,omitempty"`
	Profiles    []ResolvedProfileSnapshot `json:"profiles,omitempty"`
	Manifest    LockedTaskManifest        `json:"manifest"`
	Results     []TaskResult              `json:"results"`
	Labels      []string                  `json:"labels,omitempty"`
	OutDir      string                    `json:"out_dir,omitempty"`
	StartedAt   time.Time                 `json:"started_at"`
	CompletedAt time.Time                 `json:"completed_at,omitempty"`
	EndedAt     time.Time                 `json:"ended_at,omitempty"`
}

// Report renders a benchmark run result.
type Report struct {
	RunID     string                    `json:"run_id"`
	SuiteName string                    `json:"suite_name"`
	Mode      BenchmarkMode             `json:"mode"`
	Profiles  []ResolvedProfileSnapshot `json:"profiles"`
	Totals    ReportTotals              `json:"totals"`
	PerTask   []TaskResult              `json:"per_task"`
	Labels    []string                  `json:"labels,omitempty"` // e.g. "non-comparable" if dirty worktree
}

// ReportTotals aggregates statistics across all tasks.
type ReportTotals struct {
	TotalTasks       int     `json:"total_tasks"`
	Solved           int     `json:"solved"`
	Unsolved         int     `json:"unsolved"`
	Blocked          int     `json:"blocked"`
	Cancelled        int     `json:"cancelled"`
	VerifierFlaky    int     `json:"verifier_flaky"`
	RawSolveRate     float64 `json:"raw_solve_rate"` // solved / total
	FlakeAdjusted    float64 `json:"flake_adjusted"` // solved / (total - flaky)
	ExactCostUSD     float64 `json:"exact_cost_usd"`
	EstimatedCostUSD float64 `json:"estimated_cost_usd"`
}

// LoadRunResult reads a RunResult plus per-task results from outDir.
// It reads <outDir>/run.json for the base RunResult.
func LoadRunResult(outDir string) (RunResult, error) {
	runPath := filepath.Join(outDir, "run.json")
	data, err := os.ReadFile(runPath)
	if err != nil {
		return RunResult{}, fmt.Errorf("bench: load run result from %s: %w", runPath, err)
	}
	var rr RunResult
	if err := json.Unmarshal(data, &rr); err != nil {
		return RunResult{}, fmt.Errorf("bench: parse run.json: %w", err)
	}
	return rr, nil
}

// BuildReport projects a RunResult into a Report.
func BuildReport(rr RunResult) Report {
	totals := aggregateTotals(rr.Results)
	return Report{
		RunID:     rr.RunID,
		SuiteName: rr.SuiteName,
		Mode:      rr.Mode,
		Profiles:  rr.Profiles,
		Totals:    totals,
		PerTask:   rr.Results,
		Labels:    rr.Labels,
	}
}

func aggregateTotals(results []TaskResult) ReportTotals {
	t := ReportTotals{TotalTasks: len(results)}
	for _, r := range results {
		switch r.Status {
		case TaskStatusSolved:
			t.Solved++
		case TaskStatusUnsolved:
			t.Unsolved++
		case TaskStatusBlocked:
			t.Blocked++
		case TaskStatusCancelled:
			t.Cancelled++
		case TaskStatusVerifierFlaky:
			t.VerifierFlaky++
		}
		// Accumulate cost by confidence level.
		for _, u := range r.Usage {
			switch u.Confidence {
			case "exact":
				t.ExactCostUSD += u.CostUSD
			default:
				t.EstimatedCostUSD += u.CostUSD
			}
		}
	}
	if t.TotalTasks > 0 {
		t.RawSolveRate = float64(t.Solved) / float64(t.TotalTasks)
	}
	denominator := t.TotalTasks - t.VerifierFlaky
	if denominator > 0 {
		t.FlakeAdjusted = float64(t.Solved) / float64(denominator)
	}
	return t
}

// modeClaimLabel returns the report-safe claim label for a benchmark mode.
func modeClaimLabel(mode BenchmarkMode) string {
	switch mode {
	case ModeFullVerk:
		return "system result"
	case ModeWorkerOnly:
		return "capability result"
	case ModeRuntimeProbe:
		return "runtime result"
	default:
		return "exploratory"
	}
}

// RenderMarkdown writes a Markdown report.
func RenderMarkdown(w io.Writer, r Report) error {
	claim := modeClaimLabel(r.Mode)

	fmt.Fprintf(w, "# Benchmark Report\n\n")
	fmt.Fprintf(w, "**Run ID:** %s  \n", r.RunID)
	fmt.Fprintf(w, "**Suite:** %s  \n", r.SuiteName)
	fmt.Fprintf(w, "**Mode:** %s (%s)  \n\n", r.Mode, claim)

	// Labels block
	if len(r.Labels) > 0 {
		fmt.Fprintf(w, "## Labels\n\n")
		for _, l := range r.Labels {
			fmt.Fprintf(w, "- %s\n", l)
		}
		fmt.Fprintf(w, "\n")
	}

	// Profile snapshot table
	fmt.Fprintf(w, "## Profiles\n\n")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tWorker Runtime\tWorker Model\tReviewer Runtime\tReviewer Model\tResolved At")
	fmt.Fprintln(tw, "---\t--------------\t------------\t----------------\t--------------\t-----------")
	for _, p := range r.Profiles {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			p.ID,
			p.Worker.Runtime, p.Worker.Model,
			p.Reviewer.Runtime, p.Reviewer.Model,
			p.ResolvedAt.UTC().Format(time.RFC3339),
		)
	}
	tw.Flush()
	fmt.Fprintln(w)

	// Solve rates
	fmt.Fprintf(w, "## Solve Rate\n\n")
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "Metric\tValue")
	fmt.Fprintln(tw, "------\t-----")
	fmt.Fprintf(tw, "Total Tasks\t%d\n", r.Totals.TotalTasks)
	fmt.Fprintf(tw, "Solved\t%d\n", r.Totals.Solved)
	fmt.Fprintf(tw, "Unsolved\t%d\n", r.Totals.Unsolved)
	fmt.Fprintf(tw, "Blocked\t%d\n", r.Totals.Blocked)
	fmt.Fprintf(tw, "Cancelled\t%d\n", r.Totals.Cancelled)
	fmt.Fprintf(tw, "Verifier Flaky\t%d\n", r.Totals.VerifierFlaky)
	fmt.Fprintf(tw, "Raw Solve Rate\t%.1f%%\n", r.Totals.RawSolveRate*100)
	fmt.Fprintf(tw, "Flake-Adjusted Rate\t%.1f%%\n", r.Totals.FlakeAdjusted*100)
	tw.Flush()
	fmt.Fprintln(w)

	// Cost
	fmt.Fprintf(w, "## Cost\n\n")
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "Exact Cost (USD)\tEstimated Cost (USD)")
	fmt.Fprintln(tw, "----------------\t--------------------")
	fmt.Fprintf(tw, "$%.4f\t$%.4f\n", r.Totals.ExactCostUSD, r.Totals.EstimatedCostUSD)
	tw.Flush()
	fmt.Fprintln(w)

	// Per-task table
	fmt.Fprintf(w, "## Per-Task Results\n\n")
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "Task ID\tProfile\tStatus\tRepair Cycles\tReview Cycles\tDuration")
	fmt.Fprintln(tw, "-------\t-------\t------\t-------------\t-------------\t--------")
	for _, tr := range r.PerTask {
		dur := time.Duration(tr.DurationMS) * time.Millisecond
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%s\n",
			tr.TaskID, tr.ProfileID, tr.Status,
			tr.RepairCycles, tr.ReviewCycles, dur.Round(time.Millisecond),
		)
	}
	tw.Flush()
	fmt.Fprintln(w)

	// Non-comparable warning
	if slices.Contains(r.Labels, "non-comparable") {
		fmt.Fprintf(w, "> **Warning:** This run is marked non-comparable and should not be used for external claims.\n\n")
	}

	return nil
}

// RenderJSON writes a JSON report.
func RenderJSON(w io.Writer, r Report) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("bench: render json report: %w", err)
	}
	data = append(data, '\n')
	_, err = w.Write(data)
	return err
}
