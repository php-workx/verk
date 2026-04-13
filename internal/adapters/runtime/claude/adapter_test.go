package claude

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"verk/internal/adapters/runtime"
)

// mockCLIOutput builds a JSON response matching `claude -p --output-format json`.
func mockCLIOutput(resultText string, isError bool) []byte {
	output := runtime.CLIOutputJSON{
		Type:       "result",
		Subtype:    "success",
		IsError:    isError,
		NumTurns:   3,
		Result:     resultText,
		SessionID:  "test-session",
		DurationMS: 5000,
	}
	if isError {
		output.Subtype = "error"
	}
	data, _ := json.Marshal(output)
	return data
}

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
		if binary != "claude-test" {
			t.Fatalf("expected binary claude-test, got %q", binary)
		}
		if !hasArg(args, "-p") {
			t.Fatalf("expected -p flag in args: %v", args)
		}
		assertArgValue(t, args, "--output-format", "json")

		promptText := string(stdin)
		if !strings.Contains(promptText, "ticket-1") {
			t.Fatalf("expected prompt to contain ticket id, got: %s", promptText)
		}
		if !strings.Contains(promptText, "Attempt: 2") {
			t.Fatalf("expected prompt to contain attempt number, got: %s", promptText)
		}

		if timeout != 7*time.Minute {
			t.Fatalf("expected worker timeout 7m, got %s", timeout)
		}
		// env is nil — subprocess inherits full parent environment

		// AI returns JSON-only as instructed
		resultJSON := `{"status":"done_with_concerns","completion_code":"ok","concerns":["minor style issue in helper"]}`

		return commandResult{
			stdout:   mockCLIOutput(resultJSON, false),
			stderr:   []byte("worker log"),
			exitCode: 0,
		}, nil
	}

	adapter := NewWithCommand("claude-test")
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
	if len(result.Concerns) != 1 || result.Concerns[0] != "minor style issue in helper" {
		t.Fatalf("expected concerns [minor style issue in helper], got %v", result.Concerns)
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

func TestRunWorker_BlockedStatus(t *testing.T) {
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
		resultJSON := `{"status":"blocked","completion_code":"missing_dependency","block_reason":"required service unavailable"}`
		return commandResult{
			stdout:   mockCLIOutput(resultJSON, false),
			exitCode: 0,
		}, nil
	}

	adapter := NewWithCommand("claude-test")
	result, err := adapter.RunWorker(context.Background(), runtime.WorkerRequest{
		LeaseID:  "lease-1",
		TicketID: "ticket-1",
	})
	if err != nil {
		t.Fatalf("RunWorker returned error: %v", err)
	}
	if result.Status != runtime.WorkerStatusBlocked {
		t.Fatalf("expected blocked, got %q", result.Status)
	}
	if result.CompletionCode != "missing_dependency" {
		t.Fatalf("expected completion code missing_dependency, got %q", result.CompletionCode)
	}
}

func TestRunWorker_FallbackWhenNoResultBlock(t *testing.T) {
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
		// AI didn't follow JSON-only instruction — returned prose
		resultText := "I made all the changes. Everything looks good."
		return commandResult{
			stdout:   mockCLIOutput(resultText, false),
			exitCode: 0,
		}, nil
	}

	adapter := NewWithCommand("claude-test")
	result, err := adapter.RunWorker(context.Background(), runtime.WorkerRequest{
		LeaseID:  "lease-1",
		TicketID: "ticket-1",
	})
	if err != nil {
		t.Fatalf("RunWorker returned error: %v", err)
	}
	// Without parseable JSON, successful CLI exit → done
	if result.Status != runtime.WorkerStatusDone {
		t.Fatalf("expected done fallback, got %q", result.Status)
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
		if binary != "claude-test" {
			t.Fatalf("expected binary claude-test, got %q", binary)
		}
		if !hasArg(args, "-p") {
			t.Fatalf("expected -p flag in args: %v", args)
		}
		assertArgValue(t, args, "--output-format", "json")

		if timeout != 9*time.Minute {
			t.Fatalf("expected reviewer timeout 9m, got %s", timeout)
		}

		resultJSON := `{"review_status":"findings","summary":"needs fixes","findings":[{"severity":"P2","title":"blocking issue","body":"blocking issue","file":"internal/example.go","line":12,"disposition":"open"}]}`

		return commandResult{
			stdout: mockCLIOutput(resultJSON, false),
			stderr: []byte("review log"),
		}, nil
	}

	adapter := NewWithCommand("claude-test")
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
		resultJSON := `{"review_status":"passed","summary":"clean implementation, all criteria met","findings":[]}`
		return commandResult{
			stdout: mockCLIOutput(resultJSON, false),
		}, nil
	}

	adapter := NewWithCommand("claude-test")
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
		if binary != "claude-test" {
			t.Fatalf("expected binary claude-test, got %q", binary)
		}
		if len(args) != 1 || args[0] != "--version" {
			t.Fatalf("expected version probe, got %v", args)
		}
		return commandResult{stdout: []byte("claude 1.0.0")}, nil
	}

	if err := NewWithCommand("claude-test").CheckAvailability(context.Background()); err != nil {
		t.Fatalf("expected availability probe to pass, got %v", err)
	}
	if !probed {
		t.Fatalf("expected availability probe to run")
	}
}

func TestRunWorker_NeedsContextStatus(t *testing.T) {
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
		resultJSON := `{"status":"needs_context","completion_code":"missing_spec","block_reason":"acceptance criteria unclear"}`
		return commandResult{
			stdout:   mockCLIOutput(resultJSON, false),
			exitCode: 0,
		}, nil
	}

	adapter := NewWithCommand("claude-test")
	result, err := adapter.RunWorker(context.Background(), runtime.WorkerRequest{
		LeaseID:  "lease-1",
		TicketID: "ticket-1",
	})
	if err != nil {
		t.Fatalf("RunWorker returned error: %v", err)
	}
	if result.Status != runtime.WorkerStatusNeedsContext {
		t.Fatalf("expected needs_context, got %q", result.Status)
	}
	if result.RetryClass != runtime.RetryClassBlockedByOperatorInput {
		t.Fatalf("expected blocked_by_operator_input, got %q", result.RetryClass)
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

func assertArgValue(t *testing.T, args []string, flag, want string) {
	t.Helper()
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			if args[i+1] != want {
				t.Fatalf("expected %s %q, got %q", flag, want, args[i+1])
			}
			return
		}
	}
	t.Fatalf("expected flag %s in args: %v", flag, args)
}
