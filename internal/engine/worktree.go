package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"verk/internal/adapters/repo/git"
	"verk/internal/state"
)

const (
	worktreeHashLen = 12
)

// WorktreeManager coordinates per-wave worktrees.
type WorktreeManager struct {
	mainRoot  string
	baseRef   string // current wave base commit/ref
	runID     string
	workRoot  string // cache dir from ver-wi07
	mu        sync.Mutex
	worktrees map[string]string // ticketID -> absolute worktree path
}

// Conflict represents a file path touched by multiple tickets in the same wave.
type Conflict struct {
	Path      string
	TicketIDs []string
}

// IntraWaveConflictError indicates that one or more files were touched by
// multiple tickets in the same wave. Ticket IDs are sorted for deterministic
// comparison and output.
type IntraWaveConflictError struct {
	Conflicts []Conflict
}

func (e *IntraWaveConflictError) Error() string {
	if e == nil || len(e.Conflicts) == 0 {
		return "no intra-wave file conflicts"
	}

	parts := make([]string, 0, len(e.Conflicts))
	for _, conflict := range e.Conflicts {
		if conflict.Path == "" {
			continue
		}
		if len(conflict.TicketIDs) == 0 {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s:%s", conflict.Path, strings.Join(conflict.TicketIDs, ",")))
	}
	if len(parts) == 0 {
		return "intra-wave file conflicts"
	}
	return "intra-wave conflicts: " + strings.Join(parts, "; ")
}

func NewWorktreeManager(mainRoot, baseRef, runID, workRoot string) *WorktreeManager {
	return &WorktreeManager{
		mainRoot:  strings.TrimSpace(mainRoot),
		baseRef:   strings.TrimSpace(baseRef),
		runID:     strings.TrimSpace(runID),
		workRoot:  strings.TrimSpace(workRoot),
		worktrees: map[string]string{},
	}
}

func (wm *WorktreeManager) CreateWorktree(ctx context.Context, ticketID string) (string, error) {
	if wm == nil {
		return "", fmt.Errorf("worktree manager is nil")
	}
	ticketID = strings.TrimSpace(ticketID)
	if ticketID == "" {
		return "", fmt.Errorf("ticket id is required")
	}
	if strings.Contains(ticketID, "/") || strings.Contains(ticketID, "..") || strings.Contains(ticketID, "\\") {
		return "", fmt.Errorf("ticket id %q contains invalid path characters", ticketID)
	}
	if wm.mainRoot == "" {
		return "", fmt.Errorf("main root is required")
	}
	baseRef := strings.TrimSpace(wm.baseRef)
	if baseRef == "" {
		return "", fmt.Errorf("base ref is required")
	}
	if wm.workRoot == "" {
		return "", fmt.Errorf("work root is required")
	}

	wm.mu.Lock()
	defer wm.mu.Unlock()

	worktreePath := filepath.Join(wm.workRoot, wm.runID, ticketID)
	repo, err := git.New(wm.mainRoot)
	if err != nil {
		return "", fmt.Errorf("open main repo %q: %w", wm.mainRoot, err)
	}

	err = repo.CreateWorktree(ctx, baseRef, worktreePath)
	if err != nil && !errors.Is(err, git.ErrWorktreeExists) {
		return "", fmt.Errorf("create worktree for ticket %q: %w", ticketID, err)
	}

	if errors.Is(err, git.ErrWorktreeExists) {
		if removeErr := repo.RemoveWorktree(worktreePath); removeErr != nil {
			return "", fmt.Errorf("remove existing worktree for ticket %q: %w", ticketID, removeErr)
		}
		if createErr := repo.CreateWorktree(ctx, baseRef, worktreePath); createErr != nil {
			return "", fmt.Errorf("recreate worktree for ticket %q: %w", ticketID, createErr)
		}
	}

	if symErr := establishWorktreeVerkSymlink(worktreePath, wm.mainRoot); symErr != nil {
		if removeErr := repo.RemoveWorktree(worktreePath); removeErr != nil {
			return "", fmt.Errorf("create worktree symlink for ticket %q: %w; cleanup: %v", ticketID, symErr, removeErr)
		}
		return "", fmt.Errorf("create worktree symlink for ticket %q: %w", ticketID, symErr)
	}

	wm.worktrees[ticketID] = worktreePath
	return worktreePath, nil
}

func (wm *WorktreeManager) WorktreePath(ticketID string) string {
	if wm == nil {
		return ""
	}
	ticketID = strings.TrimSpace(ticketID)
	if ticketID == "" {
		return ""
	}

	wm.mu.Lock()
	defer wm.mu.Unlock()
	return wm.worktrees[ticketID]
}

func (wm *WorktreeManager) ChangedFiles(ticketID string) ([]string, error) {
	raw, err := wm.RawChangedFiles(ticketID)
	if err != nil {
		return nil, err
	}
	return dedupeAndSortChanged(filterEngineOwnedFilesInternal(raw)), nil
}

func (wm *WorktreeManager) RawChangedFiles(ticketID string) ([]string, error) {
	if wm == nil {
		return nil, fmt.Errorf("worktree manager is nil")
	}

	worktreePath := wm.WorktreePath(ticketID)
	if worktreePath == "" {
		return nil, fmt.Errorf("no worktree for ticket %q", strings.TrimSpace(ticketID))
	}

	repo, err := git.New(worktreePath)
	if err != nil {
		return nil, fmt.Errorf("open worktree %q: %w", worktreePath, err)
	}

	changed, err := repo.ChangedFilesAgainst(wm.baseRef)
	if err != nil {
		return nil, fmt.Errorf("collect changed files in %q against %q: %w", worktreePath, wm.baseRef, err)
	}
	return dedupeAndSortChanged(changed), nil
}

func (wm *WorktreeManager) DetectConflicts() ([]Conflict, error) {
	if wm == nil {
		return nil, fmt.Errorf("worktree manager is nil")
	}

	wm.mu.Lock()
	ticketIDs := make([]string, 0, len(wm.worktrees))
	for ticketID := range wm.worktrees {
		ticketIDs = append(ticketIDs, strings.TrimSpace(ticketID))
	}
	wm.mu.Unlock()

	return wm.DetectConflictsFor(ticketIDs)
}

func (wm *WorktreeManager) DetectConflictsFor(ticketIDs []string) ([]Conflict, error) {
	if wm == nil {
		return nil, fmt.Errorf("worktree manager is nil")
	}

	sort.Strings(ticketIDs)
	pathToTicketIDs := make(map[string]map[string]struct{}, len(ticketIDs)*2)
	for _, ticketID := range ticketIDs {
		if ticketID == "" {
			continue
		}
		changed, err := wm.ChangedFiles(ticketID)
		if err != nil {
			return nil, fmt.Errorf("collect changed files for ticket %q: %w", ticketID, err)
		}
		for _, path := range dedupeAndSortChanged(changed) {
			if path == "" {
				continue
			}
			tickets, ok := pathToTicketIDs[path]
			if !ok {
				tickets = map[string]struct{}{}
				pathToTicketIDs[path] = tickets
			}
			tickets[ticketID] = struct{}{}
		}
	}

	conflicts := make([]Conflict, 0, len(pathToTicketIDs))
	for path, tickets := range pathToTicketIDs {
		if len(tickets) < 2 {
			continue
		}
		ids := make([]string, 0, len(tickets))
		for ticketID := range tickets {
			ids = append(ids, ticketID)
		}
		sort.Strings(ids)
		conflicts = append(conflicts, Conflict{
			Path:      path,
			TicketIDs: uniqueSorted(ids),
		})
	}

	sort.Slice(conflicts, func(i, j int) bool {
		return conflicts[i].Path < conflicts[j].Path
	})

	return conflicts, nil
}

func (wm *WorktreeManager) Diff(ticketID string) (string, error) {
	if wm == nil {
		return "", fmt.Errorf("worktree manager is nil")
	}

	worktreePath := wm.WorktreePath(ticketID)
	if worktreePath == "" {
		return "", fmt.Errorf("no worktree for ticket %q", strings.TrimSpace(ticketID))
	}

	repo, err := git.New(worktreePath)
	if err != nil {
		return "", fmt.Errorf("open worktree %q: %w", worktreePath, err)
	}
	diff, err := repo.DiffAgainst(wm.baseRef)
	if err != nil {
		return "", fmt.Errorf("collect diff in %q against %q: %w", worktreePath, wm.baseRef, err)
	}
	return diff, nil
}

func (wm *WorktreeManager) CleanupAll() error {
	if wm == nil {
		return fmt.Errorf("worktree manager is nil")
	}

	wm.mu.Lock()
	entries := make(map[string]string, len(wm.worktrees))
	for ticketID, pathValue := range wm.worktrees {
		entries[ticketID] = pathValue
	}
	wm.worktrees = map[string]string{}
	wm.mu.Unlock()

	var resultErr error
	for ticketID, worktreePath := range entries {
		worktreePath = strings.TrimSpace(worktreePath)
		if worktreePath == "" {
			msg := fmt.Errorf("cleanup worktree path for ticket %q is required", ticketID)
			log.Printf("[WARN] %v", msg)
			resultErr = errors.Join(resultErr, msg)
			continue
		}

		repo, err := git.New(wm.mainRoot)
		if err != nil {
			msg := fmt.Errorf("open main repo %q for ticket %q: %w", wm.mainRoot, ticketID, err)
			log.Printf("[WARN] %v", msg)
			resultErr = errors.Join(resultErr, msg)
			continue
		}

		if err := repo.RemoveWorktree(worktreePath); err != nil {
			msg := fmt.Errorf("cleanup worktree %q for ticket %q: %w", worktreePath, ticketID, err)
			log.Printf("[WARN] %v", msg)
			resultErr = errors.Join(resultErr, msg)
		}
	}

	return resultErr
}

func prepareWaveWorktrees(ctx context.Context, repoRoot, baseRef, runID, workRoot string, ticketIDs []string) (*WorktreeManager, error) {
	if strings.TrimSpace(workRoot) == "" {
		resolvedRoot, err := ResolveWorktreeRoot(repoRoot)
		if err != nil {
			return nil, err
		}
		workRoot = resolvedRoot
	}
	manager := NewWorktreeManager(repoRoot, baseRef, runID, workRoot)
	for _, ticketID := range ticketIDs {
		if strings.TrimSpace(ticketID) == "" {
			continue
		}
		if _, err := manager.CreateWorktree(ctx, ticketID); err != nil {
			_ = manager.CleanupAll()
			return nil, err
		}
	}
	return manager, nil
}

type WaveIntegrationManager struct {
	repoRoot     string
	runID        string
	baseRef      string
	worktreePath string
}

func integrationBaseRef(runID string) string {
	return fmt.Sprintf("refs/verk/runs/%s/base", strings.TrimSpace(runID))
}

func integrationTicketRef(runID, ticketID string) string {
	return fmt.Sprintf("refs/verk/runs/%s/tickets/%s", strings.TrimSpace(runID), strings.TrimSpace(ticketID))
}

func ensureIntegrationBaseRef(repoRoot, runID, baseCommit string) (string, error) {
	refName := integrationBaseRef(runID)
	if _, err := gitRevParse(repoRoot, refName); err == nil {
		return refName, nil
	}
	if err := gitUpdateRef(repoRoot, refName, baseCommit); err != nil {
		return "", fmt.Errorf("initialize integration base ref %q at %q: %w", refName, baseCommit, err)
	}
	return refName, nil
}

func prepareWaveIntegration(ctx context.Context, repoRoot, runID, workRoot, baseRef string) (*WaveIntegrationManager, error) {
	if strings.TrimSpace(workRoot) == "" {
		resolvedRoot, err := ResolveWorktreeRoot(repoRoot)
		if err != nil {
			return nil, err
		}
		workRoot = resolvedRoot
	}
	refName, err := ensureIntegrationBaseRef(repoRoot, runID, baseRef)
	if err != nil {
		return nil, err
	}

	repo, err := git.New(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("open repo root %q: %w", repoRoot, err)
	}
	worktreePath := filepath.Join(workRoot, runID, "_integration")
	if err := repo.RemoveWorktree(worktreePath); err != nil && !worktreePathMissing(worktreePath) {
		return nil, fmt.Errorf("reset integration worktree %q: %w", worktreePath, err)
	}
	if err := repo.CreateWorktree(ctx, refName, worktreePath); err != nil {
		return nil, fmt.Errorf("create integration worktree %q from %q: %w", worktreePath, refName, err)
	}
	if err := establishWorktreeVerkSymlink(worktreePath, repoRoot); err != nil {
		_ = repo.RemoveWorktree(worktreePath)
		return nil, fmt.Errorf("link integration .verk symlink: %w", err)
	}
	return &WaveIntegrationManager{
		repoRoot:     strings.TrimSpace(repoRoot),
		runID:        strings.TrimSpace(runID),
		baseRef:      refName,
		worktreePath: worktreePath,
	}, nil
}

func (m *WaveIntegrationManager) WorktreePath() string {
	if m == nil {
		return ""
	}
	return m.worktreePath
}

func (m *WaveIntegrationManager) BaseRef() string {
	if m == nil {
		return ""
	}
	return m.baseRef
}

func (m *WaveIntegrationManager) Cleanup() error {
	if m == nil || strings.TrimSpace(m.worktreePath) == "" {
		return nil
	}
	repo, err := git.New(m.repoRoot)
	if err != nil {
		return fmt.Errorf("open repo root %q: %w", m.repoRoot, err)
	}
	return repo.RemoveWorktree(m.worktreePath)
}

func (m *WaveIntegrationManager) FreezeAcceptedTicket(ticketID, worktreePath string, changedFiles []string) (string, error) {
	if m == nil {
		return "", fmt.Errorf("wave integration manager is nil")
	}
	if strings.TrimSpace(worktreePath) == "" {
		return "", fmt.Errorf("accepted ticket %q worktree path is required", ticketID)
	}
	refName := integrationTicketRef(m.runID, ticketID)

	if len(changedFiles) > 0 {
		if err := gitAddAllPaths(worktreePath, changedFiles); err != nil {
			return "", fmt.Errorf("stage accepted ticket %q: %w", ticketID, err)
		}
	}

	changes, err := effectiveGitStatusChanges(worktreePath)
	if err != nil {
		return "", fmt.Errorf("read accepted ticket %q status: %w", ticketID, err)
	}
	if len(changes) > 0 {
		if err := gitCommitChanges(worktreePath, fmt.Sprintf("verk: freeze %s", ticketID)); err != nil {
			return "", fmt.Errorf("commit accepted ticket %q: %w", ticketID, err)
		}
	}

	if err := m.validateAcceptedTicketTree(ticketID, worktreePath); err != nil {
		return "", err
	}

	head, err := gitRevParse(worktreePath, "HEAD")
	if err != nil {
		return "", fmt.Errorf("resolve accepted ticket %q head: %w", ticketID, err)
	}
	if err := gitUpdateRef(m.repoRoot, refName, head); err != nil {
		return "", fmt.Errorf("update accepted ticket ref %q: %w", refName, err)
	}
	return refName, nil
}

func (m *WaveIntegrationManager) validateAcceptedTicketTree(ticketID, worktreePath string) error {
	baseRef := strings.TrimSpace(m.baseRef)
	if baseRef == "" {
		return fmt.Errorf("validate accepted ticket %q tree: base ref is required", ticketID)
	}
	changed, err := gitTouchedPathsInCommitRange(worktreePath, baseRef, "HEAD")
	if err != nil {
		return fmt.Errorf("validate accepted ticket %q tree: %w", ticketID, err)
	}
	for _, file := range changed {
		if isEngineOwned(file) {
			return fmt.Errorf("accepted ticket %q contains engine-owned path %q", ticketID, file)
		}
	}
	return nil
}

func (m *WaveIntegrationManager) ApplyAcceptedTicketRefs(ctx context.Context, refs []string) error {
	if m == nil {
		return fmt.Errorf("wave integration manager is nil")
	}
	if len(refs) == 0 {
		return nil
	}
	args := make([]string, 0, 4+len(refs))
	args = append(args, "-C", m.worktreePath, "cherry-pick", "--no-commit")
	args = append(args, refs...)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = engineGitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(bytes.TrimSpace(out)))
		if msg != "" {
			return fmt.Errorf("apply accepted ticket refs: %w: %s", err, msg)
		}
		return fmt.Errorf("apply accepted ticket refs: %w", err)
	}
	return nil
}

func (m *WaveIntegrationManager) ApplyToMain(changedFiles []string) error {
	if m == nil {
		return fmt.Errorf("wave integration manager is nil")
	}
	if len(changedFiles) == 0 {
		return nil
	}
	manager := &WorktreeManager{
		mainRoot:  m.repoRoot,
		worktrees: map[string]string{"integration": m.worktreePath},
	}
	if err := manager.MergeToMain("integration"); err != nil {
		return err
	}
	if err := gitAddAllPaths(m.repoRoot, changedFiles); err != nil {
		return fmt.Errorf("stage integrated wave changes in main tree: %w", err)
	}
	return nil
}

func (m *WaveIntegrationManager) CommitWave(waveID string) (string, error) {
	if m == nil {
		return "", fmt.Errorf("wave integration manager is nil")
	}
	changes, err := effectiveGitStatusChanges(m.worktreePath)
	if err != nil {
		return "", fmt.Errorf("read integration status for %s: %w", waveID, err)
	}
	if len(changes) > 0 {
		if err := gitAddAllPaths(m.worktreePath, mergeChangePaths(changes)); err != nil {
			return "", fmt.Errorf("stage integrated wave %s: %w", waveID, err)
		}
		if err := gitCommitChanges(m.worktreePath, fmt.Sprintf("verk: integrate %s", waveID)); err != nil {
			return "", fmt.Errorf("commit integrated wave %s: %w", waveID, err)
		}
	}
	head, err := gitRevParse(m.worktreePath, "HEAD")
	if err != nil {
		return "", fmt.Errorf("resolve integration head for %s: %w", waveID, err)
	}
	if err := gitUpdateRef(m.repoRoot, m.baseRef, head); err != nil {
		return "", fmt.Errorf("advance integration base ref %q: %w", m.baseRef, err)
	}
	return head, nil
}

func (m *WaveIntegrationManager) RollbackBaseRef(previousHead, expectedCurrent string) error {
	if m == nil {
		return fmt.Errorf("wave integration manager is nil")
	}
	if strings.TrimSpace(previousHead) == "" {
		return fmt.Errorf("previous integration base head is empty")
	}
	if strings.TrimSpace(expectedCurrent) == "" {
		return fmt.Errorf("expected current integration base head is empty")
	}
	if err := gitUpdateRefExpected(m.repoRoot, m.baseRef, previousHead, expectedCurrent); err != nil {
		return fmt.Errorf("rollback integration base ref %q to %s: %w", m.baseRef, previousHead, err)
	}
	return nil
}

func assertMainTreeMatchesWaveBase(repoRoot, baseRef string) error {
	repo, err := git.New(repoRoot)
	if err != nil {
		return fmt.Errorf("open repo root %q: %w", repoRoot, err)
	}
	changed, err := repo.ChangedFilesAgainst(baseRef)
	if err != nil {
		return fmt.Errorf("compare main tree against wave base %q: %w", baseRef, err)
	}
	effective := filterEngineOwnedFilesInternal(changed)
	if len(effective) == 0 {
		return nil
	}
	limit := effective
	if len(limit) > 8 {
		limit = append(append([]string(nil), effective[:8]...), fmt.Sprintf("... (%d more)", len(effective)-8))
	}
	return fmt.Errorf("dirty main tree relative to wave base %s: %s", strings.TrimSpace(baseRef), strings.Join(limit, ", "))
}

func effectiveGitStatusChanges(worktreePath string) ([]mergeToMainChange, error) {
	rawStatus, err := gitStatusPorcelainV2(worktreePath)
	if err != nil {
		return nil, err
	}
	changes, err := parsePorcelainToMergeChanges(rawStatus)
	if err != nil {
		return nil, err
	}
	return filterSyntheticWorktreeChanges(changes), nil
}

func gitAddAllPaths(worktreePath string, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	args := make([]string, 0, 5+len(paths))
	args = append(args, "-C", worktreePath, "add", "-A", "--")
	args = append(args, paths...)
	cmd := exec.Command("git", args...)
	cmd.Env = engineGitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(bytes.TrimSpace(out)))
		if msg != "" {
			return fmt.Errorf("git add tracked paths: %w: %s", err, msg)
		}
		return fmt.Errorf("git add tracked paths: %w", err)
	}
	return nil
}

func gitCommitChanges(worktreePath, message string) error {
	cmd := exec.Command("git", "-C", worktreePath, "commit", "-m", message)
	cmd.Env = engineGitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(bytes.TrimSpace(out)))
		if msg != "" {
			return fmt.Errorf("git commit: %w: %s", err, msg)
		}
		return fmt.Errorf("git commit: %w", err)
	}
	return nil
}

func gitTouchedPathsInCommitRange(worktreePath, fromRef, toRef string) ([]string, error) {
	rangeSpec := fmt.Sprintf("%s..%s", fromRef, toRef)
	cmd := exec.Command("git", "-C", worktreePath, "log", "--format=", "--name-status", "-z", "--find-renames", "--find-copies", rangeSpec, "--")
	cmd.Env = engineGitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(bytes.TrimSpace(out)))
		if msg != "" {
			return nil, fmt.Errorf("git log --name-status %s: %w: %s", rangeSpec, err, msg)
		}
		return nil, fmt.Errorf("git log --name-status %s: %w", rangeSpec, err)
	}
	if len(out) == 0 {
		return nil, nil
	}
	parts := bytes.Split(out, []byte{0})
	touched := make([]string, 0, len(parts))
	for i := 0; i < len(parts); {
		status := strings.TrimSpace(string(parts[i]))
		i++
		if status == "" {
			continue
		}
		if i >= len(parts) {
			return nil, fmt.Errorf("parse git log --name-status %s: missing path for status %q", rangeSpec, status)
		}

		oldPath := string(parts[i])
		i++
		if oldPath != "" {
			touched = append(touched, oldPath)
		}
		switch status[0] {
		case 'R', 'C':
			if i >= len(parts) {
				return nil, fmt.Errorf("parse git log --name-status %s: missing destination for status %q", rangeSpec, status)
			}
			newPath := string(parts[i])
			i++
			if newPath != "" {
				touched = append(touched, newPath)
			}
		}
	}
	return dedupeAndSortChanged(touched), nil
}

func gitRevParse(worktreePath, ref string) (string, error) {
	cmd := exec.Command("git", "-C", worktreePath, "rev-parse", "--verify", ref+"^{commit}")
	cmd.Env = engineGitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(bytes.TrimSpace(out)))
		if msg != "" {
			return "", fmt.Errorf("git rev-parse %s: %w: %s", ref, err, msg)
		}
		return "", fmt.Errorf("git rev-parse %s: %w", ref, err)
	}
	return strings.TrimSpace(string(out)), nil
}

func gitUpdateRef(repoRoot, refName, commitish string) error {
	cmd := exec.Command("git", "-C", repoRoot, "update-ref", refName, commitish)
	cmd.Env = engineGitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(bytes.TrimSpace(out)))
		if msg != "" {
			return fmt.Errorf("git update-ref %s %s: %w: %s", refName, commitish, err, msg)
		}
		return fmt.Errorf("git update-ref %s %s: %w", refName, commitish, err)
	}
	return nil
}

func gitUpdateRefExpected(repoRoot, refName, commitish, expectedOld string) error {
	cmd := exec.Command("git", "-C", repoRoot, "update-ref", refName, commitish, expectedOld)
	cmd.Env = engineGitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(bytes.TrimSpace(out)))
		if msg != "" {
			return fmt.Errorf("git update-ref %s %s %s: %w: %s", refName, commitish, expectedOld, err, msg)
		}
		return fmt.Errorf("git update-ref %s %s %s: %w", refName, commitish, expectedOld, err)
	}
	return nil
}

func persistWorktreeDiff(repoRoot, runID, ticketID, diff string) error {
	if strings.TrimSpace(diff) == "" {
		return nil
	}
	diffPath := filepath.Join(repoRoot, ".verk", "runs", runID, "tickets", ticketID, "worktree.diff")
	if err := os.MkdirAll(filepath.Dir(diffPath), 0o755); err != nil {
		return fmt.Errorf("create diff artifact dir: %w", err)
	}
	if err := os.WriteFile(diffPath, []byte(diff), 0o644); err != nil {
		return fmt.Errorf("write diff artifact %q: %w", diffPath, err)
	}
	return nil
}

func worktreePathMissing(path string) bool {
	_, err := os.Stat(path)
	return err != nil && os.IsNotExist(err)
}

func changedFilesFromManager(manager *WorktreeManager, ticketIDs []string) ([]string, error) {
	if manager == nil {
		return nil, nil
	}
	all := make([]string, 0)
	for _, ticketID := range ticketIDs {
		changed, err := manager.ChangedFiles(ticketID)
		if err != nil {
			return nil, err
		}
		all = append(all, changed...)
	}
	return dedupeAndSortChanged(all), nil
}

func rawChangedFilesFromManager(manager *WorktreeManager, ticketIDs []string) ([]string, error) {
	if manager == nil {
		return nil, nil
	}
	all := make([]string, 0)
	for _, ticketID := range ticketIDs {
		changed, err := manager.RawChangedFiles(ticketID)
		if err != nil {
			return nil, err
		}
		all = append(all, changed...)
	}
	return dedupeAndSortChanged(all), nil
}

func establishWorktreeVerkSymlink(worktreePath, mainRoot string) error {
	linkPath := filepath.Join(worktreePath, ".verk")
	targetPath := filepath.Join(mainRoot, ".verk")

	if err := os.RemoveAll(linkPath); err != nil {
		return fmt.Errorf("remove existing %q: %w", linkPath, err)
	}
	if err := os.Symlink(targetPath, linkPath); err != nil {
		return fmt.Errorf("create .verk symlink %q -> %q: %w", linkPath, targetPath, err)
	}
	return nil
}

// ResolveWorktreeRoot resolves and ensures the cache root for this repository.
// It uses the configured base root order:
// 1) VERK_WORKTREE_ROOT
// 2) XDG_CACHE_HOME/verk/worktrees
// 3) HOME/.cache/verk/worktrees
// 4) os.TempDir()/verk/worktrees
// It then appends a per-repo hash to avoid cross-repo collisions.
func ResolveWorktreeRoot(mainRoot string) (string, error) {
	if strings.TrimSpace(mainRoot) == "" {
		return "", fmt.Errorf("main root is required")
	}
	mainRootAbs, err := filepath.Abs(mainRoot)
	if err != nil {
		return "", fmt.Errorf("resolve main worktree root %q: %w", mainRoot, err)
	}

	cacheRoot, err := resolveWorktreeCacheRoot()
	if err != nil {
		return "", err
	}

	repoHash := repoWorktreeHash(mainRootAbs)
	workRoot := filepath.Join(cacheRoot, repoHash)
	if err := os.MkdirAll(workRoot, 0o755); err != nil {
		return "", fmt.Errorf("create worktree root %q: %w", workRoot, err)
	}
	return workRoot, nil
}

func ReconcileWorktrees(ctx context.Context, mainRoot, workRoot string) error {
	if ctx == nil {
		ctx = context.Background()
	}

	mainRoot = strings.TrimSpace(mainRoot)
	workRoot = strings.TrimSpace(workRoot)
	if mainRoot == "" {
		return fmt.Errorf("main root is required")
	}
	if workRoot == "" {
		return fmt.Errorf("worktree root is required")
	}

	var resultErr error
	if err := runGitWorktreePrune(ctx, mainRoot); err != nil {
		resultErr = err
	}

	entries, err := os.ReadDir(workRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return resultErr
		}
		return errors.Join(resultErr, fmt.Errorf("read worktree cache root %q: %w", workRoot, err))
	}

	for _, entry := range entries {
		runID := strings.TrimSpace(entry.Name())
		if runID == "" {
			log.Printf("[WARN] skipping malformed cache-dir entry %q", filepath.Join(workRoot, entry.Name()))
			continue
		}
		runPath := filepath.Join(workRoot, runID)
		if !entry.IsDir() {
			log.Printf("[WARN] skipping malformed cache-dir entry %q", runPath)
			continue
		}

		active, activeErr := runIsActive(mainRoot, runID)
		if activeErr != nil {
			resultErr = errors.Join(resultErr, activeErr)
			continue
		}
		if active {
			continue
		}

		if err := os.RemoveAll(runPath); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("remove stale cache entry %q: %w", runPath, err))
			continue
		}

		log.Printf("[INFO] reconciled stale worktree runID=%s path=%s", runID, runPath)

		if err := runGitWorktreePrune(ctx, mainRoot); err != nil {
			resultErr = errors.Join(resultErr, err)
		}
	}

	return resultErr
}

func runIsActive(mainRoot, runID string) (bool, error) {
	runRoot := filepath.Join(mainRoot, ".verk", "runs", runID)
	statusActive, statusErr := runStatusIsActive(runRoot)
	claimsActive, claimsErr := runClaimsAreActive(runRoot, time.Now().UTC())

	activeErr := errors.Join(statusErr, claimsErr)
	if activeErr != nil {
		return false, activeErr
	}
	return statusActive || claimsActive, nil
}

func runStatusIsActive(runRoot string) (bool, error) {
	runJSON := filepath.Join(runRoot, "run.json")
	runPath := strings.TrimSpace(runJSON)
	raw, err := os.ReadFile(runPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read run manifest %q: %w", runPath, err)
	}

	var run state.RunArtifact
	if err := json.Unmarshal(raw, &run); err != nil {
		return false, fmt.Errorf("unmarshal run manifest %q: %w", runPath, err)
	}
	if _, ok := pendingWaveVerificationID(run.ResumeCursor); ok {
		return true, nil
	}

	switch run.Status {
	case state.EpicRunStatusRunning, state.EpicRunStatusWaitingOnLeases, state.EpicRunStatusBlocked:
		return true, nil
	default:
		return false, nil
	}
}

func runClaimsAreActive(runRoot string, now time.Time) (bool, error) {
	claimDir := filepath.Join(runRoot, "claims")
	entries, err := os.ReadDir(claimDir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read run claims %q: %w", claimDir, err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		raw, readErr := os.ReadFile(filepath.Join(claimDir, entry.Name()))
		if readErr != nil {
			return false, fmt.Errorf("read claim %q: %w", entry.Name(), readErr)
		}

		var claim state.ClaimArtifact
		if err := json.Unmarshal(raw, &claim); err != nil {
			return false, fmt.Errorf("unmarshal claim %q: %w", entry.Name(), err)
		}

		if isClaimActive(claim, now) {
			return true, nil
		}
	}
	return false, nil
}

func isClaimActive(claim state.ClaimArtifact, now time.Time) bool {
	if strings.EqualFold(claim.State, "released") {
		return false
	}
	if claim.OwnerRunID == "" {
		return false
	}
	if claim.ExpiresAt.IsZero() {
		return true
	}
	return now.Before(claim.ExpiresAt.UTC())
}

func runGitWorktreePrune(ctx context.Context, mainRoot string) error {
	cmd := exec.CommandContext(ctx, "git", "-C", mainRoot, "worktree", "prune")
	cmd.Env = engineGitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(bytes.TrimSpace(out)))
		if msg != "" {
			return fmt.Errorf("run git worktree prune: %w: %s", err, msg)
		}
		return fmt.Errorf("run git worktree prune: %w", err)
	}
	return nil
}

func engineGitEnv() []string {
	env := os.Environ()
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		key, _, found := strings.Cut(entry, "=")
		if !found {
			out = append(out, entry)
			continue
		}
		switch key {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_PREFIX", "GIT_SUPER_PREFIX", "GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL":
			continue
		default:
			out = append(out, entry)
		}
	}
	out = append(out,
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_AUTHOR_NAME=verk",
		"GIT_AUTHOR_EMAIL=verk@localhost",
		"GIT_COMMITTER_NAME=verk",
		"GIT_COMMITTER_EMAIL=verk@localhost",
	)
	return out
}

func repoWorktreeHash(repoRoot string) string {
	sum := sha256.Sum256([]byte(repoRoot))
	hash := hex.EncodeToString(sum[:])
	if len(hash) > worktreeHashLen {
		hash = hash[:worktreeHashLen]
	}
	return hash
}

func resolveWorktreeCacheRoot() (string, error) {
	cacheBase := strings.TrimSpace(os.Getenv("VERK_WORKTREE_ROOT"))
	if cacheBase == "" {
		cacheBase = strings.TrimSpace(os.Getenv("XDG_CACHE_HOME"))
		if cacheBase == "" {
			home := strings.TrimSpace(os.Getenv("HOME"))
			if home != "" {
				cacheBase = filepath.Join(home, ".cache", "verk", "worktrees")
			} else {
				cacheBase = filepath.Join(os.TempDir(), "verk", "worktrees")
			}
		} else {
			cacheBase = filepath.Join(cacheBase, "verk", "worktrees")
		}
	}
	cacheBase = strings.TrimSpace(cacheBase)
	if cacheBase == "" {
		cacheBase = filepath.Join(os.TempDir(), "verk", "worktrees")
	}

	cacheAbs, err := filepath.Abs(cacheBase)
	if err != nil {
		return "", fmt.Errorf("resolve worktree cache root %q: %w", cacheBase, err)
	}
	return cacheAbs, nil
}

func dedupeAndSortChanged(changed []string) []string {
	if len(changed) == 0 {
		return nil
	}
	sort.Strings(changed)
	out := make([]string, 0, len(changed))
	for _, file := range changed {
		if len(out) == 0 || out[len(out)-1] != file {
			out = append(out, file)
		}
	}
	return out
}

func isEngineOwned(path string) bool {
	segments := pathSegments(strings.TrimSpace(path))
	if len(segments) == 0 {
		return false
	}
	switch segments[0] {
	case ".verk", ".tickets", ".git":
		return true
	default:
		return false
	}
}

type MergeToMainPartialError struct {
	TicketID     string
	TouchedPaths []string
	Cause        error
}

func (e *MergeToMainPartialError) Error() string {
	if e == nil {
		return "merge to main partial error"
	}
	return fmt.Sprintf("merge-to-main partial write for ticket %q touched %d paths: %v", e.TicketID, len(e.TouchedPaths), e.Cause)
}

func (e *MergeToMainPartialError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (wm *WorktreeManager) MergeToMain(ticketID string) error {
	if wm == nil {
		return fmt.Errorf("worktree manager is nil")
	}
	ticketID = strings.TrimSpace(ticketID)
	if ticketID == "" {
		return fmt.Errorf("ticket id is required")
	}

	mainRoot := strings.TrimSpace(wm.mainRoot)
	if mainRoot == "" {
		return fmt.Errorf("main root is required")
	}
	worktreePath := wm.WorktreePath(ticketID)
	if worktreePath == "" {
		return fmt.Errorf("no worktree for ticket %q", ticketID)
	}

	rawStatus, err := gitStatusPorcelainV2(worktreePath)
	if err != nil {
		return fmt.Errorf("read git status for ticket %q in worktree %q: %w", ticketID, worktreePath, err)
	}
	changes, err := parsePorcelainToMergeChanges(rawStatus)
	if err != nil {
		return fmt.Errorf("parse git status for ticket %q in worktree %q: %w", ticketID, worktreePath, err)
	}
	changes = filterSyntheticWorktreeChanges(changes)

	changes, err = mergeToMainPreflight(worktreePath, mainRoot, changes)
	if err != nil {
		return fmt.Errorf("preflight merge-to-main for ticket %q: %w", ticketID, err)
	}

	sort.Slice(changes, func(i, j int) bool {
		return changes[i].destRel < changes[j].destRel
	})

	touched := make([]string, 0, len(changes))
	for _, change := range changes {
		touchedDuringChange := make([]string, 0, 2)
		if err := applyMergeToMainChange(worktreePath, mainRoot, change, &touchedDuringChange); err != nil {
			return &MergeToMainPartialError{
				TicketID:     ticketID,
				TouchedPaths: append(append([]string{}, touched...), touchedDuringChange...),
				Cause:        err,
			}
		}
		touched = append(touched, touchedDuringChange...)
	}
	return nil
}

const (
	mergeChangeAddOrModify = "add_or_modify"
	mergeChangeDelete      = "delete"
	mergeChangeUntracked   = "untracked"
	mergeChangeRename      = "rename"
	mergeChangeTypeChange  = "type_change"
	mergeChangeModeOnly    = "mode_only"
	mergeChangeSymlink     = "symlink"
)

type mergeToMainChange struct {
	srcRel      string
	destRel     string
	oldRel      string
	kind        string
	mode        fs.FileMode
	symlinkDest string
}

func parsePorcelainToMergeChanges(raw []byte) ([]mergeToMainChange, error) {
	if len(raw) == 0 {
		return nil, nil
	}

	records := bytes.Split(raw, []byte{0})
	changes := make([]mergeToMainChange, 0, len(records))

	for i := 0; i < len(records); i++ {
		record := records[i]
		if len(record) == 0 {
			continue
		}

		var line string
		if len(record) >= 2 && record[0] == '?' {
			line = string(record)
			pair := strings.SplitN(line, " ", 2)
			if len(pair) != 2 || strings.TrimSpace(pair[1]) == "" {
				return nil, fmt.Errorf("malformed untracked status entry %q", string(record))
			}
			path := filepath.Clean(pair[1])
			path = strings.TrimPrefix(path, "./")
			changes = append(changes, mergeToMainChange{
				srcRel:  path,
				destRel: path,
				kind:    mergeChangeUntracked,
			})
			continue
		}

		fields, err := splitPorcelainTokens(string(record), 2)
		if err != nil {
			return nil, fmt.Errorf("split status entry %q: %w", record, err)
		}
		switch fields[0] {
		case "1":
			fields, err = splitPorcelainTokens(string(record), 9)
			if err != nil {
				return nil, fmt.Errorf("parse entry %q: %w", string(record), err)
			}
			if len(fields[1]) != 2 {
				return nil, fmt.Errorf("invalid status %q in %q", fields[1], string(record))
			}
			status := fields[1]
			path := filepath.Clean(fields[8])
			path = strings.TrimPrefix(path, "./")
			modeAfter, err := parseOctalMode(fields[5], string(record))
			if err != nil {
				return nil, err
			}
			modeBefore, err := parseOctalMode(fields[3], string(record))
			if err != nil {
				return nil, err
			}
			kind := mergeChangeAddOrModify
			if status[0] == 'D' || status[1] == 'D' {
				kind = mergeChangeDelete
			} else if status[0] == 'T' || status[1] == 'T' {
				kind = mergeChangeTypeChange
			} else if isModeOnlyChange(status, fields[6], fields[7], modeBefore, modeAfter) {
				kind = mergeChangeModeOnly
			}
			if kind == mergeChangeAddOrModify && modeAfter == fs.ModeSymlink {
				kind = mergeChangeSymlink
			}
			changes = append(changes, mergeToMainChange{
				srcRel:  path,
				destRel: path,
				kind:    kind,
				mode:    modeAfter,
			})
		case "2":
			fields, err = splitPorcelainTokens(string(record), 10)
			if err != nil {
				return nil, fmt.Errorf("parse entry %q: %w", string(record), err)
			}
			if len(fields[1]) != 2 {
				return nil, fmt.Errorf("invalid status %q in %q", fields[1], string(record))
			}
			status := fields[1]
			if status[0] != 'R' && status[1] != 'R' {
				return nil, fmt.Errorf("unsupported status %q in %q", status, string(record))
			}
			if i+1 >= len(records) {
				return nil, fmt.Errorf("missing old path in rename entry %q", string(record))
			}
			newPath := filepath.Clean(fields[9])
			newPath = strings.TrimPrefix(newPath, "./")
			oldPath := filepath.Clean(string(records[i+1]))
			oldPath = strings.TrimPrefix(oldPath, "./")
			i++
			changes = append(changes, mergeToMainChange{
				srcRel:  newPath,
				destRel: newPath,
				oldRel:  oldPath,
				kind:    mergeChangeRename,
			})
		default:
			return nil, fmt.Errorf("unsupported status kind %q in %q", fields[0], string(record))
		}
	}
	return changes, nil
}

func hashOnlyModeChange(before, after string) bool {
	return before == after
}

func isModeOnlyChange(status, headHash, indexHash string, modeBefore, modeAfter fs.FileMode) bool {
	if modeBefore == modeAfter {
		return false
	}
	if len(status) != 2 {
		return false
	}
	if status[1] != '.' {
		return false
	}
	return hashOnlyModeChange(headHash, indexHash)
}

func splitPorcelainTokens(line string, want int) ([]string, error) {
	if want <= 0 {
		return nil, fmt.Errorf("invalid token request: %d", want)
	}
	out := make([]string, want)
	rest := strings.TrimLeft(line, " ")
	for i := 0; i < want-1; i++ {
		next := strings.IndexByte(rest, ' ')
		if next < 0 {
			return nil, fmt.Errorf("expected %d tokens in %q", want, line)
		}
		out[i] = rest[:next]
		rest = rest[next+1:]
		rest = strings.TrimLeft(rest, " ")
	}
	out[want-1] = rest
	if out[want-1] == "" {
		return nil, fmt.Errorf("missing final field in %q", line)
	}
	return out, nil
}

func parseOctalMode(raw, record string) (fs.FileMode, error) {
	if raw == "" {
		return 0, fmt.Errorf("empty mode for %q", record)
	}
	mode, err := strconv.ParseUint(raw, 8, 32)
	if err != nil {
		return 0, fmt.Errorf("parse mode %q in %q: %w", raw, record, err)
	}
	return fs.FileMode(mode), nil
}

func filterSyntheticWorktreeChanges(changes []mergeToMainChange) []mergeToMainChange {
	if len(changes) == 0 {
		return nil
	}
	out := make([]mergeToMainChange, 0, len(changes))
	for _, change := range changes {
		if isSyntheticWorktreeChange(change) {
			continue
		}
		out = append(out, change)
	}
	return out
}

func mergeChangePaths(changes []mergeToMainChange) []string {
	if len(changes) == 0 {
		return nil
	}
	paths := make([]string, 0, len(changes)*3)
	for _, change := range changes {
		if strings.TrimSpace(change.srcRel) != "" {
			paths = append(paths, change.srcRel)
		}
		if strings.TrimSpace(change.destRel) != "" {
			paths = append(paths, change.destRel)
		}
		if strings.TrimSpace(change.oldRel) != "" {
			paths = append(paths, change.oldRel)
		}
	}
	return dedupeAndSortChanged(paths)
}

func isSyntheticWorktreeChange(change mergeToMainChange) bool {
	return strings.TrimSpace(change.destRel) == ".verk"
}

func resolveRelativeMergePath(path string) (string, error) {
	clean := filepath.Clean(filepath.ToSlash(strings.TrimSpace(path)))
	clean = strings.TrimPrefix(clean, "./")
	clean = strings.TrimPrefix(clean, "/")
	for strings.HasPrefix(clean, "./") {
		clean = strings.TrimPrefix(clean, "./")
	}
	if clean == "" || clean == "." {
		return "", fmt.Errorf("invalid path %q", path)
	}
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
		return "", fmt.Errorf("path escapes repository: %q", path)
	}
	if isEngineOwned(clean) {
		return "", fmt.Errorf("path is engine-owned: %q", path)
	}
	return clean, nil
}

func resolveWithinRoot(root, rel string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", fmt.Errorf("root is required")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve root %q: %w", root, err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return "", fmt.Errorf("resolve symlinks in root %q: %w", absRoot, err)
	}
	absTarget := filepath.Join(absRoot, rel)
	// Resolve symlinks in the parent directory — the target itself may not
	// exist yet (new files during merge-to-main). Walk up until we find an
	// existing ancestor.
	parent := filepath.Dir(absTarget)
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", fmt.Errorf("resolve symlinks in parent of target %q: %w", absTarget, err)
	}
	resolvedTarget := filepath.Join(resolvedParent, filepath.Base(absTarget))
	relToRoot, err := filepath.Rel(resolvedRoot, resolvedTarget)
	if err != nil {
		return "", fmt.Errorf("resolve target %q in %q: %w", rel, root, err)
	}
	if relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("target %q is outside root %q", rel, root)
	}
	return resolvedTarget, nil
}

func mergeToMainPreflight(worktreeRoot, mainRoot string, changes []mergeToMainChange) ([]mergeToMainChange, error) {
	out := make([]mergeToMainChange, 0, len(changes))
	for _, change := range changes {
		normalized, err := normalizeMergeDestination(change)
		if err != nil {
			return nil, err
		}
		validated, err := preflightMergeChange(worktreeRoot, mainRoot, normalized)
		if err != nil {
			return nil, err
		}
		out = append(out, validated)
	}
	return out, nil
}

func normalizeMergeDestination(change mergeToMainChange) (mergeToMainChange, error) {
	destRel, err := resolveRelativeMergePath(change.destRel)
	if err != nil {
		return mergeToMainChange{}, fmt.Errorf("normalize destination %q: %w", change.destRel, err)
	}
	change.destRel = destRel
	return change, nil
}

func preflightMergeChange(worktreeRoot, mainRoot string, change mergeToMainChange) (mergeToMainChange, error) {
	switch change.kind {
	case mergeChangeDelete:
		return preflightDeleteChange(mainRoot, change)
	case mergeChangeRename:
		return preflightRenameChange(worktreeRoot, mainRoot, change)
	case mergeChangeTypeChange, mergeChangeAddOrModify, mergeChangeUntracked, mergeChangeSymlink:
		return preflightWriteLikeChange(worktreeRoot, mainRoot, change)
	case mergeChangeModeOnly:
		return preflightModeOnlyChange(worktreeRoot, mainRoot, change)
	default:
		return mergeToMainChange{}, fmt.Errorf("unsupported merge change kind %q for %q", change.kind, change.destRel)
	}
}

func preflightDeleteChange(mainRoot string, change mergeToMainChange) (mergeToMainChange, error) {
	srcRel, err := resolveRelativeMergePath(change.srcRel)
	if err != nil {
		return mergeToMainChange{}, fmt.Errorf("normalize deleted path %q: %w", change.srcRel, err)
	}
	change.srcRel = srcRel
	if _, err := resolveWithinRoot(mainRoot, change.destRel); err != nil {
		return mergeToMainChange{}, fmt.Errorf("resolve delete destination %q: %w", change.destRel, err)
	}
	if err := ensureWriteParent(mainRoot, change.destRel); err != nil {
		return mergeToMainChange{}, fmt.Errorf("preflight delete parent for %q: %w", change.destRel, err)
	}
	return change, nil
}

func preflightRenameChange(worktreeRoot, mainRoot string, change mergeToMainChange) (mergeToMainChange, error) {
	srcRel, err := resolveRelativeMergePath(change.srcRel)
	if err != nil {
		return mergeToMainChange{}, fmt.Errorf("normalize rename destination source %q: %w", change.srcRel, err)
	}
	oldRel, err := resolveRelativeMergePath(change.oldRel)
	if err != nil {
		return mergeToMainChange{}, fmt.Errorf("normalize rename source %q: %w", change.oldRel, err)
	}
	change.srcRel = srcRel
	change.oldRel = oldRel
	if _, err := resolveWithinRoot(mainRoot, change.oldRel); err != nil {
		return mergeToMainChange{}, fmt.Errorf("resolve rename old path %q: %w", change.oldRel, err)
	}
	if _, err := resolveWithinRoot(mainRoot, change.destRel); err != nil {
		return mergeToMainChange{}, fmt.Errorf("resolve rename destination %q: %w", change.destRel, err)
	}
	if err := ensureWriteParent(mainRoot, change.destRel); err != nil {
		return mergeToMainChange{}, fmt.Errorf("preflight rename destination parent for %q: %w", change.destRel, err)
	}
	if err := mergeToMainValidateSource(worktreeRoot, &change); err != nil {
		return mergeToMainChange{}, err
	}
	return change, nil
}

func preflightWriteLikeChange(worktreeRoot, mainRoot string, change mergeToMainChange) (mergeToMainChange, error) {
	srcRel, err := resolveRelativeMergePath(change.srcRel)
	if err != nil {
		return mergeToMainChange{}, fmt.Errorf("normalize source %q: %w", change.srcRel, err)
	}
	change.srcRel = srcRel
	if _, err := resolveWithinRoot(mainRoot, change.destRel); err != nil {
		return mergeToMainChange{}, fmt.Errorf("resolve destination %q: %w", change.destRel, err)
	}
	if err := ensureWriteParent(mainRoot, change.destRel); err != nil {
		return mergeToMainChange{}, fmt.Errorf("preflight parent for %q: %w", change.destRel, err)
	}
	if err := mergeToMainValidateSource(worktreeRoot, &change); err != nil {
		return mergeToMainChange{}, fmt.Errorf("validate source %q: %w", change.srcRel, err)
	}
	return change, nil
}

func preflightModeOnlyChange(worktreeRoot, mainRoot string, change mergeToMainChange) (mergeToMainChange, error) {
	srcRel, err := resolveRelativeMergePath(change.srcRel)
	if err != nil {
		return mergeToMainChange{}, fmt.Errorf("normalize mode-only source %q: %w", change.srcRel, err)
	}
	change.srcRel = srcRel
	if _, err := resolveWithinRoot(mainRoot, change.destRel); err != nil {
		return mergeToMainChange{}, fmt.Errorf("resolve destination %q: %w", change.destRel, err)
	}
	mainAbs, err := resolveWithinRoot(mainRoot, change.destRel)
	if err != nil {
		return mergeToMainChange{}, err
	}
	if _, err := os.Stat(mainAbs); err != nil {
		return mergeToMainChange{}, fmt.Errorf("missing destination %q for mode-only merge: %w", change.destRel, err)
	}
	if err := ensureWriteParent(mainRoot, change.destRel); err != nil {
		return mergeToMainChange{}, fmt.Errorf("preflight mode-only parent for %q: %w", change.destRel, err)
	}
	if err := mergeToMainValidateSource(worktreeRoot, &change); err != nil {
		return mergeToMainChange{}, fmt.Errorf("validate mode-only source %q: %w", change.srcRel, err)
	}
	return change, nil
}

func ensureWriteParent(mainRoot, rel string) error {
	dest, err := resolveWithinRoot(mainRoot, rel)
	if err != nil {
		return err
	}
	parent := filepath.Dir(dest)
	root, err := filepath.Abs(mainRoot)
	if err != nil {
		return err
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolve symlinks in root %q: %w", root, err)
	}
	current := parent
	for {
		if current == "" || current == "." {
			return fmt.Errorf("invalid destination path for %q", rel)
		}
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				current = filepath.Dir(current)
				continue
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink in write path %q is not allowed", current)
		}
		if !info.IsDir() {
			return fmt.Errorf("destination parent %q is not a directory", current)
		}
		resolved, err := filepath.EvalSymlinks(current)
		if err != nil {
			return fmt.Errorf("resolve parent %q: %w", current, err)
		}
		relToRoot, err := filepath.Rel(resolvedRoot, resolved)
		if err != nil || relToRoot == ".." || strings.HasPrefix(relToRoot, ".."+string(filepath.Separator)) {
			return fmt.Errorf("destination parent %q escapes root %q", current, root)
		}
		if current == root || resolved == resolvedRoot {
			return nil
		}
		return nil
	}
}

func mergeToMainValidateSource(worktreeRoot string, change *mergeToMainChange) error {
	if change == nil {
		return fmt.Errorf("nil change")
	}
	srcAbs, err := resolveWithinRoot(worktreeRoot, change.srcRel)
	if err != nil {
		return err
	}
	info, err := os.Lstat(srcAbs)
	if err != nil {
		return fmt.Errorf("stat %q: %w", change.srcRel, err)
	}
	if info.IsDir() {
		return fmt.Errorf("source %q is a directory", change.srcRel)
	}

	change.mode = info.Mode()
	if change.kind == mergeChangeModeOnly {
		return nil
	}
	if info.Mode()&fs.ModeType == fs.ModeSymlink {
		target, err := os.Readlink(srcAbs)
		if err != nil {
			return fmt.Errorf("read symlink target %q: %w", change.srcRel, err)
		}
		change.symlinkDest = target
		if change.kind == mergeChangeRename || change.kind == mergeChangeTypeChange {
			return nil
		}
		change.kind = mergeKindForSourceMode(info.Mode())
		return nil
	}
	if change.kind == mergeChangeRename || change.kind == mergeChangeTypeChange {
		return nil
	}
	return nil
}

func mergeKindForSourceMode(mode fs.FileMode) string {
	if mode&fs.ModeType == fs.ModeSymlink {
		return mergeChangeSymlink
	}
	return mergeChangeAddOrModify
}

func applyMergeToMainChange(worktreeRoot, mainRoot string, change mergeToMainChange, touched *[]string) error {
	mainAbs, err := resolveWithinRoot(mainRoot, change.destRel)
	if err != nil {
		return err
	}

	switch change.kind {
	case mergeChangeDelete:
		if err := os.Remove(mainAbs); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil && touched != nil {
			*touched = append(*touched, change.destRel)
		}
		pruneEmptyParentDirs(mainAbs, mainRoot)
		return nil
	case mergeChangeModeOnly:
		if err := os.Chmod(mainAbs, change.mode); err != nil {
			return err
		}
		if touched != nil {
			*touched = append(*touched, change.destRel)
		}
		return nil
	case mergeChangeRename:
		oldAbs, err := resolveWithinRoot(mainRoot, change.oldRel)
		if err != nil {
			return err
		}
		if err := os.Remove(oldAbs); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil && touched != nil {
			*touched = append(*touched, change.oldRel)
		}
		pruneEmptyParentDirs(oldAbs, mainRoot)
		return applyMergeToMainChange(worktreeRoot, mainRoot, mergeToMainChange{
			srcRel:      change.srcRel,
			destRel:     change.destRel,
			kind:        mergeKindForSourceMode(change.mode),
			mode:        change.mode,
			symlinkDest: change.symlinkDest,
		}, touched)
	case mergeChangeAddOrModify:
		return applyCopyWorktreeToMainFile(worktreeRoot, mainRoot, change.srcRel, change.destRel, touched)
	case mergeChangeSymlink:
		return applyWriteSymlink(worktreeRoot, mainRoot, change.srcRel, change.destRel, change.symlinkDest, touched)
	case mergeChangeTypeChange:
		if err := os.Remove(mainAbs); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil && touched != nil {
			*touched = append(*touched, change.destRel)
		}
		pruneEmptyParentDirs(mainAbs, mainRoot)
		return applyMergeToMainChange(worktreeRoot, mainRoot, mergeToMainChange{
			srcRel:      change.srcRel,
			destRel:     change.destRel,
			kind:        mergeKindForSourceMode(change.mode),
			mode:        change.mode,
			symlinkDest: change.symlinkDest,
		}, touched)
	case mergeChangeUntracked:
		return applyCopyWorktreeToMainFile(worktreeRoot, mainRoot, change.srcRel, change.destRel, touched)
	default:
		return fmt.Errorf("unsupported merge change kind %q", change.kind)
	}
}

func applyCopyWorktreeToMainFile(worktreeRoot, mainRoot, srcRel, dstRel string, touched *[]string) error {
	srcAbs, err := resolveWithinRoot(worktreeRoot, srcRel)
	if err != nil {
		return err
	}
	dstAbs, err := resolveWithinRoot(mainRoot, dstRel)
	if err != nil {
		return err
	}
	src, err := os.Open(srcAbs)
	if err != nil {
		return err
	}
	defer src.Close()

	info, err := src.Stat()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dstAbs), 0o755); err != nil {
		return err
	}
	dst, err := os.OpenFile(dstAbs, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		if touched != nil {
			*touched = append(*touched, dstRel)
		}
		return err
	}
	if err := dst.Close(); err != nil {
		if touched != nil {
			*touched = append(*touched, dstRel)
		}
		return err
	}
	if err := os.Chmod(dstAbs, info.Mode()); err != nil {
		if touched != nil {
			*touched = append(*touched, dstRel)
		}
		return err
	}
	if touched != nil {
		*touched = append(*touched, dstRel)
	}
	return nil
}

func applyWriteSymlink(worktreeRoot, mainRoot, srcRel, dstRel, target string, touched *[]string) error {
	dstAbs, err := resolveWithinRoot(mainRoot, dstRel)
	if err != nil {
		return err
	}
	touchedNow := false
	if err := os.Remove(dstAbs); err != nil && !os.IsNotExist(err) {
		return err
	} else if err == nil {
		touchedNow = true
	}
	if err := os.MkdirAll(filepath.Dir(dstAbs), 0o755); err != nil {
		return err
	}
	srcAbs, err := resolveWithinRoot(worktreeRoot, srcRel)
	if err != nil {
		return err
	}
	if target == "" {
		target, err = os.Readlink(srcAbs)
		if err != nil {
			return err
		}
	}
	if err := os.Symlink(target, dstAbs); err != nil {
		if touchedNow && touched != nil {
			*touched = append(*touched, dstRel)
		}
		return err
	}
	if touchedNow || touched == nil {
		return nil
	}
	if touched != nil {
		*touched = append(*touched, dstRel)
	}
	return nil
}

func pruneEmptyParentDirs(path, stopRoot string) {
	cur := filepath.Clean(filepath.Dir(path))
	stopRoot = filepath.Clean(stopRoot)
	for cur != "" && cur != "." {
		if cur == stopRoot {
			return
		}
		entries, err := os.ReadDir(cur)
		if err != nil || len(entries) != 0 {
			return
		}
		if err := os.Remove(cur); err != nil {
			return
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return
		}
		cur = parent
	}
}

func gitStatusPorcelainV2(worktreePath string) ([]byte, error) {
	cmd := exec.Command("git", "-C", worktreePath, "status", "--porcelain=v2", "-z", "-uall")
	cmd.Env = engineGitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(bytes.TrimSpace(out)))
		if msg != "" {
			return nil, fmt.Errorf("run git status --porcelain=v2: %w: %s", err, msg)
		}
		return nil, fmt.Errorf("run git status --porcelain=v2: %w", err)
	}
	return out, nil
}

func pathSegments(path string) []string {
	if path == "" {
		return nil
	}
	path = filepath.ToSlash(strings.TrimSpace(path))
	path = strings.TrimPrefix(path, "./")
	path = strings.TrimPrefix(path, "/")
	for strings.HasPrefix(path, "./") {
		path = strings.TrimPrefix(path, "./")
	}
	if path == "." || path == "" {
		return nil
	}
	return strings.Split(path, "/")
}

func filterEngineOwnedFilesInternal(changed []string) []string {
	if len(changed) == 0 {
		return nil
	}
	out := make([]string, 0, len(changed))
	for _, file := range changed {
		if isEngineOwned(file) {
			continue
		}
		out = append(out, file)
	}
	return out
}

// validateWorktreeBelongsToRepo checks that a worktree path belongs to the same
// git repository as repoRoot by comparing their main worktree roots.
// It skips validation when the worktree is the repo root itself or is a main
// worktree (.git is a directory), since those are inherently same-repo.
func validateWorktreeBelongsToRepo(repoRoot, worktreePath string) error {
	if repoRoot == worktreePath {
		return nil
	}
	// Only validate linked worktrees (.git is a file, not a directory).
	// If .git doesn't exist, path-level safety is already ensured by
	// resolveWithinRoot, so skip git-level validation entirely.
	gitInfo, err := os.Lstat(filepath.Join(worktreePath, ".git"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat worktree git dir: %w", err)
	}
	// Main worktree (regular .git directory) is always valid.
	if gitInfo.IsDir() {
		return nil
	}
	repo, err := git.New(repoRoot)
	if err != nil {
		return fmt.Errorf("open repo at %q: %w", repoRoot, err)
	}
	repoMain, err := repo.MainWorktreeRoot()
	if err != nil {
		return fmt.Errorf("resolve repo main root: %w", err)
	}
	wtRepo, err := git.New(worktreePath)
	if err != nil {
		return fmt.Errorf("open worktree at %q: %w", worktreePath, err)
	}
	wtMain, err := wtRepo.MainWorktreeRoot()
	if err != nil {
		return fmt.Errorf("resolve worktree main root: %w", err)
	}
	if repoMain != wtMain {
		return fmt.Errorf("worktree %q belongs to a different repository (main root %q vs %q)", worktreePath, wtMain, repoMain)
	}
	return nil
}
