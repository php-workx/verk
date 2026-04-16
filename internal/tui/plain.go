package tui

import (
	"fmt"
	"io"
	"strings"
	"verk/internal/engine"
	"verk/internal/state"
)

// RunPlainProgress reads progress events and writes formatted log lines.
// Used when stdout is not a terminal (piped, redirected, CI).
func RunPlainProgress(w io.Writer, ch <-chan engine.ProgressEvent) {
	for evt := range ch {
		switch evt.Type {
		case engine.EventWaveStarted:
			_, _ = fmt.Fprintf(w, "[wave %d] %s\n", evt.WaveID, strings.Join(evt.Tickets, ", "))

		case engine.EventWaveCompleted:
			mark := "✓"
			if !evt.Success {
				mark = "✗"
			}
			_, _ = fmt.Fprintf(w, "[wave %d] %d/%d closed %s\n", evt.WaveID, evt.Closed, evt.Total, mark)

		case engine.EventTicketPhaseChanged:
			label := evt.TicketID
			if evt.Title != "" {
				label = evt.TicketID + " " + evt.Title
			}
			switch evt.Phase {
			case state.TicketPhaseClosed:
				_, _ = fmt.Fprintf(w, "  [ticket] %s ✓ closed\n", label)
			case state.TicketPhaseBlocked:
				if evt.Detail != "" {
					_, _ = fmt.Fprintf(w, "  [ticket] %s ✗ blocked (%s)\n", label, evt.Detail)
				} else {
					_, _ = fmt.Fprintf(w, "  [ticket] %s ✗ blocked\n", label)
				}
			default:
				phase := string(evt.Phase)
				if evt.Detail != "" {
					_, _ = fmt.Fprintf(w, "  [ticket] %s → %s (%s)\n", evt.TicketID, phase, evt.Detail)
				} else {
					_, _ = fmt.Fprintf(w, "  [ticket] %s → %s\n", evt.TicketID, phase)
				}
			}

		case engine.EventTicketDetail:
			_, _ = fmt.Fprintf(w, "           %s\n", evt.Detail)

		case engine.EventRunCompleted:
			// nothing to print — the caller handles final status
		}
	}
}
