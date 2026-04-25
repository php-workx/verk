package engine

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
	tkmd "verk/internal/adapters/ticketstore/tkmd"
)

func TestResolveWorktreeRoot_PrefersExplicitRoot(t *testing.T) {
	t.Setenv("VERK_WORKTREE_ROOT", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("HOME", "")

	cacheRoot := t.TempDir()
	t.Setenv("VERK_WORKTREE_ROOT", cacheRoot)
	if err := os.RemoveAll(cacheRoot); err != nil {
		t.Fatalf("remove existing cache root: %v", err)
	}

	mainRoot := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(mainRoot, 0o755); err != nil {
		t.Fatalf("prepare main root: %v", err)
	}

	resolved, err := ResolveWorktreeRoot(mainRoot)
	if err != nil {
		t.Fatalf("ResolveWorktreeRoot: %v", err)
	}

	if resolved != filepath.Join(cacheRoot, repoWorktreeHash(mainRoot)) {
		t.Fatalf("unexpected explicit root: %q", resolved)
	}
	if !filepath.IsAbs(resolved) {
		t.Fatalf("expected absolute path, got %q", resolved)
	}
	assertDirCreated(t, resolved)
}

func TestResolveWorktreeRoot_UsesXDGCacheFallback(t *testing.T) {
	t.Setenv("VERK_WORKTREE_ROOT", "")
	t.Setenv("HOME", "")

	xdg := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", xdg)

	mainRoot := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(mainRoot, 0o755); err != nil {
		t.Fatalf("prepare main root: %v", err)
	}

	resolved, err := ResolveWorktreeRoot(mainRoot)
	if err != nil {
		t.Fatalf("ResolveWorktreeRoot: %v", err)
	}
	want := filepath.Join(xdg, "verk", "worktrees", repoWorktreeHash(mainRoot))
	if resolved != want {
		t.Fatalf("expected xdg cache root %q, got %q", want, resolved)
	}
	assertDirCreated(t, resolved)
}

func TestResolveWorktreeRoot_UsesHomeFallback(t *testing.T) {
	t.Setenv("VERK_WORKTREE_ROOT", "")
	t.Setenv("XDG_CACHE_HOME", "")

	home := t.TempDir()
	t.Setenv("HOME", home)

	mainRoot := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(mainRoot, 0o755); err != nil {
		t.Fatalf("prepare main root: %v", err)
	}

	resolved, err := ResolveWorktreeRoot(mainRoot)
	if err != nil {
		t.Fatalf("ResolveWorktreeRoot: %v", err)
	}
	want := filepath.Join(home, ".cache", "verk", "worktrees", repoWorktreeHash(mainRoot))
	if resolved != want {
		t.Fatalf("expected home cache root %q, got %q", want, resolved)
	}
	assertDirCreated(t, resolved)
}

func TestResolveWorktreeRoot_UsesTempFallback(t *testing.T) {
	t.Setenv("VERK_WORKTREE_ROOT", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("HOME", "")

	mainRoot := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(mainRoot, 0o755); err != nil {
		t.Fatalf("prepare main root: %v", err)
	}

	resolved, err := ResolveWorktreeRoot(mainRoot)
	if err != nil {
		t.Fatalf("ResolveWorktreeRoot: %v", err)
	}
	wantPrefix := filepath.Join(os.TempDir(), "verk", "worktrees")
	if !strings.HasPrefix(resolved, wantPrefix) {
		t.Fatalf("expected temp fallback prefix %q in %q", wantPrefix, resolved)
	}
	assertDirCreated(t, resolved)
}

func assertDirCreated(t *testing.T, path string) {
	t.Helper()
	info, statErr := os.Stat(path)
	if statErr != nil {
		t.Fatalf("expected created root %q: %v", path, statErr)
	}
	if !info.IsDir() {
		t.Fatalf("expected directory root %q, got non-directory", path)
	}
}

func TestResolveWorktreeRoot_RepoHashIsStableAndCollisionResistant(t *testing.T) {
	t.Setenv("VERK_WORKTREE_ROOT", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("HOME", "")

	cacheRoot := t.TempDir()
	t.Setenv("VERK_WORKTREE_ROOT", cacheRoot)

	repoOne := filepath.Join(t.TempDir(), "repo-one")
	repoTwo := filepath.Join(t.TempDir(), "repo-two")
	if err := os.MkdirAll(repoOne, 0o755); err != nil {
		t.Fatalf("prepare repo one: %v", err)
	}
	if err := os.MkdirAll(repoTwo, 0o755); err != nil {
		t.Fatalf("prepare repo two: %v", err)
	}

	rootOneA, err := ResolveWorktreeRoot(repoOne)
	if err != nil {
		t.Fatalf("ResolveWorktreeRoot repo one first: %v", err)
	}
	rootOneB, err := ResolveWorktreeRoot(repoOne)
	if err != nil {
		t.Fatalf("ResolveWorktreeRoot repo one second: %v", err)
	}
	if rootOneA != rootOneB {
		t.Fatalf("repo hash is not stable: %q vs %q", rootOneA, rootOneB)
	}

	hashOne := filepath.Base(rootOneA)
	if len(hashOne) != 12 {
		t.Fatalf("expected 12-char hash, got %q (%d chars)", hashOne, len(hashOne))
	}
	if _, err := hex.DecodeString(hashOne); err != nil {
		t.Fatalf("expected hex hash, got %q: %v", hashOne, err)
	}

	rootTwo, err := ResolveWorktreeRoot(repoTwo)
	if err != nil {
		t.Fatalf("ResolveWorktreeRoot repo two: %v", err)
	}
	if filepath.Base(rootOneA) == filepath.Base(rootTwo) {
		t.Fatalf("expected different repo hashes for different repo roots: %s vs %s", rootOneA, rootTwo)
	}
}

func TestIsEngineOwned(t *testing.T) {
	cases := []struct {
		name  string
		path  string
		owned bool
	}{
		{name: ".verk/runs/x.json", path: ".verk/runs/x.json", owned: true},
		{name: ".verk", path: ".verk", owned: true},
		{name: ".tickets/abc.md", path: ".tickets/abc.md", owned: true},
		{name: ".git/HEAD", path: ".git/HEAD", owned: true},
		{name: "internal/foo.go", path: "internal/foo.go", owned: false},
		{name: "notverk/x", path: "notverk/x", owned: false},
		{name: ".verkignore", path: ".verkignore", owned: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isEngineOwned(tc.path); got != tc.owned {
				t.Fatalf("path %q: isEngineOwned=%v want %v", tc.path, got, tc.owned)
			}
		})
	}
}

func TestMergeToMain_RejectsEngineOwnedPaths(t *testing.T) {
	mainRoot := t.TempDir()
	baseCommit := initEpicRepo(t, mainRoot)
	manager := NewWorktreeManager(mainRoot, baseCommit, "run-main", t.TempDir())
	worktreePath, err := manager.CreateWorktree(context.Background(), "ticket-main")
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(mainRoot, ".tickets"), 0o755); err != nil {
		t.Fatalf("prepare .tickets: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mainRoot, ".tickets", "owned.md"), []byte("owned"), 0o644); err != nil {
		t.Fatalf("seed owned ticket file: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(worktreePath, ".tickets"), 0o755); err != nil {
		t.Fatalf("prepare worktree .tickets: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktreePath, "tracked.txt"), []byte("changed"), 0o644); err != nil {
		t.Fatalf("edit tracked file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktreePath, ".tickets", "owned.md"), []byte("changed"), 0o644); err != nil {
		t.Fatalf("edit engine-owned file: %v", err)
	}

	err = manager.MergeToMain("ticket-main")
	if err == nil {
		t.Fatal("expected MergeToMain to reject engine-owned path")
	}
	if !strings.Contains(err.Error(), "engine-owned") {
		t.Fatalf("expected descriptive engine-owned rejection error, got %v", err)
	}
}

func TestMergeToMain_MergedModifiedTrackedFilePreservesMode(t *testing.T) {
	mainRoot := t.TempDir()
	baseCommit := initEpicRepo(t, mainRoot)
	manager := NewWorktreeManager(mainRoot, baseCommit, "run-main-modify", t.TempDir())
	worktreePath, err := manager.CreateWorktree(context.Background(), "ticket-mod")
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}

	if err := os.WriteFile(filepath.Join(worktreePath, "tracked.txt"), []byte("modified\n"), 0o644); err != nil {
		t.Fatalf("write tracked update: %v", err)
	}
	if err := os.Chmod(filepath.Join(worktreePath, "tracked.txt"), 0o755); err != nil {
		t.Fatalf("chmod tracked update: %v", err)
	}

	if err := manager.MergeToMain("ticket-mod"); err != nil {
		t.Fatalf("MergeToMain: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(mainRoot, "tracked.txt"))
	if err != nil {
		t.Fatalf("read merged tracked.txt: %v", err)
	}
	if string(got) != "modified\n" {
		t.Fatalf("unexpected merged content: %q", string(got))
	}
	mainInfo, err := os.Stat(filepath.Join(mainRoot, "tracked.txt"))
	if err != nil {
		t.Fatalf("stat merged tracked.txt: %v", err)
	}
	if mainInfo.Mode().Perm() != 0o755 {
		t.Fatalf("expected mode 0o755, got %v", mainInfo.Mode().Perm())
	}
}

func TestMergeToMain_MergesAddedTrackedFile(t *testing.T) {
	mainRoot := t.TempDir()
	baseCommit := initEpicRepo(t, mainRoot)
	manager := NewWorktreeManager(mainRoot, baseCommit, "run-main-add", t.TempDir())
	worktreePath, err := manager.CreateWorktree(context.Background(), "ticket-add")
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}

	added := filepath.Join(worktreePath, "added.txt")
	if err := os.WriteFile(added, []byte("added\n"), 0o755); err != nil {
		t.Fatalf("write added file: %v", err)
	}
	mustRunGit(t, worktreePath, "add", "added.txt")

	if err := manager.MergeToMain("ticket-add"); err != nil {
		t.Fatalf("MergeToMain: %v", err)
	}

	if got, err := os.ReadFile(filepath.Join(mainRoot, "added.txt")); err != nil {
		t.Fatalf("read merged added file: %v", err)
	} else if string(got) != "added\n" {
		t.Fatalf("unexpected added content: %q", string(got))
	}
	info, err := os.Stat(filepath.Join(mainRoot, "added.txt"))
	if err != nil {
		t.Fatalf("stat merged added file: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("expected executable mode for added file, got %v", info.Mode().Perm())
	}
}

func TestMergeToMain_MergesUntrackedFile(t *testing.T) {
	mainRoot := t.TempDir()
	baseCommit := initEpicRepo(t, mainRoot)
	manager := NewWorktreeManager(mainRoot, baseCommit, "run-main-untracked", t.TempDir())
	worktreePath, err := manager.CreateWorktree(context.Background(), "ticket-untracked")
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}

	if err := os.WriteFile(filepath.Join(worktreePath, "new-untracked.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatalf("write untracked file: %v", err)
	}

	if err := manager.MergeToMain("ticket-untracked"); err != nil {
		t.Fatalf("MergeToMain: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(mainRoot, "new-untracked.txt")); err != nil {
		t.Fatalf("read merged untracked file: %v", err)
	} else if string(got) != "new\n" {
		t.Fatalf("unexpected merged untracked content: %q", string(got))
	}
}

func TestMergeToMain_RejectsNonDirectoryDestinationParent(t *testing.T) {
	mainRoot := t.TempDir()
	baseCommit := initEpicRepo(t, mainRoot)
	manager := NewWorktreeManager(mainRoot, baseCommit, "run-main-parent", t.TempDir())
	worktreePath, err := manager.CreateWorktree(context.Background(), "ticket-parent")
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(worktreePath, "blocked"), 0o755); err != nil {
		t.Fatalf("prepare worktree parent: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktreePath, "blocked", "child.txt"), []byte("blocked\n"), 0o644); err != nil {
		t.Fatalf("write nested tracked file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mainRoot, "blocked"), []byte("file\n"), 0o644); err != nil {
		t.Fatalf("seed conflicting parent path in main: %v", err)
	}

	err = manager.MergeToMain("ticket-parent")
	if err == nil {
		t.Fatal("expected MergeToMain to reject non-directory destination parent")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("expected destination parent validation error, got %v", err)
	}
}

func TestMergeToMain_DeletesRemovedTrackedFile(t *testing.T) {
	mainRoot := t.TempDir()
	baseCommit := initEpicRepo(t, mainRoot)
	manager := NewWorktreeManager(mainRoot, baseCommit, "run-main-delete", t.TempDir())
	worktreePath, err := manager.CreateWorktree(context.Background(), "ticket-delete")
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}

	if err := os.Remove(filepath.Join(worktreePath, "tracked.txt")); err != nil {
		t.Fatalf("remove tracked file: %v", err)
	}

	if err := manager.MergeToMain("ticket-delete"); err != nil {
		t.Fatalf("MergeToMain: %v", err)
	}
	if _, err := os.Stat(filepath.Join(mainRoot, "tracked.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected tracked.txt removed in main root")
	}
}

func TestMergeToMain_RenamesTrackedFile(t *testing.T) {
	mainRoot := t.TempDir()
	baseCommit := initEpicRepo(t, mainRoot)
	manager := NewWorktreeManager(mainRoot, baseCommit, "run-main-rename", t.TempDir())
	worktreePath, err := manager.CreateWorktree(context.Background(), "ticket-rename")
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}

	mustRunGit(t, worktreePath, "mv", "tracked.txt", "renamed.txt")

	if err := manager.MergeToMain("ticket-rename"); err != nil {
		t.Fatalf("MergeToMain: %v", err)
	}
	if _, err := os.Stat(filepath.Join(mainRoot, "tracked.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected source path removed after rename merge")
	}
	if got, err := os.ReadFile(filepath.Join(mainRoot, "renamed.txt")); err != nil {
		t.Fatalf("read renamed file in main: %v", err)
	} else if string(got) != "base\n" {
		t.Fatalf("unexpected renamed content: %q", string(got))
	}
}

func TestMergeToMain_TypeChangeReplacesPath(t *testing.T) {
	mainRoot := t.TempDir()
	initEpicRepo(t, mainRoot)
	targetPath := filepath.Join(mainRoot, "target.txt")
	if err := os.WriteFile(targetPath, []byte("target\n"), 0o644); err != nil {
		t.Fatalf("seed target in main: %v", err)
	}
	mustRunGit(t, mainRoot, "add", "target.txt")
	mustRunGit(t, mainRoot, "commit", "-m", "add target")

	manager := NewWorktreeManager(mainRoot, "HEAD", "run-main-type", t.TempDir())
	worktreePath, err := manager.CreateWorktree(context.Background(), "ticket-type")
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}

	if err := os.Remove(filepath.Join(worktreePath, "tracked.txt")); err != nil {
		t.Fatalf("replace tracked file with symlink: %v", err)
	}
	if err := os.Symlink("target.txt", filepath.Join(worktreePath, "tracked.txt")); err != nil {
		t.Fatalf("create source symlink: %v", err)
	}

	if err := manager.MergeToMain("ticket-type"); err != nil {
		t.Fatalf("MergeToMain: %v", err)
	}
	if fi, err := os.Lstat(filepath.Join(mainRoot, "tracked.txt")); err != nil {
		t.Fatalf("stat merged tracked.txt: %v", err)
	} else if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected tracked.txt to become symlink")
	}
}

func TestMergeToMain_ModesSourceOnly(t *testing.T) {
	mainRoot := t.TempDir()
	baseCommit := initEpicRepo(t, mainRoot)
	manager := NewWorktreeManager(mainRoot, baseCommit, "run-main-mode", t.TempDir())
	worktreePath, err := manager.CreateWorktree(context.Background(), "ticket-mode")
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}

	if err := os.Chmod(filepath.Join(worktreePath, "tracked.txt"), 0o755); err != nil {
		t.Fatalf("chmod tracked file in worktree: %v", err)
	}

	if err := manager.MergeToMain("ticket-mode"); err != nil {
		t.Fatalf("MergeToMain: %v", err)
	}
	mainInfo, err := os.Stat(filepath.Join(mainRoot, "tracked.txt"))
	if err != nil {
		t.Fatalf("stat merged tracked.txt: %v", err)
	}
	if mainInfo.Mode().Perm() != 0o755 {
		t.Fatalf("expected mode 0o755 in main, got %v", mainInfo.Mode().Perm())
	}
}

func TestMergeToMain_MergesSymlinkPath(t *testing.T) {
	mainRoot := t.TempDir()
	initEpicRepo(t, mainRoot)
	target := filepath.Join(mainRoot, "target.txt")
	if err := os.WriteFile(target, []byte("link target\n"), 0o644); err != nil {
		t.Fatalf("seed target in main: %v", err)
	}
	mustRunGit(t, mainRoot, "add", "target.txt")
	mustRunGit(t, mainRoot, "commit", "-m", "add target")

	manager := NewWorktreeManager(mainRoot, "HEAD", "run-main-symlink", t.TempDir())
	worktreePath, err := manager.CreateWorktree(context.Background(), "ticket-symlink")
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	if err := os.Symlink("target.txt", filepath.Join(worktreePath, "link.txt")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}

	if err := manager.MergeToMain("ticket-symlink"); err != nil {
		t.Fatalf("MergeToMain: %v", err)
	}
	fi, err := os.Lstat(filepath.Join(mainRoot, "link.txt"))
	if err != nil {
		t.Fatalf("stat merged link.txt: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected link.txt in main to be symlink")
	}
	if target, err := os.Readlink(filepath.Join(mainRoot, "link.txt")); err != nil {
		t.Fatalf("read merged symlink target: %v", err)
	} else if target != "target.txt" {
		t.Fatalf("unexpected symlink target: %q", target)
	}
}

func TestMergeToMain_ReportsPartialWriteAfterMutation(t *testing.T) {
	mainRoot := t.TempDir()
	baseCommit := initEpicRepo(t, mainRoot)
	manager := NewWorktreeManager(mainRoot, baseCommit, "run-main-partial", t.TempDir())
	worktreePath, err := manager.CreateWorktree(context.Background(), "ticket-partial")
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}

	if err := os.WriteFile(filepath.Join(worktreePath, "tracked.txt"), []byte("modified\n"), 0o644); err != nil {
		t.Fatalf("modify tracked file: %v", err)
	}
	if err := os.Mkdir(filepath.Join(mainRoot, "zz-readonly"), 0o500); err != nil {
		t.Fatalf("prepare readonly directory: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(worktreePath, "zz-readonly"), 0o755); err != nil {
		t.Fatalf("prepare readonly directory in worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktreePath, "zz-readonly", "new.txt"), []byte("blocked\n"), 0o644); err != nil {
		t.Fatalf("write blocked file: %v", err)
	}
	mustRunGit(t, worktreePath, "add", "zz-readonly/new.txt")

	if err := os.Chmod(filepath.Join(worktreePath, "tracked.txt"), 0o644); err != nil {
		t.Fatalf("chmod tracked file: %v", err)
	}

	err = manager.MergeToMain("ticket-partial")
	if err == nil {
		t.Fatal("expected MergeToMain partial-write error")
	}
	var partial *MergeToMainPartialError
	if !errors.As(err, &partial) {
		t.Fatalf("expected MergeToMainPartialError, got %v", err)
	}
	if !contains(partial.TouchedPaths, "tracked.txt") {
		t.Fatalf("expected touched paths to include tracked.txt, got %v", partial.TouchedPaths)
	}
	if contains(partial.TouchedPaths, "zz-readonly/new.txt") {
		t.Fatalf("did not expect untouched path in partial list: %v", partial.TouchedPaths)
	}
}

func TestMergeToMain_RenameFailureReportsRemovedSource(t *testing.T) {
	mainRoot := t.TempDir()
	baseCommit := initEpicRepo(t, mainRoot)
	manager := NewWorktreeManager(mainRoot, baseCommit, "run-main-rename-partial", t.TempDir())
	worktreePath, err := manager.CreateWorktree(context.Background(), "ticket-rename-partial")
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}

	mustRunGit(t, worktreePath, "mv", "tracked.txt", "renamed.txt")
	if err := os.Mkdir(filepath.Join(mainRoot, "renamed.txt"), 0o755); err != nil {
		t.Fatalf("prepare destination blocking path: %v", err)
	}

	err = manager.MergeToMain("ticket-rename-partial")
	if err == nil {
		t.Fatal("expected MergeToMain partial-write error")
	}
	var partial *MergeToMainPartialError
	if !errors.As(err, &partial) {
		t.Fatalf("expected MergeToMainPartialError, got %v", err)
	}
	if !contains(partial.TouchedPaths, "tracked.txt") {
		t.Fatalf("expected source removal to be recorded in partial list: %v", partial.TouchedPaths)
	}
}

func TestWorktreeManager_CreateWorktreeAndSymlink(t *testing.T) {
	mainRoot := t.TempDir()
	baseCommit := initEpicRepo(t, mainRoot)
	if err := os.MkdirAll(filepath.Join(mainRoot, ".verk"), 0o755); err != nil {
		t.Fatalf("prepare .verk in main root: %v", err)
	}

	manager := NewWorktreeManager(mainRoot, baseCommit, "run-worktree", t.TempDir())
	worktreePath, err := manager.CreateWorktree(context.Background(), "ticket-a")
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}

	if got := manager.WorktreePath("ticket-a"); got != worktreePath {
		t.Fatalf("WorktreePath(%q)=%q, want %q", "ticket-a", got, worktreePath)
	}

	if !filepath.IsAbs(worktreePath) {
		t.Fatalf("expected absolute worktree path, got %q", worktreePath)
	}

	markerPath := filepath.Join(worktreePath, ".verk", "from-worktree.txt")
	if err := os.WriteFile(markerPath, []byte("from-worktree"), 0o644); err != nil {
		t.Fatalf("write through worktree symlink: %v", err)
	}

	if got, err := os.ReadFile(filepath.Join(mainRoot, ".verk", "from-worktree.txt")); err != nil {
		t.Fatalf("read shared .verk marker from main root: %v", err)
	} else if string(got) != "from-worktree" {
		t.Fatalf("unexpected shared marker value %q", string(got))
	}
}

func TestWorktreeManager_CreateWorktreeReusesExistingWorktree(t *testing.T) {
	mainRoot := t.TempDir()
	baseCommit := initEpicRepo(t, mainRoot)
	if err := os.MkdirAll(filepath.Join(mainRoot, ".verk"), 0o755); err != nil {
		t.Fatalf("prepare .verk in main root: %v", err)
	}

	manager := NewWorktreeManager(mainRoot, baseCommit, "run-reuse", t.TempDir())

	// First creation succeeds.
	path1, err := manager.CreateWorktree(context.Background(), "ticket-reuse")
	if err != nil {
		t.Fatalf("first CreateWorktree: %v", err)
	}

	// Second creation on same ticket should succeed by reusing the existing worktree.
	path2, err := manager.CreateWorktree(context.Background(), "ticket-reuse")
	if err != nil {
		t.Fatalf("second CreateWorktree (reuse): %v", err)
	}
	if path2 != path1 {
		t.Fatalf("reused worktree path %q != original path %q", path2, path1)
	}

	// Verify the worktree is functional.
	markerPath := filepath.Join(path2, ".verk", "reuse-test.txt")
	if err := os.WriteFile(markerPath, []byte("reused"), 0o644); err != nil {
		t.Fatalf("write through reused worktree symlink: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(mainRoot, ".verk", "reuse-test.txt")); err != nil {
		t.Fatalf("read shared .verk marker from main root: %v", err)
	} else if string(got) != "reused" {
		t.Fatalf("unexpected marker value %q", string(got))
	}
}

func TestPrepareWaveWorktrees_RecreatesExistingDirtyWorktree(t *testing.T) {
	mainRoot := t.TempDir()
	baseCommit := initEpicRepo(t, mainRoot)
	if err := os.MkdirAll(filepath.Join(mainRoot, ".verk"), 0o755); err != nil {
		t.Fatalf("prepare .verk in main root: %v", err)
	}

	runID := "run-dirty-recreate"
	ticketID := "ticket-dirty"
	workRoot := t.TempDir()
	manager, err := prepareWaveWorktrees(context.Background(), mainRoot, baseCommit, runID, workRoot, []string{ticketID})
	if err != nil {
		t.Fatalf("initial prepareWaveWorktrees: %v", err)
	}
	worktreePath := manager.WorktreePath(ticketID)
	stalePath := filepath.Join(worktreePath, "stale.txt")
	if err := os.WriteFile(stalePath, []byte("stale\n"), 0o644); err != nil {
		t.Fatalf("write stale file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktreePath, "tracked.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatalf("dirty tracked file: %v", err)
	}

	manager, err = prepareWaveWorktrees(context.Background(), mainRoot, baseCommit, runID, workRoot, []string{ticketID})
	if err != nil {
		t.Fatalf("reprepare dirty worktree: %v", err)
	}
	worktreePath = manager.WorktreePath(ticketID)
	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Fatalf("expected stale file removed after recreate, stat err=%v", err)
	}
	got, err := os.ReadFile(filepath.Join(worktreePath, "tracked.txt"))
	if err != nil {
		t.Fatalf("read recreated tracked file: %v", err)
	}
	if string(got) != "base\n" {
		t.Fatalf("expected recreated tracked file at base content, got %q", string(got))
	}
}

func TestPrepareWaveWorktrees_RejectsExistingWrongBaseWorktree(t *testing.T) {
	mainRoot := t.TempDir()
	baseCommit := initEpicRepo(t, mainRoot)
	if err := os.MkdirAll(filepath.Join(mainRoot, ".verk"), 0o755); err != nil {
		t.Fatalf("prepare .verk in main root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mainRoot, "later.txt"), []byte("later\n"), 0o644); err != nil {
		t.Fatalf("write later file: %v", err)
	}
	mustRunGit(t, mainRoot, "add", "later.txt")
	mustRunGit(t, mainRoot, "commit", "-m", "add later file")
	laterOut, err := gitOutput(mainRoot, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("resolve later commit: %v", err)
	}
	laterCommit := strings.TrimSpace(laterOut)

	runID := "run-wrong-base"
	ticketID := "ticket-wrong-base"
	workRoot := t.TempDir()
	worktreePath := filepath.Join(workRoot, runID, ticketID)
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		t.Fatalf("prepare worktree parent: %v", err)
	}
	mustRunGit(t, mainRoot, "worktree", "add", "--detach", worktreePath, laterCommit)

	manager, err := prepareWaveWorktrees(context.Background(), mainRoot, baseCommit, runID, workRoot, []string{ticketID})
	if err != nil {
		t.Fatalf("prepare existing wrong-base worktree: %v", err)
	}
	worktreePath = manager.WorktreePath(ticketID)
	head, err := gitRevParse(worktreePath, "HEAD")
	if err != nil {
		t.Fatalf("resolve recreated worktree HEAD: %v", err)
	}
	if head != baseCommit {
		t.Fatalf("expected existing wrong-base worktree recreated at %s, got %s", baseCommit, head)
	}
	if _, err := os.Stat(filepath.Join(worktreePath, "later.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected later file removed after recreate, stat err=%v", err)
	}
}

func TestWorktreeManager_CreateWorktreeSerializesGitWorktreeAdd(t *testing.T) {
	mainRoot := t.TempDir()
	baseCommit := initEpicRepo(t, mainRoot)
	if err := os.MkdirAll(filepath.Join(mainRoot, ".verk"), 0o755); err != nil {
		t.Fatalf("prepare .verk in main root: %v", err)
	}

	manager := NewWorktreeManager(mainRoot, baseCommit, "run-parallel", t.TempDir())

	const tickets = 4
	var wg sync.WaitGroup
	var mu sync.Mutex
	errs := make([]error, 0, tickets)
	paths := make([]string, 0, tickets)

	for i := 0; i < tickets; i++ {
		ticketID := fmt.Sprintf("ticket-%d", i)
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			path, err := manager.CreateWorktree(context.Background(), id)

			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			paths = append(paths, path)
		}(ticketID)
	}
	wg.Wait()

	if len(errs) != 0 {
		t.Fatalf("concurrent CreateWorktree returned errors: %v", errs)
	}
	if len(paths) != tickets {
		t.Fatalf("expected %d created worktrees, got %d", tickets, len(paths))
	}
	seen := map[string]struct{}{}
	for _, path := range paths {
		if path == "" {
			t.Fatalf("expected non-empty worktree path")
		}
		if !filepath.IsAbs(path) {
			t.Fatalf("expected absolute worktree path, got %q", path)
		}
		if _, exists := seen[path]; exists {
			t.Fatalf("duplicate worktree path %q", path)
		}
		seen[path] = struct{}{}
	}
}

func TestWorktreeManager_WorktreePath(t *testing.T) {
	mainRoot := t.TempDir()
	baseCommit := initEpicRepo(t, mainRoot)
	manager := NewWorktreeManager(mainRoot, baseCommit, "run-path", t.TempDir())

	path, err := manager.CreateWorktree(context.Background(), "ticket-path")
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	if got, want := manager.WorktreePath("ticket-path"), path; got != want {
		t.Fatalf("WorktreePath returned %q, want %q", got, want)
	}
	if got := manager.WorktreePath("missing-ticket"); got != "" {
		t.Fatalf("expected missing ticket to return empty path, got %q", got)
	}
}

func TestWorktreeManager_ChangedFilesFiltersEngineOwned(t *testing.T) {
	mainRoot := t.TempDir()
	initEpicRepo(t, mainRoot)

	if err := os.MkdirAll(filepath.Join(mainRoot, ".tickets"), 0o755); err != nil {
		t.Fatalf("prepare ticket path in main root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mainRoot, ".tickets", "owned.md"), []byte("owned\n"), 0o644); err != nil {
		t.Fatalf("seed owned ticket file: %v", err)
	}
	mustRunGit(t, mainRoot, "add", "-f", ".tickets/owned.md")
	mustRunGit(t, mainRoot, "commit", "-m", "add owned ticket file")
	out, err := gitOutput(mainRoot, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("read new base commit: %v", err)
	}
	baseCommit := strings.TrimSpace(out)

	manager := NewWorktreeManager(mainRoot, baseCommit, "run-changed", t.TempDir())
	if err := os.MkdirAll(filepath.Join(mainRoot, ".verk"), 0o755); err != nil {
		t.Fatalf("prepare .verk in main root: %v", err)
	}
	path, err := manager.CreateWorktree(context.Background(), "ticket-changed")
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}

	if err := os.WriteFile(filepath.Join(path, "tracked.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatalf("edit tracked file in worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatalf("create new file in worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, ".tickets", "owned.md"), []byte("changed-by-ticket\n"), 0o644); err != nil {
		t.Fatalf("edit engine-owned file in worktree: %v", err)
	}

	changed, err := manager.ChangedFiles("ticket-changed")
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}

	if !sort.StringsAreSorted(changed) {
		t.Fatalf("expected sorted changed files: %v", changed)
	}

	for _, file := range changed {
		if strings.HasPrefix(file, ".tickets/") || strings.HasPrefix(file, ".verk/") || strings.HasPrefix(file, ".git/") {
			t.Fatalf("expected engine-owned files excluded, got %q", file)
		}
	}

	if !contains(changed, "tracked.txt") {
		t.Fatalf("expected tracked.txt in changed files, got %v", changed)
	}
	if !contains(changed, "new.txt") {
		t.Fatalf("expected new.txt in changed files, got %v", changed)
	}
	if contains(changed, ".tickets/owned.md") {
		t.Fatalf("expected engine-owned file removed, got %v", changed)
	}
}

func TestWorktreeManager_ChangedFilesKeepsNonEngineOwnedCacheLikePaths(t *testing.T) {
	mainRoot := t.TempDir()
	baseCommit := initEpicRepo(t, mainRoot)
	manager := NewWorktreeManager(mainRoot, baseCommit, "run-cache-deliverable", t.TempDir())
	path, err := manager.CreateWorktree(context.Background(), "ticket-cache-deliverable")
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(path, ".gradle"), 0o755); err != nil {
		t.Fatalf("prepare gradle dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, ".gradle", "settings.gradle"), []byte("pluginManagement {}\n"), 0o644); err != nil {
		t.Fatalf("write gradle settings: %v", err)
	}

	changed, err := manager.ChangedFiles("ticket-cache-deliverable")
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}
	if !contains(changed, ".gradle/settings.gradle") {
		t.Fatalf("expected .gradle/settings.gradle to remain deliverable, got %v", changed)
	}
}

func TestWorktreeManager_ChangedFilesKeepsNonEngineOwnedPaths(t *testing.T) {
	mainRoot := t.TempDir()
	baseCommit := initEpicRepo(t, mainRoot)
	manager := NewWorktreeManager(mainRoot, baseCommit, "run-nondeliverable", t.TempDir())
	path, err := manager.CreateWorktree(context.Background(), "ticket-nondeliverable")
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}

	if err := os.WriteFile(filepath.Join(path, "tracked.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatalf("edit tracked file in worktree: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(path, ".gocache"), 0o755); err != nil {
		t.Fatalf("prepare go cache dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, ".gocache", "cache.txt"), []byte("cache\n"), 0o644); err != nil {
		t.Fatalf("write go cache file: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(path, ".pytest_cache"), 0o755); err != nil {
		t.Fatalf("prepare pytest cache dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, ".pytest_cache", "state"), []byte("state\n"), 0o644); err != nil {
		t.Fatalf("write pytest cache file: %v", err)
	}

	rawChanged, err := manager.RawChangedFiles("ticket-nondeliverable")
	if err != nil {
		t.Fatalf("RawChangedFiles: %v", err)
	}
	if !contains(rawChanged, ".gocache/cache.txt") {
		t.Fatalf("expected raw changed files to include go cache path, got %v", rawChanged)
	}
	if !contains(rawChanged, ".pytest_cache/state") {
		t.Fatalf("expected raw changed files to include pytest cache path, got %v", rawChanged)
	}

	changed, err := manager.ChangedFiles("ticket-nondeliverable")
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}
	if !contains(changed, ".gocache/cache.txt") {
		t.Fatalf("expected go cache path in deliverable changes, got %v", changed)
	}
	if !contains(changed, ".pytest_cache/state") {
		t.Fatalf("expected pytest cache path in deliverable changes, got %v", changed)
	}
	if !contains(changed, "tracked.txt") {
		t.Fatalf("expected tracked.txt in deliverable changes, got %v", changed)
	}
}

func TestWorktreeManager_DetectConflictsReportsNonEngineOwnedOverlap(t *testing.T) {
	mainRoot := t.TempDir()
	baseCommit := initEpicRepo(t, mainRoot)
	manager := NewWorktreeManager(mainRoot, baseCommit, "run-conflict-filter", t.TempDir())

	pathA, err := manager.CreateWorktree(context.Background(), "ticket-a")
	if err != nil {
		t.Fatalf("CreateWorktree ticket-a: %v", err)
	}
	pathB, err := manager.CreateWorktree(context.Background(), "ticket-b")
	if err != nil {
		t.Fatalf("CreateWorktree ticket-b: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(pathA, ".gocache"), 0o755); err != nil {
		t.Fatalf("prepare go cache dir ticket-a: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pathA, ".gocache", "shared"), []byte("a\n"), 0o644); err != nil {
		t.Fatalf("write go cache file ticket-a: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pathA, "ticket-a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatalf("write deliverable file ticket-a: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(pathB, ".gocache"), 0o755); err != nil {
		t.Fatalf("prepare go cache dir ticket-b: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pathB, ".gocache", "shared"), []byte("b\n"), 0o644); err != nil {
		t.Fatalf("write go cache file ticket-b: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pathB, "ticket-b.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatalf("write deliverable file ticket-b: %v", err)
	}

	conflicts, err := manager.DetectConflicts()
	if err != nil {
		t.Fatalf("DetectConflicts: %v", err)
	}
	if len(conflicts) != 1 {
		t.Fatalf("expected one non-engine-owned overlap conflict, got conflicts %#v", conflicts)
	}
	if conflicts[0].Path != ".gocache/shared" {
		t.Fatalf("expected .gocache/shared conflict, got %#v", conflicts[0])
	}
}

func TestWorktreeManager_Diff(t *testing.T) {
	mainRoot := t.TempDir()
	baseCommit := initEpicRepo(t, mainRoot)
	manager := NewWorktreeManager(mainRoot, baseCommit, "run-diff", t.TempDir())
	path, err := manager.CreateWorktree(context.Background(), "ticket-diff")
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}

	if err := os.WriteFile(filepath.Join(path, "tracked.txt"), []byte("updated\n"), 0o644); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}

	diff, err := manager.Diff("ticket-diff")
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.Contains(diff, "diff --git a/tracked.txt b/tracked.txt") {
		t.Fatalf("expected tracked.txt diff, got %q", diff)
	}
}

func TestWaveIntegration_InternalCommitsDoNotRequireUserGitIdentity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_AUTHOR_NAME", "")
	t.Setenv("GIT_AUTHOR_EMAIL", "")
	t.Setenv("GIT_COMMITTER_NAME", "")
	t.Setenv("GIT_COMMITTER_EMAIL", "")

	repoRoot := t.TempDir()
	mustRunGit(t, repoRoot, "init")
	if err := os.WriteFile(filepath.Join(repoRoot, "file.txt"), []byte("content\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := gitAddAllPaths(repoRoot, []string{"file.txt"}); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if err := gitCommitChanges(repoRoot, "verk: internal commit without user identity"); err != nil {
		t.Fatalf("internal commit should not require user git identity: %v", err)
	}
}

func TestGitRevParseIgnoresHostileGitEnvironment(t *testing.T) {
	mainRoot := t.TempDir()
	mainCommit := initEpicRepo(t, mainRoot)

	hostileRoot := t.TempDir()
	initEpicRepo(t, hostileRoot)
	if err := os.WriteFile(filepath.Join(hostileRoot, "hostile.txt"), []byte("hostile\n"), 0o644); err != nil {
		t.Fatalf("write hostile file: %v", err)
	}
	mustRunGit(t, hostileRoot, "add", "hostile.txt")
	mustRunGit(t, hostileRoot, "commit", "-m", "hostile commit")
	hostileOut, err := gitOutput(hostileRoot, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("resolve hostile commit: %v", err)
	}
	hostileCommit := strings.TrimSpace(hostileOut)
	t.Setenv("GIT_DIR", filepath.Join(hostileRoot, ".git"))
	t.Setenv("GIT_WORK_TREE", hostileRoot)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(t.TempDir(), "hostile-index"))
	t.Setenv("GIT_OBJECT_DIRECTORY", filepath.Join(hostileRoot, ".git", "objects"))
	t.Setenv("GIT_ALTERNATE_OBJECT_DIRECTORIES", filepath.Join(hostileRoot, ".git", "objects"))

	got, err := gitRevParse(mainRoot, "HEAD")
	if err != nil {
		t.Fatalf("gitRevParse with hostile env: %v", err)
	}
	if got != mainCommit {
		t.Fatalf("gitRevParse resolved %s, want main commit %s (hostile commit %s)", got, mainCommit, hostileCommit)
	}
}

func TestWaveIntegration_RejectsCommittedEngineOwnedPathsBeforeTicketRef(t *testing.T) {
	mainRoot := t.TempDir()
	baseCommit := initEpicRepo(t, mainRoot)
	runID := "run-reject-engine-owned"
	ticketID := "ticket-engine-owned"
	manager := NewWorktreeManager(mainRoot, baseCommit, runID, t.TempDir())

	worktreePath, err := manager.CreateWorktree(context.Background(), ticketID)
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(worktreePath, ".tickets"), 0o755); err != nil {
		t.Fatalf("prepare .tickets: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktreePath, ".tickets", "owned.md"), []byte("engine metadata\n"), 0o644); err != nil {
		t.Fatalf("write engine-owned file: %v", err)
	}
	mustRunGit(t, worktreePath, "add", "-A")
	mustRunGit(t, worktreePath, "commit", "-m", "worker committed engine metadata")

	integration := &WaveIntegrationManager{
		repoRoot: mainRoot,
		runID:    runID,
		baseRef:  baseCommit,
	}
	refName, err := integration.FreezeAcceptedTicket(ticketID, worktreePath, nil)
	if err == nil {
		t.Fatalf("expected committed engine-owned path to be rejected")
	}
	if !strings.Contains(err.Error(), "engine-owned path") {
		t.Fatalf("expected engine-owned path error, got %v", err)
	}
	if refName != "" {
		t.Fatalf("expected no accepted ticket ref name, got %q", refName)
	}
	if _, parseErr := gitRevParse(mainRoot, integrationTicketRef(runID, ticketID)); parseErr == nil {
		t.Fatalf("expected accepted ticket ref to remain unset")
	}
}

func TestWaveIntegration_RejectsEngineOwnedRenameSourceBeforeTicketRef(t *testing.T) {
	mainRoot := t.TempDir()
	initEpicRepo(t, mainRoot)
	runID := "run-reject-engine-owned-rename"
	ticketID := "ticket-engine-owned-rename"
	if err := os.MkdirAll(filepath.Join(mainRoot, ".tickets"), 0o755); err != nil {
		t.Fatalf("prepare main .tickets: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mainRoot, ".tickets", "owned.md"), []byte("engine metadata\n"), 0o644); err != nil {
		t.Fatalf("write engine-owned file: %v", err)
	}
	mustRunGit(t, mainRoot, "add", "-f", ".tickets/owned.md")
	mustRunGit(t, mainRoot, "commit", "-m", "add engine-owned ticket file")
	baseOut, err := gitOutput(mainRoot, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("resolve engine-owned base commit: %v", err)
	}
	baseCommit := strings.TrimSpace(baseOut)

	manager := NewWorktreeManager(mainRoot, baseCommit, runID, t.TempDir())
	worktreePath, err := manager.CreateWorktree(context.Background(), ticketID)
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	mustRunGit(t, worktreePath, "mv", ".tickets/owned.md", "deliverable.txt")
	mustRunGit(t, worktreePath, "commit", "-m", "rename engine metadata to deliverable")

	integration := &WaveIntegrationManager{
		repoRoot: mainRoot,
		runID:    runID,
		baseRef:  baseCommit,
	}
	refName, err := integration.FreezeAcceptedTicket(ticketID, worktreePath, nil)
	if err == nil {
		t.Fatalf("expected engine-owned rename source to be rejected")
	}
	if !strings.Contains(err.Error(), "engine-owned path") {
		t.Fatalf("expected engine-owned path error, got %v", err)
	}
	if refName != "" {
		t.Fatalf("expected no accepted ticket ref name, got %q", refName)
	}
	if _, parseErr := gitRevParse(mainRoot, integrationTicketRef(runID, ticketID)); parseErr == nil {
		t.Fatalf("expected accepted ticket ref to remain unset")
	}
}

func TestWaveIntegration_RejectsEngineOwnedPathTouchedAndRemovedBeforeTicketRef(t *testing.T) {
	mainRoot := t.TempDir()
	baseCommit := initEpicRepo(t, mainRoot)
	runID := "run-reject-engine-owned-history"
	ticketID := "ticket-engine-owned-history"
	manager := NewWorktreeManager(mainRoot, baseCommit, runID, t.TempDir())

	worktreePath, err := manager.CreateWorktree(context.Background(), ticketID)
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(worktreePath, ".tickets"), 0o755); err != nil {
		t.Fatalf("prepare .tickets: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktreePath, ".tickets", "transient.md"), []byte("engine metadata\n"), 0o644); err != nil {
		t.Fatalf("write transient engine-owned file: %v", err)
	}
	mustRunGit(t, worktreePath, "add", "-A")
	mustRunGit(t, worktreePath, "commit", "-m", "worker added engine metadata")
	if err := os.Remove(filepath.Join(worktreePath, ".tickets", "transient.md")); err != nil {
		t.Fatalf("remove transient engine-owned file: %v", err)
	}
	mustRunGit(t, worktreePath, "add", "-A")
	mustRunGit(t, worktreePath, "commit", "-m", "worker removed engine metadata")

	integration := &WaveIntegrationManager{
		repoRoot: mainRoot,
		runID:    runID,
		baseRef:  baseCommit,
	}
	refName, err := integration.FreezeAcceptedTicket(ticketID, worktreePath, nil)
	if err == nil {
		t.Fatalf("expected transient engine-owned path history to be rejected")
	}
	if !strings.Contains(err.Error(), "engine-owned path") {
		t.Fatalf("expected engine-owned path error, got %v", err)
	}
	if refName != "" {
		t.Fatalf("expected no accepted ticket ref name, got %q", refName)
	}
	if _, parseErr := gitRevParse(mainRoot, integrationTicketRef(runID, ticketID)); parseErr == nil {
		t.Fatalf("expected accepted ticket ref to remain unset")
	}
}

func TestWorktreeManager_CleanupAll(t *testing.T) {
	mainRoot := t.TempDir()
	baseCommit := initEpicRepo(t, mainRoot)
	if err := os.MkdirAll(filepath.Join(mainRoot, ".verk"), 0o755); err != nil {
		t.Fatalf("prepare .verk in main root: %v", err)
	}
	manager := NewWorktreeManager(mainRoot, baseCommit, "run-clean", t.TempDir())

	pathOne, err := manager.CreateWorktree(context.Background(), "ticket-clean-a")
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}
	pathTwo, err := manager.CreateWorktree(context.Background(), "ticket-clean-b")
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}

	if err := manager.CleanupAll(); err != nil {
		t.Fatalf("CleanupAll: %v", err)
	}

	for _, path := range []string{pathOne, pathTwo} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("expected worktree removed, got %q: %v", path, statErr)
		}
	}
}

func TestWorktreeManager_CleanupAll_WithOneFailure(t *testing.T) {
	mainRoot := t.TempDir()
	baseCommit := initEpicRepo(t, mainRoot)
	manager := NewWorktreeManager(mainRoot, baseCommit, "run-clean-fail", t.TempDir())
	goodPath, err := manager.CreateWorktree(context.Background(), "ticket-clean-ok")
	if err != nil {
		t.Fatalf("CreateWorktree: %v", err)
	}

	manager.mu.Lock()
	manager.worktrees["ticket-clean-bad"] = ""
	manager.mu.Unlock()

	logs := captureLogOutput(t, func() {
		err = manager.CleanupAll()
	})
	if err == nil {
		t.Fatalf("expected cleanup error")
	}
	if !strings.Contains(err.Error(), "worktree path") {
		t.Fatalf("expected joined cleanup error, got %v", err)
	}
	if !strings.Contains(logs, "[WARN]") {
		t.Fatalf("expected warning log for cleanup failure, got %q", logs)
	}

	if _, statErr := os.Stat(goodPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected successfully removed worktree %q, got %v", goodPath, statErr)
	}
}

// A stale worktree is a cached worktree path in VERK_WORKTREE_ROOT that is no
// longer owned by an active run (its run markers vanished after a crash) or has a
// missing filesystem target. Reconciliation should clean these up so git does not
// keep stale metadata and future runs do not reuse dead directories.
func TestReconcile_StaleCacheDirWorktree_Removed(t *testing.T) {
	mainRoot := t.TempDir()
	baseCommit := initEpicRepo(t, mainRoot)
	workRoot := filepath.Join(t.TempDir(), repoWorktreeHash(mainRoot))
	now := time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC)

	staleRunID := "run-stale-no-activity"
	staleTicketID := "ticket-a"
	stalePath := filepath.Join(workRoot, staleRunID, staleTicketID)

	if err := os.MkdirAll(filepath.Dir(stalePath), 0o755); err != nil {
		t.Fatalf("prepare cache run root %q: %v", filepath.Dir(stalePath), err)
	}
	mustRunGit(t, mainRoot, "worktree", "add", stalePath, baseCommit)

	if _, err := tkmd.AcquireClaim(mainRoot, staleRunID, staleTicketID, "lease-stale", 30*time.Minute, now); err != nil {
		t.Fatalf("seed active claim: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(mainRoot, ".verk", "runs", staleRunID)); err != nil {
		t.Fatalf("remove stale run marker: %v", err)
	}

	before := gitWorktreeList(t, mainRoot)
	if !containsWorktreePath(before, stalePath) {
		t.Fatalf("expected stale worktree to be registered before reconcile: %q", stalePath)
	}

	logs := captureLogOutput(t, func() {
		if err := ReconcileWorktrees(context.Background(), mainRoot, workRoot); err != nil {
			t.Fatalf("ReconcileWorktrees: %v", err)
		}
	})

	if _, err := os.Stat(stalePath); err == nil {
		t.Fatalf("expected stale cache entry removed: %s", stalePath)
	} else if !os.IsNotExist(err) {
		t.Fatalf("unexpected stale path stat error: %v", err)
	}
	if !strings.Contains(logs, "[INFO] reconciled stale worktree") {
		t.Fatalf("expected stale reconcile info log, got %q", logs)
	}

	after := gitWorktreeList(t, mainRoot)
	if containsWorktreePath(after, stalePath) {
		t.Fatalf("expected stale path removed from git worktree registry: %q", stalePath)
	}
}

func TestReconcile_StaleGitRegistryEntry_Pruned(t *testing.T) {
	mainRoot := t.TempDir()
	baseCommit := initEpicRepo(t, mainRoot)
	workRoot := filepath.Join(t.TempDir(), repoWorktreeHash(mainRoot))

	runID := "run-stale-registry"
	ticketID := "ticket-a"
	worktreePath := filepath.Join(workRoot, runID, ticketID)
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		t.Fatalf("prepare worktree parent %q: %v", filepath.Dir(worktreePath), err)
	}
	mustRunGit(t, mainRoot, "worktree", "add", worktreePath, baseCommit)

	registryRoot := filepath.Join(mainRoot, ".git", "worktrees")
	entries, err := os.ReadDir(registryRoot)
	if err != nil {
		t.Fatalf("read worktree registry: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("expected registry to contain created worktree entry")
	}

	if err := os.RemoveAll(worktreePath); err != nil {
		t.Fatalf("remove worktree path: %v", err)
	}

	if err := ReconcileWorktrees(context.Background(), mainRoot, workRoot); err != nil {
		t.Fatalf("ReconcileWorktrees: %v", err)
	}

	entriesAfter, err := os.ReadDir(registryRoot)
	if err != nil {
		if !os.IsNotExist(err) {
			t.Fatalf("read worktree registry after reconcile: %v", err)
		}
		entriesAfter = nil
	}
	if len(entriesAfter) != 0 {
		t.Fatalf("expected stale git worktree entry pruned, got %d remaining", len(entriesAfter))
	}

	after := gitWorktreeList(t, mainRoot)
	if containsWorktreePath(after, worktreePath) {
		t.Fatalf("expected stale path removed from git worktree registry: %q", worktreePath)
	}
}

func TestReconcile_ActiveRun_NotTouched(t *testing.T) {
	mainRoot := t.TempDir()
	baseCommit := initEpicRepo(t, mainRoot)
	workRoot := filepath.Join(t.TempDir(), repoWorktreeHash(mainRoot))
	now := time.Now().UTC().Add(24 * time.Hour)

	runID := "run-active-claim"
	ticketID := "ticket-active"
	runPath := filepath.Join(workRoot, runID)
	worktreePath := filepath.Join(runPath, ticketID)
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		t.Fatalf("prepare worktree parent %q: %v", filepath.Dir(worktreePath), err)
	}
	mustRunGit(t, mainRoot, "worktree", "add", worktreePath, baseCommit)

	if _, err := tkmd.AcquireClaim(mainRoot, runID, ticketID, "lease-active", 30*time.Minute, now); err != nil {
		t.Fatalf("seed active claim: %v", err)
	}

	before := gitWorktreeList(t, mainRoot)
	if !containsWorktreePath(before, worktreePath) {
		t.Fatalf("expected live worktree to be registered before reconcile: %q", worktreePath)
	}

	if err := ReconcileWorktrees(context.Background(), mainRoot, workRoot); err != nil {
		t.Fatalf("ReconcileWorktrees: %v", err)
	}

	if _, err := os.Stat(runPath); err != nil {
		t.Fatalf("expected active run cache dir preserved: %v", err)
	}

	after := gitWorktreeList(t, mainRoot)
	if !containsWorktreePath(after, worktreePath) {
		t.Fatalf("expected active worktree registry entry preserved: %q", worktreePath)
	}
}

func TestReconcile_MalformedCacheEntry_Skipped(t *testing.T) {
	mainRoot := t.TempDir()
	initEpicRepo(t, mainRoot)

	workRoot := filepath.Join(t.TempDir(), repoWorktreeHash(mainRoot))
	if err := os.MkdirAll(workRoot, 0o755); err != nil {
		t.Fatalf("prepare work root: %v", err)
	}

	malformedPath := filepath.Join(workRoot, "not-a-dir")
	if err := os.WriteFile(malformedPath, []byte("malformed"), 0o644); err != nil {
		t.Fatalf("prepare malformed cache entry %q: %v", malformedPath, err)
	}

	logs := captureLogOutput(t, func() {
		if err := ReconcileWorktrees(context.Background(), mainRoot, workRoot); err != nil {
			t.Fatalf("ReconcileWorktrees: %v", err)
		}
	})

	if _, err := os.Stat(malformedPath); err != nil {
		t.Fatalf("expected malformed cache entry to remain: %v", err)
	}
	if !strings.Contains(logs, "[WARN] skipping malformed cache-dir entry") {
		t.Fatalf("expected malformed cache-dir warning, got %q", logs)
	}
}

func TestReconcile_EmptyWorkRoot_NoError(t *testing.T) {
	mainRoot := t.TempDir()
	initEpicRepo(t, mainRoot)

	workRoot := filepath.Join(t.TempDir(), repoWorktreeHash(mainRoot))
	if err := os.MkdirAll(workRoot, 0o755); err != nil {
		t.Fatalf("prepare work root: %v", err)
	}

	before := snapshotDirEntries(t, workRoot)
	if err := ReconcileWorktrees(context.Background(), mainRoot, workRoot); err != nil {
		t.Fatalf("ReconcileWorktrees: %v", err)
	}
	after := snapshotDirEntries(t, workRoot)

	if len(before) != len(after) {
		t.Fatalf("expected empty work root to be unchanged: before=%v after=%v", before, after)
	}
}

func captureLogOutput(t *testing.T, fn func()) string {
	t.Helper()

	var buf bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(previous)

	fn()
	return buf.String()
}

func gitWorktreeList(t *testing.T, mainRoot string) []string {
	t.Helper()

	out, err := gitOutput(mainRoot, "worktree", "list", "--porcelain")
	if err != nil {
		t.Fatalf("git worktree list: %v", err)
	}

	paths := make([]string, 0)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "worktree ") {
			paths = append(paths, filepath.Clean(strings.TrimPrefix(line, "worktree ")))
		}
	}
	return paths
}

func snapshotDirEntries(t *testing.T, path string) []string {
	t.Helper()

	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatalf("read directory %q: %v", path, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func contains(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

func canonicalPathForContains(path string) string {
	clean := filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		return filepath.Clean(resolved)
	}
	return clean
}

func containsWorktreePath(list []string, target string) bool {
	canonicalTarget := canonicalPathForContains(target)
	for _, path := range list {
		if canonicalPathForContains(path) == canonicalTarget {
			return true
		}
	}
	return false
}
