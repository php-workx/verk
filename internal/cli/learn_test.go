package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"verk/internal/memory"
)

// runLearnInDir runs a verk command from the given directory, returning stdout, stderr, exit code.
func runLearnInDir(t *testing.T, dir string, args ...string) (string, string, int) {
	t.Helper()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("get wd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalWD) })

	var stdout, stderr bytes.Buffer
	root := newRootCmd()
	root.SetArgs(args)
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	err = root.Execute()
	code := 0
	if err != nil {
		code = 1
		if e, ok := err.(*cliExitError); ok {
			code = e.ExitCode()
		}
	}
	return stdout.String(), stderr.String(), code
}

func TestLearn_EscapedRecordsLesson(t *testing.T) {
	dir := t.TempDir()
	initCLITestRepo(t, dir)

	stdout, stderr, code := runLearnInDir(t, dir,
		"learn", "escaped",
		"--summary", "validation did not catch nil pointer",
		"--missed-by", "reviewer",
	)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "lesson recorded:") {
		t.Fatalf("expected 'lesson recorded:' in stdout, got: %s", stdout)
	}

	// Read the JSONL file directly.
	memDir := filepath.Join(dir, ".verk", "memory")
	data, err := os.ReadFile(filepath.Join(memDir, "escaped-defects.jsonl"))
	if err != nil {
		t.Fatalf("read escaped-defects.jsonl: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line in JSONL, got %d", len(lines))
	}

	var lesson memory.EscapedDefect
	if err := json.Unmarshal([]byte(lines[0]), &lesson); err != nil {
		t.Fatalf("unmarshal lesson: %v", err)
	}
	if lesson.Summary != "validation did not catch nil pointer" {
		t.Errorf("Summary = %q, want %q", lesson.Summary, "validation did not catch nil pointer")
	}
	if lesson.Status != memory.StatusProposed {
		t.Errorf("Status = %q, want %q", lesson.Status, memory.StatusProposed)
	}
	if len(lesson.MissedBy) != 1 || lesson.MissedBy[0] != "reviewer" {
		t.Errorf("MissedBy = %v, want [reviewer]", lesson.MissedBy)
	}
}

func TestLearn_EscapedRejectsBadMissedBy(t *testing.T) {
	dir := t.TempDir()
	initCLITestRepo(t, dir)

	_, _, code := runLearnInDir(t, dir,
		"learn", "escaped",
		"--summary", "something went wrong",
		"--missed-by", "nonsense",
	)
	if code == 0 {
		t.Fatal("expected non-zero exit for unknown --missed-by value, got 0")
	}
}

func TestLearn_ListShowsLessons(t *testing.T) {
	dir := t.TempDir()
	initCLITestRepo(t, dir)

	// Seed a lesson via the store directly.
	memDir := filepath.Join(dir, ".verk", "memory")
	id := fmt.Sprintf("learn-%d", time.Now().UnixNano())
	lesson := memory.EscapedDefect{
		ID:        id,
		CreatedAt: time.Now().UTC(),
		Summary:   "reviewer missed edge case in loop",
		MissedBy:  []string{"reviewer"},
		Status:    memory.StatusProposed,
	}
	if err := memory.AppendLesson(memDir, lesson); err != nil {
		t.Fatalf("seed lesson: %v", err)
	}

	stdout, stderr, code := runLearnInDir(t, dir, "learn", "list")
	if code != 0 {
		t.Fatalf("expected exit 0, got %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, id) {
		t.Fatalf("expected lesson ID %q in output, got:\n%s", id, stdout)
	}
}

func TestLearn_ShowPrintsFullDetail(t *testing.T) {
	dir := t.TempDir()
	initCLITestRepo(t, dir)

	memDir := filepath.Join(dir, ".verk", "memory")
	id := fmt.Sprintf("learn-%d", time.Now().UnixNano())
	lesson := memory.EscapedDefect{
		ID:              id,
		CreatedAt:       time.Now().UTC(),
		Summary:         "planner review skipped edge case",
		MissedBy:        []string{"planner_review", "reviewer"},
		RecommendedRule: "always check nil before dereference",
		Status:          memory.StatusProposed,
	}
	if err := memory.AppendLesson(memDir, lesson); err != nil {
		t.Fatalf("seed lesson: %v", err)
	}

	stdout, stderr, code := runLearnInDir(t, dir, "learn", "show", id)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	checks := []string{
		lesson.Summary,
		"planner_review",
		"reviewer",
		"always check nil before dereference",
	}
	for _, want := range checks {
		if !strings.Contains(stdout, want) {
			t.Errorf("expected %q in output, got:\n%s", want, stdout)
		}
	}
}

func TestLearn_PromoteNotImplementedYet(t *testing.T) {
	dir := t.TempDir()
	initCLITestRepo(t, dir)

	_, _, code := runLearnInDir(t, dir,
		"learn", "promote", "learn-12345",
		"--target", ".agents/patterns/test.md",
		"--rule-id", "rule-001",
	)
	if code == 0 {
		t.Fatal("expected non-zero exit for promote (not implemented), got 0")
	}
}
