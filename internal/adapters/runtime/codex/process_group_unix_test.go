//go:build unix
// +build unix

package codex

import (
	"context"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// TestSetupProcessGroup_CancelTreatsESRCHAsSuccess — see the claude package
// equivalent for full rationale. Mirrored here to keep the three runtime
// helpers behaviourally consistent.
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
	if err := cmd.Cancel(); err != nil {
		t.Fatalf("cmd.Cancel after process exit: expected nil, got %v", err)
	}
}

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

func TestSetupProcessGroup_PreservesNonZeroWaitDelay(t *testing.T) {
	t.Parallel()

	cmd := exec.Command("/bin/true")
	cmd.WaitDelay = 42 * time.Second

	setupProcessGroup(cmd)

	if cmd.WaitDelay != 42*time.Second {
		t.Errorf("WaitDelay was overwritten: got %v, want %v", cmd.WaitDelay, 42*time.Second)
	}
}

func TestSetupProcessGroup_AppliesDefaultWaitDelay(t *testing.T) {
	t.Parallel()

	cmd := exec.Command("/bin/true")

	setupProcessGroup(cmd)

	if cmd.WaitDelay != defaultProcessGroupWaitDelay {
		t.Errorf("default WaitDelay not applied: got %v, want %v", cmd.WaitDelay, defaultProcessGroupWaitDelay)
	}
}

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
