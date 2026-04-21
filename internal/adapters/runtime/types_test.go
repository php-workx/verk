package runtime_test

import (
	"context"
	"testing"
	"time"
	"verk/internal/adapters/runtime"

	runtimefake "verk/internal/adapters/runtime/fake"
)

func fixedRuntimeTimes() (time.Time, time.Time) {
	start := time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC)
	return start, start.Add(2 * time.Minute)
}

func TestWorkerResultValidate_AllowsOnlyCanonicalImplementerStatuses(t *testing.T) {
	startedAt, finishedAt := fixedRuntimeTimes()

	for _, status := range []runtime.WorkerStatus{
		runtime.WorkerStatusDone,
		runtime.WorkerStatusDoneWithConcerns,
		runtime.WorkerStatusNeedsContext,
		runtime.WorkerStatusBlocked,
	} {
		result := runtime.WorkerResult{
			Status:             status,
			RetryClass:         runtime.RetryClassRetryable,
			LeaseID:            "lease-1",
			StartedAt:          startedAt,
			FinishedAt:         finishedAt,
			StdoutPath:         "/tmp/stdout",
			StderrPath:         "/tmp/stderr",
			ResultArtifactPath: "/tmp/result.json",
			CompletionCode:     "ok",
		}
		if err := result.Validate(); err != nil {
			t.Fatalf("expected status %q to validate, got error: %v", status, err)
		}
	}

	result := runtime.WorkerResult{
		Status:     runtime.WorkerStatus("finished"),
		RetryClass: runtime.RetryClassRetryable,
		LeaseID:    "lease-1",
		StartedAt:  startedAt,
		FinishedAt: finishedAt,
	}
	if err := result.Validate(); err == nil {
		t.Fatalf("expected non-canonical worker status to be rejected")
	}
}

func TestReviewResultValidate_NormalizesFindings(t *testing.T) {
	startedAt, finishedAt := fixedRuntimeTimes()
	// Use time.Now() to ensure the expiry is always in the future regardless of when the test runs.
	waiverExpiresAt := time.Now().Add(24 * time.Hour)

	valid := runtime.ReviewResult{
		Status:             runtime.WorkerStatusDone,
		RetryClass:         runtime.RetryClassTerminal,
		LeaseID:            "lease-1",
		StartedAt:          startedAt,
		FinishedAt:         finishedAt,
		ResultArtifactPath: "/tmp/review-result.json",
		ReviewStatus:       runtime.ReviewStatusPassed,
		Summary:            "clean",
		Findings: []runtime.ReviewFinding{
			{
				ID:          "f-1",
				Severity:    runtime.SeverityP1,
				Title:       "resolved issue",
				Body:        "resolved issue",
				File:        "internal/example.go",
				Line:        12,
				Disposition: runtime.ReviewDispositionResolved,
			},
			{
				ID:              "f-2",
				Severity:        runtime.SeverityP0,
				Title:           "waived issue",
				Body:            "waived issue",
				File:            "internal/example.go",
				Line:            34,
				Disposition:     runtime.ReviewDispositionWaived,
				WaivedBy:        "reviewer",
				WaivedAt:        startedAt,
				WaiverReason:    "accepted risk",
				WaiverExpiresAt: &waiverExpiresAt,
			},
		},
	}
	if got := valid.DerivedReviewStatus(runtime.SeverityP2); got != runtime.ReviewStatusPassed {
		t.Fatalf("expected resolved and waived findings to derive passed, got %q", got)
	}
	if err := valid.Validate(runtime.SeverityP2); err != nil {
		t.Fatalf("expected canonical findings to validate, got error: %v", err)
	}

	for _, tc := range []struct {
		name   string
		result runtime.ReviewResult
	}{
		{
			name: "invalid severity",
			result: runtime.ReviewResult{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            "lease-1",
				StartedAt:          startedAt,
				FinishedAt:         finishedAt,
				ResultArtifactPath: "/tmp/review-result.json",
				ReviewStatus:       runtime.ReviewStatusFindings,
				Findings: []runtime.ReviewFinding{
					{
						ID:          "f-1",
						Severity:    runtime.Severity("P9"),
						Title:       "bad severity",
						Body:        "bad severity",
						File:        "internal/example.go",
						Line:        12,
						Disposition: runtime.ReviewDispositionOpen,
					},
				},
			},
		},
		{
			name: "invalid disposition",
			result: runtime.ReviewResult{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            "lease-1",
				StartedAt:          startedAt,
				FinishedAt:         finishedAt,
				ResultArtifactPath: "/tmp/review-result.json",
				ReviewStatus:       runtime.ReviewStatusFindings,
				Findings: []runtime.ReviewFinding{
					{
						ID:          "f-1",
						Severity:    runtime.SeverityP2,
						Title:       "bad disposition",
						Body:        "bad disposition",
						File:        "internal/example.go",
						Line:        12,
						Disposition: runtime.ReviewDisposition("ignored"),
					},
				},
			},
		},
		{
			name: "waived finding missing metadata",
			result: runtime.ReviewResult{
				Status:             runtime.WorkerStatusDone,
				RetryClass:         runtime.RetryClassTerminal,
				LeaseID:            "lease-1",
				StartedAt:          startedAt,
				FinishedAt:         finishedAt,
				ResultArtifactPath: "/tmp/review-result.json",
				ReviewStatus:       runtime.ReviewStatusFindings,
				Findings: []runtime.ReviewFinding{
					{
						ID:          "f-1",
						Severity:    runtime.SeverityP2,
						Title:       "missing waiver metadata",
						Body:        "missing waiver metadata",
						File:        "internal/example.go",
						Line:        12,
						Disposition: runtime.ReviewDispositionWaived,
					},
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.result.Validate(runtime.SeverityP2); err == nil {
				t.Fatalf("expected %s to be rejected", tc.name)
			}
		})
	}
}

func TestReviewResultValidate_RequiresDerivedStatusToMatchFindings(t *testing.T) {
	startedAt, finishedAt := fixedRuntimeTimes()

	result := runtime.ReviewResult{
		Status:             runtime.WorkerStatusDone,
		RetryClass:         runtime.RetryClassTerminal,
		LeaseID:            "lease-1",
		StartedAt:          startedAt,
		FinishedAt:         finishedAt,
		ResultArtifactPath: "/tmp/review-result.json",
		ReviewStatus:       runtime.ReviewStatusPassed,
		Findings: []runtime.ReviewFinding{
			{
				ID:          "f-1",
				Severity:    runtime.SeverityP2,
				Title:       "blocking issue",
				Body:        "blocking issue",
				File:        "internal/example.go",
				Line:        12,
				Disposition: runtime.ReviewDispositionOpen,
			},
		},
	}

	if got := result.DerivedReviewStatus(runtime.SeverityP2); got != runtime.ReviewStatusFindings {
		t.Fatalf("expected open P2 finding at threshold P2 to derive findings, got %q", got)
	}
	if err := result.Validate(runtime.SeverityP2); err == nil {
		t.Fatalf("expected contradictory review status to be rejected")
	}
}

func TestIsBlockingFinding_ExpiredWaiverIsReElevated(t *testing.T) {
	past := time.Now().Add(-1 * time.Hour)
	future := time.Now().Add(1 * time.Hour)

	makeResult := func(disposition runtime.ReviewDisposition, waiverExpiresAt *time.Time) runtime.ReviewResult {
		finding := runtime.ReviewFinding{
			ID:          "f-1",
			Severity:    runtime.SeverityP2,
			Title:       "finding",
			Body:        "finding body",
			File:        "internal/example.go",
			Line:        1,
			Disposition: disposition,
		}
		if disposition == runtime.ReviewDispositionWaived {
			finding.WaivedBy = "reviewer"
			finding.WaivedAt = time.Now().Add(-2 * time.Hour)
			finding.WaiverReason = "temporary waiver"
			finding.WaiverExpiresAt = waiverExpiresAt
		}
		return runtime.ReviewResult{
			Findings: []runtime.ReviewFinding{finding},
		}
	}

	for _, tc := range []struct {
		name            string
		disposition     runtime.ReviewDisposition
		waiverExpiresAt *time.Time
		wantStatus      runtime.ReviewStatus
	}{
		{
			name:            "waived with expired waiver is re-elevated to blocking",
			disposition:     runtime.ReviewDispositionWaived,
			waiverExpiresAt: &past,
			wantStatus:      runtime.ReviewStatusFindings,
		},
		{
			name:            "waived with future expiry remains non-blocking",
			disposition:     runtime.ReviewDispositionWaived,
			waiverExpiresAt: &future,
			wantStatus:      runtime.ReviewStatusPassed,
		},
		{
			name:            "waived with nil expiry is permanent waiver (non-blocking)",
			disposition:     runtime.ReviewDispositionWaived,
			waiverExpiresAt: nil,
			wantStatus:      runtime.ReviewStatusPassed,
		},
		{
			name:        "open finding is blocking",
			disposition: runtime.ReviewDispositionOpen,
			wantStatus:  runtime.ReviewStatusFindings,
		},
		{
			name:        "resolved finding is non-blocking",
			disposition: runtime.ReviewDispositionResolved,
			wantStatus:  runtime.ReviewStatusPassed,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := makeResult(tc.disposition, tc.waiverExpiresAt)
			got := result.DerivedReviewStatus(runtime.SeverityP2)
			if got != tc.wantStatus {
				t.Fatalf("expected DerivedReviewStatus to be %q, got %q", tc.wantStatus, got)
			}
		})
	}
}

func TestIsBlockingFinding_ExpiredWaiverRespectsSeverityThreshold(t *testing.T) {
	past := time.Now().Add(-1 * time.Hour)

	makeResult := func(severity runtime.Severity) runtime.ReviewResult {
		waivedAt := time.Now().Add(-2 * time.Hour)
		return runtime.ReviewResult{
			Findings: []runtime.ReviewFinding{
				{
					ID:              "f-1",
					Severity:        severity,
					Title:           "expired waiver finding",
					Body:            "expired waiver finding",
					File:            "internal/example.go",
					Line:            1,
					Disposition:     runtime.ReviewDispositionWaived,
					WaivedBy:        "reviewer",
					WaivedAt:        waivedAt,
					WaiverReason:    "temporary",
					WaiverExpiresAt: &past,
				},
			},
		}
	}

	// Expired waiver with severity at/above threshold (P2) → blocking
	if got := makeResult(runtime.SeverityP2).DerivedReviewStatus(runtime.SeverityP2); got != runtime.ReviewStatusFindings {
		t.Fatalf("expected expired waived P2 finding at P2 threshold to be findings, got %q", got)
	}
	if got := makeResult(runtime.SeverityP1).DerivedReviewStatus(runtime.SeverityP2); got != runtime.ReviewStatusFindings {
		t.Fatalf("expected expired waived P1 finding at P2 threshold to be findings, got %q", got)
	}

	// Expired waiver with severity below threshold (P3 below P2) → not blocking
	if got := makeResult(runtime.SeverityP3).DerivedReviewStatus(runtime.SeverityP2); got != runtime.ReviewStatusPassed {
		t.Fatalf("expected expired waived P3 finding at P2 threshold to be passed, got %q", got)
	}
}

func TestReviewFindingValidate_RejectsWhitespaceOnlyFields(t *testing.T) {
	startedAt, _ := fixedRuntimeTimes()

	base := runtime.ReviewFinding{
		ID:          "f-1",
		Severity:    runtime.SeverityP2,
		Title:       "real title",
		Body:        "real body",
		File:        "internal/example.go",
		Line:        1,
		Disposition: runtime.ReviewDispositionOpen,
	}

	for _, tc := range []struct {
		name   string
		mutate func(f *runtime.ReviewFinding)
	}{
		{
			name:   "whitespace-only title",
			mutate: func(f *runtime.ReviewFinding) { f.Title = "   " },
		},
		{
			name:   "newline-tab body",
			mutate: func(f *runtime.ReviewFinding) { f.Body = "\n\t" },
		},
		{
			name:   "space-only file",
			mutate: func(f *runtime.ReviewFinding) { f.File = " " },
		},
		{
			name: "tab-only waived_by on waived finding",
			mutate: func(f *runtime.ReviewFinding) {
				f.Disposition = runtime.ReviewDispositionWaived
				f.WaivedBy = "\t"
				f.WaivedAt = startedAt
				f.WaiverReason = "accepted risk"
			},
		},
		{
			name: "space-only waiver_reason on waived finding",
			mutate: func(f *runtime.ReviewFinding) {
				f.Disposition = runtime.ReviewDispositionWaived
				f.WaivedBy = "reviewer"
				f.WaivedAt = startedAt
				f.WaiverReason = " "
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := base
			tc.mutate(&f)
			if err := f.Validate(); err == nil {
				t.Fatalf("expected whitespace-only %s to be rejected", tc.name)
			}
		})
	}
}

func TestValidatedExecutable(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{name: "bare name", raw: "codex", want: "codex"},
		{name: "absolute path", raw: "/usr/local/bin/codex", want: "/usr/local/bin/codex"},
		{name: "trimmed", raw: "  ./bin/claude  ", want: "./bin/claude"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := runtime.ValidatedExecutable(tc.raw)
			if err != nil {
				t.Fatalf("expected executable to validate, got error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("expected %q, got %q", tc.want, got)
			}
		})
	}

	for _, raw := range []string{"", "   ", "codex --danger", "sh -c echo", "codex\nrm"} {
		t.Run(raw, func(t *testing.T) {
			if _, err := runtime.ValidatedExecutable(raw); err == nil {
				t.Fatalf("expected %q to be rejected", raw)
			}
		})
	}
}

func TestFakeAdapter_ReturnsScriptedResults(t *testing.T) {
	startedAt, finishedAt := fixedRuntimeTimes()
	adapter := runtimefake.New([]runtime.WorkerResult{
		{
			Status:             runtime.WorkerStatusDoneWithConcerns,
			RetryClass:         runtime.RetryClassRetryable,
			LeaseID:            "lease-1",
			StartedAt:          startedAt,
			FinishedAt:         finishedAt,
			CompletionCode:     "ok",
			ResultArtifactPath: "/tmp/worker-result.json",
		},
	}, []runtime.ReviewResult{
		{
			Status:             runtime.WorkerStatusDone,
			RetryClass:         runtime.RetryClassTerminal,
			LeaseID:            "lease-1",
			StartedAt:          startedAt,
			FinishedAt:         finishedAt,
			ReviewStatus:       runtime.ReviewStatusPassed,
			Summary:            "clean",
			ResultArtifactPath: "/tmp/review-result.json",
		},
	})

	workerResult, err := adapter.RunWorker(context.Background(), runtime.WorkerRequest{LeaseID: "lease-1"})
	if err != nil {
		t.Fatalf("expected worker result, got error: %v", err)
	}
	if workerResult.Status != runtime.WorkerStatusDoneWithConcerns {
		t.Fatalf("expected scripted worker status, got %q", workerResult.Status)
	}
	if workerResult.ResultArtifactPath != "/tmp/worker-result.json" {
		t.Fatalf("expected scripted worker artifact path, got %q", workerResult.ResultArtifactPath)
	}

	reviewResult, err := adapter.RunReviewer(context.Background(), runtime.ReviewRequest{
		LeaseID:                  "lease-1",
		EffectiveReviewThreshold: runtime.SeverityP2,
	})
	if err != nil {
		t.Fatalf("expected review result, got error: %v", err)
	}
	if reviewResult.ReviewStatus != runtime.ReviewStatusPassed {
		t.Fatalf("expected scripted review status, got %q", reviewResult.ReviewStatus)
	}
	if reviewResult.ResultArtifactPath != "/tmp/review-result.json" {
		t.Fatalf("expected scripted review artifact path, got %q", reviewResult.ResultArtifactPath)
	}
}
