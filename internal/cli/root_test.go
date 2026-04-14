package cli

import (
	"os"
	"strings"
	"testing"
)

// TestExecuteArgs_FlagIsolation verifies that flag state from one ExecuteArgs
// invocation does not bleed into a subsequent invocation.
//
// Concretely: calling `reopen --to implement` (a valid phase) sets the
// reopenToPhase flag variable. In the old global-rootCmd design, that value
// persisted across calls, so a second `reopen` call without --to would skip
// the "--to required" validation and fail later with a different error/code.
// With newRootCmd() the second invocation must see reopenToPhase == "" and
// must exit with code 2 (validation failure) with the "--to" mention in stderr.
func TestExecuteArgs_FlagIsolation(t *testing.T) {
	// First invocation: supply --to implement (valid phase).
	// This will fail because there is no git repo, but the flag is processed
	// and would have set the package-level variable under the old design.
	stdout1, stderr1 := osPipe(t)
	_ = ExecuteArgs(
		[]string{"reopen", "run-abc", "ticket-abc", "--to", "implement"},
		stdout1, stderr1,
	)

	// Second invocation: omit --to entirely.
	// A fresh command tree must present reopenToPhase == "" so that the
	// "--to flag is required" guard fires and returns exit code 2.
	// Under the old design, the leaked "implement" value bypassed this check,
	// causing the command to attempt repo resolution and return exit code 1.
	stdout2, stderr2 := osPipe(t)
	exitCode := ExecuteArgs(
		[]string{"reopen", "run-abc", "ticket-abc"},
		stdout2, stderr2,
	)
	if exitCode != 2 {
		t.Fatalf("second ExecuteArgs: expected exit code 2 (--to required), got %d", exitCode)
	}

	// The error message must mention --to to confirm it is the validation error.
	_ = stderr2.Sync()
	data, err := os.ReadFile(stderr2.Name())
	if err != nil {
		t.Fatalf("read stderr2: %v", err)
	}
	if !strings.Contains(string(data), "--to") {
		t.Fatalf("expected '--to' in stderr, got: %s", string(data))
	}
}
