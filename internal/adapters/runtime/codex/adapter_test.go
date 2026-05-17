package codex

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
	"verk/internal/adapters/runtime"
	"verk/internal/adapters/runtime/llmclibridge"
)

type fakeBridge struct {
	runFunc          func(context.Context, llmclibridge.Request) (llmclibridge.Result, error)
	availabilityFunc func(context.Context, string, string) error
	requests         []llmclibridge.Request
}

func (b *fakeBridge) Run(ctx context.Context, req llmclibridge.Request) (llmclibridge.Result, error) {
	b.requests = append(b.requests, req)
	if b.runFunc != nil {
		return b.runFunc(ctx, req)
	}
	return llmclibridge.Result{}, nil
}

func (b *fakeBridge) CheckAvailability(ctx context.Context, runtimeName, command string) error {
	if b.availabilityFunc != nil {
		return b.availabilityFunc(ctx, runtimeName, command)
	}
	return nil
}

func installFakeBridge(t *testing.T, bridge *fakeBridge) {
	t.Helper()

	oldNewBridge := newBridge
	newBridge = func() bridgeClient { return bridge }
	t.Cleanup(func() { newBridge = oldNewBridge })
}

func TestRunWorker_NormalizesAndCapturesArtifacts(t *testing.T) {
	oldNow := now
	defer func() {
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
	bridge := &fakeBridge{}
	installFakeBridge(t, bridge)
	bridge.runFunc = func(ctx context.Context, req llmclibridge.Request) (llmclibridge.Result, error) {
		t.Helper()
		if req.Command != "codex-test" {
			t.Fatalf("expected command codex-test, got %q", req.Command)
		}
		if req.RuntimeName != runtimeName {
			t.Fatalf("expected runtime codex, got %q", req.RuntimeName)
		}
		if req.SystemPrompt != runtime.WorkerSystemPrompt() {
			t.Fatalf("expected worker system prompt")
		}
		if !strings.Contains(req.UserPrompt, "ticket-1") {
			t.Fatalf("expected prompt to contain ticket id, got user prompt: %s", req.UserPrompt)
		}
		if req.Model != "gpt-5-mini" {
			t.Fatalf("expected model gpt-5-mini, got %q", req.Model)
		}
		if req.Reasoning != "medium" {
			t.Fatalf("expected reasoning medium, got %q", req.Reasoning)
		}
		if req.WorktreePath != "/tmp/worktree" {
			t.Fatalf("expected worktree path /tmp/worktree, got %q", req.WorktreePath)
		}
		if req.Timeout != 7*time.Minute {
			t.Fatalf("expected worker timeout 7m, got %s", req.Timeout)
		}

		// Codex streams JSONL events and may include the structured result as
		// a sentinel line before the final usage event.
		outputJSON := strings.Join([]string{
			`{"type":"thread.started","thread_id":"thread-1"}`,
			`{"type":"item.completed","item":{"type":"command_execution"}}`,
			`{"type":"item.completed","item":{"type":"agent_message","text":"done"}}`,
			`VERK_RESULT: {"status":"done_with_concerns","completion_code":"ok","concerns":["minor style issue"]}`,
			`{"type":"turn.completed","usage":{"input_tokens":1200,"cached_input_tokens":900,"output_tokens":80}}`,
		}, "\n")

		return llmclibridge.Result{
			Stdout:   []byte(outputJSON),
			Stderr:   []byte("worker log"),
			ExitCode: 0,
		}, nil
	}

	adapter := NewWithCommand("codex-test")
	result, err := adapter.RunWorker(context.Background(), runtime.WorkerRequest{
		LeaseID:      "lease-1",
		RunID:        "run-1",
		TicketID:     "ticket-1",
		Attempt:      2,
		WorktreePath: "/tmp/worktree",
		Model:        "gpt-5-mini",
		Reasoning:    "medium",
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
	if result.TokenUsage == nil || result.TokenUsage.InputTokens != 1200 || result.TokenUsage.CachedInputTokens != 900 || result.TokenUsage.OutputTokens != 80 {
		t.Fatalf("expected token usage from Codex stream, got %#v", result.TokenUsage)
	}
	if result.ActivityStats == nil || result.ActivityStats.EventCount != 4 || result.ActivityStats.CommandCount != 1 || result.ActivityStats.AgentMessageCount != 1 {
		t.Fatalf("expected activity stats from Codex stream, got %#v", result.ActivityStats)
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
	oldNow := now
	defer func() {
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
	bridge := &fakeBridge{}
	installFakeBridge(t, bridge)
	bridge.runFunc = func(ctx context.Context, req llmclibridge.Request) (llmclibridge.Result, error) {
		t.Helper()
		if req.Command != "codex-test" {
			t.Fatalf("expected command codex-test, got %q", req.Command)
		}
		if req.SystemPrompt != runtime.ReviewerSystemPrompt() {
			t.Fatalf("expected reviewer system prompt")
		}
		if !strings.Contains(req.UserPrompt, "ticket-2") {
			t.Fatalf("expected prompt to contain ticket id, got user prompt: %s", req.UserPrompt)
		}
		if req.Timeout != 9*time.Minute {
			t.Fatalf("expected reviewer timeout 9m, got %s", req.Timeout)
		}

		outputJSON := strings.Join([]string{
			`{"type":"thread.started","thread_id":"thread-review"}`,
			`{"type":"item.completed","item":{"type":"command_execution"}}`,
			`VERK_REVIEW: {"review_status":"findings","summary":"needs fixes","findings":[{"severity":"P2","title":"blocking issue","body":"blocking issue","file":"internal/example.go","line":12,"disposition":"open"}]}`,
			`{"type":"turn.completed","usage":{"input_tokens":2200,"cached_input_tokens":1100,"output_tokens":140}}`,
		}, "\n")

		return llmclibridge.Result{
			Stdout: []byte(outputJSON),
			Stderr: []byte("review log"),
		}, nil
	}

	adapter := NewWithCommand("codex-test")
	result, err := adapter.RunReviewer(context.Background(), runtime.ReviewRequest{
		LeaseID:                  "lease-2",
		RunID:                    "run-2",
		TicketID:                 "ticket-2",
		ChangedFiles:             []string{"src/app.go"},
		EffectiveReviewThreshold: runtime.SeverityP2,
		ExecutionConfig: runtime.ExecutionConfig{
			ReviewerTimeoutMinutes: 9,
			AuthEnvVars:            []string{"VERK_API_KEY"},
		},
	})
	if err != nil {
		t.Fatalf("RunReviewer returned error: %v", err)
	}

	if result.ResultArtifactPath == "" {
		t.Fatalf("expected result artifact path to be set")
	}
	artifactBytes, err := os.ReadFile(result.ResultArtifactPath)
	if err != nil {
		t.Fatalf("read review result artifact: %v", err)
	}
	artifactStr := string(artifactBytes)
	if !strings.Contains(artifactStr, `"changed_files"`) || !strings.Contains(artifactStr, `"src/app.go"`) {
		t.Fatalf("expected changed_files in review artifact:\n%s", artifactBytes)
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
	if result.TokenUsage == nil || result.TokenUsage.InputTokens != 2200 || result.TokenUsage.CachedInputTokens != 1100 || result.TokenUsage.OutputTokens != 140 {
		t.Fatalf("expected token usage from Codex review stream, got %#v", result.TokenUsage)
	}
	if result.ActivityStats == nil || result.ActivityStats.EventCount != 3 || result.ActivityStats.CommandCount != 1 {
		t.Fatalf("expected activity stats from Codex review stream, got %#v", result.ActivityStats)
	}
	if result.Findings[0].Disposition != runtime.ReviewDispositionOpen {
		t.Fatalf("expected canonical open disposition, got %q", result.Findings[0].Disposition)
	}
	if result.Summary != "needs fixes" {
		t.Fatalf("expected summary 'needs fixes', got %q", result.Summary)
	}
}

func TestRunReviewer_UsesCodexCdFlagForWorktreePath(t *testing.T) {
	oldNow := now
	defer func() {
		now = oldNow
	}()

	now = func() time.Time {
		return time.Date(2026, 4, 2, 13, 0, 0, 0, time.UTC)
	}

	bridge := &fakeBridge{}
	installFakeBridge(t, bridge)
	bridge.runFunc = func(ctx context.Context, req llmclibridge.Request) (llmclibridge.Result, error) {
		t.Helper()
		if req.WorktreePath != "/tmp/review-worktree" {
			t.Fatalf("expected worktree path /tmp/review-worktree, got %q", req.WorktreePath)
		}

		outputJSON := `{"review_status":"passed","summary":"clean implementation","findings":[]}`
		return llmclibridge.Result{Text: outputJSON}, nil
	}

	adapter := NewWithCommand("codex-test")
	if _, err := adapter.RunReviewer(context.Background(), runtime.ReviewRequest{
		LeaseID:                  "lease-worktree-review",
		TicketID:                 "ticket-worktree-review",
		WorktreePath:             "/tmp/review-worktree",
		EffectiveReviewThreshold: runtime.SeverityP2,
		ExecutionConfig: runtime.ExecutionConfig{
			ReviewerTimeoutMinutes: 7,
			AuthEnvVars:            []string{"VERK_API_KEY"},
		},
	}); err != nil {
		t.Fatalf("RunReviewer returned error: %v", err)
	}
}

func TestRunReviewer_PassedReview(t *testing.T) {
	oldNow := now
	defer func() {
		now = oldNow
	}()

	now = func() time.Time {
		return time.Date(2026, 4, 2, 14, 0, 0, 0, time.UTC)
	}

	bridge := &fakeBridge{}
	installFakeBridge(t, bridge)
	bridge.runFunc = func(ctx context.Context, req llmclibridge.Request) (llmclibridge.Result, error) {
		outputJSON := `{"review_status":"passed","summary":"clean implementation","findings":[]}`
		return llmclibridge.Result{Text: outputJSON}, nil
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
	probed := false
	installFakeBridge(t, &fakeBridge{
		availabilityFunc: func(ctx context.Context, gotRuntime, command string) error {
			probed = true
			if gotRuntime != runtimeName {
				t.Fatalf("expected runtime codex, got %q", gotRuntime)
			}
			if command != "codex-test" {
				t.Fatalf("expected command codex-test, got %q", command)
			}
			return nil
		},
	})

	if err := NewWithCommand("codex-test").CheckAvailability(context.Background()); err != nil {
		t.Fatalf("expected availability probe to pass, got %v", err)
	}
	if !probed {
		t.Fatalf("expected availability probe to run")
	}
}

func TestRunWorker_NeedsContextStatus(t *testing.T) {
	oldNow := now
	defer func() {
		now = oldNow
	}()

	now = func() time.Time {
		return time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC)
	}

	installFakeBridge(t, &fakeBridge{
		runFunc: func(ctx context.Context, req llmclibridge.Request) (llmclibridge.Result, error) {
			resultJSON := `{"status":"needs_context","completion_code":"missing_spec","block_reason":"acceptance criteria unclear"}`
			return llmclibridge.Result{
				Text:     resultJSON,
				ExitCode: 0,
			}, nil
		},
	})

	adapter := NewWithCommand("codex-test")
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

func TestRunWorker_NeedsMoreContextHyphenated(t *testing.T) {
	oldNow := now
	defer func() {
		now = oldNow
	}()

	now = func() time.Time {
		return time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC)
	}

	// Simulate a runtime returning the hyphenated form "needs-more-context"
	installFakeBridge(t, &fakeBridge{
		runFunc: func(ctx context.Context, req llmclibridge.Request) (llmclibridge.Result, error) {
			resultJSON := `{"status":"needs-more-context","completion_code":"missing_spec","block_reason":"need operator input"}`
			return llmclibridge.Result{
				Text:     resultJSON,
				ExitCode: 0,
			}, nil
		},
	})

	adapter := NewWithCommand("codex-test")
	result, err := adapter.RunWorker(context.Background(), runtime.WorkerRequest{
		LeaseID:  "lease-1",
		TicketID: "ticket-1",
	})
	if err != nil {
		t.Fatalf("RunWorker returned error: %v", err)
	}
	if result.Status != runtime.WorkerStatusNeedsContext {
		t.Fatalf("expected needs_context from hyphenated input, got %q", result.Status)
	}
	if result.RetryClass != runtime.RetryClassBlockedByOperatorInput {
		t.Fatalf("expected blocked_by_operator_input, got %q", result.RetryClass)
	}
}

func TestRunWorker_UsageLimitIsRetryableAndPreservesMessage(t *testing.T) {
	oldNow := now
	defer func() {
		now = oldNow
	}()

	now = func() time.Time {
		return time.Date(2026, 4, 23, 16, 2, 1, 0, time.UTC)
	}

	installFakeBridge(t, &fakeBridge{
		runFunc: func(ctx context.Context, req llmclibridge.Request) (llmclibridge.Result, error) {
			outputJSON := strings.Join([]string{
				`{"type":"thread.started","thread_id":"thread-1"}`,
				`{"type":"turn.started"}`,
				`{"type":"error","message":"You've hit your usage limit for GPT-5.3-Codex-Spark. Switch to another model now, or try again at Apr 27th, 2026 4:32 PM."}`,
				`{"type":"turn.failed","error":{"message":"You've hit your usage limit for GPT-5.3-Codex-Spark. Switch to another model now, or try again at Apr 27th, 2026 4:32 PM."}}`,
			}, "\n")
			return llmclibridge.Result{
				Stdout:   []byte(outputJSON),
				ExitCode: 1,
			}, nil
		},
	})

	adapter := NewWithCommand("codex-test")
	result, err := adapter.RunWorker(context.Background(), runtime.WorkerRequest{
		LeaseID:      "lease-usage-limit",
		TicketID:     "ticket-usage-limit",
		WorktreePath: "/tmp/worktree",
	})
	if err != nil {
		t.Fatalf("RunWorker returned error: %v", err)
	}
	if result.Status != runtime.WorkerStatusBlocked {
		t.Fatalf("expected blocked status for usage limit, got %q", result.Status)
	}
	if result.RetryClass != runtime.RetryClassRetryable {
		t.Fatalf("expected retryable retry class for usage limit, got %q", result.RetryClass)
	}
	if !strings.Contains(result.BlockReason, "usage limit") {
		t.Fatalf("expected block reason to contain usage limit message, got %q", result.BlockReason)
	}
	if strings.Contains(result.BlockReason, `{"type":"thread.started"}`) {
		t.Fatalf("expected block reason to surface the Codex error message, got raw event stream %q", result.BlockReason)
	}
}

func TestRunWorker_FallbackWhenNoJSON(t *testing.T) {
	oldNow := now
	defer func() {
		now = oldNow
	}()

	now = func() time.Time {
		return time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC)
	}

	installFakeBridge(t, &fakeBridge{
		runFunc: func(ctx context.Context, req llmclibridge.Request) (llmclibridge.Result, error) {
			return llmclibridge.Result{
				Text:     "I made all the changes. Everything looks good.",
				ExitCode: 0,
			}, nil
		},
	})

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
	oldNow := now
	defer func() {
		now = oldNow
	}()

	now = func() time.Time {
		return time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC)
	}

	installFakeBridge(t, &fakeBridge{
		runFunc: func(ctx context.Context, req llmclibridge.Request) (llmclibridge.Result, error) {
			// AI mixed prose with a sentinel-prefixed JSON line
			output := "Done with changes.\nVERK_RESULT:{\"status\":\"done\",\"completion_code\":\"ok\"}"
			return llmclibridge.Result{
				Text:     output,
				ExitCode: 0,
			}, nil
		},
	})

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

func TestCommandResultFromBridgePreservesRawStdoutAndParsesBridgeText(t *testing.T) {
	rawStdout := strings.Join([]string{
		`{"type":"item.completed","item":{"type":"command_execution"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":12,"output_tokens":3}}`,
	}, "\n")
	result := commandResultFromBridge(llmclibridge.Result{
		Stdout: []byte(rawStdout),
		Text:   `{"status":"done","completion_code":"ok"}`,
	})

	if string(result.stdout) != rawStdout {
		t.Fatalf("expected raw stdout to be preserved, got %q", string(result.stdout))
	}
	if string(result.artifactStdout()) != rawStdout {
		t.Fatalf("expected artifact stdout to prefer raw stdout, got %q", string(result.artifactStdout()))
	}
	if got := result.parseText(); got != `{"status":"done","completion_code":"ok"}` {
		t.Fatalf("expected bridge text for parsing, got %q", got)
	}
	usage, activity := extractCodexTelemetry(result.stdout)
	if usage == nil || usage.InputTokens != 12 || usage.OutputTokens != 3 || usage.TotalTokens != 15 {
		t.Fatalf("expected telemetry from raw stdout, got %#v", usage)
	}
	if activity == nil || activity.EventCount != 2 || activity.CommandCount != 1 {
		t.Fatalf("expected activity from raw stdout, got %#v", activity)
	}
}

func TestExtractCodexTelemetryHandlesLargeJSONLLine(t *testing.T) {
	stdout := []byte(strings.Repeat("x", 1024*1024+1) + "\n" +
		`{"type":"turn.completed","usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}` + "\n")

	usage, activity := extractCodexTelemetry(stdout)

	if usage == nil || usage.InputTokens != 2 || usage.OutputTokens != 3 || usage.TotalTokens != 5 {
		t.Fatalf("expected telemetry after large JSONL line, got %#v", usage)
	}
	if activity == nil || activity.EventCount != 1 {
		t.Fatalf("expected activity after large JSONL line, got %#v", activity)
	}
}

func TestExtractCodexFailureMessageHandlesLargeJSONLLine(t *testing.T) {
	stdout := []byte(strings.Repeat("x", 1024*1024+1) + "\n" +
		`{"type":"turn.failed","error":{"message":"structured failure"}}` + "\n")

	message := extractCodexFailureMessage(stdout, []byte("generic stderr"))

	if message != "structured failure" {
		t.Fatalf("expected structured failure after large JSONL line, got %q", message)
	}
}
