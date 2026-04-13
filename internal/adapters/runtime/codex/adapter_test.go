package codex

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"verk/internal/adapters/runtime"
)

func TestRunWorker_NormalizesAndCapturesArtifacts(t *testing.T) {
	oldRunCommand := runCommand
	oldNow := now
	defer func() {
		runCommand = oldRunCommand
		now = oldNow
	}()

	times := []time.Time{
		time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC),
		time.Date(2026, 4, 2, 12, 5, 0, 0, time.UTC),
	}
	now = func() time.Time {
		value := times[0]
		times = times[1:]
		return value
	}
	runCommand = func(ctx context.Context, binary string, args []string, stdin []byte, env []string, timeout time.Duration) (commandResult, error) {
		t.Helper()
		if binary != "codex-test" {
			t.Fatalf("expected binary codex-test, got %q", binary)
		}
		if !hasArg(args, "exec") {
			t.Fatalf("expected exec subcommand in args: %v", args)
		}
		if !hasArg(args, "--json") {
			t.Fatalf("expected --json flag in args: %v", args)
		}
		if !hasArg(args, "--full-auto") {
			t.Fatalf("expected --full-auto flag in args: %v", args)
		}

		lastArg := args[len(args)-1]
		if !strings.Contains(lastArg, "ticket-1") {
			t.Fatalf("expected prompt to contain ticket id, got last arg: %s", lastArg)
		}

		if timeout != 7*time.Minute {
			t.Fatalf("expected worker timeout 7m, got %s", timeout)
		}

		// AI returns JSON-only as instructed
		outputJSON := `{"status":"done_with_concerns","completion_code":"ok","concerns":["minor style issue"]}`

		return commandResult{
			stdout:   []byte(outputJSON),
			stderr:   []byte("worker log"),
			exitCode: 0,
		}, nil
	}

	adapter := NewWithCommand("codex-test")
	result, err := adapter.RunWorker(context.Background(), runtime.WorkerRequest{
		LeaseID:      "lease-1",
		RunID:        "run-1",
		TicketID:     "ticket-1",
		Attempt:      2,
		WorktreePath: "/tmp/worktree",
		ExecutionConfig: runtime.ExecutionConfig{
			WorkerTimeoutMinutes: 7,
			AuthEnvVars:          []string{"VERK_API_KEY"},
		},
	})
	if err != nil {
		t.Fatalf("RunWorker returned error: %v", err)
	}

	if result.Status != runtime.WorkerStatusDoneWithConcerns {
		t.Fatalf("expected done_with_concerns, got %q", result.Status)
	}
	if result.RetryClass != runtime.RetryClassTerminal {
		t.Fatalf("expected terminal retry class, got %q", result.RetryClass)
	}
	if len(result.Concerns) != 1 || result.Concerns[0] != "minor style issue" {
		t.Fatalf("expected concerns [minor style issue], got %v", result.Concerns)
	}
	if result.LeaseID != "lease-1" {
		t.Fatalf("expected lease id lease-1, got %q", result.LeaseID)
	}
	if result.StdoutPath == "" || result.StderrPath == "" || result.ResultArtifactPath == "" {
		t.Fatalf("expected captured artifact paths, got %#v", result)
	}

	stderrBytes, err := os.ReadFile(result.StderrPath)
	if err != nil {
		t.Fatalf("read stderr artifact: %v", err)
	}
	if string(stderrBytes) != "worker log" {
		t.Fatalf("unexpected stderr capture: %s", string(stderrBytes))
	}

	artifactBytes, err := os.ReadFile(result.ResultArtifactPath)
	if err != nil {
		t.Fatalf("read result artifact: %v", err)
	}
	var artifact map[string]any
	if err := json.Unmarshal(artifactBytes, &artifact); err != nil {
		t.Fatalf("artifact is not valid JSON: %v", err)
	}
	normalized, _ := artifact["normalized"].(map[string]any)
	if normalized["lease_id"] != "lease-1" {
		t.Fatalf("expected normalized lease id, got %#v", normalized["lease_id"])
	}
	if normalized["status"] != string(runtime.WorkerStatusDoneWithConcerns) {
		t.Fatalf("unexpected normalized status: %#v", normalized["status"])
	}
}

func TestRunReviewer_NormalizesFindingsAndDerivesStatus(t *testing.T) {
	oldRunCommand := runCommand
	oldNow := now
	defer func() {
		runCommand = oldRunCommand
		now = oldNow
	}()

	times := []time.Time{
		time.Date(2026, 4, 2, 13, 0, 0, 0, time.UTC),
		time.Date(2026, 4, 2, 13, 3, 0, 0, time.UTC),
	}
	now = func() time.Time {
		value := times[0]
		times = times[1:]
		return value
	}
	runCommand = func(ctx context.Context, binary string, args []string, stdin []byte, env []string, timeout time.Duration) (commandResult, error) {
		t.Helper()
		if binary != "codex-test" {
			t.Fatalf("expected binary codex-test, got %q", binary)
		}
		if !hasArg(args, "exec") {
			t.Fatalf("expected exec subcommand in args: %v", args)
		}

		if timeout != 9*time.Minute {
			t.Fatalf("expected reviewer timeout 9m, got %s", timeout)
		}

		outputJSON := `{"review_status":"findings","summary":"needs fixes","findings":[{"severity":"P2","title":"blocking issue","body":"blocking issue","file":"internal/example.go","line":12,"disposition":"open"}]}`

		return commandResult{
			stdout: []byte(outputJSON),
			stderr: []byte("review log"),
		}, nil
	}

	adapter := NewWithCommand("codex-test")
	result, err := adapter.RunReviewer(context.Background(), runtime.ReviewRequest{
		LeaseID:                  "lease-2",
		RunID:                    "run-2",
		TicketID:                 "ticket-2",
		EffectiveReviewThreshold: runtime.SeverityP2,
		ExecutionConfig: runtime.ExecutionConfig{
			ReviewerTimeoutMinutes: 9,
			AuthEnvVars:            []string{"VERK_API_KEY"},
		},
	})
	if err != nil {
		t.Fatalf("RunReviewer returned error: %v", err)
	}

	if result.Status != runtime.WorkerStatusDoneWithConcerns {
		t.Fatalf("expected done_with_concerns review process status, got %q", result.Status)
	}
	if result.ReviewStatus != runtime.ReviewStatusFindings {
		t.Fatalf("expected findings review status, got %q", result.ReviewStatus)
	}
	if result.RetryClass != runtime.RetryClassTerminal {
		t.Fatalf("expected terminal retry class, got %q", result.RetryClass)
	}
	if len(result.Findings) != 1 {
		t.Fatalf("expected one normalized finding, got %d", len(result.Findings))
	}
	if result.Findings[0].ID == "" {
		t.Fatalf("expected synthesized finding id")
	}
	if result.Findings[0].Severity != runtime.SeverityP2 {
		t.Fatalf("expected canonical severity P2, got %q", result.Findings[0].Severity)
	}
	if result.Findings[0].Disposition != runtime.ReviewDispositionOpen {
		t.Fatalf("expected canonical open disposition, got %q", result.Findings[0].Disposition)
	}
	if result.Summary != "needs fixes" {
		t.Fatalf("expected summary 'needs fixes', got %q", result.Summary)
	}
}

func TestRunReviewer_PassedReview(t *testing.T) {
	oldRunCommand := runCommand
	oldNow := now
	defer func() {
		runCommand = oldRunCommand
		now = oldNow
	}()

	now = func() time.Time {
		return time.Date(2026, 4, 2, 14, 0, 0, 0, time.UTC)
	}

	runCommand = func(ctx context.Context, binary string, args []string, stdin []byte, env []string, timeout time.Duration) (commandResult, error) {
		outputJSON := `{"review_status":"passed","summary":"clean implementation","findings":[]}`
		return commandResult{
			stdout: []byte(outputJSON),
		}, nil
	}

	adapter := NewWithCommand("codex-test")
	result, err := adapter.RunReviewer(context.Background(), runtime.ReviewRequest{
		LeaseID:                  "lease-3",
		EffectiveReviewThreshold: runtime.SeverityP2,
	})
	if err != nil {
		t.Fatalf("RunReviewer returned error: %v", err)
	}
	if result.ReviewStatus != runtime.ReviewStatusPassed {
		t.Fatalf("expected passed, got %q", result.ReviewStatus)
	}
	if result.Status != runtime.WorkerStatusDone {
		t.Fatalf("expected done, got %q", result.Status)
	}
}

func TestCheckAvailability_UsesVersionProbe(t *testing.T) {
	oldRunCommand := runCommand
	defer func() { runCommand = oldRunCommand }()

	probed := false
	runCommand = func(ctx context.Context, binary string, args []string, stdin []byte, env []string, timeout time.Duration) (commandResult, error) {
		probed = true
		if binary != "codex-test" {
			t.Fatalf("expected binary codex-test, got %q", binary)
		}
		if len(args) != 1 || args[0] != "--version" {
			t.Fatalf("expected version probe, got %v", args)
		}
		return commandResult{stdout: []byte("codex 1.0.0")}, nil
	}

	if err := NewWithCommand("codex-test").CheckAvailability(context.Background()); err != nil {
		t.Fatalf("expected availability probe to pass, got %v", err)
	}
	if !probed {
		t.Fatalf("expected availability probe to run")
	}
}

func TestRunWorker_FallbackWhenNoJSON(t *testing.T) {
	oldRunCommand := runCommand
	oldNow := now
	defer func() {
		runCommand = oldRunCommand
		now = oldNow
	}()

	now = func() time.Time {
		return time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC)
	}

	runCommand = func(ctx context.Context, binary string, args []string, stdin []byte, env []string, timeout time.Duration) (commandResult, error) {
		return commandResult{
			stdout:   []byte("I made all the changes. Everything looks good."),
			exitCode: 0,
		}, nil
	}

	adapter := NewWithCommand("codex-test")
	result, err := adapter.RunWorker(context.Background(), runtime.WorkerRequest{
		LeaseID:  "lease-1",
		TicketID: "ticket-1",
	})
	if err != nil {
		t.Fatalf("RunWorker returned error: %v", err)
	}
	if result.Status != runtime.WorkerStatusDone {
		t.Fatalf("expected done fallback, got %q", result.Status)
	}
}

func TestRunWorker_SentinelFallback(t *testing.T) {
	oldRunCommand := runCommand
	oldNow := now
	defer func() {
		runCommand = oldRunCommand
		now = oldNow
	}()

	now = func() time.Time {
		return time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC)
	}

	runCommand = func(ctx context.Context, binary string, args []string, stdin []byte, env []string, timeout time.Duration) (commandResult, error) {
		// AI mixed prose with a sentinel-prefixed JSON line
		output := "Done with changes.\nVERK_RESULT:{\"status\":\"done\",\"completion_code\":\"ok\"}"
		return commandResult{
			stdout:   []byte(output),
			exitCode: 0,
		}, nil
	}

	adapter := NewWithCommand("codex-test")
	result, err := adapter.RunWorker(context.Background(), runtime.WorkerRequest{
		LeaseID:  "lease-1",
		TicketID: "ticket-1",
	})
	if err != nil {
		t.Fatalf("RunWorker returned error: %v", err)
	}
	if result.Status != runtime.WorkerStatusDone {
		t.Fatalf("expected done via sentinel fallback, got %q", result.Status)
	}
	if result.CompletionCode != "ok" {
		t.Fatalf("expected completion code ok, got %q", result.CompletionCode)
	}
}

func hasArg(args []string, value string) bool {
	for _, arg := range args {
		if arg == value {
			return true
		}
	}
	return false
}
