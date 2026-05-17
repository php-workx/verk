package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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

var ErrWorktreeExists = errors.New("worktree already exists")

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
		root:         displayRoot,
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
		commonDir, err = gitOutput(r.root, "rev-parse", "--git-common-dir")
		if err != nil {
			return "", err
		}
	}
	commonDir = filepath.Clean(strings.TrimRight(commonDir, "\r\n"))
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(r.root, commonDir)
	}
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
	untracked, err := gitBytes(r.root, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return "", err
	}
	untrackedDiff, err := r.diffUntrackedFiles(splitNullList(untracked))
	if err != nil {
		return "", err
	}
	return out + untrackedDiff, nil
}

func (r *Repo) diffUntrackedFiles(paths []string) (string, error) {
	var buf bytes.Buffer
	for _, raw := range paths {
		normalized, err := normalizeRepoRelativePath(raw)
		if err != nil {
			return "", fmt.Errorf("normalize untracked file %q: %w", raw, err)
		}
		fullPath := filepath.Join(r.root, filepath.FromSlash(normalized))
		info, err := os.Lstat(fullPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return "", fmt.Errorf("stat untracked file %q: %w", normalized, err)
		}
		mode := "100644"
		var content []byte
		if info.Mode()&os.ModeSymlink != 0 {
			mode = "120000"
			target, readErr := os.Readlink(fullPath)
			if readErr != nil {
				if errors.Is(readErr, os.ErrNotExist) {
					continue
				}
				return "", fmt.Errorf("read untracked symlink %q: %w", normalized, readErr)
			}
			content = []byte(target)
		} else {
			if info.Mode()&0o111 != 0 {
				mode = "100755"
			}
			read, readErr := os.ReadFile(fullPath)
			if readErr != nil {
				if errors.Is(readErr, os.ErrNotExist) {
					continue
				}
				return "", fmt.Errorf("read untracked file %q: %w", normalized, readErr)
			}
			content = read
		}
		if buf.Len() > 0 && !bytes.HasSuffix(buf.Bytes(), []byte("\n")) {
			buf.WriteByte('\n')
		}
		writeNewFileDiff(&buf, normalized, mode, content)
	}
	return buf.String(), nil
}

func writeNewFileDiff(buf *bytes.Buffer, relPath, mode string, content []byte) {
	fmt.Fprintf(buf, "diff --git a/%s b/%s\n", relPath, relPath)
	fmt.Fprintf(buf, "new file mode %s\n", mode)
	fmt.Fprintf(buf, "index 0000000..0000000\n")
	fmt.Fprintf(buf, "--- /dev/null\n")
	fmt.Fprintf(buf, "+++ b/%s\n", relPath)
	if len(content) == 0 {
		return
	}
	if mode != "120000" && bytes.IndexByte(content, 0) >= 0 {
		fmt.Fprintf(buf, "Binary files /dev/null and b/%s differ\n", relPath)
		return
	}
	hasNoTrailingNewline := !bytes.HasSuffix(content, []byte("\n"))
	lineCount := bytes.Count(content, []byte("\n"))
	if hasNoTrailingNewline {
		lineCount++
	}
	fmt.Fprintf(buf, "@@ -0,0 +1,%d @@\n", lineCount)
	lines := bytes.SplitAfter(content, []byte("\n"))
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		buf.WriteByte('+')
		buf.Write(line)
		if !bytes.HasSuffix(line, []byte("\n")) {
			buf.WriteByte('\n')
		}
	}
	if hasNoTrailingNewline {
		fmt.Fprintf(buf, "\\ No newline at end of file\n")
	}
}

// syntheticDiffSizeLimit is the maximum number of bytes read from an untracked
// file before the output is truncated.
const syntheticDiffSizeLimit = 256 * 1024

// syntheticDiffLineLimit is the maximum number of lines emitted for an untracked
// file before the output is truncated.
const syntheticDiffLineLimit = 4000

// DiffAgainstFiles returns a unified diff of the given files against baseCommit.
// Tracked changes are produced by git diff; untracked selected files get a
// synthetic new-file diff so callers see their content regardless of staging
// status.
func (r *Repo) DiffAgainstFiles(baseCommit string, files []string) (string, error) {
	if r == nil {
		return "", fmt.Errorf("nil repo")
	}
	baseCommit = strings.TrimSpace(baseCommit)
	if baseCommit == "" {
		return "", fmt.Errorf("base commit is required")
	}
	if err := r.verifyCommit(baseCommit); err != nil {
		return "", err
	}

	normalized, err := r.normalizeDiffPaths(files)
	if err != nil {
		return "", err
	}
	if len(normalized) == 0 {
		return "", nil
	}

	args := append([]string{"diff", baseCommit, "--"}, normalized...)
	trackedDiff, err := gitOutput(r.root, args...)
	if err != nil {
		return "", err
	}

	untrackedDiff, err := r.syntheticUntrackedDiff(normalized)
	if err != nil {
		return "", err
	}

	return joinDiffParts(trackedDiff, untrackedDiff), nil
}

// normalizeDiffPaths normalizes and deduplicates file paths for use in
// DiffAgainstFiles. It filters empty paths, rejects NUL-containing paths, and
// rejects paths that escape the repo root.
func (r *Repo) normalizeDiffPaths(files []string) ([]string, error) {
	seen := make(map[string]struct{}, len(files))
	out := make([]string, 0, len(files))
	for _, f := range files {
		if f == "" {
			continue
		}
		if strings.Contains(f, "\x00") {
			return nil, fmt.Errorf("diff path contains NUL: %q", f)
		}
		normalized := normalizeComparablePath(f)
		if normalized == ".." || strings.HasPrefix(normalized, "../") {
			return nil, fmt.Errorf("diff path escapes repo root: %s", f)
		}
		if _, already := seen[normalized]; already {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	sort.Strings(out)
	return out, nil
}

// syntheticUntrackedDiff builds new-file diffs for any paths in the given list
// that are untracked in the working tree. Tracked files are silently skipped
// (their diff comes from git diff). Binary files get a placeholder marker.
// Large files are truncated at syntheticDiffSizeLimit / syntheticDiffLineLimit.
func (r *Repo) syntheticUntrackedDiff(paths []string) (string, error) {
	// Collect the set of untracked files known to git so we can skip tracked ones.
	untrackedRaw, err := gitBytes(r.root, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return "", err
	}
	untrackedSet := make(map[string]struct{})
	for _, raw := range splitNullList(untrackedRaw) {
		n, normErr := normalizeRepoRelativePath(raw)
		if normErr != nil {
			continue
		}
		untrackedSet[n] = struct{}{}
	}

	var buf bytes.Buffer
	for _, p := range paths {
		if _, ok := untrackedSet[p]; !ok {
			continue
		}
		fullPath := filepath.Join(r.root, filepath.FromSlash(p))
		info, err := os.Lstat(fullPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return "", fmt.Errorf("stat untracked file %q: %w", p, err)
		}

		mode := "100644"
		var content []byte
		if info.Mode()&os.ModeSymlink != 0 {
			mode = "120000"
			target, readErr := os.Readlink(fullPath)
			if readErr != nil {
				if errors.Is(readErr, os.ErrNotExist) {
					continue
				}
				return "", fmt.Errorf("read untracked symlink %q: %w", p, readErr)
			}
			content = []byte(target)
		} else {
			if info.Mode()&0o111 != 0 {
				mode = "100755"
			}
			f, openErr := os.Open(fullPath)
			if openErr != nil {
				if errors.Is(openErr, os.ErrNotExist) {
					continue
				}
				return "", fmt.Errorf("open untracked file %q: %w", p, openErr)
			}
			read, readErr := io.ReadAll(io.LimitReader(f, syntheticDiffSizeLimit+1))
			_ = f.Close()
			if readErr != nil {
				if errors.Is(readErr, os.ErrNotExist) {
					continue
				}
				return "", fmt.Errorf("read untracked file %q: %w", p, readErr)
			}
			content = read
		}

		if buf.Len() > 0 && !bytes.HasSuffix(buf.Bytes(), []byte("\n")) {
			buf.WriteByte('\n')
		}
		writeNewFileDiffWithLimits(&buf, p, mode, content)
	}
	return buf.String(), nil
}

// writeNewFileDiffWithLimits is like writeNewFileDiff but applies size and line
// limits to the content, and emits a truncation marker when exceeded.
func writeNewFileDiffWithLimits(buf *bytes.Buffer, relPath, mode string, content []byte) {
	fmt.Fprintf(buf, "diff --git a/%s b/%s\n", relPath, relPath)
	fmt.Fprintf(buf, "new file mode %s\n", mode)
	fmt.Fprintf(buf, "index 0000000..0000000\n")
	fmt.Fprintf(buf, "--- /dev/null\n")
	fmt.Fprintf(buf, "+++ b/%s\n", relPath)
	if len(content) == 0 {
		return
	}
	if mode != "120000" && bytes.IndexByte(content, 0) >= 0 {
		fmt.Fprintf(buf, "Binary files /dev/null and b/%s differ\n", relPath)
		return
	}

	originalNoTrailingNewline := !bytes.HasSuffix(content, []byte("\n"))

	truncated := false
	if len(content) > syntheticDiffSizeLimit {
		content = content[:syntheticDiffSizeLimit]
		truncated = true
	}

	lineCount := bytes.Count(content, []byte("\n"))
	hasNoTrailingNewline := !bytes.HasSuffix(content, []byte("\n"))
	if hasNoTrailingNewline {
		lineCount++
	}
	if lineCount > syntheticDiffLineLimit {
		lineCount = syntheticDiffLineLimit
	}

	fmt.Fprintf(buf, "@@ -0,0 +1,%d @@\n", lineCount)
	lines := bytes.SplitAfter(content, []byte("\n"))
	emitted := 0
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		if emitted >= syntheticDiffLineLimit {
			truncated = true
			break
		}
		buf.WriteByte('+')
		buf.Write(line)
		if !bytes.HasSuffix(line, []byte("\n")) {
			buf.WriteByte('\n')
		}
		emitted++
	}
	if truncated {
		fmt.Fprintf(buf, "\\ [diff truncated: file too large]\n")
	} else if originalNoTrailingNewline {
		fmt.Fprintf(buf, "\\ No newline at end of file\n")
	}
}

// joinDiffParts joins two diff strings, ensuring exactly one newline between
// them when both are non-empty.
func joinDiffParts(a, b string) string {
	if b == "" {
		return a
	}
	if a == "" {
		return b
	}
	if strings.HasSuffix(a, "\n") {
		return a + b
	}
	return a + "\n" + b
}

func (r *Repo) NormalizeOwnedPath(candidate string) (string, error) {
	if r == nil {
		return "", fmt.Errorf("nil repo")
	}
	return normalizeOwnedPath(r.root, r.physicalRoot, candidate)
}

// CreateWorktree creates a linked worktree at targetPath at commitish.
//
// git worktree add is O(100ms–seconds on large repositories, and callers should
// create worktrees serially (not in parallel) to avoid `.git/worktrees/` lock
// contention.
func (r *Repo) CreateWorktree(ctx context.Context, commitish, targetPath string) error {
	if r == nil {
		return fmt.Errorf("nil repo")
	}
	if strings.TrimSpace(commitish) == "" {
		return fmt.Errorf("commitish is required")
	}
	if strings.TrimSpace(targetPath) == "" {
		return fmt.Errorf("target path is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	worktreeRoot, err := r.MainWorktreeRoot()
	if err != nil {
		return fmt.Errorf("resolve main worktree root: %w", err)
	}
	targetPath, err = canonicalPath(worktreeRoot, targetPath)
	if err != nil {
		return fmt.Errorf("normalize target path %q: %w", targetPath, err)
	}

	already, err := hasWorktreePath(worktreeRoot, targetPath)
	if err != nil {
		return err
	}
	if already {
		return ErrWorktreeExists
	}

	targetParent := filepath.Dir(targetPath)
	if targetParent != "." && targetParent != "" {
		if err := os.MkdirAll(targetParent, 0o755); err != nil {
			return fmt.Errorf("create worktree parent directory %q: %w", targetParent, err)
		}
	}

	if _, err := gitBytesContext(ctx, worktreeRoot, "worktree", "add", "--detach", targetPath, commitish); err != nil {
		return fmt.Errorf("create worktree %q for commit %q: %w", targetPath, commitish, err)
	}
	return nil
}

func (r *Repo) RemoveWorktree(targetPath string) error {
	if r == nil {
		return fmt.Errorf("nil repo")
	}
	if strings.TrimSpace(targetPath) == "" {
		return fmt.Errorf("target path is required")
	}

	worktreeRoot, err := r.MainWorktreeRoot()
	if err != nil {
		return fmt.Errorf("resolve main worktree root: %w", err)
	}
	targetPath, err = canonicalPath(worktreeRoot, targetPath)
	if err != nil {
		return fmt.Errorf("normalize target path %q: %w", targetPath, err)
	}

	if _, err := gitBytes(worktreeRoot, "worktree", "remove", "--force", targetPath); err == nil {
		return nil
	} else if !isWorktreeMissingFromGit(err) && !isPathMissing(targetPath) {
		return err
	}

	registered, checkErr := hasWorktreePath(worktreeRoot, targetPath)
	if checkErr != nil {
		return fmt.Errorf("verify worktree registration for %q: %w", targetPath, checkErr)
	}
	if !registered && !isPathMissing(targetPath) {
		return fmt.Errorf("refusing to remove unregistered worktree path %q", targetPath)
	}

	if err := os.RemoveAll(targetPath); err != nil {
		return fmt.Errorf("remove worktree path %q: %w", targetPath, err)
	}
	if _, err := gitBytes(worktreeRoot, "worktree", "prune"); err != nil {
		return fmt.Errorf("prune worktrees: %w", err)
	}
	return nil
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

func hasWorktreePath(mainRoot, target string) (bool, error) {
	targetPath, err := canonicalPath(mainRoot, target)
	if err != nil {
		return false, err
	}
	targetPath = normalizeComparablePathWithFallbackResolve(targetPath)

	out, err := gitBytes(mainRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return false, err
	}

	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, "worktree ") {
			continue
		}
		raw := strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
		if raw == "" {
			continue
		}
		pathValue, err := canonicalPath(mainRoot, raw)
		if err != nil {
			return false, err
		}
		pathValue = normalizeComparablePathWithFallbackResolve(pathValue)
		if pathValue == targetPath {
			info, err := os.Stat(pathValue)
			if err != nil {
				if os.IsNotExist(err) {
					return false, nil
				}
				return false, fmt.Errorf("check worktree path %q: %w", pathValue, err)
			}
			return info.IsDir(), nil
		}
	}
	return false, nil
}

func canonicalPath(mainRoot, target string) (string, error) {
	if mainRoot == "" {
		return "", fmt.Errorf("main root is required")
	}
	if target == "" {
		return "", fmt.Errorf("target is required")
	}

	resolved, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(target) {
		resolved = filepath.Join(mainRoot, target)
	}

	resolved = filepath.Clean(resolved)
	if normalized, err := resolvePath(resolved); err == nil {
		return normalized, nil
	}

	return resolved, nil
}

func normalizeComparablePathWithFallbackResolve(candidate string) string {
	candidate = filepath.Clean(candidate)
	if resolved, err := resolvePath(candidate); err == nil {
		return filepath.Clean(resolved)
	}
	return candidate
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

func gitBytesContext(ctx context.Context, cwd string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = cwd
	cmd.Env = gitCommandEnv()
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

func isWorktreeMissingFromGit(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not a working tree")
}

func isPathMissing(candidate string) bool {
	_, err := os.Stat(candidate)
	return err != nil && os.IsNotExist(err)
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
	cmd.Env = gitCommandEnv()
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

func gitCommandEnv() []string {
	env := os.Environ()
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		key, _, found := strings.Cut(entry, "=")
		if !found {
			out = append(out, entry)
			continue
		}
		switch key {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_PREFIX", "GIT_SUPER_PREFIX":
			continue
		default:
			out = append(out, entry)
		}
	}
	out = append(out, "GIT_OPTIONAL_LOCKS=0")
	return out
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
