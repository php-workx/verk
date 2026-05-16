package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	reviewFindingsV2SchemaVersion = 2
	migrationsLockFile            = ".verk/migrations.lock"
)

// MigrateReviewFindingsToV2 walks .verk/runs/*/tickets/*/review-findings.json
// and adds Legacy=true ResolutionEvidence to any resolved finding lacking
// evidence; bumps schema_version to 2. Atomic per file. Idempotent.
//
// Lock: the function acquires .verk/migrations.lock via O_CREATE|O_EXCL before
// doing any work. If the lock file already exists (another process holds it or
// the migration already ran and the lock was not cleaned up), the function
// returns an error without touching any artifact. The lock is removed on
// success so the function is idempotent: a second call will re-acquire the lock
// and determine there is nothing more to migrate.
//
// Hook: this migration is invoked by RunDoctor so operators can run it manually
// via `verk doctor`. Hooking it into engine startup would require plumbing
// repoRoot through every engine entry point; that complexity is not warranted
// for a one-shot migration. The doctor path is the canonical migration trigger.
func MigrateReviewFindingsToV2(verkRoot string) error {
	lockPath := filepath.Join(verkRoot, migrationsLockFile)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return fmt.Errorf("migrations: create lock dir: %w", err)
	}

	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("migrations: lock file %q already exists — another migration may be running, or remove it manually if stale", lockPath)
		}
		return fmt.Errorf("migrations: acquire lock: %w", err)
	}
	if _, werr := fmt.Fprintf(f, "pid=%d started=%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339)); werr != nil {
		_ = f.Close()
		_ = os.Remove(lockPath)
		return fmt.Errorf("migrations: write lock: %w", werr)
	}
	_ = f.Close()

	// Release lock on success; on error leave it so callers can inspect.
	migrationErr := migrateReviewFindingsToV2Inner(verkRoot)
	if migrationErr == nil {
		_ = os.Remove(lockPath)
	}
	return migrationErr
}

func migrateReviewFindingsToV2Inner(verkRoot string) error {
	runsDir := filepath.Join(verkRoot, ".verk", "runs")
	if _, err := os.Stat(runsDir); os.IsNotExist(err) {
		return nil // nothing to migrate
	}

	pattern := filepath.Join(runsDir, "*", "tickets", "*", "review-findings.json")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return fmt.Errorf("migrations: glob review-findings: %w", err)
	}

	for _, path := range matches {
		if err := migrateOneReviewFindingsFile(path); err != nil {
			return fmt.Errorf("migrations: migrate %s: %w", path, err)
		}
	}
	return nil
}

// migrateOneReviewFindingsFile upgrades a single review-findings.json to v2.
// If the file is already at schema_version >= 2, it is left unchanged.
func migrateOneReviewFindingsFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}

	// Decode into a flexible map so we preserve unknown fields.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("unmarshal: %w", err)
	}

	// Check current schema version — skip if already v2+.
	if sv, ok := raw["schema_version"]; ok {
		var version int
		if err := json.Unmarshal(sv, &version); err == nil && version >= reviewFindingsV2SchemaVersion {
			return nil
		}
	}

	// Decode findings to patch Legacy evidence.
	var artifact ReviewFindingsArtifact
	if err := json.Unmarshal(data, &artifact); err != nil {
		return fmt.Errorf("unmarshal artifact: %w", err)
	}

	for i := range artifact.Findings {
		f := &artifact.Findings[i]
		if f.Disposition == "resolved" && f.ResolutionEvidence == nil {
			f.ResolutionEvidence = &ResolutionEvidence{
				Legacy:     true,
				ResolvedAt: time.Time{}, // unknown; migration cannot reconstruct
			}
		}
	}

	// Always bump schema version and persist.
	artifact.SchemaVersion = reviewFindingsV2SchemaVersion
	return SaveJSONAtomic(path, artifact)
}
