package constraints

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// ExecuteResult is the result of running a single constraint.
type ExecuteResult struct {
	ConstraintID string
	Passed       bool
	Output       string
	Err          string
	DurationMs   int64
	TimedOut     bool
}

// Execute runs all active constraints for a ticket verify pass.
// repoRoot is the repo root; worktreePath is the ticket's worktree.
// Results are returned in constraint order; budget exhaustion is noted via TimedOut.
// Returns: (results, overBudget bool, err)
func Execute(ctx context.Context, store *Store, repoRoot, worktreePath string, totalBudgetMs int) ([]ExecuteResult, bool, error) {
	active, err := store.ListActiveConstraints()
	if err != nil {
		return nil, false, fmt.Errorf("list active constraints: %w", err)
	}

	results := make([]ExecuteResult, 0, len(active))
	budgetDeadline := time.Now().Add(time.Duration(totalBudgetMs) * time.Millisecond)
	overBudget := false

	for _, c := range active {
		if time.Now().After(budgetDeadline) {
			overBudget = true
			break
		}

		remaining := time.Until(budgetDeadline)
		timeoutMs := c.Check.TimeoutMs
		if timeoutMs <= 0 {
			timeoutMs = 30000 // 30s default per-constraint
		}
		constraintTimeout := time.Duration(timeoutMs) * time.Millisecond
		if remaining < constraintTimeout {
			constraintTimeout = remaining
		}

		result := runSingleConstraint(ctx, c, repoRoot, worktreePath, constraintTimeout)
		results = append(results, result)
	}

	return results, overBudget, nil
}

func runSingleConstraint(ctx context.Context, c Constraint, repoRoot, worktreePath string, timeout time.Duration) ExecuteResult {
	start := time.Now()
	res := ExecuteResult{ConstraintID: c.ID}

	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	switch c.Check.Type {
	case "grep":
		spec, err := extractGrepSpec(c.Check.Spec)
		if err != nil {
			res.Err = fmt.Sprintf("invalid grep spec: %v", err)
			res.DurationMs = time.Since(start).Milliseconds()
			return res
		}
		output, timedOut, runErr := runGrepConstraint(cctx, repoRoot, spec)
		res.TimedOut = timedOut
		res.DurationMs = time.Since(start).Milliseconds()
		if runErr != nil {
			res.Err = runErr.Error()
			return res
		}
		res.Output = output
		matched := strings.TrimSpace(output) != ""
		if spec.MustNotMatch {
			res.Passed = !matched
		} else {
			// detection mode: always pass (just record output)
			res.Passed = true
		}
	case "command":
		spec, err := extractCommandSpec(c.Check.Spec)
		if err != nil {
			res.Err = fmt.Sprintf("invalid command spec: %v", err)
			res.DurationMs = time.Since(start).Milliseconds()
			return res
		}
		cwd := repoRoot
		if spec.CwdMode == "worktree" {
			cwd = worktreePath
		}
		output, timedOut, runErr := runCommandConstraint(cctx, cwd, spec.Command, spec.Args)
		res.TimedOut = timedOut
		res.DurationMs = time.Since(start).Milliseconds()
		if runErr != nil {
			res.Err = runErr.Error()
			res.Passed = false
			return res
		}
		res.Output = output
		res.Passed = true
	default:
		res.Err = fmt.Sprintf("unknown constraint check type %q", c.Check.Type)
		res.DurationMs = time.Since(start).Milliseconds()
	}

	return res
}

func runGrepConstraint(ctx context.Context, repoRoot string, spec GrepSpec) (string, bool, error) {
	// Build git diff command to get the diff output for the file glob.
	// Use HEAD as base (wave_base_commit support is v2).
	args := []string{"diff", "HEAD", "--", spec.FileGlob}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoRoot

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return "", true, nil
	}
	// git diff exits 0 even if there's nothing; non-zero means error
	if runErr != nil {
		// If git diff fails because there's nothing to diff (no commits), treat as empty
		stderrStr := stderr.String()
		if strings.Contains(stderrStr, "ambiguous argument 'HEAD'") ||
			strings.Contains(stderrStr, "unknown revision") {
			// No commits yet — nothing to match against
			return "", false, nil
		}
		// Other errors: return as informational, not fatal
		return "", false, nil
	}

	diffOutput := stdout.String()

	// Apply the pattern against the diff output.
	re, err := regexp.Compile(spec.Pattern)
	if err != nil {
		return "", false, fmt.Errorf("compile pattern %q: %w", spec.Pattern, err)
	}

	var matches []string
	for _, line := range strings.Split(diffOutput, "\n") {
		if re.MatchString(line) {
			matches = append(matches, line)
		}
	}

	return strings.Join(matches, "\n"), false, nil
}

func runCommandConstraint(ctx context.Context, cwd, command string, args []string) (string, bool, error) {
	cmdArgs := append([]string{command}, args...)
	cmd := exec.CommandContext(ctx, cmdArgs[0], cmdArgs[1:]...)
	cmd.Dir = cwd

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return out.String(), true, nil
	}
	if err != nil {
		return out.String(), false, err
	}
	return out.String(), false, nil
}

func extractGrepSpec(raw interface{}) (GrepSpec, error) {
	data, err := json.Marshal(raw)
	if err != nil {
		return GrepSpec{}, err
	}
	var spec GrepSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		return GrepSpec{}, err
	}
	if spec.FileGlob == "" {
		spec.FileGlob = "**/*"
	}
	return spec, nil
}

func extractCommandSpec(raw interface{}) (CommandSpec, error) {
	data, err := json.Marshal(raw)
	if err != nil {
		return CommandSpec{}, err
	}
	var spec CommandSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		return CommandSpec{}, err
	}
	if spec.Command == "" {
		return CommandSpec{}, fmt.Errorf("command spec missing command field")
	}
	return spec, nil
}

// FailingConstraints returns the subset of results that did not pass
// and are not timed out (timed-out constraints are not blocking).
func FailingConstraints(results []ExecuteResult) []ExecuteResult {
	var out []ExecuteResult
	for _, r := range results {
		if !r.Passed && !r.TimedOut && r.Err == "" {
			out = append(out, r)
		}
	}
	return out
}

// ConstraintFailureSummary returns a human-readable summary of failing constraints.
func ConstraintFailureSummary(failing []ExecuteResult) string {
	ids := make([]string, 0, len(failing))
	for _, r := range failing {
		ids = append(ids, r.ConstraintID)
	}
	return fmt.Sprintf("constraint check(s) failed: %s", strings.Join(ids, ", "))
}
