//go:build !unix

package command

import "os/exec"

// setupProcessGroup is a no-op on non-Unix platforms where process groups and
// POSIX signals are not available. Subprocess cleanup on cancellation falls back
// to the default exec.CommandContext behaviour (SIGKILL to the main process only).
func setupProcessGroup(_ *exec.Cmd) {}
