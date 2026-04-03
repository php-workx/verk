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
	t.Setenv("VERK_API_KEY", "test-key")
	t.Setenv("VERK_SECRET_TOKEN", "should-not-leak")

	runCommand = func(ctx context.Context, binary string, args []string, stdin []byte, env []string, timeout time.Duration) (commandResult, error) {
		t.Helper()
		if binary != "codex-test" {
			t.Fatalf("expected binary codex-test, got %q", binary)
		}
		if !hasArg(args, "worker") {
			t.Fatalf("expected worker subcommand in args: %v", args)
		}
		assertArgValue(t, args, "--lease-id", "lease-1")
		assertArgValue(t, args, "--runtime", runtimeName)

		var req runtime.WorkerRequest
		if err := json.Unmarshal(stdin, &req); err != nil {
			t.Fatalf("unexpected worker request payload: %v", err)
		}
		if req.LeaseID != "lease-1" {
			t.Fatalf("expected lease id lease-1, got %q", req.LeaseID)
		}
		if req.Runtime != runtimeName {
			t.Fatalf("expected runtime %q, got %q", runtimeName, req.Runtime)
		}
		if timeout != 7*time.Minute {
			t.Fatalf("expected worker timeout 7m, got %s", timeout)
		}
		assertEnvValue(t, env, "VERK_API_KEY", "test-key")
		assertEnvMissing(t, env, "VERK_SECRET_TOKEN")

		return commandResult{
			stdout:   []byte(`{"status":"done_with_concerns","completion_code":"ok","lease_id":"lease-1","result_artifact_path":"runtime-worker.json"}`),
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
	if result.LeaseID != "lease-1" {
		t.Fatalf("expected lease id lease-1, got %q", result.LeaseID)
	}
	if result.StdoutPath == "" || result.StderrPath == "" || result.ResultArtifactPath == "" {
		t.Fatalf("expected captured artifact paths, got %#v", result)
	}

	stdoutBytes, err := os.ReadFile(result.StdoutPath)
	if err != nil {
		t.Fatalf("read stdout artifact: %v", err)
	}
	if string(stdoutBytes) != `{"status":"done_with_concerns","completion_code":"ok","lease_id":"lease-1","result_artifact_path":"runtime-worker.json"}` {
		t.Fatalf("unexpected stdout capture: %s", string(stdoutBytes))
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
	t.Setenv("VERK_API_KEY", "review-key")
	t.Setenv("VERK_SECRET_TOKEN", "should-not-leak")

	runCommand = func(ctx context.Context, binary string, args []string, stdin []byte, env []string, timeout time.Duration) (commandResult, error) {
		t.Helper()
		if binary != "codex-test" {
			t.Fatalf("expected binary codex-test, got %q", binary)
		}
		if !hasArg(args, "review") {
			t.Fatalf("expected review subcommand in args: %v", args)
		}
		assertArgValue(t, args, "--lease-id", "lease-2")
		assertArgValue(t, args, "--runtime", runtimeName)
		assertArgValue(t, args, "--effective-review-threshold", string(runtime.SeverityP2))
		if !hasArg(args, "--fresh-context") {
			t.Fatalf("expected fresh reviewer flag in args: %v", args)
		}

		var req runtime.ReviewRequest
		if err := json.Unmarshal(stdin, &req); err != nil {
			t.Fatalf("unexpected review request payload: %v", err)
		}
		if req.LeaseID != "lease-2" {
			t.Fatalf("expected lease id lease-2, got %q", req.LeaseID)
		}
		if req.Runtime != runtimeName {
			t.Fatalf("expected runtime %q, got %q", runtimeName, req.Runtime)
		}
		if timeout != 9*time.Minute {
			t.Fatalf("expected reviewer timeout 9m, got %s", timeout)
		}
		assertEnvValue(t, env, "VERK_API_KEY", "review-key")
		assertEnvMissing(t, env, "VERK_SECRET_TOKEN")

		return commandResult{
			stdout: []byte(`{"status":"done","completion_code":"reviewed","lease_id":"lease-2","review_status":"findings","summary":"needs fixes","findings":[{"severity":"p2","title":"blocking issue","body":"blocking issue","file":"internal/example.go","line":12,"disposition":"open"}]}`),
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

	if result.Status != runtime.WorkerStatusDone {
		t.Fatalf("expected done review process status, got %q", result.Status)
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
}

func TestRunReviewer_RejectsContradictoryReviewStatus(t *testing.T) {
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
		return commandResult{
			stdout: []byte(`{"status":"done","lease_id":"lease-3","review_status":"passed","summary":"looks good","findings":[{"severity":"P2","title":"blocking issue","body":"blocking issue","file":"internal/example.go","line":12,"disposition":"open"}]}`),
		}, nil
	}

	adapter := NewWithCommand("codex-test")
	_, err := adapter.RunReviewer(context.Background(), runtime.ReviewRequest{
		LeaseID:                  "lease-3",
		EffectiveReviewThreshold: runtime.SeverityP2,
	})
	if err == nil {
		t.Fatalf("expected contradictory review status to fail")
	}
	if !strings.Contains(err.Error(), "contradicts derived status") {
		t.Fatalf("unexpected error: %v", err)
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

func assertEnvValue(t *testing.T, env []string, key, want string) {
	t.Helper()
	for _, pair := range env {
		if strings.HasPrefix(pair, key+"=") {
			if got := strings.TrimPrefix(pair, key+"="); got != want {
				t.Fatalf("expected %s=%q, got %q", key, want, got)
			}
			return
		}
	}
	t.Fatalf("expected %s in env, got %v", key, env)
}

func assertEnvMissing(t *testing.T, env []string, key string) {
	t.Helper()
	for _, pair := range env {
		if strings.HasPrefix(pair, key+"=") {
			t.Fatalf("expected %s to be omitted, got env %v", key, env)
		}
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
