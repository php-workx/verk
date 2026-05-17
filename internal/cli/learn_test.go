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

func TestLearn_PromoteAppendsToPromotedRules(t *testing.T) {
	dir := t.TempDir()
	initCLITestRepo(t, dir)

	memDir := filepath.Join(dir, ".verk", "memory")
	id := fmt.Sprintf("learn-%d", time.Now().UnixNano())
	lesson := memory.EscapedDefect{
		ID:        id,
		CreatedAt: time.Now().UTC(),
		Summary:   "reviewer missed nil check in handler",
		MissedBy:  []string{"reviewer"},
		Status:    memory.StatusProposed,
	}
	if err := memory.AppendLesson(memDir, lesson); err != nil {
		t.Fatalf("seed lesson: %v", err)
	}

	stdout, stderr, code := runLearnInDir(t, dir,
		"learn", "promote", id,
		"--target", "ticket-quality-rule",
		"--rule-id", "my-rule",
	)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "my-rule") {
		t.Fatalf("expected rule ID in stdout, got: %s", stdout)
	}

	// Verify promoted-rules.jsonl exists and contains a matching entry.
	data, err := os.ReadFile(filepath.Join(memDir, "promoted-rules.jsonl"))
	if err != nil {
		t.Fatalf("read promoted-rules.jsonl: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line in promoted-rules.jsonl, got %d", len(lines))
	}

	var entry memory.PromotionEntry
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatalf("unmarshal promotion entry: %v", err)
	}
	if entry.LessonID != id {
		t.Errorf("LessonID = %q, want %q", entry.LessonID, id)
	}
	if entry.RuleID != "my-rule" {
		t.Errorf("RuleID = %q, want %q", entry.RuleID, "my-rule")
	}
	if entry.Target != "ticket-quality-rule" {
		t.Errorf("Target = %q, want %q", entry.Target, "ticket-quality-rule")
	}
	if entry.Summary != lesson.Summary {
		t.Errorf("Summary = %q, want %q", entry.Summary, lesson.Summary)
	}
}

func TestLearn_PromoteMarksLessonStatusPromoted(t *testing.T) {
	dir := t.TempDir()
	initCLITestRepo(t, dir)

	memDir := filepath.Join(dir, ".verk", "memory")
	id := fmt.Sprintf("learn-%d", time.Now().UnixNano())
	lesson := memory.EscapedDefect{
		ID:        id,
		CreatedAt: time.Now().UTC(),
		Summary:   "planner skipped acceptance criteria",
		MissedBy:  []string{"planner_review"},
		Status:    memory.StatusProposed,
	}
	if err := memory.AppendLesson(memDir, lesson); err != nil {
		t.Fatalf("seed lesson: %v", err)
	}

	_, stderr, code := runLearnInDir(t, dir,
		"learn", "promote", id,
		"--target", "ticket-quality-rule",
		"--rule-id", "rule-ac-check",
	)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d\nstderr: %s", code, stderr)
	}

	// Verify via store that status is now promoted.
	got, found, err := memory.GetLesson(memDir, id)
	if err != nil {
		t.Fatalf("GetLesson: %v", err)
	}
	if !found {
		t.Fatal("lesson not found after promote")
	}
	if got.Status != memory.StatusPromoted {
		t.Errorf("Status = %q, want %q", got.Status, memory.StatusPromoted)
	}
}

func TestLearn_PromoteRequiresRuleID(t *testing.T) {
	dir := t.TempDir()
	initCLITestRepo(t, dir)

	memDir := filepath.Join(dir, ".verk", "memory")
	id := fmt.Sprintf("learn-%d", time.Now().UnixNano())
	lesson := memory.EscapedDefect{
		ID:        id,
		CreatedAt: time.Now().UTC(),
		Summary:   "some lesson",
		MissedBy:  []string{"reviewer"},
		Status:    memory.StatusProposed,
	}
	if err := memory.AppendLesson(memDir, lesson); err != nil {
		t.Fatalf("seed lesson: %v", err)
	}

	_, _, code := runLearnInDir(t, dir,
		"learn", "promote", id,
		"--target", "ticket-quality-rule",
		// intentionally omitting --rule-id
	)
	if code == 0 {
		t.Fatal("expected non-zero exit when --rule-id is omitted, got 0")
	}
}

func TestLearn_PromoteUnknownLessonFails(t *testing.T) {
	dir := t.TempDir()
	initCLITestRepo(t, dir)

	_, _, code := runLearnInDir(t, dir,
		"learn", "promote", "learn-does-not-exist",
		"--target", "ticket-quality-rule",
		"--rule-id", "rule-xyz",
	)
	if code == 0 {
		t.Fatal("expected non-zero exit for unknown lesson ID, got 0")
	}
}
