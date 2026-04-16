package fake

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"verk/internal/adapters/runtime"
)

var (
	ErrNoScriptedWorkerResult = errors.New("runtime fake: no scripted worker result available")
	ErrNoScriptedReviewResult = errors.New("runtime fake: no scripted review result available")
)

type Adapter struct {
	mu sync.Mutex

	workerResults []runtime.WorkerResult
	reviewResults []runtime.ReviewResult

	workerRequests []runtime.WorkerRequest
	reviewRequests []runtime.ReviewRequest

	workerIndex int
	reviewIndex int
}

func New(workerResults []runtime.WorkerResult, reviewResults []runtime.ReviewResult) *Adapter {
	return &Adapter{
		workerResults: append([]runtime.WorkerResult(nil), workerResults...),
		reviewResults: cloneReviewResults(reviewResults),
	}
}

func (a *Adapter) WorkerRequests() []runtime.WorkerRequest {
	a.mu.Lock()
	defer a.mu.Unlock()

	return append([]runtime.WorkerRequest(nil), a.workerRequests...)
}

func (a *Adapter) ReviewRequests() []runtime.ReviewRequest {
	a.mu.Lock()
	defer a.mu.Unlock()

	return append([]runtime.ReviewRequest(nil), a.reviewRequests...)
}

func (a *Adapter) RunWorker(ctx context.Context, req runtime.WorkerRequest) (runtime.WorkerResult, error) {
	if err := ctx.Err(); err != nil {
		return runtime.WorkerResult{}, err
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	a.workerRequests = append(a.workerRequests, req)
	if a.workerIndex >= len(a.workerResults) {
		return runtime.WorkerResult{}, ErrNoScriptedWorkerResult
	}

	result := a.workerResults[a.workerIndex]
	a.workerIndex++

	if err := result.Validate(); err != nil {
		return runtime.WorkerResult{}, fmt.Errorf("runtime fake worker result invalid: %w", err)
	}

	return result, nil
}

func (a *Adapter) RunReviewer(ctx context.Context, req runtime.ReviewRequest) (runtime.ReviewResult, error) {
	if err := ctx.Err(); err != nil {
		return runtime.ReviewResult{}, err
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	a.reviewRequests = append(a.reviewRequests, req)
	if a.reviewIndex >= len(a.reviewResults) {
		return runtime.ReviewResult{}, ErrNoScriptedReviewResult
	}

	result := a.reviewResults[a.reviewIndex]
	a.reviewIndex++

	if err := result.Validate(req.EffectiveReviewThreshold); err != nil {
		return runtime.ReviewResult{}, fmt.Errorf("runtime fake review result invalid: %w", err)
	}

	return cloneReviewResult(result), nil
}

func cloneReviewResults(in []runtime.ReviewResult) []runtime.ReviewResult {
	out := make([]runtime.ReviewResult, len(in))
	for i, result := range in {
		out[i] = cloneReviewResult(result)
	}
	return out
}

func cloneReviewResult(in runtime.ReviewResult) runtime.ReviewResult {
	if len(in.Findings) > 0 {
		in.Findings = append([]runtime.ReviewFinding(nil), in.Findings...)
	}
	return in
}
