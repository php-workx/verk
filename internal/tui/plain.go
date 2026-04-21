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
			_, _ = fmt.Fprintf(w, "[%s] %s\n", plainWaveLabel(evt), strings.Join(evt.Tickets, ", "))

		case engine.EventWaveCompleted:
			// Three-way marker so partial (accepted-with-warnings) waves are
			// visually distinct from fully-closed and hard-failed waves.
			mark := "✓"
			switch {
			case !evt.Success:
				mark = "✗"
			case len(evt.BlockedTickets) > 0:
				mark = "⚠"
			}
			line := fmt.Sprintf("[%s] %d/%d closed %s", plainWaveLabel(evt), evt.Closed, evt.Total, mark)
			if len(evt.BlockedTickets) > 0 {
				line += " — blocked: " + strings.Join(evt.BlockedTickets, ", ")
			}
			_, _ = fmt.Fprintln(w, line)
			// Render per-blocked-ticket explanations so the summary never
			// hides a blocked ticket behind a "3/4 closed" tally.
			for _, d := range evt.BlockedTicketDetails {
				renderPlainBlockedTicket(w, d)
			}

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
			// Prefix with the ticket ID when the engine attributes the detail
			// to a specific ticket; otherwise emit a bare activity line. This
			// preserves the ticket ID in the final blocked-epic summary where
			// the engine emits one detail line per non-closed child.
			if evt.TicketID != "" {
				_, _ = fmt.Fprintf(w, "           %s: %s\n", evt.TicketID, evt.Detail)
			} else {
				_, _ = fmt.Fprintf(w, "           %s\n", evt.Detail)
			}

		case engine.EventRepairCycleStarted:
			// Show which checks triggered the repair cycle so the operator can
			// correlate output with the check that failed.
			if len(evt.CheckIDs) > 0 {
				_, _ = fmt.Fprintf(w, "  [repair] cycle %d/%d: %s\n",
					evt.RepairCycle, evt.MaxRepairCycles, strings.Join(evt.CheckIDs, ", "))
			} else {
				_, _ = fmt.Fprintf(w, "  [repair] cycle %d/%d started\n",
					evt.RepairCycle, evt.MaxRepairCycles)
			}

		case engine.EventRepairCycleSucceeded:
			// A short "repaired" line so the operator sees the fix rather than
			// only the final wave-closed or ticket-closed summary.
			_, _ = fmt.Fprintf(w, "  [repair] cycle %d: repaired ✓\n", evt.RepairCycle)

		case engine.EventRepairCycleExhausted:
			// Show the failing checks and the next action so operators know what
			// manual work is required. This mirrors the blocked-ticket guidance
			// style used by renderPlainBlockedTicket.
			if len(evt.CheckIDs) > 0 {
				_, _ = fmt.Fprintf(w, "  [repair] exhausted after %d cycle(s): %s — manual follow-up required\n",
					evt.RepairCycle, strings.Join(evt.CheckIDs, ", "))
			} else {
				_, _ = fmt.Fprintf(w, "  [repair] exhausted after %d cycle(s) — manual follow-up required\n",
					evt.RepairCycle)
			}

		case engine.EventRunCompleted:
			// nothing to print — the caller handles final status
		}
	}
}

func plainWaveLabel(evt engine.ProgressEvent) string {
	if evt.ParentTicketID != "" {
		return fmt.Sprintf("sub-wave %d for %s", evt.WaveID, evt.ParentTicketID)
	}
	return fmt.Sprintf("wave %d", evt.WaveID)
}

// renderPlainBlockedTicket writes a single explicit blocked-ticket line,
// covering the acceptance-criteria items for ver-ssp3: ticket id, phase,
// block reason, whether user input is required, and whether `verk run` can
// retry automatically.
func renderPlainBlockedTicket(w io.Writer, d engine.BlockedTicketSummary) {
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
	_, _ = fmt.Fprintf(w, "           - %s [%s] %s (%s)\n", d.TicketID, phase, reason, retry)
}
