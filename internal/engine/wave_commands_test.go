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

	err := runWaveVerificationLoop(context.Background(), makeEpicReq(repoRoot, adapter), cfg, wave, wavePath, nil)
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
