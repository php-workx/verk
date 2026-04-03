package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"verk/internal/adapters/repo/git"
	"verk/internal/adapters/runtime/claude"
	"verk/internal/adapters/runtime/codex"
	"verk/internal/policy"
)

type DoctorCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Details string `json:"details"`
}

type RuntimeCheck struct {
	Runtime   string `json:"runtime"`
	Available bool   `json:"available"`
	Details   string `json:"details,omitempty"`
}

type DoctorReport struct {
	RepoRoot string         `json:"repo_root"`
	Checks   []DoctorCheck  `json:"checks"`
	Runtimes []RuntimeCheck `json:"runtimes"`
}

func RunDoctor(repoRoot string) (DoctorReport, int, error) {
	repoRoot, err := resolveEngineRepoRoot(repoRoot)
	if err != nil {
		return DoctorReport{}, 2, err
	}

	report := DoctorReport{RepoRoot: repoRoot}
	blocking := false
	warnings := false

	repo, err := git.New(repoRoot)
	if err != nil {
		report.Checks = append(report.Checks, DoctorCheck{Name: "repo_root", Status: "failed", Details: err.Error()})
		return report, 2, nil
	}
	root, err := repo.RepoRoot()
	if err != nil {
		report.Checks = append(report.Checks, DoctorCheck{Name: "repo_root", Status: "failed", Details: err.Error()})
		return report, 2, nil
	}
	report.RepoRoot = root
	report.Checks = append(report.Checks, DoctorCheck{Name: "repo_root", Status: "passed", Details: root})

	ticketsDir := filepath.Join(root, ".tickets")
	if _, err := os.Stat(ticketsDir); err != nil {
		report.Checks = append(report.Checks, DoctorCheck{Name: "ticket_store", Status: "failed", Details: err.Error()})
		blocking = true
	} else {
		report.Checks = append(report.Checks, DoctorCheck{Name: "ticket_store", Status: "passed", Details: ticketsDir})
	}

	claimDir := filepath.Join(ticketsDir, ".claims")
	claimStatus := DoctorCheck{Name: "claim_dir", Status: "passed", Details: claimDir}
	if entries, err := filepath.Glob(filepath.Join(claimDir, "*.json")); err == nil {
		for _, path := range entries {
			if _, err := loadOptionalClaim(path); err != nil {
				claimStatus.Status = "failed"
				claimStatus.Details = err.Error()
				blocking = true
				break
			}
		}
	} else {
		claimStatus.Status = "failed"
		claimStatus.Details = err.Error()
		blocking = true
	}
	report.Checks = append(report.Checks, claimStatus)

	artifactStatus := DoctorCheck{Name: "artifacts", Status: "passed", Details: filepath.Join(root, ".verk", "runs")}
	if paths, err := collectJSONArtifacts(filepath.Join(root, ".verk", "runs")); err == nil {
		for _, path := range paths {
			var payload map[string]any
			if err := loadJSONMap(path, &payload); err != nil {
				artifactStatus.Status = "failed"
				artifactStatus.Details = err.Error()
				blocking = true
				break
			}
		}
	} else {
		artifactStatus.Status = "failed"
		artifactStatus.Details = err.Error()
		blocking = true
	}
	report.Checks = append(report.Checks, artifactStatus)

	if _, err := repo.HeadCommit(); err != nil {
		report.Checks = append(report.Checks, DoctorCheck{Name: "git_worktree", Status: "failed", Details: err.Error()})
		blocking = true
	} else {
		report.Checks = append(report.Checks, DoctorCheck{Name: "git_worktree", Status: "passed", Details: "git worktree visible"})
	}

	cfg, err := policy.LoadConfig(root)
	if err != nil {
		report.Checks = append(report.Checks, DoctorCheck{Name: "config", Status: "failed", Details: err.Error()})
		blocking = true
	} else {
		report.Checks = append(report.Checks, DoctorCheck{Name: "config", Status: "passed", Details: "config loaded"})
		for _, runtimeCheck := range checkRuntimes(context.Background(), cfg) {
			report.Runtimes = append(report.Runtimes, runtimeCheck)
			if !runtimeCheck.Available {
				warnings = true
			}
		}
	}

	switch {
	case blocking:
		return report, 2, nil
	case warnings:
		return report, 1, nil
	default:
		return report, 0, nil
	}
}

func checkRuntimes(ctx context.Context, cfg policy.Config) []RuntimeCheck {
	names := configuredRuntimes(cfg)
	checks := make([]RuntimeCheck, 0, len(names))
	for _, name := range names {
		checks = append(checks, RuntimeCheck{Runtime: name})
	}
	for i := range checks {
		var err error
		switch checks[i].Runtime {
		case "codex":
			err = codex.New().CheckAvailability(ctx)
		case "claude":
			err = claude.New().CheckAvailability(ctx)
		default:
			err = fmt.Errorf("unsupported runtime %q", checks[i].Runtime)
		}
		checks[i].Available = err == nil
		if err != nil {
			checks[i].Details = err.Error()
		} else if cfg.Runtime.DefaultRuntime == checks[i].Runtime {
			checks[i].Details = "available (default runtime)"
		} else {
			checks[i].Details = "available"
		}
	}
	return checks
}

func configuredRuntimes(cfg policy.Config) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, 2)
	for _, candidate := range []string{cfg.Runtime.DefaultRuntime} {
		name := strings.TrimSpace(strings.ToLower(candidate))
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	if len(out) == 0 {
		return []string{"codex"}
	}
	sort.Strings(out)
	return out
}

func loadJSONMap(path string, target *map[string]any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("parse json %q: %w", path, err)
	}
	return nil
}

func collectJSONArtifacts(root string) ([]string, error) {
	paths := make([]string, 0)
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return paths, nil
		}
		return nil, err
	}
	if err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if filepath.Ext(path) == ".json" {
			paths = append(paths, path)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	sort.Strings(paths)
	return paths, nil
}
