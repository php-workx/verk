package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"verk/internal/policy"

	runtimefake "verk/internal/adapters/runtime/fake"
)

func TestRunWaveVerificationLoop_RunsWaveCommandsInAdditionToQualityCommands(t *testing.T) {
	repoRoot, wavePath, wave := makeWaveVerifyFixture(t)
	adapter := runtimefake.New(nil, nil)
	cfg := policy.DefaultConfig()
	cfg.Verification.QualityCommands = []policy.QualityCommand{
		{Path: ".", Run: []string{"printf quality >> wave-checks.log"}},
	}
	cfg.Verification.WaveCommands = []policy.QualityCommand{
		{Path: ".", Run: []string{"printf wave >> wave-checks.log"}},
	}

	err := runWaveVerificationLoop(context.Background(), makeEpicReq(repoRoot, adapter), cfg, wave, wavePath, nil, "")
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(repoRoot, "wave-checks.log"))
	if err != nil {
		t.Fatalf("read wave checks log: %v", err)
	}
	if got, want := string(content), "qualitywave"; got != want {
		t.Fatalf("expected wave gate to run quality and wave commands, got %q", got)
	}
}

func TestRunWaveVerificationLoop_UsesExplicitWorkDir(t *testing.T) {
	repoRoot, wavePath, wave := makeWaveVerifyFixture(t)
	workDir := filepath.Join(t.TempDir(), "wt")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("mkdir workDir: %v", err)
	}
	adapter := runtimefake.New(nil, nil)
	cfg := policy.DefaultConfig()
	cfg.Verification.QualityCommands = []policy.QualityCommand{
		{Path: ".", Run: []string{"printf scoped >> wave-checks.log"}},
	}

	err := runWaveVerificationLoop(context.Background(), makeEpicReq(repoRoot, adapter), cfg, wave, wavePath, nil, workDir)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "wave-checks.log")); !os.IsNotExist(err) {
		t.Fatalf("expected no repo-root output file when workDir is set, got err=%v", err)
	}
	content, err := os.ReadFile(filepath.Join(workDir, "wave-checks.log"))
	if err != nil {
		t.Fatalf("read workDir wave checks log: %v", err)
	}
	if got, want := string(content), "scoped"; got != want {
		t.Fatalf("expected workDir output %q, got %q", want, got)
	}
}
