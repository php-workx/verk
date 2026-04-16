package policy

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// WriteConfig serialises cfg to .verk/config.yaml under repoRoot, creating the
// .verk directory if needed. An existing config file is overwritten.
func WriteConfig(repoRoot string, cfg Config) error {
	verkDir := filepath.Join(repoRoot, ".verk")
	if err := os.MkdirAll(verkDir, 0o755); err != nil {
		return fmt.Errorf("create .verk directory: %w", err)
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(cfg); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("close yaml encoder: %w", err)
	}

	configPath := filepath.Join(verkDir, "config.yaml")
	if err := os.WriteFile(configPath, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write config %q: %w", configPath, err)
	}
	return nil
}

// DetectedTooling describes a project's build tooling found in a directory.
type DetectedTooling struct {
	// File is the tooling file that was found (e.g. "Justfile", "Makefile").
	File string
	// SuggestedCommands are the recommended quality gate commands for this tooling.
	SuggestedCommands []string
}

// DetectProjectTooling scans dir for known build/quality tooling files and
// returns suggestions for quality gate commands. Multiple entries are returned
// for monorepo roots where several packages are detected.
func DetectProjectTooling(dir string) []DetectedTooling {
	var results []DetectedTooling

	type probe struct {
		file     string
		commands func(dir string) []string
	}

	probes := []probe{
		{
			file: "Justfile",
			commands: func(dir string) []string {
				// Prefer project-level CI target; fall back to check or lint.
				for _, target := range []string{"ci", "check", "lint", "test"} {
					if justTargetExists(dir, target) {
						return []string{"just " + target}
					}
				}
				return []string{"just --list"}
			},
		},
		{
			file: "justfile", // lowercase variant
			commands: func(dir string) []string {
				for _, target := range []string{"ci", "check", "lint", "test"} {
					if justTargetExists(dir, target) {
						return []string{"just " + target}
					}
				}
				return []string{"just --list"}
			},
		},
		{
			file: "Makefile",
			commands: func(dir string) []string {
				for _, target := range []string{"ci", "check", "lint", "test"} {
					if makeTargetExists(dir, target) {
						return []string{"make " + target}
					}
				}
				return []string{"make help"}
			},
		},
		{
			file: "package.json",
			commands: func(dir string) []string {
				for _, script := range []string{"ci", "check", "lint"} {
					if npmScriptExists(dir, script) {
						return []string{"npm run " + script}
					}
				}
				return []string{"npm run lint", "npm test"}
			},
		},
		{
			file:     "Cargo.toml",
			commands: func(dir string) []string { return []string{"cargo clippy -- -D warnings", "cargo test"} },
		},
		{
			file:     "go.mod",
			commands: func(dir string) []string { return []string{"go vet ./...", "go test ./..."} },
		},
		{
			file:     "pyproject.toml",
			commands: func(dir string) []string { return []string{"ruff check .", "pytest"} },
		},
		{
			file:     "setup.py",
			commands: func(dir string) []string { return []string{"flake8 .", "pytest"} },
		},
	}

	seen := make(map[string]bool)
	for _, p := range probes {
		target := filepath.Join(dir, p.file)
		if _, err := os.Stat(target); err != nil {
			continue
		}
		// Deduplicate: don't suggest the same file twice (e.g. Justfile and justfile).
		key := strings.ToLower(p.file)
		if seen[key] {
			continue
		}
		seen[key] = true
		results = append(results, DetectedTooling{
			File:              p.file,
			SuggestedCommands: p.commands(dir),
		})
	}
	return results
}

// justTargetExists returns true when `just --list` output contains the named target.
func justTargetExists(dir, target string) bool {
	cmd := exec.Command("just", "--list", "--list-heading", "")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == target {
			return true
		}
	}
	return false
}

// makeTargetExists returns true when the Makefile in dir declares the named target.
func makeTargetExists(dir, target string) bool {
	cmd := exec.Command("make", "-qp", "--no-print-directory")
	cmd.Dir = dir
	out, _ := cmd.Output() // make -qp exits non-zero even on success
	prefix := target + ":"
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

// npmScriptExists returns true when package.json in dir has the named script.
func npmScriptExists(dir, script string) bool {
	cmd := exec.Command("npm", "run", "--json")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	// Simple string search — avoids a JSON parser dependency for a hint.
	return strings.Contains(string(out), `"`+script+`"`)
}
