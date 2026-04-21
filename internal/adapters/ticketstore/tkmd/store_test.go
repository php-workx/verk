package tkmd

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRoundTripsUnknownFrontmatter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ticket.md")
	original := strings.Join([]string{
		"---",
		"id: tk-1",
		"title: \"Round trip\"",
		"status: ready",
		"deps: [dep-1]",
		"priority: 2",
		"parent: epic-1",
		"tags: [verk, v1]",
		"custom_flag: true",
		"---",
		"",
		"Body text.",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("seed ticket: %v", err)
	}

	ticket, err := LoadTicket(path)
	if err != nil {
		t.Fatalf("LoadTicket: %v", err)
	}
	if got := ticket.UnknownFrontmatter["parent"]; got != "epic-1" {
		t.Fatalf("expected parent frontmatter to round-trip, got %#v", got)
	}
	if got := ticket.UnknownFrontmatter["tags"]; len(asStringSlice(got)) != 2 {
		t.Fatalf("expected tags frontmatter, got %#v", got)
	}

	outPath := filepath.Join(dir, "roundtrip.md")
	if err := SaveTicket(outPath, ticket); err != nil {
		t.Fatalf("SaveTicket: %v", err)
	}

	roundTripped, err := LoadTicket(outPath)
	if err != nil {
		t.Fatalf("LoadTicket roundtrip: %v", err)
	}
	if roundTripped.Body != ticket.Body {
		t.Fatalf("body changed across round-trip")
	}
	if roundTripped.UnknownFrontmatter["parent"] != "epic-1" {
		t.Fatalf("unknown frontmatter lost across round-trip")
	}
}

func TestPreservesBodyExactly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ticket.md")
	body := "# Heading\n\nParagraph line.\n\n- item 1\n- item 2\n---\nnot frontmatter\n"
	content := "---\nid: tk-2\nstatus: ready\n---\n" + body
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("seed ticket: %v", err)
	}

	ticket, err := LoadTicket(path)
	if err != nil {
		t.Fatalf("LoadTicket: %v", err)
	}
	if ticket.Body != body {
		t.Fatalf("body mismatch\nwant: %q\ngot:  %q", body, ticket.Body)
	}

	outPath := filepath.Join(dir, "saved.md")
	if err := SaveTicket(outPath, ticket); err != nil {
		t.Fatalf("SaveTicket: %v", err)
	}
	saved, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read saved ticket: %v", err)
	}
	if !strings.HasSuffix(string(saved), body) {
		t.Fatalf("saved file body suffix changed")
	}
}

func TestRejectsGlobOwnedPaths(t *testing.T) {
	err := ValidateTicketSchedulingFields(Ticket{
		ID:         "tk-3",
		Status:     StatusReady,
		OwnedPaths: []string{"internal/**/*.go"},
	})
	if err == nil {
		t.Fatal("expected glob owned_paths to be rejected")
	}
}

func TestUsesCanonicalReadinessPredicate(t *testing.T) {
	dir := t.TempDir()
	ticketsDir := filepath.Join(dir, ".tickets")
	if err := os.MkdirAll(filepath.Join(ticketsDir, ".claims"), 0o755); err != nil {
		t.Fatalf("mkdir tickets: %v", err)
	}

	writeTicket := func(id, parent, status string, deps []string) {
		t.Helper()
		ticket := Ticket{
			ID:         id,
			Title:      id,
			Status:     Status(status),
			Deps:       deps,
			OwnedPaths: []string{"internal/" + id},
			Body:       "body\n",
			UnknownFrontmatter: map[string]any{
				"parent": parent,
			},
		}
		if err := SaveTicket(filepath.Join(ticketsDir, id+".md"), ticket); err != nil {
			t.Fatalf("SaveTicket %s: %v", id, err)
		}
	}

	writeTicket("dep-closed", "root-1", string(StatusClosed), nil)
	writeTicket("dep-open", "root-1", string(StatusOpen), nil)
	writeTicket("ready-unclaimed", "root-1", string(StatusReady), []string{"dep-closed"})
	writeTicket("ready-same-run", "root-1", string(StatusReady), []string{"dep-closed"})
	writeTicket("ready-other-run", "root-1", string(StatusReady), []string{"dep-closed"})
	writeTicket("blocked-by-status", "root-1", string(StatusInProgress), []string{"dep-closed"})
	writeTicket("blocked-by-dep", "root-1", string(StatusReady), []string{"dep-open"})

	now := time.Now().UTC()
	activeClaim := func(ticketID, ownerRunID string) {
		t.Helper()
		claim := claimRecord{
			TicketID:   ticketID,
			OwnerRunID: ownerRunID,
			LeaseID:    "lease-" + ticketID,
			ExpiresAt:  now.Add(time.Hour),
			State:      "active",
		}
		data, err := json.Marshal(claim)
		if err != nil {
			t.Fatalf("marshal claim: %v", err)
		}
		if err := os.WriteFile(filepath.Join(ticketsDir, ".claims", ticketID+".json"), data, 0o644); err != nil {
			t.Fatalf("write claim: %v", err)
		}
	}

	activeClaim("ready-same-run", "run-current")
	activeClaim("ready-other-run", "run-other")

	got, err := ListReadyChildren(dir, "root-1", "run-current")
	if err != nil {
		t.Fatalf("ListReadyChildren: %v", err)
	}

	names := make([]string, 0, len(got))
	for _, ticket := range got {
		names = append(names, ticket.ID)
	}

	want := map[string]struct{}{
		"dep-open":        {}, // open tickets with resolved deps are now schedulable
		"ready-unclaimed": {},
		"ready-same-run":  {},
	}
	if len(names) != len(want) {
		t.Fatalf("unexpected ready set: %v", names)
	}
	for _, name := range names {
		if _, ok := want[name]; !ok {
			t.Fatalf("unexpected ready ticket %q in %v", name, names)
		}
	}
}

func TestClaimAllowsReady_RejectsPathTraversalTicketID(t *testing.T) {
	dir := t.TempDir()
	ticketsDir := filepath.Join(dir, ".tickets")
	if err := os.MkdirAll(filepath.Join(ticketsDir, ".claims"), 0o755); err != nil {
		t.Fatalf("mkdir .claims: %v", err)
	}

	maliciousIDs := []string{
		"../escape",
		"..",
		".",
		"ticket/evil",
		"ticket\\evil",
		"/tmp/hijack",
		"foo/../bar",
	}

	for _, ticketID := range maliciousIDs {
		t.Run(ticketID, func(t *testing.T) {
			_, err := claimAllowsReady(ticketsDir, ticketID, "run-current")
			if err == nil {
				t.Fatalf("expected ticket id %q to be rejected", ticketID)
			}
		})
	}
}

func TestLoadEpicChildrenMalformed(t *testing.T) {
	dir := t.TempDir()
	// A frontmatter line without a colon causes splitKeyValue to return an error.
	epicPath := filepath.Join(dir, "epic-bad.md")
	malformed := "---\nno_colon_here\n---\nbody\n"
	if err := os.WriteFile(epicPath, []byte(malformed), 0o644); err != nil {
		t.Fatalf("write malformed epic: %v", err)
	}

	children, err := loadEpicChildren(dir, "epic-bad")
	if err == nil {
		t.Fatal("expected non-nil error for malformed epic, got nil")
	}
	if len(children) != 0 {
		t.Fatalf("expected zero children on parse failure, got %d", len(children))
	}
}

func TestListReadyChildren_PropagatesMalformedEpicError(t *testing.T) {
	dir := t.TempDir()
	ticketsDir := filepath.Join(dir, ".tickets")
	if err := os.MkdirAll(ticketsDir, 0o755); err != nil {
		t.Fatalf("mkdir .tickets: %v", err)
	}

	epicPath := filepath.Join(ticketsDir, "epic-bad.md")
	malformed := "---\nno_colon_here\n---\nEpic body.\n"
	if err := os.WriteFile(epicPath, []byte(malformed), 0o644); err != nil {
		t.Fatalf("write malformed epic: %v", err)
	}
	childPath := filepath.Join(ticketsDir, "child-1.md")
	child := strings.Join([]string{
		"---",
		"id: child-1",
		"parent: epic-bad",
		"status: open",
		"---",
		"",
		"Child body.",
		"",
	}, "\n")
	if err := os.WriteFile(childPath, []byte(child), 0o644); err != nil {
		t.Fatalf("write child ticket: %v", err)
	}

	children, err := ListReadyChildren(dir, "epic-bad")
	if err == nil {
		t.Fatalf("expected error from ListReadyChildren for malformed epic, got nil and children=%v", children)
	}
}

func TestLoadEpicChildrenValid(t *testing.T) {
	dir := t.TempDir()
	epicPath := filepath.Join(dir, "epic-ok.md")
	content := strings.Join([]string{
		"---",
		"id: epic-ok",
		"title: \"Test Epic\"",
		"status: open",
		"deps: [child-1, child-2]",
		"---",
		"",
		"Epic body.",
		"",
	}, "\n")
	if err := os.WriteFile(epicPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write epic: %v", err)
	}

	children, err := loadEpicChildren(dir, "epic-ok")
	if err != nil {
		t.Fatalf("expected nil error for valid epic, got: %v", err)
	}
	if len(children) != 2 {
		t.Fatalf("expected 2 children, got %d: %v", len(children), children)
	}
	for _, id := range []string{"child-1", "child-2"} {
		if _, ok := children[id]; !ok {
			t.Fatalf("expected child %q in map, got: %v", id, children)
		}
	}
}

func TestRoundTripNoTitleInFrontmatter(t *testing.T) {
	// A ticket whose title comes from the # heading (not frontmatter)
	// must produce identical content after a load-save round-trip.
	dir := t.TempDir()
	path := filepath.Join(dir, "derived-title.md")
	original := "---\nid: tk-dt\nstatus: open\n---\n# Derived Title\n\nSome body text.\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("seed ticket: %v", err)
	}

	ticket, err := LoadTicket(path)
	if err != nil {
		t.Fatalf("LoadTicket: %v", err)
	}
	if ticket.Title != "Derived Title" {
		t.Fatalf("expected title from heading, got %q", ticket.Title)
	}
	if !ticket.titleDerived {
		t.Fatal("expected titleDerived to be true when title comes from heading")
	}

	outPath := filepath.Join(dir, "roundtrip.md")
	if err := SaveTicket(outPath, ticket); err != nil {
		t.Fatalf("SaveTicket: %v", err)
	}

	saved, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read saved ticket: %v", err)
	}
	if string(saved) != original {
		t.Fatalf("round-trip mismatch\nwant:\n%s\ngot:\n%s", original, string(saved))
	}
}

func writeTicketToRepo(t *testing.T, repoRoot, id, parent string, status Status, deps ...string) {
	t.Helper()
	ticketsDir := filepath.Join(repoRoot, ".tickets")
	parts := []string{"---"}
	parts = append(parts, "id: "+id)
	if parent != "" {
		parts = append(parts, "parent: "+parent)
	}
	parts = append(parts, "status: "+string(status))
	if len(deps) > 0 {
		parts = append(parts, "deps: ["+strings.Join(deps, ", ")+"]")
	}
	parts = append(parts, "---", "", "Body for "+id+".", "")
	path := filepath.Join(ticketsDir, id+".md")
	if err := os.MkdirAll(ticketsDir, 0o755); err != nil {
		t.Fatalf("mkdir .tickets: %v", err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(parts, "\n")), 0o644); err != nil {
		t.Fatalf("write ticket %s: %v", id, err)
	}
}

func TestHasChildren(t *testing.T) {
	dir := t.TempDir()

	writeTicketToRepo(t, dir, "epic-1", "", StatusOpen)
	writeTicketToRepo(t, dir, "child-1", "epic-1", StatusOpen)
	writeTicketToRepo(t, dir, "child-2", "epic-1", StatusOpen)
	writeTicketToRepo(t, dir, "orphan-1", "", StatusOpen)

	has, err := HasChildren(dir, "epic-1")
	if err != nil {
		t.Fatalf("HasChildren: %v", err)
	}
	if !has {
		t.Fatal("expected epic-1 to have children")
	}

	has, err = HasChildren(dir, "orphan-1")
	if err != nil {
		t.Fatalf("HasChildren: %v", err)
	}
	if has {
		t.Fatal("expected orphan-1 to have no children")
	}
}

func TestListAllChildren(t *testing.T) {
	dir := t.TempDir()

	writeTicketToRepo(t, dir, "epic-1", "", StatusOpen)
	writeTicketToRepo(t, dir, "child-1", "epic-1", StatusOpen)
	writeTicketToRepo(t, dir, "child-2", "epic-1", StatusClosed)
	writeTicketToRepo(t, dir, "child-3", "epic-1", StatusBlocked)
	writeTicketToRepo(t, dir, "other-1", "other-epic", StatusOpen)

	children, err := ListAllChildren(dir, "epic-1")
	if err != nil {
		t.Fatalf("ListAllChildren: %v", err)
	}
	if len(children) != 3 {
		t.Fatalf("expected 3 children, got %d", len(children))
	}

	// ListAllChildren returns all statuses unlike ListReadyChildren
	ids := make(map[string]bool)
	for _, c := range children {
		ids[c.ID] = true
	}
	for _, id := range []string{"child-1", "child-2", "child-3"} {
		if !ids[id] {
			t.Errorf("expected child %q in results", id)
		}
	}
}

func TestListAllChildren_MissingParent(t *testing.T) {
	dir := t.TempDir()

	// Seed the repo with unrelated tickets so the .tickets directory exists and
	// the glob has something to find, but the parent we query does not.
	writeTicketToRepo(t, dir, "unrelated-1", "", StatusOpen)
	writeTicketToRepo(t, dir, "unrelated-2", "some-other-parent", StatusOpen)

	missingID := "nonexistent-epic"
	children, err := ListAllChildren(dir, missingID)
	if err == nil {
		t.Fatalf("expected non-nil error for missing parent %q, got children=%v", missingID, children)
	}
	if children != nil {
		t.Fatalf("expected nil children slice on error, got %v", children)
	}
	if !strings.Contains(err.Error(), missingID) {
		t.Fatalf("expected error message to reference missing parent %q, got: %v", missingID, err)
	}
}

func TestHasChildrenViaDeps(t *testing.T) {
	dir := t.TempDir()

	// Epic with deps that reference other tickets
	ticketsDir := filepath.Join(dir, ".tickets")
	if err := os.MkdirAll(ticketsDir, 0o755); err != nil {
		t.Fatalf("mkdir .tickets: %v", err)
	}
	epicPath := filepath.Join(ticketsDir, "epic-2.md")
	content := strings.Join([]string{
		"---",
		"id: epic-2",
		"status: open",
		"deps: [dep-child-1, dep-child-2]",
		"---",
		"",
		"Epic body.",
		"",
	}, "\n")
	if err := os.WriteFile(epicPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write epic: %v", err)
	}
	writeTicketToRepo(t, dir, "dep-child-1", "", StatusOpen)
	writeTicketToRepo(t, dir, "dep-child-2", "", StatusOpen)

	has, err := HasChildren(dir, "epic-2")
	if err != nil {
		t.Fatalf("HasChildren: %v", err)
	}
	if !has {
		t.Fatal("expected epic-2 to have children via deps")
	}
}

// Regression: peer tk links between siblings must not be treated as child edges.
// Running the parent epic must not see either sibling as a child of the other.
func TestLinkedSiblingsNotChildrenOfEachOther(t *testing.T) {
	dir := t.TempDir()
	ticketsDir := filepath.Join(dir, ".tickets")
	if err := os.MkdirAll(ticketsDir, 0o755); err != nil {
		t.Fatalf("mkdir .tickets: %v", err)
	}

	// Parent epic
	writeTicketToRepo(t, dir, "par-1", "", StatusOpen)

	// Two sibling tickets that reference each other via links
	writeLinkedTicket := func(id, parent, linkedTo string) {
		t.Helper()
		content := strings.Join([]string{
			"---",
			"id: " + id,
			"parent: " + parent,
			"status: open",
			"links: [" + linkedTo + "]",
			"---",
			"",
			"Body for " + id + ".",
			"",
		}, "\n")
		path := filepath.Join(ticketsDir, id+".md")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write ticket %s: %v", id, err)
		}
	}
	writeLinkedTicket("sib-a", "par-1", "sib-b")
	writeLinkedTicket("sib-b", "par-1", "sib-a")

	// Both siblings should be children of par-1 (via parent field).
	children, err := ListAllChildren(dir, "par-1")
	if err != nil {
		t.Fatalf("ListAllChildren par-1: %v", err)
	}
	if len(children) != 2 {
		t.Fatalf("expected 2 children of par-1, got %d", len(children))
	}

	// Neither sibling should see the other as its child.
	for _, sib := range []string{"sib-a", "sib-b"} {
		sibChildren, err := ListAllChildren(dir, sib)
		if err != nil {
			t.Fatalf("ListAllChildren %s: %v", sib, err)
		}
		if len(sibChildren) != 0 {
			ids := make([]string, 0, len(sibChildren))
			for _, c := range sibChildren {
				ids = append(ids, c.ID)
			}
			t.Errorf("sibling %s should have no children, got: %v", sib, ids)
		}
		has, err := HasChildren(dir, sib)
		if err != nil {
			t.Fatalf("HasChildren %s: %v", sib, err)
		}
		if has {
			t.Errorf("HasChildren(%s) should be false; linked siblings must not be epic children", sib)
		}
	}
}

func TestDetectEpicCycleNoCycle(t *testing.T) {
	ancestors := map[string]struct{}{
		"epic-root":   {},
		"epic-parent": {},
	}
	if err := DetectEpicCycle("epic-child", ancestors); err != nil {
		t.Fatalf("expected no cycle, got: %v", err)
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

func TestDetectEpicCycleNilAncestors(t *testing.T) {
	// nil ancestors map means no ancestors; any epicID is safe.
	if err := DetectEpicCycle("any-epic", nil); err != nil {
		t.Fatalf("expected no cycle with nil ancestors, got: %v", err)
	}
}
