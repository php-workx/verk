package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"verk/internal/adapters/runtime"
	runtimefake "verk/internal/adapters/runtime/fake"
	"verk/internal/policy"
	"verk/internal/state"
)

var waveTestStart = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func validWorkerResult(leaseID, artifactPath string) runtime.WorkerResult {
	return runtime.WorkerResult{
		Status:             runtime.WorkerStatusDone,
		RetryClass:         runtime.RetryClassTerminal,
		LeaseID:            leaseID,
		StartedAt:          waveTestStart,
		FinishedAt:         waveTestStart.Add(time.Second),
		ResultArtifactPath: artifactPath,
	}
}

// makeWaveVerifyFixture creates a minimal temp repo root, wave artifact, and wave path
// for use in runWaveVerificationLoop tests.
func makeWaveVerifyFixture(t *testing.T) (repoRoot, wavePath string, wave *state.WaveArtifact) {
	t.Helper()
	repoRoot = t.TempDir()
	waveDir := filepath.Join(repoRoot, ".verk", "runs", "run-test", "waves")
	if err := os.MkdirAll(waveDir, 0o755); err != nil {
		t.Fatal(err)
	}
	wavePath = filepath.Join(waveDir, "wave-1.json")
	wave = &state.WaveArtifact{
		ArtifactMeta: state.ArtifactMeta{RunID: "run-test"},
		WaveID:       "wave-1",
		Ordinal:      1,
		Status:       state.WaveStatusAccepted,
		Acceptance:   map[string]any{},
	}
	if err := state.SaveJSONAtomic(wavePath, wave); err != nil {
		t.Fatal(err)
	}
	return repoRoot, wavePath, wave
}

func makeEpicReq(repoRoot string, adapter *runtimefake.Adapter) RunEpicRequest {
	return RunEpicRequest{
		RepoRoot: repoRoot,
		RunID:    "run-test",
		Adapter:  adapter,
	}
}

func qualityCmd(cmd string) policy.QualityCommand {
	return policy.QualityCommand{Path: ".", Run: []string{cmd}}
}

func TestRunWaveVerificationLoop_NoQualityCommands(t *testing.T) {
	repoRoot, wavePath, wave := makeWaveVerifyFixture(t)
	adapter := runtimefake.New(nil, nil)
	cfg := policy.DefaultConfig()
	cfg.Verification.QualityCommands = nil

	err := runWaveVerificationLoop(context.Background(), makeEpicReq(repoRoot, adapter), cfg, wave, wavePath, nil)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if len(adapter.WorkerRequests()) != 0 {
		t.Errorf("expected no worker calls, got %d", len(adapter.WorkerRequests()))
	}
	// No verification fields set when commands are absent
	if _, ok := wave.Acceptance["wave_verification_passed"]; ok {
		t.Error("expected wave_verification_passed not to be set when no commands configured")
	}
}

func TestRunWaveVerificationLoop_PassesFirstTry(t *testing.T) {
	repoRoot, wavePath, wave := makeWaveVerifyFixture(t)
	adapter := runtimefake.New(nil, nil)
	cfg := policy.DefaultConfig()
	cfg.Verification.QualityCommands = []policy.QualityCommand{qualityCmd("true")}

	err := runWaveVerificationLoop(context.Background(), makeEpicReq(repoRoot, adapter), cfg, wave, wavePath, nil)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if len(adapter.WorkerRequests()) != 0 {
		t.Errorf("expected no worker calls, got %d", len(adapter.WorkerRequests()))
	}
	if wave.Acceptance["wave_verification_passed"] != true {
		t.Errorf("expected wave_verification_passed=true, got %v", wave.Acceptance["wave_verification_passed"])
	}
	if wave.Acceptance["wave_verification_cycles"] != 0 {
		t.Errorf("expected wave_verification_cycles=0, got %v", wave.Acceptance["wave_verification_cycles"])
	}
}

func TestRunWaveVerificationLoop_MaxWaveRepairCyclesZero_FailsImmediately(t *testing.T) {
	repoRoot, wavePath, wave := makeWaveVerifyFixture(t)
	adapter := runtimefake.New(nil, nil)
	cfg := policy.DefaultConfig()
	cfg.Verification.QualityCommands = []policy.QualityCommand{qualityCmd("false")}
	cfg.Policy.MaxWaveRepairCycles = 0

	err := runWaveVerificationLoop(context.Background(), makeEpicReq(repoRoot, adapter), cfg, wave, wavePath, nil)

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if len(adapter.WorkerRequests()) != 0 {
		t.Errorf("expected no worker calls with MaxWaveRepairCycles=0, got %d", len(adapter.WorkerRequests()))
	}
	if wave.Acceptance["wave_verification_passed"] != false {
		t.Errorf("expected wave_verification_passed=false, got %v", wave.Acceptance["wave_verification_passed"])
	}
}

func TestRunWaveVerificationLoop_RepairSucceedsOnFirstCycle(t *testing.T) {
	repoRoot, wavePath, wave := makeWaveVerifyFixture(t)

	artifactPath := filepath.Join(repoRoot, "worker-result.json")
	if err := os.WriteFile(artifactPath, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	adapter := runtimefake.New([]runtime.WorkerResult{
		validWorkerResult("wave-repair-wave-1-1", artifactPath),
	}, nil)

	// First verify fails; after repair, use "true" — we simulate this by having
	// the repair command write a sentinel file that subsequent steps don't use.
	// Instead, use a script: first call returns exit 1, then exit 0.
	// Since RunQualityCommands runs real commands, we use a file-based toggle:
	// create a file to signal "first call", remove it on first call, so second call passes.
	sentinelPath := filepath.Join(repoRoot, "first_call")
	if err := os.WriteFile(sentinelPath, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	toggleScript := filepath.Join(repoRoot, "toggle.sh")
	script := "#!/bin/sh\nif [ -f " + sentinelPath + " ]; then rm " + sentinelPath + "; exit 1; fi\nexit 0\n"
	if err := os.WriteFile(toggleScript, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := policy.DefaultConfig()
	cfg.Verification.QualityCommands = []policy.QualityCommand{{Path: ".", Run: []string{toggleScript}}}
	cfg.Policy.MaxWaveRepairCycles = 3

	err := runWaveVerificationLoop(context.Background(), makeEpicReq(repoRoot, adapter), cfg, wave, wavePath, []string{"some/file.go"})
	if err != nil {
		t.Fatalf("expected nil error after repair, got: %v", err)
	}
	workerReqs := adapter.WorkerRequests()
	if len(workerReqs) != 1 {
		t.Fatalf("expected 1 worker call, got %d", len(workerReqs))
	}
	if workerReqs[0].WaveID != "wave-1" {
		t.Errorf("expected WaveID=wave-1, got %q", workerReqs[0].WaveID)
	}
	if wave.Acceptance["wave_verification_passed"] != true {
		t.Errorf("expected wave_verification_passed=true, got %v", wave.Acceptance["wave_verification_passed"])
	}
	if wave.Acceptance["wave_verification_cycles"] != 1 {
		t.Errorf("expected wave_verification_cycles=1, got %v", wave.Acceptance["wave_verification_cycles"])
	}
}

func TestRunWaveVerificationLoop_ExhaustsRepairCycles(t *testing.T) {
	repoRoot, wavePath, wave := makeWaveVerifyFixture(t)

	artifactPath := filepath.Join(repoRoot, "worker-result.json")
	if err := os.WriteFile(artifactPath, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	adapter := runtimefake.New([]runtime.WorkerResult{
		validWorkerResult("wave-repair-wave-1-1", artifactPath),
		validWorkerResult("wave-repair-wave-1-2", artifactPath),
	}, nil)

	cfg := policy.DefaultConfig()
	cfg.Verification.QualityCommands = []policy.QualityCommand{qualityCmd("false")}
	cfg.Policy.MaxWaveRepairCycles = 2

	err := runWaveVerificationLoop(context.Background(), makeEpicReq(repoRoot, adapter), cfg, wave, wavePath, nil)

	if err == nil {
		t.Fatal("expected error after exhausting repair cycles, got nil")
	}
	if len(adapter.WorkerRequests()) != 2 {
		t.Errorf("expected 2 worker calls (one per cycle), got %d", len(adapter.WorkerRequests()))
	}
	if wave.Acceptance["wave_verification_passed"] != false {
		t.Errorf("expected wave_verification_passed=false, got %v", wave.Acceptance["wave_verification_passed"])
	}
	if wave.Acceptance["wave_verification_cycles"] != 2 {
		t.Errorf("expected wave_verification_cycles=2, got %v", wave.Acceptance["wave_verification_cycles"])
	}
}

func TestRunWaveVerificationLoop_WorkerError_Aborts(t *testing.T) {
	repoRoot, wavePath, wave := makeWaveVerifyFixture(t)

	adapter := runtimefake.New(nil, nil) // no scripted results → returns ErrNoScriptedWorkerResult

	cfg := policy.DefaultConfig()
	cfg.Verification.QualityCommands = []policy.QualityCommand{qualityCmd("false")}
	cfg.Policy.MaxWaveRepairCycles = 3

	err := runWaveVerificationLoop(context.Background(), makeEpicReq(repoRoot, adapter), cfg, wave, wavePath, nil)

	if err == nil {
		t.Fatal("expected error when worker call fails, got nil")
	}
	// Only one worker call attempted before aborting
	if len(adapter.WorkerRequests()) != 1 {
		t.Errorf("expected 1 worker call before abort, got %d", len(adapter.WorkerRequests()))
	}
}

func TestRunWaveVerificationLoop_RepairWaveIDAndLeaseID(t *testing.T) {
	repoRoot, wavePath, wave := makeWaveVerifyFixture(t)

	artifactPath := filepath.Join(repoRoot, "worker-result2.json")
	if err := os.WriteFile(artifactPath, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	adapter := runtimefake.New([]runtime.WorkerResult{
		validWorkerResult("wave-repair-wave-1-1", artifactPath),
	}, nil)

	sentinelPath := filepath.Join(repoRoot, "first_call2")
	if err := os.WriteFile(sentinelPath, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	toggleScript := filepath.Join(repoRoot, "toggle2.sh")
	script := "#!/bin/sh\nif [ -f " + sentinelPath + " ]; then rm " + sentinelPath + "; exit 1; fi\nexit 0\n"
	if err := os.WriteFile(toggleScript, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := policy.DefaultConfig()
	cfg.Verification.QualityCommands = []policy.QualityCommand{{Path: ".", Run: []string{toggleScript}}}

	if err := runWaveVerificationLoop(context.Background(), makeEpicReq(repoRoot, adapter), cfg, wave, wavePath, nil); err != nil {
		t.Fatal(err)
	}

	reqs := adapter.WorkerRequests()
	if len(reqs) == 0 {
		t.Fatal("expected worker request")
	}
	req := reqs[0]
	if req.WaveID != "wave-1" {
		t.Errorf("WaveID = %q, want wave-1", req.WaveID)
	}
	if req.TicketID != "" {
		t.Errorf("TicketID should be empty for wave repair, got %q", req.TicketID)
	}
	if req.LeaseID != "wave-repair-wave-1-1" {
		t.Errorf("LeaseID = %q, want wave-repair-wave-1-1", req.LeaseID)
	}
}

func TestPendingWaveVerificationCursor(t *testing.T) {
	cursor := map[string]any{}

	setPendingWaveVerification(cursor, "wave-3")
	id, ok := pendingWaveVerificationID(cursor)
	if !ok || id != "wave-3" {
		t.Errorf("expected pending wave-3, got ok=%v id=%q", ok, id)
	}

	clearPendingWaveVerification(cursor)
	_, ok = pendingWaveVerificationID(cursor)
	if ok {
		t.Error("expected pending wave verification to be cleared")
	}
}

func TestPendingWaveVerificationCursor_NilCursor(t *testing.T) {
	setPendingWaveVerification(nil, "wave-1") // must not panic
	clearPendingWaveVerification(nil)         // must not panic

	_, ok := pendingWaveVerificationID(nil)
	if ok {
		t.Error("expected false for nil cursor")
	}
}
