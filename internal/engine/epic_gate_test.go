package engine

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"verk/internal/adapters/runtime"
	"verk/internal/adapters/ticketstore/tkmd"
	verifycommand "verk/internal/adapters/verify/command"
	"verk/internal/policy"
	"verk/internal/state"

	runtimefake "verk/internal/adapters/runtime/fake"
)

// epicGateTestStart returns a stable base time used by epic gate test fixtures
// so generated artifact timestamps are deterministic.
var epicGateTestStart = time.Date(2026, 4, 2, 15, 0, 0, 0, time.UTC)

// epicGateWorkerResult builds a minimal valid WorkerResult for a repair worker.
func epicGateWorkerResult(leaseID string) runtime.WorkerResult {
	return runtime.WorkerResult{
		Status:             runtime.WorkerStatusDone,
		RetryClass:         runtime.RetryClassTerminal,
		LeaseID:            leaseID,
		StartedAt:          epicGateTestStart,
		FinishedAt:         epicGateTestStart.Add(time.Second),
		ResultArtifactPath: "artifact.json",
	}
}

// epicGateReviewPassed builds a minimal valid passing ReviewResult.
func epicGateReviewPassed(leaseID string) runtime.ReviewResult {
	return runtime.ReviewResult{
		Status:             runtime.WorkerStatusDone,
		RetryClass:         runtime.RetryClassTerminal,
		LeaseID:            leaseID,
		StartedAt:          epicGateTestStart.Add(2 * time.Second),
		FinishedAt:         epicGateTestStart.Add(3 * time.Second),
		ReviewStatus:       runtime.ReviewStatusPassed,
		Summary:            "no blocking gaps found",
		Findings:           nil,
		ResultArtifactPath: "review-artifact.json",
	}
}

func TestResolveRepairedFindingsFiltersBySource(t *testing.T) {
	findings := []state.EpicClosureFinding{
		{ID: "review-one", Source: "epic_reviewer"},
		{ID: "broad-one", Source: "broad_check"},
	}

	resolveRepairedFindings(findings, nil, "epic_reviewer")

	if !findings[0].Resolved {
		t.Fatalf("expected missing reviewer finding to resolve")
	}
	if findings[1].Resolved {
		t.Fatalf("did not expect check finding to resolve during reviewer refresh")
	}

	resolveRepairedFindings(findings, nil, "broad_check", "derived_check")

	if !findings[1].Resolved {
		t.Fatalf("expected missing check finding to resolve during check refresh")
	}
}

// epicGateReviewWithP1Finding builds a valid ReviewResult that carries one P1
// blocking finding (blocks at the default P2 threshold). LeaseID is required by
// the fake adapter's Validate call.
func epicGateReviewWithP1Finding(leaseID string) runtime.ReviewResult {
	return runtime.ReviewResult{
		Status:             runtime.WorkerStatusDone,
		RetryClass:         runtime.RetryClassTerminal,
		LeaseID:            leaseID,
		StartedAt:          epicGateTestStart.Add(2 * time.Second),
		FinishedAt:         epicGateTestStart.Add(3 * time.Second),
		ReviewStatus:       runtime.ReviewStatusFindings,
		Summary:            "acceptance criterion not evidenced",
		ResultArtifactPath: "review-artifact.json",
		Findings: []runtime.ReviewFinding{
			{
				ID:          "finding-ac-gap-1",
				Severity:    runtime.SeverityP1,
				Title:       "acceptance criterion not covered by any child artifact",
				Body:        "AC #2 (rate limiting) is not evidenced by any test in the child ticket artifacts",
				File:        "internal/api/rate_limiter.go",
				Line:        42,
				Disposition: runtime.ReviewDispositionOpen,
			},
		},
	}
}

// makeEpicGateReq builds a minimal RunEpicRequest sufficient for runEpicClosureGate.
func makeEpicGateReq(repoRoot, epicID string, adapter runtime.Adapter) RunEpicRequest {
	return RunEpicRequest{
		RepoRoot:     repoRoot,
		RunID:        "run-epic-gate-test",
		RootTicketID: epicID,
		Adapter:      adapter,
		Config:       policy.DefaultConfig(),
	}
}

// makeEpicGateChildren builds a minimal child ticket slice used to populate
// the gate's child context. Each child has non-overlapping owned paths so the
// file→ticket mapping tests work correctly.
func makeEpicGateChildren() []tkmd.Ticket {
	return []tkmd.Ticket{
		{
			ID:         "child-a",
			Title:      "Child A",
			Status:     tkmd.StatusClosed,
			OwnedPaths: []string{"internal/api"},
		},
		{
			ID:         "child-b",
			Title:      "Child B",
			Status:     tkmd.StatusClosed,
			OwnedPaths: []string{"internal/worker"},
		},
	}
}

// ---- Happy path ----------------------------------------------------------------

// TestRunEpicClosureGate_HappyPath verifies that when no broad commands are
// configured and the epic reviewer passes, the gate returns nil and persists a
// Closable=true artifact.
func TestRunEpicClosureGate_HappyPath(t *testing.T) {
	repoRoot := t.TempDir()
	epicID := "epic-gate-happy"

	adapter := runtimefake.New(nil, []runtime.ReviewResult{
		epicGateReviewPassed("epic-review-" + epicID + "-0"),
	})

	cfg := policy.DefaultConfig()
	cfg.Verification.EpicClosureCommands = nil
	cfg.Verification.EpicStaleWordingTerms = nil

	req := makeEpicGateReq(repoRoot, epicID, adapter)
	req.Config = cfg

	err := runEpicClosureGate(context.Background(), req, cfg, makeEpicGateChildren(), nil)
	if err != nil {
		t.Fatalf("expected nil error for happy-path gate, got: %v", err)
	}

	// Verify exactly one reviewer call was made.
	reviewReqs := adapter.ReviewRequests()
	if len(reviewReqs) != 1 {
		t.Fatalf("expected 1 reviewer call, got %d", len(reviewReqs))
	}

	// Artifact must be persisted to disk with Closable=true.
	var artifact state.EpicClosureArtifact
	artifactPath := epicClosureArtifactPath(repoRoot, req.RunID)
	if err := state.LoadJSON(artifactPath, &artifact); err != nil {
		t.Fatalf("load epic closure artifact: %v", err)
	}
	if !artifact.Closable {
		t.Errorf("expected Closable=true in artifact, got false")
	}
	if artifact.EpicID != epicID {
		t.Errorf("expected EpicID=%q, got %q", epicID, artifact.EpicID)
	}
	if artifact.ClosureReason == "" {
		t.Error("expected non-empty ClosureReason in artifact")
	}
	if len(artifact.Cycles) != 0 {
		t.Errorf("expected 0 repair cycles, got %d", len(artifact.Cycles))
	}
}

// ---- Reviewer instructions contain required framing ---------------------------

// TestRunEpicClosureGate_ReviewerInstructionsContainFraming verifies that the
// instructions passed to the epic reviewer open with the canonical
// EpicReviewFraming wording and include all child ticket IDs.
func TestRunEpicClosureGate_ReviewerInstructionsContainFraming(t *testing.T) {
	repoRoot := t.TempDir()
	epicID := "epic-gate-framing"

	adapter := runtimefake.New(nil, []runtime.ReviewResult{
		epicGateReviewPassed("epic-review-" + epicID + "-0"),
	})

	cfg := policy.DefaultConfig()
	cfg.Verification.EpicClosureCommands = nil

	req := makeEpicGateReq(repoRoot, epicID, adapter)
	req.Config = cfg
	children := makeEpicGateChildren()

	if err := runEpicClosureGate(context.Background(), req, cfg, children, nil); err != nil {
		t.Fatalf("unexpected gate error: %v", err)
	}

	reviewReqs := adapter.ReviewRequests()
	if len(reviewReqs) == 0 {
		t.Fatal("expected at least one reviewer call")
	}
	instructions := reviewReqs[0].Instructions

	// The instructions must open with the canonical framing wording.
	if !strings.HasPrefix(instructions, runtime.EpicReviewFraming) {
		t.Errorf("reviewer instructions do not start with EpicReviewFraming.\ngot prefix: %.200s", instructions)
	}

	// All child ticket IDs must appear in the instructions.
	for _, child := range children {
		if !strings.Contains(instructions, child.ID) {
			t.Errorf("reviewer instructions missing child ticket ID %q", child.ID)
		}
	}

	// Epic ID must appear in the instructions.
	if !strings.Contains(instructions, epicID) {
		t.Errorf("reviewer instructions missing epic ID %q", epicID)
	}
}

// ---- Failed broad check: repair disabled --------------------------------------

// TestRunEpicClosureGate_BroadCheckFails_RepairDisabled_Blocks verifies that
// when a broad closure command fails and MaxEpicRepairCycles=0, the gate
// returns a BlockedRunError immediately without dispatching a repair worker.
func TestRunEpicClosureGate_BroadCheckFails_RepairDisabled_Blocks(t *testing.T) {
	repoRoot := t.TempDir()
	epicID := "epic-gate-broad-norepair"

	// The reviewer is still invoked even when checks fail (so the artifact is
	// complete). Provide one reviewer result that passes at the item-level so
	// the block reason comes from the check failure, not the reviewer.
	adapter := runtimefake.New(nil, []runtime.ReviewResult{
		epicGateReviewPassed("epic-review-" + epicID + "-0"),
	})

	cfg := policy.DefaultConfig()
	cfg.Policy.MaxEpicRepairCycles = 0
	cfg.Verification.EpicClosureCommands = []policy.QualityCommand{
		{Path: ".", Run: []string{"false"}},
	}

	req := makeEpicGateReq(repoRoot, epicID, adapter)
	req.Config = cfg

	err := runEpicClosureGate(context.Background(), req, cfg, makeEpicGateChildren(), nil)
	if err == nil {
		t.Fatal("expected BlockedRunError, got nil")
	}
	if !errors.Is(err, ErrEpicBlocked) {
		t.Fatalf("expected ErrEpicBlocked, got: %v", err)
	}

	// No repair workers must have been dispatched.
	if len(adapter.WorkerRequests()) != 0 {
		t.Errorf("expected 0 worker (repair) calls, got %d", len(adapter.WorkerRequests()))
	}

	// Artifact must be persisted and flagged non-closable.
	var artifact state.EpicClosureArtifact
	artifactPath := epicClosureArtifactPath(repoRoot, req.RunID)
	if err := state.LoadJSON(artifactPath, &artifact); err != nil {
		t.Fatalf("load epic closure artifact: %v", err)
	}
	if artifact.Closable {
		t.Error("expected Closable=false when broad check failed with repair disabled")
	}
	if artifact.BlockReason == "" {
		t.Error("expected non-empty BlockReason when gate is blocked")
	}
}

// ---- Failed broad check: repair succeeds -------------------------------------

// TestRunEpicClosureGate_BroadCheckFails_RoutesRepair_ThenPasses verifies that
// when a broad closure command fails on the first run, a repair worker is
// dispatched and the gate passes after the repair makes the command succeed.
//
// A toggle script (counter-file based) simulates "check fails first, passes
// after repair" without requiring the fake adapter to modify the filesystem.
func TestRunEpicClosureGate_BroadCheckFails_RoutesRepair_ThenPasses(t *testing.T) {
	repoRoot := t.TempDir()
	epicID := "epic-gate-broad-repair"

	// Toggle: first invocation exits 1, second exits 0.
	counterPath := filepath.Join(repoRoot, ".gate-call-counter")
	toggleScript := filepath.Join(repoRoot, "toggle-broad.sh")
	script := "#!/bin/sh\n" +
		"COUNT=$(cat " + counterPath + " 2>/dev/null || echo 0)\n" +
		"NEXT=$((COUNT+1))\n" +
		"printf %s $NEXT > " + counterPath + "\n" +
		"[ \"$COUNT\" -ge 1 ] && exit 0 || exit 1\n"
	if err := os.WriteFile(toggleScript, []byte(script), 0o755); err != nil {
		t.Fatalf("write toggle script: %v", err)
	}

	// One repair worker result (for cycle 1) plus one reviewer result after repair.
	adapter := runtimefake.New(
		[]runtime.WorkerResult{
			epicGateWorkerResult("epic-repair-" + epicID + "-1"),
		},
		[]runtime.ReviewResult{
			// Attempt 1 (before repair, while checks still failing) — reviewer
			// runs concurrently with check evidence; provide passing result.
			epicGateReviewPassed("epic-review-" + epicID + "-0"),
			// Attempt 2 (after repair, checks now pass).
			epicGateReviewPassed("epic-review-" + epicID + "-1"),
		},
	)

	cfg := policy.DefaultConfig()
	cfg.Policy.MaxEpicRepairCycles = 2
	cfg.Verification.EpicClosureCommands = []policy.QualityCommand{
		{Path: ".", Run: []string{toggleScript}},
	}

	req := makeEpicGateReq(repoRoot, epicID, adapter)
	req.Config = cfg

	err := runEpicClosureGate(context.Background(), req, cfg, makeEpicGateChildren(), nil)
	if err != nil {
		t.Fatalf("expected nil after repair, got: %v", err)
	}

	// Exactly one repair worker must have been dispatched.
	workerReqs := adapter.WorkerRequests()
	if len(workerReqs) != 1 {
		t.Fatalf("expected 1 repair worker call, got %d", len(workerReqs))
	}

	// Artifact must be closable.
	var artifact state.EpicClosureArtifact
	if err := state.LoadJSON(epicClosureArtifactPath(repoRoot, req.RunID), &artifact); err != nil {
		t.Fatalf("load artifact: %v", err)
	}
	if !artifact.Closable {
		t.Errorf("expected Closable=true after repair")
	}
	if len(artifact.Cycles) != 1 {
		t.Errorf("expected 1 repair cycle recorded, got %d", len(artifact.Cycles))
	}
}

// ---- Stale wording detected --------------------------------------------------

// TestRunEpicClosureGate_StaleWordingDetected_Blocks verifies that when a doc
// file contains a configured stale wording term, the derived stale-wording
// check fails and the gate blocks (with repair disabled) before closing.
//
// This covers test case 2 from the ticket: "Child ticket updates
// docs/self-hosting.md but leaves stale scanner wording in CONTRIBUTING.md."
func TestRunEpicClosureGate_StaleWordingDetected_Blocks(t *testing.T) {
	repoRoot := t.TempDir()
	epicID := "epic-gate-stale"

	// Write a doc file containing the stale term.
	docPath := filepath.Join(repoRoot, "CONTRIBUTING.md")
	if err := os.WriteFile(docPath, []byte("# Contributing\n\nPlease use old-scanner before committing.\n"), 0o644); err != nil {
		t.Fatalf("write doc file: %v", err)
	}

	adapter := runtimefake.New(nil, []runtime.ReviewResult{
		epicGateReviewPassed("epic-review-" + epicID + "-0"),
	})

	cfg := policy.DefaultConfig()
	cfg.Policy.MaxEpicRepairCycles = 0
	cfg.Verification.EpicStaleWordingTerms = []string{"old-scanner"}
	cfg.Verification.EpicClosureDocs = []string{"CONTRIBUTING.md"} // scope to our test file

	req := makeEpicGateReq(repoRoot, epicID, adapter)
	req.Config = cfg

	err := runEpicClosureGate(context.Background(), req, cfg, makeEpicGateChildren(), nil)
	if err == nil {
		t.Fatal("expected BlockedRunError for stale wording, got nil")
	}
	if !errors.Is(err, ErrEpicBlocked) {
		t.Fatalf("expected ErrEpicBlocked, got: %v", err)
	}

	// The artifact should record the derived check as a finding.
	var artifact state.EpicClosureArtifact
	if err := state.LoadJSON(epicClosureArtifactPath(repoRoot, req.RunID), &artifact); err != nil {
		t.Fatalf("load artifact: %v", err)
	}
	if artifact.Closable {
		t.Error("expected Closable=false when stale wording detected")
	}
	if len(artifact.DerivedCommands) == 0 {
		t.Error("expected at least one derived command recorded in artifact")
	}
	if artifact.BlockReason == "" {
		t.Error("expected non-empty BlockReason")
	}
}

// TestRunEpicClosureGate_StaleWordingRepaired verifies that when a doc file
// contains stale wording and a repair worker (custom adapter) fixes the file,
// the re-run of the derived check passes and the gate closes successfully.
func TestRunEpicClosureGate_StaleWordingRepaired(t *testing.T) {
	repoRoot := t.TempDir()
	epicID := "epic-gate-stale-repaired"

	// Write a doc file containing the stale term.
	docPath := filepath.Join(repoRoot, "CONTRIBUTING.md")
	if err := os.WriteFile(docPath, []byte("# Contributing\n\nPlease use old-scanner.\n"), 0o644); err != nil {
		t.Fatalf("write stale doc: %v", err)
	}

	// Custom adapter: repair worker removes the stale wording from the file.
	fixedDoc := "# Contributing\n\nPlease use the new linter.\n"
	repairAdapter := &staleWordingRepairAdapter{
		docPath:    docPath,
		fixContent: []byte(fixedDoc),
		reviewResults: []runtime.ReviewResult{
			// Attempt 1 (pre-repair, checks failing).
			epicGateReviewPassed("epic-review-" + epicID + "-0"),
			// Attempt 2 (post-repair, checks passing).
			epicGateReviewPassed("epic-review-" + epicID + "-1"),
		},
	}

	cfg := policy.DefaultConfig()
	cfg.Policy.MaxEpicRepairCycles = 1
	cfg.Verification.EpicStaleWordingTerms = []string{"old-scanner"}
	cfg.Verification.EpicClosureDocs = []string{"CONTRIBUTING.md"}

	req := makeEpicGateReq(repoRoot, epicID, repairAdapter)
	req.Config = cfg

	err := runEpicClosureGate(context.Background(), req, cfg, makeEpicGateChildren(), nil)
	if err != nil {
		t.Fatalf("expected nil after stale wording repair, got: %v", err)
	}
	if !repairAdapter.repaired {
		t.Error("expected repair worker to have been called")
	}

	var artifact state.EpicClosureArtifact
	if err := state.LoadJSON(epicClosureArtifactPath(repoRoot, req.RunID), &artifact); err != nil {
		t.Fatalf("load artifact: %v", err)
	}
	if !artifact.Closable {
		t.Errorf("expected Closable=true after stale wording repair; BlockReason=%q", artifact.BlockReason)
	}
}

func TestBuildEpicStaleWordingCommand_EscapesTermsAndPaths(t *testing.T) {
	repoRoot := t.TempDir()
	docsDir := filepath.Join(repoRoot, "docs with spaces")
	if err := os.MkdirAll(docsDir, 0o755); err != nil {
		t.Fatalf("mkdir docs dir: %v", err)
	}
	docPath := filepath.Join(docsDir, "guide.md")
	if err := os.WriteFile(docPath, []byte("this text says don't use old.scanner\n"), 0o644); err != nil {
		t.Fatalf("write stale doc: %v", err)
	}

	command := buildEpicStaleWordingCommand(
		[]string{"don't", "old.scanner"},
		[]string{"docs with spaces/guide.md"},
	)
	result := exec.Command("/bin/sh", "-c", command)
	result.Dir = repoRoot
	if err := result.Run(); err == nil {
		t.Fatal("expected inverted stale wording command to fail when a quoted term matches")
	}

	if err := os.WriteFile(docPath, []byte("clean text\n"), 0o644); err != nil {
		t.Fatalf("write clean doc: %v", err)
	}
	result = exec.Command("/bin/sh", "-c", command)
	result.Dir = repoRoot
	if output, err := result.CombinedOutput(); err != nil {
		t.Fatalf("expected escaped stale wording command to pass for clean docs, err=%v output=%s", err, output)
	}
}

func TestCollectEpicVerificationOutput_CapsLargeArtifacts(t *testing.T) {
	repoRoot := t.TempDir()
	stdoutPath := filepath.Join(repoRoot, "large.stdout.log")
	if err := os.WriteFile(stdoutPath, []byte(strings.Repeat("x", epicGateOutputLimit*2)), 0o644); err != nil {
		t.Fatalf("write large stdout: %v", err)
	}

	output := collectEpicVerificationOutput(
		[]verifycommand.CommandResult{{
			Command:    "failing-check",
			ExitCode:   1,
			StdoutPath: stdoutPath,
		}},
		nil,
		nil,
	)
	if len(output) > epicGateOutputLimit {
		t.Fatalf("expected output length <= %d, got %d", epicGateOutputLimit, len(output))
	}
	if !strings.Contains(output, "$ failing-check (exit 1)") {
		t.Fatalf("expected command header in output, got %q", output)
	}
}

// staleWordingRepairAdapter is a test double that rewrites a target doc file
// when the repair worker is called, simulating a real worker fixing stale
// wording in documentation. The reviewer always returns the pre-configured
// results via an internal fake.Adapter.
type staleWordingRepairAdapter struct {
	docPath       string
	fixContent    []byte
	repaired      bool
	reviewResults []runtime.ReviewResult

	inner     *runtimefake.Adapter
	innerOnce bool
}

func (a *staleWordingRepairAdapter) RunWorker(_ context.Context, req runtime.WorkerRequest) (runtime.WorkerResult, error) {
	// Overwrite the file to remove stale wording.
	if err := os.WriteFile(a.docPath, a.fixContent, 0o644); err != nil {
		return runtime.WorkerResult{}, err
	}
	a.repaired = true
	now := time.Now().UTC()
	return runtime.WorkerResult{
		Status:             runtime.WorkerStatusDone,
		RetryClass:         runtime.RetryClassTerminal,
		LeaseID:            req.LeaseID,
		StartedAt:          now,
		FinishedAt:         now.Add(time.Second),
		ResultArtifactPath: "artifact.json",
	}, nil
}

func (a *staleWordingRepairAdapter) RunReviewer(ctx context.Context, req runtime.ReviewRequest) (runtime.ReviewResult, error) {
	if !a.innerOnce {
		a.inner = runtimefake.New(nil, a.reviewResults)
		a.innerOnce = true
	}
	return a.inner.RunReviewer(ctx, req)
}

// ---- Reviewer finds blocking finding, repair disabled ------------------------

// TestRunEpicClosureGate_ReviewerFindingBlocks verifies that when the epic
// reviewer returns a P1 blocking finding and MaxEpicRepairCycles=0, the gate
// returns a BlockedRunError without dispatching a repair worker.
//
// This covers test case 3 from the ticket: "Epic reviewer identifies that one
// acceptance criterion is not evidenced by any child artifact."
func TestRunEpicClosureGate_ReviewerFindingBlocks(t *testing.T) {
	repoRoot := t.TempDir()
	epicID := "epic-gate-reviewer-block"

	adapter := runtimefake.New(nil, []runtime.ReviewResult{
		epicGateReviewWithP1Finding("epic-review-" + epicID + "-0"),
	})

	cfg := policy.DefaultConfig()
	cfg.Policy.MaxEpicRepairCycles = 0
	cfg.Verification.EpicClosureCommands = nil

	req := makeEpicGateReq(repoRoot, epicID, adapter)
	req.Config = cfg

	err := runEpicClosureGate(context.Background(), req, cfg, makeEpicGateChildren(), nil)
	if err == nil {
		t.Fatal("expected BlockedRunError for reviewer blocking finding, got nil")
	}
	if !errors.Is(err, ErrEpicBlocked) {
		t.Fatalf("expected ErrEpicBlocked, got: %v", err)
	}

	// No repair workers must have been dispatched.
	if len(adapter.WorkerRequests()) != 0 {
		t.Errorf("expected 0 repair worker calls, got %d", len(adapter.WorkerRequests()))
	}

	var artifact state.EpicClosureArtifact
	if err := state.LoadJSON(epicClosureArtifactPath(repoRoot, req.RunID), &artifact); err != nil {
		t.Fatalf("load artifact: %v", err)
	}
	if artifact.Closable {
		t.Error("expected Closable=false when reviewer found blocking gap")
	}
	if len(artifact.Findings) == 0 {
		t.Error("expected at least one finding in artifact")
	}
}

func TestRunEpicClosureGate_ReviewerErrorBlocksWithoutRepair(t *testing.T) {
	repoRoot := t.TempDir()
	epicID := "epic-gate-reviewer-error"

	adapter := runtimefake.New([]runtime.WorkerResult{
		epicGateWorkerResult("epic-repair-" + epicID + "-1"),
	}, nil)

	cfg := policy.DefaultConfig()
	cfg.Policy.MaxEpicRepairCycles = 1
	cfg.Verification.EpicClosureCommands = nil

	req := makeEpicGateReq(repoRoot, epicID, adapter)
	req.Config = cfg

	err := runEpicClosureGate(context.Background(), req, cfg, makeEpicGateChildren(), nil)
	if err == nil {
		t.Fatal("expected BlockedRunError for reviewer runtime error, got nil")
	}
	if !errors.Is(err, ErrEpicBlocked) {
		t.Fatalf("expected ErrEpicBlocked, got: %v", err)
	}
	if len(adapter.WorkerRequests()) != 0 {
		t.Errorf("expected reviewer error to block without repair, got %d repair calls", len(adapter.WorkerRequests()))
	}

	var artifact state.EpicClosureArtifact
	if err := state.LoadJSON(epicClosureArtifactPath(repoRoot, req.RunID), &artifact); err != nil {
		t.Fatalf("load artifact: %v", err)
	}
	if artifact.Closable {
		t.Error("expected Closable=false when reviewer execution fails")
	}
	if !strings.Contains(artifact.BlockReason, "reviewer error") {
		t.Fatalf("expected reviewer error in block reason, got %q", artifact.BlockReason)
	}
	if len(artifact.Cycles) != 0 {
		t.Fatalf("expected no repair cycles for reviewer execution error, got %d", len(artifact.Cycles))
	}
}

// ---- Repair cycles exhausted -------------------------------------------------

// TestRunEpicClosureGate_RepairCyclesExhausted verifies that when the broad
// closure command never passes and all repair cycles are consumed, the gate
// returns a BlockedRunError that carries the repair limit evidence.
//
// This covers test case 4 from the ticket: "Epic repair repeatedly fails. The
// epic blocks with the failed finding and repair-cycle summary."
func TestRunEpicClosureGate_RepairCyclesExhausted(t *testing.T) {
	repoRoot := t.TempDir()
	epicID := "epic-gate-exhaust"

	// Two repair cycles, each followed by a reviewer call that also passes
	// (so only the check failure drives the block).
	adapter := runtimefake.New(
		[]runtime.WorkerResult{
			epicGateWorkerResult("epic-repair-" + epicID + "-1"),
			epicGateWorkerResult("epic-repair-" + epicID + "-2"),
		},
		[]runtime.ReviewResult{
			// Attempt 1 (initial).
			epicGateReviewPassed("epic-review-" + epicID + "-0"),
			// Attempt 2 (after cycle 1).
			epicGateReviewPassed("epic-review-" + epicID + "-1"),
			// Attempt 3 (after cycle 2).
			epicGateReviewPassed("epic-review-" + epicID + "-2"),
		},
	)

	cfg := policy.DefaultConfig()
	cfg.Policy.MaxEpicRepairCycles = 2
	cfg.Verification.EpicClosureCommands = []policy.QualityCommand{
		{Path: ".", Run: []string{"false"}}, // always fails
	}

	req := makeEpicGateReq(repoRoot, epicID, adapter)
	req.Config = cfg

	err := runEpicClosureGate(context.Background(), req, cfg, makeEpicGateChildren(), nil)
	if err == nil {
		t.Fatal("expected BlockedRunError after exhausting repair cycles, got nil")
	}
	if !errors.Is(err, ErrEpicBlocked) {
		t.Fatalf("expected ErrEpicBlocked, got: %v", err)
	}

	// Both repair workers must have been dispatched.
	if len(adapter.WorkerRequests()) != 2 {
		t.Errorf("expected 2 repair worker calls, got %d", len(adapter.WorkerRequests()))
	}

	// Artifact must record the repair limit.
	var artifact state.EpicClosureArtifact
	if err := state.LoadJSON(epicClosureArtifactPath(repoRoot, req.RunID), &artifact); err != nil {
		t.Fatalf("load artifact: %v", err)
	}
	if artifact.Closable {
		t.Error("expected Closable=false after exhausting repair cycles")
	}
	if artifact.RepairLimit == nil {
		t.Error("expected RepairLimit to be set after exhausting cycles")
	}
	if artifact.RepairLimit != nil && artifact.RepairLimit.Limit != 2 {
		t.Errorf("expected RepairLimit.Limit=2, got %d", artifact.RepairLimit.Limit)
	}
	if len(artifact.Cycles) != 2 {
		t.Errorf("expected 2 repair cycles recorded, got %d", len(artifact.Cycles))
	}
	if artifact.BlockReason == "" {
		t.Error("expected non-empty BlockReason after repair cycles exhausted")
	}
}

// ---- Reviewer finding routed to repair worker --------------------------------

// TestRunEpicClosureGate_ReviewerFindingRoutedToRepair verifies that when the
// epic reviewer returns a blocking finding and repair cycles are enabled, a
// repair worker is dispatched and the gate passes after the reviewer clears the
// finding on the second attempt.
func TestRunEpicClosureGate_ReviewerFindingRoutedToRepair(t *testing.T) {
	repoRoot := t.TempDir()
	epicID := "epic-gate-reviewer-repair"

	adapter := runtimefake.New(
		[]runtime.WorkerResult{
			epicGateWorkerResult("epic-repair-" + epicID + "-1"),
		},
		[]runtime.ReviewResult{
			// Attempt 1: P1 blocking finding → triggers repair.
			epicGateReviewWithP1Finding("epic-review-" + epicID + "-0"),
			// Attempt 2 (after repair): reviewer passes.
			epicGateReviewPassed("epic-review-" + epicID + "-1"),
		},
	)

	cfg := policy.DefaultConfig()
	cfg.Policy.MaxEpicRepairCycles = 1
	cfg.Verification.EpicClosureCommands = nil

	req := makeEpicGateReq(repoRoot, epicID, adapter)
	req.Config = cfg

	err := runEpicClosureGate(context.Background(), req, cfg, makeEpicGateChildren(), nil)
	if err != nil {
		t.Fatalf("expected nil after reviewer finding repaired, got: %v", err)
	}

	// Repair worker must have been dispatched.
	if len(adapter.WorkerRequests()) != 1 {
		t.Errorf("expected 1 repair worker call, got %d", len(adapter.WorkerRequests()))
	}

	var artifact state.EpicClosureArtifact
	if err := state.LoadJSON(epicClosureArtifactPath(repoRoot, req.RunID), &artifact); err != nil {
		t.Fatalf("load artifact: %v", err)
	}
	if !artifact.Closable {
		t.Errorf("expected Closable=true after reviewer finding repaired; BlockReason=%q", artifact.BlockReason)
	}
}

// ---- Findings mapped to owning child ticket ----------------------------------

// TestRunEpicClosureGate_FindingsMappedToChildTicket verifies that when the
// epic reviewer returns a finding whose file path falls within a child ticket's
// owned paths, the finding's OwningTicketID is set to that child's ID.
func TestRunEpicClosureGate_FindingsMappedToChildTicket(t *testing.T) {
	repoRoot := t.TempDir()
	epicID := "epic-gate-mapping"

	// Finding in "internal/api/rate_limiter.go" → owned by "child-a" ("internal/api").
	reviewWithMapping := runtime.ReviewResult{
		Status:             runtime.WorkerStatusDone,
		RetryClass:         runtime.RetryClassTerminal,
		LeaseID:            "epic-review-" + epicID + "-0",
		StartedAt:          epicGateTestStart.Add(2 * time.Second),
		FinishedAt:         epicGateTestStart.Add(3 * time.Second),
		ReviewStatus:       runtime.ReviewStatusFindings,
		Summary:            "one gap in child-a scope",
		ResultArtifactPath: "review-artifact.json",
		Findings: []runtime.ReviewFinding{
			{
				ID:          "f-owned-1",
				Severity:    runtime.SeverityP1,
				Title:       "rate limiter test missing",
				Body:        "The rate limiter is not tested at the integration level",
				File:        "internal/api/rate_limiter.go",
				Line:        10,
				Disposition: runtime.ReviewDispositionOpen,
			},
		},
	}

	// MaxEpicRepairCycles=0 so the gate blocks immediately after detecting the
	// finding. We only care that the finding is mapped, not that repair runs.
	adapter := runtimefake.New(nil, []runtime.ReviewResult{reviewWithMapping})

	cfg := policy.DefaultConfig()
	cfg.Policy.MaxEpicRepairCycles = 0
	cfg.Verification.EpicClosureCommands = nil

	req := makeEpicGateReq(repoRoot, epicID, adapter)
	req.Config = cfg

	children := []tkmd.Ticket{
		{ID: "child-a", Title: "API child", Status: tkmd.StatusClosed, OwnedPaths: []string{"internal/api"}},
		{ID: "child-b", Title: "Worker child", Status: tkmd.StatusClosed, OwnedPaths: []string{"internal/worker"}},
	}

	err := runEpicClosureGate(context.Background(), req, cfg, children, nil)
	// Gate must block (P1 finding, repair disabled).
	if err == nil {
		t.Fatal("expected BlockedRunError for reviewer finding")
	}

	var artifact state.EpicClosureArtifact
	if err := state.LoadJSON(epicClosureArtifactPath(repoRoot, req.RunID), &artifact); err != nil {
		t.Fatalf("load artifact: %v", err)
	}
	if len(artifact.Findings) == 0 {
		t.Fatal("expected at least one finding in artifact")
	}
	finding := artifact.Findings[0]
	if finding.OwningTicketID != "child-a" {
		t.Errorf("expected OwningTicketID=child-a for file in internal/api, got %q", finding.OwningTicketID)
	}
	if finding.AutoRepairPossible {
		// The finding in internal/api with "child-a" owning means auto-repair can be routed.
		// The NextAction should mention the owning ticket.
		if !strings.Contains(finding.NextAction, "child-a") {
			t.Errorf("expected NextAction to mention child-a, got %q", finding.NextAction)
		}
	}
}

// ---- Expensive broad check runs only at epic closure -------------------------

// TestRunEpicClosureGate_BroadCheckRunsAtEpicClosure_NotPerTicket verifies
// that EpicClosureCommands are recorded in the artifact's BroadCommands list
// but are not part of the per-ticket QualityCommands. This confirms the
// architectural invariant: expensive broad checks (e.g. `go test ./...`) run
// once at epic closure rather than on every ticket/wave.
func TestRunEpicClosureGate_BroadCheckRunsAtEpicClosure_NotPerTicket(t *testing.T) {
	repoRoot := t.TempDir()
	epicID := "epic-gate-broad-once"

	adapter := runtimefake.New(nil, []runtime.ReviewResult{
		epicGateReviewPassed("epic-review-" + epicID + "-0"),
	})

	cfg := policy.DefaultConfig()
	cfg.Verification.QualityCommands = nil // no per-ticket checks
	cfg.Verification.EpicClosureCommands = []policy.QualityCommand{
		{Path: ".", Run: []string{"true"}}, // broad e2e-style command
	}

	req := makeEpicGateReq(repoRoot, epicID, adapter)
	req.Config = cfg

	if err := runEpicClosureGate(context.Background(), req, cfg, makeEpicGateChildren(), nil); err != nil {
		t.Fatalf("expected nil from gate with passing broad command, got: %v", err)
	}

	// Artifact must list the broad command.
	var artifact state.EpicClosureArtifact
	if err := state.LoadJSON(epicClosureArtifactPath(repoRoot, req.RunID), &artifact); err != nil {
		t.Fatalf("load artifact: %v", err)
	}
	if len(artifact.BroadCommands) == 0 {
		t.Error("expected BroadCommands to record the epic closure command")
	}
	found := false
	for _, cmd := range artifact.BroadCommands {
		if strings.Contains(cmd, "true") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected broad command 'true' in BroadCommands, got %v", artifact.BroadCommands)
	}

	// Coverage must record declared checks from the epic closure commands.
	if artifact.Coverage == nil {
		t.Fatal("expected Coverage to be set in artifact")
	}
	if len(artifact.Coverage.DeclaredChecks) == 0 {
		t.Error("expected DeclaredChecks in coverage from epic closure command")
	}
}

// ---- Child ticket IDs recorded in artifact -----------------------------------

// TestRunEpicClosureGate_ChildIDsRecorded verifies that the closure artifact
// records all child ticket IDs so downstream tooling can trace which tickets
// contributed to the final epic state.
func TestRunEpicClosureGate_ChildIDsRecorded(t *testing.T) {
	repoRoot := t.TempDir()
	epicID := "epic-gate-child-ids"

	adapter := runtimefake.New(nil, []runtime.ReviewResult{
		epicGateReviewPassed("epic-review-" + epicID + "-0"),
	})

	cfg := policy.DefaultConfig()
	cfg.Verification.EpicClosureCommands = nil

	req := makeEpicGateReq(repoRoot, epicID, adapter)
	req.Config = cfg

	children := []tkmd.Ticket{
		{ID: "tk-alpha", Status: tkmd.StatusClosed, OwnedPaths: []string{"a"}},
		{ID: "tk-beta", Status: tkmd.StatusClosed, OwnedPaths: []string{"b"}},
		{ID: "tk-gamma", Status: tkmd.StatusClosed, OwnedPaths: []string{"c"}},
	}

	if err := runEpicClosureGate(context.Background(), req, cfg, children, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var artifact state.EpicClosureArtifact
	if err := state.LoadJSON(epicClosureArtifactPath(repoRoot, req.RunID), &artifact); err != nil {
		t.Fatalf("load artifact: %v", err)
	}

	want := map[string]bool{"tk-alpha": true, "tk-beta": true, "tk-gamma": true}
	for _, id := range artifact.ChildTicketIDs {
		delete(want, id)
	}
	if len(want) > 0 {
		missing := make([]string, 0, len(want))
		for id := range want {
			missing = append(missing, id)
		}
		t.Errorf("artifact missing child ticket IDs: %v", missing)
	}
}

// ---- Cursor helpers ----------------------------------------------------------

// TestEpicGateCursorHelpers verifies that the pending-epic-gate cursor helpers
// correctly set and clear the marker, and that both are safe to call with a nil
// cursor (no panic).
func TestEpicGateCursorHelpers(t *testing.T) {
	// setPendingEpicGate must be visible after set and absent after clear.
	cursor := map[string]any{}
	setPendingEpicGate(cursor)
	if _, ok := cursor["pending_epic_gate"]; !ok {
		t.Error("expected pending_epic_gate to be set")
	}

	clearPendingEpicGate(cursor)
	if _, ok := cursor["pending_epic_gate"]; ok {
		t.Error("expected pending_epic_gate to be cleared")
	}

	// Must not panic on nil cursor.
	setPendingEpicGate(nil)
	clearPendingEpicGate(nil)
}

// ---- Unresolved findings visible in artifact --------------------------------

// TestRunEpicClosureGate_UnresolvedFindingsVisible verifies that after repair
// cycles are exhausted, the artifact's Findings slice still lists all
// unresolved items with their severity, owning scope, and next action so
// operators have actionable information.
func TestRunEpicClosureGate_UnresolvedFindingsVisible(t *testing.T) {
	repoRoot := t.TempDir()
	epicID := "epic-gate-unresolved"

	// Reviewer always returns the same P1 finding so repair never clears it.
	blockingReview := epicGateReviewWithP1Finding
	adapter := runtimefake.New(
		[]runtime.WorkerResult{
			epicGateWorkerResult("epic-repair-" + epicID + "-1"),
		},
		[]runtime.ReviewResult{
			blockingReview("epic-review-" + epicID + "-0"),
			// After repair: still blocking.
			blockingReview("epic-review-" + epicID + "-1"),
		},
	)

	cfg := policy.DefaultConfig()
	cfg.Policy.MaxEpicRepairCycles = 1
	cfg.Verification.EpicClosureCommands = nil

	req := makeEpicGateReq(repoRoot, epicID, adapter)
	req.Config = cfg

	err := runEpicClosureGate(context.Background(), req, cfg, makeEpicGateChildren(), nil)
	if err == nil {
		t.Fatal("expected BlockedRunError when reviewer finding persists after repair")
	}

	var artifact state.EpicClosureArtifact
	if err := state.LoadJSON(epicClosureArtifactPath(repoRoot, req.RunID), &artifact); err != nil {
		t.Fatalf("load artifact: %v", err)
	}

	// At least one finding must remain unresolved.
	hasUnresolved := false
	for _, f := range artifact.Findings {
		if !f.Resolved {
			hasUnresolved = true
			// Each unresolved finding must have severity, title, and next_action.
			if string(f.Severity) == "" {
				t.Errorf("unresolved finding %q missing severity", f.ID)
			}
			if f.Title == "" {
				t.Errorf("unresolved finding %q missing title", f.ID)
			}
			if f.NextAction == "" {
				t.Errorf("unresolved finding %q missing next_action", f.ID)
			}
		}
	}
	if !hasUnresolved {
		t.Error("expected at least one unresolved finding in blocked artifact")
	}

	// Block reason must reference the unresolved finding(s).
	if artifact.BlockReason == "" {
		t.Error("expected non-empty BlockReason when findings are unresolved")
	}
}

// ---- Changed files passed to reviewer ----------------------------------------

// TestRunEpicClosureGate_ChangedFilesPassedToReviewer verifies that the
// changed-files list provided to runEpicClosureGate is surfaced in the reviewer
// instructions so the reviewer can see the full epic diff scope.
func TestRunEpicClosureGate_ChangedFilesPassedToReviewer(t *testing.T) {
	repoRoot := t.TempDir()
	epicID := "epic-gate-changedfiles"

	adapter := runtimefake.New(nil, []runtime.ReviewResult{
		epicGateReviewPassed("epic-review-" + epicID + "-0"),
	})

	cfg := policy.DefaultConfig()
	cfg.Verification.EpicClosureCommands = nil

	req := makeEpicGateReq(repoRoot, epicID, adapter)
	req.Config = cfg

	changedFiles := []string{
		"internal/api/handler.go",
		"internal/worker/processor.go",
		"docs/changelog.md",
	}

	if err := runEpicClosureGate(context.Background(), req, cfg, makeEpicGateChildren(), changedFiles); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	reviewReqs := adapter.ReviewRequests()
	if len(reviewReqs) == 0 {
		t.Fatal("expected at least one reviewer call")
	}
	instructions := reviewReqs[0].Instructions

	// At least some of the changed files should appear in the review instructions.
	foundAny := false
	for _, f := range changedFiles {
		if strings.Contains(instructions, f) {
			foundAny = true
			break
		}
	}
	if !foundAny {
		t.Errorf("reviewer instructions did not mention any changed files from %v", changedFiles)
	}
}
