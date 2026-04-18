package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"verk/internal/policy"
)

const defaultVerificationTimeout = 15 * time.Minute

// artifactFile is the interface for command output artifact files.
// Declared as an interface to allow test injection of closers that simulate
// write or close errors.
type artifactFile interface {
	io.Writer
	io.Closer
}

// createArtifactFile creates a stdout/stderr artifact file at the given path.
// It is a package-level variable so tests can inject a failing implementation
// to verify that Close() errors are propagated.
var createArtifactFile = func(name string) (artifactFile, error) {
	return os.Create(name)
}

type CommandResult struct { //nolint:revive // stuttering name is intentional for clarity
	Command    string    `json:"command"`
	Cwd        string    `json:"cwd"`
	ExitCode   int       `json:"exit_code"`
	TimedOut   bool      `json:"timed_out"`
	DurationMS int64     `json:"duration_ms"`
	StdoutPath string    `json:"stdout_path"`
	StderrPath string    `json:"stderr_path"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
}

func RunCommands(ctx context.Context, repoRoot string, cmds []string, cfg policy.VerificationConfig) ([]CommandResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	absRepoRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve repo root %q: %w", repoRoot, err)
	}
	absRepoRoot = filepath.Clean(absRepoRoot)

	info, err := os.Stat(absRepoRoot)
	if err != nil {
		return nil, fmt.Errorf("stat repo root %q: %w", absRepoRoot, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("repo root %q is not a directory", absRepoRoot)
	}

	env := verificationEnv(cfg.EnvPassthrough)
	timeout := time.Duration(cfg.DefaultTimeoutMinutes) * time.Minute
	if timeout <= 0 {
		timeout = defaultVerificationTimeout
	}

	verificationRoot := filepath.Join(absRepoRoot, ".verk", "verification")
	if err := os.MkdirAll(verificationRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create verification root %q: %w", verificationRoot, err)
	}
	runDir, err := os.MkdirTemp(verificationRoot, "run-")
	if err != nil {
		return nil, fmt.Errorf("create verification run directory under %q: %w", verificationRoot, err)
	}

	results := make([]CommandResult, 0, len(cmds))
	for i, rawCmd := range cmds {
		command := strings.TrimSpace(rawCmd)
		if command == "" {
			return results, fmt.Errorf("verification command %d is empty", i+1)
		}

		stdoutPath := filepath.Join(runDir, fmt.Sprintf("command-%02d.stdout.log", i+1))
		stderrPath := filepath.Join(runDir, fmt.Sprintf("command-%02d.stderr.log", i+1))

		result, err := func() (r CommandResult, retErr error) {
			stdoutFile, err := createArtifactFile(stdoutPath)
			if err != nil {
				return r, fmt.Errorf("create stdout artifact for command %d: %w", i+1, err)
			}
			defer func() {
				if cerr := stdoutFile.Close(); cerr != nil && retErr == nil {
					retErr = fmt.Errorf("close stdout artifact for command %d: %w", i+1, cerr)
				}
			}()
			stderrFile, err := createArtifactFile(stderrPath)
			if err != nil {
				return r, fmt.Errorf("create stderr artifact for command %d: %w", i+1, err)
			}
			defer func() {
				if cerr := stderrFile.Close(); cerr != nil && retErr == nil {
					retErr = fmt.Errorf("close stderr artifact for command %d: %w", i+1, cerr)
				}
			}()

			startedAt := time.Now().UTC()
			cmdCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			cmd := exec.CommandContext(cmdCtx, "/bin/sh", "-c", command)
			cmd.Dir = absRepoRoot
			cmd.Env = env
			cmd.Stdout = stdoutFile
			cmd.Stderr = stderrFile
			// Put the subprocess in its own process group so that any processes
			// spawned by the shell command are also killed on context cancellation.
			setupProcessGroup(cmd)

			execErr := cmd.Run()
			finishedAt := time.Now().UTC()
			cmdErr := cmdCtx.Err()
			parentErr := ctx.Err()

			timedOut := errors.Is(cmdErr, context.DeadlineExceeded) || errors.Is(parentErr, context.DeadlineExceeded)
			if execErr != nil && !timedOut && !isExpectedExecutionFailure(execErr) {
				return r, fmt.Errorf("run verification command %d: %w", i+1, execErr)
			}

			exitCode := 0
			switch {
			case timedOut:
				exitCode = -1
			case cmd.ProcessState != nil:
				exitCode = cmd.ProcessState.ExitCode()
			case execErr != nil:
				exitCode = exitCodeFromError(execErr)
			}

			return CommandResult{
				Command:    command,
				Cwd:        absRepoRoot,
				ExitCode:   exitCode,
				TimedOut:   timedOut,
				DurationMS: finishedAt.Sub(startedAt).Milliseconds(),
				StdoutPath: stdoutPath,
				StderrPath: stderrPath,
				StartedAt:  startedAt,
				FinishedAt: finishedAt,
			}, nil
		}()
		if err != nil {
			return results, err
		}
		results = append(results, result)
	}

	return results, nil
}

// RunQualityCommands runs structured quality commands before per-ticket validation
// commands. Each QualityCommand specifies an optional subdirectory (relative to
// repoRoot) and one or more shell commands to run sequentially from that directory.
// This supports monorepo setups where different packages have different quality gates.
func RunQualityCommands(ctx context.Context, repoRoot string, cmds []policy.QualityCommand, cfg policy.VerificationConfig) ([]CommandResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(cmds) == 0 {
		return nil, nil
	}

	absRepoRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve repo root %q: %w", repoRoot, err)
	}
	absRepoRoot = filepath.Clean(absRepoRoot)

	info, err := os.Stat(absRepoRoot)
	if err != nil {
		return nil, fmt.Errorf("stat repo root %q: %w", absRepoRoot, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("repo root %q is not a directory", absRepoRoot)
	}

	env := verificationEnv(cfg.EnvPassthrough)
	timeout := time.Duration(cfg.DefaultTimeoutMinutes) * time.Minute
	if timeout <= 0 {
		timeout = defaultVerificationTimeout
	}

	verificationRoot := filepath.Join(absRepoRoot, ".verk", "verification")
	if err := os.MkdirAll(verificationRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create verification root %q: %w", verificationRoot, err)
	}
	runDir, err := os.MkdirTemp(verificationRoot, "quality-")
	if err != nil {
		return nil, fmt.Errorf("create quality run directory: %w", err)
	}

	var results []CommandResult
	cmdIndex := 0
	for _, qc := range cmds {
		workDir := absRepoRoot
		if qc.Path != "" {
			workDir = filepath.Join(absRepoRoot, filepath.Clean(qc.Path))
			if !strings.HasPrefix(workDir+string(filepath.Separator), absRepoRoot+string(filepath.Separator)) {
				return results, fmt.Errorf("quality command path %q escapes repo root", qc.Path)
			}
		}

		for _, rawCmd := range qc.Run {
			command := strings.TrimSpace(rawCmd)
			if command == "" {
				return results, fmt.Errorf("quality command %d is empty", cmdIndex+1)
			}

			stdoutPath := filepath.Join(runDir, fmt.Sprintf("command-%02d.stdout.log", cmdIndex+1))
			stderrPath := filepath.Join(runDir, fmt.Sprintf("command-%02d.stderr.log", cmdIndex+1))

			// Capture cmdIndex for use in the closure; it is mutated after this block.
			idx := cmdIndex
			result, err := func() (r CommandResult, retErr error) {
				stdoutFile, err := createArtifactFile(stdoutPath)
				if err != nil {
					return r, fmt.Errorf("create stdout artifact for quality command %d: %w", idx+1, err)
				}
				defer func() {
					if cerr := stdoutFile.Close(); cerr != nil && retErr == nil {
						retErr = fmt.Errorf("close stdout artifact for quality command %d: %w", idx+1, cerr)
					}
				}()
				stderrFile, err := createArtifactFile(stderrPath)
				if err != nil {
					return r, fmt.Errorf("create stderr artifact for quality command %d: %w", idx+1, err)
				}
				defer func() {
					if cerr := stderrFile.Close(); cerr != nil && retErr == nil {
						retErr = fmt.Errorf("close stderr artifact for quality command %d: %w", idx+1, cerr)
					}
				}()

				startedAt := time.Now().UTC()
				cmdCtx, cancel := context.WithTimeout(ctx, timeout)
				defer cancel()
				cmd := exec.CommandContext(cmdCtx, "/bin/sh", "-c", command)
				cmd.Dir = workDir
				cmd.Env = env
				cmd.Stdout = stdoutFile
				cmd.Stderr = stderrFile
				// Put the subprocess in its own process group so that any processes
				// spawned by the shell command are also killed on context cancellation.
				setupProcessGroup(cmd)

				execErr := cmd.Run()
				finishedAt := time.Now().UTC()
				cmdErr := cmdCtx.Err()
				parentErr := ctx.Err()

				timedOut := errors.Is(cmdErr, context.DeadlineExceeded) || errors.Is(parentErr, context.DeadlineExceeded)
				if execErr != nil && !timedOut && !isExpectedExecutionFailure(execErr) {
					return r, fmt.Errorf("run quality command %d: %w", idx+1, execErr)
				}

				exitCode := 0
				switch {
				case timedOut:
					exitCode = -1
				case cmd.ProcessState != nil:
					exitCode = cmd.ProcessState.ExitCode()
				case execErr != nil:
					exitCode = exitCodeFromError(execErr)
				}

				return CommandResult{
					Command:    command,
					Cwd:        workDir,
					ExitCode:   exitCode,
					TimedOut:   timedOut,
					DurationMS: finishedAt.Sub(startedAt).Milliseconds(),
					StdoutPath: stdoutPath,
					StderrPath: stderrPath,
					StartedAt:  startedAt,
					FinishedAt: finishedAt,
				}, nil
			}()
			if err != nil {
				return results, err
			}
			results = append(results, result)
			cmdIndex++
		}
	}

	return results, nil
}

func DeriveVerificationPassed(results []CommandResult) bool {
	if len(results) == 0 {
		return false
	}
	for _, result := range results {
		if result.TimedOut || result.ExitCode != 0 {
			return false
		}
	}
	return true
}

// defaultEnvAllowlist contains the minimum set of environment variables always
// included in the verification environment. This ensures PATH-dependent commands
// (go, git, npm, cargo, etc.) can locate their executables while keeping the
// environment deterministic and free of unintended variable leakage.
var defaultEnvAllowlist = []string{
	"CI",
	"HOME",
	"LOGNAME",
	"PATH",
	"TERM",
	"USER",
}

// verificationEnv builds a deterministic environment for verification commands
// by allowlisting variables from the host environment. It always includes the
// defaultEnvAllowlist entries, then overlays any caller-configured passthrough
// variables. The returned slice is never nil — commands always run with an
// explicit environment rather than inheriting the full parent environment.
func verificationEnv(allowlist []string) []string {
	keys := make(map[string]bool, len(defaultEnvAllowlist)+len(allowlist))
	for _, k := range defaultEnvAllowlist {
		keys[k] = true
	}
	for _, k := range allowlist {
		if k = strings.TrimSpace(k); k != "" {
			keys[k] = true
		}
	}

	pairs := make([]string, 0, len(keys))
	for k := range keys {
		if value, ok := os.LookupEnv(k); ok {
			pairs = append(pairs, k+"="+value)
		}
	}
	sort.Strings(pairs)
	return pairs
}

func isExpectedExecutionFailure(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr)
}

func exitCodeFromError(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ProcessState != nil {
		return exitErr.ExitCode()
	}
	return -1
}
