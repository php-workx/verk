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

func TestAcceptWave_PartialWave_RecordsBlockedTickets(t *testing.T) {
	// A four-ticket wave where three tickets close and one is blocked must
	// still be accepted (engine keeps later ready waves schedulable), but the
	// wave acceptance must carry the blocked ticket IDs so progress plumbing
	// can surface them in wave summaries instead of a silent success.
	wave := state.WaveArtifact{
		WaveID:    "wave-1",
		Status:    state.WaveStatusRunning,
		TicketIDs: []string{"ver-a", "ver-b", "ver-c", "ver-d"},
	}

	accepted, err := AcceptWave(WaveAcceptanceRequest{
		Wave: wave,
		TicketPhases: []state.TicketPhase{
			state.TicketPhaseClosed,
			state.TicketPhaseClosed,
			state.TicketPhaseClosed,
			state.TicketPhaseBlocked,
		},
		ClaimsReleased:       true,
		PersistenceSucceeded: true,
	})
	if err != nil {
		t.Fatalf("expected partial wave to be accepted with warnings, got error: %v", err)
	}
	if accepted.Status != state.WaveStatusAccepted {
		t.Fatalf("expected WaveStatusAccepted for partial wave, got %q", accepted.Status)
	}
	blocked, ok := accepted.Acceptance["blocked_tickets"].([]string)
	if !ok {
		t.Fatalf("expected Acceptance[\"blocked_tickets\"] to be []string, got %T", accepted.Acceptance["blocked_tickets"])
	}
	if len(blocked) != 1 || blocked[0] != "ver-d" {
		t.Fatalf("expected blocked_tickets=[ver-d], got %v", blocked)
	}
	warnings, ok := accepted.Acceptance["warnings"].([]string)
	if !ok || len(warnings) == 0 {
		t.Fatalf("expected warnings slice on partial wave, got %v", accepted.Acceptance["warnings"])
	}
}

func TestCollectBlockedTicketIDs_OrderingAndEmptyCases(t *testing.T) {
	// Helper must preserve wave ordering and return nil (not a zero-length
	// slice) when every ticket closed, so consumers can rely on a simple
	// len(BlockedTickets) > 0 check to detect partial waves.
	outcomes := []waveTicketOutcome{
		{ticketID: "ver-a", phase: state.TicketPhaseClosed},
		{ticketID: "ver-b", phase: state.TicketPhaseBlocked},
		{ticketID: "ver-c", phase: state.TicketPhaseImplement},
		{ticketID: "ver-d", phase: state.TicketPhaseClosed},
	}
	got := collectBlockedTicketIDs(outcomes)
	want := []string{"ver-b", "ver-c"}
	if len(got) != len(want) {
		t.Fatalf("expected %d blocked ids, got %d: %v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected blocked[%d]=%q, got %q", i, want[i], got[i])
		}
	}

	allClosed := []waveTicketOutcome{
		{ticketID: "ver-a", phase: state.TicketPhaseClosed},
		{ticketID: "ver-b", phase: state.TicketPhaseClosed},
	}
	if ids := collectBlockedTicketIDs(allClosed); ids != nil {
		t.Fatalf("expected nil when every ticket closed, got %v", ids)
	}
}

// TestAcceptWave_BlockedTicketDetails_RecordsExplicitReasons asserts AC3/AC5:
// a 3-closed + 1-blocked wave records structured per-blocked-ticket details
// on the wave artifact so downstream consumers can surface the ticket id,
// phase, block reason, operator-required flag, and automatic-retry flag.
func TestAcceptWave_BlockedTicketDetails_RecordsExplicitReasons(t *testing.T) {
	wave := state.WaveArtifact{
		WaveID:    "wave-1",
		Status:    state.WaveStatusRunning,
		TicketIDs: []string{"ver-a", "ver-b", "ver-c", "ver-d"},
	}
	details := map[string]WaveTicketDetail{
		"ver-d": {
			Title:                 "needs operator",
			BlockReason:           "non_convergent_verification: give up after 3 cycles",
			RequiresOperator:      true,
			CanRetryAutomatically: false,
		},
	}
	accepted, err := AcceptWave(WaveAcceptanceRequest{
		Wave: wave,
		TicketPhases: []state.TicketPhase{
			state.TicketPhaseClosed,
			state.TicketPhaseClosed,
			state.TicketPhaseClosed,
			state.TicketPhaseBlocked,
		},
		ClaimsReleased:       true,
		PersistenceSucceeded: true,
		TicketDetails:        details,
	})
	if err != nil {
		t.Fatalf("expected partial wave to be accepted, got %v", err)
	}
	if accepted.Status != state.WaveStatusAccepted {
		t.Fatalf("expected accepted status, got %q", accepted.Status)
	}
	raw, ok := accepted.Acceptance["blocked_ticket_details"].([]BlockedTicketSummary)
	if !ok {
		t.Fatalf("expected blocked_ticket_details []BlockedTicketSummary, got %T", accepted.Acceptance["blocked_ticket_details"])
	}
	if len(raw) != 1 {
		t.Fatalf("expected 1 blocked ticket detail, got %d", len(raw))
	}
	got := raw[0]
	if got.TicketID != "ver-d" {
		t.Errorf("expected TicketID=ver-d, got %q", got.TicketID)
	}
	if got.Phase != state.TicketPhaseBlocked {
		t.Errorf("expected Phase=blocked, got %q", got.Phase)
	}
	if got.BlockReason != "non_convergent_verification: give up after 3 cycles" {
		t.Errorf("expected block reason preserved, got %q", got.BlockReason)
	}
	if !got.RequiresOperator {
		t.Errorf("expected RequiresOperator=true")
	}
	if got.CanRetryAutomatically {
		t.Errorf("expected CanRetryAutomatically=false for operator-required ticket")
	}

	// Warnings should surface the reason so log scrapers see it alongside
	// the bare blocked-id list.
	warnings, _ := accepted.Acceptance["warnings"].([]string)
	var sawReason bool
	for _, w := range warnings {
		if w == "ver-d blocked: non_convergent_verification: give up after 3 cycles" {
			sawReason = true
			break
		}
	}
	if !sawReason {
		t.Errorf("expected warnings to include the ver-d block reason; got %v", warnings)
	}
}

// TestAcceptWave_BlockedTicketDetails_DefaultsForMissingDetail verifies
// that a blocked ticket with no supplied detail still receives a
// structural explanation so the wave summary is never silent about why
// the ticket did not close.
func TestAcceptWave_BlockedTicketDetails_DefaultsForMissingDetail(t *testing.T) {
	wave := state.WaveArtifact{
		WaveID:    "wave-1",
		Status:    state.WaveStatusRunning,
		TicketIDs: []string{"ver-a", "ver-b"},
	}
	accepted, err := AcceptWave(WaveAcceptanceRequest{
		Wave: wave,
		TicketPhases: []state.TicketPhase{
			state.TicketPhaseClosed,
			state.TicketPhaseBlocked,
		},
		ClaimsReleased:       true,
		PersistenceSucceeded: true,
	})
	if err != nil {
		t.Fatalf("expected acceptance with warnings, got %v", err)
	}
	details, ok := accepted.Acceptance["blocked_ticket_details"].([]BlockedTicketSummary)
	if !ok || len(details) != 1 {
		t.Fatalf("expected 1 blocked ticket detail, got %T=%v", accepted.Acceptance["blocked_ticket_details"], accepted.Acceptance["blocked_ticket_details"])
	}
	d := details[0]
	if d.TicketID != "ver-b" {
		t.Errorf("expected TicketID=ver-b, got %q", d.TicketID)
	}
	if d.Phase != state.TicketPhaseBlocked {
		t.Errorf("expected Phase=blocked, got %q", d.Phase)
	}
	if d.BlockReason == "" {
		t.Errorf("expected structural default BlockReason, got empty string")
	}
	if !d.RequiresOperator {
		t.Errorf("expected RequiresOperator=true for a blocked ticket with no supplied detail")
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
