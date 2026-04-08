package policy

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"verk/internal/state"
)

type SchedulerConfig struct {
	MaxConcurrency int `yaml:"max_concurrency" json:"max_concurrency"`
}

type PolicyConfig struct {
	ReviewThreshold           state.Severity `yaml:"review_threshold" json:"review_threshold"`
	MaxImplementationAttempts int            `yaml:"max_implementation_attempts" json:"max_implementation_attempts"`
	MaxRepairCycles           int            `yaml:"max_repair_cycles" json:"max_repair_cycles"`
	AllowDirtyWorktree        bool           `yaml:"allow_dirty_worktree" json:"allow_dirty_worktree"`
}

type RuntimeConfig struct {
	DefaultRuntime         string   `yaml:"default_runtime" json:"default_runtime"`
	WorkerTimeoutMinutes   int      `yaml:"worker_timeout_minutes" json:"worker_timeout_minutes"`
	ReviewerTimeoutMinutes int      `yaml:"reviewer_timeout_minutes" json:"reviewer_timeout_minutes"`
	AuthEnvVars            []string `yaml:"auth_env_vars" json:"auth_env_vars"`
}

type VerificationConfig struct {
	DefaultTimeoutMinutes int      `yaml:"default_timeout_minutes" json:"default_timeout_minutes"`
	EnvPassthrough        []string `yaml:"env_passthrough" json:"env_passthrough"`
}

type LoggingConfig struct {
	Level             string `yaml:"level" json:"level"`
	ArtifactRetention int    `yaml:"artifact_retention" json:"artifact_retention"`
}

type Config struct {
	Scheduler    SchedulerConfig    `yaml:"scheduler" json:"scheduler"`
	Policy       PolicyConfig       `yaml:"policy" json:"policy"`
	Runtime      RuntimeConfig      `yaml:"runtime" json:"runtime"`
	Verification VerificationConfig `yaml:"verification" json:"verification"`
	Logging      LoggingConfig      `yaml:"logging" json:"logging"`
}

func LoadConfig(repoRoot string) (Config, error) {
	cfg := DefaultConfig()
	configPath := filepath.Join(repoRoot, ".verk", "config.yaml")

	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return Config{}, fmt.Errorf("read config %q: %w", configPath, err)
	}

	if strings.TrimSpace(string(data)) != "" {
		if err := applyConfigYAML(&cfg, data); err != nil {
			return Config{}, fmt.Errorf("decode config %q: %w", configPath, err)
		}
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func (c Config) Validate() error {
	if c.Scheduler.MaxConcurrency <= 0 {
		return fmt.Errorf("scheduler.max_concurrency must be greater than zero")
	}
	if err := validateSeverity(c.Policy.ReviewThreshold, "policy.review_threshold"); err != nil {
		return err
	}
	if c.Policy.MaxImplementationAttempts <= 0 {
		return fmt.Errorf("policy.max_implementation_attempts must be greater than zero")
	}
	if c.Policy.MaxRepairCycles < 0 {
		return fmt.Errorf("policy.max_repair_cycles must be zero or greater")
	}
	if c.Verification.DefaultTimeoutMinutes <= 0 {
		return fmt.Errorf("verification.default_timeout_minutes must be greater than zero")
	}
	if err := validateStringList(c.Runtime.AuthEnvVars, "runtime.auth_env_vars"); err != nil {
		return err
	}
	if err := validateStringList(c.Verification.EnvPassthrough, "verification.env_passthrough"); err != nil {
		return err
	}
	if c.Logging.ArtifactRetention < 0 {
		return fmt.Errorf("logging.artifact_retention must be zero or greater")
	}
	return nil
}

func (c Config) EffectiveReviewThreshold(cliOverride, ticketThreshold *state.Severity) state.Severity {
	return EffectiveReviewThreshold(cliOverride, ticketThreshold, c.Policy.ReviewThreshold)
}

func EffectiveReviewThreshold(cliOverride, ticketThreshold *state.Severity, configThreshold state.Severity) state.Severity {
	if cliOverride != nil {
		return *cliOverride
	}
	if ticketThreshold != nil {
		return *ticketThreshold
	}
	return configThreshold
}

func validateSeverity(severity state.Severity, field string) error {
	switch severity {
	case state.SeverityP0, state.SeverityP1, state.SeverityP2, state.SeverityP3, state.SeverityP4:
		return nil
	default:
		return fmt.Errorf("%s must be one of P0, P1, P2, P3, or P4", field)
	}
}

func validateStringList(values []string, field string) error {
	for i, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s[%d] must not be empty", field, i)
		}
	}
	return nil
}

func applyConfigYAML(cfg *Config, data []byte) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil {
		return err
	}
	return nil
}
