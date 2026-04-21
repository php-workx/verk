//go:build unix

package command

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
)

// defaultProcessGroupWaitDelay is the grace period exec.Cmd gives to close I/O
// pipes and unblock cmd.Wait after the group has been killed.
const defaultProcessGroupWaitDelay = 5 * time.Second

// setupProcessGroup configures cmd to run in its own process group and arranges
// for the entire group to be killed when the command's context is cancelled.
// Verification commands run shell scripts that may fork subprocesses (e.g. npm,
// go test, cargo); putting them in a process group ensures those grandchildren
// are also terminated when the parent context is cancelled.
//
// See internal/adapters/runtime/claude/process_group_unix.go for full rationale.
// The runtime and verification helpers must remain behaviourally consistent.
func setupProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true

	prevCancel := cmd.Cancel
	cmd.Cancel = func() error {
		var killErr error
		if cmd.Process != nil {
			// Negative PID in Kill targets the process group. ESRCH is a benign
			// race: the group already exited before we got here.
			if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				killErr = err
			}
		}
		if prevCancel != nil {
			// Preserve caller-supplied cancel hooks but treat their error as
			// non-authoritative; the default exec.CommandContext cancel will
			// typically return "process already finished" after our group kill.
			_ = prevCancel()
		}
		return killErr
	}

	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = defaultProcessGroupWaitDelay
	}
}
