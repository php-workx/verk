package engine

import (
	"time"

	"verk/internal/state"
)

// ProgressEventType identifies the kind of progress event.
type ProgressEventType int

const (
	EventWaveStarted        ProgressEventType = iota // New wave beginning
	EventWaveCompleted                               // Wave finished (accepted or failed)
	EventTicketPhaseChanged                          // Ticket transitioned to a new phase
	EventTicketDetail                                // Sub-phase activity (worker launch, verify result, etc.)
	EventRunCompleted                                // Entire run finished
)

// ProgressEvent is a structured progress notification from the engine.
// Sent on the Progress channel during RunTicket, RunEpic, and ResumeRun.
type ProgressEvent struct {
	Time     time.Time         `json:"time"`
	Type     ProgressEventType `json:"type"`
	TicketID string            `json:"ticket_id,omitempty"`
	Title    string            `json:"title,omitempty"`
	WaveID   int               `json:"wave_id,omitempty"`
	Phase    state.TicketPhase `json:"phase,omitempty"`
	Detail   string            `json:"detail,omitempty"`
	Closed   int               `json:"closed,omitempty"`
	Total    int               `json:"total,omitempty"`
	Tickets  []string          `json:"tickets,omitempty"`
	Success  bool              `json:"success,omitempty"`
}

// SendProgress sends an event on the channel if it's not nil.
func SendProgress(ch chan<- ProgressEvent, evt ProgressEvent) {
	if ch == nil {
		return
	}
	if evt.Time.IsZero() {
		evt.Time = time.Now().UTC()
	}
	ch <- evt
}
