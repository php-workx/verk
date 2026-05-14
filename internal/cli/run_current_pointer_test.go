package cli

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"verk/internal/adapters/ticketstore/epos"
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
		cmd.Env = testGitEnv()
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

func testGitEnv() []string {
	env := os.Environ()
	out := make([]string, 0, len(env)+6)
	for _, entry := range env {
		key, _, found := strings.Cut(entry, "=")
		if !found {
			out = append(out, entry)
			continue
		}
		if isGitLocalEnv(key) {
			continue
		}
		out = append(out, entry)
	}
	out = append(out,
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=core.hooksPath",
		"GIT_CONFIG_VALUE_0="+os.DevNull,
	)
	return out
}

func isGitLocalEnv(key string) bool {
	if strings.HasPrefix(key, "GIT_CONFIG_KEY_") || strings.HasPrefix(key, "GIT_CONFIG_VALUE_") {
		return true
	}
	switch key {
	case "GIT_ALTERNATE_OBJECT_DIRECTORIES",
		"GIT_COMMON_DIR",
		"GIT_CONFIG",
		"GIT_CONFIG_COUNT",
		"GIT_CONFIG_GLOBAL",
		"GIT_CONFIG_NOSYSTEM",
		"GIT_CONFIG_PARAMETERS",
		"GIT_DIR",
		"GIT_GRAFT_FILE",
		"GIT_IMPLICIT_WORK_TREE",
		"GIT_INDEX_FILE",
		"GIT_NO_REPLACE_OBJECTS",
		"GIT_OBJECT_DIRECTORY",
		"GIT_PREFIX",
		"GIT_REPLACE_REF_BASE",
		"GIT_SHALLOW_FILE",
		"GIT_SUPER_PREFIX",
		"GIT_OPTIONAL_LOCKS",
		"GIT_WORK_TREE":
		return true
	default:
		return false
	}
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
	ticket := epos.Ticket{
		ID:     "ver-ptr-test",
		Title:  "Pointer ordering test ticket",
		Status: epos.StatusReady,
	}
	if err := epos.SaveTicket(filepath.Join(ticketsDir, "ver-ptr-test.md"), ticket); err != nil {
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

func TestDoRunTicket_CurrentPointerNotSetOnTicketSaveFailure(t *testing.T) {
	dir := t.TempDir()
	initCLITestRepo(t, dir)

	ticketsDir := filepath.Join(dir, ".tickets")
	if err := os.MkdirAll(ticketsDir, 0o755); err != nil {
		t.Fatalf("mkdir .tickets: %v", err)
	}
	ticket := epos.Ticket{
		ID:     "ver-ticket-save",
		Title:  "Ticket save failure test",
		Status: epos.StatusReady,
	}
	if err := epos.SaveTicket(filepath.Join(ticketsDir, "ver-ticket-save.md"), ticket); err != nil {
		t.Fatalf("save ticket: %v", err)
	}

	origSaveTicket := saveTicket
	defer func() { saveTicket = origSaveTicket }()
	saveTicket = func(_ string, _ epos.Ticket) error {
		return errors.New("injected ticket save error")
	}

	t.Chdir(dir)

	var stdout, stderr bytes.Buffer
	runID, err := doRunTicket(&stdout, &stderr, "ver-ticket-save")
	if err == nil {
		t.Fatal("expected error from injected ticket save failure, got nil")
	}
	if runID == "" {
		t.Fatal("expected doRunTicket to return a non-empty runID even on failure")
	}

	currentPath := filepath.Join(dir, ".verk", "current")
	data, readErr := os.ReadFile(currentPath)
	if readErr == nil && strings.TrimSpace(string(data)) == runID {
		t.Errorf(".verk/current = %q after ticket save failure; pointer must not advance", runID)
	}
}

// TestDoRunEpic_DoesNotAdvanceCurrentOnEngineFailure verifies that doRunEpic
// does not advance .verk/current to a new runID when engine.RunEpic returns an
// error before it can successfully persist the initial run artifact.
func TestDoRunEpic_DoesNotAdvanceCurrentOnEngineFailure(t *testing.T) {
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

	// .verk/current must not be set to the new run ID after this early engine
	// failure because the run.json artifact was never persisted.
	currentPath := filepath.Join(dir, ".verk", "current")
	data, readErr := os.ReadFile(currentPath)
	if readErr != nil {
		// File not present at all — the pointer was never set or was cleaned up.
		// This is acceptable.
		return
	}
	got := strings.TrimSpace(string(data))
	if got == runID {
		t.Errorf(".verk/current = %q after engine failure; pointer must not advance "+
			"(got non-empty runID pointing at potentially missing run.json)", runID)
	}
}

func TestDoRunEpic_RejectsUnsafeTicketIDBeforeRunLockPathEscape(t *testing.T) {
	dir := t.TempDir()
	initCLITestRepo(t, dir)
	if err := os.MkdirAll(filepath.Join(dir, ".tickets"), 0o755); err != nil {
		t.Fatalf("mkdir .tickets: %v", err)
	}

	t.Chdir(dir)

	var stdout, stderr bytes.Buffer
	_, err := doRunEpic(&stdout, &stderr, "../escaped")
	if err == nil {
		t.Fatal("expected unsafe ticket id to be rejected")
	}
	if strings.Contains(stdout.String(), "run_id=") {
		t.Fatalf("unsafe ticket id printed bogus run id: %s", stdout.String())
	}
	if !strings.Contains(err.Error(), "invalid ticket id") {
		t.Fatalf("expected invalid ticket id error, got: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".verk", "escaped", "run.lock")); !os.IsNotExist(statErr) {
		t.Fatalf("unsafe ticket id created escaped lock path: %v", statErr)
	}
}

func TestDoAutoResume_RejectsUnsafeCurrentRunBeforeArtifactRead(t *testing.T) {
	dir := t.TempDir()
	initCLITestRepo(t, dir)
	if err := os.MkdirAll(filepath.Join(dir, ".verk"), 0o755); err != nil {
		t.Fatalf("mkdir .verk: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".verk", "current"), []byte("../escaped\n"), 0o644); err != nil {
		t.Fatalf("write current: %v", err)
	}

	t.Chdir(dir)

	var stdout, stderr bytes.Buffer
	err := doAutoResume(&stdout, &stderr)
	if err == nil {
		t.Fatal("expected unsafe current run id to be rejected")
	}
	if !strings.Contains(stdout.String(), "invalid current run") {
		t.Fatalf("expected invalid current run message, got stdout=%s stderr=%s err=%v", stdout.String(), stderr.String(), err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".verk", "escaped")); !os.IsNotExist(statErr) {
		t.Fatalf("unsafe current run id read escaped artifact path: %v", statErr)
	}
}

func TestDoRunEpic_CurrentPointerSetForPersistedBlockedRun(t *testing.T) {
	dir := t.TempDir()
	initCLITestRepo(t, dir)

	ticketsDir := filepath.Join(dir, ".tickets")
	if err := os.MkdirAll(ticketsDir, 0o755); err != nil {
		t.Fatalf("mkdir .tickets: %v", err)
	}
	epic := epos.Ticket{
		ID:     "ver-current-epic",
		Title:  "Current pointer epic",
		Status: epos.StatusReady,
		UnknownFrontmatter: map[string]any{
			"type": "epic",
		},
	}
	child := epos.Ticket{
		ID:     "ver-current-child",
		Title:  "Blocked child",
		Status: epos.StatusBlocked,
		UnknownFrontmatter: map[string]any{
			"parent": epic.ID,
			"type":   "task",
		},
	}
	if err := epos.SaveTicket(filepath.Join(ticketsDir, epic.ID+".md"), epic); err != nil {
		t.Fatalf("save epic: %v", err)
	}
	if err := epos.SaveTicket(filepath.Join(ticketsDir, child.ID+".md"), child); err != nil {
		t.Fatalf("save child: %v", err)
	}

	t.Chdir(dir)

	var stdout, stderr bytes.Buffer
	runID, err := doRunEpic(&stdout, &stderr, epic.ID)
	if err == nil {
		t.Fatal("expected blocked epic error, got nil")
	}
	if runID == "" {
		t.Fatal("expected doRunEpic to return a non-empty runID")
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".verk", "runs", runID, "run.json")); statErr != nil {
		t.Fatalf("expected persisted run artifact: %v", statErr)
	}
	data, readErr := os.ReadFile(filepath.Join(dir, ".verk", "current"))
	if readErr != nil {
		t.Fatalf("read .verk/current: %v", readErr)
	}
	if got := strings.TrimSpace(string(data)); got != runID {
		t.Fatalf("expected .verk/current=%q for blocked persisted run, got %q", runID, got)
	}
}
