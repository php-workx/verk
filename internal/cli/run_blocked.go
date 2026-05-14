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

// noReasonRecorded is the fallback string used when a blocked ticket has no
// recorded stop reason. It is a named constant to satisfy the goconst linter.
const noReasonRecorded = "blocked (no reason recorded)"

// ticketDecision represents the operator's choice for a needs_decision ticket.
type ticketDecision int

const (
	ticketDecisionRetryImplement  ticketDecision = iota // reopen → implement
	ticketDecisionRetryRepair                           // reopen → repair
	ticketDecisionLeaveAsDecision                       // keep outcome=needs_decision
	ticketDecisionMarkBlocked                           // mark as explicitly blocked
	ticketDecisionStop                                  // abort the whole interaction
)

// decisionPrompter asks the operator what to do with a needs_decision ticket.
// The interface exists so tests can inject stub answers without a real terminal.
type decisionPrompter interface {
	ChooseTicketDecision(ticket engine.BlockedTicket) (ticketDecision, error)
}

// terminalDecisionPrompter implements decisionPrompter using a real terminal.
// It uses single-keystroke input when running in raw mode and falls back to
// line-buffered input (e.g. for pipe-based testing without a real TTY).
type terminalDecisionPrompter struct {
	r io.Reader
	w io.Writer
}

// ChooseTicketDecision presents a numbered menu for the given ticket and reads
// a single-character reply. The menu maps:
//
//	1 → retry from implement
//	2 → retry from repair
//	3 → leave as needs_decision
//	4 → mark blocked
//	s → stop
//
// Any other character is ignored; the prompt repeats until a valid choice or
// EOF/error is received, at which point the function returns
// ticketDecisionLeaveAsDecision (fail-open: safest option for the ticket).
func (p terminalDecisionPrompter) ChooseTicketDecision(ticket engine.BlockedTicket) (ticketDecision, error) {
	reason := strings.TrimSpace(ticket.Reason)
	if reason == "" {
		reason = noReasonRecorded
	}
	_, _ = fmt.Fprintf(p.w, "\r\nDecision for %s: %s\r\n", ticket.ID, reason)
	_, _ = fmt.Fprint(p.w, "  [1] retry from implement\r\n")
	_, _ = fmt.Fprint(p.w, "  [2] retry from repair\r\n")
	_, _ = fmt.Fprint(p.w, "  [3] leave as needs decision\r\n")
	_, _ = fmt.Fprint(p.w, "  [4] mark blocked\r\n")
	_, _ = fmt.Fprint(p.w, "  [s] stop\r\n")

	ctx := context.Background()
	for {
		_, _ = fmt.Fprint(p.w, "  Choice: ")
		b, ok, cancelled := readPromptByte(ctx, p.r)
		if cancelled || !ok {
			_, _ = fmt.Fprint(p.w, "\r\n")
			return ticketDecisionLeaveAsDecision, nil
		}
		_, _ = fmt.Fprintf(p.w, "%c\r\n", b)
		switch b {
		case '1':
			return ticketDecisionRetryImplement, nil
		case '2':
			return ticketDecisionRetryRepair, nil
		case '3':
			return ticketDecisionLeaveAsDecision, nil
		case '4':
			return ticketDecisionMarkBlocked, nil
		case 's', 'S':
			return ticketDecisionStop, nil
		case 0x03, 0x18: // Ctrl+C, Ctrl+X
			return ticketDecisionStop, nil
		default:
			// Unknown key — show menu again.
		}
	}
}

// promptNeedsDecisionTickets asks the operator, one ticket at a time, what to
// do with each needs_decision ticket. It records each decision and prints what
// it would do (ticket writes are deferred to a later task). Returns true if the
// loop was stopped early by the operator.
func promptNeedsDecisionTickets(ctx context.Context, w io.Writer, prompter decisionPrompter, blocked *engine.BlockedRunError) (stopped bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	var ndTickets []engine.BlockedTicket
	for _, t := range blocked.BlockedTickets {
		if t.Outcome == state.TicketOutcomeNeedsDecision {
			ndTickets = append(ndTickets, t)
		}
	}
	if len(ndTickets) == 0 {
		return false
	}
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "Tickets needing your decision:")
	for _, t := range ndTickets {
		select {
		case <-ctx.Done():
			return true
		default:
		}
		decision, err := prompter.ChooseTicketDecision(t)
		if err != nil {
			return false
		}
		switch decision {
		case ticketDecisionRetryImplement:
			_, _ = fmt.Fprintf(w, "  → would reopen %s to implement\r\n", t.ID)
		case ticketDecisionRetryRepair:
			_, _ = fmt.Fprintf(w, "  → would reopen %s to repair\r\n", t.ID)
		case ticketDecisionLeaveAsDecision:
			_, _ = fmt.Fprintf(w, "  → leaving %s as needs_decision\r\n", t.ID)
		case ticketDecisionMarkBlocked:
			_, _ = fmt.Fprintf(w, "  → would mark %s as blocked\r\n", t.ID)
		case ticketDecisionStop:
			_, _ = fmt.Fprintf(w, "  → stopping at %s\r\n", t.ID)
			return true
		}
	}
	return false
}

// printBlockedRunGuidance writes a stable, testable representation of a blocked
// epic run to w. Tickets are grouped into three distinct sections based on
// their outcome:
//   - "Retryable tickets": failed_retryable outcome — safe to reopen automatically.
//   - "Tickets needing decision": needs_decision outcome — operator must choose
//     between implement, repair, or blocking the ticket explicitly.
//   - "Blocked tickets": all other non-closed, non-retryable tickets.
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

	// Partition tickets into three buckets by outcome.
	var retryable, needsDecision, hardBlocked []engine.BlockedTicket
	for _, t := range blocked.BlockedTickets {
		switch {
		case t.RetryPhase != "" && t.Outcome != state.TicketOutcomeNeedsDecision:
			retryable = append(retryable, t)
		case t.Outcome == state.TicketOutcomeNeedsDecision:
			needsDecision = append(needsDecision, t)
		default:
			hardBlocked = append(hardBlocked, t)
		}
	}

	// Section: retryable tickets.
	if len(retryable) > 0 {
		_, _ = fmt.Fprintln(w, "Retryable tickets:")
		for _, t := range retryable {
			reason := strings.TrimSpace(t.Reason)
			if reason == "" {
				reason = noReasonRecorded
			}
			_, _ = fmt.Fprintf(w, "  %s: %s\n", t.ID, reason)
			_, _ = fmt.Fprintf(w, "  To retry:  verk reopen %s %s --to %s\n", blocked.RunID, t.ID, t.RetryPhase)
		}
		_, _ = fmt.Fprintln(w)
	}

	// Section: needs-decision tickets.
	if len(needsDecision) > 0 {
		_, _ = fmt.Fprintln(w, "Tickets needing decision:")
		for _, t := range needsDecision {
			reason := strings.TrimSpace(t.Reason)
			if reason == "" {
				reason = noReasonRecorded
			}
			_, _ = fmt.Fprintf(w, "  %s: %s\n", t.ID, reason)
			_, _ = fmt.Fprintf(w, "  To retry:  verk reopen %s %s --to implement\n", blocked.RunID, t.ID)
			_, _ = fmt.Fprintf(w, "  To retry:  verk reopen %s %s --to repair\n", blocked.RunID, t.ID)
			_, _ = fmt.Fprintf(w, "  To block:  verk block %s --reason \"...\"\n", t.ID)
		}
		_, _ = fmt.Fprintln(w)
	}

	// Section: hard-blocked tickets (no retry possible).
	if len(hardBlocked) > 0 {
		_, _ = fmt.Fprintln(w, "Blocked tickets:")
		for _, t := range hardBlocked {
			reason := strings.TrimSpace(t.Reason)
			if reason == "" {
				reason = noReasonRecorded
			}
			_, _ = fmt.Fprintf(w, "  %s: %s\n", t.ID, reason)
		}
		_, _ = fmt.Fprintln(w)
	}

	// If nothing is retryable at all, emit a hint.
	if len(retryable) == 0 && len(needsDecision) == 0 {
		_, _ = fmt.Fprintln(w, "Retry: no tickets can be reopened automatically")
		_, _ = fmt.Fprintln(w, "  resolve blockers or dependencies, then run verk run")
		return
	}
	_, _ = fmt.Fprintln(w, "  verk run")
}

func retryableBlockedTickets(blocked *engine.BlockedRunError) []engine.BlockedTicket {
	if blocked == nil {
		return nil
	}
	out := make([]engine.BlockedTicket, 0, len(blocked.BlockedTickets))
	for _, ticket := range blocked.BlockedTickets {
		if ticket.RetryPhase == "" {
			continue
		}
		if ticket.Outcome == state.TicketOutcomeNeedsDecision {
			continue
		}
		out = append(out, ticket)
	}
	return out
}

// promptBlockedRetry asks the operator, one ticket at a time, which blocked
// tickets they want to reopen and retry. It returns the selected retryable
// tickets and whether the prompt was cancelled. The default for every prompt
// is "no" so that accidentally hitting Enter never retries a ticket.
//
// Reading is line-buffered: an empty line counts as "no" and EOF on stdin
// aborts the selection loop (treated as "no for all remaining tickets"). Any
// scan error is treated as a "no" to fail closed.
func promptBlockedRetry(ctx context.Context, r io.Reader, w io.Writer, blocked *engine.BlockedRunError) ([]engine.BlockedTicket, bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	retryable := retryableBlockedTickets(blocked)
	if r == nil || len(retryable) == 0 {
		return nil, false
	}
	scanner := bufio.NewScanner(r)
	// Allow pasted multi-line block reasons etc. without surprises.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "Select blocked tickets to reopen and retry (default=no):")

	var selected []engine.BlockedTicket
	for _, t := range retryable {
		_, _ = fmt.Fprintf(w, "  Reopen %s? [y/N] ", t.ID)
		answer, ok, cancelled := scanPromptLine(ctx, scanner)
		if cancelled {
			_, _ = fmt.Fprintln(w)
			return selected, true
		}
		if !ok {
			// EOF or scan error: treat remaining tickets as "no".
			return selected, false
		}
		answer = strings.ToLower(strings.TrimSpace(answer))
		if answer == "y" || answer == "yes" {
			selected = append(selected, t)
		}
	}
	return selected, false
}

type promptLineResult struct {
	text string
	ok   bool
}

func scanPromptLine(ctx context.Context, scanner *bufio.Scanner) (string, bool, bool) {
	ch := make(chan promptLineResult, 1)
	go func() {
		if scanner.Scan() {
			ch <- promptLineResult{text: scanner.Text(), ok: true}
			return
		}
		ch <- promptLineResult{}
	}()

	select {
	case <-ctx.Done():
		return "", false, true
	case result := <-ch:
		return result.text, result.ok, false
	}
}

func promptBlockedRetryTTY(ctx context.Context, in *os.File, w io.Writer, blocked *engine.BlockedRunError) ([]engine.BlockedTicket, bool) {
	if in == nil {
		return promptBlockedRetry(ctx, nil, w, blocked)
	}
	oldState, err := term.MakeRaw(in.Fd())
	if err != nil {
		return promptBlockedRetry(ctx, in, w, blocked)
	}
	defer func() { _ = term.Restore(in.Fd(), oldState) }()
	return promptBlockedRetryKeys(ctx, in, w, blocked)
}

func promptBlockedRetryKeys(ctx context.Context, r io.Reader, w io.Writer, blocked *engine.BlockedRunError) ([]engine.BlockedTicket, bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	retryable := retryableBlockedTickets(blocked)
	if r == nil || len(retryable) == 0 {
		return nil, false
	}

	_, _ = fmt.Fprint(w, "\r\n")
	_, _ = fmt.Fprint(w, "Select blocked tickets to reopen and retry (default=no):\r\n")

	var selected []engine.BlockedTicket
	for _, t := range retryable {
		_, _ = fmt.Fprintf(w, "  Reopen %s? [y/N] ", t.ID)
		for {
			b, ok, cancelled := readPromptByte(ctx, r)
			if cancelled {
				_, _ = fmt.Fprintln(w)
				return selected, true
			}
			if !ok {
				return selected, false
			}
			switch b {
			case 0x03:
				_, _ = fmt.Fprint(w, "^C\r\n")
				return selected, true
			case 0x18:
				_, _ = fmt.Fprint(w, "^X\r\n")
				return selected, true
			case 'y', 'Y':
				selected = append(selected, t)
				_, _ = fmt.Fprint(w, "y\r\n")
				goto nextTicket
			case 'n', 'N':
				_, _ = fmt.Fprint(w, "n\r\n")
				goto nextTicket
			case '\r', '\n':
				_, _ = fmt.Fprint(w, "\r\n")
				goto nextTicket
			default:
				// Ignore any other key while staying on the same prompt.
			}
		}
	nextTicket:
	}
	return selected, false
}

type promptByteResult struct {
	b  byte
	ok bool
}

func readPromptByte(ctx context.Context, r io.Reader) (byte, bool, bool) {
	ch := make(chan promptByteResult, 1)
	go func() {
		var buf [1]byte
		n, err := r.Read(buf[:])
		if n > 0 {
			ch <- promptByteResult{b: buf[0], ok: true}
			return
		}
		if err != nil {
			ch <- promptByteResult{}
			return
		}
		ch <- promptByteResult{}
	}()

	select {
	case <-ctx.Done():
		return 0, false, true
	case result := <-ch:
		return result.b, result.ok, false
	}
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
	tickets []engine.BlockedTicket,
) error {
	for _, ticket := range tickets {
		if err := engine.ReopenTicket(ctx, engine.ReopenRequest{
			RepoRoot: repoRoot,
			RunID:    runID,
			TicketID: ticket.ID,
			ToPhase:  ticket.RetryPhase,
		}); err != nil {
			return fmt.Errorf("reopen %s: %w", ticket.ID, err)
		}
		_, _ = fmt.Fprintf(w, "reopened %s → %s\n", ticket.ID, ticket.RetryPhase)
	}

	if err := writeCurrentRunID(repoRoot, runID); err != nil {
		_, _ = fmt.Fprintf(errw, "warning: could not write current run: %v\n", err)
	}
	_, _ = fmt.Fprintf(w, "Resuming run %s...\n", runID)

	resumeCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	ch := make(chan engine.ProgressEvent, 64)
	var report engine.ResumeReport
	var resumeErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(ch)
		report, resumeErr = engine.ResumeRun(resumeCtx, engine.ResumeRequest{
			RepoRoot: repoRoot,
			RunID:    runID,
			AdapterFactory: func(ticketPreference string) (runtime.Adapter, error) {
				return runtimeAdapterFor(ticketPreference, cfg.defaultRuntime)
			},
			Config:   cfg.cfg,
			Progress: ch,
		})
	}()

	if tuiErr := runProgress(runID, ch, w, cancel); tuiErr != nil {
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

	// Handle needs_decision tickets interactively before retrying retryable ones.
	var hasNeedsDecision bool
	for _, t := range blocked.BlockedTickets {
		if t.Outcome == state.TicketOutcomeNeedsDecision {
			hasNeedsDecision = true
			break
		}
	}
	if hasNeedsDecision {
		prompter := terminalDecisionPrompter{r: interactor.in, w: interactor.out}
		if stopped := promptNeedsDecisionTickets(ctx, interactor.out, prompter, blocked); stopped {
			return false, context.Canceled
		}
	}

	selected, cancelled := promptBlockedRetryTTY(ctx, interactor.in, interactor.out, blocked)
	if cancelled {
		return false, context.Canceled
	}
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
