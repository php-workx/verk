package bench

import (
	"encoding/json"
	"errors"
	"fmt"

	"gopkg.in/yaml.v3"
)

const (
	designFixedReviewer = "fixed-reviewer"
	designFullFactorial = "full-factorial"
	designExploratory   = "exploratory"
)

// ParseMatrix decodes a YAML or JSON matrix definition.
// It attempts JSON first; on failure it falls back to YAML.
func ParseMatrix(data []byte) (Matrix, error) {
	var m Matrix
	// Try JSON first.
	if err := json.Unmarshal(data, &m); err == nil {
		return m, nil
	}
	// Fall back to YAML.
	if err := yaml.Unmarshal(data, &m); err != nil {
		return Matrix{}, fmt.Errorf("bench: parse matrix: %w", err)
	}
	return m, nil
}

// ValidateMatrix checks for duplicate profile IDs, valid modes,
// supported comparison designs, and fallback policy correctness.
func ValidateMatrix(m Matrix) error {
	// Validate mode.
	switch m.Mode {
	case ModeFullVerk, ModeWorkerOnly, ModeRuntimeProbe:
		// valid
	default:
		return fmt.Errorf("bench: invalid mode %q: must be one of full-verk, worker-only, runtime-probe", m.Mode)
	}

	// Validate comparison design.
	switch m.ComparisonDesign {
	case designFixedReviewer, designFullFactorial, designExploratory:
		// valid
	default:
		return fmt.Errorf("bench: invalid comparison_design %q: must be one of fixed-reviewer, full-factorial, exploratory", m.ComparisonDesign)
	}

	// Reject exploratory worker-only leaderboard requests.
	if m.ComparisonDesign == designExploratory && m.Mode == ModeWorkerOnly {
		return errors.New("bench: exploratory comparison_design is not allowed with worker-only mode (non-comparable leaderboard results)")
	}

	// Validate fallback policy.
	switch m.FallbackPolicy {
	case "", "strict", "allow", "sticky":
		// valid (empty means use default)
	default:
		return fmt.Errorf("bench: invalid fallback_policy %q: must be one of strict, allow, sticky", m.FallbackPolicy)
	}

	// Check duplicate profile IDs.
	seen := make(map[string]bool, len(m.Profiles))
	for _, p := range m.Profiles {
		if seen[p.ID] {
			return fmt.Errorf("bench: duplicate profile ID %q", p.ID)
		}
		seen[p.ID] = true
	}

	// Mode-specific profile validation.
	for _, p := range m.Profiles {
		if m.Mode == ModeFullVerk {
			if p.Reviewer.Runtime == "" && p.Reviewer.Model == "" {
				return fmt.Errorf("bench: profile %q: full-verk mode requires reviewer to be set", p.ID)
			}
		}
	}

	return nil
}

// effectiveFallbackPolicy returns the fallback policy, applying mode defaults
// when FallbackPolicy is unset.
func effectiveFallbackPolicy(m Matrix) string {
	if m.FallbackPolicy != "" {
		return m.FallbackPolicy
	}
	if m.Mode == ModeFullVerk {
		return "strict"
	}
	return "allow"
}

// ExpandPairings produces the (worker, reviewer) pairs that should run,
// expanding fixed-reviewer or full-factorial designs.
//
// Returns a slice of [2]MatrixProfile where index 0 is the worker profile
// and index 1 is the reviewer profile (may be zero-value for worker-only mode).
func ExpandPairings(m Matrix) [][2]MatrixProfile {
	switch m.ComparisonDesign {
	case designFixedReviewer:
		return expandFixedReviewer(m)
	case designFullFactorial:
		return expandFullFactorial(m)
	default:
		// "exploratory" and unknown: return as-declared.
		return expandExploratory(m)
	}
}

// expandFixedReviewer pairs all workers with a single reviewer.
// The reviewer is the first profile whose Reviewer field is set; if none,
// the last profile in the list is used as the reviewer for all others.
func expandFixedReviewer(m Matrix) [][2]MatrixProfile {
	if len(m.Profiles) == 0 {
		return nil
	}

	// Find designated reviewer: first profile that has a Reviewer set.
	// In a fixed-reviewer design, typically one profile plays reviewer for all workers.
	// We use the first profile that declares a non-empty Reviewer model as the
	// canonical reviewer profile. If none has a reviewer set, use the last profile.
	reviewerIdx := len(m.Profiles) - 1
	for i, p := range m.Profiles {
		if p.Reviewer.Model != "" {
			reviewerIdx = i
			break
		}
	}

	reviewer := m.Profiles[reviewerIdx]
	var pairs [][2]MatrixProfile
	for i, p := range m.Profiles {
		if i == reviewerIdx {
			continue // skip reviewer-as-worker pairing with itself
		}
		pairs = append(pairs, [2]MatrixProfile{p, reviewer})
	}

	// If only one profile exists, pair it with itself.
	if len(pairs) == 0 {
		pairs = append(pairs, [2]MatrixProfile{reviewer, reviewer})
	}

	return pairs
}

// expandFullFactorial produces every (worker, reviewer) pair.
func expandFullFactorial(m Matrix) [][2]MatrixProfile {
	var pairs [][2]MatrixProfile
	for _, worker := range m.Profiles {
		for _, reviewer := range m.Profiles {
			if worker.ID == reviewer.ID {
				continue
			}
			pairs = append(pairs, [2]MatrixProfile{worker, reviewer})
		}
	}
	return pairs
}

// expandExploratory returns as-declared: each profile paired with its own
// Reviewer field (zero-value if not set).
func expandExploratory(m Matrix) [][2]MatrixProfile {
	pairs := make([][2]MatrixProfile, 0, len(m.Profiles))
	for _, p := range m.Profiles {
		reviewerProfile := MatrixProfile{
			ID:       p.Reviewer.Model,
			Worker:   p.Reviewer,
			Reviewer: ModelRef{},
		}
		pairs = append(pairs, [2]MatrixProfile{p, reviewerProfile})
	}
	return pairs
}

// EffectiveFallbackPolicy is the exported version for use by other packages.
func EffectiveFallbackPolicy(m Matrix) string {
	return effectiveFallbackPolicy(m)
}
