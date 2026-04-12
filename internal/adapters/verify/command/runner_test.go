package command

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"verk/internal/policy"
)

func TestRunCommands_CapturesExitCodeAndArtifacts(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoRoot, "marker.txt"), []byte("present"), 0o644); err != nil {
		t.Fatalf("write marker file: %v", err)
	}

	results, err := RunCommands(context.Background(), repoRoot, []string{
		"test -f marker.txt && printf 'hello' && printf 'err' >&2",
	}, policy.VerificationConfig{
		DefaultTimeoutMinutes: 1,
		EnvPassthrough:        []string{"PATH"},
	})
	if err != nil {
		t.Fatalf("RunCommands returned error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 command result, got %d", len(results))
	}

	result := results[0]
	if result.Command != "test -f marker.txt && printf 'hello' && printf 'err' >&2" {
		t.Fatalf("unexpected command recorded: %q", result.Command)
	}
	if result.Cwd != repoRoot {
		t.Fatalf("expected cwd %q, got %q", repoRoot, result.Cwd)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", result.ExitCode)
	}
	if result.TimedOut {
		t.Fatalf("expected command not to time out")
	}
	if result.DurationMS < 0 {
		t.Fatalf("expected non-negative duration, got %d", result.DurationMS)
	}
	if result.StartedAt.IsZero() || result.FinishedAt.IsZero() {
		t.Fatalf("expected timestamps to be recorded: started_at=%v finished_at=%v", result.StartedAt, result.FinishedAt)
	}
	if result.FinishedAt.Before(result.StartedAt) {
		t.Fatalf("expected finished_at to be on or after started_at")
	}
	if result.StdoutPath == "" || result.StderrPath == "" {
		t.Fatalf("expected artifact paths to be populated, got stdout=%q stderr=%q", result.StdoutPath, result.StderrPath)
	}

	stdoutData, err := os.ReadFile(result.StdoutPath)
	if err != nil {
		t.Fatalf("read stdout artifact: %v", err)
	}
	if string(stdoutData) != "hello" {
		t.Fatalf("unexpected stdout artifact content: %q", string(stdoutData))
	}

	stderrData, err := os.ReadFile(result.StderrPath)
	if err != nil {
		t.Fatalf("read stderr artifact: %v", err)
	}
	if string(stderrData) != "err" {
		t.Fatalf("unexpected stderr artifact content: %q", string(stderrData))
	}

	if !DeriveVerificationPassed(results) {
		t.Fatalf("expected successful command results to derive a passing verification")
	}
}

func TestRunCommands_TimeoutMarksTimedOut(t *testing.T) {
	repoRoot := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	results, err := RunCommands(ctx, repoRoot, []string{
		"/bin/sleep 1",
	}, policy.VerificationConfig{
		DefaultTimeoutMinutes: 1,
	})
	if err != nil {
		t.Fatalf("RunCommands returned error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 command result, got %d", len(results))
	}

	result := results[0]
	if !result.TimedOut {
		t.Fatalf("expected command to time out")
	}
	if result.ExitCode == 0 {
		t.Fatalf("expected timeout to produce a non-zero exit code")
	}
	if DeriveVerificationPassed(results) {
		t.Fatalf("expected timed out command to fail derived verification")
	}
}

func TestRunCommands_UsesAllowlistedEnvOnly(t *testing.T) {
	repoRoot := t.TempDir()
	t.Setenv("KEEP_ME", "allowed")
	t.Setenv("DROP_ME", "secret")

	results, err := RunCommands(context.Background(), repoRoot, []string{
		`printf '%s' "${KEEP_ME:-missing}:${DROP_ME:-missing}"`,
	}, policy.VerificationConfig{
		DefaultTimeoutMinutes: 1,
		EnvPassthrough:        []string{"KEEP_ME"},
	})
	if err != nil {
		t.Fatalf("RunCommands returned error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 command result, got %d", len(results))
	}

	stdoutData, err := os.ReadFile(results[0].StdoutPath)
	if err != nil {
		t.Fatalf("read stdout artifact: %v", err)
	}
	if got := strings.TrimSpace(string(stdoutData)); got != "allowed:missing" {
		t.Fatalf("expected only allowlisted env to be present, got %q", got)
	}
}

func TestRunCommands_StartsWithCleanEnv(t *testing.T) {
	repoRoot := t.TempDir()
	t.Setenv("DROP_ME", "secret")

	results, err := RunCommands(context.Background(), repoRoot, []string{
		`printf '%s' "${DROP_ME:-missing}"`,
	}, policy.VerificationConfig{
		DefaultTimeoutMinutes: 1,
	})
	if err != nil {
		t.Fatalf("RunCommands returned error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 command result, got %d", len(results))
	}

	stdoutData, err := os.ReadFile(results[0].StdoutPath)
	if err != nil {
		t.Fatalf("read stdout artifact: %v", err)
	}
	if got := strings.TrimSpace(string(stdoutData)); got != "missing" {
		t.Fatalf("expected clean environment to omit inherited variables, got %q", got)
	}
}

func TestRunCommands_DefaultEnvIncludesPath(t *testing.T) {
	// When no EnvPassthrough is configured, verification commands must still
	// be able to find executables via PATH. This tests the fix for ver-93kv:
	// nil env must NOT become []string{} (which would strip all env vars
	// including PATH), and verificationEnv must always include the default
	// allowlist (PATH, HOME, etc.).
	repoRoot := t.TempDir()

	results, err := RunCommands(context.Background(), repoRoot, []string{
		`printf '%s' "${PATH:-missing}"`,
	}, policy.VerificationConfig{
		DefaultTimeoutMinutes: 1,
	})
	if err != nil {
		t.Fatalf("RunCommands returned error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 command result, got %d", len(results))
	}
	if results[0].ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d (command must find PATH)", results[0].ExitCode)
	}

	stdoutData, err := os.ReadFile(results[0].StdoutPath)
	if err != nil {
		t.Fatalf("read stdout artifact: %v", err)
	}
	if strings.TrimSpace(string(stdoutData)) == "missing" {
		t.Fatalf("PATH must be available in default verification environment")
	}
}

func TestDeriveVerificationPassed_FailsOnNonZeroExit(t *testing.T) {
	if DeriveVerificationPassed(nil) {
		t.Fatalf("expected empty results to fail verification")
	}

	results := []CommandResult{
		{ExitCode: 0},
		{ExitCode: 7},
	}
	if DeriveVerificationPassed(results) {
		t.Fatalf("expected non-zero exit code to fail verification")
	}

	results = []CommandResult{
		{ExitCode: 0},
		{TimedOut: true, ExitCode: -1},
	}
	if DeriveVerificationPassed(results) {
		t.Fatalf("expected timeout to fail verification")
	}
}
