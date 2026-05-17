package state

import (
	"strings"
	"testing"
)

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

// ---------------------------------------------------------------------------
// ValidateTestReference tests
// ---------------------------------------------------------------------------

func TestValidateTestReference_TestFunctionRequiresPackageAndName(t *testing.T) {
	// Missing Package
	if err := ValidateTestReference(TestReference{Kind: "test_function", Name: "TestSomething"}); err == nil || !strings.Contains(err.Error(), "missing package") {
		t.Fatalf("expected missing package error, got: %v", err)
	}
	// Missing Name
	if err := ValidateTestReference(TestReference{Kind: "test_function", Package: "engine"}); err == nil || !strings.Contains(err.Error(), "missing name") {
		t.Fatalf("expected missing name error, got: %v", err)
	}
}

func TestValidateTestReference_FileLineRequiresFileAndLine(t *testing.T) {
	// Missing File
	if err := ValidateTestReference(TestReference{Kind: "file_line", Line: 10}); err == nil || !strings.Contains(err.Error(), "missing file") {
		t.Fatalf("expected missing file error, got: %v", err)
	}
	// Missing Line (zero value)
	if err := ValidateTestReference(TestReference{Kind: "file_line", File: "internal/foo.go"}); err == nil || !strings.Contains(err.Error(), "missing line") {
		t.Fatalf("expected missing line error, got: %v", err)
	}
}

func TestValidateTestReference_UnknownKindRejected(t *testing.T) {
	if err := ValidateTestReference(TestReference{Kind: "free_form_string"}); err == nil || !strings.Contains(err.Error(), "unknown test reference kind") {
		t.Fatalf("expected unknown kind error, got: %v", err)
	}
}

func TestValidateTestReference_AcceptsValid(t *testing.T) {
	cases := []TestReference{
		{Kind: "test_function", Package: "engine", Name: "TestFoo"},
		{Kind: "file_line", File: "internal/engine/closeout.go", Line: 42},
	}
	for _, ref := range cases {
		if err := ValidateTestReference(ref); err != nil {
			t.Fatalf("expected valid reference (kind=%q) to pass, got: %v", ref.Kind, err)
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
