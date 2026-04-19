package tui

import (
	"bytes"
	"strings"
	"testing"
	"verk/internal/engine"
)

// feedEvents sends the given events through a closed channel, returning a
// receive-only channel ready to be consumed by RunPlainProgress.
func feedEvents(events []engine.ProgressEvent) <-chan engine.ProgressEvent {
	ch := make(chan engine.ProgressEvent, len(events))
	for _, evt := range events {
		ch <- evt
	}
	close(ch)
	return ch
}

func TestRunPlainProgress_PartialWave_RendersWarningMarker(t *testing.T) {
	// A four-ticket wave where three tickets close and one remains blocked
	// must render as a warning/partial marker (⚠), not as a green check (✓),
	// and must include the blocked ticket ID so it is not silently lost as
	// later waves scroll past.
	ch := feedEvents([]engine.ProgressEvent{
		{
			Type:    engine.EventWaveStarted,
			WaveID:  1,
			Tickets: []string{"ver-a", "ver-b", "ver-c", "ver-d"},
		},
		{
			Type:           engine.EventWaveCompleted,
			WaveID:         1,
			Closed:         3,
			Total:          4,
			Success:        true, // wave accepted with warnings
			BlockedTickets: []string{"ver-d"},
		},
	})

	var buf bytes.Buffer
	RunPlainProgress(&buf, ch)
	out := buf.String()

	if strings.Contains(out, "3/4 closed ✓") {
		t.Fatalf("partial wave must not render as ✓; output:\n%s", out)
	}
	if !strings.Contains(out, "3/4 closed ⚠") {
		t.Fatalf("partial wave must render as ⚠; output:\n%s", out)
	}
	if !strings.Contains(out, "ver-d") {
		t.Fatalf("partial wave summary must include blocked ticket ID; output:\n%s", out)
	}
}

func TestRunPlainProgress_FullyClosedWave_RendersOKMarker(t *testing.T) {
	// Control: a wave with no blocked tickets still renders as the green ✓
	// marker. This guards against the partial-wave branch over-triggering.
	ch := feedEvents([]engine.ProgressEvent{
		{
			Type:    engine.EventWaveCompleted,
			WaveID:  2,
			Closed:  2,
			Total:   2,
			Success: true,
		},
	})

	var buf bytes.Buffer
	RunPlainProgress(&buf, ch)
	out := buf.String()

	if !strings.Contains(out, "2/2 closed ✓") {
		t.Fatalf("fully closed wave must render as ✓; output:\n%s", out)
	}
	if strings.Contains(out, "⚠") {
		t.Fatalf("fully closed wave must not render a warning marker; output:\n%s", out)
	}
}

func TestRunPlainProgress_FailedWave_RendersFailMarker(t *testing.T) {
	// Control: a hard-failed wave still renders as ✗ (not ⚠), even if a
	// blocked ticket list happens to ride along on the event.
	ch := feedEvents([]engine.ProgressEvent{
		{
			Type:           engine.EventWaveCompleted,
			WaveID:         3,
			Closed:         0,
			Total:          2,
			Success:        false,
			BlockedTickets: []string{"ver-x", "ver-y"},
		},
	})

	var buf bytes.Buffer
	RunPlainProgress(&buf, ch)
	out := buf.String()

	if !strings.Contains(out, "0/2 closed ✗") {
		t.Fatalf("failed wave must render as ✗; output:\n%s", out)
	}
	if strings.Contains(out, "⚠") {
		t.Fatalf("failed wave must not render a warning marker; output:\n%s", out)
	}
}

// TestRunPlainProgress_PartialWave_RendersExplicitBlockedDetails covers
// AC5: a 3-closed + 1-blocked wave must surface per-ticket phase, reason,
// and retry routing so operators never have to re-read the wave artifact
// just to see why a ticket stopped.
func TestRunPlainProgress_PartialWave_RendersExplicitBlockedDetails(t *testing.T) {
	ch := feedEvents([]engine.ProgressEvent{
		{
			Type:    engine.EventWaveStarted,
			WaveID:  1,
			Tickets: []string{"ver-a", "ver-b", "ver-c", "ver-d"},
		},
		{
			Type:           engine.EventWaveCompleted,
			WaveID:         1,
			Closed:         3,
			Total:          4,
			Success:        true,
			BlockedTickets: []string{"ver-d"},
			BlockedTicketDetails: []engine.BlockedTicketSummary{{
				TicketID:              "ver-d",
				Phase:                 "blocked",
				BlockReason:           "non_convergent_verification: gave up after 3 cycles",
				RequiresOperator:      true,
				CanRetryAutomatically: false,
			}},
		},
	})

	var buf bytes.Buffer
	RunPlainProgress(&buf, ch)
	out := buf.String()

	if !strings.Contains(out, "3/4 closed ⚠") {
		t.Fatalf("partial wave must render ⚠ summary; output:\n%s", out)
	}
	if !strings.Contains(out, "ver-d") {
		t.Fatalf("partial wave must name the blocked ticket; output:\n%s", out)
	}
	if !strings.Contains(out, "[blocked]") {
		t.Fatalf("blocked detail must include phase [blocked]; output:\n%s", out)
	}
	if !strings.Contains(out, "non_convergent_verification") {
		t.Fatalf("blocked detail must include block reason; output:\n%s", out)
	}
	if !strings.Contains(out, "requires operator") {
		t.Fatalf("blocked detail must flag that operator input is required; output:\n%s", out)
	}
}

// TestRunPlainProgress_PartialWave_AutoRetryRouting covers the automatic
// retry branch: a blocked ticket that `verk run` can pick up again must
// advertise that retry path so operators don't escalate unnecessarily.
func TestRunPlainProgress_PartialWave_AutoRetryRouting(t *testing.T) {
	ch := feedEvents([]engine.ProgressEvent{
		{
			Type:    engine.EventWaveCompleted,
			WaveID:  1,
			Closed:  1,
			Total:   2,
			Success: true,
			BlockedTicketDetails: []engine.BlockedTicketSummary{{
				TicketID:              "ver-e",
				Phase:                 "implement",
				BlockReason:           "transient worker crash",
				RequiresOperator:      false,
				CanRetryAutomatically: true,
			}},
		},
	})
	var buf bytes.Buffer
	RunPlainProgress(&buf, ch)
	out := buf.String()
	if !strings.Contains(out, "retry: verk run") {
		t.Fatalf("auto-retryable ticket must advertise retry path; output:\n%s", out)
	}
}

func TestRunPlainProgress_TicketDetail_IncludesTicketID(t *testing.T) {
	// The final blocked-epic summary relies on per-child EventTicketDetail
	// events that carry a TicketID. The plain renderer must surface that ID
	// instead of dropping it, so blocked tickets can be identified from the
	// terminating output alone.
	ch := feedEvents([]engine.ProgressEvent{
		{
			Type:     engine.EventTicketDetail,
			TicketID: "ver-d",
			Detail:   "blocked",
		},
	})

	var buf bytes.Buffer
	RunPlainProgress(&buf, ch)
	out := buf.String()

	if !strings.Contains(out, "ver-d") {
		t.Fatalf("ticket-scoped detail must include ticket ID; output:\n%s", out)
	}
	if !strings.Contains(out, "blocked") {
		t.Fatalf("ticket-scoped detail must include reason; output:\n%s", out)
	}
}
