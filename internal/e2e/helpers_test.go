package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"verk/internal/adapters/ticketstore/epos"
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
	cmd.Env = testGitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
	}
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = testGitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func testGitEnv() []string {
	env := os.Environ()
	out := make([]string, 0, len(env)+6)
	for _, entry := range env {
		key, _, found := strings.Cut(entry, "=")
		if !found {
			out = append(out, entry)
			continue
		}
		if strings.HasPrefix(key, "GIT_CONFIG_KEY_") || strings.HasPrefix(key, "GIT_CONFIG_VALUE_") {
			continue
		}
		switch key {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_PREFIX", "GIT_SUPER_PREFIX", "GIT_OPTIONAL_LOCKS", "GIT_CONFIG", "GIT_CONFIG_COUNT", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_NOSYSTEM", "GIT_CONFIG_PARAMETERS":
			continue
		default:
			out = append(out, entry)
		}
	}
	out = append(out,
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=core.hooksPath",
		"GIT_CONFIG_VALUE_0="+os.DevNull,
	)
	return out
}

func saveTicket(t *testing.T, repoRoot string, ticket epos.Ticket) {
	t.Helper()
	if err := epos.SaveTicket(filepath.Join(repoRoot, ".tickets", ticket.ID+".md"), ticket); err != nil {
		t.Fatalf("SaveTicket(%s): %v", ticket.ID, err)
	}
}

func taskTicket(id string, status epos.Status, owned []string) epos.Ticket {
	return epos.Ticket{
		ID:                 id,
		Title:              "Ticket " + id,
		Status:             status,
		OwnedPaths:         append([]string(nil), owned...),
		AcceptanceCriteria: []string{"done"},
		ValidationCommands: []string{"true"},
		UnknownFrontmatter: map[string]any{"type": "task"},
	}
}

func epicTicket(id string, owned []string) epos.Ticket {
	return epos.Ticket{
		ID:         id,
		Title:      "Epic " + id,
		Status:     epos.StatusReady,
		OwnedPaths: append([]string(nil), owned...),
		UnknownFrontmatter: map[string]any{
			"type": "epic",
		},
	}
}

func epicChild(id, parent string, status epos.Status, owned []string) epos.Ticket {
	ticket := taskTicket(id, status, owned)
	ticket.UnknownFrontmatter["parent"] = parent
	return ticket
}

func testPlanAndClaim(t *testing.T, repoRoot string, cfg policy.Config, runID string, ticket epos.Ticket, leaseID string, commands []string) (state.PlanArtifact, state.ClaimArtifact) {
	t.Helper()
	plan, err := engine.BuildPlanArtifact(ticket, cfg)
	if err != nil {
		t.Fatalf("BuildPlanArtifact: %v", err)
	}
	plan.RunID = runID
	plan.ValidationCommands = append([]string(nil), commands...)
	claim, err := epos.AcquireClaim(repoRoot, runID, ticket.ID, leaseID, 10*time.Minute, time.Now().UTC())
	if err != nil {
		t.Fatalf("AcquireClaim: %v", err)
	}
	return plan, claim
}

func testTime() time.Time {
	return time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC)
}
