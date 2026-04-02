package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveJSONAtomic_ReplacesAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.json")

	if err := os.WriteFile(path, []byte(`{"status":"old"}`), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	replacement := map[string]string{"status": "new"}
	if err := SaveJSONAtomic(path, replacement); err != nil {
		t.Fatalf("SaveJSONAtomic returned error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(data) == `{"status":"old"}` {
		t.Fatalf("expected file content to be replaced")
	}

	matches, err := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if err != nil {
		t.Fatalf("glob tmp files: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("expected no tmp files left behind, found %v", matches)
	}
}

func TestLoadJSON_MalformedFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.json")

	if err := os.WriteFile(path, []byte(`{"status":`), 0o644); err != nil {
		t.Fatalf("write malformed file: %v", err)
	}

	var dst map[string]any
	if err := LoadJSON(path, &dst); err == nil {
		t.Fatal("expected malformed JSON to fail")
	}
}

func TestWriteTransitionCommit_CrashBeforeRunJSONLeavesUncommittedState(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()

	paths := TransitionPaths{
		TicketArtifactPath: filepath.Join(dir, "tickets", "abc", "implementation.json"),
		ClaimArtifactPath:  filepath.Join(dir, "claims", "claim-abc.json"),
		WaveArtifactPath:   filepath.Join(dir, "waves", "wave-1.json"),
		RunArtifactPath:    filepath.Join(dir, "run.json"),
	}

	payloads := TransitionPayloads{
		TicketArtifact: map[string]any{"ticket_id": "abc", "updated_at": now.Format(time.RFC3339Nano)},
		ClaimArtifact:  map[string]any{"ticket_id": "abc", "lease_id": "lease-1"},
		WaveArtifact:   map[string]any{"wave_id": "wave-1", "status": "running"},
		RunArtifact:    map[string]any{"broken": make(chan int)},
	}

	err := WriteTransitionCommit(paths, payloads)
	if err == nil {
		t.Fatal("expected run artifact write to fail")
	}

	for _, path := range []string{paths.TicketArtifactPath, paths.ClaimArtifactPath, paths.WaveArtifactPath} {
		if _, statErr := os.Stat(path); statErr != nil {
			t.Fatalf("expected sidecar artifact %s to exist: %v", path, statErr)
		}
	}

	if _, statErr := os.Stat(paths.RunArtifactPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected run artifact to be absent after failed commit, got: %v", statErr)
	}
}
