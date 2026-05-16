package verknative_test

import (
	"os"
	"path/filepath"
	"testing"
	"verk/internal/bench"
	"verk/internal/bench/providers/verknative"
)

func TestProvider_Name(t *testing.T) {
	p := verknative.New()
	if got := p.Name(); got != "verk-native" {
		t.Fatalf("Name() = %q, want %q", got, "verk-native")
	}
}

func TestProvider_SuitesContainsSmoke(t *testing.T) {
	p := verknative.New()
	suites := p.Suites()
	for _, s := range suites {
		if s.Name == "smoke" {
			return
		}
	}
	t.Fatal("Suites() does not contain a suite named 'smoke'")
}

func TestProvider_LoadTasksSmokeReturnsThree(t *testing.T) {
	p := verknative.New()
	tasks, err := p.LoadTasks("smoke")
	if err != nil {
		t.Fatalf("LoadTasks(smoke) error: %v", err)
	}
	if len(tasks) != 3 {
		t.Fatalf("LoadTasks(smoke) returned %d tasks, want 3", len(tasks))
	}
	// Verify suite is stamped.
	for _, task := range tasks {
		if task.Suite != "smoke" {
			t.Errorf("task %q has Suite=%q, want %q", task.ID, task.Suite, "smoke")
		}
	}
}

func TestProvider_LoadTasksUnknownSuiteErrors(t *testing.T) {
	p := verknative.New()
	_, err := p.LoadTasks("nonexistent")
	if err == nil {
		t.Fatal("LoadTasks(nonexistent) expected an error, got nil")
	}
}

func TestProvider_CapabilitiesIsolatedVerifier(t *testing.T) {
	p := verknative.New()
	caps := p.Capabilities()
	if !caps.SupportsIsolatedVerifier {
		t.Fatal("Capabilities().SupportsIsolatedVerifier should be true")
	}
}

func TestPrepareWorkspace_CreatesDir(t *testing.T) {
	p := verknative.New()
	dir := t.TempDir()
	// Use a subdirectory that doesn't exist yet.
	target := filepath.Join(dir, "workspace")

	task := bench.Task{
		ID:    "smoke-001",
		Title: "Test task",
		Suite: "smoke",
		Spec: map[string]any{
			"instruction": "do something",
		},
	}

	if err := p.PrepareWorkspace(task, target); err != nil {
		t.Fatalf("PrepareWorkspace error: %v", err)
	}

	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("workspace directory not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("workspace path is not a directory")
	}

	// TASK.md should be seeded.
	taskMD := filepath.Join(target, "TASK.md")
	if _, err := os.Stat(taskMD); err != nil {
		t.Fatalf("TASK.md not created in workspace: %v", err)
	}
}

// smokeTask returns a bench.Task with the given marker/contents spec fields.
func smokeTask(marker, contents string) bench.Task {
	spec := map[string]any{}
	if marker != "" {
		spec["expect_marker"] = marker
	}
	if contents != "" {
		spec["expect_contents"] = contents
	}
	return bench.Task{
		ID:    "smoke-001",
		Suite: "smoke",
		Title: "Test",
		Spec:  spec,
	}
}

func TestScore_SolvedWhenMarkerFileMatches(t *testing.T) {
	dir := t.TempDir()

	// Write the expected marker file.
	if err := os.WriteFile(filepath.Join(dir, "done.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}

	task := smokeTask("done.txt", "ok")
	score := verknative.Score(task, dir)
	if !score.Solved {
		t.Fatal("Score.Solved should be true when marker file exists with correct contents")
	}
}

func TestScore_UnsolvedWhenMarkerMissing(t *testing.T) {
	dir := t.TempDir()
	// No files written — marker is absent.
	task := smokeTask("done.txt", "ok")
	score := verknative.Score(task, dir)
	if score.Solved {
		t.Fatal("Score.Solved should be false when marker file is missing")
	}
}

func TestScore_UnsolvedWhenContentsMismatch(t *testing.T) {
	dir := t.TempDir()

	// Write file with wrong contents.
	if err := os.WriteFile(filepath.Join(dir, "done.txt"), []byte("wrong"), 0o644); err != nil {
		t.Fatal(err)
	}

	task := smokeTask("done.txt", "ok")
	score := verknative.Score(task, dir)
	if score.Solved {
		t.Fatal("Score.Solved should be false when file contents do not match")
	}
}
