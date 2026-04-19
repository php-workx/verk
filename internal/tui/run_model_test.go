package tui

import (
	"strings"
	"testing"
	"verk/internal/engine"

	tea "charm.land/bubbletea/v2"
)

// applyEvents feeds a sequence of progress events through the Model's
// Update pipeline, returning the resulting model. It bypasses the
// channel-backed command loop because the tests care only about
// state/view after each event, not the command scheduling.
func applyEvents(t *testing.T, m Model, events []engine.ProgressEvent) Model {
	t.Helper()
	for _, evt := range events {
		next, _ := m.Update(evt)
		casted, ok := next.(Model)
		if !ok {
			t.Fatalf("Update returned non-Model tea.Model: %T", next)
		}
		m = casted
	}
	return m
}

func TestBubbleModel_PartialWave_RendersWarningWithBlockedIDs(t *testing.T) {
	// When a wave completes with three of four tickets closed, the Bubble
	// view must render a warning marker (⚠) instead of the green ✓, and
	// include the blocked ticket ID so operators see it before later waves
	// scroll past.
	ch := make(chan engine.ProgressEvent)
	m := NewRunModel("run-test", ch)

	m = applyEvents(t, m, []engine.ProgressEvent{
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
		},
	})

	view := viewString(m)
	if !strings.Contains(view, "3/4 closed") {
		t.Fatalf("view missing wave tally; view:\n%s", view)
	}
	if !strings.Contains(view, "⚠") {
		t.Fatalf("partial wave must render ⚠ marker; view:\n%s", view)
	}
	if strings.Contains(view, "3/4 closed ✓") {
		t.Fatalf("partial wave must not render ✓ marker; view:\n%s", view)
	}
	if !strings.Contains(view, "ver-d") {
		t.Fatalf("partial wave summary must include blocked ticket ID; view:\n%s", view)
	}
}

func TestBubbleModel_FullyClosedWave_RendersOKMarker(t *testing.T) {
	// Control: the green ✓ marker still renders when every ticket closed.
	ch := make(chan engine.ProgressEvent)
	m := NewRunModel("run-test", ch)

	m = applyEvents(t, m, []engine.ProgressEvent{
		{
			Type:    engine.EventWaveStarted,
			WaveID:  1,
			Tickets: []string{"ver-a", "ver-b"},
		},
		{
			Type:    engine.EventWaveCompleted,
			WaveID:  1,
			Closed:  2,
			Total:   2,
			Success: true,
		},
	})

	view := viewString(m)
	if !strings.Contains(view, "2/2 closed") {
		t.Fatalf("view missing wave tally; view:\n%s", view)
	}
	if !strings.Contains(view, "✓") {
		t.Fatalf("fully closed wave must render ✓; view:\n%s", view)
	}
	if strings.Contains(view, "⚠") {
		t.Fatalf("fully closed wave must not render ⚠; view:\n%s", view)
	}
}

func TestBubbleModel_FailedWave_RendersFailMarker(t *testing.T) {
	// Control: a hard-failed wave must render as ✗ regardless of whether
	// BlockedTickets happens to accompany the event.
	ch := make(chan engine.ProgressEvent)
	m := NewRunModel("run-test", ch)

	m = applyEvents(t, m, []engine.ProgressEvent{
		{
			Type:    engine.EventWaveStarted,
			WaveID:  1,
			Tickets: []string{"ver-a", "ver-b"},
		},
		{
			Type:           engine.EventWaveCompleted,
			WaveID:         1,
			Closed:         0,
			Total:          2,
			Success:        false,
			BlockedTickets: []string{"ver-a", "ver-b"},
		},
	})

	view := viewString(m)
	if !strings.Contains(view, "✗") {
		t.Fatalf("failed wave must render ✗; view:\n%s", view)
	}
	if strings.Contains(view, "⚠") {
		t.Fatalf("failed wave must not render ⚠; view:\n%s", view)
	}
}

// TestBubbleModel_PartialWave_RendersExplicitBlockedDetails asserts that
// the TUI model carries EventWaveCompleted.BlockedTicketDetails into the
// wave summary so operators see the blocked ticket's phase, reason, and
// retry routing without having to read the wave artifact on disk.
func TestBubbleModel_PartialWave_RendersExplicitBlockedDetails(t *testing.T) {
	ch := make(chan engine.ProgressEvent)
	m := NewRunModel("run-test", ch)

	m = applyEvents(t, m, []engine.ProgressEvent{
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

	view := viewString(m)
	if !strings.Contains(view, "ver-d") {
		t.Fatalf("wave summary must include blocked ticket id; view:\n%s", view)
	}
	if !strings.Contains(view, "[blocked]") {
		t.Fatalf("wave summary must include phase [blocked]; view:\n%s", view)
	}
	if !strings.Contains(view, "non_convergent_verification") {
		t.Fatalf("wave summary must surface the block reason; view:\n%s", view)
	}
	if !strings.Contains(view, "requires operator") {
		t.Fatalf("wave summary must note that operator input is required; view:\n%s", view)
	}
}

// viewString is a compact helper around Model.View for tests.
func viewString(m Model) string {
	return m.View().Content
}

// compile-time check that Model satisfies tea.Model.
var _ tea.Model = Model{}
