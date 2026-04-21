package engine

import (
	"os"
	"path/filepath"
	"testing"
	"verk/internal/adapters/ticketstore/tkmd"
	"verk/internal/state"
)

func TestMostRestrictiveThreshold(t *testing.T) {
	tests := []struct {
		name string
		a    state.Severity
		b    state.Severity
		want state.Severity
	}{
		{"strict beats lenient", "strict", "lenient", "strict"},
		{"strict beats standard", "strict", "standard", "strict"},
		{"standard beats lenient", "standard", "lenient", "standard"},
		{"lenient beats empty", "lenient", "", "lenient"},
		{"empty loses to standard", "", "standard", "standard"},
		{"order independent: strict first", "strict", "lenient", "strict"},
		{"order independent: lenient first", "lenient", "strict", "strict"},
		{"equal values return either", "standard", "standard", "standard"},
		{"P0 is strict equivalent", state.SeverityP0, "lenient", state.SeverityP0},
		{"P1 is standard equivalent", state.SeverityP1, "lenient", state.SeverityP1},
		{"P2 is lenient equivalent", state.SeverityP2, "", state.SeverityP2},
		{"P0 beats P1", state.SeverityP0, state.SeverityP1, state.SeverityP0},
		{"P1 beats P2", state.SeverityP1, state.SeverityP2, state.SeverityP1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mostRestrictiveThreshold(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("mostRestrictiveThreshold(%q, %q) = %q, want %q", tt.a, tt.b, got, tt.want)
			}
			// Also verify reverse order produces the same result.
			rev := mostRestrictiveThreshold(tt.b, tt.a)
			if rev != got {
				t.Errorf("mostRestrictiveThreshold(%q, %q) = %q, but reverse gave %q — must be order independent", tt.b, tt.a, rev, got)
			}
		})
	}
}

func TestDeriveStatus_UsesRunArtifactsAndClaimsOnly(t *testing.T) {
	repoRoot := t.TempDir()
	runID := "run-status"
	ticketID := "ticket-1"

	writeOpRunFixture(t, repoRoot, runID, state.RunArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		Mode:         "ticket",
		RootTicketID: ticketID,
		Status:       state.EpicRunStatusBlocked,
		CurrentPhase: state.TicketPhaseBlocked,
		TicketIDs:    []string{ticketID},
	})
	writePlanFixture(t, repoRoot, runID, state.PlanArtifact{
		ArtifactMeta:             state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:                 ticketID,
		EffectiveReviewThreshold: state.SeverityP1,
	})
	writeTicketRunFixture(t, repoRoot, runID, TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:     ticketID,
		CurrentPhase: state.TicketPhaseBlocked,
		BlockReason:  "failed review",
		Closeout: &state.CloseoutArtifact{
			ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
			TicketID:     ticketID,
			Closable:     false,
			FailedGate:   "review",
		},
	})
	writeTicketMarkdownFixture(t, repoRoot, tkmd.Ticket{
		ID:                 ticketID,
		Title:              "Contradictory ticket",
		Status:             tkmd.StatusOpen,
		OwnedPaths:         []string{"docs"},
		UnknownFrontmatter: map[string]any{"type": "task"},
	})
	released := state.ClaimArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:     ticketID,
		OwnerRunID:   runID,
		LeaseID:      "lease-1",
		State:        "released",
	}
	if err := state.SaveJSONAtomic(durableClaimPath(repoRoot, runID, ticketID), released); err != nil {
		t.Fatalf("save durable claim: %v", err)
	}

	report, err := DeriveStatus(StatusRequest{RepoRoot: repoRoot, RunID: runID})
	if err != nil {
		t.Fatalf("DeriveStatus returned error: %v", err)
	}
	if report.RunStatus != state.EpicRunStatusBlocked {
		t.Fatalf("expected blocked run status, got %q", report.RunStatus)
	}
	if report.EffectiveReviewThreshold != state.SeverityP1 {
		t.Fatalf("expected effective threshold P1, got %q", report.EffectiveReviewThreshold)
	}
	if report.LastFailedGate != "review" {
		t.Fatalf("expected review failed gate, got %q", report.LastFailedGate)
	}
	if len(report.Tickets) != 1 || report.Tickets[0].Phase != state.TicketPhaseBlocked {
		t.Fatalf("expected blocked ticket phase from artifacts, got %#v", report.Tickets)
	}
	if report.ClaimDivergence {
		t.Fatal("expected no claim divergence")
	}
}

func TestDeriveStatus_IncludesFailureSummaries(t *testing.T) {
	repoRoot := t.TempDir()
	runID := "run-status-failures"
	ticketID := "ticket-1"

	writeOpRunFixture(t, repoRoot, runID, state.RunArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		Mode:         "epic",
		RootTicketID: "epic-1",
		Status:       state.EpicRunStatusBlocked,
		CurrentPhase: state.TicketPhaseBlocked,
		TicketIDs:    []string{ticketID},
	})
	writePlanFixture(t, repoRoot, runID, state.PlanArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:     ticketID,
	})
	writeTicketRunFixture(t, repoRoot, runID, TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:     ticketID,
		CurrentPhase: state.TicketPhaseBlocked,
		BlockReason:  "worker blocked by operator input: {\"type\":\"thread.started\"}",
	})
	verification := state.VerificationArtifact{
		ArtifactMeta: state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:     ticketID,
		Passed:       false,
		Results: []state.VerificationResult{{
			Command:    "just lint-check",
			ExitCode:   1,
			Passed:     false,
			StdoutPath: filepath.Join(repoRoot, ".verk", "verification", "command.stdout.log"),
			StderrPath: filepath.Join(repoRoot, ".verk", "verification", "command.stderr.log"),
		}},
	}
	if err := os.MkdirAll(filepath.Join(repoRoot, ".verk", "verification"), 0o755); err != nil {
		t.Fatalf("create verification log dir: %v", err)
	}
	if err := os.WriteFile(verification.Results[0].StdoutPath, []byte("internal/policy/config.go:176:23: unnecessary conversion (unconvert)\n"), 0o644); err != nil {
		t.Fatalf("write stdout log: %v", err)
	}
	if err := state.SaveJSONAtomic(verificationArtifactPath(repoRoot, runID, ticketID), verification); err != nil {
		t.Fatalf("save verification fixture: %v", err)
	}
	repair := state.RepairCycleArtifact{
		ArtifactMeta:         state.ArtifactMeta{SchemaVersion: 1, RunID: runID},
		TicketID:             ticketID,
		Cycle:                1,
		Status:               "repair_pending",
		TriggerCheckIDs:      []string{"check-1"},
		RepairNotes:          "repair cycle 1 triggered by failing checks: check-1",
		VerificationArtifact: verificationArtifactPath(repoRoot, runID, ticketID),
	}
	if err := state.SaveJSONAtomic(filepath.Join(ticketDir(repoRoot, runID, ticketID), "cycles", "repair-1.json"), repair); err != nil {
		t.Fatalf("save repair cycle fixture: %v", err)
	}

	report, err := DeriveStatus(StatusRequest{RepoRoot: repoRoot, RunID: runID})
	if err != nil {
		t.Fatalf("DeriveStatus returned error: %v", err)
	}
	if len(report.Tickets) != 1 {
		t.Fatalf("expected one ticket, got %#v", report.Tickets)
	}
	failures := report.Tickets[0].Failures
	if len(failures) != 2 {
		t.Fatalf("expected verification and repair failures, got %#v", failures)
	}
	if failures[0].Kind != "verification" || failures[0].Command != "just lint-check" {
		t.Fatalf("expected lint-check verification failure first, got %#v", failures[0])
	}
	if failures[0].ExitCode != 1 || failures[0].StdoutPath == "" || failures[0].StderrPath == "" {
		t.Fatalf("expected verification failure details, got %#v", failures[0])
	}
	if failures[0].Detail != "internal/policy/config.go:176:23: unnecessary conversion (unconvert)" {
		t.Fatalf("expected concise failure detail from log, got %#v", failures[0])
	}
	if failures[1].Kind != "repair" || failures[1].Summary == "" {
		t.Fatalf("expected repair failure summary, got %#v", failures[1])
	}
}

func TestTicketStatusReason_UsesFailuresForRuntimeEventStream(t *testing.T) {
	snapshot := TicketRunSnapshot{
		CurrentPhase: state.TicketPhaseBlocked,
		BlockReason:  "worker blocked by operator input: {\"type\":\"thread.started\"}",
		Verification: &state.VerificationArtifact{
			Passed: false,
			Results: []state.VerificationResult{{
				Command:  "just lint-check",
				ExitCode: 1,
			}},
		},
	}

	got := ticketStatusReason(snapshot)
	want := "verification: just lint-check failed with exit code 1"
	if got != want {
		t.Fatalf("expected concise verification reason %q, got %q", want, got)
	}
}
