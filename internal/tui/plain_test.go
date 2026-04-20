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

// TestRunPlainProgress_RepairCycleStarted_ShowsCheckIDs covers TC2 (first half):
// when a repair cycle starts, the plain output shows which check IDs triggered
// it so operators can correlate the failure without reading the wave artifact.
func TestRunPlainProgress_RepairCycleStarted_ShowsCheckIDs(t *testing.T) {
	ch := feedEvents([]engine.ProgressEvent{
		{
			Type:            engine.EventRepairCycleStarted,
			TicketID:        "wave-1",
			RepairCycle:     1,
			MaxRepairCycles: 3,
			CheckIDs:        []string{"check-go-test", "check-lint"},
		},
	})

	var buf bytes.Buffer
	RunPlainProgress(&buf, ch)
	out := buf.String()

	if !strings.Contains(out, "repair") {
		t.Fatalf("repair cycle start must mention repair; output:\n%s", out)
	}
	if !strings.Contains(out, "check-go-test") {
		t.Fatalf("repair cycle start must include check ID; output:\n%s", out)
	}
	if !strings.Contains(out, "1/3") {
		t.Fatalf("repair cycle start must include cycle N/M; output:\n%s", out)
	}
}

// TestRunPlainProgress_RepairCycleSucceeded_ShowsRepairedStatus covers TC2:
// when a repair cycle succeeds, the output shows "repaired" so the operator
// sees the fix rather than only the final close.
func TestRunPlainProgress_RepairCycleSucceeded_ShowsRepairedStatus(t *testing.T) {
	ch := feedEvents([]engine.ProgressEvent{
		{
			Type:            engine.EventRepairCycleStarted,
			TicketID:        "wave-1",
			RepairCycle:     1,
			MaxRepairCycles: 2,
			CheckIDs:        []string{"check-go-test"},
		},
		{
			Type:            engine.EventRepairCycleSucceeded,
			TicketID:        "wave-1",
			RepairCycle:     1,
			MaxRepairCycles: 2,
		},
		{
			Type:    engine.EventWaveCompleted,
			WaveID:  1,
			Closed:  1,
			Total:   1,
			Success: true,
		},
	})

	var buf bytes.Buffer
	RunPlainProgress(&buf, ch)
	out := buf.String()

	// The output must contain both the repaired marker AND the final close
	// marker — not just the close.
	if !strings.Contains(out, "repaired") {
		t.Fatalf("output must contain repaired marker; output:\n%s", out)
	}
	if !strings.Contains(out, "1/1 closed ✓") {
		t.Fatalf("output must also show wave close; output:\n%s", out)
	}
}

// TestRunPlainProgress_RepairCycleExhausted_ShowsFailedChecksAndNextAction
// covers TC3: when repair budget is exhausted, the output names the checks
// that could not be repaired and directs the operator to follow up manually.
func TestRunPlainProgress_RepairCycleExhausted_ShowsFailedChecksAndNextAction(t *testing.T) {
	ch := feedEvents([]engine.ProgressEvent{
		{
			Type:            engine.EventRepairCycleStarted,
			TicketID:        "wave-1",
			RepairCycle:     1,
			MaxRepairCycles: 1,
			CheckIDs:        []string{"check-go-test"},
		},
		{
			Type:            engine.EventRepairCycleExhausted,
			TicketID:        "wave-1",
			RepairCycle:     1,
			MaxRepairCycles: 1,
			CheckIDs:        []string{"check-go-test"},
		},
	})

	var buf bytes.Buffer
	RunPlainProgress(&buf, ch)
	out := buf.String()

	if !strings.Contains(out, "check-go-test") {
		t.Fatalf("exhausted repair must name the failing check; output:\n%s", out)
	}
	if !strings.Contains(out, "manual follow-up") {
		t.Fatalf("exhausted repair must indicate manual follow-up required; output:\n%s", out)
	}
}

// TestRunPlainProgress_AdvisorySkippedChecks_DoesNotAddNoise covers TC6:
// a wave that closes cleanly with no blocked tickets must not emit any
// advisory/skipped check noise — a quiet success is the right default.
func TestRunPlainProgress_AdvisorySkippedChecks_DoesNotAddNoise(t *testing.T) {
	ch := feedEvents([]engine.ProgressEvent{
		{
			Type:    engine.EventWaveStarted,
			WaveID:  1,
			Tickets: []string{"ver-a"},
		},
		{
			Type:    engine.EventWaveCompleted,
			WaveID:  1,
			Closed:  1,
			Total:   1,
			Success: true,
			// No BlockedTicketDetails — all checks passed or were skipped quietly.
		},
	})

	var buf bytes.Buffer
	RunPlainProgress(&buf, ch)
	out := buf.String()

	if !strings.Contains(out, "1/1 closed ✓") {
		t.Fatalf("all-closed wave must render ✓; output:\n%s", out)
	}
	// No advisory or skipped label should appear in quiet output.
	for _, noisyToken := range []string{"advisory", "skipped", "[repair]"} {
		if strings.Contains(strings.ToLower(out), noisyToken) {
			t.Fatalf("all-closed wave must not mention %q; output:\n%s", noisyToken, out)
		}
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
