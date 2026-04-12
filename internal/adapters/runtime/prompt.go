package runtime

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Sentinel prefixes for fallback extraction when the AI mixes prose with JSON.
const (
	ResultSentinel = "VERK_RESULT:"
	ReviewSentinel = "VERK_REVIEW:"
)

// WorkerSystemPrompt returns the system prompt for a verk implementation worker.
func WorkerSystemPrompt() string {
	return `You are a verk implementation worker. Implement the changes described in the user prompt.

Rules:
- Read the ticket's acceptance criteria carefully before starting.
- Only modify files within the declared owned_paths scope.
- Run verification commands after making changes to confirm they pass.
- Do not commit changes; just make the edits.

When you are finished, your final message MUST be ONLY a JSON object — no prose, no markdown, no explanation. The JSON must conform to this schema:

{
  "status": "done | done_with_concerns | needs_context | blocked",
  "completion_code": "brief reason or ok",
  "concerns": ["optional list if status is done_with_concerns"],
  "block_reason": "explanation if status is needs_context or blocked"
}

Status values:
- "done": implementation complete, all criteria addressed
- "done_with_concerns": complete but with non-blocking concerns worth noting
- "needs_context": cannot proceed without additional information from the operator
- "blocked": cannot proceed due to a technical or environmental blocker`
}

// ReviewerSystemPrompt returns the system prompt for a verk code reviewer.
func ReviewerSystemPrompt() string {
	return `You are a verk code reviewer performing a rigorous, independent, fresh-context review.

Your review process — follow these steps in order:

1. READ the full ticket description carefully. Understand the problem being solved, the affected code, and the intended fix approach.
2. READ the acceptance criteria. These are necessary but not sufficient — the implementation must also be correct beyond what the criteria check.
3. READ the diff line by line. For every changed line, verify:
   - Does this change match what the ticket description asks for?
   - Is the logic correct? Are there off-by-one errors, nil pointer risks, missing edge cases?
   - Are there changes that don't belong (scope creep, unrelated modifications)?
   - Is the code consistent with the surrounding codebase style?
4. CHECK for omissions: did the implementation miss anything described in the ticket? Missing test coverage? Missing error handling?
5. VERIFY each acceptance criterion is actually satisfied by the diff — not just plausibly addressed.

Rules:
- You are a reviewer only — do not modify any files.
- Be rigorous. A weak review that misses real issues is worse than no review.
- Assess each finding with a severity level: P0 (critical/correctness), P1 (high/logic error), P2 (medium/missing case), P3 (low/style), P4 (trivial/nit).
- Only flag real issues. Do not manufacture findings to appear thorough.
- If the diff is correct and complete, say so — don't invent problems.

When you are finished, your final message MUST be ONLY a JSON object — no prose, no markdown, no explanation. The JSON must conform to this schema:

{
  "review_status": "passed | findings",
  "summary": "brief summary of the review",
  "findings": [
    {
      "severity": "P0 | P1 | P2 | P3 | P4",
      "title": "short title",
      "body": "detailed description of the issue",
      "file": "path/to/file.go",
      "line": 42,
      "disposition": "open"
    }
  ]
}

If no issues are found, set review_status to "passed" and findings to an empty array.`
}

// BuildWorkerPrompt constructs the user prompt for an implementation worker.
func BuildWorkerPrompt(req WorkerRequest) string {
	var b strings.Builder

	if req.TicketID != "" {
		fmt.Fprintf(&b, "Ticket: %s\n", req.TicketID)
	}
	if req.Attempt > 1 {
		fmt.Fprintf(&b, "Attempt: %d (previous attempt failed)\n", req.Attempt)
	}
	fmt.Fprintf(&b, "Lease ID: %s\n", req.LeaseID)

	if req.WorktreePath != "" {
		fmt.Fprintf(&b, "Working directory: %s\n", req.WorktreePath)
	}

	if req.Instructions != "" {
		b.WriteString("\n")
		b.WriteString(req.Instructions)
	}

	if req.InputArtifactPath != "" {
		fmt.Fprintf(&b, "\nPrior artifact: %s\n", req.InputArtifactPath)
	}

	return b.String()
}

// BuildReviewPrompt constructs the user prompt for a code reviewer.
func BuildReviewPrompt(req ReviewRequest) string {
	var b strings.Builder

	if req.TicketID != "" {
		fmt.Fprintf(&b, "Ticket: %s\n", req.TicketID)
	}
	fmt.Fprintf(&b, "Review threshold: %s (findings at or above this severity block closure)\n", req.EffectiveReviewThreshold)
	fmt.Fprintf(&b, "Lease ID: %s\n", req.LeaseID)

	if req.Instructions != "" {
		b.WriteString("\n")
		b.WriteString(req.Instructions)
	}

	if req.Standards != "" {
		b.WriteString("\n### Review Standards\n\n")
		b.WriteString(req.Standards)
		b.WriteString("\n")
	}

	if req.Diff != "" {
		b.WriteString("\n### Diff\n\n")
		b.WriteString("```diff\n")
		b.WriteString(req.Diff)
		if !strings.HasSuffix(req.Diff, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("```\n")
	}

	if req.InputArtifactPath != "" {
		fmt.Fprintf(&b, "\nImplementation artifact: %s\n", req.InputArtifactPath)
	}

	return b.String()
}

// VerkResultBlock is the JSON structure workers must return.
type VerkResultBlock struct {
	Status         string   `json:"status"`
	CompletionCode string   `json:"completion_code,omitempty"`
	Concerns       []string `json:"concerns,omitempty"`
	BlockReason    string   `json:"block_reason,omitempty"`
}

// VerkReviewBlock is the JSON structure reviewers must return.
type VerkReviewBlock struct {
	ReviewStatus string       `json:"review_status"`
	Summary      string       `json:"summary"`
	Findings     []RawFinding `json:"findings"`
}

// RawFinding matches the JSON shape expected from reviewers.
type RawFinding struct {
	ID              string  `json:"id,omitempty"`
	Severity        string  `json:"severity,omitempty"`
	Title           string  `json:"title,omitempty"`
	Body            string  `json:"body,omitempty"`
	File            string  `json:"file,omitempty"`
	Line            int     `json:"line,omitempty"`
	Disposition     string  `json:"disposition,omitempty"`
	WaivedBy        string  `json:"waived_by,omitempty"`
	WaivedAt        string  `json:"waived_at,omitempty"`
	WaiverReason    string  `json:"waiver_reason,omitempty"`
	WaiverExpiresAt *string `json:"waiver_expires_at,omitempty"`
}

// ParseResultBlock extracts a VerkResultBlock from AI output.
// Tries three strategies in order:
//  1. Direct JSON parse of the entire text
//  2. Scan for a VERK_RESULT:-prefixed line
//  3. Parse the last JSON object in the text
func ParseResultBlock(text string) (VerkResultBlock, bool) {
	text = strings.TrimSpace(text)

	// 1. Direct parse — the AI returned only JSON as instructed.
	var block VerkResultBlock
	if err := json.Unmarshal([]byte(text), &block); err == nil && block.Status != "" {
		return block, true
	}

	// 2. Sentinel-prefixed line fallback.
	if b, ok := parseSentinelLine[VerkResultBlock](text, ResultSentinel); ok {
		return b, true
	}

	// 3. Last JSON object fallback.
	if b, ok := parseLastJSON[VerkResultBlock](text); ok && b.Status != "" {
		return b, true
	}

	return VerkResultBlock{}, false
}

// ParseReviewBlock extracts a VerkReviewBlock from AI output.
// Same three-strategy approach as ParseResultBlock.
func ParseReviewBlock(text string) (VerkReviewBlock, bool) {
	text = strings.TrimSpace(text)

	var block VerkReviewBlock
	if err := json.Unmarshal([]byte(text), &block); err == nil && block.ReviewStatus != "" {
		return block, true
	}

	if b, ok := parseSentinelLine[VerkReviewBlock](text, ReviewSentinel); ok {
		return b, true
	}

	if b, ok := parseLastJSON[VerkReviewBlock](text); ok && b.ReviewStatus != "" {
		return b, true
	}

	return VerkReviewBlock{}, false
}

// parseSentinelLine scans for a line starting with the given prefix and parses
// the remainder as JSON.
func parseSentinelLine[T any](text, prefix string) (T, bool) {
	var zero T
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			payload := strings.TrimSpace(strings.TrimPrefix(line, prefix))
			var result T
			if err := json.Unmarshal([]byte(payload), &result); err == nil {
				return result, true
			}
		}
	}
	return zero, false
}

// parseLastJSON finds the last { ... } block in the text and tries to parse it.
func parseLastJSON[T any](text string) (T, bool) {
	var zero T
	lastBrace := strings.LastIndex(text, "{")
	if lastBrace < 0 {
		return zero, false
	}
	// Find the matching closing brace by trying progressively larger substrings.
	for i := len(text); i > lastBrace; i-- {
		candidate := text[lastBrace:i]
		if !strings.Contains(candidate, "}") {
			continue
		}
		var result T
		if err := json.Unmarshal([]byte(candidate), &result); err == nil {
			return result, true
		}
	}
	return zero, false
}

// CLIOutputJSON is the JSON structure returned by `claude -p --output-format json`.
type CLIOutputJSON struct {
	Type          string  `json:"type"`
	Subtype       string  `json:"subtype"`
	CostUSD       float64 `json:"cost_usd"`
	DurationMS    int64   `json:"duration_ms"`
	DurationAPIMS int64   `json:"duration_api_ms"`
	IsError       bool    `json:"is_error"`
	NumTurns      int     `json:"num_turns"`
	Result        string  `json:"result"`
	SessionID     string  `json:"session_id"`
}

// ExtractCLIResultText parses the JSON output from `claude -p --output-format json`
// and returns the result text. If the output is not Claude CLI JSON (identified by
// type="result"), it returns the raw text as-is (for plain text output or other
// JSON formats like Codex).
func ExtractCLIResultText(stdout []byte) (string, bool) {
	var output CLIOutputJSON
	if err := json.Unmarshal(stdout, &output); err != nil {
		return string(stdout), false
	}
	// Discriminator: Claude CLI output always has type="result".
	if output.Type != "result" {
		return string(stdout), false
	}
	return output.Result, !output.IsError
}
