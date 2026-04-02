package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"verk/internal/adapters/ticketstore/tkmd"
	"verk/internal/state"
)

func TestStatusJSON_EmitsMachineReadableReport(t *testing.T) {
	repoRoot := t.TempDir()
	writeCLIRepo(t, repoRoot)
	runID := "run-status-json"
	ticketID := "ticket-1"

	writeJSONFixture(t, filepath.Join(repoRoot, ".verk", "runs", runID, "run.json"), state.RunArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		Mode:         "ticket",
		RootTicketID: ticketID,
		Status:       state.EpicRunStatusBlocked,
		CurrentPhase: state.TicketPhaseBlocked,
		TicketIDs:    []string{ticketID},
	})
	writeJSONFixture(t, filepath.Join(repoRoot, ".verk", "runs", runID, "tickets", ticketID, "plan.json"), state.PlanArtifact{
		ArtifactMeta:             state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:                 ticketID,
		EffectiveReviewThreshold: state.SeverityP1,
	})
	writeJSONFixture(t, filepath.Join(repoRoot, ".verk", "runs", runID, "tickets", ticketID, "ticket-run.json"), map[string]any{
		"schema_version":          1,
		"run_id":                  runID,
		"ticket_id":               ticketID,
		"current_phase":           "blocked",
		"block_reason":            "failed review",
		"implementation_attempts": 1,
		"verification_attempts":   1,
		"review_attempts":         1,
		"closeout": map[string]any{
			"schema_version": 1,
			"run_id":         runID,
			"ticket_id":      ticketID,
			"closable":       false,
			"failed_gate":    "review",
		},
	})

	stdout, stderr, code := runFromDir(t, repoRoot, "status", runID, "--json")
	if code != 0 {
		t.Fatalf("expected success, got code %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"run_id": "run-status-json"`) {
		t.Fatalf("expected run_id in JSON output, got %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"effective_review_threshold": "P1"`) {
		t.Fatalf("expected threshold in JSON output, got %s", stdout.String())
	}
	if strings.TrimSpace(stderr.String()) != "" {
		t.Fatalf("expected empty stderr, got %s", stderr.String())
	}
}

func TestDoctor_ExitCodes(t *testing.T) {
	repoRoot := t.TempDir()
	writeCLIRepo(t, repoRoot)
	if err := os.RemoveAll(filepath.Join(repoRoot, ".tickets")); err != nil {
		t.Fatalf("remove .tickets: %v", err)
	}

	_, _, code := runFromDir(t, repoRoot, "doctor")
	if code != 2 {
		t.Fatalf("expected doctor blocking exit code 2 for invalid repo, got %d", code)
	}
}

func TestReopen_ValidatesTargetPhase(t *testing.T) {
	repoRoot := t.TempDir()
	writeCLIRepo(t, repoRoot)
	runID := "run-reopen-cli"
	ticketID := "ticket-1"

	writeJSONFixture(t, filepath.Join(repoRoot, ".verk", "runs", runID, "run.json"), state.RunArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		Mode:         "epic",
		RootTicketID: "epic-1",
		Status:       state.EpicRunStatusBlocked,
		CurrentPhase: state.TicketPhaseBlocked,
		TicketIDs:    []string{ticketID},
	})
	writeJSONFixture(t, filepath.Join(repoRoot, ".verk", "runs", runID, "tickets", ticketID, "plan.json"), state.PlanArtifact{
		ArtifactMeta:             state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:                 ticketID,
		EffectiveReviewThreshold: state.SeverityP2,
	})
	writeJSONFixture(t, filepath.Join(repoRoot, ".verk", "runs", runID, "tickets", ticketID, "ticket-run.json"), map[string]any{
		"schema_version":          1,
		"run_id":                  runID,
		"ticket_id":               ticketID,
		"current_phase":           "blocked",
		"implementation_attempts": 1,
		"verification_attempts":   1,
		"review_attempts":         1,
	})
	if err := tkmd.SaveTicket(filepath.Join(repoRoot, ".tickets", ticketID+".md"), tkmd.Ticket{
		ID:                 ticketID,
		Title:              "Blocked ticket",
		Status:             tkmd.StatusBlocked,
		OwnedPaths:         []string{"internal/app"},
		UnknownFrontmatter: map[string]any{"type": "task"},
	}); err != nil {
		t.Fatalf("SaveTicket: %v", err)
	}

	_, stderr, code := runFromDir(t, repoRoot, "reopen", runID, ticketID, "--to", "closed")
	if code != 1 {
		t.Fatalf("expected validation failure exit code 1, got %d", code)
	}
	if !strings.Contains(stderr.String(), "not allowed") {
		t.Fatalf("expected validation error, got %s", stderr.String())
	}
}

func runFromDir(t *testing.T, dir string, args ...string) (*bytes.Buffer, *bytes.Buffer, int) {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir(%s): %v", dir, err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(cwd)
	})
	var stdout, stderr bytes.Buffer
	code := execute(args, &stdout, &stderr)
	return &stdout, &stderr, code
}

func writeCLIRepo(t *testing.T, repoRoot string) {
	t.Helper()
	runGit(t, repoRoot, "init")
	runGit(t, repoRoot, "config", "user.email", "test@example.com")
	runGit(t, repoRoot, "config", "user.name", "Test User")
	if err := os.MkdirAll(filepath.Join(repoRoot, ".tickets", ".claims"), 0o755); err != nil {
		t.Fatalf("mkdir .tickets: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, ".verk", "runs"), 0o755); err != nil {
		t.Fatalf("mkdir .verk: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "README.md"), []byte("verk\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runGit(t, repoRoot, "add", "README.md", ".tickets", ".verk")
	runGit(t, repoRoot, "commit", "-m", "init")
}

func writeJSONFixture(t *testing.T, path string, payload any) {
	t.Helper()
	switch v := payload.(type) {
	case state.RunArtifact:
		if v.CreatedAt.IsZero() {
			v.CreatedAt = time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC)
			v.UpdatedAt = v.CreatedAt
		}
		payload = v
	case state.PlanArtifact:
		if v.CreatedAt.IsZero() {
			v.CreatedAt = time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC)
			v.UpdatedAt = v.CreatedAt
		}
		payload = v
	}
	if err := state.SaveJSONAtomic(path, payload); err != nil {
		t.Fatalf("SaveJSONAtomic(%s): %v", path, err)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
	}
}
