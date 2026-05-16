package bench

import (
	"testing"
)

const yamlMatrix = `
mode: full-verk
comparison_design: fixed-reviewer
fallback_policy: strict
profiles:
  - id: p-worker
    worker:
      runtime: claude-code
      model: claude-sonnet-4-6
    reviewer:
      runtime: claude-code
      model: claude-opus-4
  - id: p-reviewer
    worker:
      runtime: claude-code
      model: claude-opus-4
    reviewer:
      runtime: claude-code
      model: claude-opus-4
`

const jsonMatrix = `{
  "mode": "worker-only",
  "comparison_design": "full-factorial",
  "fallback_policy": "allow",
  "profiles": [
    {"id": "p1", "worker": {"runtime": "r", "model": "m1"}},
    {"id": "p2", "worker": {"runtime": "r", "model": "m2"}}
  ]
}`

func TestParseMatrix_YAML(t *testing.T) {
	m, err := ParseMatrix([]byte(yamlMatrix))
	if err != nil {
		t.Fatalf("ParseMatrix YAML: %v", err)
	}
	if m.Mode != ModeFullVerk {
		t.Errorf("mode: got %q want %q", m.Mode, ModeFullVerk)
	}
	if m.ComparisonDesign != "fixed-reviewer" {
		t.Errorf("comparison_design: got %q want fixed-reviewer", m.ComparisonDesign)
	}
	if len(m.Profiles) != 2 {
		t.Errorf("profiles: got %d want 2", len(m.Profiles))
	}
}

func TestParseMatrix_JSON(t *testing.T) {
	m, err := ParseMatrix([]byte(jsonMatrix))
	if err != nil {
		t.Fatalf("ParseMatrix JSON: %v", err)
	}
	if m.Mode != ModeWorkerOnly {
		t.Errorf("mode: got %q want %q", m.Mode, ModeWorkerOnly)
	}
	if len(m.Profiles) != 2 {
		t.Errorf("profiles: got %d want 2", len(m.Profiles))
	}
	if m.Profiles[0].ID != "p1" {
		t.Errorf("profile[0].id: got %q want p1", m.Profiles[0].ID)
	}
}

func TestValidateMatrix_RejectsDuplicateProfileIDs(t *testing.T) {
	m := Matrix{
		Mode:             ModeWorkerOnly,
		ComparisonDesign: "full-factorial",
		Profiles: []MatrixProfile{
			{ID: "dup", Worker: ModelRef{Runtime: "r", Model: "m"}},
			{ID: "dup", Worker: ModelRef{Runtime: "r", Model: "m2"}},
		},
	}
	if err := ValidateMatrix(m); err == nil {
		t.Fatal("expected error for duplicate profile IDs, got nil")
	}
}

func TestValidateMatrix_RejectsExploratoryWorkerLeaderboard(t *testing.T) {
	m := Matrix{
		Mode:             ModeWorkerOnly,
		ComparisonDesign: "exploratory",
		Profiles: []MatrixProfile{
			{ID: "p1", Worker: ModelRef{Runtime: "r", Model: "m"}},
		},
	}
	err := ValidateMatrix(m)
	if err == nil {
		t.Fatal("expected error for exploratory+worker-only, got nil")
	}
}

func TestExpandPairings_FixedReviewer(t *testing.T) {
	// In fixed-reviewer design: all workers paired with one reviewer.
	// Profile p-rev has Reviewer set (used as canonical reviewer profile).
	m := Matrix{
		Mode:             ModeFullVerk,
		ComparisonDesign: "fixed-reviewer",
		Profiles: []MatrixProfile{
			{
				ID:       "p-w1",
				Worker:   ModelRef{Runtime: "r", Model: "worker1"},
				Reviewer: ModelRef{Runtime: "r", Model: "rev"},
			},
			{
				ID:       "p-w2",
				Worker:   ModelRef{Runtime: "r", Model: "worker2"},
				Reviewer: ModelRef{Runtime: "r", Model: "rev"},
			},
			{
				ID:       "p-rev",
				Worker:   ModelRef{Runtime: "r", Model: "reviewer"},
				Reviewer: ModelRef{Runtime: "r", Model: "rev"},
			},
		},
	}
	pairs := ExpandPairings(m)
	// Should produce 2 pairs (skip the reviewer-as-worker pairing with itself).
	if len(pairs) != 2 {
		t.Errorf("fixed-reviewer: got %d pairs want 2", len(pairs))
	}
}

func TestExpandPairings_FullFactorial(t *testing.T) {
	m := Matrix{
		Mode:             ModeWorkerOnly,
		ComparisonDesign: "full-factorial",
		Profiles: []MatrixProfile{
			{ID: "p1", Worker: ModelRef{Runtime: "r", Model: "m1"}},
			{ID: "p2", Worker: ModelRef{Runtime: "r", Model: "m2"}},
			{ID: "p3", Worker: ModelRef{Runtime: "r", Model: "m3"}},
		},
	}
	pairs := ExpandPairings(m)
	// 3 profiles: 3*2 = 6 pairs (all ordered pairs excluding self).
	if len(pairs) != 6 {
		t.Errorf("full-factorial: got %d pairs want 6", len(pairs))
	}
	// Verify no self-pairs.
	for _, pair := range pairs {
		if pair[0].ID == pair[1].ID {
			t.Errorf("full-factorial: self-pair found for %q", pair[0].ID)
		}
	}
}

func TestValidateMatrix_FallbackPolicyDefaultsByMode(t *testing.T) {
	cases := []struct {
		name           string
		mode           BenchmarkMode
		design         string
		wantPolicy     string
		explicitPolicy string
	}{
		{
			name:       "full-verk defaults to strict",
			mode:       ModeFullVerk,
			design:     "fixed-reviewer",
			wantPolicy: "strict",
		},
		{
			name:       "worker-only defaults to allow",
			mode:       ModeWorkerOnly,
			design:     "full-factorial",
			wantPolicy: "allow",
		},
		{
			name:           "explicit policy respected",
			mode:           ModeFullVerk,
			design:         "fixed-reviewer",
			explicitPolicy: "sticky",
			wantPolicy:     "sticky",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := Matrix{
				Mode:             tc.mode,
				ComparisonDesign: tc.design,
				FallbackPolicy:   tc.explicitPolicy,
				Profiles: []MatrixProfile{
					{
						ID:       "p1",
						Worker:   ModelRef{Runtime: "r", Model: "m"},
						Reviewer: ModelRef{Runtime: "r", Model: "rev"}, // satisfy full-verk requirement
					},
				},
			}
			got := EffectiveFallbackPolicy(m)
			if got != tc.wantPolicy {
				t.Errorf("fallback policy: got %q want %q", got, tc.wantPolicy)
			}
		})
	}
}

func TestValidateMatrix_InvalidMode(t *testing.T) {
	m := Matrix{
		Mode:             "unknown-mode",
		ComparisonDesign: "fixed-reviewer",
		Profiles:         []MatrixProfile{{ID: "p1", Worker: ModelRef{Runtime: "r", Model: "m"}}},
	}
	if err := ValidateMatrix(m); err == nil {
		t.Fatal("expected error for invalid mode, got nil")
	}
}

func TestValidateMatrix_FullVerkRequiresReviewer(t *testing.T) {
	m := Matrix{
		Mode:             ModeFullVerk,
		ComparisonDesign: "fixed-reviewer",
		Profiles: []MatrixProfile{
			{ID: "p1", Worker: ModelRef{Runtime: "r", Model: "m"}},
			// no reviewer set
		},
	}
	if err := ValidateMatrix(m); err == nil {
		t.Fatal("expected error for full-verk without reviewer, got nil")
	}
}
