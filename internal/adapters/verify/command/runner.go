package command

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"verk/internal/policy"
)

const defaultVerificationTimeout = 15 * time.Minute

type CommandResult struct {
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
	if env == nil {
		env = []string{}
	}
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
		stdoutFile, err := os.Create(stdoutPath)
		if err != nil {
			return results, fmt.Errorf("create stdout artifact for command %d: %w", i+1, err)
		}
		stderrFile, err := os.Create(stderrPath)
		if err != nil {
			_ = stdoutFile.Close()
			return results, fmt.Errorf("create stderr artifact for command %d: %w", i+1, err)
		}

		startedAt := time.Now().UTC()
		cmdCtx, cancel := context.WithTimeout(ctx, timeout)
		cmd := exec.CommandContext(cmdCtx, "/bin/sh", "-c", command)
		cmd.Dir = absRepoRoot
		cmd.Env = env
		cmd.Stdout = stdoutFile
		cmd.Stderr = stderrFile

		runErr := cmd.Run()
		finishedAt := time.Now().UTC()
		cmdErr := cmdCtx.Err()
		parentErr := ctx.Err()
		cancel()

		_ = stdoutFile.Close()
		_ = stderrFile.Close()

		timedOut := errors.Is(cmdErr, context.DeadlineExceeded) || errors.Is(parentErr, context.DeadlineExceeded)
		if runErr != nil && !timedOut && !isExpectedExecutionFailure(runErr) {
			return results, fmt.Errorf("run verification command %d: %w", i+1, runErr)
		}

		exitCode := 0
		switch {
		case timedOut:
			exitCode = -1
		case cmd.ProcessState != nil:
			exitCode = cmd.ProcessState.ExitCode()
		case runErr != nil:
			exitCode = exitCodeFromError(runErr)
		}

		results = append(results, CommandResult{
			Command:    command,
			Cwd:        absRepoRoot,
			ExitCode:   exitCode,
			TimedOut:   timedOut,
			DurationMS: finishedAt.Sub(startedAt).Milliseconds(),
			StdoutPath: stdoutPath,
			StderrPath: stderrPath,
			StartedAt:  startedAt,
			FinishedAt: finishedAt,
		})
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

func verificationEnv(allowlist []string) []string {
	if len(allowlist) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(allowlist))
	pairs := make([]string, 0, len(allowlist))
	for _, name := range allowlist {
		key := strings.TrimSpace(name)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if value, ok := os.LookupEnv(key); ok {
			pairs = append(pairs, key+"="+value)
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
		return exitErr.ProcessState.ExitCode()
	}
	return -1
}
