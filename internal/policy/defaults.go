package policy

import "verk/internal/state"

func DefaultConfig() Config {
	return Config{
		Scheduler: SchedulerConfig{
			MaxConcurrency: 4,
			MaxDepth:       3,
		},
		Policy: PolicyConfig{
			ReviewThreshold:           state.SeverityP2,
			MaxImplementationAttempts: 3,
			MaxRepairCycles:           5,
			MaxWaveRepairCycles:       3,
			AllowDirtyWorktree:        true,
		},
		Runtime: RuntimeConfig{
			DefaultRuntime:         "claude",
			WorkerTimeoutMinutes:   30,
			ReviewerTimeoutMinutes: 15,
			Worker: RoleProfile{
				Runtime:   "claude",
				Model:     "sonnet",
				Reasoning: "high",
			},
			Reviewer: RoleProfile{
				Runtime:   "claude",
				Model:     "opus",
				Reasoning: "xhigh",
			},
		},
		Verification: VerificationConfig{
			DefaultTimeoutMinutes: 15,
			EnvPassthrough:        []string{"PATH", "HOME", "CI"},
		},
	}
}
