//go:build unix

package claude

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
)

// defaultProcessGroupWaitDelay is the grace period exec.Cmd gives to close I/O
// pipes and unblock cmd.Wait after the group has been killed. Exposed as a
// constant so tests can assert the default value.
const defaultProcessGroupWaitDelay = 5 * time.Second

// setupProcessGroup configures cmd to run in its own process group and arranges
// for the entire group to be killed when the command's context is cancelled.
// This ensures MCP helper subprocesses launched by the worker (grandchildren of
// the verk process) do not outlive the parent command when the run is interrupted.
//
// Behaviour:
//   - Setpgid: true puts the child in a new process group (PGID = child PID).
//   - cmd.Cancel overrides the default per-process kill with a group-wide SIGKILL,
//     so all grandchildren in the group are also terminated immediately. A
//     previously installed Cancel (e.g. the default one that exec.CommandContext
//     attaches) is composed after the group kill rather than silently discarded,
//     so caller-supplied cancel hooks still run.
//   - syscall.ESRCH from the group kill is treated as success: it means the
//     group already exited between the Process nil check and the kill, which
//     is a normal race during cancellation and must not surface as an error.
//   - WaitDelay gives Go time to close I/O pipes and return from cmd.Wait after
//     the group has been killed. A non-zero WaitDelay configured by the caller
//     is preserved; the default is only applied when it is zero.
//   - SysProcAttr fields configured before this call are preserved; Setpgid is
//     set on the existing struct rather than replacing it wholesale, so callers
//     that need to customise other SysProcAttr fields can do so safely either
//     before or after setupProcessGroup runs.
func setupProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true

	// Kill the entire process group, not just the top-level process.
	// Using SIGKILL directly is appropriate for worker subprocesses: they hold
	// no server state and are designed to be restarted from a clean snapshot.
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
			// Run the previous Cancel for its side effects (e.g. caller-supplied
			// cleanup hooks) but treat its error as non-authoritative. The
			// default Cancel that exec.CommandContext installs calls
			// Process.Kill on the leader, which is already dead after our group
			// kill and would therefore return "process already finished" —
			// surfacing that as a cancellation failure would be misleading.
			_ = prevCancel()
		}
		return killErr
	}

	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = defaultProcessGroupWaitDelay
	}
}
