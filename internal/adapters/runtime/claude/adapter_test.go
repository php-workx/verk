package claude

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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

func freezeNow(t *testing.T, values ...time.Time) {
	t.Helper()

	oldNow := now
	now = func() time.Time {
		if len(values) == 0 {
			t.Fatal("now called more times than expected")
		}
		value := values[0]
		values = values[1:]
		return value
	}
	t.Cleanup(func() { now = oldNow })
}

func TestRunWorker_NormalizesAndCapturesArtifacts(t *testing.T) {
	freezeNow(t,
		time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC),
		time.Date(2026, 4, 2, 12, 5, 0, 0, time.UTC),
	)

	rawStdout := []byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read"}]}}` + "\n")
	resultJSON := `{"status":"done_with_concerns","completion_code":"ok","concerns":["minor style issue in helper"]}`
	bridge := &fakeBridge{
		runFunc: func(ctx context.Context, req llmclibridge.Request) (llmclibridge.Result, error) {
			t.Helper()
			if req.RuntimeName != runtimeName {
				t.Fatalf("expected runtime %q, got %q", runtimeName, req.RuntimeName)
			}
			if req.Command != "claude-test" {
				t.Fatalf("expected command claude-test, got %q", req.Command)
			}
			if req.Model != "sonnet" {
				t.Fatalf("expected model sonnet, got %q", req.Model)
			}
			if req.Reasoning != "" {
				t.Fatalf("expected Claude reasoning to be empty, got %q", req.Reasoning)
			}
			if req.WorktreePath != "/tmp/worktree" {
				t.Fatalf("expected worktree path /tmp/worktree, got %q", req.WorktreePath)
			}
			if req.Timeout != 7*time.Minute {
				t.Fatalf("expected worker timeout 7m, got %s", req.Timeout)
			}
			if !strings.Contains(req.UserPrompt, "ticket-1") || !strings.Contains(req.UserPrompt, "Attempt: 2") {
				t.Fatalf("expected worker prompt to include ticket and attempt, got: %s", req.UserPrompt)
			}

			return llmclibridge.Result{
				Text:     resultJSON,
				Stdout:   rawStdout,
				Stderr:   []byte("worker log"),
				ExitCode: 0,
			}, nil
		},
	}
	installFakeBridge(t, bridge)

	adapter := NewWithCommand("claude-test")
	result, err := adapter.RunWorker(context.Background(), runtime.WorkerRequest{
		LeaseID:      "lease-1",
		RunID:        "run-1",
		TicketID:     "ticket-1",
		Attempt:      2,
		Model:        "sonnet",
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

	assertLegacyStdoutArtifact(t, result.StdoutPath, resultJSON, rawStdout)
	stderrBytes, err := os.ReadFile(result.StderrPath)
	if err != nil {
		t.Fatalf("read stderr artifact: %v", err)
	}
	if string(stderrBytes) != "worker log" {
		t.Fatalf("unexpected stderr capture: %s", string(stderrBytes))
	}

	var artifact workerArtifact
	readJSONArtifact(t, result.ResultArtifactPath, &artifact)
	if artifact.Normalized.LeaseID != "lease-1" {
		t.Fatalf("expected normalized lease id, got %q", artifact.Normalized.LeaseID)
	}
	if artifact.Normalized.Status != runtime.WorkerStatusDoneWithConcerns {
		t.Fatalf("unexpected normalized status: %q", artifact.Normalized.Status)
	}
	var cliOut runtime.CLIOutputJSON
	if err := json.Unmarshal(artifact.CLIOutput, &cliOut); err != nil {
		t.Fatalf("artifact cli output is not legacy CLI JSON: %v", err)
	}
	if cliOut.Result == "" || !strings.Contains(cliOut.Result, "done_with_concerns") {
		t.Fatalf("expected CLI output result to contain bridge text, got %#v", cliOut)
	}
}

func TestRunWorker_ProgressCallbackFlowsThroughBridge(t *testing.T) {
	freezeNow(t,
		time.Date(2026, 4, 2, 11, 0, 0, 0, time.UTC),
		time.Date(2026, 4, 2, 11, 1, 0, 0, time.UTC),
	)

	var progress []string
	bridge := &fakeBridge{
		runFunc: func(ctx context.Context, req llmclibridge.Request) (llmclibridge.Result, error) {
			t.Helper()
			if req.WorktreePath != "/tmp/worktree-streaming" {
				t.Fatalf("expected worktree path /tmp/worktree-streaming, got %q", req.WorktreePath)
			}
			if req.OnProgress == nil {
				t.Fatalf("expected progress callback")
			}
			req.OnProgress("reading internal/adapters/runtime/claude/adapter.go")
			return llmclibridge.Result{
				Text:     `{"status":"done","completion_code":"ok"}`,
				ExitCode: 0,
			}, nil
		},
	}
	installFakeBridge(t, bridge)

	result, err := NewWithCommand("claude-test").RunWorker(context.Background(), runtime.WorkerRequest{
		LeaseID:      "lease-stream-1",
		TicketID:     "ticket-stream-1",
		WorktreePath: "/tmp/worktree-streaming",
		OnProgress: func(detail string) {
			progress = append(progress, detail)
		},
	})
	if err != nil {
		t.Fatalf("RunWorker returned error: %v", err)
	}
	if result.Status != runtime.WorkerStatusDone {
		t.Fatalf("expected done, got %q", result.Status)
	}
	if len(progress) != 1 || progress[0] != "reading internal/adapters/runtime/claude/adapter.go" {
		t.Fatalf("expected progress from bridge, got %#v", progress)
	}
}

func TestRunWorker_BlockedStatus(t *testing.T) {
	freezeNow(t, time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC), time.Date(2026, 4, 2, 12, 0, 1, 0, time.UTC))
	installFakeBridge(t, &fakeBridge{
		runFunc: func(ctx context.Context, req llmclibridge.Request) (llmclibridge.Result, error) {
			if req.WorktreePath != "" {
				t.Fatalf("expected empty worktree path when unset, got %q", req.WorktreePath)
			}
			return llmclibridge.Result{
				Text:     `{"status":"blocked","completion_code":"missing_dependency","block_reason":"required service unavailable"}`,
				ExitCode: 0,
			}, nil
		},
	})

	result, err := NewWithCommand("claude-test").RunWorker(context.Background(), runtime.WorkerRequest{
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
	freezeNow(t, time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC), time.Date(2026, 4, 2, 12, 0, 1, 0, time.UTC))
	installFakeBridge(t, &fakeBridge{
		runFunc: func(context.Context, llmclibridge.Request) (llmclibridge.Result, error) {
			return llmclibridge.Result{
				Text:     "I made all the changes. Everything looks good.",
				ExitCode: 0,
			}, nil
		},
	})

	result, err := NewWithCommand("claude-test").RunWorker(context.Background(), runtime.WorkerRequest{
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

func TestRunReviewer_NormalizesFindingsAndDerivesStatus(t *testing.T) {
	freezeNow(t,
		time.Date(2026, 4, 2, 13, 0, 0, 0, time.UTC),
		time.Date(2026, 4, 2, 13, 3, 0, 0, time.UTC),
	)

	rawStdout := []byte(`{"type":"stream-json","event":"raw"}` + "\n")
	resultJSON := `{"review_status":"findings","summary":"needs fixes","findings":[{"severity":"P2","title":"blocking issue","body":"blocking issue","file":"internal/example.go","line":12,"disposition":"open"}]}`
	bridge := &fakeBridge{
		runFunc: func(ctx context.Context, req llmclibridge.Request) (llmclibridge.Result, error) {
			t.Helper()
			if req.Command != "claude-test" {
				t.Fatalf("expected command claude-test, got %q", req.Command)
			}
			if req.Reasoning != "" {
				t.Fatalf("expected Claude reasoning to be empty, got %q", req.Reasoning)
			}
			if req.Timeout != 9*time.Minute {
				t.Fatalf("expected reviewer timeout 9m, got %s", req.Timeout)
			}
			if !strings.Contains(req.UserPrompt, "src/app.go") {
				t.Fatalf("expected review prompt to include changed file, got: %s", req.UserPrompt)
			}
			return llmclibridge.Result{
				Text:   resultJSON,
				Stdout: rawStdout,
				Stderr: []byte("review log"),
			}, nil
		},
	}
	installFakeBridge(t, bridge)

	result, err := NewWithCommand("claude-test").RunReviewer(context.Background(), runtime.ReviewRequest{
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
	assertLegacyStdoutArtifact(t, result.StdoutPath, resultJSON, rawStdout)
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
	if result.Findings[0].Disposition != runtime.ReviewDispositionOpen {
		t.Fatalf("expected canonical open disposition, got %q", result.Findings[0].Disposition)
	}
	if result.Summary != "needs fixes" {
		t.Fatalf("expected summary 'needs fixes', got %q", result.Summary)
	}
}

func TestRunReviewer_PassedReview(t *testing.T) {
	freezeNow(t, time.Date(2026, 4, 2, 14, 0, 0, 0, time.UTC), time.Date(2026, 4, 2, 14, 0, 1, 0, time.UTC))
	installFakeBridge(t, &fakeBridge{
		runFunc: func(context.Context, llmclibridge.Request) (llmclibridge.Result, error) {
			return llmclibridge.Result{
				Text: `{"review_status":"passed","summary":"clean implementation, all criteria met","findings":[]}`,
			}, nil
		},
	})

	result, err := NewWithCommand("claude-test").RunReviewer(context.Background(), runtime.ReviewRequest{
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

func TestRunIntent_NormalizesBridgeText(t *testing.T) {
	rawStdout := []byte(`{"type":"stream-json","event":"raw"}` + "\n")
	resultJSON := `{"covered_criteria":["criterion 1"],"target_files":["internal/example.go"],"test_plan":"go test ./..."}`
	beforeArtifacts := intentStdoutArtifactSet(t)
	installFakeBridge(t, &fakeBridge{
		runFunc: func(ctx context.Context, req llmclibridge.Request) (llmclibridge.Result, error) {
			t.Helper()
			if req.Timeout != 0 {
				t.Fatalf("expected no intent timeout, got %s", req.Timeout)
			}
			if req.OnProgress != nil {
				t.Fatalf("expected no intent progress callback")
			}
			if !strings.Contains(req.UserPrompt, "ticket-intent") {
				t.Fatalf("expected intent prompt to include ticket id, got: %s", req.UserPrompt)
			}
			return llmclibridge.Result{
				Text:   resultJSON,
				Stdout: rawStdout,
			}, nil
		},
	})

	result, err := NewWithCommand("claude-test").RunIntent(context.Background(), runtime.IntentRequest{
		LeaseID:  "lease-intent",
		TicketID: "ticket-intent",
	})
	if err != nil {
		t.Fatalf("RunIntent returned error: %v", err)
	}
	if len(result.CoveredCriteria) != 1 || result.CoveredCriteria[0] != "criterion 1" {
		t.Fatalf("unexpected covered criteria: %#v", result.CoveredCriteria)
	}
	if len(result.TargetFiles) != 1 || result.TargetFiles[0] != "internal/example.go" {
		t.Fatalf("unexpected target files: %#v", result.TargetFiles)
	}
	if result.TestPlan != "go test ./..." {
		t.Fatalf("unexpected test plan: %q", result.TestPlan)
	}
	matches := newIntentStdoutArtifacts(t, beforeArtifacts)
	if len(matches) != 1 {
		t.Fatalf("expected one new intent stdout artifact, got %d: %v", len(matches), matches)
	}
	assertLegacyStdoutArtifact(t, matches[0], resultJSON, rawStdout)
}

func TestCheckAvailability_UsesBridge(t *testing.T) {
	probed := false
	installFakeBridge(t, &fakeBridge{
		availabilityFunc: func(ctx context.Context, runtimeName, command string) error {
			probed = true
			if runtimeName != "claude" {
				t.Fatalf("expected runtime claude, got %q", runtimeName)
			}
			if command != "claude-test" {
				t.Fatalf("expected command claude-test, got %q", command)
			}
			return nil
		},
	})

	if err := NewWithCommand("claude-test").CheckAvailability(context.Background()); err != nil {
		t.Fatalf("expected availability probe to pass, got %v", err)
	}
	if !probed {
		t.Fatalf("expected availability probe to run")
	}
}

func TestCheckAvailability_WrapsBridgeError(t *testing.T) {
	installFakeBridge(t, &fakeBridge{
		availabilityFunc: func(context.Context, string, string) error {
			return errors.New("not logged in")
		},
	})

	err := NewWithCommand("claude-test").CheckAvailability(context.Background())
	if err == nil {
		t.Fatalf("expected availability error")
	}
	if !strings.Contains(err.Error(), "claude availability check failed") || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("unexpected availability error: %v", err)
	}
}

func TestRunWorker_NeedsContextStatus(t *testing.T) {
	freezeNow(t, time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC), time.Date(2026, 4, 2, 12, 0, 1, 0, time.UTC))
	installFakeBridge(t, &fakeBridge{
		runFunc: func(context.Context, llmclibridge.Request) (llmclibridge.Result, error) {
			return llmclibridge.Result{
				Text:     `{"status":"needs_context","completion_code":"missing_spec","block_reason":"acceptance criteria unclear"}`,
				ExitCode: 0,
			}, nil
		},
	})

	result, err := NewWithCommand("claude-test").RunWorker(context.Background(), runtime.WorkerRequest{
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
	freezeNow(t, time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC), time.Date(2026, 4, 2, 12, 0, 1, 0, time.UTC))
	installFakeBridge(t, &fakeBridge{
		runFunc: func(context.Context, llmclibridge.Request) (llmclibridge.Result, error) {
			return llmclibridge.Result{
				Text:     `{"status":"needs-more-context","completion_code":"missing_spec","block_reason":"need operator input"}`,
				ExitCode: 0,
			}, nil
		},
	})

	result, err := NewWithCommand("claude-test").RunWorker(context.Background(), runtime.WorkerRequest{
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

func TestCommandResultFromBridgeUsesTextForParsingAndArtifacts(t *testing.T) {
	rawStdout := []byte(`{"type":"assistant","delta":"not legacy cli json"}` + "\n")
	result := commandResultFromBridge(llmclibridge.Result{
		Text:     `{"status":"done","completion_code":"ok"}`,
		Stdout:   rawStdout,
		Stderr:   []byte("stderr"),
		ExitCode: 0,
	})

	resultText, ok := runtime.ExtractCLIResultText(result.stdout)
	if !ok {
		t.Fatalf("expected bridge text to be wrapped in legacy CLI JSON")
	}
	if resultText != `{"status":"done","completion_code":"ok"}` {
		t.Fatalf("unexpected extracted result text: %q", resultText)
	}
	artifactText, artifactOK := runtime.ExtractCLIResultText(result.artifactStdout())
	if !artifactOK {
		t.Fatalf("expected artifact stdout to be wrapped in legacy CLI JSON, got %q", result.artifactStdout())
	}
	if artifactText != resultText {
		t.Fatalf("expected artifact stdout result %q, got %q", resultText, artifactText)
	}
	if string(result.artifactStdout()) == string(rawStdout) {
		t.Fatalf("expected artifact stdout to preserve legacy CLI envelope instead of raw bridge stdout")
	}
}

func assertLegacyStdoutArtifact(t *testing.T, path, expectedResult string, rawStdout []byte) {
	t.Helper()

	stdoutBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read stdout artifact: %v", err)
	}
	resultText, cliOK := runtime.ExtractCLIResultText(stdoutBytes)
	if !cliOK {
		t.Fatalf("expected stdout artifact to be parseable legacy CLI JSON, got %q", stdoutBytes)
	}
	if resultText != expectedResult {
		t.Fatalf("expected stdout artifact result %q, got %q", expectedResult, resultText)
	}
	if string(stdoutBytes) == string(rawStdout) {
		t.Fatalf("expected stdout artifact to preserve legacy CLI envelope instead of raw bridge stdout")
	}
}

func intentStdoutArtifactSet(t *testing.T) map[string]struct{} {
	t.Helper()

	matches, err := filepath.Glob(filepath.Join(os.TempDir(), "claude-intent-stdout-*.log"))
	if err != nil {
		t.Fatalf("glob intent stdout artifacts: %v", err)
	}
	artifacts := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		artifacts[match] = struct{}{}
	}
	return artifacts
}

func newIntentStdoutArtifacts(t *testing.T, before map[string]struct{}) []string {
	t.Helper()

	matches, err := filepath.Glob(filepath.Join(os.TempDir(), "claude-intent-stdout-*.log"))
	if err != nil {
		t.Fatalf("glob intent stdout artifacts: %v", err)
	}
	var newMatches []string
	for _, match := range matches {
		if _, ok := before[match]; !ok {
			newMatches = append(newMatches, match)
		}
	}
	return newMatches
}

func readJSONArtifact(t *testing.T, path string, target any) {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read JSON artifact %s: %v", path, err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatalf("artifact %s is not valid JSON: %v", path, err)
	}
}
