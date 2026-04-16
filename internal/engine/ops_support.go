package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"verk/internal/adapters/repo/git"
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
		root, err := git.RepoRoot()
		if err != nil {
			return "", fmt.Errorf("resolve git repo root: %w", err)
		}
		repoRoot = root
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
		if err := loadTicketSnapshot(repoRoot, runID, ticketID, &snapshot); err != nil {
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
	entries, err := os.ReadDir(filepath.Join(runDir(repoRoot, runID), "tickets"))
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			ids = append(ids, entry.Name())
		}
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

func implementationArtifactPath(repoRoot, runID, ticketID string) string {
	return filepath.Join(ticketDir(repoRoot, runID, ticketID), "implementation.json")
}

func verificationArtifactPath(repoRoot, runID, ticketID string) string {
	return filepath.Join(ticketDir(repoRoot, runID, ticketID), "verification.json")
}

func reviewArtifactPath(repoRoot, runID, ticketID string) string {
	return filepath.Join(ticketDir(repoRoot, runID, ticketID), "review-findings.json")
}

func repairCycleArtifactGlob(repoRoot, runID, ticketID string) string {
	return filepath.Join(ticketDir(repoRoot, runID, ticketID), "cycles", "repair-*.json")
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

func loadTicketSnapshot(repoRoot, runID, ticketID string, target *TicketRunSnapshot) error {
	if err := state.LoadJSON(ticketSnapshotPath(repoRoot, runID, ticketID), target); err == nil {
		return nil
	} else if !os.IsNotExist(extractReadErr(err)) {
		return err
	}

	derived, err := deriveTicketSnapshot(repoRoot, runID, ticketID)
	if err != nil {
		return err
	}
	*target = derived
	return nil
}

func deriveTicketSnapshot(repoRoot, runID, ticketID string) (TicketRunSnapshot, error) {
	snapshot := TicketRunSnapshot{
		ArtifactMeta: state.ArtifactMeta{
			SchemaVersion: artifactSchemaVersion,
			RunID:         runID,
			CreatedAt:     stateTime(),
			UpdatedAt:     stateTime(),
		},
		TicketID: ticketID,
	}
	var implementation state.ImplementationArtifact
	if err := state.LoadJSON(implementationArtifactPath(repoRoot, runID, ticketID), &implementation); err == nil {
		snapshot.Implementation = &implementation
	} else if !os.IsNotExist(extractReadErr(err)) {
		return TicketRunSnapshot{}, err
	}
	var verification state.VerificationArtifact
	if err := state.LoadJSON(verificationArtifactPath(repoRoot, runID, ticketID), &verification); err == nil {
		snapshot.Verification = &verification
	} else if !os.IsNotExist(extractReadErr(err)) {
		return TicketRunSnapshot{}, err
	}
	var review state.ReviewFindingsArtifact
	if err := state.LoadJSON(reviewArtifactPath(repoRoot, runID, ticketID), &review); err == nil {
		snapshot.Review = &review
	} else if !os.IsNotExist(extractReadErr(err)) {
		return TicketRunSnapshot{}, err
	}
	var closeout state.CloseoutArtifact
	if err := state.LoadJSON(closeoutArtifactPath(repoRoot, runID, ticketID), &closeout); err == nil {
		snapshot.Closeout = &closeout
	} else if !os.IsNotExist(extractReadErr(err)) {
		return TicketRunSnapshot{}, err
	}
	cyclePaths, err := filepath.Glob(repairCycleArtifactGlob(repoRoot, runID, ticketID))
	if err != nil {
		return TicketRunSnapshot{}, err
	}
	sort.Strings(cyclePaths)
	for _, path := range cyclePaths {
		var cycle state.RepairCycleArtifact
		if err := state.LoadJSON(path, &cycle); err != nil {
			return TicketRunSnapshot{}, err
		}
		snapshot.RepairCycles = append(snapshot.RepairCycles, cycle)
	}
	if snapshot.Implementation != nil {
		snapshot.ImplementationAttempts = snapshot.Implementation.Attempt
	}
	if snapshot.Verification != nil {
		snapshot.VerificationAttempts = snapshot.Verification.Attempt
	}
	if snapshot.Review != nil {
		snapshot.ReviewAttempts = snapshot.Review.Attempt
	}
	switch {
	case snapshot.Closeout != nil && snapshot.Closeout.Closable:
		snapshot.CurrentPhase = state.TicketPhaseClosed
	case snapshot.Closeout != nil && snapshot.Closeout.FailedGate != "":
		snapshot.CurrentPhase = state.TicketPhaseBlocked
		snapshot.BlockReason = snapshot.Closeout.FailedGate
	case len(snapshot.RepairCycles) > 0:
		last := snapshot.RepairCycles[len(snapshot.RepairCycles)-1]
		if last.Status == "repair_pending" {
			snapshot.CurrentPhase = state.TicketPhaseRepair
		} else {
			snapshot.CurrentPhase = state.TicketPhaseReview
		}
	case snapshot.Review != nil:
		snapshot.CurrentPhase = state.TicketPhaseReview
	case snapshot.Verification != nil:
		snapshot.CurrentPhase = state.TicketPhaseVerify
	case snapshot.Implementation != nil:
		snapshot.CurrentPhase = state.TicketPhaseImplement
	default:
		snapshot.CurrentPhase = state.TicketPhaseIntake
	}
	return snapshot, nil
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

func configMap(v any) map[string]any {
	data, err := json.Marshal(v)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return map[string]any{}
	}
	return out
}

// phasePriority defines the ordering of active ticket phases.
// Higher values indicate higher priority (later in the workflow).
// When computing the run's current phase from multiple tickets,
// the highest-priority active phase is used so the run reflects
// the most advanced work in progress.
var phasePriority = map[state.TicketPhase]int{
	state.TicketPhaseImplement: 1,
	state.TicketPhaseVerify:    2,
	state.TicketPhaseReview:    3,
	state.TicketPhaseRepair:    4,
	state.TicketPhaseCloseout:  5,
}

func updateRunStatusFromTickets(run *state.RunArtifact, tickets map[string]TicketRunSnapshot) {
	if run == nil {
		return
	}
	anyRunning := false
	var highestPhase state.TicketPhase
	highestPriority := -1
	for _, snapshot := range tickets {
		switch snapshot.CurrentPhase {
		case state.TicketPhaseBlocked:
			run.Status = state.EpicRunStatusBlocked
			run.CurrentPhase = state.TicketPhaseBlocked
			run.UpdatedAt = time.Now().UTC()
			return
		case state.TicketPhaseClosed:
			// Terminal — does not contribute to the active phase.
		default:
			anyRunning = true
			if p, ok := phasePriority[snapshot.CurrentPhase]; ok && p > highestPriority {
				highestPriority = p
				highestPhase = snapshot.CurrentPhase
			}
		}
	}
	if anyRunning {
		run.Status = state.EpicRunStatusRunning
		if highestPriority >= 0 {
			run.CurrentPhase = highestPhase
		} else {
			run.CurrentPhase = state.TicketPhaseImplement
		}
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
	ticket.Status = tkmd.StatusOpen
	return tkmd.SaveTicket(ticketMarkdownPath(repoRoot, ticketID), ticket)
}
