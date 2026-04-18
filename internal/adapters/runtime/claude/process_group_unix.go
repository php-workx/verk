//go:build unix

package claude

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
// Behaviour:
//   - Setpgid: true puts the child in a new process group (PGID = child PID).
//   - cmd.Cancel overrides the default per-process kill with a group-wide SIGKILL,
//     so all grandchildren in the group are also terminated immediately.
//   - WaitDelay gives Go time to close I/O pipes and return from cmd.Wait after
//     the group has been killed.
func setupProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Kill the entire process group, not just the top-level process.
	// Using SIGKILL directly is appropriate for worker subprocesses: they hold
	// no server state and are designed to be restarted from a clean snapshot.
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Negative PID in Kill targets the process group.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 5 * time.Second
}
