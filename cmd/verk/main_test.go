package main

import (
	"encoding/json"
	"io"
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

	type result struct{ data []byte }
	stdoutCh := make(chan result, 1)
	stderrCh := make(chan result, 1)

	go func() {
		data, _ := io.ReadAll(stdoutR)
		stdoutCh <- result{data}
	}()
	go func() {
		data, _ := io.ReadAll(stderrR)
		stderrCh <- result{data}
	}()

	code := cli.ExecuteArgs(args, stdoutW, stderrW)

	_ = stdoutW.Close()
	_ = stderrW.Close()

	stdoutRes := <-stdoutCh
	_ = stdoutR.Close()
	stderrRes := <-stderrCh
	_ = stderrR.Close()

	return string(stdoutRes.data), string(stderrRes.data), code
}

// TestRunCLIFromDir_LargeOutput_NoDeadlock verifies that the goroutine-based
// pipe draining in runCLIFromDir does not deadlock when more than 64KB is
// written to stdout or stderr before ExecuteArgs returns.
func TestRunCLIFromDir_LargeOutput_NoDeadlock(t *testing.T) {
	const size = 128 * 1024 // 128KB — well above the old 64KB fixed buffer

	// Directly exercise the goroutine+io.ReadAll mechanism used by runCLIFromDir.
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe (stdout): %v", err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe (stderr): %v", err)
	}

	type result struct{ data []byte }
	stdoutCh := make(chan result, 1)
	stderrCh := make(chan result, 1)

	go func() {
		data, _ := io.ReadAll(stdoutR)
		stdoutCh <- result{data}
	}()
	go func() {
		data, _ := io.ReadAll(stderrR)
		stderrCh <- result{data}
	}()

	// Write >64KB to both streams before closing.
	large := make([]byte, size)
	if _, err := stdoutW.Write(large); err != nil {
		t.Fatalf("stdoutW.Write: %v", err)
	}
	if _, err := stderrW.Write(large); err != nil {
		t.Fatalf("stderrW.Write: %v", err)
	}

	_ = stdoutW.Close()
	_ = stderrW.Close()

	stdoutRes := <-stdoutCh
	_ = stdoutR.Close()
	stderrRes := <-stderrCh
	_ = stderrR.Close()

	if got := len(stdoutRes.data); got != size {
		t.Errorf("stdout: expected %d bytes, got %d", size, got)
	}
	if got := len(stderrRes.data); got != size {
		t.Errorf("stderr: expected %d bytes, got %d", size, got)
	}
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
	// Blocked runs now fall through to resume — they attempt re-execution
	// which will fail without a runtime adapter, but the blocked message
	// and resume intent should be visible.
	if !strings.Contains(stdout, "blocked") {
		t.Fatalf("expected 'blocked' message, got: %s", stdout)
	}
	if !strings.Contains(stdout, "resum") {
		t.Fatalf("expected resume/Resuming message, got: %s", stdout)
	}
	_ = code // exit code depends on whether resume succeeds (adapter-dependent)
}

// TestRunTicket_AdapterFailure_ReleasesClaim verifies that a runtime adapter
// selection failure after claim acquisition releases the live claim so that
// retries are not blocked by a leaked claim (ver-m8d1 AC#3: adapter lookup failure).
func TestRunTicket_AdapterFailure_ReleasesClaim(t *testing.T) {
	repoRoot := t.TempDir()
	writeCLIRepo(t, repoRoot)

	ticketID := "ver-adapter-fail"
	if err := tkmd.SaveTicket(filepath.Join(repoRoot, ".tickets", ticketID+".md"), tkmd.Ticket{
		ID:                 ticketID,
		Title:              "Adapter failure ticket",
		Status:             tkmd.StatusOpen,
		OwnedPaths:         []string{"internal/app"},
		Runtime:            "unsupported_runtime_xyz",
		UnknownFrontmatter: map[string]any{"type": "task"},
	}); err != nil {
		t.Fatalf("SaveTicket: %v", err)
	}

	// Write a config that overrides default_runtime to also be unsupported,
	// ensuring normalizeRuntime picks the ticket's bogus value.
	cfgPath := filepath.Join(repoRoot, ".verk", "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("runtime:\n  default_runtime: \"\"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	stdout, _, code := runCLIFromDir(t, repoRoot, "run", "ticket", ticketID)
	if code == 0 {
		t.Fatalf("expected non-zero exit for unsupported runtime, got 0")
	}

	// Extract run ID from stdout (format: run_id=<id>)
	runID := extractRunID(t, stdout)
	assertCLIClaimReleased(t, repoRoot, runID, ticketID)

	// Verify re-acquisition is not blocked.
	_, err := tkmd.AcquireClaim(repoRoot, "run-retry-adapter", ticketID, "lease-retry-adapter", 10*time.Minute, time.Now().UTC())
	if err != nil {
		t.Fatalf("expected claim re-acquisition after adapter failure, got error: %v", err)
	}
}

// TestRunTicket_GitMetadataFailure_ReleasesClaim verifies that a git metadata
// lookup failure (HeadCommit) after claim acquisition releases the live claim
// (ver-m8d1 AC#3: git metadata failure).
func TestRunTicket_GitMetadataFailure_ReleasesClaim(t *testing.T) {
	repoRoot := t.TempDir()
	// Create a git repo with NO commits — HeadCommit will fail.
	runGit(t, repoRoot, "init")
	runGit(t, repoRoot, "config", "user.email", "test@example.com")
	runGit(t, repoRoot, "config", "user.name", "Test User")
	if err := os.MkdirAll(filepath.Join(repoRoot, ".tickets", ".claims"), 0o755); err != nil {
		t.Fatalf("mkdir .tickets: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, ".verk", "runs"), 0o755); err != nil {
		t.Fatalf("mkdir .verk: %v", err)
	}

	ticketID := "ver-git-fail"
	if err := tkmd.SaveTicket(filepath.Join(repoRoot, ".tickets", ticketID+".md"), tkmd.Ticket{
		ID:                 ticketID,
		Title:              "Git metadata failure ticket",
		Status:             tkmd.StatusOpen,
		OwnedPaths:         []string{"internal/app"},
		UnknownFrontmatter: map[string]any{"type": "task"},
	}); err != nil {
		t.Fatalf("SaveTicket: %v", err)
	}

	stdout, _, code := runCLIFromDir(t, repoRoot, "run", "ticket", ticketID)
	if code == 0 {
		t.Fatalf("expected non-zero exit for git metadata failure, got 0")
	}

	runID := extractRunID(t, stdout)
	if runID == "" {
		// If the failure happened before run_id was printed, the claim was
		// never acquired — no release needed. Verify no live claim exists.
		livePath := filepath.Join(repoRoot, ".tickets", ".claims", ticketID+".json")
		if _, err := os.Stat(livePath); err == nil {
			t.Fatalf("live claim file should not exist when run_id was not assigned")
		}
		return
	}
	assertCLIClaimReleased(t, repoRoot, runID, ticketID)

	// Verify re-acquisition is not blocked.
	_, err := tkmd.AcquireClaim(repoRoot, "run-retry-git", ticketID, "lease-retry-git", 10*time.Minute, time.Now().UTC())
	if err != nil {
		t.Fatalf("expected claim re-acquisition after git metadata failure, got error: %v", err)
	}
}

// extractRunID parses "run_id=<id>" from CLI stdout.
func extractRunID(t *testing.T, stdout string) string {
	t.Helper()
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, "run_id=") {
			return strings.TrimPrefix(line, "run_id=")
		}
	}
	return ""
}

// assertCLIClaimReleased verifies that the claim was released: the live claim
// file is removed and the durable claim has state "released".
func assertCLIClaimReleased(t *testing.T, repoRoot, runID, ticketID string) {
	t.Helper()
	// Live claim file should have been removed by release.
	livePath := filepath.Join(repoRoot, ".tickets", ".claims", ticketID+".json")
	if _, err := os.Stat(livePath); err == nil {
		t.Fatalf("expected live claim file to be removed, but it still exists: %s", livePath)
	}
	// Durable claim should be in released state.
	durablePath := filepath.Join(repoRoot, ".verk", "runs", runID, "claims", "claim-"+ticketID+".json")
	data, err := os.ReadFile(durablePath)
	if err != nil {
		t.Fatalf("read durable claim: %v", err)
	}
	var durable state.ClaimArtifact
	if err := json.Unmarshal(data, &durable); err != nil {
		t.Fatalf("decode durable claim: %v", err)
	}
	if durable.State != "released" {
		t.Fatalf("expected durable claim state 'released', got %q", durable.State)
	}
}
