package git

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

type Repo struct {
	root         string
	physicalRoot string
}

func New(worktree string) (*Repo, error) {
	if worktree == "" {
		worktree = "."
	}

	absWorktree, err := filepath.Abs(worktree)
	if err != nil {
		return nil, fmt.Errorf("resolve worktree: %w", err)
	}

	displayRoot, err := gitOutput(absWorktree, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	displayRoot = filepath.Clean(strings.TrimRight(displayRoot, "\r\n"))

	physicalRoot := displayRoot
	if resolved, err := resolvePath(displayRoot); err == nil {
		physicalRoot = resolved
	}

	return &Repo{
		root:         physicalRoot,
		physicalRoot: physicalRoot,
	}, nil
}

func RepoRoot() (string, error) {
	repo, err := New(".")
	if err != nil {
		return "", err
	}
	return repo.RepoRoot()
}

func HeadCommit() (string, error) {
	repo, err := New(".")
	if err != nil {
		return "", err
	}
	return repo.HeadCommit()
}

func CurrentBranch() (string, error) {
	repo, err := New(".")
	if err != nil {
		return "", err
	}
	return repo.CurrentBranch()
}

func ChangedFilesAgainst(baseCommit string) ([]string, error) {
	repo, err := New(".")
	if err != nil {
		return nil, err
	}
	return repo.ChangedFilesAgainst(baseCommit)
}

func NormalizeOwnedPath(repoRoot, candidate string) (string, error) {
	physicalRoot := repoRoot
	if resolved, err := resolvePath(repoRoot); err == nil {
		physicalRoot = resolved
	}
	return normalizeOwnedPath(repoRoot, physicalRoot, candidate)
}

func EnsureCleanWorktree() error {
	repo, err := New(".")
	if err != nil {
		return err
	}
	return repo.EnsureCleanWorktree()
}

func (r *Repo) RepoRoot() (string, error) {
	if r == nil {
		return "", fmt.Errorf("nil repo")
	}
	return r.root, nil
}

// MainWorktreeRoot returns the root of the main (primary) worktree.
// For regular repos this is the same as RepoRoot. For git worktrees,
// this returns the parent of .git (the main checkout), which is where
// shared untracked directories like .tickets/ live.
func (r *Repo) MainWorktreeRoot() (string, error) {
	if r == nil {
		return "", fmt.Errorf("nil repo")
	}
	commonDir, err := gitOutput(r.root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		// Fallback: if git doesn't support --path-format, use the current root
		return r.root, nil
	}
	commonDir = filepath.Clean(strings.TrimRight(commonDir, "\r\n"))
	// The main worktree root is the parent of the .git directory
	mainRoot := filepath.Dir(commonDir)
	if resolved, err := resolvePath(mainRoot); err == nil {
		return resolved, nil
	}
	return mainRoot, nil
}

func (r *Repo) HeadCommit() (string, error) {
	if r == nil {
		return "", fmt.Errorf("nil repo")
	}

	out, err := gitOutput(r.root, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (r *Repo) CurrentBranch() (string, error) {
	if r == nil {
		return "", fmt.Errorf("nil repo")
	}
	out, err := gitOutput(r.root, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (r *Repo) ChangedFilesAgainst(baseCommit string) ([]string, error) {
	if r == nil {
		return nil, fmt.Errorf("nil repo")
	}
	if strings.TrimSpace(baseCommit) == "" {
		return nil, fmt.Errorf("base commit is required")
	}
	if err := r.verifyCommit(baseCommit); err != nil {
		return nil, err
	}

	changed := map[string]struct{}{}

	tracked, err := gitBytes(r.root, "diff", "--name-only", "-z", baseCommit, "--")
	if err != nil {
		return nil, err
	}
	for _, raw := range splitNullList(tracked) {
		normalized, err := normalizeRepoRelativePath(raw)
		if err != nil {
			return nil, fmt.Errorf("normalize changed file %q: %w", raw, err)
		}
		changed[normalized] = struct{}{}
	}

	untracked, err := gitBytes(r.root, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	for _, raw := range splitNullList(untracked) {
		normalized, err := normalizeRepoRelativePath(raw)
		if err != nil {
			return nil, fmt.Errorf("normalize untracked file %q: %w", raw, err)
		}
		changed[normalized] = struct{}{}
	}

	out := make([]string, 0, len(changed))
	for p := range changed {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

func (r *Repo) DiffAgainst(baseCommit string) (string, error) {
	if r == nil {
		return "", fmt.Errorf("nil repo")
	}
	if strings.TrimSpace(baseCommit) == "" {
		return "", fmt.Errorf("base commit is required")
	}
	if err := r.verifyCommit(baseCommit); err != nil {
		return "", err
	}

	out, err := gitOutput(r.root, "diff", baseCommit, "--")
	if err != nil {
		return "", err
	}
	return out, nil
}

func (r *Repo) NormalizeOwnedPath(candidate string) (string, error) {
	if r == nil {
		return "", fmt.Errorf("nil repo")
	}
	return normalizeOwnedPath(r.root, r.physicalRoot, candidate)
}

func (r *Repo) PathsOverlap(a, b string) bool {
	return PathsOverlap(a, b)
}

func (r *Repo) EnsureCleanWorktree() error {
	if r == nil {
		return fmt.Errorf("nil repo")
	}

	out, err := gitOutput(r.root, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return err
	}
	trimmed := strings.TrimSpace(out)
	if trimmed != "" {
		lines := strings.Split(trimmed, "\n")
		if len(lines) > 3 {
			lines = lines[:3]
		}
		return fmt.Errorf("dirty worktree:\n%s", strings.Join(lines, "\n"))
	}
	return nil
}

func PathsOverlap(a, b string) bool {
	na := normalizeComparablePath(a)
	nb := normalizeComparablePath(b)

	if na == "." || nb == "." {
		return true
	}
	if na == nb {
		return true
	}
	return hasPathPrefix(na, nb) || hasPathPrefix(nb, na)
}

func normalizeOwnedPath(displayRoot, physicalRoot, candidate string) (string, error) {
	if candidate == "" {
		return "", fmt.Errorf("owned path is required")
	}
	if strings.Contains(candidate, "\x00") {
		return "", fmt.Errorf("owned path contains NUL")
	}
	if looksLikeGlob(candidate) {
		return "", fmt.Errorf("glob patterns are not allowed in owned paths")
	}

	rootAbs := displayRoot
	if !filepath.IsAbs(rootAbs) {
		rootAbs = filepath.Clean(rootAbs)
	}

	candidateAbs := candidate
	if !filepath.IsAbs(candidateAbs) {
		candidateAbs = filepath.Join(rootAbs, candidateAbs)
	}
	candidateAbs = filepath.Clean(candidateAbs)

	displayRel, err := filepath.Rel(rootAbs, candidateAbs)
	if err != nil {
		return "", fmt.Errorf("relativize owned path: %w", err)
	}
	displayRel = filepath.ToSlash(filepath.Clean(displayRel))
	if displayRel == ".." || strings.HasPrefix(displayRel, "../") {
		return "", fmt.Errorf("owned path escapes repo root: %s", candidate)
	}

	resolvedRoot := physicalRoot
	if resolvedRoot == "" {
		resolvedRoot = rootAbs
	}
	resolvedCandidate, resolvedErr := resolvePath(candidateAbs)
	if resolvedErr == nil && !pathWithinRoot(resolvedRoot, resolvedCandidate) {
		return "", fmt.Errorf("owned path escapes repo root: %s", candidate)
	}
	if resolvedErr != nil && !pathWithinRoot(rootAbs, candidateAbs) {
		return "", fmt.Errorf("owned path escapes repo root: %s", candidate)
	}

	return normalizeComparablePath(displayRel), nil
}

func normalizeRepoRelativePath(candidate string) (string, error) {
	if candidate == "" {
		return "", fmt.Errorf("empty path")
	}
	normalized := normalizeComparablePath(candidate)
	if normalized == ".." || strings.HasPrefix(normalized, "../") {
		return "", fmt.Errorf("path escapes repo root: %s", candidate)
	}
	return normalized, nil
}

func normalizeComparablePath(candidate string) string {
	if candidate == "" {
		return "."
	}
	candidate = filepath.ToSlash(candidate)
	candidate = path.Clean(candidate)
	candidate = strings.TrimSuffix(candidate, "/")
	if candidate == "" {
		return "."
	}
	return candidate
}

func hasPathPrefix(pathValue, prefix string) bool {
	if prefix == "." {
		return true
	}
	if pathValue == prefix {
		return true
	}
	return strings.HasPrefix(pathValue, prefix+"/")
}

func pathWithinRoot(root, candidate string) bool {
	root = filepath.Clean(root)
	candidate = filepath.Clean(candidate)
	if root == candidate {
		return true
	}
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	return rel != ".." && !strings.HasPrefix(rel, "../")
}

func resolvePath(p string) (string, error) {
	abs := p
	if !filepath.IsAbs(abs) {
		var err error
		abs, err = filepath.Abs(abs)
		if err != nil {
			return "", err
		}
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return filepath.Clean(resolved), nil
	}

	dir := filepath.Dir(abs)
	dirResolved, dirErr := filepath.EvalSymlinks(dir)
	if dirErr != nil {
		return filepath.Clean(abs), err
	}
	return filepath.Clean(filepath.Join(dirResolved, filepath.Base(abs))), nil
}

func splitNullList(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	parts := bytes.Split(data, []byte{0})
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) == 0 {
			continue
		}
		out = append(out, string(part))
	}
	return out
}

func gitOutput(cwd string, args ...string) (string, error) {
	out, err := gitBytes(cwd, args...)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func gitBytes(cwd string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, msg)
		}
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

func (r *Repo) verifyCommit(baseCommit string) error {
	_, err := gitOutput(r.root, "rev-parse", "--verify", baseCommit+"^{commit}")
	if err != nil {
		return fmt.Errorf("verify base commit %q: %w", baseCommit, err)
	}
	return nil
}

func looksLikeGlob(candidate string) bool {
	return strings.ContainsAny(candidate, "*?[]")
}
