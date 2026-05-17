// Package verknative is the first concrete bench.Provider implementation.
// It ships small in-tree smoke tasks that exercise the verk pipeline without
// any external data dependency. Scoring is deterministic (no LLM calls).
package verknative

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"verk/internal/bench"
)

//go:embed fixtures/smoke.json
var smokeFixture []byte

// Provider is the verk-native benchmark provider.
type Provider struct{}

// New returns a new Provider.
func New() *Provider { return &Provider{} }

// Name implements bench.Provider.
func (p *Provider) Name() string { return "verk-native" }

// Suites implements bench.Provider.
func (p *Provider) Suites() []bench.SuiteMeta {
	return []bench.SuiteMeta{
		{
			Name:         "smoke",
			Provider:     "verk-native",
			TaskCount:    3,
			SamplingMode: "smoke",
			Description:  "Tiny in-tree smoke tasks for verk pipeline",
		},
		{
			Name:         "regression",
			Provider:     "verk-native",
			TaskCount:    3,
			SamplingMode: "regression",
		},
	}
}

// LoadTasks implements bench.Provider.
// Both "smoke" and "regression" suites load from the embedded smoke.json fixture;
// the suite name is stamped onto each task so downstream consumers can distinguish them.
func (p *Provider) LoadTasks(suite string) ([]bench.Task, error) {
	if suite == "smoke" || suite == "regression" {
		var tasks []bench.Task
		if err := json.Unmarshal(smokeFixture, &tasks); err != nil {
			return nil, fmt.Errorf("verknative: unmarshal fixture: %w", err)
		}
		// Stamp the suite name onto each task.
		for i := range tasks {
			tasks[i].Suite = suite
		}
		return tasks, nil
	}
	return nil, fmt.Errorf("verknative: unknown suite %q", suite)
}

// Capabilities implements bench.Provider.
func (p *Provider) Capabilities() bench.ProviderCapabilities {
	return bench.ProviderCapabilities{
		SupportsIsolatedVerifier: true,
		CachePolicy:              "locked",
		SupportedModes:           []bench.BenchmarkMode{bench.ModeFullVerk, bench.ModeWorkerOnly},
	}
}

// PrepareWorkspace creates the isolated solution workspace for the task.
// Callers should pass an empty directory; the provider seeds it with any
// fixture content. The mutable solution workspace and the immutable
// verifier workspace are conceptually separate; callers that need
// stronger isolation should copy the prepared workspace to a verifier
// directory before scoring.
func (p *Provider) PrepareWorkspace(task bench.Task, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("verknative: prepare workspace: %w", err)
	}

	// Write a lightweight README so the agent has orientation.
	readme := fmt.Sprintf("# Benchmark Task: %s\n\nID: %s\n\nSuite: %s\n", task.Title, task.ID, task.Suite)
	if instr, ok := task.Spec["instruction"].(string); ok {
		readme += "\n## Instruction\n\n" + instr + "\n"
	}
	readmePath := filepath.Join(dir, "TASK.md")
	if err := os.WriteFile(readmePath, []byte(readme), 0o644); err != nil {
		return fmt.Errorf("verknative: write TASK.md: %w", err)
	}

	return nil
}

// Score is a deterministic offline scorer. Given the resolved Task and the
// worker's produced workspace path, it computes a Score without any LLM calls.
//
// Scoring logic for smoke fixtures:
//   - Reads task.Spec["expect_marker"] for the expected file path (relative to dir).
//   - Reads task.Spec["expect_contents"] for the expected file contents.
//   - The task is considered solved iff the marker file exists and its trimmed
//     contents match expect_contents exactly.
func Score(task bench.Task, workspaceDir string) bench.Score {
	markerRaw, hasMarker := task.Spec["expect_marker"]
	if !hasMarker {
		// No marker spec — cannot score; treat as unsolved.
		return bench.Score{Solved: false}
	}

	marker, ok := markerRaw.(string)
	if !ok || marker == "" {
		return bench.Score{Solved: false}
	}

	path := filepath.Join(workspaceDir, filepath.FromSlash(marker))
	cleanPath := filepath.Clean(path)
	if !strings.HasPrefix(cleanPath, filepath.Clean(workspaceDir)+string(filepath.Separator)) {
		return bench.Score{Solved: false}
	}
	data, err := os.ReadFile(cleanPath)
	if err != nil {
		// File missing or unreadable — not solved.
		return bench.Score{Solved: false}
	}

	// If expect_contents is provided, require a trimmed match.
	if expectedRaw, ok := task.Spec["expect_contents"]; ok {
		expected, ok := expectedRaw.(string)
		if !ok {
			return bench.Score{Solved: false}
		}
		if strings.TrimSpace(string(data)) != strings.TrimSpace(expected) {
			return bench.Score{Solved: false}
		}
	}

	return bench.Score{Solved: true}
}
