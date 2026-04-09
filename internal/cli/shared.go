package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	repoadapter "verk/internal/adapters/repo/git"
	"verk/internal/adapters/runtime"
	"verk/internal/adapters/runtime/claude"
	"verk/internal/adapters/runtime/codex"
	"verk/internal/policy"
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

func printJSON(w io.Writer, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = w.Write(data)
	return err
}
