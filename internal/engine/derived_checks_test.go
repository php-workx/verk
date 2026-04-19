package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"verk/internal/state"
)

// fakeLookup returns a ToolLookup that reports true for any name in the
// provided set. Unknown names return false.
func fakeLookup(names ...string) ToolLookup {
	have := make(map[string]struct{}, len(names))
	for _, n := range names {
		have[n] = struct{}{}
	}
	return func(name string) bool {
		_, ok := have[name]
		return ok
	}
}

// findCheck locates a derived check whose command contains the given
// substring. Returns (check, true) on a match; otherwise the zero value
// and false. This keeps assertions robust to reason/ID differences.
func findCheck(checks []state.ValidationCheck, commandSubstr string) (state.ValidationCheck, bool) {
	for _, c := range checks {
		if strings.Contains(c.Command, commandSubstr) {
			return c, true
		}
	}
	return state.ValidationCheck{}, false
}

func findSkipped(skipped []state.ValidationCheckSkip, idSubstr string) (state.ValidationCheckSkip, bool) {
	for _, s := range skipped {
		if strings.Contains(s.CheckID, idSubstr) || strings.Contains(s.Reason, idSubstr) {
			return s, true
		}
	}
	return state.ValidationCheckSkip{}, false
}

func TestDeriveChecks_GoPackageFromTouchedSource(t *testing.T) {
	result := DeriveChecks(DeriveChecksInput{
		Plan:         state.PlanArtifact{TicketID: "ver-y29o"},
		ChangedFiles: []string{"internal/engine/ticket_run.go"},
	})

	got, ok := findCheck(result.Checks, "go test ./internal/engine")
	if !ok {
		t.Fatalf("expected go test ./internal/engine check, got %#v", result.Checks)
	}
	if got.Source != state.ValidationCheckSourceDerived {
		t.Fatalf("expected derived source, got %q", got.Source)
	}
	if !got.Advisory {
		t.Fatalf("expected derived go check to be advisory by default")
	}
	if got.TicketID != "ver-y29o" {
		t.Fatalf("expected ticket id to propagate, got %q", got.TicketID)
	}
	if len(got.MatchedFiles) != 1 || got.MatchedFiles[0] != "internal/engine/ticket_run.go" {
		t.Fatalf("expected matched files to include the changed file, got %#v", got.MatchedFiles)
	}
	if got.Reason == "" {
		t.Fatalf("expected non-empty reason for derived go check")
	}
}

func TestDeriveChecks_GoTestFilePackageInferenceMatchesNormalSources(t *testing.T) {
	result := DeriveChecks(DeriveChecksInput{
		Plan: state.PlanArtifact{TicketID: "ver-y29o"},
		ChangedFiles: []string{
			"internal/engine/ticket_run.go",
			"internal/engine/ticket_run_test.go",
		},
	})

	matches := 0
	for _, c := range result.Checks {
		if c.Command == "go test ./internal/engine" {
			matches++
			if len(c.MatchedFiles) != 2 {
				t.Fatalf("expected both files in matched files, got %#v", c.MatchedFiles)
			}
		}
	}
	if matches != 1 {
		t.Fatalf("expected exactly one derived check for internal/engine, got %d", matches)
	}
}

func TestDeriveChecks_GoTestOnlyFilesStillDeriveCheck(t *testing.T) {
	result := DeriveChecks(DeriveChecksInput{
		Plan:         state.PlanArtifact{TicketID: "ver-y29o"},
		ChangedFiles: []string{"internal/state/validation_coverage_test.go"},
	})

	got, ok := findCheck(result.Checks, "go test ./internal/state")
	if !ok {
		t.Fatalf("expected derived go test for touched _test.go file, got %#v", result.Checks)
	}
	if !got.Advisory {
		t.Fatalf("expected derived check to be advisory")
	}
}

func TestDeriveChecks_GoFilesOutsideKnownRootsAreIgnored(t *testing.T) {
	// Files outside conventional Go roots (internal, cmd, pkg) should not
	// produce a derived go test command — derivation stays conservative.
	result := DeriveChecks(DeriveChecksInput{
		Plan:         state.PlanArtifact{TicketID: "ver-y29o"},
		ChangedFiles: []string{"scripts/tool.go", "vendor/foo/bar.go"},
	})

	for _, c := range result.Checks {
		if strings.HasPrefix(c.Command, "go test") {
			t.Fatalf("did not expect derived go test for out-of-root files, got %q", c.Command)
		}
	}
}

func TestDeriveChecks_PythonDerivesPytestAndRuffWhenRuffConfigured(t *testing.T) {
	result := DeriveChecks(DeriveChecksInput{
		Plan:         state.PlanArtifact{TicketID: "ver-y29o"},
		ChangedFiles: []string{"tests/integration/test_http_mcp.py"},
		Tools: ToolSignals{
			HasPyproject:  true,
			HasRuffConfig: true,
			HasRuff:       true,
		},
	})

	if _, ok := findCheck(result.Checks, "pytest tests/integration/test_http_mcp.py"); !ok {
		t.Fatalf("expected focused pytest command, got %#v", result.Checks)
	}
	ruff, ok := findCheck(result.Checks, "ruff check tests/integration/test_http_mcp.py")
	if !ok {
		t.Fatalf("expected ruff check command, got %#v", result.Checks)
	}
	if !ruff.Advisory {
		t.Fatalf("expected ruff derived check to be advisory")
	}
}

func TestDeriveChecks_PythonSkipsRuffWhenToolMissing(t *testing.T) {
	result := DeriveChecks(DeriveChecksInput{
		Plan:         state.PlanArtifact{TicketID: "ver-y29o"},
		ChangedFiles: []string{"tests/integration/test_http_mcp.py"},
		Tools: ToolSignals{
			HasPyproject:  true,
			HasRuffConfig: true,
			HasRuff:       false,
		},
	})

	if _, ok := findCheck(result.Checks, "pytest"); !ok {
		t.Fatalf("expected pytest check even when ruff is missing")
	}
	if _, ok := findCheck(result.Checks, "ruff"); ok {
		t.Fatalf("did not expect a ruff derived check when tool is missing")
	}
	if _, ok := findSkipped(result.Skipped, "ruff"); !ok {
		t.Fatalf("expected skipped ruff entry with explanation, got %#v", result.Skipped)
	}
}

func TestDeriveChecks_PythonWithoutRuffAdvertisementDoesNotDeriveRuff(t *testing.T) {
	// No pyproject.toml, no ruff.toml — the repo does not advertise Ruff,
	// so derivation should stay silent (no ruff check, no skipped ruff).
	result := DeriveChecks(DeriveChecksInput{
		Plan:         state.PlanArtifact{TicketID: "ver-y29o"},
		ChangedFiles: []string{"scripts/run.py"},
	})

	if _, ok := findCheck(result.Checks, "ruff"); ok {
		t.Fatalf("did not expect ruff check without tooling signals")
	}
	if _, ok := findSkipped(result.Skipped, "ruff"); ok {
		t.Fatalf("did not expect ruff skipped entry without tooling signals")
	}
}

func TestDeriveChecks_MarkdownStaleWordingForDocsTicket(t *testing.T) {
	plan := state.PlanArtifact{
		TicketID:    "ver-docs",
		Title:       "Update docs for scanner",
		Description: "Refresh docs/self-hosting.md and prune stale wording.",
		OwnedPaths:  []string{"docs"},
	}
	result := DeriveChecks(DeriveChecksInput{
		Plan:              plan,
		ChangedFiles:      []string{"docs/self-hosting.md"},
		Tools:             ToolSignals{HasMarkdownlint: true},
		StaleWordingTerms: []string{"betterleaks", "trufflehog"},
	})

	stale, ok := findCheck(result.Checks, "grep")
	if !ok {
		t.Fatalf("expected stale-wording derived check, got %#v", result.Checks)
	}
	if !stale.Advisory {
		t.Fatalf("expected stale-wording check to be advisory")
	}
	if !strings.Contains(stale.Command, "README.md") ||
		!strings.Contains(stale.Command, "CONTRIBUTING.md") ||
		!strings.Contains(stale.Command, "docs/**/*.md") {
		t.Fatalf("expected stale-wording command to scan README, CONTRIBUTING, and docs, got %q", stale.Command)
	}
	if !strings.Contains(stale.Command, "betterleaks") {
		t.Fatalf("expected stale-wording command to include configured term, got %q", stale.Command)
	}
	if _, ok := findCheck(result.Checks, "markdownlint docs/self-hosting.md"); !ok {
		t.Fatalf("expected markdownlint check when tool is available")
	}
}

func TestDeriveChecks_MarkdownSkipsStaleWordingWithoutDocsSignal(t *testing.T) {
	// Non-docs ticket changing a markdown file should not trigger
	// stale-wording derivation even when terms are supplied — the
	// derivation layer must not turn every markdown change into a docs
	// sweep.
	plan := state.PlanArtifact{
		TicketID:    "ver-unrelated",
		Title:       "Refactor internal helper",
		Description: "Pure internal refactor; not touching user-facing copy.",
	}
	result := DeriveChecks(DeriveChecksInput{
		Plan:              plan,
		ChangedFiles:      []string{"internal/engine/notes.md"},
		Tools:             ToolSignals{HasMarkdownlint: true},
		StaleWordingTerms: []string{"betterleaks"},
	})

	if _, ok := findCheck(result.Checks, "grep"); ok {
		t.Fatalf("did not expect stale-wording check for non-docs ticket")
	}
}

func TestDeriveChecks_MarkdownRecordsSkippedStaleWordingWhenNoTerms(t *testing.T) {
	plan := state.PlanArtifact{
		TicketID:    "ver-docs",
		Title:       "Docs ticket",
		Description: "Update documentation.",
		OwnedPaths:  []string{"docs"},
	}
	result := DeriveChecks(DeriveChecksInput{
		Plan:         plan,
		ChangedFiles: []string{"docs/self-hosting.md"},
		Tools:        ToolSignals{HasMarkdownlint: false},
	})

	if _, ok := findSkipped(result.Skipped, "stale wording"); !ok {
		t.Fatalf("expected skipped stale-wording entry when docs ticket has no terms, got %#v", result.Skipped)
	}
	if _, ok := findSkipped(result.Skipped, "markdownlint"); !ok {
		t.Fatalf("expected skipped markdownlint entry when tool is missing")
	}
}

func TestDeriveChecks_YAMLEmitsCheckOrSkip(t *testing.T) {
	// Tool available: derived yamllint check.
	available := DeriveChecks(DeriveChecksInput{
		Plan:         state.PlanArtifact{TicketID: "ver-y29o"},
		ChangedFiles: []string{".github/workflows/ci.yml"},
		Tools:        ToolSignals{HasYamllint: true},
	})
	yl, ok := findCheck(available.Checks, "yamllint .github/workflows/ci.yml")
	if !ok {
		t.Fatalf("expected yamllint check when tool is available, got %#v", available.Checks)
	}
	if !yl.Advisory {
		t.Fatalf("expected yamllint derived check to be advisory")
	}

	// Tool missing: skipped entry with explanation.
	missing := DeriveChecks(DeriveChecksInput{
		Plan:         state.PlanArtifact{TicketID: "ver-y29o"},
		ChangedFiles: []string{".github/workflows/ci.yml"},
		Tools:        ToolSignals{HasYamllint: false},
	})
	if _, ok := findCheck(missing.Checks, "yamllint"); ok {
		t.Fatalf("did not expect a yamllint check when tool is missing")
	}
	skip, ok := findSkipped(missing.Skipped, "yamllint")
	if !ok {
		t.Fatalf("expected skipped yamllint entry, got %#v", missing.Skipped)
	}
	if skip.Reason == "" || skip.Detail == "" {
		t.Fatalf("expected skipped entry to carry reason and detail, got %#v", skip)
	}
}

func TestDeriveChecks_ShellEmitsCheckOrSkip(t *testing.T) {
	// Tool available.
	available := DeriveChecks(DeriveChecksInput{
		Plan:         state.PlanArtifact{TicketID: "ver-y29o"},
		ChangedFiles: []string{"scripts/deploy.sh"},
		Tools:        ToolSignals{HasShellcheck: true},
	})
	if _, ok := findCheck(available.Checks, "shellcheck scripts/deploy.sh"); !ok {
		t.Fatalf("expected shellcheck check when tool is available, got %#v", available.Checks)
	}

	// Tool missing: skipped entry.
	missing := DeriveChecks(DeriveChecksInput{
		Plan:         state.PlanArtifact{TicketID: "ver-y29o"},
		ChangedFiles: []string{"scripts/deploy.sh"},
	})
	if _, ok := findCheck(missing.Checks, "shellcheck"); ok {
		t.Fatalf("did not expect a shellcheck check when tool is missing")
	}
	if _, ok := findSkipped(missing.Skipped, "shellcheck"); !ok {
		t.Fatalf("expected skipped shellcheck entry, got %#v", missing.Skipped)
	}
}

func TestDeriveChecks_NoOpWhenNoChangedFiles(t *testing.T) {
	result := DeriveChecks(DeriveChecksInput{
		Plan: state.PlanArtifact{TicketID: "ver-metadata"},
	})
	if len(result.Checks) != 0 {
		t.Fatalf("expected no derived checks for metadata-only ticket, got %#v", result.Checks)
	}
	if len(result.Skipped) != 1 {
		t.Fatalf("expected a single skipped entry documenting no files, got %#v", result.Skipped)
	}
	if !strings.Contains(result.Skipped[0].Reason, "no changed files") {
		t.Fatalf("expected skipped reason to mention no changed files, got %q", result.Skipped[0].Reason)
	}
}

func TestDeriveChecks_AllDerivedChecksAreAdvisoryByDefault(t *testing.T) {
	result := DeriveChecks(DeriveChecksInput{
		Plan: state.PlanArtifact{TicketID: "ver-y29o"},
		ChangedFiles: []string{
			"internal/engine/ticket_run.go",
			"tests/test_foo.py",
			"docs/self-hosting.md",
			".github/workflows/ci.yml",
			"scripts/deploy.sh",
		},
		Tools: ToolSignals{
			HasPyproject:    true,
			HasRuffConfig:   true,
			HasRuff:         true,
			HasMarkdownlint: true,
			HasYamllint:     true,
			HasShellcheck:   true,
		},
	})
	if len(result.Checks) == 0 {
		t.Fatalf("expected some derived checks for a broad change set")
	}
	for _, c := range result.Checks {
		if !c.Advisory {
			t.Fatalf("derived check %q must be advisory by default to avoid false positives", c.Command)
		}
		if c.Source != state.ValidationCheckSourceDerived {
			t.Fatalf("derived check %q must carry derived source, got %q", c.Command, c.Source)
		}
	}
}

func TestDeriveChecks_ChangedFilesDeduplicatedAndNormalized(t *testing.T) {
	result := DeriveChecks(DeriveChecksInput{
		Plan: state.PlanArtifact{TicketID: "ver-y29o"},
		ChangedFiles: []string{
			"internal/engine/ticket_run.go",
			" internal/engine/ticket_run.go ",
			"internal/engine/./ticket_run.go",
			"",
		},
	})
	checks := 0
	for _, c := range result.Checks {
		if c.Command == "go test ./internal/engine" {
			checks++
			if len(c.MatchedFiles) != 1 {
				t.Fatalf("expected deduplicated matched files, got %#v", c.MatchedFiles)
			}
		}
	}
	if checks != 1 {
		t.Fatalf("expected a single derived go check after dedupe, got %d", checks)
	}
}

func TestDetectToolSignals_ReadsRuffConfigFromPyproject(t *testing.T) {
	dir := t.TempDir()
	pyproject := filepath.Join(dir, "pyproject.toml")
	if err := os.WriteFile(pyproject, []byte("[tool.ruff]\nline-length = 100\n"), 0o644); err != nil {
		t.Fatalf("write pyproject: %v", err)
	}
	signals := DetectToolSignals(dir, fakeLookup("ruff"))
	if !signals.HasPyproject {
		t.Fatalf("expected pyproject signal to be detected")
	}
	if !signals.HasRuffConfig {
		t.Fatalf("expected [tool.ruff] section to enable ruff config signal")
	}
	if !signals.HasRuff {
		t.Fatalf("expected fake lookup to enable HasRuff")
	}
}

func TestDetectToolSignals_MissingRepoRootProducesZeroSignals(t *testing.T) {
	signals := DetectToolSignals("", fakeLookup())
	if signals.HasPyproject || signals.HasRuffConfig || signals.HasMarkdownlintConfig || signals.HasGolangciConfig {
		t.Fatalf("expected zero config signals for empty repo root, got %#v", signals)
	}
}

func TestDetectToolSignals_NilLookupIsSafe(t *testing.T) {
	dir := t.TempDir()
	// Touch a .golangci.yml so a file-based signal flips on.
	if err := os.WriteFile(filepath.Join(dir, ".golangci.yml"), []byte("run:\n"), 0o644); err != nil {
		t.Fatalf("write golangci: %v", err)
	}
	signals := DetectToolSignals(dir, nil)
	if !signals.HasGolangciConfig {
		t.Fatalf("expected golangci signal from file on disk")
	}
	if signals.HasRuff || signals.HasMarkdownlint || signals.HasYamllint || signals.HasShellcheck {
		t.Fatalf("expected optional tools to be false with nil lookup, got %#v", signals)
	}
}
