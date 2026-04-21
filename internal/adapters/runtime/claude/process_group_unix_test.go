//go:build unix
// +build unix

package claude

import (
	"context"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// TestSetupProcessGroup_CancelTreatsESRCHAsSuccess verifies that cmd.Cancel
// returns nil when syscall.Kill against the process group would return ESRCH.
// This models the race where the group exits between the cmd.Process nil
// check and the kill — a normal occurrence during cancellation that must not
// surface as an error.
func TestSetupProcessGroup_CancelTreatsESRCHAsSuccess(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "exit 0")
	setupProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("cmd.Start: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("cmd.Wait: %v", err)
	}

	// Process has exited and been reaped. syscall.Kill(-pid, SIGKILL) against
	// the (now empty) process group returns ESRCH. Cancel must translate that
	// into nil because the group has already stopped existing — the state we
	// were trying to achieve.
	if err := cmd.Cancel(); err != nil {
		t.Fatalf("cmd.Cancel after process exit: expected nil, got %v", err)
	}
}

// TestSetupProcessGroup_PreservesSysProcAttr verifies that preconfigured
// SysProcAttr fields survive the helper and that Setpgid is set on the
// existing struct rather than replacing it wholesale.
func TestSetupProcessGroup_PreservesSysProcAttr(t *testing.T) {
	t.Parallel()

	cmd := exec.Command("/bin/true")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	original := cmd.SysProcAttr

	setupProcessGroup(cmd)

	if cmd.SysProcAttr != original {
		t.Fatalf("SysProcAttr struct was replaced; pointer changed")
	}
	if !cmd.SysProcAttr.Setpgid {
		t.Errorf("Setpgid was not set to true")
	}
	if !cmd.SysProcAttr.Setsid {
		t.Errorf("preconfigured Setsid was lost")
	}
}

// TestSetupProcessGroup_AllocatesSysProcAttrWhenNil verifies that the helper
// still works when SysProcAttr is nil on entry.
func TestSetupProcessGroup_AllocatesSysProcAttrWhenNil(t *testing.T) {
	t.Parallel()

	cmd := exec.Command("/bin/true")
	cmd.SysProcAttr = nil

	setupProcessGroup(cmd)

	if cmd.SysProcAttr == nil {
		t.Fatal("SysProcAttr was not allocated")
	}
	if !cmd.SysProcAttr.Setpgid {
		t.Errorf("Setpgid was not set to true")
	}
}

// TestSetupProcessGroup_PreservesNonZeroWaitDelay verifies that a
// caller-configured WaitDelay survives the helper unchanged.
func TestSetupProcessGroup_PreservesNonZeroWaitDelay(t *testing.T) {
	t.Parallel()

	cmd := exec.Command("/bin/true")
	cmd.WaitDelay = 42 * time.Second

	setupProcessGroup(cmd)

	if cmd.WaitDelay != 42*time.Second {
		t.Errorf("WaitDelay was overwritten: got %v, want %v", cmd.WaitDelay, 42*time.Second)
	}
}

// TestSetupProcessGroup_AppliesDefaultWaitDelay verifies that a zero
// WaitDelay is populated with the helper's default.
func TestSetupProcessGroup_AppliesDefaultWaitDelay(t *testing.T) {
	t.Parallel()

	cmd := exec.Command("/bin/true")

	setupProcessGroup(cmd)

	if cmd.WaitDelay != defaultProcessGroupWaitDelay {
		t.Errorf("default WaitDelay not applied: got %v, want %v", cmd.WaitDelay, defaultProcessGroupWaitDelay)
	}
}

// TestSetupProcessGroup_ComposesExistingCancel verifies that a Cancel function
// installed before setupProcessGroup is still invoked after the helper's
// group-kill, rather than being silently discarded.
func TestSetupProcessGroup_ComposesExistingCancel(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "exit 0")
	var called bool
	cmd.Cancel = func() error {
		called = true
		return nil
	}

	setupProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("cmd.Start: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("cmd.Wait: %v", err)
	}
	if err := cmd.Cancel(); err != nil {
		t.Fatalf("cmd.Cancel: unexpected error %v", err)
	}
	if !called {
		t.Error("pre-existing Cancel was not invoked — helper silently discarded it")
	}
}
