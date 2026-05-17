package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"verk/internal/adapters/ticketstore/epos"
	"verk/internal/engine"
	"verk/internal/state"
)

// writeTicketQualityArtifact writes a minimal TicketQualityArtifact as JSON to
// .verk/runs/<runID>/ticket-quality.json under repoRoot.
func writeTicketQualityArtifact(t *testing.T, repoRoot, runID string, qa state.TicketQualityArtifact) {
	t.Helper()
	dir := filepath.Join(repoRoot, ".verk", "runs", runID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir runs dir: %v", err)
	}
	data, err := json.MarshalIndent(qa, "", "  ")
	if err != nil {
		t.Fatalf("marshal artifact: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ticket-quality.json"), data, 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
}

// TestPrintTicketQualityBlock_IncludesFindingsAndArtifactPath verifies the
// core rendering contract: the output must contain the blocked header, each
// non-passing finding, the artifact path, and a retry hint with the root id.
func TestPrintTicketQualityBlock_IncludesFindingsAndArtifactPath(t *testing.T) {
	dir := t.TempDir()
	runID := "run-ver-test-123"

	qa := state.TicketQualityArtifact{
		RootTicketID: "ver-epic",
		TicketIDs:    []string{"ver-child-1", "ver-child-2"},
		Status:       state.TicketQualityBlocked,
		Blocked:      true,
		BlockReason:  "unresolvable findings",
		Findings: []state.TicketQualityFinding{
			{
				TicketID:    "ver-child-1",
				Code:        string(state.QualityCodeMissingPublicContractScenario),
				Severity:    "P1",
				Disposition: "blocked",
			},
			{
				TicketID:    "ver-child-2",
				Code:        string(state.QualityCodeAmbiguousAcceptanceCriterion),
				Severity:    "P2",
				Disposition: "blocked",
			},
			{
				TicketID:    "ver-child-1",
				Code:        "some_passing_check",
				Severity:    "P3",
				Disposition: "pass",
			},
		},
	}
	writeTicketQualityArtifact(t, dir, runID, qa)

	var buf bytes.Buffer
	printTicketQualityBlock(&buf, dir, runID)
	out := buf.String()

	wants := []string{
		"Ticket quality gate blocked before worker dispatch",
		"ver-child-1",
		string(state.QualityCodeMissingPublicContractScenario),
		"P1",
		"ver-child-2",
		string(state.QualityCodeAmbiguousAcceptanceCriterion),
		"P2",
		filepath.Join(".verk", "runs", runID, "ticket-quality.json"),
		"verk inspect epic ver-epic --fix",
	}
	for _, want := range wants {
		if !strings.Contains(out, want) {
			t.Errorf("expected output to contain %q, got:\n%s", want, out)
		}
	}

	// Passing findings must NOT appear in the output.
	if strings.Contains(out, "some_passing_check") {
		t.Errorf("passing finding must not appear in output, got:\n%s", out)
	}
}

// TestPrintTicketQualityBlock_MissingArtifactFallback verifies that when the
// artifact JSON is absent the function still emits the blocked header and a
// human-readable note instead of silently producing empty output.
func TestPrintTicketQualityBlock_MissingArtifactFallback(t *testing.T) {
	dir := t.TempDir()

	var buf bytes.Buffer
	printTicketQualityBlock(&buf, dir, "run-missing-9999")
	out := buf.String()

	if !strings.Contains(out, "Ticket quality gate blocked before worker dispatch") {
		t.Fatalf("expected blocked header in fallback output, got:\n%s", out)
	}
	if !strings.Contains(out, "artifact not available") {
		t.Fatalf("expected fallback note in output, got:\n%s", out)
	}
}

// TestHandleBlockedEpicRun_QualityGatePrintsFindings verifies that when the
// BlockedRunError carries a Cause containing the quality-gate marker,
// handleBlockedEpicRun writes the structured quality-gate output to stdout
// (not errw) and returns retried=false so the caller exits non-zero.
func TestHandleBlockedEpicRun_QualityGatePrintsFindings(t *testing.T) {
	dir := t.TempDir()
	runID := "run-ver-epic-quality-1"

	qa := state.TicketQualityArtifact{
		RootTicketID: "ver-epic",
		TicketIDs:    []string{"ver-t1"},
		Status:       state.TicketQualityBlocked,
		Blocked:      true,
		BlockReason:  "unresolvable",
		Findings: []state.TicketQualityFinding{
			{
				TicketID:    "ver-t1",
				Code:        string(state.QualityCodeMissingAcceptanceCriteria),
				Severity:    "P1",
				Disposition: "blocked",
			},
		},
	}
	writeTicketQualityArtifact(t, dir, runID, qa)

	// Inject a nil interactor so isTTY() returns false — prevents any prompt.
	original := blockedRunInteractorFor
	t.Cleanup(func() { blockedRunInteractorFor = original })
	blockedRunInteractorFor = func() blockedRunInteractor {
		return blockedRunInteractor{in: nil, out: nil}
	}

	blockedErr := &engine.BlockedRunError{
		RunID:  runID,
		Status: state.EpicRunStatusBlocked,
		Cause:  errQualityGateBlocked("unresolvable"),
	}

	var out, errw bytes.Buffer
	retried, err := handleBlockedEpicRun(context.Background(), &out, &errw, dir, contextCfgForResume{}, blockedErr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if retried {
		t.Fatal("expected retried=false for quality-gate block")
	}

	outStr := out.String()
	wants := []string{
		"Ticket quality gate blocked before worker dispatch",
		"ver-t1",
		string(state.QualityCodeMissingAcceptanceCriteria),
		filepath.Join(".verk", "runs", runID, "ticket-quality.json"),
	}
	for _, want := range wants {
		if !strings.Contains(outStr, want) {
			t.Errorf("expected stdout to contain %q, got:\n%s", want, outStr)
		}
	}

	// Generic blocked guidance must NOT appear — the quality-gate path short-circuits.
	if strings.Contains(errw.String(), "Retryable tickets:") || strings.Contains(errw.String(), "Blocked tickets:") {
		t.Errorf("quality-gate path must not emit generic guidance, errw:\n%s", errw.String())
	}
}

// errQualityGateBlocked is a minimal error type used in tests to simulate the
// cause string embedded in a BlockedRunError for a quality-gate block.
type errQualityGateStr string

func (e errQualityGateStr) Error() string { return string(e) }

func errQualityGateBlocked(reason string) error {
	return errQualityGateStr(ticketQualityGateMarker + ": " + reason)
}

// TestRun_TicketQualityBlockSurfacesFindings is an integration-style test that
// injects a fake runTicket implementation returning a quality-gate-blocked
// snapshot and verifies that doRunTicket surfaces the findings and artifact path
// in its stdout output.
func TestRun_TicketQualityBlockSurfacesFindings(t *testing.T) {
	dir := t.TempDir()
	initCLITestRepo(t, dir)

	// Write a minimal ticket file.
	ticketsDir := filepath.Join(dir, ".tickets")
	if err := os.MkdirAll(ticketsDir, 0o755); err != nil {
		t.Fatalf("mkdir .tickets: %v", err)
	}
	ticket := epos.Ticket{
		ID:     "ver-qg-block",
		Title:  "Missing acceptance criteria ticket",
		Status: epos.StatusReady,
	}
	if err := epos.SaveTicket(filepath.Join(ticketsDir, "ver-qg-block.md"), ticket); err != nil {
		t.Fatalf("save ticket: %v", err)
	}

	// Save original package-level vars and restore on cleanup.
	origRunTicket := runTicket
	origRunProgress := runProgress
	origSaveJSONAtomic := saveJSONAtomic
	origSaveTicket := saveTicket
	t.Cleanup(func() {
		runTicket = origRunTicket
		runProgress = origRunProgress
		saveJSONAtomic = origSaveJSONAtomic
		saveTicket = origSaveTicket
	})

	// The fake runTicket writes the quality artifact and returns a blocked snapshot.
	var capturedRunID string
	runTicket = func(_ context.Context, req engine.RunTicketRequest) (engine.RunTicketResult, error) {
		capturedRunID = req.RunID

		// Write the quality artifact that printTicketQualityBlock will read.
		qa := state.TicketQualityArtifact{
			RootTicketID: req.Ticket.ID,
			TicketIDs:    []string{req.Ticket.ID},
			Status:       state.TicketQualityBlocked,
			Blocked:      true,
			BlockReason:  "unresolvable findings remain",
			Findings: []state.TicketQualityFinding{
				{
					TicketID:    req.Ticket.ID,
					Code:        string(state.QualityCodeMissingAcceptanceCriteria),
					Severity:    "P1",
					Disposition: "blocked",
				},
			},
		}
		writeTicketQualityArtifact(t, req.RepoRoot, req.RunID, qa)

		return engine.RunTicketResult{
			Snapshot: engine.TicketRunSnapshot{
				TicketID:     req.Ticket.ID,
				CurrentPhase: state.TicketPhaseBlocked,
				BlockReason:  ticketQualityGateMarker + ": unresolvable findings remain",
			},
		}, nil
	}

	// Drain progress channel immediately (no real TUI in tests).
	runProgress = func(_ string, ch <-chan engine.ProgressEvent, _ io.Writer, _ func()) error {
		for range ch {
		}
		return nil
	}

	// Allow the initial run.json write; short-circuit the final write so we
	// don't have to set up a full state machine. Use the first call's path
	// to write a minimal run.json that later calls can skip.
	callCount := 0
	saveJSONAtomic = func(path string, v any) error {
		callCount++
		if callCount == 1 {
			// First call: initial run.json — use real implementation.
			return state.SaveJSONAtomic(path, v)
		}
		// Final run.json update — succeed silently.
		return nil
	}
	saveTicket = func(_ string, _ epos.Ticket) error { return nil }

	t.Chdir(dir)

	var stdout, stderr bytes.Buffer
	runID, err := doRunTicket(&stdout, &stderr, "ver-qg-block")
	if err != nil {
		t.Fatalf("unexpected error from doRunTicket: %v", err)
	}
	if runID == "" {
		t.Fatal("expected non-empty runID")
	}
	_ = capturedRunID

	outStr := stdout.String()
	wants := []string{
		"Ticket quality gate blocked before worker dispatch",
		string(state.QualityCodeMissingAcceptanceCriteria),
		filepath.Join(".verk", "runs", runID, "ticket-quality.json"),
	}
	for _, want := range wants {
		if !strings.Contains(outStr, want) {
			t.Errorf("expected stdout to contain %q, got:\n%s", want, outStr)
		}
	}
}
