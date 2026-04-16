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

func TestValidatePerTicketScope_NoScopes_Passes(t *testing.T) {
	// Unscoped tickets (nil map) are exempt — no boundaries declared means no scope to enforce.
	err := validatePerTicketScope([]string{"ticket-a"}, []string{"foo/bar.go"}, nil)
	if err != nil {
		t.Fatalf("expected nil for unscoped ticket (nil map), got %v", err)
	}
}

func TestValidatePerTicketScope_EmptyScopes_Passes(t *testing.T) {
	// Same: empty map means all tickets are unscoped, skip enforcement.
	err := validatePerTicketScope([]string{"ticket-a"}, []string{"foo/bar.go"}, map[string][]string{})
	if err != nil {
		t.Fatalf("expected nil for unscoped ticket (empty map), got %v", err)
	}
}

func TestValidatePerTicketScope_TicketMissingFromScopes_Passes(t *testing.T) {
	// ticket-b is missing from the scope map → treated as unscoped → whole wave is exempt.
	err := validatePerTicketScope(
		[]string{"ticket-a", "ticket-b"},
		[]string{"internal/app/main.go"},
		map[string][]string{
			"ticket-a": {"internal/app"},
		},
	)
	if err != nil {
		t.Fatalf("expected nil when one ticket is unscoped, got %v", err)
	}
}

func TestValidatePerTicketScope_TicketWithEmptyScope_Passes(t *testing.T) {
	// Explicit empty slice is treated as unscoped → whole wave is exempt.
	err := validatePerTicketScope(
		[]string{"ticket-a"},
		[]string{"internal/app/main.go"},
		map[string][]string{
			"ticket-a": {},
		},
	)
	if err != nil {
		t.Fatalf("expected nil when ticket has empty owned_paths, got %v", err)
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

func TestAcceptWave_UnscopedTickets_Accepted(t *testing.T) {
	// Tickets with no owned_paths are exempt from scope enforcement; the wave should
	// be accepted even when TicketScopes is nil or contains only empty entries.
	wave := state.WaveArtifact{
		WaveID:       "wave-1",
		Status:       state.WaveStatusRunning,
		TicketIDs:    []string{"ticket-a"},
		PlannedScope: []string{"internal/app"},
	}

	req := WaveAcceptanceRequest{
		Wave:                 wave,
		TicketPhases:         []state.TicketPhase{state.TicketPhaseClosed},
		ChangedFiles:         []string{"internal/app/main.go"},
		TicketScopes:         nil, // no owned_paths declared → unscoped → skip check
		ClaimsReleased:       true,
		PersistenceSucceeded: true,
	}

	result, err := AcceptWave(req)
	if err != nil {
		t.Fatalf("expected no error for unscoped tickets, got %v", err)
	}
	if result.Status != state.WaveStatusAccepted {
		t.Fatalf("expected accepted status for unscoped tickets, got %q", result.Status)
	}
}

func TestAcceptWave_PerTicketScopeValidation(t *testing.T) {
	wave := state.WaveArtifact{
		WaveID:       "wave-1",
		Status:       state.WaveStatusRunning,
		TicketIDs:    []string{"ticket-a"},
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
