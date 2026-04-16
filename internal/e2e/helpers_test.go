package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"verk/internal/adapters/ticketstore/tkmd"
	"verk/internal/engine"
	"verk/internal/policy"
	"verk/internal/state"
)

func initRepo(t *testing.T, root string) string {
	t.Helper()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "test@example.com")
	runGit(t, root, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	runGit(t, root, "add", "tracked.txt")
	runGit(t, root, "commit", "-m", "base")
	head, err := gitOutput(root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	return strings.TrimSpace(head)
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

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func saveTicket(t *testing.T, repoRoot string, ticket tkmd.Ticket) {
	t.Helper()
	if err := tkmd.SaveTicket(filepath.Join(repoRoot, ".tickets", ticket.ID+".md"), ticket); err != nil {
		t.Fatalf("SaveTicket(%s): %v", ticket.ID, err)
	}
}

func taskTicket(id string, status tkmd.Status, owned []string) tkmd.Ticket {
	return tkmd.Ticket{
		ID:                 id,
		Title:              "Ticket " + id,
		Status:             status,
		OwnedPaths:         append([]string(nil), owned...),
		AcceptanceCriteria: []string{"done"},
		ValidationCommands: []string{"true"},
		UnknownFrontmatter: map[string]any{"type": "task"},
	}
}

func epicTicket(id string, owned []string) tkmd.Ticket {
	return tkmd.Ticket{
		ID:         id,
		Title:      "Epic " + id,
		Status:     tkmd.StatusReady,
		OwnedPaths: append([]string(nil), owned...),
		UnknownFrontmatter: map[string]any{
			"type": "epic",
		},
	}
}

func epicChild(id, parent string, status tkmd.Status, owned []string) tkmd.Ticket {
	ticket := taskTicket(id, status, owned)
	ticket.UnknownFrontmatter["parent"] = parent
	return ticket
}

func testPlanAndClaim(t *testing.T, repoRoot string, cfg policy.Config, runID string, ticket tkmd.Ticket, leaseID string, commands []string) (state.PlanArtifact, state.ClaimArtifact) {
	t.Helper()
	plan, err := engine.BuildPlanArtifact(ticket, cfg)
	if err != nil {
		t.Fatalf("BuildPlanArtifact: %v", err)
	}
	plan.RunID = runID
	plan.ValidationCommands = append([]string(nil), commands...)
	claim, err := tkmd.AcquireClaim(repoRoot, runID, ticket.ID, leaseID, 10*time.Minute, testTime())
	if err != nil {
		t.Fatalf("AcquireClaim: %v", err)
	}
	return plan, claim
}

func testTime() time.Time {
	return time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC)
}
