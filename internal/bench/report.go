package bench

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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
			case confidenceExact:
				t.ExactCostUSD += u.CostUSD
			default:
				t.EstimatedCostUSD += u.CostUSD
			}
		}
		// If Usage is empty but the provider reported a cost on Score, use it.
		if len(r.Usage) == 0 && r.Score.CostUSD > 0 {
			t.EstimatedCostUSD += r.Score.CostUSD
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

const (
	claimSystemResult = "system result"
	confidenceExact   = "exact"
	confidenceEstim   = "estimated"
)

// modeClaimLabel returns the report-safe claim label for a benchmark mode.
func modeClaimLabel(mode BenchmarkMode) string {
	switch mode {
	case ModeFullVerk:
		return claimSystemResult
	case ModeWorkerOnly:
		return "capability result"
	case ModeRuntimeProbe:
		return "runtime result"
	default:
		return designExploratory
	}
}

// RenderMarkdown writes a Markdown report.
func RenderMarkdown(w io.Writer, r Report) error {
	bw := bufio.NewWriter(w)
	claim := modeClaimLabel(r.Mode)

	fmt.Fprintf(bw, "# Benchmark Report\n\n")
	fmt.Fprintf(bw, "**Run ID:** %s  \n", r.RunID)
	fmt.Fprintf(bw, "**Suite:** %s  \n", r.SuiteName)
	fmt.Fprintf(bw, "**Mode:** %s (%s)  \n\n", r.Mode, claim)

	// Labels block
	if len(r.Labels) > 0 {
		fmt.Fprintf(bw, "## Labels\n\n")
		for _, l := range r.Labels {
			fmt.Fprintf(bw, "- %s\n", l)
		}
		fmt.Fprintf(bw, "\n")
	}

	// Profile snapshot table
	fmt.Fprintf(bw, "## Profiles\n\n")
	tw := tabwriter.NewWriter(bw, 0, 0, 2, ' ', 0)
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
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(bw)

	// Solve rates
	fmt.Fprintf(bw, "## Solve Rate\n\n")
	tw = tabwriter.NewWriter(bw, 0, 0, 2, ' ', 0)
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
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(bw)

	// Cost
	fmt.Fprintf(bw, "## Cost\n\n")
	tw = tabwriter.NewWriter(bw, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "Exact Cost (USD)\tEstimated Cost (USD)")
	fmt.Fprintln(tw, "----------------\t--------------------")
	fmt.Fprintf(tw, "$%.4f\t$%.4f\n", r.Totals.ExactCostUSD, r.Totals.EstimatedCostUSD)
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(bw)

	// Per-task table
	fmt.Fprintf(bw, "## Per-Task Results\n\n")
	tw = tabwriter.NewWriter(bw, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "Task ID\tProfile\tStatus\tRepair Cycles\tReview Cycles\tDuration")
	fmt.Fprintln(tw, "-------\t-------\t------\t-------------\t-------------\t--------")
	for _, tr := range r.PerTask {
		dur := time.Duration(tr.DurationMS) * time.Millisecond
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%s\n",
			tr.TaskID, tr.ProfileID, tr.Status,
			tr.RepairCycles, tr.ReviewCycles, dur.Round(time.Millisecond),
		)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(bw)

	// Failure categories
	categoryCounts := make(map[FailureCategory]int)
	for _, tr := range r.PerTask {
		if tr.Failure != "" {
			categoryCounts[tr.Failure]++
		}
	}
	if len(categoryCounts) > 0 {
		fmt.Fprintf(bw, "## Failure Categories\n\n")
		tw = tabwriter.NewWriter(bw, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "Category\tCount")
		fmt.Fprintln(tw, "--------\t-----")
		for _, cat := range []FailureCategory{
			FailureModelLimit, FailureWorkerCrash, FailureReviewerBlock,
			FailureScopeViolation, FailureVerifier, FailureSetup, FailureOther,
		} {
			if n := categoryCounts[cat]; n > 0 {
				fmt.Fprintf(tw, "%s\t%d\n", cat, n)
			}
		}
		if err := tw.Flush(); err != nil {
			return err
		}
		fmt.Fprintln(bw)
	}

	// Per-profile breakdown table
	type profileStats struct {
		tasks   int
		solved  int
		repair  int
		review  int
		costUSD float64
	}
	profileMap := make(map[string]*profileStats)
	for _, tr := range r.PerTask {
		ps := profileMap[tr.ProfileID]
		if ps == nil {
			ps = &profileStats{}
			profileMap[tr.ProfileID] = ps
		}
		ps.tasks++
		if tr.Status == TaskStatusSolved {
			ps.solved++
		}
		ps.repair += tr.RepairCycles
		ps.review += tr.ReviewCycles
		for _, u := range tr.Usage {
			ps.costUSD += u.CostUSD
		}
		if len(tr.Usage) == 0 && tr.Score.CostUSD > 0 {
			ps.costUSD += tr.Score.CostUSD
		}
	}
	if len(profileMap) > 0 {
		fmt.Fprintf(bw, "## Per-Profile Breakdown\n\n")
		tw = tabwriter.NewWriter(bw, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "Profile\tTasks\tSolved\tSolve %\tAvg Repair\tAvg Review\tTotal Cost")
		fmt.Fprintln(tw, "-------\t-----\t------\t-------\t----------\t----------\t----------")
		// iterate r.Profiles for stable ordering; profiles absent from the snapshot
		// but present in PerTask will appear at the end.
		seen := make(map[string]bool)
		for _, prof := range r.Profiles {
			ps := profileMap[prof.ID]
			if ps == nil || ps.tasks == 0 {
				continue
			}
			seen[prof.ID] = true
			solveRate := 100.0 * float64(ps.solved) / float64(ps.tasks)
			avgRepair := float64(ps.repair) / float64(ps.tasks)
			avgReview := float64(ps.review) / float64(ps.tasks)
			fmt.Fprintf(tw, "%s\t%d\t%d\t%.1f%%\t%.2f\t%.2f\t$%.4f\n",
				prof.ID, ps.tasks, ps.solved, solveRate, avgRepair, avgReview, ps.costUSD)
		}
		// Include profile IDs not in the snapshot but present in PerTask.
		for id, ps := range profileMap {
			if seen[id] || ps.tasks == 0 {
				continue
			}
			solveRate := 100.0 * float64(ps.solved) / float64(ps.tasks)
			avgRepair := float64(ps.repair) / float64(ps.tasks)
			avgReview := float64(ps.review) / float64(ps.tasks)
			fmt.Fprintf(tw, "%s\t%d\t%d\t%.1f%%\t%.2f\t%.2f\t$%.4f\n",
				id, ps.tasks, ps.solved, solveRate, avgRepair, avgReview, ps.costUSD)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
		fmt.Fprintln(bw)
	}

	// Non-comparable warning
	if slices.Contains(r.Labels, "non-comparable") {
		fmt.Fprintf(bw, "> **Warning:** This run is marked non-comparable and should not be used for external claims.\n\n")
	}

	return bw.Flush()
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

// RenderCSV writes a CSV report (one row per task result).
// Columns: task_id,profile_id,status,failure,repair_cycles,review_cycles,duration_ms,cost_usd,cost_confidence
func RenderCSV(w io.Writer, r Report) error {
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{
		"task_id", "profile_id", "status", "failure",
		"repair_cycles", "review_cycles", "duration_ms", "cost_usd", "cost_confidence",
	}); err != nil {
		return fmt.Errorf("bench: render csv header: %w", err)
	}
	for _, tr := range r.PerTask {
		var totalCost float64
		var hasExact, hasEstimated bool
		for _, u := range tr.Usage {
			totalCost += u.CostUSD
			switch defaultUsageConfidence(u) {
			case confidenceExact:
				hasExact = true
			default:
				if u.CostUSD > 0 {
					hasEstimated = true
				}
			}
		}
		if len(tr.Usage) == 0 && tr.Score.CostUSD > 0 {
			totalCost = tr.Score.CostUSD
			hasEstimated = true
		}
		// Mixed exact+estimated degrades to estimated.
		var confidence string
		switch {
		case hasExact && !hasEstimated:
			confidence = confidenceExact
		case totalCost > 0:
			confidence = confidenceEstim
		default:
			confidence = "unavailable"
		}
		if err := cw.Write([]string{
			tr.TaskID,
			tr.ProfileID,
			string(tr.Status),
			string(tr.Failure),
			strconv.Itoa(tr.RepairCycles),
			strconv.Itoa(tr.ReviewCycles),
			strconv.FormatInt(tr.DurationMS, 10),
			strconv.FormatFloat(totalCost, 'f', 6, 64),
			confidence,
		}); err != nil {
			return fmt.Errorf("bench: render csv row for task %q: %w", tr.TaskID, err)
		}
	}
	cw.Flush()
	return cw.Error()
}

// defaultUsageConfidence returns a confidence label for a UsageRecord.
// Returns the existing confidence if set, "estimated" when cost is non-zero,
// or "unavailable" when there is no cost data.
func defaultUsageConfidence(u UsageRecord) string {
	if u.Confidence != "" {
		return u.Confidence
	}
	if u.CostUSD > 0 {
		return confidenceEstim
	}
	return "unavailable"
}
