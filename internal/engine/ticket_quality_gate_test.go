package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
	"verk/internal/adapters/runtime"
	"verk/internal/adapters/ticketstore/epos"
	"verk/internal/policy"
	"verk/internal/state"

	runtimefake "verk/internal/adapters/runtime/fake"
)

// qualityChildTicket builds a child task ticket with acceptance criteria so it
// passes the quality gate. Use clearAC() to strip them when you want a failing
// ticket.
func qualityChildTicket(id, parent string) epos.Ticket {
	return epos.Ticket{
		ID:     id,
		Title:  "Child " + id,
		Status: epos.StatusReady,
		OwnedPaths: []string{
			"internal/app/" + id,
		},
		AcceptanceCriteria: []string{
			id + " exits with status 0 on success",
		},
		UnknownFrontmatter: map[string]any{
			"parent": parent,
			"type":   "task",
		},
	}
}

// qualityEpicTicket returns an epic ticket that has owned paths (passes the
// missing-owned-paths check) and is typed as "epic".
func qualityEpicTicket(id string) epos.Ticket {
	return epos.Ticket{
		ID:     id,
		Title:  "Epic " + id,
		Status: epos.StatusReady,
		OwnedPaths: []string{
			"internal/app",
		},
		UnknownFrontmatter: map[string]any{
			"type": "epic",
		},
	}
}

// loadQualityArtifact reads ticket-quality.json for the given run and returns
// the parsed artifact.
func loadQualityArtifact(t *testing.T, repoRoot, runID string) state.TicketQualityArtifact {
	t.Helper()
	path := filepath.Join(repoRoot, ".verk", "runs", runID, "ticket-quality.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read ticket-quality.json: %v", err)
	}
	var artifact state.TicketQualityArtifact
	if err := json.Unmarshal(data, &artifact); err != nil {
		t.Fatalf("unmarshal ticket-quality.json: %v", err)
	}
	return artifact
}

// TestRunEpic_BlocksBeforeFirstWaveWhenTicketQualityFails ensures that when a
// child ticket is missing acceptance criteria the epic run returns a blocked
// error without invoking any workers.
func TestRunEpic_BlocksBeforeFirstWaveWhenTicketQualityFails(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	cfg := policy.DefaultConfig()
	runID := "run-quality-blocks"

	epic := qualityEpicTicket("epic-quality-blocks")
	mustSaveTicket(t, repoRoot, epic)

	// Child ticket has NO acceptance criteria — this triggers a P1 finding that
	// meets the P2 block threshold.
	child := epos.Ticket{
		ID:     "ticket-no-ac",
		Title:  "Child without acceptance criteria",
		Status: epos.StatusReady,
		OwnedPaths: []string{
			"internal/app/no-ac",
		},
		UnknownFrontmatter: map[string]any{
			"parent": epic.ID,
			"type":   "task",
		},
	}
	mustSaveTicket(t, repoRoot, child)

	adapter := runtimefake.New(nil, nil)

	_, err := RunEpic(context.Background(), RunEpicRequest{
		RepoRoot:     repoRoot,
		RunID:        runID,
		RootTicketID: epic.ID,
		BaseCommit:   baseCommit,
		Adapter:      adapter,
		Config:       cfg,
	})

	if err == nil {
		t.Fatal("expected error when ticket quality gate blocks, got nil")
	}
	if !errors.Is(err, ErrEpicBlocked) {
		t.Fatalf("expected ErrEpicBlocked, got: %v", err)
	}

	// No workers should have been invoked — the gate runs before any dispatch.
	if reqs := adapter.WorkerRequests(); len(reqs) != 0 {
		t.Fatalf("expected no worker invocations when gate blocks, got %d", len(reqs))
	}
}

// TestRunEpic_PersistsTicketQualityArtifact verifies that ticket-quality.json
// is written for both the blocked and the passed case, and that it contains the
// expected ticket IDs.
func TestRunEpic_PersistsTicketQualityArtifact(t *testing.T) {
	t.Run("blocked case", func(t *testing.T) {
		repoRoot := t.TempDir()
		baseCommit := initEpicRepo(t, repoRoot)
		runID := "run-quality-artifact-blocked"

		epic := qualityEpicTicket("epic-artifact-blocked")
		mustSaveTicket(t, repoRoot, epic)

		child := epos.Ticket{
			ID:         "ticket-missing-ac",
			Title:      "No AC",
			Status:     epos.StatusReady,
			OwnedPaths: []string{"internal/app/x"},
			UnknownFrontmatter: map[string]any{
				"parent": epic.ID,
				"type":   "task",
			},
		}
		mustSaveTicket(t, repoRoot, child)

		_, _ = RunEpic(context.Background(), RunEpicRequest{
			RepoRoot:     repoRoot,
			RunID:        runID,
			RootTicketID: epic.ID,
			BaseCommit:   baseCommit,
			Adapter:      runtimefake.New(nil, nil),
			Config:       policy.DefaultConfig(),
		})

		artifact := loadQualityArtifact(t, repoRoot, runID)

		if artifact.Status != state.TicketQualityBlocked {
			t.Errorf("expected blocked status in artifact, got %q", artifact.Status)
		}
		if !artifact.Blocked {
			t.Error("expected artifact.Blocked == true")
		}
		wantIDs := []string{child.ID, epic.ID}
		gotSet := make(map[string]bool, len(artifact.TicketIDs))
		for _, id := range artifact.TicketIDs {
			gotSet[id] = true
		}
		for _, id := range wantIDs {
			if !gotSet[id] {
				t.Errorf("expected ticket ID %q in artifact.TicketIDs, got %v", id, artifact.TicketIDs)
			}
		}
	})

	t.Run("passed case", func(t *testing.T) {
		repoRoot := t.TempDir()
		baseCommit := initEpicRepo(t, repoRoot)
		runID := "run-quality-artifact-passed"

		epic := qualityEpicTicket("epic-artifact-passed")
		mustSaveTicket(t, repoRoot, epic)

		child := qualityChildTicket("ticket-clean", epic.ID)
		mustSaveTicket(t, repoRoot, child)

		start := epicTestStart()
		adapter := runtimefake.New(
			[]runtime.WorkerResult{{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            "lease-" + runID + "-" + child.ID + "-wave-1",
				StartedAt:          start,
				FinishedAt:         start.Add(time.Second),
				ResultArtifactPath: "artifact.json",
			}},
			[]runtime.ReviewResult{
				{
					Status:             runtime.WorkerStatusDone,
					RetryClass:         runtime.RetryClassTerminal,
					LeaseID:            "lease-" + runID + "-" + child.ID + "-wave-1",
					StartedAt:          start.Add(2 * time.Second),
					FinishedAt:         start.Add(3 * time.Second),
					ReviewStatus:       runtime.ReviewStatusPassed,
					Summary:            "clean",
					ResultArtifactPath: "review.json",
				},
				// epic closure gate reviewer slot
				{
					Status:             runtime.WorkerStatusDone,
					RetryClass:         runtime.RetryClassTerminal,
					LeaseID:            "lease-" + runID + "-epic-gate",
					StartedAt:          start.Add(4 * time.Second),
					FinishedAt:         start.Add(5 * time.Second),
					ReviewStatus:       runtime.ReviewStatusPassed,
					Summary:            "epic gate: clean",
					ResultArtifactPath: "epic-review.json",
				},
			},
		)

		_, _ = RunEpic(context.Background(), RunEpicRequest{
			RepoRoot:     repoRoot,
			RunID:        runID,
			RootTicketID: epic.ID,
			BaseCommit:   baseCommit,
			Adapter:      adapter,
			Config:       policy.DefaultConfig(),
		})

		// artifact must exist regardless of whether the run succeeded
		artifact := loadQualityArtifact(t, repoRoot, runID)
		if artifact.RootTicketID != epic.ID {
			t.Errorf("expected root_ticket_id=%q, got %q", epic.ID, artifact.RootTicketID)
		}
		found := false
		for _, id := range artifact.TicketIDs {
			if id == child.ID {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("child ticket %q not found in artifact.TicketIDs %v", child.ID, artifact.TicketIDs)
		}
	})
}

// TestRunEpic_PassesGateWhenAllTicketsAreClean is the happy path: all tickets
// have sufficient quality, gate passes, artifact is persisted with
// status=passed, and at least one worker is invoked.
func TestRunEpic_PassesGateWhenAllTicketsAreClean(t *testing.T) {
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	runID := "run-quality-pass"

	epic := qualityEpicTicket("epic-quality-pass")
	mustSaveTicket(t, repoRoot, epic)

	child := qualityChildTicket("ticket-quality-pass-a", epic.ID)
	mustSaveTicket(t, repoRoot, child)

	start := epicTestStart()
	adapter := runtimefake.New(
		[]runtime.WorkerResult{{
			Status:             runtime.WorkerStatusDone,
			RetryClass:         runtime.RetryClassTerminal,
			LeaseID:            "lease-" + runID + "-" + child.ID + "-wave-1",
			StartedAt:          start,
			FinishedAt:         start.Add(time.Second),
			ResultArtifactPath: "artifact.json",
		}},
		[]runtime.ReviewResult{
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            "lease-" + runID + "-" + child.ID + "-wave-1",
				StartedAt:          start.Add(2 * time.Second),
				FinishedAt:         start.Add(3 * time.Second),
				ReviewStatus:       runtime.ReviewStatusPassed,
				Summary:            "clean",
				ResultArtifactPath: "review.json",
			},
			// epic closure gate reviewer slot
			{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            "lease-" + runID + "-epic-gate",
				StartedAt:          start.Add(4 * time.Second),
				FinishedAt:         start.Add(5 * time.Second),
				ReviewStatus:       runtime.ReviewStatusPassed,
				Summary:            "epic gate: clean",
				ResultArtifactPath: "epic-review.json",
			},
		},
	)

	_, err := RunEpic(context.Background(), RunEpicRequest{
		RepoRoot:     repoRoot,
		RunID:        runID,
		RootTicketID: epic.ID,
		BaseCommit:   baseCommit,
		Adapter:      adapter,
		Config:       policy.DefaultConfig(),
	})
	if err != nil {
		t.Fatalf("expected no error on clean epic, got: %v", err)
	}

	// Gate must have passed.
	artifact := loadQualityArtifact(t, repoRoot, runID)
	if artifact.Blocked {
		t.Errorf("expected artifact.Blocked==false on clean epic, findings: %+v", artifact.Findings)
	}
	if artifact.Status != state.TicketQualityPassed {
		t.Errorf("expected artifact.Status=passed, got %q", artifact.Status)
	}

	// Worker must have been invoked (gate did not block dispatch).
	if reqs := adapter.WorkerRequests(); len(reqs) == 0 {
		t.Error("expected at least one worker invocation after gate passes")
	}
}

// TestRunEpic_AppliesSafeTicketQualityRepairsBeforeDispatchWhenEnabled is a
// placeholder for when auto_fix_safe is wired into policy.Config. Currently the
// feature is hardcoded to false, so this test asserts that WITHOUT repairs the
// gate still blocks when a blocking finding exists. When the config field is
// added, this test should be updated to flip the flag and assert workers DID
// run after repairs resolved the finding.
func TestRunEpic_AppliesSafeTicketQualityRepairsBeforeDispatchWhenEnabled(t *testing.T) {
	// With auto_fix_safe hardcoded to false (no policy.Config field yet), the
	// gate blocks unconditionally when unresolvable findings exist. A child
	// ticket without acceptance criteria triggers a P1 blocking finding.
	repoRoot := t.TempDir()
	baseCommit := initEpicRepo(t, repoRoot)
	runID := "run-quality-autofix"

	epic := qualityEpicTicket("epic-quality-autofix")
	mustSaveTicket(t, repoRoot, epic)

	// Child with no acceptance criteria → P1 missing_acceptance_criteria finding
	// (not auto-repairable), so gate blocks regardless of auto_fix_safe.
	child := epos.Ticket{
		ID:         "ticket-autofix-no-ac",
		Title:      "Child without AC",
		Status:     epos.StatusReady,
		OwnedPaths: []string{"internal/app/autofix"},
		UnknownFrontmatter: map[string]any{
			"parent": epic.ID,
			"type":   "task",
		},
	}
	mustSaveTicket(t, repoRoot, child)

	adapter := runtimefake.New(nil, nil)

	_, err := RunEpic(context.Background(), RunEpicRequest{
		RepoRoot:     repoRoot,
		RunID:        runID,
		RootTicketID: epic.ID,
		BaseCommit:   baseCommit,
		Adapter:      adapter,
		Config:       policy.DefaultConfig(),
	})

	// Gate blocks: missing_acceptance_criteria is not auto-repairable, so even
	// if auto_fix_safe were true it would still block.
	if err == nil {
		t.Fatal("expected gate to block when child has no acceptance criteria")
	}
	if !errors.Is(err, ErrEpicBlocked) {
		t.Fatalf("expected ErrEpicBlocked, got: %v", err)
	}
	if reqs := adapter.WorkerRequests(); len(reqs) != 0 {
		t.Fatalf("expected no worker invocations when gate blocks, got %d", len(reqs))
	}
}
