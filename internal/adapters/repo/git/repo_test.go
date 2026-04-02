package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
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
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
	}
}
