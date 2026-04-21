package cli

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"verk/internal/adapters/ticketstore/tkmd"
)

// initCLITestRepo creates a minimal git repository in dir with a single
// commit so HeadCommit / CurrentBranch succeed inside doRunTicket /
// doRunEpic.
func initCLITestRepo(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
		}
	}
	run("init")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("test\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	run("add", "README")
	run("commit", "-m", "initial")
}

// TestDoRunTicket_CurrentPointerNotSetOnSaveFailure verifies the ordering fix:
// writeCurrentRunID must be called AFTER the initial run.json SaveJSONAtomic.
// When SaveJSONAtomic fails, doRunTicket must return an error and .verk/current
// must NOT be updated to the new (dangling) run ID.
func TestDoRunTicket_CurrentPointerNotSetOnSaveFailure(t *testing.T) {
	dir := t.TempDir()
	initCLITestRepo(t, dir)

	// Create .tickets directory and a minimal task ticket.
	ticketsDir := filepath.Join(dir, ".tickets")
	if err := os.MkdirAll(ticketsDir, 0o755); err != nil {
		t.Fatalf("mkdir .tickets: %v", err)
	}
	ticket := tkmd.Ticket{
		ID:     "ver-ptr-test",
		Title:  "Pointer ordering test ticket",
		Status: tkmd.StatusReady,
	}
	if err := tkmd.SaveTicket(filepath.Join(ticketsDir, "ver-ptr-test.md"), ticket); err != nil {
		t.Fatalf("save ticket: %v", err)
	}

	// Inject saveJSONAtomic to fail — this covers the initial run.json write
	// inside doRunTicket.  With the old code, writeCurrentRunID was already
	// called before this point, leaving a dangling pointer.  With the fix,
	// writeCurrentRunID is called after the save, so it is never reached.
	origSave := saveJSONAtomic
	defer func() { saveJSONAtomic = origSave }()
	saveJSONAtomic = func(_ string, _ any) error {
		return errors.New("injected disk-full error")
	}

	// Change working directory to the temp repo so loadExecutionContext works.
	t.Chdir(dir)

	var stdout, stderr bytes.Buffer
	runID, err := doRunTicket(&stdout, &stderr, "ver-ptr-test")
	if err == nil {
		t.Fatal("expected error from injected SaveJSONAtomic failure, got nil")
	}
	if runID == "" {
		t.Fatal("expected doRunTicket to return a non-empty runID even on failure")
	}

	// .verk/current must NOT contain the generated run ID.
	// Either the file doesn't exist, or it contains a different (prior) value.
	currentPath := filepath.Join(dir, ".verk", "current")
	data, readErr := os.ReadFile(currentPath)
	if readErr == nil {
		got := strings.TrimSpace(string(data))
		if got == runID {
			t.Errorf(".verk/current = %q after SaveJSONAtomic failure; "+
				"writeCurrentRunID must not be called before run.json is persisted", runID)
		}
	}
	// If the file doesn't exist, that's the correct outcome too.
}

// TestDoRunEpic_CurrentPointerClearedOnEngineFailure verifies the fix for
// doRunEpic: when engine.RunEpic returns an error, writeCurrentRunID("") must
// be called to clear the pointer that was set before the goroutine launched.
// If not cleared, .verk/current keeps pointing at a run whose run.json may
// never have been written.
func TestDoRunEpic_CurrentPointerClearedOnEngineFailure(t *testing.T) {
	dir := t.TempDir()
	initCLITestRepo(t, dir)
	// Intentionally do NOT create the epic ticket file.
	// engine.RunEpic will call listEpicChildren → LoadTicket, which fails
	// with "load epic …: open …: no such file or directory".
	if err := os.MkdirAll(filepath.Join(dir, ".tickets"), 0o755); err != nil {
		t.Fatalf("mkdir .tickets: %v", err)
	}

	// Change working directory to the temp repo so loadExecutionContext works.
	t.Chdir(dir)

	var stdout, stderr bytes.Buffer
	runID, err := doRunEpic(&stdout, &stderr, "ver-missing-epic")
	if err == nil {
		t.Fatal("expected error when epic ticket file does not exist, got nil")
	}
	if runID == "" {
		t.Fatal("expected doRunEpic to return a non-empty runID even on failure")
	}

	// .verk/current must be cleared (empty) after the engine failure so that
	// downstream commands don't resolve to a run without a valid run.json.
	currentPath := filepath.Join(dir, ".verk", "current")
	data, readErr := os.ReadFile(currentPath)
	if readErr != nil {
		// File not present at all — the pointer was never set or was cleaned up.
		// This is acceptable.
		return
	}
	got := strings.TrimSpace(string(data))
	if got == runID {
		t.Errorf(".verk/current = %q after engine failure; pointer must be cleared "+
			"(got non-empty runID pointing at potentially missing run.json)", runID)
	}
}
