//go:build unix
// +build unix

package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestRunStreamingCommand_NormalCompletion verifies that a subprocess producing
// valid stream-json output is processed correctly and no error is returned.
func TestRunStreamingCommand_NormalCompletion(t *testing.T) {
	resultLine := `{"type":"result","subtype":"success","result":"hello","is_error":false,"duration_ms":100,"num_turns":1}`
	result, err := defaultRunStreamingCommand(
		context.Background(),
		"/bin/sh",
		[]string{"-c", fmt.Sprintf("printf '%%s\n' '%s'", resultLine)},
		nil,
		nil,
		10*time.Second,
		nil,
	)
	if err != nil {
		t.Fatalf("expected no error on normal completion, got: %v", err)
	}
	if len(result.stdout) == 0 {
		t.Fatal("expected non-empty stdout in result")
	}
	// The result stdout should be the marshalled result event JSON.
	var out map[string]any
	if err := json.Unmarshal(result.stdout, &out); err != nil {
		t.Fatalf("result stdout is not valid JSON: %v — raw: %s", err, result.stdout)
	}
	if out["result"] != "hello" {
		t.Fatalf("expected result field 'hello', got %v", out["result"])
	}
}

// TestSetupProcessGroup_KillsGrandchildren verifies that setupProcessGroup
// causes SIGKILL to be sent to the entire process group on context cancellation,
// terminating both the direct child process and any grandchildren it spawned.
// This ensures MCP helper subprocesses launched by a worker do not survive a
// context cancellation triggered by Ctrl-C / SIGINT / SIGTERM.
func TestSetupProcessGroup_KillsGrandchildren(t *testing.T) {
	t.Parallel()

	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")

	// Shell script: spawn a long-running background job (grandchild), write
	// its PID to a file so we can verify it is dead after cancellation, then
	// wait for it. Non-interactive shells do not create separate process groups
	// for background jobs, so sleep inherits the shell's PGID.
	script := fmt.Sprintf(
		`sleep 100 & gcp=$!; printf '%%d\n' "$gcp" > %s; wait $gcp`,
		pidFile,
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", script)
	setupProcessGroup(cmd)

	if err := cmd.Start(); err != nil {
		t.Fatalf("cmd.Start: %v", err)
	}

	// Poll until the shell writes the grandchild PID to disk (usually <20 ms).
	var grandchildPID int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, readErr := os.ReadFile(pidFile)
		if readErr == nil {
			var pid int
			if n, _ := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &pid); n == 1 && pid > 0 {
				grandchildPID = pid
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if grandchildPID == 0 {
		t.Fatal("timed out waiting for grandchild PID to be written to disk")
	}
	t.Logf("grandchild PID: %d", grandchildPID)

	// Cancel the context. setupProcessGroup.cmd.Cancel sends SIGKILL to the
	// entire process group (shell + grandchild sleep).
	cancel()

	// cmd.Wait must return promptly after the group is killed (WaitDelay gives
	// Go time to close pipes and unblock Wait).
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	select {
	case <-waitDone:
	case <-time.After(10 * time.Second):
		t.Fatal("cmd.Wait did not return after context cancellation — group kill may have failed")
	}

	// Give the kernel a moment to reap the grandchild.
	time.Sleep(100 * time.Millisecond)

	// syscall.Kill(pid, 0) returns ESRCH when the process no longer exists.
	// If it returns nil the process is still alive — the group kill did not
	// reach the grandchild.
	err := syscall.Kill(grandchildPID, 0)
	switch {
	case err == nil:
		t.Errorf("grandchild process %d is still alive after context cancellation — "+
			"process group SIGKILL did not reach the grandchild", grandchildPID)
	case errors.Is(err, syscall.ESRCH):
		// Process does not exist — expected outcome.
	default:
		// EPERM or other: process exists but we lack permission; treat as still alive.
		t.Errorf("unexpected error checking grandchild %d: %v — "+
			"process may still be alive", grandchildPID, err)
	}
}
