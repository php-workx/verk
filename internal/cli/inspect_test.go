package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTicketMD(t *testing.T, ticketsDir, id, content string) {
	t.Helper()
	if err := os.MkdirAll(ticketsDir, 0o755); err != nil {
		t.Fatalf("mkdir tickets: %v", err)
	}
	path := filepath.Join(ticketsDir, id+".md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write ticket %s: %v", id, err)
	}
}

func runInspectInDir(t *testing.T, dir string, args ...string) (string, string, int) {
	t.Helper()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("get wd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalWD) })

	var stdout, stderr bytes.Buffer
	root := newRootCmd()
	root.SetArgs(args)
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	err = root.Execute()
	code := 0
	if err != nil {
		code = 1
		if e, ok := err.(*cliExitError); ok {
			code = e.ExitCode()
		}
	}
	return stdout.String(), stderr.String(), code
}

func TestInspect_TicketMissingAcceptanceCriteriaBlocks(t *testing.T) {
	dir := t.TempDir()
	initCLITestRepo(t, dir)
	ticketsDir := filepath.Join(dir, ".tickets")
	writeTicketMD(t, ticketsDir, "ver-bad", `---
id: ver-bad
title: Bad ticket
status: ready
owned_paths:
  - internal/widget
---
Bad body.
`)
	stdout, _, code := runInspectInDir(t, dir, "inspect", "ticket", "ver-bad")
	if code == 0 {
		t.Fatalf("expected non-zero exit, got %d. stdout:\n%s", code, stdout)
	}
	if !strings.Contains(stdout, "missing_acceptance_criteria") {
		t.Fatalf("expected missing_acceptance_criteria in output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "blocked") {
		t.Fatalf("expected blocked status in output:\n%s", stdout)
	}
}

func TestInspect_TicketWithCriteriaPasses(t *testing.T) {
	dir := t.TempDir()
	initCLITestRepo(t, dir)
	ticketsDir := filepath.Join(dir, ".tickets")
	writeTicketMD(t, ticketsDir, "ver-ok", `---
id: ver-ok
title: Good ticket
status: ready
owned_paths:
  - internal/widget
acceptance_criteria:
  - "verk widget --enable exits 0 and writes /tmp/widget.log"
---
Good body.
`)
	stdout, _, code := runInspectInDir(t, dir, "inspect", "ticket", "ver-ok")
	if code != 0 {
		t.Fatalf("expected exit 0, got %d. stdout:\n%s", code, stdout)
	}
	if !strings.Contains(stdout, "passed") {
		t.Fatalf("expected passed status in output:\n%s", stdout)
	}
}

func TestInspect_EpicShowsChildFindings(t *testing.T) {
	dir := t.TempDir()
	initCLITestRepo(t, dir)
	ticketsDir := filepath.Join(dir, ".tickets")
	writeTicketMD(t, ticketsDir, "ver-root", `---
id: ver-root
title: Big feature
status: ready
type: epic
owned_paths:
  - internal/cli
deps:
  - ver-child-cli
  - ver-child-docs
---
Epic body.
`)
	writeTicketMD(t, ticketsDir, "ver-child-cli", `---
id: ver-child-cli
title: CLI bits
status: ready
parent: ver-root
owned_paths:
  - internal/cli
acceptance_criteria:
  - "the feature works"
---
CLI body.
`)
	writeTicketMD(t, ticketsDir, "ver-child-docs", `---
id: ver-child-docs
title: Docs bits
status: ready
parent: ver-root
type: docs
owned_paths:
  - docs/feature.md
acceptance_criteria:
  - "docs explain --x"
---
Docs body.
`)
	stdout, _, code := runInspectInDir(t, dir, "inspect", "epic", "ver-root")
	if code == 0 {
		t.Fatalf("expected non-zero exit, got %d. stdout:\n%s", code, stdout)
	}
	if !strings.Contains(stdout, "integration_gap") {
		t.Fatalf("expected integration_gap in output:\n%s", stdout)
	}
}
