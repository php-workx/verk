package engine

import (
	"context"
	"time"
	"verk/internal/state"
)

// ProgressEventType identifies the kind of progress event.
type ProgressEventType int

const (
	EventWaveStarted          ProgressEventType = iota // New wave beginning
	EventWaveCompleted                                 // Wave finished (accepted or failed)
	EventTicketPhaseChanged                            // Ticket transitioned to a new phase
	EventTicketDetail                                  // Sub-phase activity (worker launch, verify result, etc.)
	EventRunCompleted                                  // Entire run finished
	EventRepairCycleStarted                            // Repair cycle dispatched for a ticket or wave
	EventRepairCycleSucceeded                          // Repair cycle fixed all failing checks
	EventRepairCycleExhausted                          // Repair budget exhausted; checks still failing
)

// ProgressEvent is a structured progress notification from the engine.
// Sent on the Progress channel during RunTicket, RunEpic, and ResumeRun.
//
// For sub-wave events (EventWaveStarted / EventWaveCompleted emitted from
// runSubEpic), ParentTicketID is set to the ID of the parent ticket whose
// children are being executed. This distinguishes sub-wave ordinals from
// top-level wave ordinals: a top-level wave-2 and a sub-wave-2 for ticket-X
// are unambiguously different because the sub-wave event carries
// ParentTicketID="ticket-X".
//
// BlockedTickets carries the IDs of tickets in the wave that did not reach
// the Closed phase (soft blocked / needs-context / implement / verify /
// review / repair / etc.). Consumers use len(BlockedTickets) > 0 combined
// with Success to distinguish fully closed (✓), partial/accepted-with-warnings
// (⚠) and hard-failed (✗) waves.
type ProgressEvent struct {
	Time                 time.Time              `json:"time"`
	Type                 ProgressEventType      `json:"type"`
	TicketID             string                 `json:"ticket_id,omitempty"`
	Title                string                 `json:"title,omitempty"`
	WaveID               int                    `json:"wave_id,omitempty"`
	ParentTicketID       string                 `json:"parent_ticket_id,omitempty"`
	Phase                state.TicketPhase      `json:"phase,omitempty"`
	Detail               string                 `json:"detail,omitempty"`
	Closed               int                    `json:"closed,omitempty"`
	Total                int                    `json:"total,omitempty"`
	Tickets              []string               `json:"tickets,omitempty"`
	BlockedTickets       []string               `json:"blocked_tickets,omitempty"`
	Success              bool                   `json:"success,omitempty"`
	BlockedTicketDetails []BlockedTicketSummary `json:"blocked_ticket_details,omitempty"`
	// RepairCycle is the ordinal of the repair cycle this event describes.
	// Set on EventRepairCycleStarted, EventRepairCycleSucceeded, and
	// EventRepairCycleExhausted events.
	RepairCycle int `json:"repair_cycle,omitempty"`
	// MaxRepairCycles is the policy limit on repair cycles. Set on repair
	// events so consumers can render "cycle N/M" without loading policy config.
	MaxRepairCycles int `json:"max_repair_cycles,omitempty"`
	// CheckIDs contains the identifiers of the checks relevant to this event.
	// On repair events it lists the failing checks that triggered the cycle;
	// on exhaustion events it lists the checks that could not be repaired.
	CheckIDs []string `json:"check_ids,omitempty"`
}

// SendProgress sends an event on the channel if it's not nil.
func SendProgress(ctx context.Context, ch chan<- ProgressEvent, evt ProgressEvent) {
	if ch == nil {
		return
	}
	if evt.Time.IsZero() {
		evt.Time = time.Now().UTC()
	}
	select {
	case ch <- evt:
	case <-ctx.Done():
	}
}
