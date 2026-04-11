package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/spinner"

	"verk/internal/engine"
	"verk/internal/state"
)

// doneMsg signals the progress channel has been closed.
type doneMsg struct{}

// tickMsg fires periodically to update elapsed times.
type tickMsg time.Time

// ticketLine tracks the progress of a single ticket.
type ticketLine struct {
	id        string
	title     string
	phases    []string
	active    string // current active operation (e.g., "claude worker running")
	done      bool
	ok        bool
	startedAt time.Time
}

// waveState tracks a wave.
type waveState struct {
	id       int
	tickets  []string
	closed   int
	total    int
	done     bool
	ok       bool
}

// Model is the Bubble Tea model for verk run progress.
type Model struct {
	runID     string
	ch        <-chan engine.ProgressEvent
	tickets   map[string]*ticketLine
	waves     []waveState
	details   []string // rolling activity log (last 3)
	done      bool
	err       error
	spinner   spinner.Model
	startTime time.Time
}

// NewRunModel creates a new run progress model.
func NewRunModel(runID string, ch <-chan engine.ProgressEvent) Model {
	s := spinner.New()
	s.Spinner = spinner.Dot
	return Model{
		runID:     runID,
		ch:        ch,
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

	case engine.ProgressEvent:
		m.handleEvent(msg)
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

func (m *Model) handleEvent(evt engine.ProgressEvent) {
	switch evt.Type {
	case engine.EventWaveStarted:
		m.waves = append(m.waves, waveState{
			id:      evt.WaveID,
			tickets: evt.Tickets,
			total:   len(evt.Tickets),
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
		}

	case engine.EventTicketPhaseChanged:
		tl := m.ensureTicket(evt.TicketID, evt.Title)
		if evt.Phase == state.TicketPhaseClosed {
			tl.phases = append(tl.phases, "✓")
			tl.done = true
			tl.ok = true
			tl.active = ""
		} else if evt.Phase == state.TicketPhaseBlocked {
			tl.phases = append(tl.phases, "✗")
			tl.done = true
			tl.ok = false
			tl.active = ""
		} else {
			tl.phases = append(tl.phases, shortPhaseName(evt.Phase))
			tl.active = ""
		}

	case engine.EventTicketDetail:
		tl := m.ensureTicket(evt.TicketID, evt.Title)
		tl.active = evt.Detail
		m.addDetail(fmt.Sprintf("%s: %s", evt.TicketID, evt.Detail))

	case engine.EventRunCompleted:
		m.done = true
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

	return tea.NewView(b.String())
}

func (m Model) renderWave(b *strings.Builder, w *waveState, isCurrent bool) {
	// Wave header
	if w.done {
		mark := styleWaveSummaryOK.Render("✓")
		if !w.ok {
			mark = styleWaveSummaryFail.Render("✗")
		}
		summary := fmt.Sprintf("  Wave %d  %d/%d closed %s", w.id, w.closed, w.total, mark)
		if !isCurrent {
			b.WriteString(styleDivider.Render(summary) + "\n")
			return // Collapsed — don't show ticket lines for past waves
		}
		b.WriteString(styleWaveTitle.Render(summary) + "\n")
	} else {
		b.WriteString(styleWaveTitle.Render(fmt.Sprintf("  Wave %d", w.id)) + "\n")
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

	// Elapsed time
	elapsed := ""
	if !tl.startedAt.IsZero() && !tl.done {
		elapsed = styleElapsed.Render(fmt.Sprintf(" (%s)", formatDuration(time.Since(tl.startedAt))))
	} else if tl.done && !tl.startedAt.IsZero() {
		elapsed = styleElapsed.Render(fmt.Sprintf(" (%s)", formatDuration(time.Since(tl.startedAt))))
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
