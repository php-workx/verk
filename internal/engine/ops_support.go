package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"verk/internal/adapters/ticketstore/tkmd"
	"verk/internal/state"
)

type runArtifacts struct {
	RepoRoot string
	RunID    string
	Run      state.RunArtifact
	Tickets  map[string]TicketRunSnapshot
	Plans    map[string]state.PlanArtifact
	Waves    map[string]state.WaveArtifact
}

func resolveEngineRepoRoot(repoRoot string) (string, error) {
	if repoRoot == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve cwd: %w", err)
		}
		repoRoot = cwd
	}
	abs, err := filepath.Abs(repoRoot)
	if err != nil {
		return "", fmt.Errorf("resolve repo root %q: %w", repoRoot, err)
	}
	return abs, nil
}

func loadRunArtifacts(repoRoot, runID string) (runArtifacts, error) {
	repoRoot, err := resolveEngineRepoRoot(repoRoot)
	if err != nil {
		return runArtifacts{}, err
	}
	if runID == "" {
		return runArtifacts{}, fmt.Errorf("run id is required")
	}

	var run state.RunArtifact
	if err := state.LoadJSON(runJSONPath(repoRoot, runID), &run); err != nil {
		return runArtifacts{}, err
	}

	ticketIDs := append([]string(nil), run.TicketIDs...)
	if len(ticketIDs) == 0 {
		ticketIDs, err = discoverRunTicketIDs(repoRoot, runID)
		if err != nil {
			return runArtifacts{}, err
		}
	}

	tickets := make(map[string]TicketRunSnapshot, len(ticketIDs))
	plans := make(map[string]state.PlanArtifact, len(ticketIDs))
	for _, ticketID := range ticketIDs {
		var snapshot TicketRunSnapshot
		if err := state.LoadJSON(ticketSnapshotPath(repoRoot, runID, ticketID), &snapshot); err != nil {
			return runArtifacts{}, err
		}
		tickets[ticketID] = snapshot

		var plan state.PlanArtifact
		if err := state.LoadJSON(planArtifactPath(repoRoot, runID, ticketID), &plan); err == nil {
			plans[ticketID] = plan
		} else if !os.IsNotExist(extractReadErr(err)) {
			return runArtifacts{}, err
		}
	}

	waveIDs := append([]string(nil), run.WaveIDs...)
	if len(waveIDs) == 0 {
		waveIDs, err = discoverWaveIDs(repoRoot, runID)
		if err != nil {
			return runArtifacts{}, err
		}
	}
	waves := make(map[string]state.WaveArtifact, len(waveIDs))
	for _, waveID := range waveIDs {
		var wave state.WaveArtifact
		if err := state.LoadJSON(waveArtifactPath(repoRoot, runID, waveID), &wave); err != nil {
			return runArtifacts{}, err
		}
		waves[waveID] = wave
	}

	if len(run.TicketIDs) == 0 {
		run.TicketIDs = append([]string(nil), ticketIDs...)
	}
	if len(run.WaveIDs) == 0 {
		run.WaveIDs = append([]string(nil), waveIDs...)
	}

	return runArtifacts{
		RepoRoot: repoRoot,
		RunID:    runID,
		Run:      run,
		Tickets:  tickets,
		Plans:    plans,
		Waves:    waves,
	}, nil
}

func discoverRunTicketIDs(repoRoot, runID string) ([]string, error) {
	paths, err := filepath.Glob(filepath.Join(runDir(repoRoot, runID), "tickets", "*", "ticket-run.json"))
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(paths))
	for _, path := range paths {
		ids = append(ids, filepath.Base(filepath.Dir(path)))
	}
	sort.Strings(ids)
	return ids, nil
}

func discoverWaveIDs(repoRoot, runID string) ([]string, error) {
	paths, err := filepath.Glob(filepath.Join(runDir(repoRoot, runID), "waves", "*.json"))
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(paths))
	for _, path := range paths {
		ids = append(ids, trimJSONExt(filepath.Base(path)))
	}
	sort.Strings(ids)
	return ids, nil
}

func runDir(repoRoot, runID string) string {
	return filepath.Join(repoRoot, ".verk", "runs", runID)
}

func runJSONPath(repoRoot, runID string) string {
	return filepath.Join(runDir(repoRoot, runID), "run.json")
}

func ticketDir(repoRoot, runID, ticketID string) string {
	return filepath.Join(runDir(repoRoot, runID), "tickets", ticketID)
}

func ticketSnapshotPath(repoRoot, runID, ticketID string) string {
	return filepath.Join(ticketDir(repoRoot, runID, ticketID), "ticket-run.json")
}

func planArtifactPath(repoRoot, runID, ticketID string) string {
	return filepath.Join(ticketDir(repoRoot, runID, ticketID), "plan.json")
}

func closeoutArtifactPath(repoRoot, runID, ticketID string) string {
	return filepath.Join(ticketDir(repoRoot, runID, ticketID), "closeout.json")
}

func waveArtifactPath(repoRoot, runID, waveID string) string {
	return filepath.Join(runDir(repoRoot, runID), "waves", waveID+".json")
}

func durableClaimPath(repoRoot, runID, ticketID string) string {
	return filepath.Join(runDir(repoRoot, runID), "claims", "claim-"+ticketID+".json")
}

func liveClaimPath(repoRoot, ticketID string) string {
	return filepath.Join(repoRoot, ".tickets", ".claims", ticketID+".json")
}

func ticketMarkdownPath(repoRoot, ticketID string) string {
	return filepath.Join(repoRoot, ".tickets", ticketID+".md")
}

func loadOptionalClaim(path string) (*state.ClaimArtifact, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var claim state.ClaimArtifact
	if err := json.Unmarshal(data, &claim); err != nil {
		return nil, fmt.Errorf("unmarshal claim %q: %w", path, err)
	}
	return &claim, nil
}

func extractReadErr(err error) error {
	if err == nil {
		return nil
	}
	type unwrapper interface{ Unwrap() error }
	if next, ok := err.(unwrapper); ok && next.Unwrap() != nil {
		return next.Unwrap()
	}
	return err
}

func trimJSONExt(name string) string {
	return name[:len(name)-len(filepath.Ext(name))]
}

func isTerminalPhase(phase state.TicketPhase) bool {
	return phase == state.TicketPhaseBlocked || phase == state.TicketPhaseClosed
}

func appendRunAuditEvent(run *state.RunArtifact, eventType, ticketID string, phase state.TicketPhase, details map[string]any) {
	if run == nil {
		return
	}
	run.AuditEvents = append(run.AuditEvents, state.AuditEvent{
		At:       time.Now().UTC(),
		Type:     eventType,
		TicketID: ticketID,
		Phase:    phase,
		Details:  details,
	})
	run.UpdatedAt = time.Now().UTC()
}

func stateTime() time.Time {
	return time.Now().UTC()
}

func updateRunStatusFromTickets(run *state.RunArtifact, tickets map[string]TicketRunSnapshot) {
	if run == nil {
		return
	}
	anyRunning := false
	for _, snapshot := range tickets {
		switch snapshot.CurrentPhase {
		case state.TicketPhaseBlocked:
			run.Status = state.EpicRunStatusBlocked
			run.CurrentPhase = state.TicketPhaseBlocked
			run.UpdatedAt = time.Now().UTC()
			return
		case state.TicketPhaseClosed:
		default:
			anyRunning = true
		}
	}
	if anyRunning {
		run.Status = state.EpicRunStatusRunning
		run.CurrentPhase = state.TicketPhaseImplement
	} else {
		run.Status = state.EpicRunStatusCompleted
		run.CurrentPhase = state.TicketPhaseClosed
	}
	run.UpdatedAt = time.Now().UTC()
}

func findWaveForTicket(waves map[string]state.WaveArtifact, ticketID string) (state.WaveArtifact, bool) {
	var selected state.WaveArtifact
	found := false
	for _, wave := range waves {
		for _, candidate := range wave.TicketIDs {
			if candidate != ticketID {
				continue
			}
			if !found || wave.Ordinal > selected.Ordinal {
				selected = wave
				found = true
			}
		}
	}
	return selected, found
}

func setTicketReady(repoRoot, ticketID string) error {
	ticket, err := tkmd.LoadTicket(ticketMarkdownPath(repoRoot, ticketID))
	if err != nil {
		return err
	}
	ticket.Status = tkmd.StatusReady
	return tkmd.SaveTicket(ticketMarkdownPath(repoRoot, ticketID), ticket)
}
