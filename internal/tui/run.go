package tui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"verk/internal/engine"
	"verk/internal/state"
)

// RunProgress starts the appropriate progress display for a verk run.
// Uses an in-place updating display if stdout is a terminal, plain log
// output otherwise.
func RunProgress(runID string, ch <-chan engine.ProgressEvent, w io.Writer) error {
	if isTerminal() {
		return runInPlace(runID, ch, w)
	}
	RunPlainProgress(w, ch)
	return nil
}

func isTerminal() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// runInPlace renders progress with ANSI cursor control — each ticket gets
// a line that updates in-place. No Bubble Tea, no terminal capability queries.
func runInPlace(runID string, ch <-chan engine.ProgressEvent, w io.Writer) error {
	d := &inPlaceDisplay{
		w:       w,
		tickets: make(map[string]*ticketLine),
	}

	for evt := range ch {
		d.handle(evt)
	}
	// Final newline after the display
	fmt.Fprintln(w)
	return nil
}

type ticketLine struct {
	id     string
	title  string
	phases []string
	done   bool
	ok     bool
}

type inPlaceDisplay struct {
	mu           sync.Mutex
	w            io.Writer
	tickets      map[string]*ticketLine
	waveTickets  []string // ticket IDs in current wave (ordered)
	waveID       int
	lastDetail   string
	renderedLines int // how many lines we last rendered (for cursor-up rewrite)
}

func (d *inPlaceDisplay) handle(evt engine.ProgressEvent) {
	d.mu.Lock()
	defer d.mu.Unlock()

	switch evt.Type {
	case engine.EventWaveStarted:
		// If there was a previous wave, freeze it (don't rewrite)
		if d.waveID > 0 {
			d.renderedLines = 0
		}
		d.waveID = evt.WaveID
		d.waveTickets = evt.Tickets
		for _, id := range evt.Tickets {
			if _, exists := d.tickets[id]; !exists {
				d.tickets[id] = &ticketLine{id: id}
			}
		}
		d.lastDetail = ""

	case engine.EventWaveCompleted:
		mark := "✓"
		if !evt.Success {
			mark = "✗"
		}
		d.lastDetail = fmt.Sprintf("wave %d: %d/%d closed %s", evt.WaveID, evt.Closed, evt.Total, mark)

	case engine.EventTicketPhaseChanged:
		tl := d.ensureTicket(evt.TicketID, evt.Title)
		phase := shortPhase(evt.Phase)
		if evt.Phase == state.TicketPhaseClosed {
			tl.done = true
			tl.ok = true
			tl.phases = append(tl.phases, "✓")
		} else if evt.Phase == state.TicketPhaseBlocked {
			tl.done = true
			tl.ok = false
			tl.phases = append(tl.phases, "✗")
		} else {
			tl.phases = append(tl.phases, phase)
		}

	case engine.EventTicketDetail:
		d.ensureTicket(evt.TicketID, evt.Title)
		d.lastDetail = fmt.Sprintf("%s: %s", evt.TicketID, evt.Detail)
	}

	d.render()
}

func (d *inPlaceDisplay) ensureTicket(id, title string) *ticketLine {
	tl, exists := d.tickets[id]
	if !exists {
		tl = &ticketLine{id: id, title: title}
		d.tickets[id] = tl
	}
	if title != "" && tl.title == "" {
		tl.title = title
	}
	return tl
}

func (d *inPlaceDisplay) render() {
	// Move cursor up to overwrite previous render
	if d.renderedLines > 0 {
		fmt.Fprintf(d.w, "\033[%dA", d.renderedLines)
	}

	lines := 0

	// Wave header
	fmt.Fprintf(d.w, "\033[2K  Wave %d\n", d.waveID)
	lines++
	fmt.Fprintf(d.w, "\033[2K  %s\n", strings.Repeat("─", 60))
	lines++

	// Ticket lines
	for _, id := range d.waveTickets {
		tl := d.tickets[id]
		if tl == nil {
			continue
		}

		title := tl.title
		if len(title) > 28 {
			title = title[:25] + "..."
		}

		chain := strings.Join(tl.phases, " → ")
		if len(tl.phases) > 0 && !tl.done {
			chain += " ..."
		}
		if chain == "" {
			chain = "waiting"
		}

		fmt.Fprintf(d.w, "\033[2K  %-10s %-28s %s\n", tl.id, title, chain)
		lines++
	}

	// Detail line
	fmt.Fprintf(d.w, "\033[2K\n", )
	lines++
	if d.lastDetail != "" {
		fmt.Fprintf(d.w, "\033[2K  ── %s\n", d.lastDetail)
		lines++
	}

	d.renderedLines = lines
}

func shortPhase(phase state.TicketPhase) string {
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
