package cli

import (
	"bytes"
	"strings"
	"testing"
	"verk/internal/engine"
	"verk/internal/state"
)

func TestShortenBlockReason_ClaimRenewalSentinel(t *testing.T) {
	if got := shortenBlockReason("claim renewal failed: lease expired for ticket ver-123"); got != "lease expired" {
		t.Fatalf("expected claim renewal sentinel to shorten to lease expired, got %q", got)
	}
}

func TestShortenBlockReason_ClaimRenewalTextInsideReasonIsNotSentinel(t *testing.T) {
	reason := "context: claim renewal failed: transient"

	if got := shortenBlockReason(reason); got != reason {
		t.Fatalf("expected non-prefix claim renewal text to stay unchanged, got %q", got)
	}
}

func TestShortenBlockReason_TruncationStillApplies(t *testing.T) {
	reason := strings.Repeat("x", 80)
	got := shortenBlockReason(reason)

	if len(got) != 72 {
		t.Fatalf("expected shortened reason length 72, got %d", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("expected shortened reason to end with ellipsis, got %q", got)
	}
}

func TestShortenBlockReason_RuntimeEventStream(t *testing.T) {
	reason := "worker blocked by operator input: {\"type\":\"thread.started\"}\n{\"type\":\"turn.started\"}"

	got := shortenBlockReason(reason)
	want := "worker blocked by operator input from runtime event stream"
	if got != want {
		t.Fatalf("expected runtime event stream to be summarized as %q, got %q", want, got)
	}
}

func TestRenderStatusFailure_ShowsDetailWithoutLogPaths(t *testing.T) {
	var buf bytes.Buffer
	renderStatusFailure(&buf, doctorRenderer{}, "", engine.StatusFailure{
		Summary:    "verification: just lint-check failed with exit code 1",
		Detail:     "internal/policy/config.go:176:23: unnecessary conversion (unconvert)",
		StdoutPath: "/tmp/command.stdout.log",
		StderrPath: "/tmp/command.stderr.log",
	})

	out := buf.String()
	if !strings.Contains(out, "verification: just lint-check failed with exit code 1") {
		t.Fatalf("expected summary in output, got %q", out)
	}
	if !strings.Contains(out, "internal/policy/config.go:176:23: unnecessary conversion (unconvert)") {
		t.Fatalf("expected concise detail in output, got %q", out)
	}
	if strings.Contains(out, "stdout:") || strings.Contains(out, "stderr:") || strings.Contains(out, "/tmp/command.") {
		t.Fatalf("default status output should hide log paths, got %q", out)
	}
}

func TestRenderStatusStep_ShowsTimingAndUsage(t *testing.T) {
	var buf bytes.Buffer
	renderStatusStep(&buf, doctorRenderer{}, "", engine.StatusStep{
		Name:       "worker",
		DurationMS: 356_000,
		Runtime:    "codex",
		Model:      "gpt-5.3-codex-spark",
		Reasoning:  "high",
		TokenUsage: &state.RuntimeTokenUsage{
			InputTokens:       9_113_196,
			CachedInputTokens: 8_728_576,
			OutputTokens:      27_066,
		},
		ActivityStats: &state.RuntimeActivityStats{
			CommandCount:      157,
			AgentMessageCount: 32,
		},
	})

	out := buf.String()
	for _, want := range []string{"worker", "5m56s", "codex/gpt-5.3-codex-spark/high", "157 agent command(s)", "9.1M in", "8.7M cached", "27.1k out"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in output, got %q", want, out)
		}
	}
}
