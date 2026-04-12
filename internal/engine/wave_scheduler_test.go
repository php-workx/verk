package engine

import (
	"testing"

	"verk/internal/state"
)

func TestCheckScopeViolation_EmptyOwned_ReturnsError(t *testing.T) {
	err := CheckScopeViolation([]string{"foo/bar.go"}, nil)
	if err == nil {
		t.Fatal("expected error when owned is nil, got nil — scope check must fail closed (G9)")
	}
}

func TestCheckScopeViolation_EmptyOwnedSlice_ReturnsError(t *testing.T) {
	err := CheckScopeViolation([]string{"foo/bar.go"}, []string{})
	if err == nil {
		t.Fatal("expected error when owned is empty slice, got nil — scope check must fail closed (G9)")
	}
}

func TestCheckScopeViolation_ValidScope_Passes(t *testing.T) {
	err := CheckScopeViolation([]string{"internal/app/main.go"}, []string{"internal/app"})
	if err != nil {
		t.Fatalf("expected no error for in-scope file, got %v", err)
	}
}

func TestCheckScopeViolation_OutOfScope_Fails(t *testing.T) {
	err := CheckScopeViolation([]string{"internal/other/main.go"}, []string{"internal/app"})
	if err == nil {
		t.Fatal("expected error for out-of-scope file, got nil")
	}
}

func TestValidatePerTicketScope_NoScopes_ReturnsError(t *testing.T) {
	err := validatePerTicketScope([]string{"ticket-a"}, []string{"foo/bar.go"}, nil)
	if err == nil {
		t.Fatal("expected error when ticketScopes is nil, got nil — must fail closed")
	}
}

func TestValidatePerTicketScope_EmptyScopes_ReturnsError(t *testing.T) {
	err := validatePerTicketScope([]string{"ticket-a"}, []string{"foo/bar.go"}, map[string][]string{})
	if err == nil {
		t.Fatal("expected error when ticketScopes is empty, got nil — must fail closed")
	}
}

func TestValidatePerTicketScope_TicketMissingFromScopes_ReturnsError(t *testing.T) {
	err := validatePerTicketScope(
		[]string{"ticket-a", "ticket-b"},
		[]string{"internal/app/main.go"},
		map[string][]string{
			"ticket-a": {"internal/app"},
			// ticket-b is missing — must fail closed
		},
	)
	if err == nil {
		t.Fatal("expected error when ticket-b has no scope declarations, got nil")
	}
}

func TestValidatePerTicketScope_TicketWithEmptyScope_ReturnsError(t *testing.T) {
	err := validatePerTicketScope(
		[]string{"ticket-a"},
		[]string{"internal/app/main.go"},
		map[string][]string{
			"ticket-a": {}, // empty scope declarations — must fail closed
		},
	)
	if err == nil {
		t.Fatal("expected error when ticket has empty scope declarations, got nil")
	}
}

func TestValidatePerTicketScope_ValidPerTicketScope_Passes(t *testing.T) {
	err := validatePerTicketScope(
		[]string{"ticket-a"},
		[]string{"internal/app/main.go"},
		map[string][]string{
			"ticket-a": {"internal/app"},
		},
	)
	if err != nil {
		t.Fatalf("expected no error for valid per-ticket scope, got %v", err)
	}
}

func TestValidatePerTicketScope_OutOfScope_Fails(t *testing.T) {
	err := validatePerTicketScope(
		[]string{"ticket-a"},
		[]string{"internal/other/main.go"},
		map[string][]string{
			"ticket-a": {"internal/app"},
		},
	)
	if err == nil {
		t.Fatal("expected error for out-of-scope file, got nil")
	}
}

func TestValidatePerTicketScope_MultipleTickets_UnionScope(t *testing.T) {
	// ticket-a owns "internal/app", ticket-b owns "docs"
	// A file in "docs" should be fine even though it's not in ticket-a's scope
	err := validatePerTicketScope(
		[]string{"ticket-a", "ticket-b"},
		[]string{"internal/app/main.go", "docs/readme.md"},
		map[string][]string{
			"ticket-a": {"internal/app"},
			"ticket-b": {"docs"},
		},
	)
	if err != nil {
		t.Fatalf("expected no error for files within union of per-ticket scopes, got %v", err)
	}
}

func TestValidatePerTicketScope_MultipleTickets_CrossScopeViolation(t *testing.T) {
	// ticket-a owns "internal/app", ticket-b owns "docs"
	// A file in "internal/other" is not in any ticket's scope
	err := validatePerTicketScope(
		[]string{"ticket-a", "ticket-b"},
		[]string{"internal/app/main.go", "internal/other/hack.go"},
		map[string][]string{
			"ticket-a": {"internal/app"},
			"ticket-b": {"docs"},
		},
	)
	if err == nil {
		t.Fatal("expected error for file outside all per-ticket scopes, got nil")
	}
}

func TestAcceptWave_RejectsEmptyTicketScopes(t *testing.T) {
	wave := state.WaveArtifact{
		WaveID:      "wave-1",
		Status:      state.WaveStatusRunning,
		TicketIDs:   []string{"ticket-a"},
		PlannedScope: []string{"internal/app"},
	}

	req := WaveAcceptanceRequest{
		Wave:                 wave,
		TicketPhases:         []state.TicketPhase{state.TicketPhaseClosed},
		ChangedFiles:         []string{"internal/app/main.go"},
		TicketScopes:         nil, // no scopes provided — must fail closed
		ClaimsReleased:       true,
		PersistenceSucceeded: true,
	}

	_, err := AcceptWave(req)
	if err == nil {
		t.Fatal("expected error when TicketScopes is nil, got nil — must fail closed")
	}
}

func TestAcceptWave_PericTicketScopeValidation(t *testing.T) {
	wave := state.WaveArtifact{
		WaveID:      "wave-1",
		Status:      state.WaveStatusRunning,
		TicketIDs:   []string{"ticket-a"},
		PlannedScope: []string{"internal/app"},
	}

	req := WaveAcceptanceRequest{
		Wave:                 wave,
		TicketPhases:         []state.TicketPhase{state.TicketPhaseClosed},
		ChangedFiles:         []string{"internal/app/main.go"},
		TicketScopes:         map[string][]string{"ticket-a": {"internal/app"}},
		ClaimsReleased:       true,
		PersistenceSucceeded: true,
	}

	accepted, err := AcceptWave(req)
	if err != nil {
		t.Fatalf("expected acceptance, got error: %v", err)
	}
	if accepted.Status != state.WaveStatusAccepted {
		t.Fatalf("expected accepted status, got %q", accepted.Status)
	}
}