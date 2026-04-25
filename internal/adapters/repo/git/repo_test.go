package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestRepoRootAndHeadCommit(t *testing.T) {
	repo, root, _ := newTestRepo(t)
	wantRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		wantRoot = root
	}

	gotRoot, err := repo.RepoRoot()
	if err != nil {
		t.Fatalf("RepoRoot: %v", err)
	}
	if gotRoot != wantRoot {
		t.Fatalf("RepoRoot = %q, want %q", gotRoot, wantRoot)
	}

	head, err := repo.HeadCommit()
	if err != nil {
		t.Fatalf("HeadCommit: %v", err)
	}
	if len(head) != 40 {
		t.Fatalf("HeadCommit length = %d, want 40", len(head))
	}
}

func TestRejectsRepoEscape(t *testing.T) {
	repo, _, _ := newTestRepo(t)

	if _, err := repo.NormalizeOwnedPath("../escape.txt"); err == nil {
		t.Fatal("NormalizeOwnedPath accepted path escaping repo root")
	}
}

func TestRespectsSegmentBoundaries(t *testing.T) {
	if PathsOverlap("src/api", "src/apix") {
		t.Fatal("expected no overlap for sibling segment prefixes")
	}
	if !PathsOverlap("src/api", "src/api/v1") {
		t.Fatal("expected overlap for directory prefix")
	}
	if !PathsOverlap("src/api", "src/api") {
		t.Fatal("expected overlap for identical paths")
	}
}

func TestChangedFilesAgainstBaseline(t *testing.T) {
	repo, root, baseCommit := newTestRepo(t)

	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("updated\n"), 0o644); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatalf("write untracked file: %v", err)
	}

	changed, err := repo.ChangedFilesAgainst(baseCommit)
	if err != nil {
		t.Fatalf("ChangedFilesAgainst: %v", err)
	}

	sort.Strings(changed)
	want := []string{"new.txt", "tracked.txt"}
	if !reflect.DeepEqual(changed, want) {
		t.Fatalf("ChangedFilesAgainst = %v, want %v", changed, want)
	}
}

func TestDiffAgainstIncludesUntrackedFiles(t *testing.T) {
	repo, root, baseCommit := newTestRepo(t)

	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatalf("write untracked file: %v", err)
	}

	diff, err := repo.DiffAgainst(baseCommit)
	if err != nil {
		t.Fatalf("DiffAgainst: %v", err)
	}
	if !strings.Contains(diff, "diff --git a/new.txt b/new.txt") {
		t.Fatalf("expected untracked file diff, got:\n%s", diff)
	}
	if !strings.Contains(diff, "+new") {
		t.Fatalf("expected untracked file content in diff, got:\n%s", diff)
	}
}

func TestDiffAgainstSkipsDisappearedUntrackedFiles(t *testing.T) {
	repo, root, _ := newTestRepo(t)

	path := filepath.Join(root, "gone.txt")
	if err := os.WriteFile(path, []byte("gone\n"), 0o644); err != nil {
		t.Fatalf("write untracked file: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove untracked file: %v", err)
	}

	diff, err := repo.diffUntrackedFiles([]string{"gone.txt"})
	if err != nil {
		t.Fatalf("diffUntrackedFiles: %v", err)
	}
	if diff != "" {
		t.Fatalf("expected disappeared untracked file to be skipped, got:\n%s", diff)
	}
}

func TestCreateWorktreeSetsHeadAndChangedFiles(t *testing.T) {
	repo, root, head := newTestRepo(t)
	worktreePath := filepath.Join(root, "worktree-one")

	if err := repo.CreateWorktree(context.Background(), head, worktreePath); err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}

	worktreeRepo, err := New(worktreePath)
	if err != nil {
		t.Fatalf("New(worktree): %v", err)
	}

	if got, err := worktreeRepo.HeadCommit(); err != nil {
		t.Fatalf("HeadCommit(worktree): %v", err)
	} else if got != head {
		t.Fatalf("HeadCommit(worktree) = %s, want %s", got, head)
	}

	worktreeFile := filepath.Join(worktreePath, "in-worktree.txt")
	if err := os.WriteFile(worktreeFile, []byte("in-worktree\n"), 0o644); err != nil {
		t.Fatalf("write worktree file: %v", err)
	}

	changed, err := worktreeRepo.ChangedFilesAgainst(head)
	if err != nil {
		t.Fatalf("ChangedFilesAgainst(worktree): %v", err)
	}
	if !reflect.DeepEqual(changed, []string{"in-worktree.txt"}) {
		t.Fatalf("ChangedFilesAgainst(worktree) = %v, want %v", changed, []string{"in-worktree.txt"})
	}
}

func TestCreateWorktreeTwiceReturnsErrWorktreeExists(t *testing.T) {
	repo, root, head := newTestRepo(t)
	worktreePath := filepath.Join(root, "worktree-two")

	if err := repo.CreateWorktree(context.Background(), head, worktreePath); err != nil {
		t.Fatalf("CreateWorktree first call: %v", err)
	}
	if err := repo.CreateWorktree(context.Background(), head, worktreePath); !errors.Is(err, ErrWorktreeExists) {
		t.Fatalf("CreateWorktree second call error = %v, want %v", err, ErrWorktreeExists)
	}
}

func TestCreateWorktreeCreatesMissingParentDirectory(t *testing.T) {
	repo, root, head := newTestRepo(t)
	worktreePath := filepath.Join(root, "missing", "parent", "worktree-three")

	if err := repo.CreateWorktree(context.Background(), head, worktreePath); err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}

	if info, err := os.Stat(filepath.Dir(worktreePath)); err != nil || !info.IsDir() {
		t.Fatalf("missing parent dir was not created: stat=%v, isDir=%v", err, info != nil && info.IsDir())
	}
}

func TestCreateWorktreeFromLinkedWorktreeUsesMainWorktreeRoot(t *testing.T) {
	repo, root, head := newTestRepo(t)
	linkedPath := filepath.Join(root, "linked-source")
	if err := repo.CreateWorktree(context.Background(), head, linkedPath); err != nil {
		t.Fatalf("CreateWorktree linked source: %v", err)
	}

	linkedRepo, err := New(linkedPath)
	if err != nil {
		t.Fatalf("New linked repo: %v", err)
	}
	if err := linkedRepo.CreateWorktree(context.Background(), head, "created-from-linked"); err != nil {
		t.Fatalf("CreateWorktree from linked repo: %v", err)
	}

	wantPath := filepath.Join(root, "created-from-linked")
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("expected worktree add to run from main worktree root %q: %v", wantPath, err)
	}
	wrongPath := filepath.Join(linkedPath, "created-from-linked")
	if _, err := os.Stat(wrongPath); err == nil {
		t.Fatalf("expected no worktree nested under linked root %q", wrongPath)
	}
}

func TestRemoveWorktreeHappyPath(t *testing.T) {
	repo, root, head := newTestRepo(t)
	worktreePath := filepath.Join(root, "worktree-four")

	if err := repo.CreateWorktree(context.Background(), head, worktreePath); err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	if err := repo.RemoveWorktree(worktreePath); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Fatalf("expected worktree path to be removed, got err=%v", err)
	}
	if hasWorktreeEntry(t, root, worktreePath) {
		t.Fatalf("worktree entry still present after RemoveWorktree")
	}
}

func TestRemoveWorktreeReconcilingMissingPath(t *testing.T) {
	repo, root, head := newTestRepo(t)
	worktreePath := filepath.Join(root, "worktree-five")

	if err := repo.CreateWorktree(context.Background(), head, worktreePath); err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	if err := os.RemoveAll(worktreePath); err != nil {
		t.Fatalf("remove worktree path: %v", err)
	}
	if err := repo.RemoveWorktree(worktreePath); err != nil {
		t.Fatalf("RemoveWorktree reconciliating missing path: %v", err)
	}

	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Fatalf("expected worktree path to be absent, got err=%v", err)
	}
	if hasWorktreeEntry(t, root, worktreePath) {
		t.Fatalf("worktree entry still present after reconcile")
	}
	if count := countWorktreeMetadataDirs(t, root); count != 0 {
		t.Fatalf("expected zero worktree metadata dirs after prune, got %d", count)
	}
}

func TestFailsOnDirtyRepo(t *testing.T) {
	repo, root, _ := newTestRepo(t)

	if err := repo.EnsureCleanWorktree(); err != nil {
		t.Fatalf("EnsureCleanWorktree clean repo: %v", err)
	}

	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatalf("write dirty tracked file: %v", err)
	}

	if err := repo.EnsureCleanWorktree(); err == nil {
		t.Fatal("EnsureCleanWorktree accepted dirty repo")
	}
}

func newTestRepo(t *testing.T) (*Repo, string, string) {
	t.Helper()

	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "test@example.com")
	runGit(t, root, "config", "user.name", "Test User")

	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("initial\n"), 0o644); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}
	runGit(t, root, "add", "tracked.txt")
	runGit(t, root, "commit", "-m", "initial commit")

	repo, err := New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	head, err := repo.HeadCommit()
	if err != nil {
		t.Fatalf("HeadCommit: %v", err)
	}
	return repo, root, head
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = testGitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
	}
}

func runGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = testGitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
	}
	return string(out)
}

func testGitEnv() []string {
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

func hasWorktreeEntry(t *testing.T, repoRoot, targetPath string) bool {
	t.Helper()

	list := runGitOutput(t, repoRoot, "worktree", "list", "--porcelain")
	targetPath = filepath.Clean(targetPath)
	for _, line := range strings.Split(list, "\n") {
		if !strings.HasPrefix(line, "worktree ") {
			continue
		}
		pathValue := strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
		if filepath.IsAbs(pathValue) {
			pathValue = filepath.Clean(pathValue)
		} else {
			pathValue = filepath.Clean(filepath.Join(repoRoot, pathValue))
		}
		if pathValue == targetPath {
			return true
		}
	}
	return false
}

func countWorktreeMetadataDirs(t *testing.T, repoRoot string) int {
	t.Helper()

	entries, err := os.ReadDir(filepath.Join(repoRoot, ".git", "worktrees"))
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("read worktree metadata: %v", err)
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() {
			count++
		}
	}
	return count
}
