package epos

import (
	"reflect"
	"testing"

	eposticket "github.com/php-workx/epos/ticket"
)

func TestTicketTypeShape(t *testing.T) {
	got := reflect.TypeOf(Ticket{})
	wantFields := []struct {
		name string
		typ  string
	}{
		{"ID", "string"},
		{"Title", "string"},
		{"Status", "epos.Status"},
		{"Deps", "[]string"},
		{"Priority", "int"},
		{"AcceptanceCriteria", "[]string"},
		{"TestCases", "[]string"},
		{"ValidationCommands", "[]string"},
		{"OwnedPaths", "[]string"},
		{"ReviewThreshold", "string"},
		{"Runtime", "string"},
		{"Model", "string"},
		{"Body", "string"},
		{"UnknownFrontmatter", "map[string]interface {}"},
		{"present", "map[string]bool"},
		{"titleDerived", "bool"},
	}
	for _, want := range wantFields {
		field, ok := got.FieldByName(want.name)
		if !ok {
			t.Fatalf("missing field %s", want.name)
		}
		if field.Type.String() != want.typ {
			t.Fatalf("field %s type = %s, want %s", want.name, field.Type, want.typ)
		}
	}
	if got.NumField() != len(wantFields) {
		t.Fatalf("Ticket field count = %d, want %d", got.NumField(), len(wantFields))
	}

	if StatusOpen != "open" ||
		StatusReady != "ready" ||
		StatusInProgress != "in_progress" ||
		StatusBlocked != "blocked" ||
		StatusClosed != "closed" {
		t.Fatalf("status constants do not match tkmd values")
	}
}

func TestConvert_RoundTripCoreFields(t *testing.T) {
	in := Ticket{
		ID:                 "ticket-1",
		Title:              "Do thing",
		Status:             StatusReady,
		Deps:               []string{"dep-1", "dep-2"},
		Priority:           4,
		AcceptanceCriteria: []string{"ac"},
		TestCases:          []string{"test"},
		ValidationCommands: []string{"go test ./..."},
		OwnedPaths:         []string{"internal/foo.go"},
		ReviewThreshold:    "strict",
		Runtime:            "codex",
		present:            map[string]bool{"title": true},
		titleDerived:       true,
	}
	got := fromEpos(toEpos(in))
	if !reflect.DeepEqual(got.Deps, in.Deps) ||
		!reflect.DeepEqual(got.AcceptanceCriteria, in.AcceptanceCriteria) ||
		!reflect.DeepEqual(got.TestCases, in.TestCases) ||
		!reflect.DeepEqual(got.ValidationCommands, in.ValidationCommands) ||
		!reflect.DeepEqual(got.OwnedPaths, in.OwnedPaths) {
		t.Fatalf("slice fields did not round-trip: %#v", got)
	}
	if got.ID != in.ID ||
		got.Title != in.Title ||
		got.Status != in.Status ||
		got.Priority != in.Priority ||
		got.ReviewThreshold != in.ReviewThreshold ||
		got.Runtime != in.Runtime ||
		!got.titleDerived ||
		!got.present["title"] {
		t.Fatalf("core fields did not round-trip: %#v", got)
	}
}

func TestConvert_NamedFieldsToUnknownFrontmatter(t *testing.T) {
	in := &eposticket.Ticket{
		ID:             "ticket-1",
		Title:          "Do thing",
		Status:         eposticket.StatusPending,
		Parent:         "epic-1",
		Type:           "task",
		ExtendedStatus: "pending",
		Created:        "2026-05-08T00:00:00Z",
		UpdatedAt:      "2026-05-08T01:00:00Z",
		Order:          12,
		Extra:          map[string]any{"custom": "value"},
		Present: map[string]bool{
			"parent": true, "type": true, "extended_status": true,
			"created": true, "updated_at": true, "order": true,
		},
	}
	got := fromEpos(in)
	for key, want := range map[string]any{
		"parent":          "epic-1",
		"type":            "task",
		"extended_status": "pending",
		"created":         "2026-05-08T00:00:00Z",
		"updated_at":      "2026-05-08T01:00:00Z",
		"order":           12,
		"custom":          "value",
	} {
		if !reflect.DeepEqual(got.UnknownFrontmatter[key], want) {
			t.Fatalf("UnknownFrontmatter[%s] = %#v, want %#v", key, got.UnknownFrontmatter[key], want)
		}
	}
	back := toEpos(got)
	if back.Parent != "epic-1" || back.Type != "task" || back.ExtendedStatus != "pending" ||
		back.Created != "2026-05-08T00:00:00Z" || back.UpdatedAt != "2026-05-08T01:00:00Z" ||
		back.Order != 12 || back.Extra["custom"] != "value" {
		t.Fatalf("named fields not restored: %#v", back)
	}
	if _, duplicated := back.Extra["parent"]; duplicated {
		t.Fatalf("parent duplicated into Extra: %#v", back.Extra)
	}
}

func TestConvert_ModelExtraRoundTrip(t *testing.T) {
	in := Ticket{ID: "ticket-1", Model: "gpt-5", UnknownFrontmatter: map[string]any{"other": "x"}}
	ep := toEpos(in)
	if ep.Extra["model"] != "gpt-5" {
		t.Fatalf("Extra[model] = %#v", ep.Extra["model"])
	}
	got := fromEpos(ep)
	if got.Model != "gpt-5" || got.UnknownFrontmatter["model"] != "gpt-5" {
		t.Fatalf("model did not round-trip: %#v", got)
	}
}

func TestConvert_EposNamedTagsAndDescriptionRoundTrip(t *testing.T) {
	in := &eposticket.Ticket{
		ID:          "ticket-1",
		Tags:        []string{"epos", "verk"},
		Description: "desc",
		Present:     map[string]bool{"tags": true, "description": true},
	}
	got := fromEpos(in)
	if !reflect.DeepEqual(got.UnknownFrontmatter["tags"], []string{"epos", "verk"}) {
		t.Fatalf("tags not bridged: %#v", got.UnknownFrontmatter["tags"])
	}
	if got.UnknownFrontmatter["description"] != "desc" {
		t.Fatalf("description not bridged: %#v", got.UnknownFrontmatter["description"])
	}
	back := toEpos(got)
	if !reflect.DeepEqual(back.Tags, []string{"epos", "verk"}) || back.Description != "desc" {
		t.Fatalf("named fields not restored: %#v", back)
	}
}

func TestConvert_AllEposNamedFieldsRoundTrip(t *testing.T) {
	in := &eposticket.Ticket{
		ID:                    "ticket-1",
		RequirementIDs:        []string{"req-1"},
		SourceRefs:            []string{"src-1"},
		LineageID:             "lineage-1",
		RiskLevel:             "high",
		Intent:                "ship safely",
		Constraints:           []string{"constraint"},
		Warnings:              []string{"warning"},
		Scope:                 eposticket.TaskScope{ReadOnlyPaths: []string{"README.md"}, SharedPaths: []string{"go.mod"}, IsolationMode: "worktree"},
		FilesLikelyTouched:    []string{"internal/foo.go"},
		ImplementationDetail:  eposticket.ImplementationDetail{Approach: "small patch", Files: []eposticket.FileChange{{Path: "internal/foo.go", Change: "edit", Reason: "test"}}, Notes: "note"},
		LearningContext:       []eposticket.LearningRef{{ID: "learn-1", Type: "doc", Title: "Learning"}},
		ValidationChecks:      []eposticket.ValidationCheck{{Command: "go test ./...", Expected: "pass", Description: "tests"}},
		RequiredEvidence:      []string{"test output"},
		ReviewerGuidance:      "review carefully",
		ETag:                  "etag-1",
		CreatedFrom:           "fabrikk",
		GroupingReason:        "same subsystem",
		GroupedRequirementIDs: []string{"req-1", "req-2"},
		Present: map[string]bool{
			"requirement_ids": true, "source_refs": true, "lineage_id": true,
			"risk_level": true, "intent": true, "constraints": true, "warnings": true,
			"read_only_paths": true, "shared_paths": true, "isolation_mode": true,
			"files_likely_touched": true, "implementation_detail": true, "learning_context": true,
			"validation_checks": true, "required_evidence": true, "reviewer_guidance": true,
			"etag": true, "created_from": true, "grouping_reason": true, "grouped_requirement_ids": true,
		},
	}

	back := toEpos(fromEpos(in))
	if !reflect.DeepEqual(back.RequirementIDs, in.RequirementIDs) ||
		!reflect.DeepEqual(back.SourceRefs, in.SourceRefs) ||
		back.LineageID != in.LineageID ||
		back.RiskLevel != in.RiskLevel ||
		back.Intent != in.Intent ||
		!reflect.DeepEqual(back.Constraints, in.Constraints) ||
		!reflect.DeepEqual(back.Warnings, in.Warnings) ||
		!reflect.DeepEqual(back.Scope.ReadOnlyPaths, in.Scope.ReadOnlyPaths) ||
		!reflect.DeepEqual(back.Scope.SharedPaths, in.Scope.SharedPaths) ||
		back.Scope.IsolationMode != in.Scope.IsolationMode ||
		!reflect.DeepEqual(back.FilesLikelyTouched, in.FilesLikelyTouched) ||
		!reflect.DeepEqual(back.ImplementationDetail, in.ImplementationDetail) ||
		!reflect.DeepEqual(back.LearningContext, in.LearningContext) ||
		!reflect.DeepEqual(back.ValidationChecks, in.ValidationChecks) ||
		!reflect.DeepEqual(back.RequiredEvidence, in.RequiredEvidence) ||
		back.ReviewerGuidance != in.ReviewerGuidance ||
		back.ETag != in.ETag ||
		back.CreatedFrom != in.CreatedFrom ||
		back.GroupingReason != in.GroupingReason ||
		!reflect.DeepEqual(back.GroupedRequirementIDs, in.GroupedRequirementIDs) {
		t.Fatalf("epos named fields did not round-trip:\n got: %#v\nwant: %#v", back, in)
	}
}

func TestNormalizeStatus_EposExtendedStatuses(t *testing.T) {
	tests := map[eposticket.Status]Status{
		eposticket.StatusPending:       StatusOpen,
		eposticket.StatusRepairPending: StatusOpen,
		eposticket.StatusClaimed:       StatusInProgress,
		eposticket.StatusImplementing:  StatusInProgress,
		eposticket.StatusVerifying:     StatusInProgress,
		eposticket.StatusUnderReview:   StatusInProgress,
		eposticket.StatusDone:          StatusClosed,
		eposticket.StatusFailed:        StatusClosed,
		eposticket.StatusHeld:          StatusBlocked,
		eposticket.StatusReady:         StatusReady,
	}
	for in, want := range tests {
		if got := normalizeStatus(in); got != want {
			t.Fatalf("normalizeStatus(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestConvert_ExtendedStatusPreserved(t *testing.T) {
	got := fromEpos(&eposticket.Ticket{
		ID:             "ticket-1",
		Status:         eposticket.StatusUnderReview,
		ExtendedStatus: string(eposticket.StatusUnderReview),
		Present:        map[string]bool{"extended_status": true},
	})
	if got.Status != StatusInProgress {
		t.Fatalf("Status = %q, want %q", got.Status, StatusInProgress)
	}
	if got.UnknownFrontmatter["extended_status"] != string(eposticket.StatusUnderReview) {
		t.Fatalf("extended_status not preserved: %#v", got.UnknownFrontmatter)
	}
}
