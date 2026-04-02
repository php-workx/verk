package state

import "fmt"

var allowedTransitions = map[TicketPhase]map[TicketPhase]struct{}{
	TicketPhaseIntake: {
		TicketPhaseImplement: {},
	},
	TicketPhaseImplement: {
		TicketPhaseVerify:  {},
		TicketPhaseBlocked: {},
	},
	TicketPhaseVerify: {
		TicketPhaseReview:    {},
		TicketPhaseImplement: {},
		TicketPhaseBlocked:   {},
	},
	TicketPhaseReview: {
		TicketPhaseCloseout: {},
		TicketPhaseRepair:   {},
		TicketPhaseBlocked:  {},
	},
	TicketPhaseRepair: {
		TicketPhaseVerify:  {},
		TicketPhaseBlocked: {},
	},
	TicketPhaseCloseout: {
		TicketPhaseClosed:  {},
		TicketPhaseRepair:  {},
		TicketPhaseBlocked: {},
	},
}

func ValidateTicketTransition(from, to TicketPhase) error {
	targets, ok := allowedTransitions[from]
	if !ok {
		return fmt.Errorf("ticket phase %q has no outgoing transitions", from)
	}
	if _, ok := targets[to]; !ok {
		return fmt.Errorf("ticket transition %q -> %q is forbidden", from, to)
	}
	return nil
}

func EffectiveReviewThreshold(cli *Severity, ticket *Severity, cfg Severity) Severity {
	if cli != nil {
		return *cli
	}
	if ticket != nil {
		return *ticket
	}
	return cfg
}
