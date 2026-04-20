package tui

import (
	"regexp"
	"strings"
	"testing"
	"verk/internal/engine"
	"verk/internal/state"

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

	view := stripANSI(viewString(m))
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

	view := stripANSI(viewString(m))
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

	view := stripANSI(viewString(m))
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

	view := stripANSI(viewString(m))
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

// TestBubbleModel_NoDuplicateWaveHeaders_OnMultipleStartedEvents covers TC5:
// when the engine emits two EventWaveStarted events for the same wave (e.g.
// due to resume or sub-wave progress ordering), the TUI must render the wave
// header exactly once — not twice.
func TestBubbleModel_NoDuplicateWaveHeaders_OnMultipleStartedEvents(t *testing.T) {
	ch := make(chan engine.ProgressEvent)
	m := NewRunModel("run-test", ch)

	m = applyEvents(t, m, []engine.ProgressEvent{
		{Type: engine.EventWaveStarted, WaveID: 1, Tickets: []string{"ver-a"}},
		{Type: engine.EventWaveStarted, WaveID: 1, Tickets: []string{"ver-a"}}, // duplicate
	})

	view := stripANSI(viewString(m))
	// "Wave 1" must appear exactly once in the rendered view.
	count := strings.Count(view, "Wave 1")
	if count != 1 {
		t.Fatalf("expected exactly 1 Wave 1 header, got %d; view:\n%s", count, view)
	}
}

func TestBubbleModel_ActiveTicketRendersActivityOnSeparateIndentedLine(t *testing.T) {
	ch := make(chan engine.ProgressEvent)
	m := NewRunModel("run-test", ch)

	title := "Ignore local setup file handling during local setup"
	detail := "$ cat /Users/runger/workspaces/modal-mcp/.env.example"
	m = applyEvents(t, m, []engine.ProgressEvent{
		{Type: engine.EventWaveStarted, WaveID: 1, Tickets: []string{"mm-z1sx"}},
		{Type: engine.EventTicketPhaseChanged, TicketID: "mm-z1sx", Title: title, Phase: state.TicketPhaseImplement},
		{Type: engine.EventTicketPhaseChanged, TicketID: "mm-z1sx", Title: title, Phase: state.TicketPhaseVerify},
		{Type: engine.EventTicketPhaseChanged, TicketID: "mm-z1sx", Title: title, Phase: state.TicketPhaseReview},
		{Type: engine.EventTicketDetail, TicketID: "mm-z1sx", Title: title, Detail: detail},
	})

	view := stripANSI(viewString(m))
	ticketLine := lineContaining(view, "mm-z1sx")
	if ticketLine == "" {
		t.Fatalf("view must contain ticket line; view:\n%s", view)
	}
	if !strings.Contains(ticketLine, title) {
		t.Fatalf("active ticket title should have room on identity line; line: %q\nview:\n%s", ticketLine, view)
	}
	if strings.Contains(ticketLine, detail) {
		t.Fatalf("active detail must not be appended to ticket identity line; line: %q", ticketLine)
	}

	phaseLine := lineContaining(view, "impl → verify → review")
	if !strings.HasPrefix(phaseLine, "             ") {
		t.Fatalf("phase chain should render on an indented continuation line; line: %q\nview:\n%s", phaseLine, view)
	}

	detailLine := lineContaining(view, detail)
	if !strings.HasPrefix(detailLine, "             ") {
		t.Fatalf("active detail should render on an indented continuation line; line: %q\nview:\n%s", detailLine, view)
	}
	if strings.Contains(detailLine, "impl → verify → review") {
		t.Fatalf("active detail line should not repeat the phase chain; line: %q", detailLine)
	}
}

func TestBubbleModel_ClosedTicketKeepsCompactSingleLine(t *testing.T) {
	ch := make(chan engine.ProgressEvent)
	m := NewRunModel("run-test", ch)

	m = applyEvents(t, m, []engine.ProgressEvent{
		{Type: engine.EventWaveStarted, WaveID: 1, Tickets: []string{"mm-done"}},
		{Type: engine.EventTicketPhaseChanged, TicketID: "mm-done", Title: "Closed ticket stays compact", Phase: state.TicketPhaseImplement},
		{Type: engine.EventTicketPhaseChanged, TicketID: "mm-done", Title: "Closed ticket stays compact", Phase: state.TicketPhaseClosed},
	})

	view := stripANSI(viewString(m))
	if strings.Contains(view, "\n             impl → ✓") {
		t.Fatalf("closed tickets should keep the compact one-line layout; view:\n%s", view)
	}
	ticketLine := lineContaining(view, "mm-done")
	if !strings.Contains(ticketLine, "impl → ✓") {
		t.Fatalf("closed ticket should keep phase chain on the ticket line; line: %q\nview:\n%s", ticketLine, view)
	}
}

// TestBubbleModel_RepairCycleStarted_AppearsInActivityLog covers that repair
// cycle events are recorded in the rolling activity log so operators can see
// which checks triggered the repair from within the TUI.
func TestBubbleModel_RepairCycleStarted_AppearsInActivityLog(t *testing.T) {
	ch := make(chan engine.ProgressEvent)
	m := NewRunModel("run-test", ch)

	m = applyEvents(t, m, []engine.ProgressEvent{
		{
			Type:            engine.EventRepairCycleStarted,
			RepairCycle:     1,
			MaxRepairCycles: 3,
			CheckIDs:        []string{"check-go-test"},
		},
	})

	view := viewString(m)
	if !strings.Contains(view, "repair") {
		t.Fatalf("repair event must appear in activity log; view:\n%s", view)
	}
	if !strings.Contains(view, "check-go-test") {
		t.Fatalf("repair event must name the check; view:\n%s", view)
	}
}

// TestBubbleModel_RepairCycleSucceeded_ShowsRepairedInActivityLog covers TC2
// for the TUI: a successful repair cycle must be visible in the activity log
// so the operator sees the fix status before the final wave summary.
func TestBubbleModel_RepairCycleSucceeded_ShowsRepairedInActivityLog(t *testing.T) {
	ch := make(chan engine.ProgressEvent)
	m := NewRunModel("run-test", ch)

	m = applyEvents(t, m, []engine.ProgressEvent{
		{
			Type:            engine.EventRepairCycleStarted,
			RepairCycle:     1,
			MaxRepairCycles: 2,
			CheckIDs:        []string{"check-go-test"},
		},
		{
			Type:        engine.EventRepairCycleSucceeded,
			RepairCycle: 1,
		},
	})

	view := viewString(m)
	if !strings.Contains(view, "repaired") {
		t.Fatalf("repair success must show 'repaired' in activity log; view:\n%s", view)
	}
}

// TestBubbleModel_RepairCycleExhausted_ShowsExhaustionInActivityLog covers TC3
// for the TUI: when repair budget is exhausted, the activity log must show the
// failing check IDs and that manual follow-up is required.
func TestBubbleModel_RepairCycleExhausted_ShowsExhaustionInActivityLog(t *testing.T) {
	ch := make(chan engine.ProgressEvent)
	m := NewRunModel("run-test", ch)

	m = applyEvents(t, m, []engine.ProgressEvent{
		{
			Type:            engine.EventRepairCycleExhausted,
			RepairCycle:     2,
			MaxRepairCycles: 2,
			CheckIDs:        []string{"check-go-test"},
		},
	})

	view := viewString(m)
	if !strings.Contains(view, "check-go-test") {
		t.Fatalf("exhaustion must name the failing check in activity log; view:\n%s", view)
	}
	if !strings.Contains(view, "manual follow-up") {
		t.Fatalf("exhaustion must say manual follow-up required; view:\n%s", view)
	}
}

// TestBubbleModel_WaveCompletedMatchesByID verifies that EventWaveCompleted
// correctly matches its wave by ID and parent, not always the last slot. This
// guards against the previous repeated-wave output bug where an
// EventWaveCompleted for sub-wave 1 (parent=ver-x) would corrupt the top-level
// wave 1 state when both existed in m.waves.
func TestBubbleModel_WaveCompletedMatchesByID(t *testing.T) {
	ch := make(chan engine.ProgressEvent)
	m := NewRunModel("run-test", ch)

	m = applyEvents(t, m, []engine.ProgressEvent{
		// Two distinct waves: top-level wave 1 and sub-wave 1 for ver-x
		{Type: engine.EventWaveStarted, WaveID: 1, ParentTicketID: "", Tickets: []string{"ver-x"}},
		{Type: engine.EventWaveStarted, WaveID: 1, ParentTicketID: "ver-x", Tickets: []string{"ver-y"}},
		// Complete the sub-wave — must not clobber the top-level wave
		{
			Type:           engine.EventWaveCompleted,
			WaveID:         1,
			ParentTicketID: "ver-x",
			Closed:         1,
			Total:          1,
			Success:        true,
		},
	})

	// Top-level wave 1 (parentTicketID="") must still be in progress (not done)
	topLevelWave := m.findWave(1, "")
	if topLevelWave == nil {
		t.Fatal("top-level wave must exist in model")
	}
	if topLevelWave.done {
		t.Fatalf("top-level wave must not be marked done by sub-wave completion; wave: %+v", *topLevelWave)
	}

	// Sub-wave 1 must be done
	subWave := m.findWave(1, "ver-x")
	if subWave == nil {
		t.Fatal("sub-wave must exist in model")
	}
	if !subWave.done {
		t.Fatalf("sub-wave must be marked done after EventWaveCompleted; wave: %+v", *subWave)
	}
}

func TestBubbleModel_CancelKeysInvokeCancelCallback(t *testing.T) {
	tests := []struct {
		name string
		key  tea.KeyPressMsg
	}{
		{
			name: "ctrl+c",
			key:  tea.KeyPressMsg(tea.Key{Mod: tea.ModCtrl, Code: 'c'}),
		},
		{
			name: "ctrl+x",
			key:  tea.KeyPressMsg(tea.Key{Mod: tea.ModCtrl, Code: 'x'}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch := make(chan engine.ProgressEvent)
			cancelCalls := 0
			m := NewRunModelWithCancel("run-test", ch, func() {
				cancelCalls++
			})

			next, _ := m.Update(tt.key)
			casted, ok := next.(Model)
			if !ok {
				t.Fatalf("Update returned non-Model tea.Model: %T", next)
			}
			if cancelCalls != 1 {
				t.Fatalf("expected cancel callback once, got %d", cancelCalls)
			}
			if !casted.cancelRequested {
				t.Fatal("model should remember cancellation was requested")
			}

			_, _ = casted.Update(tt.key)
			if cancelCalls != 1 {
				t.Fatalf("cancel callback should be idempotent, got %d calls", cancelCalls)
			}
		})
	}
}

// viewString is a compact helper around Model.View for tests.
func viewString(m Model) string {
	return m.View().Content
}

func lineContaining(s, needle string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	return ""
}

var ansiSequence = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string {
	return ansiSequence.ReplaceAllString(s, "")
}

// compile-time check that Model satisfies tea.Model.
var _ tea.Model = Model{}
