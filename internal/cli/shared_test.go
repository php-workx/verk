package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLatestRunID_SortsByNameNotMtime(t *testing.T) {
	repoRoot := t.TempDir()
	runsDir := filepath.Join(repoRoot, ".verk", "runs")
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Create run directories with timestamp-based names.
	// Use the same ticket ID prefix so the unixNano suffix determines order.
	// Intentionally create them in non-chronological order on disk
	// to verify that lexicographic ordering (by name) is used,
	// not filesystem mtime.
	names := []string{
		"run-ver-test-1712345678000000000",
		"run-ver-test-1712345679999999999",
		"run-ver-test-1712345678900000000",
	}
	for _, name := range names {
		if err := os.Mkdir(filepath.Join(runsDir, name), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
	}

	latest, err := latestRunID(repoRoot)
	if err != nil {
		t.Fatalf("latestRunID: %v", err)
	}
	if latest != "run-ver-test-1712345679999999999" {
		t.Fatalf("expected latest run to be run-ver-test-1712345679999999999, got %s", latest)
	}
}

func TestLatestRunID_EmptyDir(t *testing.T) {
	repoRoot := t.TempDir()
	runsDir := filepath.Join(repoRoot, ".verk", "runs")
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	latest, err := latestRunID(repoRoot)
	if err != nil {
		t.Fatalf("latestRunID: %v", err)
	}
	if latest != "" {
		t.Fatalf("expected empty latest run ID, got %q", latest)
	}
}

func TestLatestRunID_NoRunsDir(t *testing.T) {
	repoRoot := t.TempDir()

	latest, err := latestRunID(repoRoot)
	if err != nil {
		t.Fatalf("latestRunID: %v", err)
	}
	if latest != "" {
		t.Fatalf("expected empty latest run ID, got %q", latest)
	}
}

func TestLatestRunID_IgnoresNonRunDirs(t *testing.T) {
	repoRoot := t.TempDir()
	runsDir := filepath.Join(repoRoot, ".verk", "runs")
	if err := os.MkdirAll(runsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Create a directory that doesn't start with "run-"
	if err := os.Mkdir(filepath.Join(runsDir, "other-dir"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// And one valid run directory
	if err := os.Mkdir(filepath.Join(runsDir, "run-ver-abc-1000000000000000000"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	latest, err := latestRunID(repoRoot)
	if err != nil {
		t.Fatalf("latestRunID: %v", err)
	}
	if latest != "run-ver-abc-1000000000000000000" {
		t.Fatalf("expected run-ver-abc-1000000000000000000, got %s", latest)
	}
}