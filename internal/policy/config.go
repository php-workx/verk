package policy

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"verk/internal/state"

	"gopkg.in/yaml.v3"
)

type SchedulerConfig struct {
	MaxConcurrency int `yaml:"max_concurrency" json:"max_concurrency"`
	MaxDepth       int `yaml:"max_depth" json:"max_depth"`
}

type PolicyConfig struct { //nolint:revive // stuttering name matches Go convention
	ReviewThreshold           state.Severity `yaml:"review_threshold" json:"review_threshold"`
	MaxImplementationAttempts int            `yaml:"max_implementation_attempts" json:"max_implementation_attempts"`
	MaxRepairCycles           int            `yaml:"max_repair_cycles" json:"max_repair_cycles"`
	MaxWaveRepairCycles       int            `yaml:"max_wave_repair_cycles" json:"max_wave_repair_cycles"`
	// MaxEpicRepairCycles bounds the epic closure gate's repair/review loop.
	// A value of 0 disables epic-level repair entirely (the gate still runs
	// broad checks and an epic review, but any blocking finding marks the
	// epic blocked without dispatching a repair worker).
	MaxEpicRepairCycles int  `yaml:"max_epic_repair_cycles" json:"max_epic_repair_cycles"`
	AllowDirtyWorktree  bool `yaml:"allow_dirty_worktree" json:"allow_dirty_worktree"`
}

// RoleProfile describes the runtime, model, and reasoning level that should be
// used to execute a given role (worker or reviewer). Runtime selects the CLI
// adapter (e.g. "claude", "codex"); Model selects the model identifier passed
// to that adapter (e.g. "sonnet", "opus"); Reasoning selects the reasoning
// effort level (e.g. "low", "medium", "high", "xhigh") when the runtime
// supports one.
//
// Role profiles are owned by policy/config; they are intentionally NOT
// derived from ticket frontmatter so that workers cannot silently change
// the execution model by editing ticket metadata.
type RoleProfile struct {
	Runtime   string `yaml:"runtime" json:"runtime"`
	Model     string `yaml:"model" json:"model"`
	Reasoning string `yaml:"reasoning" json:"reasoning"`
}

// IsZero reports whether the profile is entirely unpopulated.
func (p RoleProfile) IsZero() bool {
	return strings.TrimSpace(p.Runtime) == "" &&
		strings.TrimSpace(p.Model) == "" &&
		strings.TrimSpace(p.Reasoning) == ""
}

type RuntimeConfig struct {
	// DefaultRuntime is retained as a backward-compatibility fallback for
	// configs that predate role-specific profiles. When a role profile has
	// no Runtime set, DefaultRuntime is used. Role profiles take precedence.
	DefaultRuntime         string   `yaml:"default_runtime" json:"default_runtime"`
	WorkerTimeoutMinutes   int      `yaml:"worker_timeout_minutes" json:"worker_timeout_minutes"`
	ReviewerTimeoutMinutes int      `yaml:"reviewer_timeout_minutes" json:"reviewer_timeout_minutes"`
	AuthEnvVars            []string `yaml:"auth_env_vars" json:"auth_env_vars"`

	// Worker is the role profile used for worker (implement/repair) attempts.
	Worker RoleProfile `yaml:"worker" json:"worker"`
	// Reviewer is the role profile used for reviewer attempts.
	Reviewer RoleProfile `yaml:"reviewer" json:"reviewer"`
	// WorkerFallback, if set, is used by retry/resume logic when the primary
	// Worker profile's model is unavailable, rate-limited, or repeatedly
	// failing. Optional.
	WorkerFallback RoleProfile `yaml:"worker_fallback" json:"worker_fallback"`
	// ReviewerFallback is the equivalent fallback for the reviewer role.
	ReviewerFallback RoleProfile `yaml:"reviewer_fallback" json:"reviewer_fallback"`
}

// QualityCommand describes a set of shell commands to run from an optional
// subdirectory. Ticket-level quality_commands should stay lightweight. They may
// use auto-fixing format/lint targets so worker verification can repair simple
// hygiene issues before deciding a ticket needs a repair pass.
//
// Path is relative to the repo root; leave empty to run from the repo root.
// Run lists the shell commands to execute sequentially.
//
// Example (monorepo):
//
//	quality_commands:
//	  - path: "."
//	    run: ["just format", "just lint", "just build-check"]
//	  - path: "packages/api"
//	    run: ["cargo clippy -- -D warnings", "cargo test"]
type QualityCommand struct {
	Path string   `yaml:"path" json:"path"`
	Run  []string `yaml:"run" json:"run"`
}

type VerificationConfig struct {
	DefaultTimeoutMinutes int              `yaml:"default_timeout_minutes" json:"default_timeout_minutes"`
	EnvPassthrough        []string         `yaml:"env_passthrough" json:"env_passthrough"`
	QualityCommands       []QualityCommand `yaml:"quality_commands" json:"quality_commands"`
	// WaveCommands run only at wave verification, after the ticket-level
	// quality_commands. Use them for broader gates that are too heavy for each
	// worker/ticket verification but useful before accepting a merged wave.
	WaveCommands []QualityCommand `yaml:"wave_commands" json:"wave_commands"`
	// EpicClosureCommands are broad gates that run only at epic closure —
	// e.g. `go test ./internal/e2e/...` or repo-wide integration suites
	// that would be too expensive to repeat on every ticket and wave. They
	// execute after per-ticket and per-wave verification have already passed
	// and are subject to the same timeout and env_passthrough settings.
	EpicClosureCommands []QualityCommand `yaml:"epic_closure_commands" json:"epic_closure_commands"`
	// EpicStaleWordingTerms are literal strings the epic closure gate
	// should sweep across epic-scoped documentation for. When empty, no
	// stale-wording sweep runs at epic closure. Used to catch
	// cross-ticket inconsistencies (e.g. one ticket renamed "Gitleaks"
	// to "Betterleaks" but another doc still says "Gitleaks").
	EpicStaleWordingTerms []string `yaml:"epic_stale_wording_terms" json:"epic_stale_wording_terms"`
	// EpicClosureDocs lists doc paths the epic closure gate may scan for
	// stale wording and cross-ticket consistency. When empty, a built-in
	// default set (README.md, CONTRIBUTING.md, docs) is used.
	EpicClosureDocs []string `yaml:"epic_closure_docs" json:"epic_closure_docs"`
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
		rawConfig, err := decodeRawYAMLConfig(data)
		if err != nil {
			return Config{}, fmt.Errorf("decode config %q: %w", configPath, err)
		}
		if err := applyConfigYAML(&cfg, data); err != nil {
			return Config{}, fmt.Errorf("decode config %q: %w", configPath, err)
		}
		applyRoleRuntimeDefaultsFromDefaultRuntime(&cfg, rawConfig)
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func decodeRawYAMLConfig(data []byte) (map[string]any, error) {
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func hasYAMLPath(raw map[string]any, path ...string) bool {
	if len(path) == 0 {
		return true
	}
	cur := raw
	for i, key := range path {
		value, ok := cur[key]
		if !ok {
			return false
		}
		if i == len(path)-1 {
			return true
		}
		next, ok := value.(map[string]any)
		if !ok {
			return false
		}
		cur = next
	}
	return true
}

func applyRoleRuntimeDefaultsFromDefaultRuntime(cfg *Config, raw map[string]any) {
	defaultRuntime := strings.TrimSpace(cfg.Runtime.DefaultRuntime)
	if defaultRuntime == "" {
		return
	}

	if !hasYAMLPath(raw, "runtime", "worker") || !hasYAMLPath(raw, "runtime", "worker", "runtime") {
		cfg.Runtime.Worker.Runtime = defaultRuntime
	}
	if !hasYAMLPath(raw, "runtime", "reviewer") || !hasYAMLPath(raw, "runtime", "reviewer", "runtime") {
		cfg.Runtime.Reviewer.Runtime = defaultRuntime
	}
}

func (c Config) Validate() error {
	if c.Scheduler.MaxConcurrency <= 0 {
		return fmt.Errorf("scheduler.max_concurrency must be greater than zero")
	}
	if c.Scheduler.MaxDepth <= 0 {
		return fmt.Errorf("scheduler.max_depth must be greater than zero")
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
	if c.Policy.MaxWaveRepairCycles < 0 {
		return fmt.Errorf("policy.max_wave_repair_cycles must be zero or greater")
	}
	if c.Policy.MaxEpicRepairCycles < 0 {
		return fmt.Errorf("policy.max_epic_repair_cycles must be zero or greater")
	}
	if c.Runtime.WorkerTimeoutMinutes <= 0 {
		return fmt.Errorf("runtime.worker_timeout_minutes must be greater than zero")
	}
	if c.Runtime.ReviewerTimeoutMinutes <= 0 {
		return fmt.Errorf("runtime.reviewer_timeout_minutes must be greater than zero")
	}
	if c.Verification.DefaultTimeoutMinutes <= 0 {
		return fmt.Errorf("verification.default_timeout_minutes must be greater than zero")
	}
	if err := validateStringList(c.Runtime.AuthEnvVars, "runtime.auth_env_vars"); err != nil {
		return err
	}
	if err := validateRoleProfile(c.Runtime.Worker, "runtime.worker"); err != nil {
		return err
	}
	if err := validateRoleProfile(c.Runtime.Reviewer, "runtime.reviewer"); err != nil {
		return err
	}
	if err := validateOptionalRoleProfile(c.Runtime.WorkerFallback, "runtime.worker_fallback"); err != nil {
		return err
	}
	if err := validateOptionalRoleProfile(c.Runtime.ReviewerFallback, "runtime.reviewer_fallback"); err != nil {
		return err
	}
	if err := validateStringList(c.Verification.EnvPassthrough, "verification.env_passthrough"); err != nil {
		return err
	}
	if err := validateQualityCommands(c.Verification.QualityCommands); err != nil {
		return err
	}
	if err := validateWaveCommands(c.Verification.WaveCommands); err != nil {
		return err
	}
	if err := validateEpicClosureCommands(c.Verification.EpicClosureCommands); err != nil {
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

func validateQualityCommands(cmds []QualityCommand) error {
	return validateVerificationCommands(cmds, "verification.quality_commands")
}

func validateWaveCommands(cmds []QualityCommand) error {
	return validateVerificationCommands(cmds, "verification.wave_commands")
}

func validateVerificationCommands(cmds []QualityCommand, field string) error {
	for i, qc := range cmds {
		if len(qc.Run) == 0 {
			return fmt.Errorf("%s[%d] must have at least one command in run", field, i)
		}
		for j, cmd := range qc.Run {
			if strings.TrimSpace(cmd) == "" {
				return fmt.Errorf("%s[%d].run[%d] must not be empty", field, i, j)
			}
		}
		if qc.Path != "" {
			if filepath.IsAbs(qc.Path) {
				return fmt.Errorf("%s[%d].path must be relative, got %q", field, i, qc.Path)
			}
			if strings.HasPrefix(filepath.Clean(qc.Path), "..") {
				return fmt.Errorf("%s[%d].path must not traverse outside repo root, got %q", field, i, qc.Path)
			}
		}
	}
	return nil
}

// validateEpicClosureCommands mirrors validateQualityCommands but reports
// errors under the epic_closure_commands key so operators can locate a
// malformed entry without having to guess which list produced the error.
// Epic closure commands are broad gates that run once at epic closure (for
// example `go test ./internal/e2e/...`), so the same safety rules apply:
// non-empty runs, relative paths, and no traversal outside the repo root.
func validateEpicClosureCommands(cmds []QualityCommand) error {
	return validateVerificationCommands(cmds, "verification.epic_closure_commands")
}

// validateRoleProfile enforces that a primary (required) role profile has
// Runtime, Model, and Reasoning populated. Empty role profiles are rejected
// with a clear field-qualified message so operators can correct the config.
func validateRoleProfile(profile RoleProfile, field string) error {
	if strings.TrimSpace(profile.Runtime) == "" {
		return fmt.Errorf("%s.runtime must not be empty", field)
	}
	if strings.TrimSpace(profile.Model) == "" {
		return fmt.Errorf("%s.model must not be empty", field)
	}
	if strings.TrimSpace(profile.Reasoning) == "" {
		return fmt.Errorf("%s.reasoning must not be empty", field)
	}
	return nil
}

// validateOptionalRoleProfile permits an entirely empty profile (treated as
// "no fallback configured") but requires all three fields when any one is set
// so partially configured fallbacks surface at load time instead of at retry.
func validateOptionalRoleProfile(profile RoleProfile, field string) error {
	if profile.IsZero() {
		return nil
	}
	return validateRoleProfile(profile, field)
}

// EffectiveRoleProfile resolves the final role profile used at execution time.
// DefaultRuntime fills in as a backward-compatibility fallback when the role
// profile has no Runtime explicitly set.
func (c Config) EffectiveRoleProfile(profile RoleProfile) RoleProfile {
	out := profile
	if strings.TrimSpace(out.Runtime) == "" {
		out.Runtime = strings.TrimSpace(c.Runtime.DefaultRuntime)
	}
	return out
}

// EffectiveWorkerProfile returns the worker role profile with fallbacks
// applied. See EffectiveRoleProfile for rules.
func (c Config) EffectiveWorkerProfile() RoleProfile {
	return c.EffectiveRoleProfile(c.Runtime.Worker)
}

// EffectiveReviewerProfile returns the reviewer role profile with fallbacks
// applied. See EffectiveRoleProfile for rules.
func (c Config) EffectiveReviewerProfile() RoleProfile {
	return c.EffectiveRoleProfile(c.Runtime.Reviewer)
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
