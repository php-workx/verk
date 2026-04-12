package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"verk/internal/adapters/ticketstore/tkmd"
	"verk/internal/cli"
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

	stdout, stderr, code := runCLIFromDir(t, repoRoot, "status", runID, "--json")
	if code != 0 {
		t.Fatalf("expected success, got code %d stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"run_id": "run-status-json"`) {
		t.Fatalf("expected run_id in JSON output, got %s", stdout)
	}
	if !strings.Contains(stdout, `"effective_review_threshold": "P1"`) {
		t.Fatalf("expected threshold in JSON output, got %s", stdout)
	}
}

func TestDoctor_ExitCodes(t *testing.T) {
	repoRoot := t.TempDir()
	writeCLIRepo(t, repoRoot)
	if err := os.RemoveAll(filepath.Join(repoRoot, ".tickets")); err != nil {
		t.Fatalf("remove .tickets: %v", err)
	}

	_, _, code := runCLIFromDir(t, repoRoot, "doctor")
	if code == 0 {
		t.Fatalf("expected doctor non-zero exit for invalid repo, got 0")
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

	_, stderr, code := runCLIFromDir(t, repoRoot, "reopen", runID, ticketID, "--to", "closed")
	if code != 2 {
		t.Fatalf("expected validation failure exit code 2, got %d stderr=%s", code, stderr)
	}
	if !strings.Contains(stderr, "must be one of") {
		t.Fatalf("expected validation error, got %s", stderr)
	}
}

// runCLIFromDir runs the CLI with the given args from the specified directory,
// capturing stdout and stderr. Returns stdout, stderr, and exit code.
func runCLIFromDir(t *testing.T, dir string, args ...string) (string, string, int) {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir(%s): %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	stdoutR, stdoutW, _ := os.Pipe()
	stderrR, stderrW, _ := os.Pipe()

	code := cli.ExecuteArgs(args, stdoutW, stderrW)

	stdoutW.Close()
	stderrW.Close()

	stdoutBuf := make([]byte, 64*1024)
	n, _ := stdoutR.Read(stdoutBuf)
	stdoutR.Close()

	stderrBuf := make([]byte, 64*1024)
	m, _ := stderrR.Read(stderrBuf)
	stderrR.Close()

	return string(stdoutBuf[:n]), string(stderrBuf[:m]), code
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

func TestRunNoArgs_NoCurrentRun(t *testing.T) {
	repoRoot := t.TempDir()
	writeCLIRepo(t, repoRoot)

	stdout, _, code := runCLIFromDir(t, repoRoot, "run")
	if code == 0 {
		t.Fatalf("expected non-zero exit code, got 0")
	}
	if !strings.Contains(stdout, "no active run") {
		t.Fatalf("expected 'no active run' message, got: %s", stdout)
	}
}

func TestRunNoArgs_CompletedRun(t *testing.T) {
	repoRoot := t.TempDir()
	writeCLIRepo(t, repoRoot)
	runID := "run-completed"

	writeJSONFixture(t, filepath.Join(repoRoot, ".verk", "runs", runID, "run.json"), state.RunArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		Mode:         "ticket",
		RootTicketID: "ticket-1",
		Status:       state.EpicRunStatusCompleted,
		CurrentPhase: state.TicketPhaseClosed,
		TicketIDs:    []string{"ticket-1"},
	})

	// Write .verk/current so verk run finds it
	if err := os.WriteFile(filepath.Join(repoRoot, ".verk", "current"), []byte(runID+"\n"), 0o644); err != nil {
		t.Fatalf("write current: %v", err)
	}

	stdout, _, code := runCLIFromDir(t, repoRoot, "run")
	if code != 0 {
		t.Fatalf("expected exit code 0 for completed run info, got %d", code)
	}
	if !strings.Contains(stdout, "completed") {
		t.Fatalf("expected 'completed' message, got: %s", stdout)
	}
}

func TestRunNoArgs_BlockedRun(t *testing.T) {
	repoRoot := t.TempDir()
	writeCLIRepo(t, repoRoot)
	runID := "run-blocked"

	writeJSONFixture(t, filepath.Join(repoRoot, ".verk", "runs", runID, "run.json"), state.RunArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		Mode:         "ticket",
		RootTicketID: "ticket-1",
		Status:       state.EpicRunStatusBlocked,
		CurrentPhase: state.TicketPhaseBlocked,
		TicketIDs:    []string{"ticket-1"},
	})

	if err := os.WriteFile(filepath.Join(repoRoot, ".verk", "current"), []byte(runID+"\n"), 0o644); err != nil {
		t.Fatalf("write current: %v", err)
	}

	stdout, _, code := runCLIFromDir(t, repoRoot, "run")
	if code != 0 {
		t.Fatalf("expected exit code 0 for blocked run info, got %d", code)
	}
	if !strings.Contains(stdout, "blocked") {
		t.Fatalf("expected 'blocked' message, got: %s", stdout)
	}
	if !strings.Contains(stdout, "reopen") {
		t.Fatalf("expected reopen guidance, got: %s", stdout)
	}
}
