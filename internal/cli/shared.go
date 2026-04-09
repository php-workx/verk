package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	repoadapter "verk/internal/adapters/repo/git"
	"verk/internal/adapters/runtime"
	"verk/internal/adapters/runtime/claude"
	"verk/internal/adapters/runtime/codex"
	"verk/internal/policy"

	"github.com/spf13/cobra"
)

func loadExecutionContext() (string, policy.Config, *repoadapter.Repo, error) {
	repo, err := repoadapter.New(".")
	if err != nil {
		return "", policy.Config{}, nil, err
	}
	repoRoot, err := repo.RepoRoot()
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
	case "codex":
		return codex.New(), nil
	case "claude":
		return claude.New(), nil
	default:
		return nil, fmt.Errorf("unsupported runtime %q", normalizeRuntime(ticketPreference, defaultRuntime))
	}
}

func normalizeRuntime(ticketPreference, defaultRuntime string) string {
	for _, candidate := range []string{ticketPreference, defaultRuntime, "codex"} {
		candidate = strings.TrimSpace(strings.ToLower(candidate))
		if candidate != "" {
			return candidate
		}
	}
	return "codex"
}

func newRunID(ticketID string) string {
	return fmt.Sprintf("run-%s-%d", ticketID, time.Now().UTC().UnixNano())
}

func mustJSONMap(v any) map[string]any {
	data, err := json.Marshal(v)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return map[string]any{}
	}
	return out
}

func resolveRepoRoot() (string, error) {
	repo, err := repoadapter.New(".")
	if err != nil {
		return "", err
	}
	return repo.RepoRoot()
}

func writeCurrentRunID(repoRoot, runID string) error {
	dir := filepath.Join(repoRoot, ".verk")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "current"), []byte(runID+"\n"), 0o644)
}

func readCurrentRunID(repoRoot string) (string, error) {
	data, err := os.ReadFile(filepath.Join(repoRoot, ".verk", "current"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
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

// cmdError prints an error to the command's stdout (so it's always visible,
// even when stderr is suppressed) and returns it as a cobra error.
func cmdError(cmd *cobra.Command, err error, code int) error {
	fmt.Fprintf(cmd.OutOrStdout(), "Error: %s\n", err)
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
