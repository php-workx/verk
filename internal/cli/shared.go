package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"verk/internal/adapters/runtime"
	"verk/internal/adapters/runtime/claude"
	"verk/internal/adapters/runtime/codex"
	"verk/internal/policy"

	repoadapter "verk/internal/adapters/repo/git"

	"github.com/spf13/cobra"
)

const (
	runtimeNameClaude = "claude"
	runtimeNameCodex  = "codex"
)

func loadExecutionContext() (string, policy.Config, *repoadapter.Repo, error) {
	repo, err := repoadapter.New(".")
	if err != nil {
		return "", policy.Config{}, nil, err
	}
	// Use the main worktree root so .tickets/ and .verk/ are found
	// even when running from a git worktree.
	repoRoot, err := repo.MainWorktreeRoot()
	if err != nil {
		return "", policy.Config{}, nil, err
	}
	cfg, err := policy.LoadConfig(repoRoot)
	if err != nil {
		return "", policy.Config{}, nil, err
	}
	return repoRoot, cfg, repo, nil
}

func runtimeAdapterFor(ticketPreference, defaultRuntime string) (runtime.Adapter, error) {
	switch normalizeRuntime(ticketPreference, defaultRuntime) {
	case runtimeNameCodex:
		return codex.New(), nil
	case runtimeNameClaude:
		return claude.New(), nil
	default:
		return nil, fmt.Errorf("unsupported runtime %q", normalizeRuntime(ticketPreference, defaultRuntime))
	}
}

func normalizeRuntime(ticketPreference, defaultRuntime string) string {
	for _, candidate := range []string{ticketPreference, defaultRuntime, runtimeNameCodex} {
		candidate = strings.TrimSpace(strings.ToLower(candidate))
		if candidate != "" {
			return candidate
		}
	}
	return runtimeNameCodex
}

func newRunID(ticketID string) string {
	return fmt.Sprintf("run-%s-%d", ticketID, time.Now().UTC().UnixNano())
}

func mustJSONMap(v any) map[string]any {
	data, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Errorf("mustJSONMap: marshal %T: %w", v, err))
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		panic(fmt.Errorf("mustJSONMap: unmarshal %T: %w", v, err))
	}
	return out
}

func resolveRepoRoot() (string, error) {
	repo, err := repoadapter.New(".")
	if err != nil {
		return "", err
	}
	return repo.MainWorktreeRoot()
}

func writeCurrentRunID(repoRoot, runID string) error {
	dir := filepath.Join(repoRoot, ".verk")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "current"), []byte(runID+"\n"), 0o644)
}

func clearCurrentRunID(repoRoot string) error {
	path := filepath.Join(repoRoot, ".verk", "current")
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func readCurrentRunID(repoRoot string) (string, error) {
	data, err := os.ReadFile(filepath.Join(repoRoot, ".verk", "current"))
	if err == nil {
		if id := strings.TrimSpace(string(data)); id != "" {
			return id, nil
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	// Fallback: find the most recent run directory.
	return latestRunID(repoRoot)
}

func latestRunID(repoRoot string) (string, error) {
	runsDir := filepath.Join(repoRoot, ".verk", "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	var latestName string
	var latestTS int64 = -1
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "run-") {
			continue
		}
		name := entry.Name()
		// Run IDs are formatted run-<ticketID>-<unixNano>.
		// Parse the unixNano suffix so that runs across different ticket IDs
		// are ordered correctly by time, not lexicographically by ticket name.
		idx := strings.LastIndex(name, "-")
		if idx < 0 {
			// Non-conforming entry in the runs directory; log unconditionally
			// because the project has no debug-level log facility and these
			// entries indicate an unexpected filesystem state worth surfacing.
			log.Printf("debug: latestRunID: skipping %q (no '-' separator)", name)
			continue
		}
		ts, err := strconv.ParseInt(name[idx+1:], 10, 64)
		if err != nil {
			// Same rationale: unparseable timestamp suffix is unexpected;
			// emit via log.Printf so the operator can diagnose stale entries.
			log.Printf("debug: latestRunID: skipping %q (unparseable timestamp: %v)", name, err)
			continue
		}
		if ts > latestTS {
			latestTS = ts
			latestName = name
		}
	}
	return latestName, nil
}

func resolveRunID(args []string) (string, error) {
	if len(args) > 0 {
		return args[0], nil
	}
	repoRoot, err := resolveRepoRoot()
	if err != nil {
		return "", fmt.Errorf("could not determine repo root: %w", err)
	}
	runID, err := readCurrentRunID(repoRoot)
	if err != nil {
		return "", fmt.Errorf("could not read current run: %w", err)
	}
	if runID == "" {
		return "", fmt.Errorf("no current run — start one with: verk run ticket <id>")
	}
	return runID, nil
}

// cmdError prints an error to the command's stderr and returns it as a cobra error.
// It silences cobra's own error output to avoid duplication.
func cmdError(cmd *cobra.Command, err error, code int) error {
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Error: %s\n", err)
	cmd.SilenceErrors = true
	return withExitCode(err, code)
}

func printJSON(w io.Writer, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = w.Write(data)
	return err
}
