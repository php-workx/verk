package e2e

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"verk/internal/adapters/ticketstore/epos"
	"verk/internal/engine"
	"verk/internal/policy"

	runtimefake "verk/internal/adapters/runtime/fake"
)

// TestE2E_TicketQualityGate_CLITicketMissingScenarioBlocks verifies that an
// epic with a CLI-surface child ticket (owned_paths contains "cmd/") that lacks
// a concrete command invocation in its acceptance criteria is blocked by the
// quality gate before any worker is dispatched.
func TestE2E_TicketQualityGate_CLITicketMissingScenarioBlocks(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initRepo(t, repoRoot)
	cfg := policy.DefaultConfig()

	root := epicTicket("epic-cli-gate", []string{"cmd/verk", "internal/cli"})
	saveTicket(t, repoRoot, root)

	// CLI ticket: owned_paths has cmd/, but acceptance criteria contain no
	// concrete invocation (no flags, no exit codes, no stdout/stderr).
	cliChild := epos.Ticket{
		ID:     "ticket-cli-no-scenario",
		Title:  "Add CLI subcommand",
		Status: epos.StatusReady,
		OwnedPaths: []string{
			"cmd/verk",
		},
		AcceptanceCriteria: []string{
			"The command runs successfully",
			"Output is correct",
		},
		ValidationCommands: []string{"true"},
		UnknownFrontmatter: map[string]any{
			"parent": root.ID,
			"type":   "task",
		},
	}
	saveTicket(t, repoRoot, cliChild)

	// Adapter with no results — the gate should block before any are needed.
	adapter := runtimefake.New(nil, nil)

	result, err := engine.RunEpic(context.Background(), engine.RunEpicRequest{
		RepoRoot:     repoRoot,
		RunID:        "run-cli-gate",
		RootTicketID: root.ID,
		BaseCommit:   baseCommit,
		Adapter:      adapter,
		Config:       cfg,
	})

	if !errors.Is(err, engine.ErrEpicBlocked) {
		t.Fatalf("expected ErrEpicBlocked, got: %v", err)
	}

	// No workers should have been invoked — gate runs before any dispatch.
	if reqs := adapter.WorkerRequests(); len(reqs) != 0 {
		t.Fatalf("expected no worker invocations before gate, got %d", len(reqs))
	}

	// Run artifact should reflect blocked status.
	if result.Run.Status != "blocked" {
		t.Fatalf("expected blocked run status, got %q", result.Run.Status)
	}

	// ticket-quality.json should exist in the run dir.
	artifactPath := filepath.Join(repoRoot, ".verk", "runs", "run-cli-gate", "ticket-quality.json")
	if _, statErr := os.Stat(artifactPath); statErr != nil {
		t.Fatalf("expected ticket-quality.json artifact, stat failed: %v", statErr)
	}
}

// TestE2E_TicketQualityGate_MultiSurfaceEpicWithoutIntegrationBlocks verifies
// that an epic with CLI and docs children but no integration ticket is blocked
// by the quality gate before any worker is dispatched.
func TestE2E_TicketQualityGate_MultiSurfaceEpicWithoutIntegrationBlocks(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initRepo(t, repoRoot)
	cfg := policy.DefaultConfig()

	root := epicTicket("epic-multisurface", []string{"cmd/verk", "internal/cli", "docs"})
	saveTicket(t, repoRoot, root)

	// CLI child — triggers the "cli" surface.
	cliChild := epos.Ticket{
		ID:     "ticket-cli",
		Title:  "CLI subcommand",
		Status: epos.StatusReady,
		OwnedPaths: []string{
			"internal/cli",
		},
		AcceptanceCriteria: []string{
			"verk inspect ticket <id> exits 0 and prints findings",
		},
		ValidationCommands: []string{"true"},
		UnknownFrontmatter: map[string]any{
			"parent": root.ID,
			"type":   "task",
		},
	}
	saveTicket(t, repoRoot, cliChild)

	// Docs child — triggers the "docs" surface. No integration child present.
	docsChild := epos.Ticket{
		ID:     "ticket-docs",
		Title:  "Document the feature",
		Status: epos.StatusReady,
		OwnedPaths: []string{
			"docs/ticket-quality-gate.md",
		},
		AcceptanceCriteria: []string{
			"docs/ticket-quality-gate.md exists and describes all finding codes",
		},
		ValidationCommands: []string{"true"},
		UnknownFrontmatter: map[string]any{
			"parent": root.ID,
			"type":   "docs",
		},
	}
	saveTicket(t, repoRoot, docsChild)

	adapter := runtimefake.New(nil, nil)

	result, err := engine.RunEpic(context.Background(), engine.RunEpicRequest{
		RepoRoot:     repoRoot,
		RunID:        "run-multisurface",
		RootTicketID: root.ID,
		BaseCommit:   baseCommit,
		Adapter:      adapter,
		Config:       cfg,
	})

	if !errors.Is(err, engine.ErrEpicBlocked) {
		t.Fatalf("expected ErrEpicBlocked for multi-surface epic without integration child, got: %v", err)
	}

	if reqs := adapter.WorkerRequests(); len(reqs) != 0 {
		t.Fatalf("expected no worker invocations before gate, got %d", len(reqs))
	}

	if result.Run.Status != "blocked" {
		t.Fatalf("expected blocked run status, got %q", result.Run.Status)
	}
}

// TestE2E_TicketQualityGate_InspectEpicFixAppliesSafeRepairs verifies that
// running the inspect + fix path on an epic whose owned_paths is empty but
// whose children all have non-empty owned_paths updates the ticket file with
// the union, and that the re-evaluated artifact still reports any remaining
// (unresolvable) findings.
func TestE2E_TicketQualityGate_InspectEpicFixAppliesSafeRepairs(t *testing.T) {
	repoRoot := t.TempDir()
	_ = initRepo(t, repoRoot)
	cfg := policy.DefaultConfig()

	// Epic with no owned_paths — this triggers the missing_owned_paths safe repair.
	root := epos.Ticket{
		ID:     "epic-missing-paths",
		Title:  "Epic missing owned paths",
		Status: epos.StatusReady,
		// OwnedPaths intentionally empty
		UnknownFrontmatter: map[string]any{
			"type": "epic",
		},
	}
	saveTicket(t, repoRoot, root)

	// Children each have non-empty owned_paths, so the union is computable.
	child1 := epos.Ticket{
		ID:     "ticket-child1",
		Title:  "Child one",
		Status: epos.StatusReady,
		OwnedPaths: []string{
			"internal/engine",
		},
		AcceptanceCriteria: []string{
			"internal/engine passes go test ./internal/engine/...",
		},
		ValidationCommands: []string{"true"},
		UnknownFrontmatter: map[string]any{
			"parent": root.ID,
			"type":   "task",
		},
	}
	saveTicket(t, repoRoot, child1)

	child2 := epos.Ticket{
		ID:     "ticket-child2",
		Title:  "Child two",
		Status: epos.StatusReady,
		OwnedPaths: []string{
			"internal/state",
		},
		AcceptanceCriteria: []string{
			"internal/state passes go test ./internal/state/...",
		},
		ValidationCommands: []string{"true"},
		UnknownFrontmatter: map[string]any{
			"parent": root.ID,
			"type":   "task",
		},
	}
	saveTicket(t, repoRoot, child2)

	// Run evaluate → plan → apply repairs (mirroring what `verk inspect epic --fix` does).
	children := []epos.Ticket{child1, child2}
	tickets := append([]epos.Ticket{root}, children...)
	input := engine.TicketQualityInput{
		RootTicket: root,
		Tickets:    tickets,
		Config:     cfg,
	}
	artifact := engine.EvaluateTicketQuality(input)

	plan := engine.BuildTicketQualityRepairPlan(input, artifact)
	for id, repaired := range plan.Tickets {
		path := filepath.Join(repoRoot, ".tickets", id+".md")
		if err := epos.SaveTicket(path, repaired); err != nil {
			t.Fatalf("save repaired ticket %s: %v", id, err)
		}
	}

	// Assert that the epic ticket file was updated (repair was applied).
	if _, ok := plan.Tickets[root.ID]; !ok {
		t.Fatal("expected repair plan to include epic ticket (owned_paths union), but it was absent")
	}

	// Reload the epic from disk and confirm owned_paths was written.
	epicPath := filepath.Join(repoRoot, ".tickets", root.ID+".md")
	reloaded, err := epos.LoadTicket(epicPath)
	if err != nil {
		t.Fatalf("reload repaired epic: %v", err)
	}
	if len(reloaded.OwnedPaths) == 0 {
		t.Fatal("expected epic owned_paths to be set after repair, still empty")
	}
	// Union should include both children's paths.
	pathSet := make(map[string]bool, len(reloaded.OwnedPaths))
	for _, p := range reloaded.OwnedPaths {
		pathSet[p] = true
	}
	for _, want := range []string{"internal/engine", "internal/state"} {
		if !pathSet[want] {
			t.Errorf("expected %q in repaired epic owned_paths, got %v", want, reloaded.OwnedPaths)
		}
	}

	// Re-evaluate with the repaired tickets to check remaining findings.
	freshTickets := make([]epos.Ticket, 0, len(tickets))
	freshRoot := root
	for _, tk := range tickets {
		if repaired, ok := plan.Tickets[tk.ID]; ok {
			freshTickets = append(freshTickets, repaired)
			if tk.ID == root.ID {
				freshRoot = repaired
			}
		} else {
			freshTickets = append(freshTickets, tk)
		}
	}
	input.Tickets = freshTickets
	input.RootTicket = freshRoot
	postArtifact := engine.EvaluateTicketQuality(input)

	// The epic itself now has owned_paths, so missing_owned_paths for the epic
	// should no longer appear. Children are clean, so the overall artifact
	// should either pass or have only non-missing-owned-paths findings.
	for _, f := range postArtifact.Findings {
		if f.TicketID == root.ID && f.Code == "missing_owned_paths" {
			t.Errorf("post-repair artifact still has missing_owned_paths finding for epic: %+v", f)
		}
	}
}
