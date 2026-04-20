package tui

import (
	"fmt"
	"strings"
	"time"
	"verk/internal/engine"
	"verk/internal/state"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
)

// doneMsg signals the progress channel has been closed.
type doneMsg struct{}

// tickMsg fires periodically to update elapsed times.
type tickMsg time.Time

// ticketLine tracks the progress of a single ticket.
type ticketLine struct {
	id         string
	title      string
	phases     []string
	active     string // current active operation (e.g., "claude worker running")
	done       bool
	ok         bool
	startedAt  time.Time
	finishedAt time.Time
}

// waveState tracks a wave.
type waveState struct {
	id             int
	parentTicketID string
	tickets        []string
	closed         int
	total          int
	done           bool
	// ok is true when the engine accepted the wave, which can include an
	// "accepted with warnings" state where some tickets did not close.
	ok             bool
	blockedIDs     []string
	blockedDetails []engine.BlockedTicketSummary
}

// Model is the Bubble Tea model for verk run progress.
type Model struct {
	runID           string
	ch              <-chan engine.ProgressEvent
	onCancel        func()
	tickets         map[string]*ticketLine
	waves           []waveState
	details         []string // rolling activity log (last 3)
	done            bool
	cancelRequested bool
	spinner         spinner.Model
	startTime       time.Time
}

// NewRunModel creates a new run progress model.
func NewRunModel(runID string, ch <-chan engine.ProgressEvent) Model {
	return NewRunModelWithCancel(runID, ch, nil)
}

// NewRunModelWithCancel creates a run progress model that invokes onCancel
// when the operator presses a cancellation key in the TUI.
func NewRunModelWithCancel(runID string, ch <-chan engine.ProgressEvent, onCancel func()) Model {
	s := spinner.New()
	s.Spinner = spinner.Dot
	return Model{
		runID:     runID,
		ch:        ch,
		onCancel:  onCancel,
		tickets:   make(map[string]*ticketLine),
		startTime: time.Now(),
		spinner:   s,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		waitForEvent(m.ch),
		m.spinner.Tick,
		tickEvery(),
	)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return m, nil

	case tea.KeyPressMsg:
		if isCancelKey(msg) {
			if !m.cancelRequested {
				m.cancelRequested = true
				m.addDetail("cancellation requested; stopping workers")
				if m.onCancel != nil {
					m.onCancel()
				}
			}
			return m, nil
		}
		return m, nil

	case engine.ProgressEvent:
		cmd := m.handleEvent(msg)
		if cmd != nil {
			return m, cmd
		}
		return m, waitForEvent(m.ch)

	case doneMsg:
		m.done = true
		return m, tea.Quit

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case tickMsg:
		// Just triggers a re-render for elapsed time updates
		return m, tickEvery()
	}
	return m, nil
}

func (m *Model) handleEvent(evt engine.ProgressEvent) tea.Cmd {
	switch evt.Type {
	case engine.EventWaveStarted:
		m.waves = append(m.waves, waveState{
			id:             evt.WaveID,
			parentTicketID: evt.ParentTicketID,
			tickets:        evt.Tickets,
			total:          len(evt.Tickets),
		})
		for _, id := range evt.Tickets {
			if _, exists := m.tickets[id]; !exists {
				m.tickets[id] = &ticketLine{id: id, startedAt: time.Now()}
			}
		}

	case engine.EventWaveCompleted:
		if len(m.waves) > 0 {
			w := &m.waves[len(m.waves)-1]
			w.closed = evt.Closed
			w.done = true
			w.ok = evt.Success
			w.blockedIDs = append([]string(nil), evt.BlockedTickets...)
			// Preserve the structured blocked-ticket explanations so the
			// wave summary can show phase / reason / retry routing per
			// ticket rather than collapsing the blockers to a bare list.
			w.blockedDetails = append([]engine.BlockedTicketSummary(nil), evt.BlockedTicketDetails...)
		}

	case engine.EventTicketPhaseChanged:
		tl := m.ensureTicket(evt.TicketID, evt.Title)
		switch evt.Phase {
		case state.TicketPhaseClosed:
			tl.phases = append(tl.phases, "✓")
			tl.done = true
			tl.ok = true
			tl.active = ""
			tl.finishedAt = time.Now()
		case state.TicketPhaseBlocked:
			tl.phases = append(tl.phases, "✗")
			tl.done = true
			tl.ok = false
			tl.active = ""
			tl.finishedAt = time.Now()
		default:
			tl.phases = append(tl.phases, shortPhaseName(evt.Phase))
			tl.active = ""
		}

	case engine.EventTicketDetail:
		tl := m.ensureTicket(evt.TicketID, evt.Title)
		tl.active = evt.Detail
		m.addDetail(fmt.Sprintf("%s: %s", evt.TicketID, evt.Detail))

	case engine.EventRunCompleted:
		m.done = true
		return tea.Quit
	}
	return nil
}

func isCancelKey(msg tea.KeyPressMsg) bool {
	switch msg.String() {
	case "ctrl+c", "ctrl+x":
		return true
	default:
		return false
	}
}

func (m *Model) ensureTicket(id, title string) *ticketLine {
	tl, exists := m.tickets[id]
	if !exists {
		tl = &ticketLine{id: id, title: title, startedAt: time.Now()}
		m.tickets[id] = tl
	}
	if title != "" && tl.title == "" {
		tl.title = title
	}
	return tl
}

func (m *Model) addDetail(line string) {
	m.details = append(m.details, line)
	if len(m.details) > 3 {
		m.details = m.details[len(m.details)-3:]
	}
}

func (m Model) View() tea.View {
	var b strings.Builder

	b.WriteString("\n")
	b.WriteString(styleHeader.Render(fmt.Sprintf("  Run: %s", m.runID)))
	b.WriteString("\n\n")

	for i := range m.waves {
		m.renderWave(&b, &m.waves[i], i == len(m.waves)-1)
	}

	// Activity log
	if len(m.details) > 0 {
		b.WriteString("\n")
		b.WriteString(styleDetailDim.Render("  Activity:") + "\n")
		for _, line := range m.details {
			b.WriteString("  " + styleDetailDim.Render("── "+line) + "\n")
		}
	}

	if m.done {
		b.WriteString("\n")
	}

	v := tea.NewView(b.String())
	v.DisableBracketedPasteMode = true
	return v
}

func (m Model) renderWave(b *strings.Builder, w *waveState, isCurrent bool) {
	// Wave header
	if w.done {
		// Three-way mark: green ✓ when every ticket closed, yellow ⚠ when the
		// wave was accepted but some tickets remain not-closed (soft blocked,
		// needs-context, etc.), red ✗ when the wave hard-failed.
		var mark string
		switch {
		case !w.ok:
			mark = styleWaveSummaryFail.Render("✗")
		case len(w.blockedIDs) > 0:
			mark = styleWaveSummaryPartial.Render("⚠")
		default:
			mark = styleWaveSummaryOK.Render("✓")
		}
		summary := fmt.Sprintf("  %s  %d/%d closed %s", waveLabel(w), w.closed, w.total, mark)
		if len(w.blockedIDs) > 0 {
			summary += " — blocked: " + strings.Join(w.blockedIDs, ", ")
		}
		if !isCurrent {
			b.WriteString(styleDivider.Render(summary) + "\n")
			// Past waves collapse to one line, but any blocked tickets stay
			// visible with their phase/reason/retry so the summary never
			// silently swallows work that still needs attention.
			renderBlockedTicketDetails(b, w.blockedDetails)
			return
		}
		b.WriteString(styleWaveTitle.Render(summary) + "\n")
		renderBlockedTicketDetails(b, w.blockedDetails)
	} else {
		b.WriteString(styleWaveTitle.Render(fmt.Sprintf("  %s", waveLabel(w))) + "\n")
	}
	divider := strings.Repeat("─", 62)
	b.WriteString("  " + styleDivider.Render(divider) + "\n")

	// Ticket lines
	for _, ticketID := range w.tickets {
		tl := m.tickets[ticketID]
		if tl == nil {
			continue
		}
		m.renderTicket(b, tl)
	}
	b.WriteString("\n")
}

// renderBlockedTicketDetails emits one line per blocked/skipped ticket with
// its phase, block reason, and retry routing so wave summaries never hide
// a blocked ticket behind a "3/4 closed" tally. Details are no-ops when the
// wave has none.
func renderBlockedTicketDetails(b *strings.Builder, details []engine.BlockedTicketSummary) {
	if len(details) == 0 {
		return
	}
	for _, d := range details {
		phase := string(d.Phase)
		if phase == "" {
			phase = "unknown"
		}
		reason := d.BlockReason
		if reason == "" {
			reason = "no block reason recorded"
		}
		var retry string
		switch {
		case d.RequiresOperator:
			retry = "requires operator"
		case d.CanRetryAutomatically:
			retry = "retry: verk run"
		default:
			retry = "manual follow-up"
		}
		line := fmt.Sprintf("    - %s [%s] %s (%s)", d.TicketID, phase, reason, retry)
		b.WriteString(styleDetailDim.Render(line) + "\n")
	}
}

func waveLabel(w *waveState) string {
	if w.parentTicketID != "" {
		return fmt.Sprintf("Sub-wave %d for %s", w.id, w.parentTicketID)
	}
	return fmt.Sprintf("Wave %d", w.id)
}

func (m Model) renderTicket(b *strings.Builder, tl *ticketLine) {
	// ID
	id := styleTicketID.Render(fmt.Sprintf("  %-10s", tl.id))

	// Title (truncated)
	title := ""
	if tl.title != "" {
		t := tl.title
		if len(t) > 26 {
			t = t[:23] + "..."
		}
		title = styleTicketTitle.Render(fmt.Sprintf("%-26s", t))
	} else {
		title = strings.Repeat(" ", 26)
	}

	// Phase chain + active state
	chain := m.renderPhaseChain(tl)

	// Elapsed time — frozen for done tickets, live for active
	elapsed := ""
	if !tl.startedAt.IsZero() {
		var dur time.Duration
		if tl.done && !tl.finishedAt.IsZero() {
			dur = tl.finishedAt.Sub(tl.startedAt)
		} else {
			dur = time.Since(tl.startedAt)
		}
		elapsed = styleElapsed.Render(fmt.Sprintf(" (%s)", formatDuration(dur)))
	}

	b.WriteString(id + " " + title + " " + chain + elapsed + "\n")
}

func (m Model) renderPhaseChain(tl *ticketLine) string {
	if len(tl.phases) == 0 {
		return stylePhaseWait.Render("waiting")
	}

	parts := make([]string, 0, len(tl.phases))
	for _, phase := range tl.phases {
		switch phase {
		case "✓":
			parts = append(parts, stylePhaseDone.Render("✓"))
		case "✗":
			parts = append(parts, stylePhaseFail.Render("✗"))
		default:
			parts = append(parts, stylePhaseChain.Render(phase))
		}
	}

	chain := strings.Join(parts, styleDivider.Render(" → "))

	// Show active operation with spinner
	if tl.active != "" && !tl.done {
		active := tl.active
		if len(active) > 30 {
			active = active[:27] + "..."
		}
		chain += " " + m.spinner.View() + " " + styleActive.Render(active)
	}

	return chain
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%dm%02ds", m, s)
}

// waitForEvent returns a tea.Cmd that reads the next event from the channel.
func waitForEvent(ch <-chan engine.ProgressEvent) tea.Cmd {
	return func() tea.Msg {
		evt, ok := <-ch
		if !ok {
			return doneMsg{}
		}
		return evt
	}
}

// tickEvery returns a cmd that fires a tickMsg every second for elapsed time updates.
func tickEvery() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func shortPhaseName(phase state.TicketPhase) string {
	switch phase {
	case state.TicketPhaseImplement:
		return "impl"
	case state.TicketPhaseVerify:
		return "verify"
	case state.TicketPhaseReview:
		return "review"
	case state.TicketPhaseRepair:
		return "repair"
	case state.TicketPhaseCloseout:
		return "close"
	default:
		return string(phase)
	}
}
