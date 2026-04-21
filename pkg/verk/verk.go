package verk

import (
	"context"
	"verk/internal/engine"
)

// RunTicket executes a single ticket through the full lifecycle:
// intake → implement → verify → review → closeout.
func RunTicket(ctx context.Context, req RunTicketRequest) (RunTicketResult, error) {
	return engine.RunTicket(ctx, req)
}

// RunEpic executes an epic (multi-ticket) run, scheduling waves of
// concurrent ticket runs until all children are closed or blocked.
func RunEpic(ctx context.Context, req RunEpicRequest) (RunEpicResult, error) {
	return engine.RunEpic(ctx, req)
}

// ResumeRun resumes a previously interrupted run, reconciling claims
// and repairing any incomplete artifact state.
func ResumeRun(ctx context.Context, req ResumeRequest) (ResumeReport, error) {
	return engine.ResumeRun(ctx, req)
}

// ReopenTicket transitions a blocked or closed ticket back to an
// earlier phase (implement or repair) so it can be re-executed.
func ReopenTicket(ctx context.Context, req ReopenRequest) error {
	return engine.ReopenTicket(ctx, req)
}

// DeriveStatus computes the current status of a run by loading and
// inspecting all run, ticket, wave, and claim artifacts.
func DeriveStatus(req StatusRequest) (StatusReport, error) {
	return engine.DeriveStatus(req)
}

// RunDoctor performs a health check on a verk repository, validating
// the ticket store, artifacts, git state, and runtime availability.
// It returns a report, an exit code (0=ok, 1=warnings, 2=errors),
// and any fatal error encountered during the check itself.
func RunDoctor(repoRoot string) (DoctorReport, int, error) {
	return engine.RunDoctor(repoRoot)
}
