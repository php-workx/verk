package epos

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"verk/internal/state"

	eposticket "github.com/php-workx/epos/ticket"
	eposmarkdown "github.com/php-workx/epos/ticket/markdown"
	eposruntime "github.com/php-workx/epos/ticket/runtime"
)

func TestLoadTicket_PreservesBodyExactly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ticket.md")
	body := "# Heading\r\n\nText with --- inside.\n\n## Notes\n- keep me\n"
	data := "---\nid: ticket-1\nstatus: open\nowned_paths:\n  - internal/foo.go\n---\n" + body
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	ticket, err := LoadTicket(path)
	if err != nil {
		t.Fatal(err)
	}
	if ticket.Body != body {
		t.Fatalf("Body = %#v, want %#v", ticket.Body, body)
	}
	if ticket.Title != "Heading" {
		t.Fatalf("Title = %q, want Heading", ticket.Title)
	}
	if ticket.Status != StatusOpen {
		t.Fatalf("Status = %q, want open", ticket.Status)
	}
}

func TestExtractHeadingTitle_ParityWithEpos(t *testing.T) {
	body := "\nnot heading\n# Real Title\n## Later\n"
	title, ok := eposmarkdown.ExtractHeadingTitle(body)
	if !ok {
		t.Fatal("expected epos heading title")
	}
	path := filepath.Join(t.TempDir(), "ticket.md")
	if err := os.WriteFile(path, []byte("---\nid: t\nstatus: open\n---\n"+body), 0o644); err != nil {
		t.Fatal(err)
	}
	ticket, err := LoadTicket(path)
	if err != nil {
		t.Fatal(err)
	}
	if ticket.Title != title {
		t.Fatalf("Title = %q, want %q", ticket.Title, title)
	}
	if !ticket.titleDerived {
		t.Fatal("expected titleDerived to mirror epos")
	}
}

func TestSaveTicket_WritesExactPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "custom", "out.md")
	ticket := Ticket{
		ID:         "ticket-1",
		Title:      "Title",
		Status:     StatusOpen,
		OwnedPaths: []string{"internal/foo.go"},
		Body:       "# Title\n\nbody\n",
		present: map[string]bool{
			"id":          true,
			"title":       true,
			"status":      true,
			"owned_paths": true,
		},
	}
	if err := SaveTicket(path, ticket); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected exact path to exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".tickets", "ticket-1.md")); !os.IsNotExist(err) {
		t.Fatalf("SaveTicket wrote redirected path")
	}
}

func TestSaveTicket_ChangedBodyDoesNotRenderEposSections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ticket.md")
	body := "# Custom\n\nThis body is not generated from epos sections.\n"
	ticket := Ticket{
		ID:     "ticket-1",
		Status: StatusOpen,
		Body:   body,
		present: map[string]bool{
			"id":     true,
			"status": true,
		},
	}
	if err := SaveTicket(path, ticket); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got[len(got)-len(body):] != body {
		t.Fatalf("saved body = %#v, want suffix %#v", got, body)
	}
}

func TestLoadSave_PreservesEposNamedFrontmatterFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ticket.md")
	data := `---
id: ticket-1
status: open
requirement_ids:
  - req-1
source_refs:
  - src-1
lineage_id: lineage-1
risk_level: high
intent: ship safely
constraints:
  - constraint
warnings:
  - warning
read_only_paths:
  - README.md
shared_paths:
  - go.mod
isolation_mode: worktree
files_likely_touched:
  - internal/foo.go
implementation_detail:
  approach: small patch
  files:
    - path: internal/foo.go
      change: edit
      reason: test
  notes: note
learning_context:
  - id: learn-1
    type: doc
    title: Learning
validation_checks:
  - command: go test ./...
    expected: pass
    description: tests
required_evidence:
  - test output
reviewer_guidance: review carefully
etag: etag-1
created_from: fabrikk
grouping_reason: same subsystem
grouped_requirement_ids:
  - req-1
  - req-2
---
# Ticket 1
`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	ticket, err := LoadTicket(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveTicket(path, ticket); err != nil {
		t.Fatal(err)
	}
	roundTripped, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := eposmarkdown.UnmarshalTicket(roundTripped)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(parsed.RequirementIDs, []string{"req-1"}) ||
		!reflect.DeepEqual(parsed.SourceRefs, []string{"src-1"}) ||
		parsed.LineageID != "lineage-1" ||
		parsed.RiskLevel != "high" ||
		parsed.Intent != "ship safely" ||
		!reflect.DeepEqual(parsed.Constraints, []string{"constraint"}) ||
		!reflect.DeepEqual(parsed.Warnings, []string{"warning"}) ||
		!reflect.DeepEqual(parsed.Scope.ReadOnlyPaths, []string{"README.md"}) ||
		!reflect.DeepEqual(parsed.Scope.SharedPaths, []string{"go.mod"}) ||
		parsed.Scope.IsolationMode != "worktree" ||
		!reflect.DeepEqual(parsed.FilesLikelyTouched, []string{"internal/foo.go"}) ||
		!reflect.DeepEqual(parsed.ImplementationDetail, (eposticket.ImplementationDetail{Approach: "small patch", Files: []eposticket.FileChange{{Path: "internal/foo.go", Change: "edit", Reason: "test"}}, Notes: "note"})) ||
		!reflect.DeepEqual(parsed.LearningContext, []eposticket.LearningRef{{ID: "learn-1", Type: "doc", Title: "Learning"}}) ||
		!reflect.DeepEqual(parsed.ValidationChecks, []eposticket.ValidationCheck{{Command: "go test ./...", Expected: "pass", Description: "tests"}}) ||
		!reflect.DeepEqual(parsed.RequiredEvidence, []string{"test output"}) ||
		parsed.ReviewerGuidance != "review carefully" ||
		parsed.ETag != "etag-1" ||
		parsed.CreatedFrom != "fabrikk" ||
		parsed.GroupingReason != "same subsystem" ||
		!reflect.DeepEqual(parsed.GroupedRequirementIDs, []string{"req-1", "req-2"}) {
		t.Fatalf("epos named fields were not preserved after load/save: %#v", parsed)
	}
}

func TestSaveTicket_ConstructedTicketWritesStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ticket.md")
	if err := SaveTicket(path, Ticket{
		ID:     "ticket-1",
		Title:  "Ticket 1",
		Status: StatusOpen,
		Body:   "# Ticket 1\n",
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "status: open\n") {
		t.Fatalf("expected status frontmatter, got:\n%s", data)
	}
}

func TestRejectsGlobOwnedPaths(t *testing.T) {
	err := SaveTicket(filepath.Join(t.TempDir(), "ticket.md"), Ticket{
		ID:         "ticket-1",
		Status:     StatusOpen,
		OwnedPaths: []string{"internal/*.go"},
	})
	if err == nil {
		t.Fatal("expected glob owned path rejection")
	}
}

func TestValidateTicketSchedulingFields_RejectsInvalidStatus(t *testing.T) {
	err := ValidateTicketSchedulingFields(Ticket{
		ID:     "ticket-1",
		Status: "claimed",
	})
	if err == nil {
		t.Fatal("expected invalid status rejection")
	}
}

func TestValidateTicketSchedulingFields_RejectsEmptyStatus(t *testing.T) {
	err := ValidateTicketSchedulingFields(Ticket{
		ID: "ticket-1",
	})
	if err == nil {
		t.Fatal("expected empty status rejection")
	}
}

func TestValidateTicketSchedulingFields_RejectsInvalidOwnedPath(t *testing.T) {
	err := ValidateTicketSchedulingFields(Ticket{
		ID:         "ticket-1",
		Status:     StatusOpen,
		OwnedPaths: []string{"../outside.go"},
	})
	if err == nil {
		t.Fatal("expected invalid owned path rejection")
	}
}

func TestListAllChildren(t *testing.T) {
	dir := t.TempDir()

	writeEposTicketFile(t, dir, "epic-1", map[string]string{"type": "epic"}, []string{"c-duplicate", "a-dep"})
	writeEposTicketFile(t, dir, "a-dep", nil, nil)
	writeEposTicketFile(t, dir, "b-direct", map[string]string{"parent": "epic-1"}, nil)
	writeEposTicketFile(t, dir, "c-duplicate", map[string]string{"parent": "epic-1"}, nil)
	writeEposTicketFile(t, dir, "d-other", map[string]string{"parent": "other-epic"}, nil)

	children, err := ListAllChildren(dir, "epic-1")
	if err != nil {
		t.Fatalf("ListAllChildren: %v", err)
	}
	got := ticketIDs(children)
	want := []string{"a-dep", "b-direct", "c-duplicate"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("children = %v, want %v", got, want)
	}

	children, err = ListAllChildren(dir, "missing-parent")
	if err == nil {
		t.Fatalf("expected missing parent error, got children=%v", children)
	}
	if children != nil {
		t.Fatalf("children on missing parent = %v, want nil", children)
	}
}

func TestHasChildrenViaDeps(t *testing.T) {
	dir := t.TempDir()

	writeEposTicketFile(t, dir, "epic-2", map[string]string{"type": "epic"}, []string{"dep-child-1", "dep-child-2"})
	writeEposTicketFile(t, dir, "dep-child-1", nil, nil)
	writeEposTicketFile(t, dir, "dep-child-2", nil, nil)
	writeEposTicketFile(t, dir, "orphan", nil, nil)

	has, err := HasChildren(dir, "epic-2")
	if err != nil {
		t.Fatalf("HasChildren epic-2: %v", err)
	}
	if !has {
		t.Fatal("expected epic-2 to have children via deps")
	}

	has, err = HasChildren(dir, "orphan")
	if err != nil {
		t.Fatalf("HasChildren orphan: %v", err)
	}
	if has {
		t.Fatal("expected orphan to have no children")
	}
}

func TestNonEpicDepsDoNotCreateChildren(t *testing.T) {
	dir := t.TempDir()

	writeEposTicketFile(t, dir, "task-1", map[string]string{"type": "task"}, []string{"dep-a", "dep-b"})
	writeEposTicketFile(t, dir, "dep-a", nil, nil)
	writeEposTicketFile(t, dir, "dep-b", nil, nil)

	has, err := HasChildren(dir, "task-1")
	if err != nil {
		t.Fatalf("HasChildren: %v", err)
	}
	if has {
		t.Fatal("expected non-epic task deps to remain scheduling edges, not child edges")
	}

	children, err := ListAllChildren(dir, "task-1")
	if err != nil {
		t.Fatalf("ListAllChildren: %v", err)
	}
	if len(children) != 0 {
		t.Fatalf("children = %v, want none", ticketIDs(children))
	}
}

func TestListReadyChildren_IncludesStatusReady(t *testing.T) {
	dir := t.TempDir()

	writeEposTicketFile(t, dir, "epic-ready", map[string]string{"type": "epic"}, []string{"child-open"})
	writeEposTicketFileWithStatus(t, dir, "child-open", "open", map[string]string{"parent": "epic-ready"}, nil)
	writeEposTicketFileWithStatus(t, dir, "child-ready", "ready", map[string]string{"parent": "epic-ready"}, nil)
	writeEposTicketFileWithStatus(t, dir, "child-closed", "closed", map[string]string{"parent": "epic-ready"}, nil)

	children, err := ListReadyChildren(dir, "epic-ready")
	if err != nil {
		t.Fatalf("ListReadyChildren: %v", err)
	}
	got := strings.Join(ticketIDs(children), ",")
	want := "child-open,child-ready"
	if got != want {
		t.Fatalf("ready children = %s, want %s", got, want)
	}
}

func TestListReadyChildren_RequiresClosedDeps(t *testing.T) {
	dir := t.TempDir()

	writeEposTicketFile(t, dir, "epic-deps", map[string]string{"type": "epic"}, nil)
	writeEposTicketFileWithStatus(t, dir, "dep-closed", "closed", nil, nil)
	writeEposTicketFileWithStatus(t, dir, "dep-done", "done", nil, nil)
	writeEposTicketFileWithStatus(t, dir, "dep-open", "open", nil, nil)
	writeEposTicketFileWithStatus(t, dir, "ready-closed-dep", "open", map[string]string{"parent": "epic-deps"}, []string{"dep-closed"})
	writeEposTicketFileWithStatus(t, dir, "ready-done-dep", "open", map[string]string{"parent": "epic-deps"}, []string{"dep-done"})
	writeEposTicketFileWithStatus(t, dir, "blocked-open-dep", "open", map[string]string{"parent": "epic-deps"}, []string{"dep-open"})
	writeEposTicketFileWithStatus(t, dir, "blocked-missing-dep", "open", map[string]string{"parent": "epic-deps"}, []string{"dep-missing"})

	children, err := ListReadyChildren(dir, "epic-deps")
	if err != nil {
		t.Fatalf("ListReadyChildren: %v", err)
	}
	got := strings.Join(ticketIDs(children), ",")
	want := "ready-closed-dep,ready-done-dep"
	if got != want {
		t.Fatalf("ready children = %s, want %s", got, want)
	}
}

func TestUsesCanonicalReadinessPredicate(t *testing.T) {
	dir := t.TempDir()

	writeEposTicketFile(t, dir, "epic-canonical", map[string]string{"type": "epic"}, []string{"dep-child"})
	writeEposTicketFileWithStatus(t, dir, "dep-failed", "failed", nil, nil)
	writeEposTicketFileWithStatus(t, dir, "dep-child", "pending", nil, []string{"dep-failed"})
	writeEposTicketFileWithStatus(t, dir, "parent-child", "repair_pending", map[string]string{"parent": "epic-canonical"}, nil)
	writeEposTicketFileWithStatus(t, dir, "busy-child", "claimed", map[string]string{"parent": "epic-canonical"}, nil)
	writeEposTicketFileWithStatus(t, dir, "held-child", "held", map[string]string{"parent": "epic-canonical"}, nil)

	children, err := ListReadyChildren(dir, "epic-canonical", "run-current")
	if err != nil {
		t.Fatalf("ListReadyChildren: %v", err)
	}
	got := strings.Join(ticketIDs(children), ",")
	want := "dep-child,parent-child"
	if got != want {
		t.Fatalf("ready children = %s, want %s", got, want)
	}
}

func TestListReadyChildren_DurableClaimFiltering(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()

	writeEposTicketFile(t, dir, "epic-claims", map[string]string{"type": "epic"}, nil)
	writeEposTicketFileWithStatus(t, dir, "durable-active", "open", map[string]string{"parent": "epic-claims"}, nil)
	writeEposTicketFileWithStatus(t, dir, "durable-released", "open", map[string]string{"parent": "epic-claims"}, nil)
	writeEposTicketFileWithStatus(t, dir, "durable-expired", "open", map[string]string{"parent": "epic-claims"}, nil)
	writeDurableClaim(t, dir, "run-other", state.ClaimArtifact{
		TicketID:   "durable-active",
		OwnerRunID: "run-other",
		LeaseID:    "lease-active",
		LeasedAt:   now,
		ExpiresAt:  now.Add(time.Hour),
		State:      "active",
	})
	writeDurableClaim(t, dir, "run-other", state.ClaimArtifact{
		TicketID:      "durable-released",
		OwnerRunID:    "run-other",
		LeaseID:       "lease-released",
		LeasedAt:      now,
		ExpiresAt:     now.Add(time.Hour),
		State:         "released",
		ReleasedAt:    now,
		ReleaseReason: "done",
	})
	writeDurableClaim(t, dir, "run-other", state.ClaimArtifact{
		TicketID:   "durable-expired",
		OwnerRunID: "run-other",
		LeaseID:    "lease-expired",
		LeasedAt:   now.Add(-2 * time.Hour),
		ExpiresAt:  now.Add(-time.Hour),
		State:      "active",
	})

	children, err := ListReadyChildren(dir, "epic-claims")
	if err != nil {
		t.Fatalf("ListReadyChildren: %v", err)
	}
	got := strings.Join(ticketIDs(children), ",")
	want := "durable-expired,durable-released"
	if got != want {
		t.Fatalf("ready children = %s, want %s", got, want)
	}
}

func TestListReadyChildren_LiveClaimFilteringUsesActiveClaimSet(t *testing.T) {
	dir := t.TempDir()

	writeEposTicketFile(t, dir, "epic-live", map[string]string{"type": "epic"}, nil)
	writeEposTicketFileWithStatus(t, dir, "live-claimed", "open", map[string]string{"parent": "epic-live"}, nil)
	writeEposTicketFileWithStatus(t, dir, "live-open", "open", map[string]string{"parent": "epic-live"}, nil)
	if err := eposruntime.Claim(dir, "live-claimed", "run-other", "backend-other", time.Hour, eposruntime.WithLeaseID("lease-live")); err != nil {
		t.Fatalf("eposruntime.Claim: %v", err)
	}

	children, err := ListReadyChildren(dir, "epic-live")
	if err != nil {
		t.Fatalf("ListReadyChildren: %v", err)
	}
	got := strings.Join(ticketIDs(children), ",")
	want := "live-open"
	if got != want {
		t.Fatalf("ready children = %s, want %s", got, want)
	}
}

func TestListReadyChildren_CurrentRunSeesOwnClaim(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()

	writeEposTicketFile(t, dir, "epic-own", map[string]string{"type": "epic"}, nil)
	writeEposTicketFileWithStatus(t, dir, "durable-own", "open", map[string]string{"parent": "epic-own"}, nil)
	writeEposTicketFileWithStatus(t, dir, "live-own", "open", map[string]string{"parent": "epic-own"}, nil)
	writeDurableClaim(t, dir, "run-current", state.ClaimArtifact{
		TicketID:   "durable-own",
		OwnerRunID: "run-current",
		LeaseID:    "lease-durable-own",
		LeasedAt:   now,
		ExpiresAt:  now.Add(time.Hour),
		State:      "active",
	})
	if err := eposruntime.Claim(dir, "live-own", "run-current", "backend-current", time.Hour, eposruntime.WithLeaseID("lease-live-own")); err != nil {
		t.Fatalf("eposruntime.Claim: %v", err)
	}

	children, err := ListReadyChildren(dir, "epic-own", "run-current")
	if err != nil {
		t.Fatalf("ListReadyChildren: %v", err)
	}
	got := strings.Join(ticketIDs(children), ",")
	want := "durable-own,live-own"
	if got != want {
		t.Fatalf("ready children = %s, want %s", got, want)
	}
}

func TestDetectEpicCycleDetected(t *testing.T) {
	ancestors := map[string]struct{}{
		"epic-root": {},
		"epic-mid":  {},
	}
	err := DetectEpicCycle("epic-root", ancestors)
	if err == nil {
		t.Fatal("expected ErrEpicCycle, got nil")
	}
	if !errors.Is(err, ErrEpicCycle) {
		t.Fatalf("expected errors.Is(err, ErrEpicCycle), got: %v", err)
	}
}

func TestDetectEpicCycleAllowsNewAncestor(t *testing.T) {
	ancestors := map[string]struct{}{
		"epic-root":   {},
		"epic-parent": {},
	}
	if err := DetectEpicCycle("epic-child", ancestors); err != nil {
		t.Fatalf("expected no cycle, got: %v", err)
	}
	if err := DetectEpicCycle("any-epic", nil); err != nil {
		t.Fatalf("expected no cycle with nil ancestors, got: %v", err)
	}
}

func writeEposTicketFile(t *testing.T, rootDir, id string, fields map[string]string, deps []string) {
	t.Helper()
	writeEposTicketFileWithStatus(t, rootDir, id, "open", fields, deps)
}

func writeEposTicketFileWithStatus(t *testing.T, rootDir, id, status string, fields map[string]string, deps []string) {
	t.Helper()
	ticketsDir := filepath.Join(rootDir, ".tickets")
	if err := os.MkdirAll(ticketsDir, 0o755); err != nil {
		t.Fatalf("mkdir .tickets: %v", err)
	}

	lines := []string{
		"---",
		"id: " + id,
		"status: " + status,
	}
	if parent := fields["parent"]; parent != "" {
		lines = append(lines, "parent: "+parent)
	}
	if ticketType := fields["type"]; ticketType != "" {
		lines = append(lines, "type: "+ticketType)
	}
	if len(deps) > 0 {
		lines = append(lines, "deps: ["+strings.Join(deps, ", ")+"]")
	}
	lines = append(lines, "---", "", "# "+id, "")

	path := filepath.Join(ticketsDir, id+".md")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatalf("write %s: %v", id, err)
	}
}

func writeDurableClaim(t *testing.T, rootDir, runID string, claim state.ClaimArtifact) {
	t.Helper()
	if claim.SchemaVersion == 0 {
		claim.SchemaVersion = claimSchemaVersion
	}
	if claim.RunID == "" {
		claim.RunID = runID
	}
	if claim.CreatedAt.IsZero() {
		claim.CreatedAt = claim.LeasedAt
	}
	if claim.UpdatedAt.IsZero() {
		claim.UpdatedAt = claim.CreatedAt
	}
	path := filepath.Join(rootDir, ".verk", "runs", runID, "claims", "claim-"+claim.TicketID+".json")
	if err := state.SaveJSONAtomic(path, claim); err != nil {
		t.Fatalf("write durable claim %s: %v", claim.TicketID, err)
	}
}

func ticketIDs(tickets []Ticket) []string {
	ids := make([]string, 0, len(tickets))
	for _, ticket := range tickets {
		ids = append(ids, ticket.ID)
	}
	return ids
}
