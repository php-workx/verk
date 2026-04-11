package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"verk/internal/engine"
	"verk/internal/state"
)

// doneMsg signals the progress channel has been closed.
type doneMsg struct{}

// errMsg carries an engine error.
type errMsg struct{ err error }

// ticketState tracks the progress of a single ticket.
type ticketState struct {
	id     string
	title  string
	phases []state.TicketPhase // history of phases reached
	detail string             // latest sub-phase detail
	done   bool               // terminal state reached (closed or blocked)
	ok     bool               // closed (true) vs blocked (false)
}

// waveState tracks a completed or in-progress wave.
type waveState struct {
	id      int
	tickets []string
	closed  int
	total   int
	done    bool
	ok      bool
}

// Model is the Bubble Tea model for verk run progress.
type Model struct {
	runID    string
	ch       <-chan engine.ProgressEvent
	tickets  map[string]*ticketState
	waves    []waveState
	details  []string // recent detail lines (rolling buffer)
	done     bool
	err      error
	width    int
}

// NewRunModel creates a new run progress model.
func NewRunModel(runID string, ch <-chan engine.ProgressEvent) Model {
	return Model{
		runID:   runID,
		ch:      ch,
		tickets: make(map[string]*ticketState),
		width:   80,
	}
}

func (m Model) Init() tea.Cmd {
	return waitForEvent(m.ch)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil

	case engine.ProgressEvent:
		m.handleEvent(msg)
		return m, waitForEvent(m.ch)

	case doneMsg:
		m.done = true
		return m, tea.Quit

	case errMsg:
		m.err = msg.err
		m.done = true
		return m, tea.Quit

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			m.done = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *Model) handleEvent(evt engine.ProgressEvent) {
	switch evt.Type {
	case engine.EventWaveStarted:
		m.waves = append(m.waves, waveState{
			id:      evt.WaveID,
			tickets: evt.Tickets,
			total:   len(evt.Tickets),
		})
		// Initialize ticket states for this wave
		for _, id := range evt.Tickets {
			if _, exists := m.tickets[id]; !exists {
				m.tickets[id] = &ticketState{id: id}
			}
		}

	case engine.EventWaveCompleted:
		if len(m.waves) > 0 {
			w := &m.waves[len(m.waves)-1]
			w.closed = evt.Closed
			w.done = true
			w.ok = evt.Success
		}

	case engine.EventTicketPhaseChanged:
		ts := m.ensureTicket(evt.TicketID, evt.Title)
		ts.phases = append(ts.phases, evt.Phase)
		if evt.Phase == state.TicketPhaseClosed {
			ts.done = true
			ts.ok = true
		} else if evt.Phase == state.TicketPhaseBlocked {
			ts.done = true
			ts.ok = false
		}
		if evt.Detail != "" {
			ts.detail = evt.Detail
		}

	case engine.EventTicketDetail:
		ts := m.ensureTicket(evt.TicketID, evt.Title)
		ts.detail = evt.Detail
		m.addDetail(fmt.Sprintf("%s: %s", evt.TicketID, evt.Detail))

	case engine.EventRunCompleted:
		m.done = true
	}
}

func (m *Model) ensureTicket(id, title string) *ticketState {
	ts, exists := m.tickets[id]
	if !exists {
		ts = &ticketState{id: id, title: title}
		m.tickets[id] = ts
	}
	if title != "" && ts.title == "" {
		ts.title = title
	}
	return ts
}

func (m *Model) addDetail(line string) {
	m.details = append(m.details, line)
	if len(m.details) > 3 {
		m.details = m.details[len(m.details)-3:]
	}
}

func (m Model) View() tea.View {
	var b strings.Builder

	// Header
	b.WriteString(styleRunHeader.Render(fmt.Sprintf("  Run: %s", m.runID)))
	b.WriteString("\n\n")

	// Render each wave
	for i := range m.waves {
		m.renderWave(&b, &m.waves[i])
	}

	// Detail area (last 3 activity lines)
	if len(m.details) > 0 {
		b.WriteString("\n")
		for _, line := range m.details {
			b.WriteString("  " + styleDetailLine.Render("── "+line) + "\n")
		}
	}

	// Done message
	if m.done {
		b.WriteString("\n")
		if m.err != nil {
			b.WriteString("  " + styleCheckFail.Render(fmt.Sprintf("Error: %s", m.err)) + "\n")
		}
	}

	return tea.NewView(b.String())
}

func (m Model) renderWave(b *strings.Builder, w *waveState) {
	// Wave header
	header := fmt.Sprintf("  Wave %d", w.id)
	if w.done {
		mark := styleCheckOK.Render("✓")
		if !w.ok {
			mark = styleCheckFail.Render("✗")
		}
		header += fmt.Sprintf("  %d/%d closed %s", w.closed, w.total, mark)
	}
	b.WriteString(styleWaveHeader.Render(header) + "\n")
	b.WriteString("  " + styleDivider.Render(strings.Repeat("─", 38)) + "\n")

	// Ticket lines
	for _, ticketID := range w.tickets {
		ts := m.tickets[ticketID]
		if ts == nil {
			continue
		}
		m.renderTicket(b, ts)
	}
	b.WriteString("\n")
}

func (m Model) renderTicket(b *strings.Builder, ts *ticketState) {
	// ID + title
	id := styleTicketID.Render(fmt.Sprintf("  %-10s", ts.id))
	title := ""
	if ts.title != "" {
		t := ts.title
		if len(t) > 28 {
			t = t[:25] + "..."
		}
		title = styleTitle.Render(fmt.Sprintf("%-28s", t))
	}

	// Phase chain
	chain := renderPhaseChain(ts.phases, ts.done, ts.ok)

	b.WriteString(id + " " + title + " " + chain + "\n")
}

func renderPhaseChain(phases []state.TicketPhase, done, ok bool) string {
	if len(phases) == 0 {
		return stylePhaseWait.Render("waiting...")
	}

	parts := make([]string, 0, len(phases))
	for i, phase := range phases {
		name := shortPhaseName(phase)
		isLast := i == len(phases)-1

		if phase == state.TicketPhaseClosed {
			parts = append(parts, stylePhaseDone.Render("✓"))
		} else if phase == state.TicketPhaseBlocked {
			parts = append(parts, stylePhaseFail.Render("✗"))
		} else if isLast && !done {
			parts = append(parts, stylePhaseActive.Render(name+" ..."))
		} else {
			parts = append(parts, stylePhaseActive.Render(name))
		}
	}

	return strings.Join(parts, styleDivider.Render(" → "))
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
