package bench

import (
	"testing"
)

func TestSuiteClaimLabel_AllModes(t *testing.T) {
	tests := []struct {
		mode string
		want string
	}{
		{SamplingModeSmoke, "regression/sanity only — not an externally defensible ranking"},
		{SamplingModeHoldout, "holdout — do not use for tuning"},
		{SamplingModePublic, "public comparison — see sampling metadata"},
		{SamplingModeRegression, "regression — not an externally defensible ranking"},
		{"unknown", "exploratory"},
		{"", "exploratory"},
	}

	for _, tc := range tests {
		got := SuiteClaimLabel(tc.mode)
		if got != tc.want {
			t.Errorf("SuiteClaimLabel(%q): got %q, want %q", tc.mode, got, tc.want)
		}
	}
}

func TestValidateSuiteMeta_RejectsPublicWithoutSampling(t *testing.T) {
	tests := []struct {
		name    string
		suite   SuiteMeta
		wantErr bool
	}{
		{
			name: "public without total_corpus",
			suite: SuiteMeta{
				Name:         "public-suite",
				SamplingMode: SamplingModePublic,
				TaskCount:    50,
				Sampling:     SamplingMetadata{Sampled: 50},
			},
			wantErr: true,
		},
		{
			name: "public without sampled",
			suite: SuiteMeta{
				Name:         "public-suite",
				SamplingMode: SamplingModePublic,
				TaskCount:    50,
				Sampling:     SamplingMetadata{TotalCorpus: 500},
			},
			wantErr: true,
		},
		{
			name: "public with too few tasks",
			suite: SuiteMeta{
				Name:         "public-suite",
				SamplingMode: SamplingModePublic,
				TaskCount:    10, // below minPublicTaskCount=30
				Sampling:     SamplingMetadata{TotalCorpus: 500, Sampled: 10},
			},
			wantErr: true,
		},
		{
			name: "public with full valid metadata",
			suite: SuiteMeta{
				Name:         "public-suite",
				SamplingMode: SamplingModePublic,
				TaskCount:    50,
				Sampling:     SamplingMetadata{TotalCorpus: 500, Sampled: 50},
			},
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateSuiteMeta(tc.suite)
			if tc.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateSuiteMeta_AllowsSmokeWithoutSampling(t *testing.T) {
	suite := SuiteMeta{
		Name:         "smoke-suite",
		SamplingMode: SamplingModeSmoke,
		TaskCount:    3,
		// No Sampling populated — this should be fine for smoke.
	}

	if err := ValidateSuiteMeta(suite); err != nil {
		t.Errorf("expected no error for smoke suite without sampling metadata, got: %v", err)
	}
}

func TestValidateSuiteMeta_AllowsRegressionWithoutSampling(t *testing.T) {
	suite := SuiteMeta{
		Name:         "regression-suite",
		SamplingMode: SamplingModeRegression,
		TaskCount:    10,
	}

	if err := ValidateSuiteMeta(suite); err != nil {
		t.Errorf("expected no error for regression suite without sampling metadata, got: %v", err)
	}
}

func TestValidateSuiteMeta_AllowsHoldoutWithoutSampling(t *testing.T) {
	suite := SuiteMeta{
		Name:         "holdout-suite",
		SamplingMode: SamplingModeHoldout,
		TaskCount:    20,
	}

	if err := ValidateSuiteMeta(suite); err != nil {
		t.Errorf("expected no error for holdout suite without sampling metadata, got: %v", err)
	}
}
