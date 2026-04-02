package policy

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"verk/internal/state"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Scheduler.MaxConcurrency != 4 {
		t.Fatalf("expected scheduler max concurrency 4, got %d", cfg.Scheduler.MaxConcurrency)
	}
	if cfg.Policy.ReviewThreshold != state.SeverityP2 {
		t.Fatalf("expected default review threshold P2, got %q", cfg.Policy.ReviewThreshold)
	}
	if cfg.Policy.MaxImplementationAttempts != 3 {
		t.Fatalf("expected default implementation attempts 3, got %d", cfg.Policy.MaxImplementationAttempts)
	}
	if cfg.Policy.MaxRepairCycles != 2 {
		t.Fatalf("expected default repair cycles 2, got %d", cfg.Policy.MaxRepairCycles)
	}
	if cfg.Verification.DefaultTimeoutMinutes != 15 {
		t.Fatalf("expected default verification timeout 15, got %d", cfg.Verification.DefaultTimeoutMinutes)
	}
	if !reflect.DeepEqual(cfg.Verification.EnvPassthrough, []string{"PATH", "HOME", "CI"}) {
		t.Fatalf("unexpected verification env passthrough: %#v", cfg.Verification.EnvPassthrough)
	}
}

func TestLoadConfig_ReturnsDefaultsWhenConfigMissing(t *testing.T) {
	repoRoot := t.TempDir()

	cfg, err := LoadConfig(repoRoot)
	if err != nil {
		t.Fatalf("expected missing config to load defaults, got error: %v", err)
	}

	if got, want := cfg, DefaultConfig(); !reflect.DeepEqual(got, want) {
		t.Fatalf("expected default config, got %#v", got)
	}
}

func TestLoadConfig_MergesYAMLWithDefaults(t *testing.T) {
	repoRoot := t.TempDir()
	configDir := filepath.Join(repoRoot, ".verk")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}

	configYAML := []byte(`
scheduler:
  max_concurrency: 8
policy:
  allow_dirty_worktree: true
runtime:
  default_runtime: claude
  auth_env_vars:
    - VERK_API_KEY
verification:
  env_passthrough:
    - PATH
    - HOME
logging:
  level: debug
  artifact_retention: 14
`)
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), configYAML, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfig(repoRoot)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.Scheduler.MaxConcurrency != 8 {
		t.Fatalf("expected scheduler max concurrency 8, got %d", cfg.Scheduler.MaxConcurrency)
	}
	if cfg.Policy.AllowDirtyWorktree != true {
		t.Fatalf("expected allow_dirty_worktree true")
	}
	if cfg.Policy.ReviewThreshold != state.SeverityP2 {
		t.Fatalf("expected default review threshold to remain P2, got %q", cfg.Policy.ReviewThreshold)
	}
	if cfg.Policy.MaxImplementationAttempts != 3 {
		t.Fatalf("expected default implementation attempts to remain 3, got %d", cfg.Policy.MaxImplementationAttempts)
	}
	if cfg.Runtime.DefaultRuntime != "claude" {
		t.Fatalf("expected runtime default_runtime claude, got %q", cfg.Runtime.DefaultRuntime)
	}
	if !reflect.DeepEqual(cfg.Runtime.AuthEnvVars, []string{"VERK_API_KEY"}) {
		t.Fatalf("unexpected runtime auth env vars: %#v", cfg.Runtime.AuthEnvVars)
	}
	if !reflect.DeepEqual(cfg.Verification.EnvPassthrough, []string{"PATH", "HOME"}) {
		t.Fatalf("unexpected verification env passthrough: %#v", cfg.Verification.EnvPassthrough)
	}
	if cfg.Logging.Level != "debug" {
		t.Fatalf("expected logging level debug, got %q", cfg.Logging.Level)
	}
	if cfg.Logging.ArtifactRetention != 14 {
		t.Fatalf("expected artifact retention 14, got %d", cfg.Logging.ArtifactRetention)
	}
}

func TestLoadConfig_RejectsUnknownFields(t *testing.T) {
	repoRoot := t.TempDir()
	configDir := filepath.Join(repoRoot, ".verk")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}

	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte("schedulr:\n  max_concurrency: 8\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if _, err := LoadConfig(repoRoot); err == nil {
		t.Fatalf("expected unknown field to be rejected")
	}
}

func TestEffectiveReviewThreshold_Precedence(t *testing.T) {
	cfg := DefaultConfig()
	ticket := state.SeverityP3
	cli := state.SeverityP1

	if got := EffectiveReviewThreshold(&cli, &ticket, cfg.Policy.ReviewThreshold); got != state.SeverityP1 {
		t.Fatalf("expected CLI threshold to win, got %q", got)
	}
	if got := EffectiveReviewThreshold(nil, &ticket, cfg.Policy.ReviewThreshold); got != state.SeverityP3 {
		t.Fatalf("expected ticket threshold to win when CLI absent, got %q", got)
	}
	if got := EffectiveReviewThreshold(nil, nil, cfg.Policy.ReviewThreshold); got != state.SeverityP2 {
		t.Fatalf("expected config threshold fallback, got %q", got)
	}
}
