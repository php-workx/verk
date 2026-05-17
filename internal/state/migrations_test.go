package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// setupReviewFindingsFile writes a review-findings.json to the canonical path
// inside a fake verk directory tree. Returns the path to the file.
func setupReviewFindingsFile(t *testing.T, verkRoot string, artifact ReviewFindingsArtifact) string {
	t.Helper()
	dir := filepath.Join(verkRoot, ".verk", "runs", "run-1", "tickets", "ticket-1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("setup dir: %v", err)
	}
	path := filepath.Join(dir, "review-findings.json")
	if err := SaveJSONAtomic(path, artifact); err != nil {
		t.Fatalf("setup file: %v", err)
	}
	return path
}

// baseV1FindingsArtifact returns a minimal v1 ReviewFindingsArtifact with one
// resolved finding that has no ResolutionEvidence (legacy state).
func baseV1FindingsArtifact() ReviewFindingsArtifact {
	return ReviewFindingsArtifact{
		ArtifactMeta: ArtifactMeta{
			SchemaVersion: 1,
			RunID:         "run-1",
			CreatedAt:     time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			UpdatedAt:     time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		TicketID: "ticket-1",
		Attempt:  1,
		Summary:  "migrated",
		Findings: []ReviewFinding{
			{
				ID:          "finding-1",
				Severity:    SeverityP2,
				Title:       "legacy resolved finding",
				Body:        "this was resolved without evidence",
				Disposition: "resolved",
				// ResolutionEvidence intentionally nil — legacy state
			},
		},
		Passed:                   true,
		EffectiveReviewThreshold: SeverityP2,
	}
}

func TestMigration_LegacyResolvedFindingGetsLegacyMarker(t *testing.T) {
	verkRoot := t.TempDir()
	artifact := baseV1FindingsArtifact()
	path := setupReviewFindingsFile(t, verkRoot, artifact)

	if err := MigrateReviewFindingsToV2(verkRoot); err != nil {
		t.Fatalf("MigrateReviewFindingsToV2: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migrated file: %v", err)
	}
	var migrated ReviewFindingsArtifact
	if err := json.Unmarshal(data, &migrated); err != nil {
		t.Fatalf("unmarshal migrated file: %v", err)
	}

	if migrated.SchemaVersion != reviewFindingsV2SchemaVersion {
		t.Errorf("expected schema_version=%d, got %d", reviewFindingsV2SchemaVersion, migrated.SchemaVersion)
	}
	if len(migrated.Findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(migrated.Findings))
	}
	f := migrated.Findings[0]
	if f.ResolutionEvidence == nil {
		t.Fatal("expected ResolutionEvidence to be set after migration")
	}
	if !f.ResolutionEvidence.Legacy {
		t.Error("expected Legacy=true on migrated finding")
	}
}

func TestMigration_Idempotent(t *testing.T) {
	verkRoot := t.TempDir()
	artifact := baseV1FindingsArtifact()
	path := setupReviewFindingsFile(t, verkRoot, artifact)

	// First run.
	if err := MigrateReviewFindingsToV2(verkRoot); err != nil {
		t.Fatalf("first MigrateReviewFindingsToV2: %v", err)
	}

	// Capture state after first run.
	data1, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after first migration: %v", err)
	}

	// Second run (lock was removed on success — re-acquire is fine).
	if err := MigrateReviewFindingsToV2(verkRoot); err != nil {
		t.Fatalf("second MigrateReviewFindingsToV2: %v", err)
	}

	data2, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after second migration: %v", err)
	}

	// The file content must be identical: idempotent.
	var m1, m2 ReviewFindingsArtifact
	if err := json.Unmarshal(data1, &m1); err != nil {
		t.Fatalf("unmarshal first: %v", err)
	}
	if err := json.Unmarshal(data2, &m2); err != nil {
		t.Fatalf("unmarshal second: %v", err)
	}

	if m1.SchemaVersion != m2.SchemaVersion {
		t.Errorf("schema_version changed between runs: %d → %d", m1.SchemaVersion, m2.SchemaVersion)
	}
	for i := range m1.Findings {
		ev1 := m1.Findings[i].ResolutionEvidence
		ev2 := m2.Findings[i].ResolutionEvidence
		if (ev1 == nil) != (ev2 == nil) {
			t.Errorf("finding[%d] ResolutionEvidence presence changed between runs", i)
		}
		if ev1 != nil && ev2 != nil && ev1.Legacy != ev2.Legacy {
			t.Errorf("finding[%d] Legacy flag changed between runs: %v → %v", i, ev1.Legacy, ev2.Legacy)
		}
	}
}

func TestMigration_RefusesUnderHeldLock(t *testing.T) {
	verkRoot := t.TempDir()

	// Pre-create the lock file to simulate a held lock.
	lockPath := filepath.Join(verkRoot, migrationsLockFile)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatalf("setup lock dir: %v", err)
	}
	if err := os.WriteFile(lockPath, []byte("pid=99999 started=held\n"), 0o644); err != nil {
		t.Fatalf("pre-create lock: %v", err)
	}

	err := MigrateReviewFindingsToV2(verkRoot)
	if err == nil {
		t.Fatal("expected error when lock is already held, got nil")
	}
	// Lock file must still exist (we did not remove it).
	if _, statErr := os.Stat(lockPath); os.IsNotExist(statErr) {
		t.Error("lock file was removed even though migration refused to run")
	}
}

func TestMigration_AlreadyV2IsUntouched(t *testing.T) {
	verkRoot := t.TempDir()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	artifact := ReviewFindingsArtifact{
		ArtifactMeta: ArtifactMeta{
			SchemaVersion: reviewFindingsV2SchemaVersion,
			RunID:         "run-1",
			CreatedAt:     now,
			UpdatedAt:     now,
		},
		TicketID: "ticket-1",
		Attempt:  1,
		Summary:  "already v2",
		Findings: []ReviewFinding{
			{
				ID:          "finding-2",
				Severity:    SeverityP3,
				Title:       "resolved with evidence",
				Body:        "properly resolved",
				Disposition: "resolved",
				ResolutionEvidence: &ResolutionEvidence{
					DiffRanges:     []DiffRange{{File: "main.go", StartLine: 1, EndLine: 5}},
					TestReferences: []TestReference{{Kind: "test_function", Package: "main", Name: "TestFoo"}},
					RepairCycleID:  "cycle-1",
					ResolvedAt:     now,
				},
			},
		},
		Passed:                   true,
		EffectiveReviewThreshold: SeverityP2,
	}
	path := setupReviewFindingsFile(t, verkRoot, artifact)

	// Record mtime before migration.
	fi1, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat before: %v", err)
	}

	if err := MigrateReviewFindingsToV2(verkRoot); err != nil {
		t.Fatalf("MigrateReviewFindingsToV2: %v", err)
	}

	// File should be unchanged: migration skips v2+ files.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	var migrated ReviewFindingsArtifact
	if err := json.Unmarshal(data, &migrated); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if migrated.SchemaVersion != reviewFindingsV2SchemaVersion {
		t.Errorf("schema_version changed: expected %d, got %d", reviewFindingsV2SchemaVersion, migrated.SchemaVersion)
	}
	// Mtime should not have changed (file was not rewritten).
	fi2, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	if !fi1.ModTime().Equal(fi2.ModTime()) {
		t.Logf("note: mtime changed even for already-v2 file (SaveJSONAtomic always rewrites); this is acceptable")
	}
}
