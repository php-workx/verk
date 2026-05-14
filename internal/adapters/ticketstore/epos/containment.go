package epos

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrInvalidIdentifier marks claim identifiers rejected before filesystem access.
var ErrInvalidIdentifier = errors.New("invalid identifier")

func claimPaths(rootDir, runID, ticketID string) (string, string, error) {
	if rootDir == "" {
		return "", "", errors.New("root dir is required")
	}
	if err := validateClaimIdentifier(runID, "run_id"); err != nil {
		return "", "", err
	}
	if err := validateClaimIdentifier(ticketID, "ticket_id"); err != nil {
		return "", "", err
	}
	repoRoot := resolveRepoRoot(rootDir)
	ticketsDir := resolveTicketsDir(rootDir)
	liveDir := filepath.Join(ticketsDir, ".claims")
	verkDir := filepath.Join(repoRoot, ".verk")
	durableDir := filepath.Join(verkDir, "runs", runID, "claims")
	livePath := filepath.Join(liveDir, ticketID+".json")
	durablePath := filepath.Join(durableDir, "claim-"+ticketID+".json")
	if err := assertPathUnderIntendedBase(liveDir, ticketsDir); err != nil {
		return "", "", err
	}
	if err := assertPathUnderIntendedBase(durableDir, verkDir); err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(liveDir, 0o755); err != nil {
		return "", "", fmt.Errorf("create claim dir: %w", err)
	}
	if err := os.MkdirAll(durableDir, 0o755); err != nil {
		return "", "", fmt.Errorf("create durable claim dir: %w", err)
	}
	if err := assertPathUnderBase(livePath, liveDir); err != nil {
		return "", "", err
	}
	if err := assertPathUnderBase(durablePath, durableDir); err != nil {
		return "", "", err
	}
	return livePath, durablePath, nil
}

func assertPathUnderBase(target, base string) error {
	cleanTarget := filepath.Clean(target)
	cleanBase := filepath.Clean(base)
	resolvedBase, err := resolveBaseForContainment(cleanBase)
	if err != nil {
		return fmt.Errorf("claim path resolution failed: resolve base %q: %w", cleanBase, err)
	}
	resolvedTarget, err := resolvePathForContainment(cleanTarget)
	if err != nil {
		return fmt.Errorf("claim path resolution failed: resolve target %q: %w", cleanTarget, err)
	}
	return rejectPathEscape(resolvedBase, resolvedTarget)
}

func assertPathUnderIntendedBase(target, base string) error {
	cleanTarget := filepath.Clean(target)
	cleanBase := filepath.Clean(base)
	resolvedBase, err := resolveIntendedBaseForContainment(cleanBase)
	if err != nil {
		return fmt.Errorf("claim path resolution failed: resolve base %q: %w", cleanBase, err)
	}
	resolvedTarget, err := resolvePathForContainmentAllowMissingAncestors(cleanTarget)
	if err != nil {
		return fmt.Errorf("claim path resolution failed: resolve target %q: %w", cleanTarget, err)
	}
	return rejectPathEscape(resolvedBase, resolvedTarget)
}

func rejectPathEscape(base, target string) error {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return fmt.Errorf("claim path resolution failed: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("claim path escapes base directory")
	}
	return nil
}

func resolveBaseForContainment(base string) (string, error) {
	if _, err := filepath.EvalSymlinks(base); err != nil {
		return "", err
	}
	return resolveIntendedBaseForContainment(base)
}

func resolveIntendedBaseForContainment(base string) (string, error) {
	parent, err := filepath.EvalSymlinks(filepath.Dir(base))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(base)), nil
}

func resolvePathForContainment(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	parent, parentErr := filepath.EvalSymlinks(filepath.Dir(path))
	if parentErr != nil {
		return "", parentErr
	}
	return filepath.Join(parent, filepath.Base(path)), nil
}

func resolvePathForContainmentAllowMissingAncestors(path string) (string, error) {
	cleaned := filepath.Clean(path)
	var missingErr error
	for current := cleaned; ; current = filepath.Dir(current) {
		_, err := os.Lstat(current)
		if err == nil {
			resolved, evalErr := filepath.EvalSymlinks(current)
			if evalErr != nil {
				return "", evalErr
			}
			rel, relErr := filepath.Rel(current, cleaned)
			if relErr != nil {
				return "", relErr
			}
			if rel == "." {
				return resolved, nil
			}
			return filepath.Join(resolved, rel), nil
		}
		if !isPathNotExist(err) {
			return "", err
		}
		missingErr = err
		parent := filepath.Dir(current)
		if parent == current {
			return "", missingErr
		}
	}
}

func isPathNotExist(err error) bool {
	if err == nil {
		return false
	}
	return os.IsNotExist(err) ||
		errors.Is(err, os.ErrNotExist) ||
		strings.Contains(strings.ToLower(err.Error()), "no such file or directory")
}

func validateClaimIdentifier(id, label string) error {
	if id == "" {
		return fmt.Errorf("%w: %s is required", ErrInvalidIdentifier, label)
	}
	if id == "." || id == ".." {
		return fmt.Errorf("%w: %s contains path traversal", ErrInvalidIdentifier, label)
	}
	if filepath.IsAbs(id) {
		return fmt.Errorf("%w: %s contains absolute path", ErrInvalidIdentifier, label)
	}
	if strings.Contains(id, "..") {
		return fmt.Errorf("%w: %s contains path traversal", ErrInvalidIdentifier, label)
	}
	if strings.ContainsAny(id, "/\\") {
		return fmt.Errorf("%w: %s contains path separator", ErrInvalidIdentifier, label)
	}
	if cleaned := filepath.Clean(id); cleaned != id {
		return fmt.Errorf("%w: %s is not a clean identifier", ErrInvalidIdentifier, label)
	}
	return nil
}

func resolveRepoRoot(rootDir string) string {
	cleaned := filepath.Clean(rootDir)
	switch filepath.Base(cleaned) {
	case ".tickets", ".verk":
		return filepath.Dir(cleaned)
	default:
		return cleaned
	}
}
