//go:build unix

package codex

import (
	"os/exec"
	"syscall"
	"time"
)

// setupProcessGroup configures cmd to run in its own process group and arranges
// for the entire group to be killed when the command's context is cancelled.
// This ensures MCP helper subprocesses launched by the worker (grandchildren of
// the verk process) do not outlive the parent command when the run is interrupted.
//
// See internal/adapters/runtime/claude/process_group_unix.go for full rationale.
func setupProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 5 * time.Second
}
