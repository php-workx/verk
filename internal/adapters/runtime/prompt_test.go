package runtime

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseResultBlock_DirectJSON(t *testing.T) {
	text := `{"status":"done_with_concerns","completion_code":"ok","concerns":["minor style issue"]}`

	block, found := ParseResultBlock(text)
	if !found {
		t.Fatal("expected result block to be found")
	}
	if block.Status != "done_with_concerns" {
		t.Fatalf("expected done_with_concerns, got %q", block.Status)
	}
	if block.CompletionCode != "ok" {
		t.Fatalf("expected ok, got %q", block.CompletionCode)
	}
	if len(block.Concerns) != 1 || block.Concerns[0] != "minor style issue" {
		t.Fatalf("expected concerns, got %v", block.Concerns)
	}
}

func TestParseResultBlock_ValidatesAndNormalizesAllParsePaths(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantFound  bool
		wantStatus string
	}{
		{
			name:       "direct JSON normalizes synonym",
			input:      `{"status":"completed","completion_code":"ok"}`,
			wantFound:  true,
			wantStatus: "done",
		},
		{
			name:       "sentinel normalizes hyphenated status",
			input:      `VERK_RESULT:{"status":"done-with-concerns","completion_code":"ok"}`,
			wantFound:  true,
			wantStatus: "done_with_concerns",
		},
		{
			name:       "last JSON normalizes spaced status",
			input:      "Result follows:\n{\"status\":\"needs more context\",\"block_reason\":\"missing repo\"}",
			wantFound:  true,
			wantStatus: "needs_context",
		},
		{
			name:      "direct JSON rejects invalid status",
			input:     `{"status":"finished","completion_code":"ok"}`,
			wantFound: false,
		},
		{
			name:      "last JSON rejects invalid status",
			input:     "Result follows:\n{\"status\":\"finished\",\"completion_code\":\"ok\"}",
			wantFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block, found := ParseResultBlock(tt.input)
			if found != tt.wantFound {
				t.Fatalf("found=%v, want %v", found, tt.wantFound)
			}
			if found && block.Status != tt.wantStatus {
				t.Fatalf("status=%q, want %q", block.Status, tt.wantStatus)
			}
		})
	}
}

func TestParseResultBlock_SentinelLine(t *testing.T) {
	text := `Some extra prose the AI shouldn't have written.
VERK_RESULT:{"status":"done","completion_code":"ok"}`

	block, found := ParseResultBlock(text)
	if !found {
		t.Fatal("expected result block to be found via sentinel")
	}
	if block.Status != "done" {
		t.Fatalf("expected done, got %q", block.Status)
	}
}

func TestParseResultBlock_LastJSONFallback(t *testing.T) {
	text := `I made all the changes.
Here is my result:
{"status":"blocked","completion_code":"missing_dep","block_reason":"service down"}`

	block, found := ParseResultBlock(text)
	if !found {
		t.Fatal("expected result block via last-JSON fallback")
	}
	if block.Status != "blocked" {
		t.Fatalf("expected blocked, got %q", block.Status)
	}
	if block.BlockReason != "service down" {
		t.Fatalf("expected block reason, got %q", block.BlockReason)
	}
}

func TestParseResultBlock_NotFound(t *testing.T) {
	text := "I made all the changes. Everything looks good."
	_, found := ParseResultBlock(text)
	if found {
		t.Fatal("expected result block not to be found")
	}
}

func TestParseResultBlock_MalformedJSON(t *testing.T) {
	text := `{invalid json}`
	_, found := ParseResultBlock(text)
	if found {
		t.Fatal("expected malformed JSON to not parse")
	}
}

func TestParseReviewBlock_DirectJSON(t *testing.T) {
	text := `{
  "review_status": "findings",
  "summary": "needs fixes",
  "findings": [
    {
      "severity": "P2",
      "title": "missing error check",
      "body": "error from DoSomething is not checked",
      "file": "internal/handler.go",
      "line": 42,
      "disposition": "open"
    }
  ]
}`

	block, found := ParseReviewBlock(text)
	if !found {
		t.Fatal("expected review block to be found")
	}
	if block.ReviewStatus != "findings" {
		t.Fatalf("expected findings, got %q", block.ReviewStatus)
	}
	if block.Summary != "needs fixes" {
		t.Fatalf("expected summary, got %q", block.Summary)
	}
	if len(block.Findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(block.Findings))
	}
	if block.Findings[0].Severity != "P2" {
		t.Fatalf("expected P2, got %q", block.Findings[0].Severity)
	}
	if block.Findings[0].Line != 42 {
		t.Fatalf("expected line 42, got %d", block.Findings[0].Line)
	}
}

func TestParseReviewBlock_Passed(t *testing.T) {
	text := `{"review_status":"passed","summary":"all good","findings":[]}`

	block, found := ParseReviewBlock(text)
	if !found {
		t.Fatal("expected review block to be found")
	}
	if block.ReviewStatus != "passed" {
		t.Fatalf("expected passed, got %q", block.ReviewStatus)
	}
	if len(block.Findings) != 0 {
		t.Fatalf("expected no findings, got %d", len(block.Findings))
	}
}

func TestParseReviewBlock_ValidatesAndNormalizesAllParsePaths(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantFound  bool
		wantStatus string
	}{
		{
			name:       "direct JSON normalizes case",
			input:      `{"review_status":"PASSED","summary":"all good","findings":[]}`,
			wantFound:  true,
			wantStatus: "passed",
		},
		{
			name:       "sentinel accepts findings",
			input:      `VERK_REVIEW:{"review_status":"findings","summary":"needs work","findings":[]}`,
			wantFound:  true,
			wantStatus: "findings",
		},
		{
			name:       "last JSON normalizes spaces",
			input:      "Review follows:\n{\"review_status\":\" Passed \",\"summary\":\"ok\",\"findings\":[]}",
			wantFound:  true,
			wantStatus: "passed",
		},
		{
			name:      "direct JSON rejects invalid status",
			input:     `{"review_status":"clean","summary":"ok","findings":[]}`,
			wantFound: false,
		},
		{
			name:      "last JSON rejects invalid status",
			input:     "Review follows:\n{\"review_status\":\"clean\",\"summary\":\"ok\",\"findings\":[]}",
			wantFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block, found := ParseReviewBlock(tt.input)
			if found != tt.wantFound {
				t.Fatalf("found=%v, want %v", found, tt.wantFound)
			}
			if found && block.ReviewStatus != tt.wantStatus {
				t.Fatalf("review_status=%q, want %q", block.ReviewStatus, tt.wantStatus)
			}
		})
	}
}

func TestParseReviewBlock_SentinelLine(t *testing.T) {
	text := `Some preamble
VERK_REVIEW:{"review_status":"passed","summary":"clean","findings":[]}`

	block, found := ParseReviewBlock(text)
	if !found {
		t.Fatal("expected review block via sentinel")
	}
	if block.ReviewStatus != "passed" {
		t.Fatalf("expected passed, got %q", block.ReviewStatus)
	}
}

func TestParseResultBlock_SentinelEmptyDiscriminator(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"empty object", "VERK_RESULT:{}"},
		{"empty status", `VERK_RESULT:{"status":""}`},
		{"invalid status", `VERK_RESULT:{"status":"finished","completion_code":"ok"}`},
		{"prose with empty object", "Some prose\nVERK_RESULT:{}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, found := ParseResultBlock(tt.input)
			if found {
				t.Fatalf("expected sentinel with empty discriminator to be rejected, input=%q", tt.input)
			}
		})
	}
}

func TestParseReviewBlock_SentinelEmptyDiscriminator(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"empty object", "VERK_REVIEW:{}"},
		{"empty review_status", `VERK_REVIEW:{"review_status":""}`},
		{"invalid review_status", `VERK_REVIEW:{"review_status":"clean","summary":"ok","findings":[]}`},
		{"prose with empty object", "Some prose\nVERK_REVIEW:{}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, found := ParseReviewBlock(tt.input)
			if found {
				t.Fatalf("expected sentinel with empty discriminator to be rejected, input=%q", tt.input)
			}
		})
	}
}

func TestExtractCLIResultText_ValidJSON(t *testing.T) {
	output := CLIOutputJSON{
		Type:    "result",
		Subtype: "success",
		IsError: false,
		Result:  `{"status":"done","completion_code":"ok"}`,
	}
	data, _ := json.Marshal(output)

	text, ok := ExtractCLIResultText(data)
	if !ok {
		t.Fatal("expected ok=true for valid CLI JSON")
	}
	if text != `{"status":"done","completion_code":"ok"}` {
		t.Fatalf("expected JSON result, got %q", text)
	}
}

func TestExtractCLIResultText_ErrorJSON(t *testing.T) {
	output := CLIOutputJSON{
		Type:    "result",
		Subtype: "error",
		IsError: true,
		Result:  "error occurred",
	}
	data, _ := json.Marshal(output)

	text, ok := ExtractCLIResultText(data)
	if ok {
		t.Fatal("expected ok=false for error CLI JSON")
	}
	if text != "error occurred" {
		t.Fatalf("expected 'error occurred', got %q", text)
	}
}

func TestExtractCLIResultText_PlainText(t *testing.T) {
	text, ok := ExtractCLIResultText([]byte("just plain text output"))
	if ok {
		t.Fatal("expected ok=false for plain text")
	}
	if text != "just plain text output" {
		t.Fatalf("expected pass-through, got %q", text)
	}
}

func TestExtractCLIResultText_NonCLIJSON(t *testing.T) {
	// JSON that isn't Claude CLI format (no type="result") should pass through
	input := `{"status":"done","completion_code":"ok"}`
	text, ok := ExtractCLIResultText([]byte(input))
	if ok {
		t.Fatal("expected ok=false for non-CLI JSON")
	}
	if text != input {
		t.Fatalf("expected pass-through, got %q", text)
	}
}

func TestBuildWorkerPrompt_ContainsTicketInfo(t *testing.T) {
	prompt := BuildWorkerPrompt(WorkerRequest{
		TicketID:     "VER-001",
		LeaseID:      "lease-1",
		Attempt:      2,
		WorktreePath: "/workspace",
		Instructions: "Implement the feature",
	})
	if !strings.Contains(prompt, "VER-001") {
		t.Fatal("expected ticket id in prompt")
	}
	if !strings.Contains(prompt, "Attempt: 2") {
		t.Fatal("expected attempt number in prompt")
	}
	if !strings.Contains(prompt, "Implement the feature") {
		t.Fatal("expected instructions in prompt")
	}
	if !strings.Contains(prompt, "/workspace") {
		t.Fatal("expected worktree path in prompt")
	}
}

func TestBuildWorkerPrompt_AsksWorkerToInspectExistingImplementation(t *testing.T) {
	prompt := BuildWorkerPrompt(WorkerRequest{
		TicketID:     "VER-001",
		LeaseID:      "lease-1",
		WorktreePath: "/workspace",
		Instructions: "Implement the feature",
	})

	expected := "Before editing, inspect the current working tree to determine whether any required implementation already exists, then continue from the actual state."
	if !strings.Contains(prompt, expected) {
		t.Fatalf("expected existing-implementation instruction in prompt:\n%s", prompt)
	}
}

func TestBuildReviewPrompt_ContainsThreshold(t *testing.T) {
	prompt := BuildReviewPrompt(ReviewRequest{
		TicketID:                 "VER-002",
		LeaseID:                  "lease-2",
		EffectiveReviewThreshold: "P2",
		Instructions:             "Check the code quality",
	})
	if !strings.Contains(prompt, "VER-002") {
		t.Fatal("expected ticket id in prompt")
	}
	if !strings.Contains(prompt, "P2") {
		t.Fatal("expected review threshold in prompt")
	}
	if !strings.Contains(prompt, "Check the code quality") {
		t.Fatal("expected instructions in prompt")
	}
}

func TestWorkerSystemPrompt_JSONOnly(t *testing.T) {
	prompt := WorkerSystemPrompt()
	if !strings.Contains(prompt, "ONLY a JSON object") {
		t.Fatal("expected JSON-only instruction in system prompt")
	}
	if !strings.Contains(prompt, `"status"`) {
		t.Fatal("expected status field in schema example")
	}
	if strings.Contains(prompt, "<verk") {
		t.Fatal("expected no XML tags in system prompt")
	}
}

func TestWorkerSystemPrompt_InspectExistingImplementation(t *testing.T) {
	prompt := WorkerSystemPrompt()
	if !strings.Contains(prompt, "Inspect the existing working tree") {
		t.Fatalf("expected existing-implementation inspection guidance in worker system prompt:\n%s", prompt)
	}
	if !strings.Contains(prompt, "continue from the actual state") {
		t.Fatalf("expected continue-from-actual-state guidance in worker system prompt:\n%s", prompt)
	}
}

func TestWorkerSystemPrompt_ExternalReviewReadiness(t *testing.T) {
	prompt := WorkerSystemPrompt()
	if !strings.Contains(prompt, "brutally honest external review") {
		t.Fatalf("expected brutally honest external review goal in worker system prompt:\n%s", prompt)
	}
	if !strings.Contains(prompt, "robust") {
		t.Fatalf("expected robust-implementation language in worker system prompt:\n%s", prompt)
	}
}

func TestReviewerSystemPrompt_JSONOnly(t *testing.T) {
	prompt := ReviewerSystemPrompt()
	if !strings.Contains(prompt, "ONLY a JSON object") {
		t.Fatal("expected JSON-only instruction in system prompt")
	}
	if !strings.Contains(prompt, `"review_status"`) {
		t.Fatal("expected review_status field in schema example")
	}
	if strings.Contains(prompt, "<verk") {
		t.Fatal("expected no XML tags in system prompt")
	}
	if !strings.Contains(prompt, "READ the diff line by line") {
		t.Fatal("expected rigorous diff review instruction")
	}
	if !strings.Contains(prompt, "ticket description") {
		t.Fatal("expected instruction to read ticket description")
	}
}

func TestReviewerSystemPrompt_RigorousGapFinding(t *testing.T) {
	prompt := ReviewerSystemPrompt()
	if !strings.Contains(prompt, "brutally honest external reviewer") {
		t.Fatalf("expected brutally-honest framing in reviewer system prompt:\n%s", prompt)
	}
	if !strings.Contains(prompt, "real gaps, incomplete implementations, and missing tests") {
		t.Fatalf("expected rigorous gap-finding instruction in reviewer system prompt:\n%s", prompt)
	}
	if !strings.Contains(prompt, "not to manufacture nits") {
		t.Fatalf("expected anti-performative-findings guidance in reviewer system prompt:\n%s", prompt)
	}
}

func TestReviewerSystemPrompt_ActionableFindings(t *testing.T) {
	prompt := ReviewerSystemPrompt()
	for _, phrase := range []string{
		"owning ticket",
		"affected file or behavior",
		"severity",
		"missing validation",
		"auto-repaired",
	} {
		if !strings.Contains(prompt, phrase) {
			t.Fatalf("expected reviewer system prompt to request %q:\n%s", phrase, prompt)
		}
	}
}

func TestEpicReviewFraming_MatchesRequestedWording(t *testing.T) {
	expected := "Take a careful look at the task items we created, then conduct a rigorous review\n" +
		"of the current implementation. Find any gaps, incomplete implementations, and\n" +
		"missing tests so that we are confident that our implementation and fixes will\n" +
		"withstand a brutally honest external review."
	if EpicReviewFraming != expected {
		t.Fatalf("EpicReviewFraming does not match the requested wording.\n--- want ---\n%s\n--- got ---\n%s", expected, EpicReviewFraming)
	}
}

func TestBuildReviewPrompt_IncludesDiff(t *testing.T) {
	prompt := BuildReviewPrompt(ReviewRequest{
		TicketID:                 "VER-001",
		LeaseID:                  "lease-1",
		EffectiveReviewThreshold: "P2",
		Diff:                     "--- a/foo.go\n+++ b/foo.go\n@@ -1 +1 @@\n-old\n+new\n",
	})
	if !strings.Contains(prompt, "```diff") {
		t.Fatal("expected diff code block in prompt")
	}
	if !strings.Contains(prompt, "+new") {
		t.Fatal("expected diff content in prompt")
	}
}

func TestBuildReviewPrompt_NoDiffWhenEmpty(t *testing.T) {
	prompt := BuildReviewPrompt(ReviewRequest{
		TicketID:                 "VER-001",
		LeaseID:                  "lease-1",
		EffectiveReviewThreshold: "P2",
	})
	if strings.Contains(prompt, "```diff") {
		t.Fatal("expected no diff block when diff is empty")
	}
}
