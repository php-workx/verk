package policy

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

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
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	currentSection := ""
	currentListField := ""
	listHasItem := false
	listLine := 0
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		rawLine := scanner.Text()
		trimmedLine := strings.TrimSpace(rawLine)
		if trimmedLine == "" || strings.HasPrefix(trimmedLine, "#") {
			continue
		}

		indent := leadingSpaces(rawLine)
		if indent != 0 && indent != 2 && indent != 4 {
			return fmt.Errorf("line %d: unsupported indentation", lineNum)
		}

		for indent != 4 && currentListField != "" {
			if !listHasItem {
				return fmt.Errorf("line %d: expected list items for %s.%s", lineNum, currentSection, currentListField)
			}
			currentListField = ""
			listHasItem = false
		}

		if indent == 0 {
			if !strings.HasSuffix(trimmedLine, ":") {
				return fmt.Errorf("line %d: expected section declaration", lineNum)
			}
			section := strings.TrimSuffix(trimmedLine, ":")
			if !isKnownSection(section) {
				return fmt.Errorf("line %d: unknown config section %q", lineNum, section)
			}
			currentSection = section
			continue
		}

		if currentSection == "" {
			return fmt.Errorf("line %d: key outside of a section", lineNum)
		}

		if indent == 2 {
			key, value, hasValue, err := splitKeyValue(trimmedLine)
			if err != nil {
				return fmt.Errorf("line %d: %w", lineNum, err)
			}
			if !hasValue {
				if !isListField(currentSection, key) {
					return fmt.Errorf("line %d: key %q must have a value", lineNum, key)
				}
				if err := setListField(cfg, currentSection, key, []string{}); err != nil {
					return fmt.Errorf("line %d: %w", lineNum, err)
				}
				currentListField = key
				listHasItem = false
				listLine = lineNum
				continue
			}
			currentListField = ""
			listHasItem = false
			if isListField(currentSection, key) {
				values, err := parseStringList(value)
				if err != nil {
					return fmt.Errorf("line %d: %w", lineNum, err)
				}
				if err := setListField(cfg, currentSection, key, values); err != nil {
					return fmt.Errorf("line %d: %w", lineNum, err)
				}
				continue
			}
			if err := setScalarField(cfg, currentSection, key, value); err != nil {
				return fmt.Errorf("line %d: %w", lineNum, err)
			}
			continue
		}

		if currentListField == "" {
			return fmt.Errorf("line %d: list item without an active list", lineNum)
		}
		if !strings.HasPrefix(trimmedLine, "- ") {
			return fmt.Errorf("line %d: expected list item", lineNum)
		}
		item, err := parseScalar(strings.TrimSpace(strings.TrimPrefix(trimmedLine, "- ")))
		if err != nil {
			return fmt.Errorf("line %d: %w", lineNum, err)
		}
		if err := appendListField(cfg, currentSection, currentListField, item); err != nil {
			return fmt.Errorf("line %d: %w", lineNum, err)
		}
		listHasItem = true
	}

	if err := scanner.Err(); err != nil {
		return err
	}
	if currentListField != "" && listLine > 0 {
		return fmt.Errorf("line %d: expected list items for %s.%s", listLine, currentSection, currentListField)
	}

	return nil
}

func leadingSpaces(s string) int {
	count := 0
	for _, r := range s {
		if r != ' ' {
			break
		}
		count++
	}
	return count
}

func splitKeyValue(line string) (key, value string, hasValue bool, err error) {
	colon := strings.Index(line, ":")
	if colon < 0 {
		return "", "", false, fmt.Errorf("expected key/value pair")
	}
	key = strings.TrimSpace(line[:colon])
	if key == "" {
		return "", "", false, fmt.Errorf("missing key")
	}
	value = strings.TrimSpace(line[colon+1:])
	if value == "" {
		return key, "", false, nil
	}
	return key, value, true, nil
}

func parseScalar(value string) (string, error) {
	if len(value) >= 2 {
		switch {
		case strings.HasPrefix(value, "\"") && strings.HasSuffix(value, "\""):
			return strconv.Unquote(value)
		case strings.HasPrefix(value, "'") && strings.HasSuffix(value, "'"):
			return value[1 : len(value)-1], nil
		}
	}
	return value, nil
}

func parseStringList(value string) ([]string, error) {
	if value == "[]" {
		return []string{}, nil
	}
	if !strings.HasPrefix(value, "[") || !strings.HasSuffix(value, "]") {
		return nil, fmt.Errorf("expected list value or block list")
	}
	inner := strings.TrimSpace(value[1 : len(value)-1])
	if inner == "" {
		return []string{}, nil
	}
	parts := strings.Split(inner, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		item, err := parseScalar(strings.TrimSpace(part))
		if err != nil {
			return nil, err
		}
		values = append(values, item)
	}
	return values, nil
}

func isKnownSection(section string) bool {
	switch section {
	case "scheduler", "policy", "runtime", "verification", "logging":
		return true
	default:
		return false
	}
}

func isListField(section, key string) bool {
	switch section {
	case "runtime":
		return key == "auth_env_vars"
	case "verification":
		return key == "env_passthrough"
	default:
		return false
	}
}

func setScalarField(cfg *Config, section, key, value string) error {
	switch section {
	case "scheduler":
		if key != "max_concurrency" {
			return fmt.Errorf("unknown scheduler field %q", key)
		}
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid integer for scheduler.max_concurrency: %w", err)
		}
		cfg.Scheduler.MaxConcurrency = n
		return nil
	case "policy":
		switch key {
		case "review_threshold":
			value, err := parseScalar(value)
			if err != nil {
				return fmt.Errorf("invalid policy.review_threshold: %w", err)
			}
			cfg.Policy.ReviewThreshold = state.Severity(value)
			return nil
		case "max_implementation_attempts":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid integer for policy.max_implementation_attempts: %w", err)
			}
			cfg.Policy.MaxImplementationAttempts = n
			return nil
		case "max_repair_cycles":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid integer for policy.max_repair_cycles: %w", err)
			}
			cfg.Policy.MaxRepairCycles = n
			return nil
		case "allow_dirty_worktree":
			b, err := strconv.ParseBool(value)
			if err != nil {
				return fmt.Errorf("invalid boolean for policy.allow_dirty_worktree: %w", err)
			}
			cfg.Policy.AllowDirtyWorktree = b
			return nil
		default:
			return fmt.Errorf("unknown policy field %q", key)
		}
	case "runtime":
		switch key {
		case "default_runtime":
			value, err := parseScalar(value)
			if err != nil {
				return fmt.Errorf("invalid runtime.default_runtime: %w", err)
			}
			cfg.Runtime.DefaultRuntime = value
			return nil
		case "worker_timeout_minutes":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid integer for runtime.worker_timeout_minutes: %w", err)
			}
			cfg.Runtime.WorkerTimeoutMinutes = n
			return nil
		case "reviewer_timeout_minutes":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid integer for runtime.reviewer_timeout_minutes: %w", err)
			}
			cfg.Runtime.ReviewerTimeoutMinutes = n
			return nil
		default:
			return fmt.Errorf("unknown runtime field %q", key)
		}
	case "verification":
		switch key {
		case "default_timeout_minutes":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid integer for verification.default_timeout_minutes: %w", err)
			}
			cfg.Verification.DefaultTimeoutMinutes = n
			return nil
		default:
			return fmt.Errorf("unknown verification field %q", key)
		}
	case "logging":
		switch key {
		case "level":
			value, err := parseScalar(value)
			if err != nil {
				return fmt.Errorf("invalid logging.level: %w", err)
			}
			cfg.Logging.Level = value
			return nil
		case "artifact_retention":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid integer for logging.artifact_retention: %w", err)
			}
			cfg.Logging.ArtifactRetention = n
			return nil
		default:
			return fmt.Errorf("unknown logging field %q", key)
		}
	default:
		return fmt.Errorf("unknown section %q", section)
	}
}

func setListField(cfg *Config, section, key string, values []string) error {
	switch section {
	case "runtime":
		if key != "auth_env_vars" {
			return fmt.Errorf("unknown runtime field %q", key)
		}
		cfg.Runtime.AuthEnvVars = values
		return nil
	case "verification":
		if key != "env_passthrough" {
			return fmt.Errorf("unknown verification field %q", key)
		}
		cfg.Verification.EnvPassthrough = values
		return nil
	default:
		return fmt.Errorf("unknown list field %s.%s", section, key)
	}
}

func appendListField(cfg *Config, section, key, value string) error {
	switch section {
	case "runtime":
		if key != "auth_env_vars" {
			return fmt.Errorf("unknown runtime field %q", key)
		}
		cfg.Runtime.AuthEnvVars = append(cfg.Runtime.AuthEnvVars, value)
		return nil
	case "verification":
		if key != "env_passthrough" {
			return fmt.Errorf("unknown verification field %q", key)
		}
		cfg.Verification.EnvPassthrough = append(cfg.Verification.EnvPassthrough, value)
		return nil
	default:
		return fmt.Errorf("unknown list field %s.%s", section, key)
	}
}
