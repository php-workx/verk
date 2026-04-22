package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"
	"verk/internal/adapters/runtime"
	"verk/internal/adapters/ticketstore/tkmd"
	"verk/internal/engine"
	"verk/internal/state"
	"verk/internal/tui"

	"github.com/spf13/cobra"
)

// runProgress is the function used to display run progress. It is a
// package-level variable so tests can inject a fake implementation that
// returns early (simulating a TUI error) without draining the progress channel.
var runProgress = tui.RunProgressWithCancel

// drainProgress consumes progress events from ch in a background goroutine
// until the channel is closed. It is the intentional fallback used when
// runProgress returns an error before draining the channel itself: without
// the drain, a full buffer would cause the engine goroutine to block on
// SendProgress and wg.Wait to hang. Centralising it keeps the goroutine-leak
// prevention pattern consistent across the ticket, epic, and auto-resume
// paths.
func drainProgress(ch <-chan engine.ProgressEvent) {
	go func() {
		for range ch { //nolint:revive // intentional drain loop to prevent goroutine leak
		}
	}()
}

func writeCurrentRunIDIfArtifactExists(repoRoot, runID string, errw io.Writer) bool {
	if runID == "" {
		return false
	}
	runPath := filepath.Join(repoRoot, ".verk", "runs", runID, "run.json")
	if _, err := os.Stat(runPath); err != nil {
		if !os.IsNotExist(err) {
			_, _ = fmt.Fprintf(errw, "warning: could not inspect run artifact: %v\n", err)
		}
		return false
	}
	if wErr := writeCurrentRunID(repoRoot, runID); wErr != nil {
		_, _ = fmt.Fprintf(errw, "warning: could not write current run: %v\n", wErr)
		return false
	}
	return true
}

// saveJSONAtomic and saveTicket are package-level variables so tests can inject
// fake implementations to exercise error paths without a real filesystem.
// saveJSONAtomic is used both for the initial run.json persistence in
// doRunTicket and for the final state update in finalizeRun.
var (
	saveJSONAtomic func(string, any) error                                                        = state.SaveJSONAtomic
	saveTicket     func(string, tkmd.Ticket) error                                                = tkmd.SaveTicket
	runTicket      func(context.Context, engine.RunTicketRequest) (engine.RunTicketResult, error) = engine.RunTicket
)

// finalizeRun persists ticket and run state after the engine finishes, then
// prints the status line. It is extracted so tests can inject failures via the
// saveJSONAtomic / saveTicket package vars without wiring up the full engine.
//
// Decision on SaveTicket failure: log the warning and return the error
// (fail-closed). The ticket file is less critical than run.json, but leaving it
// inconsistent is confusing; both writes must succeed before the success message
// is printed.
func finalizeRun(
	w, errw io.Writer,
	ticketPath, runPath string,
	ticket tkmd.Ticket,
	run state.RunArtifact,
) error {
	if err := saveTicket(ticketPath, ticket); err != nil {
		_, _ = fmt.Fprintf(errw, "warning: could not save ticket: %v\n", err)
		return fmt.Errorf("save ticket: %w", err)
	}
	if err := saveJSONAtomic(runPath, run); err != nil {
		return fmt.Errorf("persist run state: %w", err)
	}
	_, _ = fmt.Fprintf(w, "status=%s phase=%s\n", run.Status, run.CurrentPhase)
	return nil
}

func initRunCmd(root *cobra.Command) {
	// SilenceUsage is deliberately NOT set on the parent runCmd here. Cobra
	// inherits SilenceUsage from parents, so enabling it on the group would
	// suppress the usage block even for argument-validation errors on the
	// `ticket` / `epic` subcommands — an operator who forgets a ticket id
	// should see `Usage:` telling them how to invoke the command. Each RunE
	// below toggles SilenceUsage itself once control has entered the function,
	// which only happens after arg validation passes.
	runCmd := &cobra.Command{
		Use:     "run [command]",
		Short:   "Run a ticket or epic, or resume the current run",
		GroupID: groupExecution,
		RunE: func(cmd *cobra.Command, args []string) error {
			err := doAutoResume(cmd.OutOrStdout(), cmd.ErrOrStderr())
			if err != nil {
				// doAutoResume already printed a human-readable error line to
				// stdout; silence Cobra's own print+usage so the operator sees
				// a single, non-redundant message.
				cmd.SilenceErrors = true
				cmd.SilenceUsage = true
				return err
			}
			return nil
		},
	}

	runTicketCmd := &cobra.Command{
		Use:   "ticket <ticket-id>",
		Short: "Run a single ticket through the full lifecycle",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := doRunTicket(cmd.OutOrStdout(), cmd.ErrOrStderr(), args[0])
			if err != nil {
				// Runtime errors from RunE do not benefit from a usage dump;
				// arg errors (which run before RunE) are unaffected.
				cmd.SilenceUsage = true
				return withExitCode(err, 1)
			}
			return nil
		},
	}

	runEpicCmd := &cobra.Command{
		Use:   "epic <ticket-id>",
		Short: "Run an epic (all child tickets in waves)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := doRunEpic(cmd.OutOrStdout(), cmd.ErrOrStderr(), args[0])
			if err != nil {
				// Runtime errors from RunE — including blocked-run errors —
				// never want a usage dump: the operator supplied a valid
				// ticket id, so the usage block would only add noise. Arg
				// errors are validated before RunE runs and are untouched.
				cmd.SilenceUsage = true
				// Blocked-run errors print their own retry guidance to
				// stderr; suppress Cobra's default error print so the
				// actionable block is the last thing on the operator's
				// screen. Other runtime errors still get printed by Cobra.
				if _, ok := asBlockedRunError(err); ok {
					cmd.SilenceErrors = true
				}
				return withExitCode(err, 1)
			}
			return nil
		},
	}

	runCmd.AddCommand(runTicketCmd, runEpicCmd)
	root.AddCommand(runCmd)
}

func doRunTicket(w, errw io.Writer, ticketID string) (runID string, err error) {
	emitRunID := true
	defer func() {
		if emitRunID && runID != "" {
			_, _ = fmt.Fprintf(w, "run_id=%s\n", runID)
		}
	}()

	// Cancel engine execution on SIGINT (Ctrl-C) or SIGTERM so that worker
	// processes and MCP helpers are terminated and claims are released before
	// the process exits.  stop() removes the signal handler when the function
	// returns so that subsequent commands get the default signal behaviour.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	repoRoot, cfg, repo, err := loadExecutionContext()
	if err != nil {
		return "", err
	}
	if !cfg.Policy.AllowDirtyWorktree {
		if err := repo.EnsureCleanWorktree(); err != nil {
			return "", err
		}
	}

	ticket, err := tkmd.LoadTicket(filepath.Join(repoRoot, ".tickets", ticketID+".md"))
	if err != nil {
		return "", err
	}
	runID = newRunID(ticketID)
	plan, err := engine.BuildPlanArtifact(ticket, cfg)
	if err != nil {
		return "", err
	}
	plan.RunID = runID
	plan.CreatedAt = time.Now().UTC()
	plan.UpdatedAt = plan.CreatedAt

	lock, err := engine.AcquireRunLock(repoRoot, runID)
	if err != nil {
		return runID, err
	}
	defer func() { _ = lock.Release() }()

	leaseID := fmt.Sprintf("lease-%s-%s", runID, ticketID)
	claim, err := tkmd.AcquireClaim(repoRoot, runID, ticketID, leaseID, 30*time.Minute, time.Now().UTC())
	if err != nil {
		return runID, err
	}
	// Release claim on startup/setup failure before engine takes ownership.
	// The engine releases the claim on its own error/success paths, so we
	// only need this guard for failures that occur before RunTicket is called.
	claimOwned := false
	defer func() {
		if !claimOwned {
			_ = tkmd.ReleaseClaim(repoRoot, runID, ticketID, leaseID, "startup_failure")
		}
	}()

	adapter, err := runtimeAdapterFor(ticket.Runtime, cfg.Runtime.DefaultRuntime)
	if err != nil {
		return runID, err
	}
	baseCommit, err := repo.HeadCommit()
	if err != nil {
		return runID, err
	}
	baseBranch, err := repo.CurrentBranch()
	if err != nil {
		return runID, err
	}

	run := state.RunArtifact{
		ArtifactMeta: state.ArtifactMeta{
			SchemaVersion: 1,
			RunID:         runID,
			CreatedAt:     time.Now().UTC(),
			UpdatedAt:     time.Now().UTC(),
		},
		Mode:         "ticket",
		RootTicketID: ticketID,
		Status:       state.EpicRunStatusRunning,
		CurrentPhase: state.TicketPhaseImplement,
		Policy:       mustJSONMap(cfg.Policy),
		Config:       mustJSONMap(cfg),
		TicketIDs:    []string{ticketID},
		BaseBranch:   baseBranch,
		BaseCommit:   baseCommit,
		ResumeCursor: map[string]any{"ticket_id": ticketID},
	}
	if err := saveJSONAtomic(filepath.Join(repoRoot, ".verk", "runs", runID, "run.json"), run); err != nil {
		return runID, err
	}
	// Write the current-run pointer only after run.json is on disk.  An early
	// return above (lock, claim, adapter, git, or this save) leaves the pointer
	// untouched so subsequent commands never resolve to a run without an artifact.
	if wErr := writeCurrentRunID(repoRoot, runID); wErr != nil {
		_, _ = fmt.Fprintf(errw, "warning: could not write current run: %v\n", wErr)
	}

	ticket.Status = tkmd.StatusInProgress
	if err := tkmd.SaveTicket(filepath.Join(repoRoot, ".tickets", ticketID+".md"), ticket); err != nil {
		return runID, err
	}

	// Run engine with progress channel — engine now owns claim lifecycle.
	claimOwned = true
	ch := make(chan engine.ProgressEvent, 64)
	var result engine.RunTicketResult
	var runErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(ch)
		result, runErr = runTicket(ctx, engine.RunTicketRequest{
			RepoRoot:   repoRoot,
			RunID:      runID,
			Ticket:     ticket,
			Plan:       plan,
			Claim:      claim,
			Adapter:    adapter,
			Config:     cfg,
			BaseCommit: baseCommit,
			Progress:   ch,
		})
	}()

	if tuiErr := runProgress(runID, ch, w, stop); tuiErr != nil {
		_, _ = fmt.Fprintf(errw, "warning: TUI error: %v\n", tuiErr)
		drainProgress(ch)
	}
	// Wait for the engine goroutine to finish before reading result/runErr.
	// runProgress can return early (e.g. on a TUI render error) while the
	// engine goroutine is still writing those variables — a data race without
	// this synchronisation point.
	wg.Wait()

	if runErr != nil {
		return runID, runErr
	}

	switch result.Snapshot.CurrentPhase {
	case state.TicketPhaseClosed:
		ticket.Status = tkmd.StatusClosed
		run.Status = state.EpicRunStatusCompleted
		run.CurrentPhase = state.TicketPhaseClosed
	default:
		ticket.Status = tkmd.StatusBlocked
		run.Status = state.EpicRunStatusBlocked
		run.CurrentPhase = state.TicketPhaseBlocked
	}
	run.UpdatedAt = time.Now().UTC()
	if err := finalizeRun(
		w, errw,
		filepath.Join(repoRoot, ".tickets", ticketID+".md"),
		filepath.Join(repoRoot, ".verk", "runs", runID, "run.json"),
		ticket,
		run,
	); err != nil {
		emitRunID = false
		return runID, err
	}
	return runID, nil
}

func doRunEpic(w, errw io.Writer, ticketID string) (string, error) {
	// Cancel engine execution on SIGINT (Ctrl-C) or SIGTERM so that worker
	// processes and MCP helpers are terminated and claims are released before
	// the process exits.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	repoRoot, cfg, repo, err := loadExecutionContext()
	if err != nil {
		return "", err
	}
	if !cfg.Policy.AllowDirtyWorktree {
		if err := repo.EnsureCleanWorktree(); err != nil {
			return "", err
		}
	}
	adapter, err := runtimeAdapterFor("", cfg.Runtime.DefaultRuntime)
	if err != nil {
		return "", err
	}
	baseCommit, err := repo.HeadCommit()
	if err != nil {
		return "", err
	}
	baseBranch, err := repo.CurrentBranch()
	if err != nil {
		return "", err
	}
	runID := newRunID(ticketID)

	_, _ = fmt.Fprintf(w, "run_id=%s\n", runID)

	// Run engine with progress channel
	ch := make(chan engine.ProgressEvent, 64)
	var result engine.RunEpicResult
	var runErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(ch)
		result, runErr = engine.RunEpic(ctx, engine.RunEpicRequest{
			RepoRoot:     repoRoot,
			RunID:        runID,
			RootTicketID: ticketID,
			BaseBranch:   baseBranch,
			BaseCommit:   baseCommit,
			AdapterFactory: func(ticketPreference string) (runtime.Adapter, error) {
				return runtimeAdapterFor(ticketPreference, cfg.Runtime.DefaultRuntime)
			},
			Adapter:  adapter,
			Config:   cfg,
			Progress: ch,
		})
	}()

	if tuiErr := runProgress(runID, ch, w, stop); tuiErr != nil {
		_, _ = fmt.Fprintf(errw, "warning: TUI error: %v\n", tuiErr)
		drainProgress(ch)
	}
	// Wait for the engine goroutine to finish before reading result/runErr.
	wg.Wait()

	// If engine.RunEpic persisted run.json, make it resumable even when the
	// terminal result is a blocked run that the operator does not retry now.
	currentRunWritten := writeCurrentRunIDIfArtifactExists(repoRoot, runID, errw)

	if runErr != nil {
		// If the run ended in a structured blocked state, hand off to the
		// blocked-run handler so the operator sees which tickets are blocked
		// and how to retry them. For interactive terminals the handler may
		// reopen the selected tickets and resume the run in place; a
		// successful retry replaces runErr with nil so the command exits 0.
		if blocked, ok := asBlockedRunError(runErr); ok {
			retried, retryErr := handleBlockedEpicRun(ctx, w, errw, repoRoot, contextCfgForResume{
				cfg:            cfg,
				defaultRuntime: cfg.Runtime.DefaultRuntime,
			}, blocked)
			if retryErr != nil {
				return runID, retryErr
			}
			if retried {
				return runID, nil
			}
		}
		return runID, runErr
	}

	if !currentRunWritten {
		// The epic run artifact has been persisted by engine.RunEpic before it
		// can return successfully. Write the current-run pointer after that point
		// so .verk/current never points at a run without run.json on disk.
		if wErr := writeCurrentRunID(repoRoot, runID); wErr != nil {
			_, _ = fmt.Fprintf(errw, "warning: could not write current run: %v\n", wErr)
		}
	}

	_, _ = fmt.Fprintf(w, "status=%s phase=%s\n", result.Run.Status, result.Run.CurrentPhase)
	return runID, nil
}

func doAutoResume(w, errw io.Writer) error {
	// Cancel engine execution on SIGINT (Ctrl-C) or SIGTERM so that worker
	// processes and MCP helpers are terminated and claims are released before
	// the process exits.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	repoRoot, cfg, _, err := loadExecutionContext()
	if err != nil {
		_, _ = fmt.Fprintf(w, "Error: %s\n", err)
		return withExitCode(err, 1)
	}

	runID, err := readCurrentRunID(repoRoot)
	if err != nil {
		msg := fmt.Errorf("could not read current run: %w", err)
		_, _ = fmt.Fprintf(w, "Error: %s\n", msg)
		return withExitCode(msg, 1)
	}
	if runID == "" {
		msg := fmt.Errorf("no active run — start one with: verk run ticket <id>")
		_, _ = fmt.Fprintf(w, "Error: %s\n", msg)
		return withExitCode(msg, 1)
	}

	var run state.RunArtifact
	runPath := filepath.Join(repoRoot, ".verk", "runs", runID, "run.json")
	if loadErr := state.LoadJSON(runPath, &run); loadErr != nil {
		if errors.Is(loadErr, os.ErrNotExist) {
			_ = clearCurrentRunID(repoRoot)
			msg := fmt.Errorf("run %s no longer exists (stale pointer cleared) — start a new run with: verk run ticket <id>", runID)
			_, _ = fmt.Fprintf(w, "Error: %s\n", msg)
			return withExitCode(msg, 1)
		}
		msg := fmt.Errorf("could not load run %s: %w", runID, loadErr)
		_, _ = fmt.Fprintf(w, "Error: %s\n", msg)
		return withExitCode(msg, 1)
	}

	switch run.Status {
	case state.EpicRunStatusCompleted:
		_, _ = fmt.Fprintf(w, "Run %s is already completed.\n", runID)
		_, _ = fmt.Fprintf(w, "Start a new one with: verk run ticket <id>\n")
		return nil
	case state.EpicRunStatusBlocked:
		_, _ = fmt.Fprintf(w, "Run %s is blocked — resuming with blocked tickets reset.\n", runID)
		// Fall through to resume: the engine will reset blocked tickets and retry.
	}

	_, _ = fmt.Fprintf(w, "Resuming run %s...\n", runID)

	// Resume with progress channel
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
				return runtimeAdapterFor(ticketPreference, cfg.Runtime.DefaultRuntime)
			},
			Config:   cfg,
			Progress: ch,
		})
	}()

	if tuiErr := runProgress(runID, ch, w, stop); tuiErr != nil {
		_, _ = fmt.Fprintf(errw, "warning: TUI error: %v\n", tuiErr)
		drainProgress(ch)
	}
	// Wait for the engine goroutine to finish before reading report/resumeErr.
	wg.Wait()

	if resumeErr != nil {
		return resumeErr
	}
	_, _ = fmt.Fprintf(w, "status=%s\n", report.Run.Status)
	return nil
}
