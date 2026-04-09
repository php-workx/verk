package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"verk/internal/adapters/runtime"
	"verk/internal/adapters/ticketstore/tkmd"
	"verk/internal/engine"
	"verk/internal/state"

	"github.com/spf13/cobra"
)

var runCmd = &cobra.Command{
	Use:     "run",
	Short:   "Run a ticket or epic",
	GroupID: groupExecution,
}

var runTicketCmd = &cobra.Command{
	Use:   "ticket <ticket-id>",
	Short: "Run a single ticket through the full lifecycle",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		runID, err := doRunTicket(args[0])
		if err != nil {
			return withExitCode(err, 1)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "run_id=%s\n", runID)
		return nil
	},
}

var runEpicCmd = &cobra.Command{
	Use:   "epic <ticket-id>",
	Short: "Run an epic (all child tickets in waves)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		runID, err := doRunEpic(args[0])
		if err != nil {
			return withExitCode(err, 1)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "run_id=%s\n", runID)
		return nil
	},
}

func initRunCmd() {
	runCmd.AddCommand(runTicketCmd, runEpicCmd)
	rootCmd.AddCommand(runCmd)
}

func doRunTicket(ticketID string) (string, error) {
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
	runID := newRunID(ticketID)
	plan, err := engine.BuildPlanArtifact(ticket, cfg)
	if err != nil {
		return "", err
	}
	plan.RunID = runID
	plan.CreatedAt = time.Now().UTC()
	plan.UpdatedAt = plan.CreatedAt

	leaseID := fmt.Sprintf("lease-%s-%s", runID, ticketID)
	claim, err := tkmd.AcquireClaim(repoRoot, runID, ticketID, leaseID, 30*time.Minute, time.Now().UTC())
	if err != nil {
		return "", err
	}
	adapter, err := runtimeAdapterFor(ticket.Runtime, cfg.Runtime.DefaultRuntime)
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
	if err := state.SaveJSONAtomic(filepath.Join(repoRoot, ".verk", "runs", runID, "run.json"), run); err != nil {
		return "", err
	}

	ticket.Status = tkmd.StatusInProgress
	if err := tkmd.SaveTicket(filepath.Join(repoRoot, ".tickets", ticketID+".md"), ticket); err != nil {
		return "", err
	}

	result, err := engine.RunTicket(context.Background(), engine.RunTicketRequest{
		RepoRoot:   repoRoot,
		RunID:      runID,
		Ticket:     ticket,
		Plan:       plan,
		Claim:      claim,
		Adapter:    adapter,
		Config:     cfg,
		BaseCommit: baseCommit,
	})
	if err != nil {
		return "", err
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
	_ = tkmd.SaveTicket(filepath.Join(repoRoot, ".tickets", ticketID+".md"), ticket)
	_ = state.SaveJSONAtomic(filepath.Join(repoRoot, ".verk", "runs", runID, "run.json"), run)
	return runID, nil
}

func doRunEpic(ticketID string) (string, error) {
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
	if _, err := engine.RunEpic(context.Background(), engine.RunEpicRequest{
		RepoRoot:     repoRoot,
		RunID:        runID,
		RootTicketID: ticketID,
		BaseBranch:   baseBranch,
		BaseCommit:   baseCommit,
		AdapterFactory: func(ticketPreference string) (runtime.Adapter, error) {
			return runtimeAdapterFor(ticketPreference, cfg.Runtime.DefaultRuntime)
		},
		Adapter: adapter,
		Config:  cfg,
	}); err != nil {
		return "", err
	}
	return runID, nil
}
