package state

import "testing"

func TestValidateTicketTransition_AllowsSpecifiedTransitions(t *testing.T) {
	cases := []struct {
		from TicketPhase
		to   TicketPhase
	}{
		{TicketPhaseIntake, TicketPhaseImplement},
		{TicketPhaseImplement, TicketPhaseVerify},
		{TicketPhaseImplement, TicketPhaseBlocked},
		{TicketPhaseVerify, TicketPhaseReview},
		{TicketPhaseVerify, TicketPhaseImplement},
		{TicketPhaseVerify, TicketPhaseBlocked},
		{TicketPhaseReview, TicketPhaseCloseout},
		{TicketPhaseReview, TicketPhaseRepair},
		{TicketPhaseReview, TicketPhaseBlocked},
		{TicketPhaseRepair, TicketPhaseVerify},
		{TicketPhaseRepair, TicketPhaseBlocked},
		{TicketPhaseCloseout, TicketPhaseClosed},
		{TicketPhaseCloseout, TicketPhaseRepair},
		{TicketPhaseCloseout, TicketPhaseBlocked},
	}

	for _, tc := range cases {
		if err := ValidateTicketTransition(tc.from, tc.to); err != nil {
			t.Fatalf("expected %q -> %q to be allowed, got error: %v", tc.from, tc.to, err)
		}
	}
}

func TestValidateTicketTransition_RejectsForbiddenTransitions(t *testing.T) {
	cases := []struct {
		from TicketPhase
		to   TicketPhase
	}{
		{TicketPhaseClosed, TicketPhaseImplement},
		{TicketPhaseBlocked, TicketPhaseImplement},
		{TicketPhaseImplement, TicketPhaseCloseout},
		{TicketPhaseVerify, TicketPhaseCloseout},
		{TicketPhaseIntake, TicketPhaseClosed},
	}

	for _, tc := range cases {
		if err := ValidateTicketTransition(tc.from, tc.to); err == nil {
			t.Fatalf("expected %q -> %q to be rejected", tc.from, tc.to)
		}
	}
}

func TestEffectiveReviewThreshold_Precedence(t *testing.T) {
	cfg := SeverityP2
	ticket := SeverityP3
	cli := SeverityP1

	if got := EffectiveReviewThreshold(&cli, &ticket, cfg); got != SeverityP1 {
		t.Fatalf("expected CLI threshold to win, got %q", got)
	}

	if got := EffectiveReviewThreshold(nil, &ticket, cfg); got != SeverityP3 {
		t.Fatalf("expected ticket threshold to win when CLI absent, got %q", got)
	}

	if got := EffectiveReviewThreshold(nil, nil, cfg); got != SeverityP2 {
		t.Fatalf("expected config threshold fallback, got %q", got)
	}
}
