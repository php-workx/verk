//go:build unix

package command

import (
	"os/exec"
	"syscall"
	"time"
)

// setupProcessGroup configures cmd to run in its own process group and arranges
// for the entire group to be killed when the command's context is cancelled.
// Verification commands run shell scripts that may fork subprocesses (e.g. npm,
// go test, cargo); putting them in a process group ensures those grandchildren
// are also terminated when the parent context is cancelled.
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
