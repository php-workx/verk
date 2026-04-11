package policy

import "verk/internal/state"

func DefaultConfig() Config {
	return Config{
		Scheduler: SchedulerConfig{
			MaxConcurrency: 4,
		},
		Policy: PolicyConfig{
			ReviewThreshold:           state.SeverityP2,
			MaxImplementationAttempts: 3,
			MaxRepairCycles:           2,
			AllowDirtyWorktree:        true,
		},
		Verification: VerificationConfig{
			DefaultTimeoutMinutes: 15,
			EnvPassthrough:        []string{"PATH", "HOME", "CI"},
		},
	}
}
