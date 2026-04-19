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
	if cfg.Policy.MaxRepairCycles != 5 {
		t.Fatalf("expected default repair cycles 5, got %d", cfg.Policy.MaxRepairCycles)
	}
	if cfg.Policy.MaxWaveRepairCycles != 3 {
		t.Fatalf("expected default wave repair cycles 3, got %d", cfg.Policy.MaxWaveRepairCycles)
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

func TestValidate_RejectsNonPositiveRuntimeTimeouts(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantMsg string
	}{
		{
			name:    "worker timeout zero",
			mutate:  func(c *Config) { c.Runtime.WorkerTimeoutMinutes = 0 },
			wantMsg: "runtime.worker_timeout_minutes must be greater than zero",
		},
		{
			name:    "worker timeout negative",
			mutate:  func(c *Config) { c.Runtime.WorkerTimeoutMinutes = -1 },
			wantMsg: "runtime.worker_timeout_minutes must be greater than zero",
		},
		{
			name:    "reviewer timeout zero",
			mutate:  func(c *Config) { c.Runtime.ReviewerTimeoutMinutes = 0 },
			wantMsg: "runtime.reviewer_timeout_minutes must be greater than zero",
		},
		{
			name:    "reviewer timeout negative",
			mutate:  func(c *Config) { c.Runtime.ReviewerTimeoutMinutes = -5 },
			wantMsg: "runtime.reviewer_timeout_minutes must be greater than zero",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tc.mutate(&cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("expected validation error, got nil")
			}
			if err.Error() != tc.wantMsg {
				t.Fatalf("expected error %q, got %q", tc.wantMsg, err.Error())
			}
		})
	}
}

func TestValidate_AcceptsPositiveRuntimeTimeouts(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Runtime.WorkerTimeoutMinutes = 1
	cfg.Runtime.ReviewerTimeoutMinutes = 1
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid config, got error: %v", err)
	}
}

func TestValidate_RejectsNegativeMaxWaveRepairCycles(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Policy.MaxWaveRepairCycles = -1
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for negative max_wave_repair_cycles, got nil")
	}
	if err.Error() != "policy.max_wave_repair_cycles must be zero or greater" {
		t.Fatalf("unexpected error message: %q", err.Error())
	}
}

func TestDefaultConfig_RoleProfiles(t *testing.T) {
	cfg := DefaultConfig()
	// Worker must default to claude/sonnet/high — this is the benchmark-critical
	// pairing that makes runs reproducible without relying on CLI defaults.
	wantWorker := RoleProfile{Runtime: "claude", Model: "sonnet", Reasoning: "high"}
	if cfg.Runtime.Worker != wantWorker {
		t.Fatalf("unexpected default worker profile: got %+v want %+v", cfg.Runtime.Worker, wantWorker)
	}
	// Reviewer must default to claude/opus/xhigh so reviewer judgments are
	// higher-effort than worker implementations by default.
	wantReviewer := RoleProfile{Runtime: "claude", Model: "opus", Reasoning: "xhigh"}
	if cfg.Runtime.Reviewer != wantReviewer {
		t.Fatalf("unexpected default reviewer profile: got %+v want %+v", cfg.Runtime.Reviewer, wantReviewer)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config must validate cleanly, got: %v", err)
	}
}

func TestEffectiveWorkerAndReviewerProfile_Defaults(t *testing.T) {
	cfg := DefaultConfig()
	worker := cfg.EffectiveWorkerProfile()
	reviewer := cfg.EffectiveReviewerProfile()
	if worker.Runtime != "claude" || worker.Model != "sonnet" || worker.Reasoning != "high" {
		t.Fatalf("effective worker profile drift: %+v", worker)
	}
	if reviewer.Runtime != "claude" || reviewer.Model != "opus" || reviewer.Reasoning != "xhigh" {
		t.Fatalf("effective reviewer profile drift: %+v", reviewer)
	}
}

func TestValidate_RejectsEmptyRoleProfiles(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantMsg string
	}{
		{
			name:    "worker runtime empty",
			mutate:  func(c *Config) { c.Runtime.Worker.Runtime = "" },
			wantMsg: "runtime.worker.runtime must not be empty",
		},
		{
			name:    "worker model empty",
			mutate:  func(c *Config) { c.Runtime.Worker.Model = "" },
			wantMsg: "runtime.worker.model must not be empty",
		},
		{
			name:    "worker reasoning empty",
			mutate:  func(c *Config) { c.Runtime.Worker.Reasoning = "" },
			wantMsg: "runtime.worker.reasoning must not be empty",
		},
		{
			name:    "reviewer runtime empty",
			mutate:  func(c *Config) { c.Runtime.Reviewer.Runtime = "" },
			wantMsg: "runtime.reviewer.runtime must not be empty",
		},
		{
			name:    "reviewer model empty",
			mutate:  func(c *Config) { c.Runtime.Reviewer.Model = "" },
			wantMsg: "runtime.reviewer.model must not be empty",
		},
		{
			name:    "reviewer reasoning empty",
			mutate:  func(c *Config) { c.Runtime.Reviewer.Reasoning = "" },
			wantMsg: "runtime.reviewer.reasoning must not be empty",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tc.mutate(&cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("expected validation error for %s", tc.name)
			}
			if err.Error() != tc.wantMsg {
				t.Fatalf("expected error %q, got %q", tc.wantMsg, err.Error())
			}
		})
	}
}

func TestValidate_PartialFallbackProfileRejected(t *testing.T) {
	cfg := DefaultConfig()
	// Populate only one field of the fallback profile — a partial fallback is
	// almost certainly a config typo, and the validator should surface it at
	// load time rather than at retry time when a real model is unavailable.
	cfg.Runtime.WorkerFallback = RoleProfile{Runtime: "codex"}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected partial worker fallback profile to fail validation")
	}
	if err.Error() != "runtime.worker_fallback.model must not be empty" {
		t.Fatalf("unexpected error: %q", err.Error())
	}
}

func TestValidate_EmptyFallbackProfileAllowed(t *testing.T) {
	cfg := DefaultConfig()
	// A fully-zero fallback profile means "no fallback configured"; it must
	// pass validation so operators are not forced to configure fallbacks.
	cfg.Runtime.WorkerFallback = RoleProfile{}
	cfg.Runtime.ReviewerFallback = RoleProfile{}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected empty fallback profiles to pass validation, got %v", err)
	}
}

func TestLoadConfig_ExplicitRoleProfilesOverrideDefaults(t *testing.T) {
	repoRoot := t.TempDir()
	configDir := filepath.Join(repoRoot, ".verk")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	// The YAML overrides every role-profile field so the loaded config must
	// report the explicit selections, not the claude/sonnet/high fallback.
	configYAML := []byte(`
runtime:
  default_runtime: claude
  worker_timeout_minutes: 30
  reviewer_timeout_minutes: 15
  worker:
    runtime: codex
    model: gpt-5-mini
    reasoning: medium
  reviewer:
    runtime: claude
    model: sonnet
    reasoning: high
  worker_fallback:
    runtime: claude
    model: sonnet
    reasoning: high
`)
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), configYAML, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadConfig(repoRoot)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Runtime.Worker != (RoleProfile{Runtime: "codex", Model: "gpt-5-mini", Reasoning: "medium"}) {
		t.Fatalf("worker override not applied: %+v", cfg.Runtime.Worker)
	}
	if cfg.Runtime.Reviewer != (RoleProfile{Runtime: "claude", Model: "sonnet", Reasoning: "high"}) {
		t.Fatalf("reviewer override not applied: %+v", cfg.Runtime.Reviewer)
	}
	if cfg.Runtime.WorkerFallback != (RoleProfile{Runtime: "claude", Model: "sonnet", Reasoning: "high"}) {
		t.Fatalf("worker fallback not applied: %+v", cfg.Runtime.WorkerFallback)
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
