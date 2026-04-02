package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	repoadapter "verk/internal/adapters/repo/git"
	"verk/internal/adapters/runtime"
	"verk/internal/adapters/runtime/claude"
	"verk/internal/adapters/runtime/codex"
	"verk/internal/adapters/ticketstore/tkmd"
	"verk/internal/engine"
	"verk/internal/policy"
	"verk/internal/state"
)

func main() {
	os.Exit(execute(os.Args[1:], os.Stdout, os.Stderr))
}

func execute(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: verk <run|reopen|resume|status|doctor> ...")
		return 2
	}

	switch args[0] {
	case "run":
		return runRun(args[1:], stdout, stderr)
	case "reopen":
		return runReopen(args[1:], stdout, stderr)
	case "resume":
		return runResume(args[1:], stdout, stderr)
	case "status":
		return runStatus(args[1:], stdout, stderr)
	case "doctor":
		return runDoctor(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		return 2
	}
}

func runRun(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: verk run <ticket|epic> <ticket-id>")
		return 2
	}
	switch args[0] {
	case "ticket":
		if len(args) != 2 {
			fmt.Fprintln(stderr, "usage: verk run ticket <ticket-id>")
			return 2
		}
		runID, err := runTicket(args[1])
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "run_id=%s\n", runID)
		return 0
	case "epic":
		if len(args) != 2 {
			fmt.Fprintln(stderr, "usage: verk run epic <ticket-id>")
			return 2
		}
		runID, err := runEpic(args[1])
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "run_id=%s\n", runID)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown run subcommand %q\n", args[0])
		return 2
	}
}

func runStatus(args []string, stdout, stderr io.Writer) int {
	args, jsonOut, err := extractBoolFlag(args, "--json")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: verk status <run-id> [--json]")
		return 2
	}

	report, err := engine.DeriveStatus(engine.StatusRequest{RunID: args[0]})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if jsonOut {
		return printJSON(stdout, report)
	}

	fmt.Fprintf(stdout, "run %s: %s (%s)\n", report.RunID, report.RunStatus, report.CurrentPhase)
	if report.CurrentWave != "" {
		fmt.Fprintf(stdout, "current wave: %s\n", report.CurrentWave)
	}
	if report.LastFailedGate != "" {
		fmt.Fprintf(stdout, "last failed gate: %s\n", report.LastFailedGate)
	}
	for _, ticket := range report.Tickets {
		fmt.Fprintf(stdout, "- %s: %s", ticket.TicketID, ticket.Phase)
		if ticket.BlockReason != "" {
			fmt.Fprintf(stdout, " (%s)", ticket.BlockReason)
		}
		fmt.Fprintln(stdout)
	}
	return 0
}

func runDoctor(args []string, stdout, stderr io.Writer) int {
	args, jsonOut, err := extractBoolFlag(args, "--json")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(args) != 0 {
		fmt.Fprintln(stderr, "usage: verk doctor [--json]")
		return 2
	}

	report, code, err := engine.RunDoctor(".")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if jsonOut {
		if printJSON(stdout, report) != 0 {
			return 2
		}
		return code
	}
	fmt.Fprintf(stdout, "repo root: %s\n", report.RepoRoot)
	for _, check := range report.Checks {
		fmt.Fprintf(stdout, "- %s: %s", check.Name, check.Status)
		if check.Details != "" {
			fmt.Fprintf(stdout, " (%s)", check.Details)
		}
		fmt.Fprintln(stdout)
	}
	for _, runtimeCheck := range report.Runtimes {
		status := "unavailable"
		if runtimeCheck.Available {
			status = "available"
		}
		fmt.Fprintf(stdout, "- runtime %s: %s", runtimeCheck.Runtime, status)
		if runtimeCheck.Details != "" {
			fmt.Fprintf(stdout, " (%s)", runtimeCheck.Details)
		}
		fmt.Fprintln(stdout)
	}
	return code
}

func runReopen(args []string, stdout, stderr io.Writer) int {
	args, to, err := extractStringFlag(args, "--to")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(args) != 2 || to == "" {
		fmt.Fprintln(stderr, "usage: verk reopen <run-id> <ticket-id> --to <phase>")
		return 2
	}

	if err := engine.ReopenTicket(context.Background(), engine.ReopenRequest{
		RunID:    args[0],
		TicketID: args[1],
		ToPhase:  state.TicketPhase(to),
	}); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "reopened %s in %s to %s\n", args[1], args[0], to)
	return 0
}

func runResume(args []string, stdout, stderr io.Writer) int {
	args, jsonOut, err := extractBoolFlag(args, "--json")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: verk resume <run-id> [--json]")
		return 2
	}

	report, err := engine.ResumeRun(context.Background(), engine.ResumeRequest{RunID: args[0]})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if jsonOut {
		return printJSON(stdout, report)
	}
	fmt.Fprintf(stdout, "run %s: %s\n", report.Run.RunID, report.Run.Status)
	if len(report.RecoveredTickets) > 0 {
		fmt.Fprintf(stdout, "recovered: %s\n", strings.Join(report.RecoveredTickets, ", "))
	}
	return 0
}

func runTicket(ticketID string) (string, error) {
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
		TicketIDs:    []string{ticketID},
		BaseCommit:   baseCommit,
	}
	if err := state.SaveJSONAtomic(filepath.Join(repoRoot, ".verk", "runs", runID, "run.json"), run); err != nil {
		return "", err
	}

	ticket.Status = tkmd.StatusInProgress
	if err := tkmd.SaveTicket(filepath.Join(repoRoot, ".tickets", ticketID+".md"), ticket); err != nil {
		return "", err
	}

	result, err := engine.RunTicket(context.Background(), engine.RunTicketRequest{
		RepoRoot: repoRoot,
		RunID:    runID,
		Ticket:   ticket,
		Plan:     plan,
		Claim:    claim,
		Adapter:  adapter,
		Config:   cfg,
	})
	if err != nil {
		return "", err
	}
	switch result.Snapshot.CurrentPhase {
	case state.TicketPhaseClosed:
		ticket.Status = tkmd.StatusClosed
		run.Status = state.EpicRunStatusCompleted
		run.CurrentPhase = state.TicketPhaseClosed
	case state.TicketPhaseBlocked:
		ticket.Status = tkmd.StatusBlocked
		run.Status = state.EpicRunStatusBlocked
		run.CurrentPhase = state.TicketPhaseBlocked
	default:
		ticket.Status = tkmd.StatusBlocked
		run.Status = state.EpicRunStatusBlocked
		run.CurrentPhase = state.TicketPhaseBlocked
	}
	run.UpdatedAt = time.Now().UTC()
	if err := tkmd.SaveTicket(filepath.Join(repoRoot, ".tickets", ticketID+".md"), ticket); err != nil {
		return "", err
	}
	if err := state.SaveJSONAtomic(filepath.Join(repoRoot, ".verk", "runs", runID, "run.json"), run); err != nil {
		return "", err
	}
	return runID, nil
}

func runEpic(ticketID string) (string, error) {
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
	runID := newRunID(ticketID)
	if _, err := engine.RunEpic(context.Background(), engine.RunEpicRequest{
		RepoRoot:     repoRoot,
		RunID:        runID,
		RootTicketID: ticketID,
		BaseCommit:   baseCommit,
		Adapter:      adapter,
		Config:       cfg,
	}); err != nil {
		return "", err
	}
	return runID, nil
}

func loadExecutionContext() (string, policy.Config, *repoadapter.Repo, error) {
	repo, err := repoadapter.New(".")
	if err != nil {
		return "", policy.Config{}, nil, err
	}
	repoRoot, err := repo.RepoRoot()
	if err != nil {
		return "", policy.Config{}, nil, err
	}
	cfg, err := policy.LoadConfig(repoRoot)
	if err != nil {
		return "", policy.Config{}, nil, err
	}
	return repoRoot, cfg, repo, nil
}

func runtimeAdapterFor(ticketPreference, defaultRuntime string) (runtime.Adapter, error) {
	switch normalizeRuntime(ticketPreference, defaultRuntime) {
	case "codex":
		return codex.New(), nil
	case "claude":
		return claude.New(), nil
	default:
		return nil, fmt.Errorf("unsupported runtime %q", normalizeRuntime(ticketPreference, defaultRuntime))
	}
}

func normalizeRuntime(ticketPreference, defaultRuntime string) string {
	for _, candidate := range []string{ticketPreference, defaultRuntime, "codex"} {
		candidate = strings.TrimSpace(strings.ToLower(candidate))
		if candidate != "" {
			return candidate
		}
	}
	return "codex"
}

func newRunID(ticketID string) string {
	return fmt.Sprintf("run-%s-%d", ticketID, time.Now().UTC().UnixNano())
}

func printJSON(w io.Writer, v any) int {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	data = append(data, '\n')
	if _, err := w.Write(data); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	return 0
}

func extractBoolFlag(args []string, name string) ([]string, bool, error) {
	rest := make([]string, 0, len(args))
	found := false
	for _, arg := range args {
		if arg == name {
			found = true
			continue
		}
		rest = append(rest, arg)
	}
	return rest, found, nil
}

func extractStringFlag(args []string, name string) ([]string, string, error) {
	rest := make([]string, 0, len(args))
	value := ""
	for i := 0; i < len(args); i++ {
		if args[i] != name {
			rest = append(rest, args[i])
			continue
		}
		if i+1 >= len(args) {
			return nil, "", fmt.Errorf("missing value for %s", name)
		}
		value = args[i+1]
		i++
	}
	return rest, value, nil
}
