package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"verk/internal/adapters/ticketstore/epos"
	"verk/internal/policy"
	"verk/internal/state"
)

func TestBuildPlanArtifact_PersistsEffectiveReviewThreshold(t *testing.T) {
	cfg := policy.DefaultConfig()
	ticket := epos.Ticket{
		ID:                 "ver-123",
		Title:              "Implement intake",
		AcceptanceCriteria: []string{"Criterion one"},
		TestCases:          []string{"go test ./..."},
		ValidationCommands: []string{"go test ./internal/engine"},
		OwnedPaths:         []string{"internal/engine"},
		ReviewThreshold:    string(state.SeverityP3),
		Runtime:            "codex",
		UnknownFrontmatter: map[string]any{
			"type": "task",
		},
	}

	artifact, err := BuildPlanArtifact(ticket, cfg)
	if err != nil {
		t.Fatalf("BuildPlanArtifact returned error: %v", err)
	}

	if artifact.EffectiveReviewThreshold != state.SeverityP3 {
		t.Fatalf("expected effective threshold P3, got %q", artifact.EffectiveReviewThreshold)
	}
	if artifact.ReviewThreshold != state.SeverityP3 {
		t.Fatalf("expected persisted review threshold P3, got %q", artifact.ReviewThreshold)
	}
	if got := artifact.OwnedPaths; len(got) != 1 || got[0] != "internal/engine" {
		t.Fatalf("expected owned paths to round-trip, got %#v", got)
	}
	if artifact.Phase != state.TicketPhaseIntake {
		t.Fatalf("expected intake phase, got %q", artifact.Phase)
	}
}

func TestBuildPlanArtifact_RejectsMissingOwnedPathsForEpic(t *testing.T) {
	cfg := policy.DefaultConfig()
	ticket := epos.Ticket{
		ID:    "ver-epic",
		Title: "Epic ticket",
		UnknownFrontmatter: map[string]any{
			"type": "epic",
		},
	}

	if _, err := BuildPlanArtifact(ticket, cfg); err == nil {
		t.Fatal("expected epic without owned_paths to fail")
	}
}

// TestBuildPlanArtifact_SnapshotsEffectiveRoleProfiles covers ver-laq2 test
// case 4: intake must snapshot the worker and reviewer role profiles resolved
// from config so later audits can reproduce which model/reasoning/runtime
// pair the run was planned against.
func TestBuildPlanArtifact_SnapshotsEffectiveRoleProfiles(t *testing.T) {
	cfg := policy.DefaultConfig()
	ticket := epos.Ticket{
		ID:                 "ver-role",
		Title:              "role profile snapshot",
		OwnedPaths:         []string{"internal/engine"},
		AcceptanceCriteria: []string{"Criterion one"},
	}

	artifact, err := BuildPlanArtifact(ticket, cfg)
	if err != nil {
		t.Fatalf("BuildPlanArtifact returned error: %v", err)
	}

	if artifact.WorkerProfile == nil {
		t.Fatal("expected worker profile snapshot, got nil")
	}
	if artifact.WorkerProfile.Runtime != "claude" || artifact.WorkerProfile.Model != "sonnet" || artifact.WorkerProfile.Reasoning != "high" {
		t.Fatalf("unexpected worker profile snapshot: %+v", artifact.WorkerProfile)
	}
	if artifact.ReviewerProfile == nil {
		t.Fatal("expected reviewer profile snapshot, got nil")
	}
	if artifact.ReviewerProfile.Runtime != "claude" || artifact.ReviewerProfile.Model != "opus" || artifact.ReviewerProfile.Reasoning != "xhigh" {
		t.Fatalf("unexpected reviewer profile snapshot: %+v", artifact.ReviewerProfile)
	}
}

// TestBuildPlanArtifact_IgnoresTicketModelFrontmatter covers ver-laq2 test
// case 3: ticket frontmatter `model` must NOT influence the effective
// execution profile. The plan snapshot reflects the config-owned model
// regardless of what a ticket author set.
func TestBuildPlanArtifact_IgnoresTicketModelFrontmatter(t *testing.T) {
	cfg := policy.DefaultConfig()
	ticket := epos.Ticket{
		ID:                 "ver-model-ignored",
		Title:              "ticket model is ignored",
		OwnedPaths:         []string{"internal/engine"},
		AcceptanceCriteria: []string{"Criterion one"},
		// A ticket author attempting to swap the execution model via
		// frontmatter must not win — model selection is policy-owned.
		Model: "ticket-override-model",
	}

	artifact, err := BuildPlanArtifact(ticket, cfg)
	if err != nil {
		t.Fatalf("BuildPlanArtifact returned error: %v", err)
	}
	if artifact.WorkerProfile == nil || artifact.WorkerProfile.Model != "sonnet" {
		t.Fatalf("expected worker model to remain the config-owned sonnet, got %+v", artifact.WorkerProfile)
	}
	if artifact.ReviewerProfile == nil || artifact.ReviewerProfile.Model != "opus" {
		t.Fatalf("expected reviewer model to remain the config-owned opus, got %+v", artifact.ReviewerProfile)
	}
}

// TestBuildPlanArtifact_RuntimePreferenceOverridesSnapshotRuntime verifies
// that ticket-level RuntimePreference (which is the ONLY ticket-frontmatter
// routing hint permitted for execution) swaps the runtime identifier in the
// snapshot, while model and reasoning remain driven by the role profile.
func TestBuildPlanArtifact_RuntimePreferenceOverridesSnapshotRuntime(t *testing.T) {
	cfg := policy.DefaultConfig()
	ticket := epos.Ticket{
		ID:                 "ver-rt-pref",
		Title:              "runtime preference override",
		OwnedPaths:         []string{"internal/engine"},
		AcceptanceCriteria: []string{"Criterion one"},
		Runtime:            "codex",
	}

	artifact, err := BuildPlanArtifact(ticket, cfg)
	if err != nil {
		t.Fatalf("BuildPlanArtifact returned error: %v", err)
	}
	if artifact.WorkerProfile == nil || artifact.WorkerProfile.Runtime != "codex" {
		t.Fatalf("expected worker runtime to follow RuntimePreference, got %+v", artifact.WorkerProfile)
	}
	if artifact.WorkerProfile.Model != "sonnet" || artifact.WorkerProfile.Reasoning != "high" {
		t.Fatalf("expected worker model/reasoning to stay policy-owned, got %+v", artifact.WorkerProfile)
	}
	if artifact.ReviewerProfile == nil || artifact.ReviewerProfile.Runtime != "codex" {
		t.Fatalf("expected reviewer runtime to follow RuntimePreference, got %+v", artifact.ReviewerProfile)
	}
	if artifact.ReviewerProfile.Model != "opus" || artifact.ReviewerProfile.Reasoning != "xhigh" {
		t.Fatalf("expected reviewer model/reasoning to stay policy-owned, got %+v", artifact.ReviewerProfile)
	}
}

// writeMinimalTicketFile writes a minimal valid ticket markdown file to dir/<id>.md
// and returns the path. The ticket has no profile field by default.
func writeMinimalTicketFile(t *testing.T, dir, id, extraFrontmatter string) string {
	t.Helper()
	path := filepath.Join(dir, id+".md")
	content := "---\nid: " + id + "\nstatus: open\nowned_paths:\n  - internal/foo.go\n" + extraFrontmatter + "---\n# " + id + "\n\nbody text\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write ticket file: %v", err)
	}
	return path
}

func TestResolveTicketProfile_DetectsAndWritesBackWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	ticketID := "ver-detect"
	writeMinimalTicketFile(t, dir, ticketID, "")

	ticket, err := epos.LoadTicket(filepath.Join(dir, ticketID+".md"))
	if err != nil {
		t.Fatalf("load ticket: %v", err)
	}
	if ticket.Profile != "" {
		t.Fatalf("expected empty profile before detection, got %q", ticket.Profile)
	}

	profile, err := ResolveTicketProfile(dir, &ticket)
	if err != nil {
		t.Fatalf("ResolveTicketProfile returned error: %v", err)
	}
	if profile == "" {
		t.Fatal("expected non-empty detected profile")
	}
	// ticket struct must be updated in-place
	if ticket.Profile != profile {
		t.Fatalf("ticket.Profile = %q, want %q", ticket.Profile, profile)
	}

	// file on disk must now contain the profile
	saved, err := epos.LoadTicket(filepath.Join(dir, ticketID+".md"))
	if err != nil {
		t.Fatalf("reload ticket: %v", err)
	}
	if saved.Profile != profile {
		t.Fatalf("saved profile = %q, want %q", saved.Profile, profile)
	}
}

func TestResolveTicketProfile_ValidatesExplicitProfile(t *testing.T) {
	dir := t.TempDir()
	ticketID := "ver-explicit"
	writeMinimalTicketFile(t, dir, ticketID, "profile: contract-engineer\n")

	ticket, err := epos.LoadTicket(filepath.Join(dir, ticketID+".md"))
	if err != nil {
		t.Fatalf("load ticket: %v", err)
	}

	profile, err := ResolveTicketProfile(dir, &ticket)
	if err != nil {
		t.Fatalf("ResolveTicketProfile returned error: %v", err)
	}
	if profile != epos.ProfileContract {
		t.Fatalf("expected %q, got %q", epos.ProfileContract, profile)
	}

	// file must NOT have been rewritten — stat mtime check is fragile on fast
	// systems, so reload and verify profile is unchanged instead
	saved, err := epos.LoadTicket(filepath.Join(dir, ticketID+".md"))
	if err != nil {
		t.Fatalf("reload ticket: %v", err)
	}
	if saved.Profile != epos.ProfileContract {
		t.Fatalf("saved profile = %q, want %q", epos.ProfileContract, saved.Profile)
	}
}

func TestResolveTicketProfile_RejectsUnknownProfile(t *testing.T) {
	dir := t.TempDir()
	ticketID := "ver-bad-profile"
	writeMinimalTicketFile(t, dir, ticketID, "profile: wizard\n")

	ticket, err := epos.LoadTicket(filepath.Join(dir, ticketID+".md"))
	if err != nil {
		t.Fatalf("load ticket: %v", err)
	}

	_, err = ResolveTicketProfile(dir, &ticket)
	if err == nil {
		t.Fatal("expected error for unknown profile, got nil")
	}
	if !strings.Contains(err.Error(), "unknown_profile") {
		t.Fatalf("error %q does not contain 'unknown_profile'", err.Error())
	}
}
