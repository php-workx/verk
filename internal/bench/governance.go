package bench

import "fmt"

// SamplingMode constants for benchmark suite classification.
const (
	SamplingModeSmoke      = "smoke"
	SamplingModeRegression = "regression"
	SamplingModeHoldout    = "holdout"
	SamplingModePublic     = "public"
)

// minPublicTaskCount is the minimum number of tasks required for a public suite
// for uncertainty reporting to be statistically meaningful.
const minPublicTaskCount = 30

// SuiteClaimLabel returns the report-safe claim level for a suite sampling mode.
func SuiteClaimLabel(mode string) string {
	switch mode {
	case SamplingModeSmoke:
		return "regression/sanity only — not an externally defensible ranking"
	case SamplingModeHoldout:
		return "holdout — do not use for tuning"
	case SamplingModePublic:
		return "public comparison — see sampling metadata"
	default:
		return "exploratory"
	}
}

// ValidateSuiteMeta enforces minimum requirements for external claims.
// Public suites must have non-zero sampled+total, and at least 30 tasks
// for uncertainty reporting to be meaningful.
func ValidateSuiteMeta(s SuiteMeta) error {
	if s.SamplingMode != SamplingModePublic {
		// Only public suites require full sampling metadata.
		return nil
	}

	if s.Sampling.TotalCorpus == 0 {
		return fmt.Errorf("bench: public suite %q must specify sampling.total_corpus", s.Name)
	}
	if s.Sampling.Sampled == 0 {
		return fmt.Errorf("bench: public suite %q must specify sampling.sampled", s.Name)
	}
	if s.TaskCount < minPublicTaskCount {
		return fmt.Errorf(
			"bench: public suite %q has only %d tasks; at least %d required for meaningful uncertainty reporting",
			s.Name, s.TaskCount, minPublicTaskCount,
		)
	}
	return nil
}
