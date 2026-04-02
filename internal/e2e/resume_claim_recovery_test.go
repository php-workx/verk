package e2e

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"verk/internal/adapters/runtime"
	runtimefake "verk/internal/adapters/runtime/fake"
	"verk/internal/adapters/ticketstore/tkmd"
	"verk/internal/engine"
	"verk/internal/policy"
	"verk/internal/state"
)

func TestResumeBlocksOnLiveDurableClaimDivergence(t *testing.T) {
	repoRoot := t.TempDir()
	_ = initRepo(t, repoRoot)
	runID := "run-divergence"
	ticket := taskTicket("ticket-1", tkmd.StatusInProgress, []string{"internal/app"})
	saveTicket(t, repoRoot, ticket)
	plan, _ := engine.BuildPlanArtifact(ticket, policy.DefaultConfig())
	plan.RunID = runID
	if err := state.SaveJSONAtomic(filepath.Join(repoRoot, ".verk", "runs", runID, "run.json"), state.RunArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		Mode:         "ticket",
		RootTicketID: ticket.ID,
		Status:       state.EpicRunStatusRunning,
		CurrentPhase: state.TicketPhaseImplement,
		TicketIDs:    []string{ticket.ID},
	}); err != nil {
		t.Fatalf("save run.json: %v", err)
	}
	if err := state.SaveJSONAtomic(filepath.Join(repoRoot, ".verk", "runs", runID, "tickets", ticket.ID, "plan.json"), plan); err != nil {
		t.Fatalf("save plan: %v", err)
	}
	if err := state.SaveJSONAtomic(filepath.Join(repoRoot, ".verk", "runs", runID, "tickets", ticket.ID, "ticket-run.json"), map[string]any{
		"schema_version":          1,
		"run_id":                  runID,
		"ticket_id":               ticket.ID,
		"current_phase":           "implement",
		"implementation_attempts": 1,
		"verification_attempts":   0,
		"review_attempts":         0,
		"implementation": map[string]any{
			"schema_version": 1,
			"run_id":         runID,
			"ticket_id":      ticket.ID,
			"lease_id":       "lease-live",
		},
	}); err != nil {
		t.Fatalf("save ticket-run: %v", err)
	}
	live := state.ClaimArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:     ticket.ID,
		OwnerRunID:   runID,
		LeaseID:      "lease-live",
		LeasedAt:     testTime(),
		ExpiresAt:    testTime().Add(10 * time.Minute),
		State:        "active",
	}
	durable := live
	durable.LeaseID = "lease-durable"
	if err := state.SaveJSONAtomic(filepath.Join(repoRoot, ".tickets", ".claims", ticket.ID+".json"), live); err != nil {
		t.Fatalf("save live claim: %v", err)
	}
	if err := state.SaveJSONAtomic(filepath.Join(repoRoot, ".verk", "runs", runID, "claims", "claim-"+ticket.ID+".json"), durable); err != nil {
		t.Fatalf("save durable claim: %v", err)
	}

	report, err := engine.ResumeRun(context.Background(), engine.ResumeRequest{RepoRoot: repoRoot, RunID: runID})
	if err != nil {
		t.Fatalf("ResumeRun returned error: %v", err)
	}
	if report.Run.Status != state.EpicRunStatusBlocked {
		t.Fatalf("expected blocked run, got %q", report.Run.Status)
	}
	if !report.Status.ClaimDivergence {
		t.Fatal("expected claim divergence")
	}
}

func TestLateWorkerResultRejectedAfterReacquisition(t *testing.T) {
	repoRoot := t.TempDir()
	_ = initRepo(t, repoRoot)
	cfg := policy.DefaultConfig()
	ticket := taskTicket("ticket-late", "", []string{"internal/app"})
	saveTicket(t, repoRoot, ticket)
	plan, claim := testPlanAndClaim(t, repoRoot, cfg, "run-late", ticket, "lease-current", []string{"true"})

	adapter := runtimefake.New(
		[]runtime.WorkerResult{{
			Status:             runtime.WorkerStatusDone,
			RetryClass:         runtime.RetryClassRetryable,
			LeaseID:            "lease-stale",
			StartedAt:          testTime(),
			FinishedAt:         testTime().Add(time.Second),
			ResultArtifactPath: filepath.Join(repoRoot, "worker.json"),
		}},
		nil,
	)

	if _, err := engine.RunTicket(context.Background(), engine.RunTicketRequest{
		RepoRoot: repoRoot,
		RunID:    "run-late",
		Ticket:   ticket,
		Plan:     plan,
		Claim:    claim,
		Adapter:  adapter,
		Config:   cfg,
	}); err == nil || !strings.Contains(err.Error(), "lease fence mismatch") {
		t.Fatalf("expected stale lease rejection, got %v", err)
	}
}

func TestCopiedEvidenceFromPreviousRunFailsCloseout(t *testing.T) {
	ticket := taskTicket("ticket-evidence", "", []string{"internal/app"})
	cfg := policy.DefaultConfig()
	plan, err := engine.BuildPlanArtifact(ticket, cfg)
	if err != nil {
		t.Fatalf("BuildPlanArtifact: %v", err)
	}
	plan.RunID = "run-current"
	verification := state.VerificationArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: "run-current"},
		TicketID:     ticket.ID,
		Attempt:      1,
		Commands:     []string{"true"},
		Results: []state.VerificationResult{{
			Command:    "true",
			ExitCode:   0,
			Passed:     true,
			StartedAt:  testTime(),
			FinishedAt: testTime().Add(time.Second),
		}},
		Passed: true,
	}
	review := state.ReviewFindingsArtifact{
		ArtifactMeta:             state.ArtifactMeta{SchemaVersion: 1, RunID: "run-current"},
		TicketID:                 ticket.ID,
		Attempt:                  1,
		EffectiveReviewThreshold: state.SeverityP2,
		Passed:                   true,
	}
	evidence := []state.CriteriaEvidence{{
		CriterionID:   "criterion-1",
		CriterionText: "done",
		EvidenceType:  "verification",
		Source:        "verification.json",
		Summary:       "copied",
		RunID:         "run-previous",
		TicketID:      ticket.ID,
		Attempt:       1,
		ArtifactRef:   "verification.json#1",
	}}

	if _, err := engine.BuildCloseoutArtifact(ticket, plan, verification, review, evidence); err == nil {
		t.Fatal("expected copied evidence from previous run to fail closeout")
	}
}
