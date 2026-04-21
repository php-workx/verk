// Package engine — derived validation checks.
//
// This file builds conservative, focused validation checks from the actual
// changed files and declared scope of a ticket, combined with signals from
// the repository (tooling config files, available linters, docs
// conventions). It is the derivation layer that consumes
// PlanArtifact.ValidationCommands / DeclaredChecks / OwnedPaths,
// implementation/repair changed files, repository tool signals, and policy
// settings so workers get helpful narrow checks even when a ticket forgot
// to list them explicitly.
//
// Derivation is intentionally conservative:
//
//   - It prefers narrow, cheap, relevant checks (package-scoped `go test`,
//     file-scoped linters) over broad repository gates.
//   - Missing optional tooling (e.g. shellcheck not installed) is recorded
//     as a skipped check with an explanation rather than producing a
//     failure.
//   - Derived checks default to Advisory. Closure gates run them and
//     report results, but an advisory derived check does not block closure
//     on its own — repair routing or reviewer severity may promote a
//     derived check to required when policy demands it.
//
// Broad repo-wide gates (e.g. repo-wide `just lint`, `go test ./...`) stay
// attached to wave or epic closure and are NOT introduced into every
// ticket by this layer.
package engine

import (
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"verk/internal/state"
)

// ToolLookup reports whether an executable named `name` is available on
// PATH. Tests pass a fake to control which tools appear installed;
// production code uses DefaultToolLookup.
type ToolLookup func(name string) bool

// DefaultToolLookup returns a ToolLookup backed by os/exec.LookPath.
func DefaultToolLookup() ToolLookup {
	return func(name string) bool {
		if strings.TrimSpace(name) == "" {
			return false
		}
		_, err := exec.LookPath(name)
		return err == nil
	}
}

// ToolSignals describes the tooling-related signals that influence derived
// check selection: which configuration files the repository advertises and
// which optional linters are available on PATH.
//
// Callers usually populate this via DetectToolSignals, but tests construct
// it directly so the derivation layer stays unit-testable without touching
// the real filesystem or PATH.
type ToolSignals struct {
	// Go tooling
	HasGolangciConfig bool

	// Python tooling
	HasPyproject  bool
	HasRuffConfig bool

	// Markdown tooling
	HasMarkdownlintConfig bool

	// Optional tool availability (detected via ToolLookup)
	HasRuff         bool
	HasMarkdownlint bool
	HasYamllint     bool
	HasShellcheck   bool
}

// DetectToolSignals inspects repoRoot for common tooling configuration and
// uses lookup to detect optional linters on PATH. repoRoot may be empty,
// in which case file-based signals default to false. lookup may be nil, in
// which case no optional tools are considered available.
//
// DetectToolSignals never returns an error: missing files or unreadable
// configs simply translate to "signal not present", which keeps derivation
// conservative by default.
func DetectToolSignals(repoRoot string, lookup ToolLookup) ToolSignals {
	if lookup == nil {
		lookup = func(string) bool { return false }
	}
	signals := ToolSignals{
		HasRuff:         lookup("ruff"),
		HasMarkdownlint: lookup("markdownlint"),
		HasYamllint:     lookup("yamllint"),
		HasShellcheck:   lookup("shellcheck"),
	}
	if strings.TrimSpace(repoRoot) == "" {
		return signals
	}
	signals.HasGolangciConfig = anyFileExists(repoRoot,
		".golangci.yml", ".golangci.yaml", ".golangci.toml")
	signals.HasPyproject = fileExists(repoRoot, "pyproject.toml")
	signals.HasRuffConfig = hasRuffConfig(repoRoot)
	signals.HasMarkdownlintConfig = hasMarkdownlintConfig(repoRoot)
	return signals
}

func fileExists(root, name string) bool {
	if root == "" || name == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(root, name))
	return err == nil
}

func anyFileExists(root string, names ...string) bool {
	for _, n := range names {
		if fileExists(root, n) {
			return true
		}
	}
	return false
}

// hasRuffConfig treats a repository as Ruff-configured when a dedicated
// ruff config file exists, or when pyproject.toml contains a [tool.ruff]
// section. The pyproject check is a substring match rather than a full
// TOML parse — Ruff accepts [tool.ruff], [tool.ruff.lint], etc., so any
// occurrence of "[tool.ruff" is a sufficient signal for the derivation
// layer.
func hasRuffConfig(root string) bool {
	if anyFileExists(root, "ruff.toml", ".ruff.toml") {
		return true
	}
	pyproject := filepath.Join(root, "pyproject.toml")
	data, err := os.ReadFile(pyproject)
	if err != nil {
		return false
	}
	return strings.Contains(string(data), "[tool.ruff")
}

// hasMarkdownlintConfig covers the common markdownlint and markdownlint-cli2
// config filenames. Any match is treated as "repo advertises markdownlint".
func hasMarkdownlintConfig(root string) bool {
	return anyFileExists(root,
		".markdownlint.json",
		".markdownlint.jsonc",
		".markdownlint.yaml",
		".markdownlint.yml",
		".markdownlintrc",
		".markdownlint-cli2.jsonc",
		".markdownlint-cli2.yaml",
		".markdownlint-cli2.yml",
	)
}

// DeriveChecksInput bundles the data needed to derive focused checks for a
// single ticket. Only ChangedFiles and Plan.TicketID are required; missing
// fields default to safe zero values.
type DeriveChecksInput struct {
	// Plan is the ticket plan artifact. TicketID, Title, Description, and
	// OwnedPaths, ValidationCommands, and DeclaredChecks are consulted;
	// the plan is not mutated.
	Plan state.PlanArtifact
	// ChangedFiles lists repository-relative paths touched by the ticket's
	// implementation or repair work. Empty / whitespace entries are
	// ignored, duplicates collapsed.
	ChangedFiles []string
	// Tools describes repo tooling signals. Construct via DetectToolSignals
	// in production; tests typically set fields directly.
	Tools ToolSignals
	// StaleWordingTerms are literal strings the engine should search for
	// in docs when the ticket touches documentation. Empty disables the
	// stale-wording derivation; the derivation layer never invents terms.
	StaleWordingTerms []string
	// StaleWordingDocs are doc paths to include in stale-wording scans.
	// Empty falls back to default docs paths used by epic closure.
	StaleWordingDocs []string

	// SkipUnscopedGoFallback disables the uncertain fallback command
	// (go test ./internal/...) when touched .go files are outside
	// internal/cmd/pkg. This is useful in higher scopes like wave
	// verification where unscoped fallback checks can cause false repair
	// cycles.
	SkipUnscopedGoFallback bool
}

// DeriveChecksResult carries the derived checks plus any optional tooling
// skipped with an explanation. Both slices are nil when there is nothing
// to report.
type DeriveChecksResult struct {
	Checks  []state.ValidationCheck
	Skipped []state.ValidationCheckSkip
}

// DeriveChecks projects a conservative set of ValidationChecks from the
// changed files and ticket scope. Derived checks are advisory by default:
// they surface in validation coverage artifacts and can feed repair
// routing, but they do not block closure on their own unless promoted to
// required by policy or reviewer severity.
//
// DeriveChecks never mutates its inputs and is safe to call with zero
// values. When ChangedFiles is empty (e.g. a pure metadata ticket), it
// returns a single Skipped entry documenting that no derivation applied.
func DeriveChecks(input DeriveChecksInput) DeriveChecksResult {
	ticketID := input.Plan.TicketID
	files := normalizeChangedFiles(input.ChangedFiles)
	if len(files) == 0 {
		return DeriveChecksResult{
			Skipped: []state.ValidationCheckSkip{{
				CheckID: declaredCheckID(ticketID, "derived-no-changed-files"),
				Reason:  "no changed files required derived coverage",
				Detail:  "ticket declared no file changes; nothing to derive",
			}},
		}
	}

	declared := explicitDeclaredCommands(input.Plan)
	result := DeriveChecksResult{}
	addGoChecks(&result, ticketID, files, declared, input.SkipUnscopedGoFallback)
	addPythonChecks(&result, ticketID, files, input.Tools, declared)
	addMarkdownChecks(&result, input.Plan, files, input.Tools, input.StaleWordingTerms, input.StaleWordingDocs, declared)
	addYAMLChecks(&result, ticketID, files, input.Tools, declared)
	addShellChecks(&result, ticketID, files, input.Tools, declared)
	return result
}

// explicitDeclaredCommands returns normalized commands from plan fields that
// were already explicitly declared (validation_commands + declared_checks).
// We use this set to avoid deriving duplicate checks that the ticket already
// asks to run.
func explicitDeclaredCommands(plan state.PlanArtifact) map[string]struct{} {
	capacity := len(plan.ValidationCommands) + len(plan.DeclaredChecks)
	out := make(map[string]struct{}, capacity)
	collect := func(commands []string) {
		for _, cmd := range commands {
			trimmed := strings.TrimSpace(cmd)
			if trimmed == "" {
				continue
			}
			out[trimmed] = struct{}{}
		}
	}
	collect(plan.ValidationCommands)
	collect(plan.DeclaredChecks)
	if len(out) == 0 {
		return nil
	}
	return out
}

func shouldSkipDerivedCommand(declared map[string]struct{}, cmd string) bool {
	trimmed := strings.TrimSpace(cmd)
	if trimmed == "" || len(declared) == 0 {
		return false
	}
	_, ok := declared[trimmed]
	return ok
}

// normalizeChangedFiles trims whitespace, drops empties, converts to
// forward-slash form, and returns a sorted deduplicated slice so output
// ordering is deterministic.
func normalizeChangedFiles(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, f := range in {
		trimmed := strings.TrimSpace(f)
		if trimmed == "" {
			continue
		}
		clean := filepath.ToSlash(filepath.Clean(trimmed))
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	}
	sort.Strings(out)
	return out
}

func filesWithSuffix(files []string, suffix string) []string {
	var out []string
	for _, f := range files {
		if strings.HasSuffix(f, suffix) {
			out = append(out, f)
		}
	}
	return out
}

// addGoChecks derives `go test ./<pkg>` for each touched Go package under
// conventional roots (internal, cmd, pkg). Files outside those roots are
// ignored here — broad fallback gates stay on wave/epic closure.
// _test.go files are treated the same as normal package files: a change
// to a test still means the containing package should be rerun.
func addGoChecks(
	result *DeriveChecksResult,
	ticketID string,
	files []string,
	declared map[string]struct{},
	skipFallback bool,
) {
	goFiles := filesWithSuffix(files, ".go")
	if len(goFiles) == 0 {
		return
	}

	packages := make(map[string][]string)
	var order []string
	for _, f := range goFiles {
		pkg := goPackageDir(f)
		if pkg == "" {
			continue
		}
		if _, ok := packages[pkg]; !ok {
			order = append(order, pkg)
		}
		packages[pkg] = append(packages[pkg], f)
	}
	sort.Strings(order)

	for _, pkg := range order {
		matched := append([]string(nil), packages[pkg]...)
		sort.Strings(matched)
		cmd := "go test ./" + pkg
		if shouldSkipDerivedCommand(declared, cmd) {
			continue
		}
		reason := "go test for changed package " + pkg
		if anyHasSuffix(matched, "_test.go") && !anyLacksSuffix(matched, "_test.go") {
			reason = "go test for changed test files in " + pkg
		}
		result.Checks = append(result.Checks, state.ValidationCheck{
			ID:           declaredCheckID(ticketID, cmd),
			Scope:        state.ValidationScopeTicket,
			Source:       state.ValidationCheckSourceDerived,
			Command:      cmd,
			Reason:       reason,
			MatchedFiles: matched,
			Severity:     state.SeverityP2,
			TicketID:     ticketID,
			Advisory:     true,
		})
	}

	if len(order) > 0 {
		return
	}

	// No package inference was possible (no recognized root under
	// internal/cmd/pkg); use a narrow internal-package fallback unless
	// explicitly disabled.
	if skipFallback {
		return
	}
	cmd := "go test ./internal/..."
	if shouldSkipDerivedCommand(declared, cmd) {
		return
	}
	matched := append([]string(nil), goFiles...)
	sort.Strings(matched)
	result.Checks = append(result.Checks, state.ValidationCheck{
		ID:           declaredCheckID(ticketID, cmd),
		Scope:        state.ValidationScopeTicket,
		Source:       state.ValidationCheckSourceDerived,
		Command:      cmd,
		Reason:       "go test for unclear go package changes; fallback to internal packages",
		MatchedFiles: matched,
		Severity:     state.SeverityP2,
		TicketID:     ticketID,
		Advisory:     true,
	})
}

// goPackageDir returns the repo-relative directory of a Go source file
// when it lives under a conventional Go root (internal, cmd, pkg).
// Returns "" for files outside those roots so the derivation layer stays
// conservative.
func goPackageDir(file string) string {
	dir := path.Dir(file)
	if dir == "" || dir == "." {
		return ""
	}
	root := firstSegment(dir)
	switch root {
	case "internal", "cmd", "pkg":
		return dir
	default:
		return ""
	}
}

func firstSegment(p string) string {
	trimmed := strings.TrimLeft(p, "./")
	if idx := strings.IndexByte(trimmed, '/'); idx >= 0 {
		return trimmed[:idx]
	}
	return trimmed
}

func anyHasSuffix(files []string, suffix string) bool {
	for _, f := range files {
		if strings.HasSuffix(f, suffix) {
			return true
		}
	}
	return false
}

func anyLacksSuffix(files []string, suffix string) bool {
	for _, f := range files {
		if !strings.HasSuffix(f, suffix) {
			return true
		}
	}
	return false
}

// addPythonChecks derives pytest for touched Python test files and
// `ruff check` for all touched Python files when the repo advertises Ruff.
// Missing `ruff` binary with a Ruff-configured repo produces a skipped
// check with the explanation that the optional tool is absent.
func addPythonChecks(result *DeriveChecksResult, ticketID string, files []string, tools ToolSignals, declared map[string]struct{}) {
	pyFiles := filesWithSuffix(files, ".py")
	if len(pyFiles) == 0 {
		return
	}

	testFiles := filterPyTestFiles(pyFiles)
	if len(testFiles) > 0 {
		cmd := "pytest " + strings.Join(testFiles, " ")
		if !shouldSkipDerivedCommand(declared, cmd) {
			result.Checks = append(result.Checks, state.ValidationCheck{
				ID:           declaredCheckID(ticketID, cmd),
				Scope:        state.ValidationScopeTicket,
				Source:       state.ValidationCheckSourceDerived,
				Command:      cmd,
				Reason:       "focused pytest for changed Python test files",
				MatchedFiles: append([]string(nil), testFiles...),
				Severity:     state.SeverityP2,
				TicketID:     ticketID,
				Advisory:     true,
			})
		}
	}

	ruffAdvertised := tools.HasRuffConfig || tools.HasPyproject
	if !ruffAdvertised {
		return
	}
	if tools.HasRuff {
		cmd := "ruff check " + strings.Join(pyFiles, " ")
		if shouldSkipDerivedCommand(declared, cmd) {
			return
		}
		result.Checks = append(result.Checks, state.ValidationCheck{
			ID:           declaredCheckID(ticketID, cmd),
			Scope:        state.ValidationScopeTicket,
			Source:       state.ValidationCheckSourceDerived,
			Command:      cmd,
			Reason:       "ruff check for changed Python files",
			MatchedFiles: append([]string(nil), pyFiles...),
			Severity:     state.SeverityP2,
			TicketID:     ticketID,
			Advisory:     true,
		})
		return
	}
	result.Skipped = append(result.Skipped, state.ValidationCheckSkip{
		CheckID: declaredCheckID(ticketID, "derived-ruff"),
		Reason:  "ruff tool not installed",
		Detail:  "repository advertises Ruff tooling but ruff binary is missing from PATH",
	})
}

// filterPyTestFiles identifies Python files that look like pytest targets
// by file-name convention or path placement under a tests/ directory.
func filterPyTestFiles(files []string) []string {
	var out []string
	for _, f := range files {
		base := path.Base(f)
		if strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py") {
			out = append(out, f)
			continue
		}
		if strings.HasPrefix(f, "tests/") || strings.Contains(f, "/tests/") {
			out = append(out, f)
		}
	}
	return out
}

// addMarkdownChecks derives markdownlint and stale-wording checks for
// markdown changes. markdownlint is emitted only when the tool is
// available; otherwise a skipped check records the missing optional
// linter. The stale-wording check fires only for docs-related tickets and
// only when the caller supplies terms to search for — the derivation
// layer never invents stale wording terms.
func addMarkdownChecks(
	result *DeriveChecksResult,
	plan state.PlanArtifact,
	files []string,
	tools ToolSignals,
	staleTerms []string,
	staleDocs []string,
	declared map[string]struct{},
) {
	mdFiles := filesWithSuffix(files, ".md")
	if len(mdFiles) == 0 {
		return
	}
	ticketID := plan.TicketID

	if tools.HasMarkdownlint {
		cmd := "markdownlint " + strings.Join(mdFiles, " ")
		if !shouldSkipDerivedCommand(declared, cmd) {
			result.Checks = append(result.Checks, state.ValidationCheck{
				ID:           declaredCheckID(ticketID, cmd),
				Scope:        state.ValidationScopeTicket,
				Source:       state.ValidationCheckSourceDerived,
				Command:      cmd,
				Reason:       "markdownlint for changed markdown files",
				MatchedFiles: append([]string(nil), mdFiles...),
				Severity:     state.SeverityP2,
				TicketID:     ticketID,
				Advisory:     true,
			})
		}
	} else {
		result.Skipped = append(result.Skipped, state.ValidationCheckSkip{
			CheckID: declaredCheckID(ticketID, "derived-markdownlint"),
			Reason:  "markdownlint tool not installed",
			Detail:  "optional markdownlint binary missing from PATH",
		})
	}

	docsTicket := ticketMentionsDocs(plan)
	terms := normalizeStaleWordingTerms(staleTerms)
	switch {
	case docsTicket && len(terms) > 0:
		cmd := buildStaleWordingCommand(terms, staleDocs)
		if shouldSkipDerivedCommand(declared, cmd) {
			return
		}
		result.Checks = append(result.Checks, state.ValidationCheck{
			ID:           declaredCheckID(ticketID, cmd),
			Scope:        state.ValidationScopeTicket,
			Source:       state.ValidationCheckSourceDerived,
			Command:      cmd,
			Reason:       "stale wording sweep for docs-related ticket",
			MatchedFiles: append([]string(nil), mdFiles...),
			Severity:     state.SeverityP2,
			TicketID:     ticketID,
			Advisory:     true,
		})
	case docsTicket:
		result.Skipped = append(result.Skipped, state.ValidationCheckSkip{
			CheckID: declaredCheckID(ticketID, "derived-stale-wording"),
			Reason:  "no stale wording terms configured",
			Detail:  "ticket touches documentation but no stale wording terms were supplied",
		})
	}
}

// ticketMentionsDocs returns true when the ticket's title, description, or
// owned paths indicate documentation work. The heuristic is intentionally
// broad so a docs-related ticket that forgot to list stale-wording terms
// still surfaces a skipped check rather than silently ignoring the ask.
func ticketMentionsDocs(plan state.PlanArtifact) bool {
	haystack := strings.ToLower(plan.Title + "\n" + plan.Description)
	keywords := []string{
		"docs", "documentation", "readme", "contributing",
		"stale wording", "scanner wording",
	}
	for _, k := range keywords {
		if strings.Contains(haystack, k) {
			return true
		}
	}
	for _, p := range plan.OwnedPaths {
		clean := strings.ToLower(strings.TrimLeft(filepath.ToSlash(p), "./"))
		if clean == "" {
			continue
		}
		if strings.HasPrefix(clean, "docs") ||
			strings.HasPrefix(clean, "readme") ||
			strings.HasPrefix(clean, "contributing") {
			return true
		}
	}
	return false
}

func normalizeStaleWordingTerms(terms []string) []string {
	var out []string
	seen := make(map[string]struct{})
	for _, t := range terms {
		trimmed := strings.TrimSpace(t)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	sort.Strings(out)
	return out
}

func buildStaleWordingCommand(terms, docPaths []string) string {
	return buildStaleWordingGrepCommand(terms, docPaths, false)
}

// addYAMLChecks derives a yamllint command for touched YAML files. When
// yamllint is not installed, a skipped check records the missing optional
// linter so operators can see why no YAML coverage was derived.
func addYAMLChecks(result *DeriveChecksResult, ticketID string, files []string, tools ToolSignals, declared map[string]struct{}) {
	yamlFiles := yamlFilesOf(files)
	if len(yamlFiles) == 0 {
		return
	}
	if tools.HasYamllint {
		cmd := "yamllint " + strings.Join(yamlFiles, " ")
		if shouldSkipDerivedCommand(declared, cmd) {
			return
		}
		result.Checks = append(result.Checks, state.ValidationCheck{
			ID:           declaredCheckID(ticketID, cmd),
			Scope:        state.ValidationScopeTicket,
			Source:       state.ValidationCheckSourceDerived,
			Command:      cmd,
			Reason:       "yamllint for changed YAML files",
			MatchedFiles: append([]string(nil), yamlFiles...),
			Severity:     state.SeverityP2,
			TicketID:     ticketID,
			Advisory:     true,
		})
		return
	}
	result.Skipped = append(result.Skipped, state.ValidationCheckSkip{
		CheckID: declaredCheckID(ticketID, "derived-yamllint"),
		Reason:  "yamllint tool not installed",
		Detail:  "optional yamllint binary missing from PATH",
	})
}

func yamlFilesOf(files []string) []string {
	var out []string
	for _, f := range files {
		if strings.HasSuffix(f, ".yml") || strings.HasSuffix(f, ".yaml") {
			out = append(out, f)
		}
	}
	return out
}

// addShellChecks derives shellcheck coverage for touched .sh files. When
// shellcheck is not installed, a skipped check records the missing
// optional linter rather than producing a failing check.
func addShellChecks(result *DeriveChecksResult, ticketID string, files []string, tools ToolSignals, declared map[string]struct{}) {
	shFiles := filesWithSuffix(files, ".sh")
	if len(shFiles) == 0 {
		return
	}
	if tools.HasShellcheck {
		cmd := "shellcheck " + strings.Join(shFiles, " ")
		if shouldSkipDerivedCommand(declared, cmd) {
			return
		}
		result.Checks = append(result.Checks, state.ValidationCheck{
			ID:           declaredCheckID(ticketID, cmd),
			Scope:        state.ValidationScopeTicket,
			Source:       state.ValidationCheckSourceDerived,
			Command:      cmd,
			Reason:       "shellcheck for changed shell scripts",
			MatchedFiles: append([]string(nil), shFiles...),
			Severity:     state.SeverityP2,
			TicketID:     ticketID,
			Advisory:     true,
		})
		return
	}
	result.Skipped = append(result.Skipped, state.ValidationCheckSkip{
		CheckID: declaredCheckID(ticketID, "derived-shellcheck"),
		Reason:  "shellcheck tool not installed",
		Detail:  "optional shellcheck binary missing from PATH",
	})
}
