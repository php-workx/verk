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
	waiverExpiresAt := finishedAt.Add(24 * time.Hour)

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
