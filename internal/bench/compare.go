package bench

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"text/tabwriter"
)

// Outcome labels used by classifyOutcome and buildDiff.
const (
	outcomeRegressed = "regressed"
	outcomeImproved  = "improved"
	outcomeUnchanged = "unchanged"
	outcomeMissing   = "missing"
)

// Comparison contrasts two runs.
type Comparison struct {
	Baseline  RunResult              `json:"baseline"`
	Candidate RunResult              `json:"candidate"`
	Pairs     []PairedTaskComparison `json:"pairs"`
	Diff      ComparisonDiff         `json:"diff"`
	Refusal   string                 `json:"refusal,omitempty"` // populated when comparison is rejected
}

// PairedTaskComparison pairs a baseline and candidate result for a single task.
type PairedTaskComparison struct {
	TaskID    string      `json:"task_id"`
	Baseline  *TaskResult `json:"baseline,omitempty"`  // nil if missing
	Candidate *TaskResult `json:"candidate,omitempty"` // nil if missing
	Outcome   string      `json:"outcome"`             // "regressed"|"improved"|"unchanged"|"missing"
}

// ComparisonDiff summarizes the differences between two runs.
type ComparisonDiff struct {
	RegressedCount     int     `json:"regressed_count"`
	ImprovedCount      int     `json:"improved_count"`
	UnchangedCount     int     `json:"unchanged_count"`
	MissingCount       int     `json:"missing_count"`
	ExactCostDelta     float64 `json:"exact_cost_delta"`
	EstimatedCostDelta float64 `json:"estimated_cost_delta"`
}

// Compare returns a Comparison. Sets Refusal when runs are non-comparable
// (different suites, different locked manifests, missing complete.json,
// either run marked non-comparable).
func Compare(baseline, candidate RunResult) Comparison {
	// Check refusal conditions.
	if baseline.SuiteName != candidate.SuiteName {
		return Comparison{
			Baseline:  baseline,
			Candidate: candidate,
			Refusal:   fmt.Sprintf("suite mismatch: baseline=%q candidate=%q", baseline.SuiteName, candidate.SuiteName),
		}
	}

	if !taskIDsEqual(baseline.Manifest.TaskIDs, candidate.Manifest.TaskIDs) {
		return Comparison{
			Baseline:  baseline,
			Candidate: candidate,
			Refusal:   "locked manifest task lists differ between runs",
		}
	}

	if slices.Contains(baseline.Labels, "non-comparable") {
		return Comparison{
			Baseline:  baseline,
			Candidate: candidate,
			Refusal:   "baseline run is marked non-comparable",
		}
	}

	if slices.Contains(candidate.Labels, "non-comparable") {
		return Comparison{
			Baseline:  baseline,
			Candidate: candidate,
			Refusal:   "candidate run is marked non-comparable",
		}
	}

	// Note: complete.json check is reflected in the Labels. When a run lacks
	// a complete marker its results are not trustworthy; we check by looking
	// for a "incomplete" label that the runner would stamp on incomplete runs.
	if slices.Contains(baseline.Labels, "incomplete") {
		return Comparison{
			Baseline:  baseline,
			Candidate: candidate,
			Refusal:   "baseline run is missing complete marker",
		}
	}

	if slices.Contains(candidate.Labels, "incomplete") {
		return Comparison{
			Baseline:  baseline,
			Candidate: candidate,
			Refusal:   "candidate run is missing complete marker",
		}
	}

	pairs := pairResults(baseline.Results, candidate.Results)
	diff := buildDiff(pairs, baseline, candidate)

	return Comparison{
		Baseline:  baseline,
		Candidate: candidate,
		Pairs:     pairs,
		Diff:      diff,
	}
}

func taskIDsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]struct{}, len(a))
	for _, id := range a {
		set[id] = struct{}{}
	}
	for _, id := range b {
		if _, ok := set[id]; !ok {
			return false
		}
	}
	return true
}

func pairResults(baseline, candidate []TaskResult) []PairedTaskComparison {
	bMap := make(map[string]*TaskResult, len(baseline))
	for i := range baseline {
		bMap[baseline[i].TaskID] = &baseline[i]
	}
	cMap := make(map[string]*TaskResult, len(candidate))
	for i := range candidate {
		cMap[candidate[i].TaskID] = &candidate[i]
	}

	// Collect all task IDs from both sides.
	seen := make(map[string]struct{})
	var taskIDs []string
	for _, r := range baseline {
		if _, ok := seen[r.TaskID]; !ok {
			taskIDs = append(taskIDs, r.TaskID)
			seen[r.TaskID] = struct{}{}
		}
	}
	for _, r := range candidate {
		if _, ok := seen[r.TaskID]; !ok {
			taskIDs = append(taskIDs, r.TaskID)
			seen[r.TaskID] = struct{}{}
		}
	}

	pairs := make([]PairedTaskComparison, 0, len(taskIDs))
	for _, id := range taskIDs {
		b := bMap[id]
		c := cMap[id]
		outcome := classifyOutcome(b, c)
		pairs = append(pairs, PairedTaskComparison{
			TaskID:    id,
			Baseline:  b,
			Candidate: c,
			Outcome:   outcome,
		})
	}
	return pairs
}

// classifyOutcome determines whether a task regressed, improved, stayed unchanged, or is missing.
func classifyOutcome(b, c *TaskResult) string {
	if b == nil || c == nil {
		return outcomeMissing
	}
	bSolved := b.Status == TaskStatusSolved
	cSolved := c.Status == TaskStatusSolved
	switch {
	case bSolved && !cSolved:
		return outcomeRegressed
	case !bSolved && cSolved:
		return outcomeImproved
	default:
		return outcomeUnchanged
	}
}

func buildDiff(pairs []PairedTaskComparison, baseline, candidate RunResult) ComparisonDiff {
	d := ComparisonDiff{}
	for _, p := range pairs {
		switch p.Outcome {
		case outcomeRegressed:
			d.RegressedCount++
		case outcomeImproved:
			d.ImprovedCount++
		case outcomeUnchanged:
			d.UnchangedCount++
		case outcomeMissing:
			d.MissingCount++
		}
	}

	baseTotals := aggregateTotals(baseline.Results)
	candTotals := aggregateTotals(candidate.Results)
	d.ExactCostDelta = candTotals.ExactCostUSD - baseTotals.ExactCostUSD
	d.EstimatedCostDelta = candTotals.EstimatedCostUSD - baseTotals.EstimatedCostUSD
	return d
}

// RenderComparisonMarkdown writes a Markdown comparison report.
func RenderComparisonMarkdown(w io.Writer, cmp Comparison) error {
	fmt.Fprintf(w, "# Benchmark Comparison\n\n")
	fmt.Fprintf(w, "**Baseline:** %s (%s)  \n", cmp.Baseline.RunID, cmp.Baseline.SuiteName)
	fmt.Fprintf(w, "**Candidate:** %s (%s)  \n\n", cmp.Candidate.RunID, cmp.Candidate.SuiteName)

	if cmp.Refusal != "" {
		fmt.Fprintf(w, "> **Refused:** %s\n\n", cmp.Refusal)
		return nil
	}

	// Diff summary
	fmt.Fprintf(w, "## Summary\n\n")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "Metric\tValue")
	fmt.Fprintln(tw, "------\t-----")
	fmt.Fprintf(tw, "Improved\t%d\n", cmp.Diff.ImprovedCount)
	fmt.Fprintf(tw, "Regressed\t%d\n", cmp.Diff.RegressedCount)
	fmt.Fprintf(tw, "Unchanged\t%d\n", cmp.Diff.UnchangedCount)
	fmt.Fprintf(tw, "Missing\t%d\n", cmp.Diff.MissingCount)
	fmt.Fprintf(tw, "Exact Cost Delta\t$%.4f\n", cmp.Diff.ExactCostDelta)
	fmt.Fprintf(tw, "Estimated Cost Delta\t$%.4f\n", cmp.Diff.EstimatedCostDelta)
	tw.Flush()
	fmt.Fprintln(w)

	// Per-task pairs
	fmt.Fprintf(w, "## Per-Task Comparison\n\n")
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "Task ID\tOutcome\tBaseline Status\tCandidate Status")
	fmt.Fprintln(tw, "-------\t-------\t---------------\t----------------")
	for _, p := range cmp.Pairs {
		bStatus := "-"
		cStatus := "-"
		if p.Baseline != nil {
			bStatus = string(p.Baseline.Status)
		}
		if p.Candidate != nil {
			cStatus = string(p.Candidate.Status)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", p.TaskID, p.Outcome, bStatus, cStatus)
	}
	tw.Flush()
	fmt.Fprintln(w)

	return nil
}

// RenderComparisonJSON writes a JSON comparison report.
func RenderComparisonJSON(w io.Writer, cmp Comparison) error {
	data, err := json.MarshalIndent(cmp, "", "  ")
	if err != nil {
		return fmt.Errorf("bench: render json comparison: %w", err)
	}
	data = append(data, '\n')
	_, err = w.Write(data)
	return err
}
