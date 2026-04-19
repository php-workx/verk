package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"verk/internal/adapters/runtime"
	"verk/internal/engine"
	"verk/internal/policy"
	"verk/internal/state"

	term "github.com/charmbracelet/x/term"
)

// blockedRunInteractor is the subset of os.File capabilities needed to
// interactively prompt the operator after a blocked epic run. It is factored
// into an interface so tests can inject non-TTY readers/writers without
// changing production code paths.
type blockedRunInteractor struct {
	in  *os.File
	out *os.File
}

// isTTY reports whether both the input and output handles are connected to a
// terminal. We require both because a prompt with no way to read a response is
// worse than no prompt at all — the run would hang or the user could not see
// the question.
func (b blockedRunInteractor) isTTY() bool {
	if b.in == nil || b.out == nil {
		return false
	}
	return term.IsTerminal(b.in.Fd()) && term.IsTerminal(b.out.Fd())
}

// defaultBlockedRunInteractor returns the standard stdin/stdout interactor.
// Tests replace blockedRunInteractorFor with a stub that returns a non-TTY
// pair or a scripted TTY pair; production callers use this default.
var blockedRunInteractorFor = func() blockedRunInteractor {
	return blockedRunInteractor{in: os.Stdin, out: os.Stdout}
}

// printBlockedRunGuidance writes a stable, testable representation of a blocked
// epic run to w: one line per blocked ticket with its reason, followed by a
// concrete retry command for each ticket and a final `verk run` resume line.
//
// The output contract is covered by CLI tests and the ticket's acceptance
// criteria — avoid changing it without updating both.
func printBlockedRunGuidance(w io.Writer, blocked *engine.BlockedRunError) {
	if w == nil || blocked == nil {
		return
	}
	_, _ = fmt.Fprintln(w)
	if len(blocked.BlockedTickets) == 0 {
		_, _ = fmt.Fprintf(w, "Epic run blocked (status=%s).\n", blocked.Status)
		if blocked.Cause != nil {
			_, _ = fmt.Fprintf(w, "  reason: %v\n", blocked.Cause)
		}
		return
	}
	_, _ = fmt.Fprintln(w, "Blocked tickets:")
	for _, t := range blocked.BlockedTickets {
		reason := strings.TrimSpace(t.Reason)
		if reason == "" {
			reason = "blocked (no reason recorded)"
		}
		_, _ = fmt.Fprintf(w, "  %s: %s\n", t.ID, reason)
	}
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "Retry:")
	for _, t := range blocked.BlockedTickets {
		_, _ = fmt.Fprintf(w, "  verk reopen %s %s --to implement\n", blocked.RunID, t.ID)
	}
	_, _ = fmt.Fprintln(w, "  verk run")
}

// promptBlockedRetry asks the operator, one ticket at a time, which blocked
// tickets they want to reopen and retry. It returns the list of selected
// ticket IDs. The default for every prompt is "no" so that accidentally
// hitting Enter never retries a ticket.
//
// Reading is line-buffered: an empty line counts as "no" and EOF on stdin
// aborts the selection loop (treated as "no for all remaining tickets"). Any
// scan error is treated as a "no" to fail closed.
func promptBlockedRetry(r io.Reader, w io.Writer, blocked *engine.BlockedRunError) []string {
	if blocked == nil || len(blocked.BlockedTickets) == 0 {
		return nil
	}
	scanner := bufio.NewScanner(r)
	// Allow pasted multi-line block reasons etc. without surprises.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "Select blocked tickets to reopen and retry (default=no):")

	var selected []string
	for _, t := range blocked.BlockedTickets {
		_, _ = fmt.Fprintf(w, "  Reopen %s? [y/N] ", t.ID)
		if !scanner.Scan() {
			// EOF or scan error: treat remaining tickets as "no".
			return selected
		}
		answer := strings.ToLower(strings.TrimSpace(scanner.Text()))
		if answer == "y" || answer == "yes" {
			selected = append(selected, t.ID)
		}
	}
	return selected
}

// reopenAndResume reopens each selected ticket to the implement phase and then
// resumes the same run so the retry lands in the original run directory
// (keeping artifacts, claims and audit history in one place).
//
// Any reopen error is returned immediately without starting a resume, because
// resuming with stale blocked-state would hide the failure from the operator.
func reopenAndResume(
	ctx context.Context,
	w, errw io.Writer,
	repoRoot string,
	cfg contextCfgForResume,
	runID string,
	ticketIDs []string,
) error {
	for _, id := range ticketIDs {
		if err := engine.ReopenTicket(ctx, engine.ReopenRequest{
			RepoRoot: repoRoot,
			RunID:    runID,
			TicketID: id,
			ToPhase:  state.TicketPhaseImplement,
		}); err != nil {
			return fmt.Errorf("reopen %s: %w", id, err)
		}
		_, _ = fmt.Fprintf(w, "reopened %s → implement\n", id)
	}

	if err := writeCurrentRunID(repoRoot, runID); err != nil {
		_, _ = fmt.Fprintf(errw, "warning: could not write current run: %v\n", err)
	}
	_, _ = fmt.Fprintf(w, "Resuming run %s...\n", runID)

	ch := make(chan engine.ProgressEvent, 64)
	var report engine.ResumeReport
	var resumeErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(ch)
		report, resumeErr = engine.ResumeRun(ctx, engine.ResumeRequest{
			RepoRoot: repoRoot,
			RunID:    runID,
			AdapterFactory: func(ticketPreference string) (runtime.Adapter, error) {
				return runtimeAdapterFor(ticketPreference, cfg.defaultRuntime)
			},
			Config:   cfg.cfg,
			Progress: ch,
		})
	}()

	if tuiErr := runProgress(runID, ch, w); tuiErr != nil {
		_, _ = fmt.Fprintf(errw, "warning: TUI error: %v\n", tuiErr)
		drainProgress(ch)
	}
	wg.Wait()

	if resumeErr != nil {
		return resumeErr
	}
	_, _ = fmt.Fprintf(w, "status=%s\n", report.Run.Status)
	return nil
}

// contextCfgForResume bundles the pieces of the execution context that the
// resume path needs. Keeping it local to this file avoids re-loading the
// policy config (which also enforces clean-worktree policy) after a retry.
type contextCfgForResume struct {
	cfg            policy.Config
	defaultRuntime string
}

// handleBlockedEpicRun prints the blocked-run guidance to errw and, when
// stdin/stdout are attached to a terminal, prompts the operator to reopen and
// retry selected blocked tickets in the same run. The bool return reports
// whether a retry ran to completion; when true the caller should treat the
// overall run as successful (the CLI decides based on the final resume status).
//
// Non-interactive callers (captured output, CI) return immediately with
// retried=false — they must never hang on a prompt.
func handleBlockedEpicRun(
	ctx context.Context,
	w, errw io.Writer,
	repoRoot string,
	cfg contextCfgForResume,
	blocked *engine.BlockedRunError,
) (retried bool, err error) {
	printBlockedRunGuidance(errw, blocked)

	interactor := blockedRunInteractorFor()
	if !interactor.isTTY() {
		return false, nil
	}

	selected := promptBlockedRetry(interactor.in, interactor.out, blocked)
	if len(selected) == 0 {
		return false, nil
	}

	if err := reopenAndResume(ctx, w, errw, repoRoot, cfg, blocked.RunID, selected); err != nil {
		return false, err
	}
	return true, nil
}

// asBlockedRunError is a tiny wrapper around errors.As that the CLI command
// uses to both test for a BlockedRunError and capture the typed value in one
// call. Returning the pointer keeps caller code tidy — callers that only need
// the check can pass nil for the out param.
func asBlockedRunError(err error) (*engine.BlockedRunError, bool) {
	var blocked *engine.BlockedRunError
	if errors.As(err, &blocked) {
		return blocked, true
	}
	return nil, false
}
